import path from "node:path";

import { randomUUID } from "node:crypto";

import { app, BrowserWindow, ipcMain, shell, type WebContents } from "electron";

import { DaemonClient } from "./client";
import { methods } from "./contract";
import { STREAM_INPUT } from "./wire";
import { ensure } from "./daemon";
import { Coalescer } from "./stream";
import { readUiState, saveUiState, type UiState } from "./uiState";

const channel = process.env.FLT_CHANNEL ?? "stable";

// clientId names this window on every connection it opens. The daemon answers a
// per-window question from it — has this window already read the finished turn —
// so the control connection that polls the fleet and the session connection
// that reports seen must carry the same name.
const clientId = `gui-${randomUUID()}`;

function userData(): string {
  return app.getPath("userData");
}

// A turn can run for minutes, so a prompt call must not inherit the default
// call timeout.
const turnTimeoutMs = 30 * 60 * 1000;

// A push waits on the network and a pull request waits on gh, so they get a
// bound of their own. A commit is local and needs far less.
const writeTimeoutMs = 5 * 60 * 1000;

// links are the session connections this window holds. The daemon is the only
// client a host has, so every window asks the daemon for its own link. The
// coalescer is per link: one session's bytes merge among themselves and never
// wait on another session's flood.
interface Link {
  client: DaemonClient;
  attached: unknown;
  bytes: Coalescer;
}

const links = new Map<string, Link>();

let control: DaemonClient | null = null;
let window: BrowserWindow | null = null;

function send(target: WebContents, event: unknown): void {
  if (!target.isDestroyed()) {
    target.send("flotilla:event", event);
  }
}

async function openLink(sessionId: string, target: WebContents): Promise<unknown> {
  const existing = links.get(sessionId);
  if (existing) {
    return existing.attached;
  }

  // Terminal bytes are merged here, in the main process, before they cross the
  // renderer boundary: one message per stream per turn of the event loop is
  // what keeps a log flood from becoming a frame per PTY read.
  const bytes = new Coalescer((chunk) => {
    send(target, {
      type: "data",
      session: chunk.session,
      stream: chunk.stream,
      bytes: chunk.bytes,
    });
  });

  const client = await ensure({
    channel,
    client: clientId,
    handlers: {
      onNotification: (method, params) => {
        if (method === methods.session.MethodUpdate) {
          send(target, { type: "update", session: sessionId, update: params });
        }
        if (method === methods.session.MethodPermission) {
          send(target, { type: "permission", session: sessionId, question: params });
        }
        // A lifecycle change is what lets the fleet show a session as blocked
        // or done without polling the host for it.
        if (method === methods.session.MethodState) {
          send(target, { type: "state", session: sessionId, state: params });
        }
        // A surface change carries the agent's whole control list, so the
        // renderer replaces its selectors rather than patching one value.
        if (method === methods.session.MethodSurfaceUpdate) {
          const response = (params ?? {}) as { controls?: unknown };
          send(target, { type: "surface", session: sessionId, controls: response.controls });
        }
      },
      onData: (stream, payload) => {
        bytes.push(sessionId, stream, payload);
      },
      onClose: (error) => {
        links.delete(sessionId);
        // Bytes already accepted still reach the renderer: the view they belong
        // to is what the close event is about to end, and dropping them would
        // truncate the screen a user is reading.
        bytes.close();
        send(target, { type: "closed", session: sessionId, error: error?.message });
      },
    },
  });

  let attached: unknown;
  try {
    attached = await client.call(methods.session.MethodAttach, { id: sessionId });
  } catch (error) {
    // A connection that never attached has nothing to carry: closing it here is
    // what keeps a failed click from leaving a pipe open on the daemon.
    client.close();
    throw error;
  }

  links.set(sessionId, { client, attached, bytes });
  return attached;
}

function link(sessionId: string): DaemonClient {
  const held = links.get(sessionId);
  if (!held) {
    throw new Error(
      `session ${sessionId} is not open in this window (open: ${[...links.keys()].join(", ") || "none"})`,
    );
  }
  return held.client;
}

// dropLink closes one session's connection and releases its queue. The bytes it
// was still holding go out first: a view that is being torn down may still be
// painting what already arrived.
function dropLink(sessionId: string): void {
  const held = links.get(sessionId);
  if (held === undefined) {
    return;
  }
  links.delete(sessionId);
  held.bytes.close();
  held.client.close();
}

function register(): void {
  ipcMain.handle("flotilla:fleet", async () => {
    // One control connection is reused: the fleet is polled, and a handshake per
    // poll would be noise on the daemon's accept loop.
    if (!control || !control.alive) {
      control = await ensure({ channel, client: clientId });
    }
    try {
      return await control.call(methods.daemon.MethodStatus);
    } catch (error) {
      control?.close();
      control = null;
      throw error;
    }
  });

  // The agent catalogue is the daemon's answer, so the renderer never guesses
  // what is installed or which agent a switch turned off.
  ipcMain.handle("flotilla:agents", async () => {
    if (!control || !control.alive) {
      control = await ensure({ channel, client: clientId });
    }
    try {
      return await control.call(methods.daemon.MethodAgents);
    } catch (error) {
      // control is shared with the fleet poll, start and stop. A daemon that
      // speaks the wire but not this method (one from before an upgrade) answers
      // an error without dropping the pipe, so only a dead connection is
      // discarded here; anything else is reported to the renderer as is.
      if (!control.alive) {
        control = null;
      }
      throw error;
    }
  });

  ipcMain.handle("flotilla:agent:set", async (_event, request: unknown) => {
    if (!control || !control.alive) {
      control = await ensure({ channel, client: clientId });
    }
    return control.call(methods.daemon.MethodAgentSet, request);
  });

  ipcMain.handle("flotilla:start", async (_event, request: unknown) => {
    if (!control || !control.alive) {
      control = await ensure({ channel, client: clientId });
    }
    // Starting a session spawns a host, which spawns an agent: give it room.
    return control.call(methods.daemon.MethodSessionStart, request, 60000);
  });

  ipcMain.handle("flotilla:stop", async (_event, sessionId: string) => {
    if (!control || !control.alive) {
      control = await ensure({ channel, client: clientId });
    }
    // The window may hold a link to this session; stopping ends it too, so the
    // link is dropped here rather than left pointing at a closed pipe.
    dropLink(sessionId);
    return control.call(methods.session.MethodStop, { id: sessionId }, 30000);
  });

  ipcMain.handle("flotilla:detach", async (_event, sessionId: string) => {
    // Closing the link is what lets the host know nobody is watching: a PTY
    // session then keeps its recent output for the next visit instead of
    // streaming it to a view that no longer exists. The session itself keeps
    // running, which is the whole point of the two-process model.
    dropLink(sessionId);
  });

  ipcMain.handle("flotilla:open", async (event, sessionId: string) =>
    openLink(sessionId, event.sender),
  );

  ipcMain.handle("flotilla:prompt", async (_event, sessionId: string, text: string) =>
    link(sessionId).call(methods.session.MethodPrompt, { text }, turnTimeoutMs),
  );

  ipcMain.handle("flotilla:seen", async (_event, sessionId: string) => {
    // Looking at a session is what turns done into idle, and it belongs to the
    // window that looked: the daemon records it against this window's client
    // name, so another window's badge stays up. The call travels on the
    // session's own link because that is the connection bound to the session.
    await link(sessionId).call(methods.session.MethodSeen, {});
  });

  ipcMain.handle("flotilla:cancel", async (_event, sessionId: string) => {
    await link(sessionId).call(methods.session.MethodCancel, {});
  });

  ipcMain.handle(
    "flotilla:surface:set",
    async (_event, sessionId: string, id: string, value: string) =>
      link(sessionId).call(methods.session.MethodSurfaceSet, { id, value }),
  );

  ipcMain.handle(
    "flotilla:answer",
    async (_event, sessionId: string, questionId: string, optionId: string) => {
      await link(sessionId).call(methods.session.MethodPermissionReply, {
        id: questionId,
        option_id: optionId,
      });
    },
  );

  ipcMain.handle("flotilla:input", async (_event, sessionId: string, text: string) => {
    link(sessionId).sendData(STREAM_INPUT, Buffer.from(text, "utf8"));
  });

  ipcMain.handle(
    "flotilla:diff",
    async (_event, sessionId: string, scope: string, path?: string) =>
      // git can be slow on a large repository; this is a read, not a turn.
      link(sessionId).call(methods.session.MethodDiff, { scope, path }, 30000),
  );

  ipcMain.handle("flotilla:comments", async (_event, sessionId: string) =>
    link(sessionId).call(methods.session.MethodCommentList, {}),
  );

  // The three writes a review ends with. They are long calls: a push waits on
  // the network, and a pull request waits on gh. A commit is local and fast.
  ipcMain.handle(
    "flotilla:commit",
    async (_event, sessionId: string, message: string) =>
      link(sessionId).call(methods.session.MethodCommit, { message }, 60000),
  );

  ipcMain.handle("flotilla:push", async (_event, sessionId: string) =>
    link(sessionId).call(methods.session.MethodPush, {}, writeTimeoutMs),
  );

  ipcMain.handle(
    "flotilla:pr",
    async (_event, sessionId: string, request: unknown) =>
      link(sessionId).call(methods.session.MethodPullRequest, request, writeTimeoutMs),
  );

  ipcMain.handle(
    "flotilla:comment:add",
    async (_event, sessionId: string, comment: unknown) =>
      link(sessionId).call(methods.session.MethodCommentAdd, comment),
  );

  ipcMain.handle(
    "flotilla:comment:remove",
    async (_event, sessionId: string, id: string) =>
      link(sessionId).call(methods.session.MethodCommentRemove, { id }),
  );

  ipcMain.handle(
    "flotilla:resize",
    async (_event, sessionId: string, cols: number, rows: number) => {
      await link(sessionId).call(methods.session.MethodResize, { cols, rows });
    },
  );

  // Layout is the renderer's to name and the main process's to store: the
  // renderer has no filesystem, and the file has to survive the window.
  ipcMain.handle("flotilla:layout:read", async () => readUiState(userData()));
  ipcMain.handle("flotilla:layout:write", async (_event, patch: UiState) => {
    saveUiState(userData(), patch);
  });
}

function createWindow(): void {
  const saved = readUiState(userData());
  const width = saved.window?.width ?? 1180;
  const height = saved.window?.height ?? 780;

  window = new BrowserWindow({
    width,
    height,
    backgroundColor: "#14161a",
    show: false,
    webPreferences: {
      preload: path.join(__dirname, "preload.js"),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
    },
  });

  // Size is remembered on close rather than on every resize event: a drag emits
  // hundreds of them, and only the size the user settled on matters.
  window.on("close", () => {
    const bounds = window?.getNormalBounds();
    if (bounds) {
      saveUiState(userData(), { window: { width: bounds.width, height: bounds.height } });
    }
  });

  // The renderer is a local page with a known set of destinations. A link that
  // leaves it — a pull request URL rendered in the diff panel — belongs to the
  // user's browser, and anything else is a navigation this window does not
  // serve: letting either through would turn one click into a second window
  // with its own privileges.
  window.webContents.setWindowOpenHandler(({ url }) => {
    if (/^https?:\/\//.test(url)) {
      void shell.openExternal(url);
    }
    return { action: "deny" };
  });
  window.webContents.on("will-navigate", (event, url) => {
    if (url === window?.webContents.getURL()) {
      return;
    }
    event.preventDefault();
    if (/^https?:\/\//.test(url)) {
      void shell.openExternal(url);
    }
  });

  window.once("ready-to-show", () => window?.show());
  void window.loadFile(path.join(__dirname, "..", "renderer", "index.html"));

  // A smoke run renders offscreen and asserts on the DOM, so the renderer can be
  // checked without a window on screen.
  if (process.env.FLT_GUI_SMOKE) {
    window.webContents.once("did-finish-load", () => {
      void import("./smoke.js").then(async (smoke) => {
        const ok = await smoke.run(window!);
        app.exit(ok ? 0 : 1);
      });
    });
  }
  window.on("closed", () => {
    window = null;
    // The sessions keep running in their hosts; this window's connections do
    // not outlive it. Anything still queued goes out first: a window that is
    // closing has a last frame worth painting.
    for (const [sessionId] of [...links]) {
      dropLink(sessionId);
    }
  });
}

app.whenReady().then(() => {
  register();
  createWindow();

  app.on("activate", () => {
    if (BrowserWindow.getAllWindows().length === 0) {
      createWindow();
    }
  });
});

app.on("window-all-closed", () => {
  // The daemon and the hosts keep running: a closed window is not a stopped
  // session.
  if (process.platform !== "darwin") {
    app.quit();
  }
});
