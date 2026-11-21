// The Electron security baseline, as gates instead of a checklist. Three
// boundaries hold the window: the switches the BrowserWindow is built with, the
// preload surface the renderer can reach, and the fact that neither the pipe
// token nor a Node module is reachable from the renderer's own source.

import fs from "node:fs";
import path from "node:path";

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const hoisted = vi.hoisted(() => ({
  exposed: null as Record<string, unknown> | null,
}));

vi.mock("electron", () => ({
  contextBridge: {
    exposeInMainWorld: (_key: string, api: Record<string, unknown>) => {
      hoisted.exposed = api;
    },
  },
  ipcRenderer: {
    invoke: vi.fn(async () => undefined),
    on: vi.fn(),
    removeListener: vi.fn(),
  },
}));

import { windowSetup } from "../electron/window";

const dirname = path.join(path.sep, "app", "dist", "electron");

function setup(smoke = false) {
  return windowSetup(dirname, { width: 1180, height: 780, smoke });
}

describe("the window's web preferences", () => {
  it("isolates the context, denies Node and keeps the renderer sandboxed", () => {
    // Any one of these off is a renderer that can reach the main process's
    // token or the filesystem; the three are asserted together because they
    // only close the boundary as a set.
    const { webPreferences } = setup();
    expect(webPreferences.contextIsolation).toBe(true);
    expect(webPreferences.nodeIntegration).toBe(false);
    expect(webPreferences.sandbox).toBe(true);
  });

  it("loads the preload bridge from the built main process", () => {
    // A preload path that does not resolve to preload.js leaves the renderer
    // with no calls at all, which sends someone looking for a UI bug.
    expect(setup().webPreferences.preload).toBe(path.join(dirname, "preload.js"));
  });

  it("renders offscreen without throttling only for a smoke run", () => {
    // A smoke run is steered by nobody, so a covered window would freeze its
    // own log. A desktop window keeps the OS default.
    expect(setup(true).x).toBe(-32000);
    expect(setup(true).webPreferences.backgroundThrottling).toBe(false);
    expect(setup(false).x).toBeUndefined();
    expect(setup(false).webPreferences.backgroundThrottling).toBe(true);
  });

  it("does not open the window before its first frame is ready", () => {
    // show:false is what keeps a flash of unstyled content off the desktop.
    expect(setup().show).toBe(false);
  });
});

describe("the preload surface", () => {
  beforeEach(() => {
    hoisted.exposed = null;
    vi.resetModules();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("exposes the bridge and nothing that names the token", async () => {
    await import("../electron/preload");

    const api = hoisted.exposed;
    expect(api, "preload never called exposeInMainWorld").not.toBeNull();

    const names = Object.keys(api!);
    expect(names).toContain("fleet");
    expect(names.every((name) => !/token/i.test(name))).toBe(true);
    // The renderer must not be able to ask for the token file or a socket: every
    // call is a named channel, not a general request.
    expect(names.every((name) => !/readFile|require|socket|connect/i.test(name))).toBe(true);
  });

  it("routes every call through a named IPC channel", async () => {
    await import("../electron/preload");
    const api = hoisted.exposed!;

    const { ipcRenderer } = (await import("electron")) as unknown as {
      ipcRenderer: { invoke: (channel: string) => Promise<unknown> };
    };

    for (const name of Object.keys(api)) {
      const value = api[name];
      if (typeof value !== "function" || name === "onEvent") {
        continue;
      }
      await (value as (request: unknown) => Promise<unknown>)({ id: "sess" });
      const channel = vi.mocked(ipcRenderer.invoke).mock.calls.at(-1)?.[0];
      expect(channel, `${name} called without a channel`).toMatch(/^flotilla:/);
    }
  });
});

describe("the renderer's source", () => {
  const root = path.join(__dirname, "..", "src");

  function sources(dir: string): string[] {
    return fs.readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) {
        return sources(full);
      }
      return /\.(ts|tsx)$/.test(entry.name) ? [full] : [];
    });
  }

  it("imports no Node module and touches no IPC object", () => {
    // The renderer is a page, not a process: a node: import or an ipcRenderer
    // here means the bridge was bypassed and the token boundary is gone.
    const offenders = sources(root).flatMap((file) => {
      const text = fs.readFileSync(file, "utf8");
      const named = /from\s+["']node:|require\(|ipcRenderer|contextBridge/.test(text);
      return named ? [path.relative(root, file)] : [];
    });
    expect(offenders, "renderer sources reaching outside the bridge").toEqual([]);
  });
});
