import { WebglAddon } from "@xterm/addon-webgl";
import type { Terminal as XTerm } from "@xterm/xterm";

// attachGpuRenderer loads the WebGL renderer and reports which one is live:
// xterm falls back to its DOM renderer when the addon is disposed, so a lost
// context or a machine without WebGL keeps a working terminal, not a blank one.
// The terminal must already be open: the addon binds its canvas on activate.
export function attachGpuRenderer(term: XTerm, report: (name: string) => void): void {
  try {
    const addon = new WebglAddon();
    addon.onContextLoss(() => {
      addon.dispose();
      report("dom");
    });
    term.loadAddon(addon);
    report("webgl");
  } catch {
    report("dom");
  }
}