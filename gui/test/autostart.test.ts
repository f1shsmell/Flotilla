// Login autostart: the pieces that decide whether the OS starts the app at
// login, what the entry looks like, and how a launch knows it was one. The
// registry itself is the state; this module only shapes the request.

import { describe, expect, it } from "vitest";

import {
  AUTOSTART_ARG,
  autostartAvailable,
  autostartRequest,
  launchedAtLogin,
} from "../electron/autostart";

describe("the login item", () => {
  it("passes the executable and the hidden flag, never anything else", () => {
    const request = autostartRequest("C:\\Apps\\Flotilla\\Flotilla.exe", true);
    expect(request).toEqual({
      path: "C:\\Apps\\Flotilla\\Flotilla.exe",
      args: [AUTOSTART_ARG],
      openAtLogin: true,
    });
    // One shape for write and read: a read with different args than the
    // write would read openAtLogin false even with the entry on.
    expect(autostartRequest("C:\\Apps\\Flotilla\\Flotilla.exe", false)).toEqual({
      path: "C:\\Apps\\Flotilla\\Flotilla.exe",
      args: [AUTOSTART_ARG],
      openAtLogin: false,
    });
  });

  it("uses the hidden flag as the launch marker", () => {
    expect(launchedAtLogin(["Flotilla.exe", AUTOSTART_ARG])).toBe(true);
    expect(launchedAtLogin(["Flotilla.exe"])).toBe(false);
    expect(launchedAtLogin(["Flotilla.exe", "--other-flag"])).toBe(false);
  });

  it("never touches the registry from a dev checkout", () => {
    // A dev run would otherwise register the electron.exe of node_modules
    // under the user's account.
    expect(autostartAvailable(true)).toBe(true);
    expect(autostartAvailable(false)).toBe(false);
  });
});
