import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

import { packagingConfig } from "../scripts/packaging-config.mjs";

// The two channels install beside each other on one machine. Every field the
// OS keys an installed app on — appId, product name, artifact, shortcut —
// must differ, or a preview build replaces the stable install in place.
describe("packaging identity", () => {
  const stable = packagingConfig("stable");
  const preview = packagingConfig("preview");

  it("gives stable and preview distinct identities", () => {
    expect(stable.appId).toBe("dev.flotilla.app");
    expect(stable.productName).toBe("Flotilla");
    expect(preview.appId).not.toBe(stable.appId);
    expect(preview.productName).not.toBe(stable.productName);
    expect(preview.artifactName).not.toBe(stable.artifactName);
    expect(preview.shortcutName).not.toBe(stable.shortcutName);
  });

  it("names the preview artifact after the preview channel", () => {
    expect(preview.artifactName).toContain("Preview");
    expect(stable.artifactName).not.toContain("Preview");
  });

  it("leaves the version to electron-builder", () => {
    // A literal version here would pin one build number into every artifact.
    for (const identity of [stable, preview]) {
      expect(identity.artifactName).toContain("${version}");
    }
  });

  it("refuses a channel it does not know", () => {
    expect(() => packagingConfig("beta")).toThrow("unknown packaging channel: beta");
  });

  it("ships the application icon the builder picks up", () => {
    // electron-builder takes build/icon.ico without configuration; a missing
    // icon silently ships the default Electron one instead.
    const icon = readFileSync(fileURLToPath(new URL("../build/icon.ico", import.meta.url)));
    expect(icon.length).toBeGreaterThan(1024);
    // ICO: reserved zero, type 1, then the size table.
    expect(icon.readUInt16LE(2)).toBe(1);
    expect(icon.readUInt16LE(4)).toBeGreaterThanOrEqual(2);
  });
});
