import path from "node:path";
import { writeFileSync } from "node:fs";

import { randomUUID } from "node:crypto";

import { app, BrowserWindow, ipcMain, Menu, nativeImage, shell, Tray, type WebContents } from "electron";

import { DaemonClient } from "./client";
import { resolveChannel } from "./channel";
import { constants, methods } from "./contract";
import {
  AUTOSTART_ARG,
  autostartAvailable,
  autostartRequest,
  launchedAtLogin,
} from "./autostart";
import { STREAM_INPUT, RpcError } from "./wire";
import { ensure } from "./daemon";
import { reconnect, type ReconnectTask } from "./reconnect";
import { Coalescer } from "./stream";
import { readUiState, saveUiState, type UiState } from "./uiState";
import { productLabel, shouldHideOnClose, trayClickAction, trayMenuSpec } from "./tray";
import { TRAY_ICON, TRAY_ICON_2X } from "./tray-icon";
import { windowSetup } from "./window";

// An installed copy launches without env (double-click, login item), so the
// builder bakes the channel into the packaged manifest; dev and smoke runs
// name their own through FLT_CHANNEL.
function manifestChannel(): string | null {
  if (!app.isPackaged) {
    return null;
  }
  try {
    const meta = require(path.join(app.getAppPath(), "package.json")) as { fltChannel?: string };
    return typeof meta.fltChannel === "string" ? meta.fltChannel : null;
  } catch {
    return null;
  }
}

const channel = resolveChannel(process.env.FLT_CHANNEL, app.isPackaged, manifestChannel());

// A login launch starts quietly: tray only, daemon warmed, no window. The
// tray is the way in.
const hiddenStart = launchedAtLogin(process.argv.slice(1));

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

// healing is the retry loop per session whose link died on its own. opening
// dedupes a dial: a user click and a healing attempt naming one session must
// share one connection, not race two onto the daemon. wanted separates the two
// callers: a heal whose dial outlived its cancel must not discard a link the
// user reopened through the same shared dial.
const healing = new Map<string, ReconnectTask>();
const opening = new Map<string, Promise<unknown>>();
const wanted = new Set<string>();

let control: DaemonClient | null = null;
let window: BrowserWindow | null = null;
// Held for the app's lifetime: an unreferenced Tray object can be collected,
// and its icon disappears from the notification area with it.
let tray: Tray | null = null;
// quitting separates the window's close button from the app's own exit: the
// close button hides to the tray, an app.quit() must really close.
let quitting = false;

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
  const pending = opening.get(sessionId);
  if (pending) {
    return pending;
  }
  const attempt = (async () => {
    try {
      return await dial(sessionId, target);
    } finally {
      opening.delete(sessionId);
    }
  })();
  opening.set(sessionId, attempt);
  return attempt;
}

async function dial(sessionId: string, target: WebContents): Promise<unknown> {
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
        // close() on purpose never reaches here — DaemonClient.fail guards on
        // closed — so this is always a death the window did not ask for.
        heal(sessionId, target);
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

  // A window that closed mid-dial owns nothing: keeping the link would leave a
  // pipe streaming to a view that no longer exists (macOS outlives its windows).
  if (target.isDestroyed()) {
    client.close();
    throw new Error(`window closed while attaching session ${sessionId}`);
  }

  links.set(sessionId, { client, attached, bytes });
  return attached;
}

// heal retries a link that died on its own. ensure() inside the dial respawns
// the daemon when nobody is listening, so a daemon restart heals here too. An
// in-flight prompt is never resent: its call already rejected when the pipe
// died, and sending it again could run the turn twice.
function heal(sessionId: string, target: WebContents): void {
  if (healing.has(sessionId) || target.isDestroyed()) {
    return;
  }
  const task = reconnect({
    attempt: async () => {
      try {
        return { status: "attached", attached: await openLink(sessionId, target) };
      } catch (error) {
        // A session the daemon no longer serves is gone for good; retrying
        // would poll a name nothing will ever answer to again.
        if (
          error instanceof RpcError &&
          (error.is(Number(constants.ipc.CodeUnknownSession)) ||
            error.is(Number(constants.ipc.CodeHostUnreachable)))
        ) {
          return { status: "gone" };
        }
        throw error;
      }
    },
    onAttached: (attached) => {
      if (healing.get(sessionId) !== task) {
        // Superseded or cancelled mid-dial: a newer open shares this dial and
        // renders its own answer, so only an unwanted link is dropped here.
        if (!wanted.has(sessionId)) {
          dropLink(sessionId);
        }
        return;
      }
      healing.delete(sessionId);
      send(target, { type: "reattached", session: sessionId, attached });
    },
    onGone: () => {
      if (healing.get(sessionId) === task) {
        healing.delete(sessionId);
      }
    },
  });
  healing.set(sessionId, task);
  // The loop reports its own failures through the callbacks; a throw escaping
  // them is a bug worth a line, not an unhandled rejection in the main process.
  void task.done.catch((error) => console.error(`heal for ${sessionId} failed`, error));
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
// painting what already arrived. A heal in progress is cancelled with it: the
// window asked for silence, not a reconnection.
function dropLink(sessionId: string): void {
  wanted.delete(sessionId);
  const retry = healing.get(sessionId);
  if (retry) {
    healing.delete(sessionId);
    retry.cancel();
  }
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

  ipcMain.handle("flotilla:open", async (event, sessionId: string) => {
    wanted.add(sessionId);
    try {
      return await openLink(sessionId, event.sender);
    } catch (error) {
      // A click that could not be honoured wants nothing: the renderer clears
      // its selection, so the heal it may have started goes too.
      wanted.delete(sessionId);
      throw error;
    } finally {
      // The click supersedes any heal: a retry waking later must not rebuild
      // the view from a snapshot older than what this open just rendered.
      const retry = healing.get(sessionId);
      if (retry) {
        healing.delete(sessionId);
        retry.cancel();
      }
    }
  });

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

  window = new BrowserWindow(
    windowSetup(__dirname, { width, height, smoke: Boolean(process.env.FLT_GUI_SMOKE) }),
  );

  // Size is remembered on close rather than on every resize event: a drag emits
  // hundreds of them, and only the size the user settled on matters.
  window.on("close", (event) => {
    const bounds = window?.getNormalBounds();
    if (bounds) {
      saveUiState(userData(), { window: { width: bounds.width, height: bounds.height } });
    }
    // The tray owns the way back: a hidden window keeps its links streaming,
    // so reopening loses nothing, and the app keeps watching the daemon.
    if (shouldHideOnClose(quitting)) {
      event.preventDefault();
      window!.hide();
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
    // FLT_GUI_SMOKE_TRAY_MENU="x,y" pops the real context menu at that screen
    // point for the evidence capture: the OS draws the menu, so no DOM or
    // offscreen assert can reach it. The caller screenshots it and sends
    // Escape; a 20s guard closes the process if the menu never went away.
    const trayMenuAt = process.env.FLT_GUI_SMOKE_TRAY_MENU;
    if (trayMenuAt) {
      const [x, y] = trayMenuAt.split(",").map((value) => Number(value));
      if (!Number.isFinite(x) || !Number.isFinite(y)) {
        console.error("smoke: FLT_GUI_SMOKE_TRAY_MENU must read x,y");
        app.exit(1);
        return;
      }
      setTimeout(() => {
        tray?.popUpContextMenu(trayMenu(), { x, y });
        console.log("smoke: tray menu popped");
        setTimeout(() => app.exit(0), 6000);
      }, 1500);
      return;
    }
    window.webContents.once("did-finish-load", () => {
      void import("./smoke.js").then(async (smoke) => {
        const ok = await smoke.run(window!);
        // FLT_GUI_SMOKE_AUTOSTART exercises the login item against the real
        // registry: "roundtrip" enables, verifies and disables; "enable"
        // leaves it on for the caller's reg query, and owes a cleanup run.
        const mode = process.env.FLT_GUI_SMOKE_AUTOSTART;
        const autostartOk = mode === undefined || autostartWalk(mode === "enable");
        // FLT_GUI_SMOKE_SHOT saves the window as it ended: the UI review reads
        // a real render, not a description of one.
        const shot = process.env.FLT_GUI_SMOKE_SHOT;
        if (shot && window) {
          try {
            await new Promise((resolve) => setTimeout(resolve, 400));
            const image = await window.webContents.capturePage();
            writeFileSync(shot, image.toPNG());
            console.log(`smoke: shot ${shot}`);
          } catch (cause) {
            // A failed shot must end the run: an unhandled rejection here would
            // leave the process alive until the CI deadline.
            console.error(`smoke: shot ${shot} failed:`, cause);
            app.exit(1);
            return;
          }
        }
        app.exit(ok && autostartOk ? 0 : 1);
      });
    });
  }
  window.on("closed", () => {
    window = null;
    // The sessions keep running in their hosts; this window's connections and
    // heals do not outlive it. Anything still queued goes out first: a window
    // that is closing has a last frame worth painting.
    for (const sessionId of new Set([...links.keys(), ...healing.keys()])) {
      dropLink(sessionId);
    }
  });
}

// trayIcon builds the notification-area image from the generated brand-mark
// PNGs. nativeImage does not read the installer's .ico, and the 2x layer keeps
// the mark from blurring on scaled displays.
function trayIcon(): Electron.NativeImage {
  const image = nativeImage.createFromBuffer(Buffer.from(TRAY_ICON, "base64"));
  image.addRepresentation({ scaleFactor: 2, buffer: Buffer.from(TRAY_ICON_2X, "base64") });
  return image;
}

function showWindow(): void {
  if (!window || window.isDestroyed()) {
    createWindow();
    return;
  }
  if (window.isMinimized()) {
    window.restore();
  }
  window.show();
  window.focus();
}

// The OS registry is the login item's state: read it, never mirror it.
function readOpenAtLogin(): boolean {
  return app
    .getLoginItemSettings({ path: process.execPath, args: [AUTOSTART_ARG] })
    .openAtLogin;
}

// The login item's real write/read round trip. A dev run writes Electron's
// default item name and the walk always ends disabled; a packaged run proves
// the channel-named entry a login item would actually launch.
function autostartWalk(leaveEnabled: boolean): boolean {
  app.setLoginItemSettings(autostartRequest(process.execPath, true));
  const on = readOpenAtLogin();
  console.log(`smoke: autostart enabled -> ${on}`);
  if (leaveEnabled) {
    return on;
  }
  app.setLoginItemSettings(autostartRequest(process.execPath, false));
  const off = readOpenAtLogin();
  console.log(`smoke: autostart disabled -> ${off}`);
  return on && !off;
}

// The menu is rebuilt from the registry after a toggle, so the checkbox reads
// the OS state rather than trusting the click.
function trayMenu(): Electron.Menu {
  return Menu.buildFromTemplate(
    trayMenuSpec(channel, {
      autostartAvailable: autostartAvailable(app.isPackaged),
      openAtLogin: readOpenAtLogin(),
    }).map((item) => ({
      label: item.label,
      type: item.action === "autostart" ? ("checkbox" as const) : ("normal" as const),
      checked: item.checked,
      click: () => {
        if (item.action === "open") {
          showWindow();
        }
        if (item.action === "quit") {
          app.quit();
        }
        if (item.action === "autostart") {
          app.setLoginItemSettings(autostartRequest(process.execPath, !readOpenAtLogin()));
          tray?.setContextMenu(trayMenu());
        }
      },
    })),
  );
}

function installTray(): void {
  tray = new Tray(trayIcon());
  tray.setToolTip(productLabel(channel));
  // Both gestures route through trayClickAction: no context menu is bound
  // outright, because that would turn a left click into a two-click restore.
  // The menu is built on demand, so its checkbox always reads the registry.
  const gesture = (button: "left" | "right"): void => {
    if (trayClickAction(button) === "show") {
      showWindow();
    } else {
      tray?.popUpContextMenu(trayMenu());
    }
  };
  tray.on("click", () => gesture("left"));
  tray.on("right-click", () => gesture("right"));
}

app.whenReady().then(() => {
  // A hidden start creates no window, and every smoke walk drives one: the
  // combination would wait out the deadline silently, so it refuses here.
  if (process.env.FLT_GUI_SMOKE && hiddenStart) {
    console.error("smoke: a hidden start has no window to drive");
    app.exit(1);
    return;
  }
  register();
  installTray();
  if (!hiddenStart) {
    createWindow();
  } else {
    // A login launch exists to have the fleet ready: start the daemon now so
    // the first window opens onto a live control plane. Failure is logged,
    // not fatal — the fleet poll respawns the daemon when a window comes.
    ensure({ channel, client: clientId })
      .then((client) => {
        control = client;
        console.log("autostart: daemon ready");
      })
      .catch((error) => console.error("autostart: daemon warm failed:", error));
  }

  app.on("activate", () => {
    if (BrowserWindow.getAllWindows().length === 0) {
      createWindow();
    } else {
      showWindow();
    }
  });
});

app.on("before-quit", () => {
  quitting = true;
});

app.on("window-all-closed", () => {
  // The daemon and the hosts keep running: a closed window is not a stopped
  // session.
  if (process.platform !== "darwin") {
    app.quit();
  }
});
