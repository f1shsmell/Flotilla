// The tray's visible surface, decided in gui/electron/tray.ts and pinned
// here because the OS renders it where no DOM test can reach. The generated
// icon module is checked against the brand mark, so a kit update that skips
// the generator fails here instead of shipping a stale tray.

import { readFileSync } from "node:fs";
import path from "node:path";

import { describe, expect, it } from "vitest";

import { Resvg } from "@resvg/resvg-js";

import { productLabel, shouldHideOnClose, trayMenuSpec } from "../electron/tray";
import { TRAY_ICON, TRAY_ICON_2X } from "../electron/tray-icon";

function packagingProductName(channel: string): string {
  // packaging-config.mjs is plain ESM under scripts/; the identity it renders
  // for electron-builder is what the tray's label must agree with.
  const source = readFileSync(
    path.join(__dirname, "..", "scripts", "packaging-config.mjs"),
    "utf8",
  );
  const match = source.match(
    new RegExp(`case "${channel}":[\\s\\S]*?productName: "([^"]+)"`),
  );
  if (!match) {
    throw new Error(`packaging-config.mjs declares no productName for ${channel}`);
  }
  return match[1];
}

describe("the tray's product label", () => {
  it("names each channel the way the installer does", () => {
    // A tray that said something else than the Start-menu entry would make the
    // two channels indistinguishable in the notification area.
    expect(productLabel("stable")).toBe(packagingProductName("stable"));
    expect(productLabel("preview")).toBe(packagingProductName("preview"));
  });
});

describe("the tray menu", () => {
  it("opens the window first and quits last", () => {
    const spec = trayMenuSpec("preview");
    expect(spec.map((item) => item.action)).toEqual(["open", "quit"]);
    expect(spec[0].label).toBe("Open Flotilla Preview");
    expect(spec[1].label).toBe("Quit Flotilla Preview");
  });

  it("stays distinct between the two channels", () => {
    expect(trayMenuSpec("stable")[0].label).toBe("Open Flotilla");
  });
});

describe("closing the window", () => {
  it("hides to the tray while the app lives, and really closes on quit", () => {
    // A user clicking X must not stop the fleet; the tray's Quit must not be
    // swallowed by the same hide.
    expect(shouldHideOnClose(false)).toBe(true);
    expect(shouldHideOnClose(true)).toBe(false);
  });
});

describe("the generated tray icon", () => {
  function render(size: number): string {
    const svg = readFileSync(
      path.join(__dirname, "..", "src", "assets", "brand-mark.svg"),
      "utf8",
    );
    return new Resvg(svg, { fitTo: { mode: "width", value: size } }).render().asPng().toString("base64");
  }

  it("is the brand mark at 1x and 2x, regenerated with the kit", () => {
    expect(TRAY_ICON).toBe(render(16));
    expect(TRAY_ICON_2X).toBe(render(32));
  });
});
