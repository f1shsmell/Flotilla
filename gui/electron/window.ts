// The window's own settings, kept out of main.ts so the security baseline is a
// value a test reads rather than a screen a human ticks off. The renderer is
// isolated, has no Node, and reaches the daemon only through the preload
// bridge; the pipe token never crosses into it.

import path from "node:path";

export interface Placement {
  width: number;
  height: number;
  // smoke renders offscreen and drives the DOM, so it must keep painting while
  // no user is looking at it.
  smoke: boolean;
}

export interface Setup {
  width: number;
  height: number;
  // minWidth and minHeight keep the window from being dragged into a size the
  // layout cannot serve: two columns and a composer need room, and a window
  // narrower than the floor would hide controls rather than reflow them.
  minWidth: number;
  minHeight: number;
  backgroundColor: string;
  show: boolean;
  x?: number;
  y?: number;
  webPreferences: {
    preload: string;
    contextIsolation: true;
    nodeIntegration: false;
    sandbox: true;
    backgroundThrottling: boolean;
  };
}

// windowSetup is the whole BrowserWindow argument: one definition for the
// security switches, so a later window cannot be built without them.
export function windowSetup(dirname: string, at: Placement): Setup {
  return {
    width: at.width,
    height: at.height,
    // The floor is where the two-column layout stops working: below it the
    // session pane would be narrower than a terminal's minimum. The window
    // refuses the size instead of the layout hiding a control.
    minWidth: 720,
    minHeight: 480,
    backgroundColor: "#f6f4ee",
    show: false,
    // A desktop window opens where the user left it; a smoke window offscreen,
    // so a click on the desktop cannot steer a run that is meant to be evidence.
    ...(at.smoke ? { x: -32000, y: -32000 } : {}),
    webPreferences: {
      preload: path.join(dirname, "preload.js"),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
      // A covered window throttles its frames, which freezes a streamed log
      // mid-turn. A real window catches up when uncovered; a smoke run has
      // nobody to uncover it.
      backgroundThrottling: !at.smoke,
    },
  };
}
