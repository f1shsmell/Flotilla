import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Terminal as XTerm } from "@xterm/xterm";

const hoisted = vi.hoisted(() => ({
  addons: [] as { handler?: () => void; disposed: boolean }[],
}));

vi.mock("@xterm/addon-webgl", () => ({
  WebglAddon: class {
    handler?: () => void;
    disposed = false;
    constructor() {
      hoisted.addons.push(this);
    }
    onContextLoss(handler: () => void): void {
      this.handler = handler;
    }
    dispose(): void {
      this.disposed = true;
    }
  },
}));

import { attachGpuRenderer } from "../src/components/renderer";

describe("attachGpuRenderer", () => {
  beforeEach(() => {
    hoisted.addons.length = 0;
  });

  it("loads the addon and reports webgl", () => {
    // The GPU renderer is the point of the walk: a terminal that silently
    // stayed on DOM would pass every other check.
    const loadAddon = vi.fn();
    const reports: string[] = [];
    attachGpuRenderer({ loadAddon } as unknown as XTerm, (name) => reports.push(name));
    expect(loadAddon).toHaveBeenCalledTimes(1);
    expect(reports).toEqual(["webgl"]);
  });

  it("reports dom when the addon cannot load", () => {
    // A machine without WebGL must keep a working terminal, not a blank one.
    const loadAddon = vi.fn(() => {
      throw new Error("no webgl context");
    });
    const reports: string[] = [];
    attachGpuRenderer({ loadAddon } as unknown as XTerm, (name) => reports.push(name));
    expect(reports).toEqual(["dom"]);
  });

  it("disposes the addon and reports dom on a lost context", () => {
    // xterm only falls back to DOM once the addon is disposed, so the loss
    // handler must dispose it, not just report the change.
    const reports: string[] = [];
    attachGpuRenderer({ loadAddon: vi.fn() } as unknown as XTerm, (name) =>
      reports.push(name),
    );
    hoisted.addons[0].handler?.();
    expect(hoisted.addons[0].disposed).toBe(true);
    expect(reports).toEqual(["webgl", "dom"]);
  });
});