// The tray's visible surface: labels, the menu's shape, and what a window
// close means. The OS renders these where no DOM test can reach, so every
// decision is data a test reads. The product label mirrors the packaging
// identity (scripts/packaging-config.mjs) by hand and is pinned to it by
// gui/test/tray.test.ts, the same arrangement home.ts has with internal/home.

export interface MenuItemSpec {
  label: string;
  action: "open" | "quit";
}

export function productLabel(channel: string): string {
  return channel === "preview" ? "Flotilla Preview" : "Flotilla";
}

export function trayMenuSpec(channel: string): MenuItemSpec[] {
  const product = productLabel(channel);
  return [
    { label: `Open ${product}`, action: "open" },
    { label: `Quit ${product}`, action: "quit" },
  ];
}

// A user's click on X ends the window, not the fleet: the sessions keep
// running in their hosts and the daemon keeps running detached. Only a quit
// the app itself initiated may actually close the window.
export function shouldHideOnClose(quitting: boolean): boolean {
  return !quitting;
}
