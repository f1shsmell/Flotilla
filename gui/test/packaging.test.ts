import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

import { packagingConfig, versionParts } from "../scripts/packaging-config.mjs";

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

  it("carries the version both consumers read", () => {
    // The installer's name and the Go binaries' `--version` come from this one
    // number; a second copy would let a release ship two different versions.
    expect(stable.version).toBe("0.1.0");
    expect(preview.version).toBe("0.1.0-preview.1");
    expect(preview.version.startsWith(stable.version)).toBe(true);
  });

  it("splits a preview version into the halves the binaries carry", () => {
    expect(versionParts("0.1.0")).toEqual({ version: "0.1.0", preRelease: "" });
    expect(versionParts("0.1.0-preview.1")).toEqual({
      version: "0.1.0",
      preRelease: "preview.1",
    });
    // A pre-release suffix with its own dash keeps the rest whole: only the
    // first dash separates the number from the suffix.
    expect(versionParts("0.1.0-preview.1-hotfix")).toEqual({
      version: "0.1.0",
      preRelease: "preview.1-hotfix",
    });
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
