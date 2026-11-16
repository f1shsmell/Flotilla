import path from "node:path";

import { app, BrowserWindow, ipcMain, type WebContents } from "electron";

import { DaemonClient } from "./client";
import { STREAM_INPUT } from "./wire";
import { ensure } from "./daemon";
import { readUiState, saveUiState, type UiState } from "./uiState";

const channel = process.env.FLT_CHANNEL ?? "stable";

function userData(): string {
  return app.getPath("userData");
}

// A turn can run for minutes, so a prompt call must not inherit the default
// call timeout.
const turnTimeoutMs = 30 * 60 * 1000;

// links are the session connections this window holds. The daemon is the only
// client a host has, so every window asks the daemon for its own link.
const links = new Map<string, { client: DaemonClient; attached: unknown }>();

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

  const client = await ensure({
    channel,
    handlers: {
      onNotification: (method, params) => {
        if (method === "session.update") {
          send(target, { type: "update", session: sessionId, update: params });
        }
        if (method === "session.permission") {
          send(target, { type: "permission", session: sessionId, question: params });
        }
        // A lifecycle change is what lets the fleet show a session as blocked
        // or done without polling the host for it.
        if (method === "session.state") {
          send(target, { type: "state", session: sessionId, state: params });
        }
      },
      onData: (stream, payload) => {
        send(target, {
          type: "data",
          session: sessionId,
          stream,
          base64: payload.toString("base64"),
        });
      },
      onClose: (error) => {
        links.delete(sessionId);
        send(target, { type: "closed", session: sessionId, error: error?.message });
      },
    },
  });

  let attached: unknown;
  try {
    attached = await client.call("session.attach", { id: sessionId });
  } catch (error) {
    // A connection that never attached has nothing to carry: closing it here is
    // what keeps a failed click from leaving a pipe open on the daemon.
    client.close();
    throw error;
  }

  links.set(sessionId, { client, attached });
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

function register(): void {
  ipcMain.handle("flotilla:fleet", async () => {
    // One control connection is reused: the fleet is polled, and a handshake per
    // poll would be noise on the daemon's accept loop.
    if (!control || !control.alive) {
      control = await ensure({ channel });
    }
    try {
      return await control.call("daemon.status");
    } catch (error) {
      control?.close();
      control = null;
      throw error;
    }
  });

  ipcMain.handle("flotilla:start", async (_event, request: unknown) => {
    if (!control || !control.alive) {
      control = await ensure({ channel });
    }
    // Starting a session spawns a host, which spawns an agent: give it room.
    return control.call("session.start", request, 60000);
  });

  ipcMain.handle("flotilla:stop", async (_event, sessionId: string) => {
    if (!control || !control.alive) {
      control = await ensure({ channel });
    }
    // The window may hold a link to this session; stopping ends it too, so the
    // link is dropped here rather than left pointing at a closed pipe.
    links.get(sessionId)?.client.close();
    links.delete(sessionId);
    return control.call("session.stop", { id: sessionId }, 30000);
  });

  ipcMain.handle("flotilla:detach", async (_event, sessionId: string) => {
    // Closing the link is what lets the host know nobody is watching: a PTY
    // session then keeps its recent output for the next visit instead of
    // streaming it to a view that no longer exists. The session itself keeps
    // running, which is the whole point of the two-process model.
    links.get(sessionId)?.client.close();
    links.delete(sessionId);
  });

  ipcMain.handle("flotilla:open", async (event, sessionId: string) =>
    openLink(sessionId, event.sender),
  );

  ipcMain.handle("flotilla:prompt", async (_event, sessionId: string, text: string) =>
    link(sessionId).call("session.prompt", { text }, turnTimeoutMs),
  );

  ipcMain.handle("flotilla:seen", async (_event, sessionId: string) => {
    // Looking at a session is what turns done into idle. It goes to the host
    // through the session's own link, because seen state belongs to the
    // session, not to the control plane.
    await link(sessionId).call("session.seen", {});
  });

  ipcMain.handle("flotilla:cancel", async (_event, sessionId: string) => {
    await link(sessionId).call("session.cancel", {});
  });

  ipcMain.handle(
    "flotilla:answer",
    async (_event, sessionId: string, questionId: string, optionId: string) => {
      await link(sessionId).call("session.permission.reply", {
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
      link(sessionId).call("session.diff", { scope, path }, 30000),
  );

  ipcMain.handle("flotilla:comments", async (_event, sessionId: string) =>
    link(sessionId).call("session.comment.list", {}),
  );

  ipcMain.handle(
    "flotilla:comment:add",
    async (_event, sessionId: string, comment: unknown) =>
      link(sessionId).call("session.comment.add", comment),
  );

  ipcMain.handle(
    "flotilla:comment:remove",
    async (_event, sessionId: string, id: string) =>
      link(sessionId).call("session.comment.remove", { id }),
  );

  ipcMain.handle(
    "flotilla:resize",
    async (_event, sessionId: string, cols: number, rows: number) => {
      await link(sessionId).call("session.resize", { cols, rows });
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
    // not outlive it.
    for (const [, held] of links) {
      held.client.close();
    }
    links.clear();
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
