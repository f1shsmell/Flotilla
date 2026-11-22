// The tray's visible surface: labels, the menu's shape, and what a window
// close means. The OS renders these where no DOM test can reach, so every
// decision is data a test reads. The product label mirrors the packaging
// identity (scripts/packaging-config.mjs) by hand and is pinned to it by
// gui/test/tray.test.ts, the same arrangement home.ts has with internal/home.

export interface MenuItemSpec {
  label: string;
  action: "open" | "quit" | "autostart";
  // The login item is a checkbox; the state comes from the OS registry.
  checked?: boolean;
}

export interface TrayOptions {
  // Whether this install may write a login item (packaged builds only).
  autostartAvailable: boolean;
  // What the OS currently says about launching at login.
  openAtLogin: boolean;
}

export function productLabel(channel: string): string {
  return channel === "preview" ? "Flotilla Preview" : "Flotilla";
}

export function trayMenuSpec(channel: string, options?: TrayOptions): MenuItemSpec[] {
  const product = productLabel(channel);
  const items: MenuItemSpec[] = [{ label: `Open ${product}`, action: "open" }];
  if (options?.autostartAvailable) {
    items.push({ label: "Start at login", action: "autostart", checked: options.openAtLogin });
  }
  items.push({ label: `Quit ${product}`, action: "quit" });
  return items;
}

// A user's click on X ends the window, not the fleet: the sessions keep
// running in their hosts and the daemon keeps running detached. Only a quit
// the app itself initiated may actually close the window.
export function shouldHideOnClose(quitting: boolean): boolean {
  return !quitting;
}

export type TrayClickAction = "show" | "menu";

// Windows convention: one click gets the user back to the fleet, the context
// menu belongs to the right button. Electron only delivers a click event once
// no context menu is bound outright, so main.ts wires both gestures through
// this mapping instead of tray.setContextMenu.
export function trayClickAction(button: "left" | "right"): TrayClickAction {
  return button === "left" ? "show" : "menu";
}
