// Login autostart: whether this install may register a login item, what the
// item launches, and how a launch knows it came from one. The OS registry is
// the state; nothing is mirrored to a file. One install, one entry: Electron
// keys the Run value on this app's AppUserModelID (verified against
// HKCU\...\Run on Windows), and each channel's packaged exe carries its own,
// so stable and preview never overwrite each other. The write and the remove
// key on the AUMID alone; the READ compares the stored path and args, so the
// request shape below is the shape both sides must share.

// The launch marker also drives the hidden start: a login launch shows only
// the tray and warms the daemon; a human click opens the window.
export const AUTOSTART_ARG = "--hidden";

// A dev checkout must never write the user's Run key: it would register the
// electron.exe of node_modules under their account.
export function autostartAvailable(packaged: boolean): boolean {
  return packaged;
}

export interface LoginItemRequest {
  path: string;
  args: string[];
  openAtLogin: boolean;
}

// One shape for the write and the read: a read that passes different args
// than the write reads openAtLogin false even though the entry is on.
export function autostartRequest(exePath: string, openAtLogin: boolean): LoginItemRequest {
  return { path: exePath, args: [AUTOSTART_ARG], openAtLogin };
}

export function launchedAtLogin(argv: string[]): boolean {
  return argv.includes(AUTOSTART_ARG);
}
