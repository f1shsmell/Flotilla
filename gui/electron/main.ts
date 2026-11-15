import path from "node:path";

import { app, BrowserWindow, ipcMain, type WebContents } from "electron";

import { DaemonClient } from "./client";
import { ensure } from "./daemon";

const channel = process.env.FLT_CHANNEL ?? "stable";

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

  const attached = await client.call<{ id: string; info: unknown }>("session.attach", {
    id: sessionId,
  });
  links.set(sessionId, { client, attached });
  return attached;
}

function link(sessionId: string): DaemonClient {
  const held = links.get(sessionId);
  if (!held) {
    throw new Error(`session ${sessionId} is not open in this window`);
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

  ipcMain.handle("flotilla:open", async (event, sessionId: string) =>
    openLink(sessionId, event.sender),
  );

  ipcMain.handle("flotilla:prompt", async (_event, sessionId: string, text: string) =>
    link(sessionId).call("session.prompt", { text }, turnTimeoutMs),
  );

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
    link(sessionId).sendData(2, Buffer.from(text, "utf8"));
  });

  ipcMain.handle(
    "flotilla:resize",
    async (_event, sessionId: string, cols: number, rows: number) => {
      await link(sessionId).call("session.resize", { cols, rows });
    },
  );
}

function createWindow(): void {
  window = new BrowserWindow({
    width: 1180,
    height: 780,
    backgroundColor: "#14161a",
    show: false,
    webPreferences: {
      preload: path.join(__dirname, "preload.js"),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
    },
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
