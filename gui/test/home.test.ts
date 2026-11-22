// The home-path parity: gui/electron/home.ts and internal/home must resolve
// state to the same directories, or a GUI and its daemon part ways on disk.
// One golden (internal/home/testdata/home-paths.json) drives both sides; the
// Go test asserts its own platform natively, this one stubs process.platform
// and asserts every row, feeding each row's base the way the resolver reads
// it and resolving the fallback home the way this runtime does.

import { readFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";

import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { resolveHome, resolvePaths } from "../electron/home";

interface ParityCase {
  platform: string;
  channel: string;
  env: string | null;
  base: string;
  suffix: string;
}

const golden = JSON.parse(
  readFileSync(path.join(__dirname, "..", "..", "internal", "home", "testdata", "home-paths.json"), "utf8"),
) as { cases: ParityCase[] };

const savedPlatform = process.platform;

function stubPlatform(platform: string): void {
  Object.defineProperty(process, "platform", { value: platform, configurable: true });
}

beforeEach(() => {
  stubPlatform(savedPlatform);
});

afterEach(() => {
  stubPlatform(savedPlatform);
});

describe("the home-path golden", () => {
  for (const tc of golden.cases) {
    it(`resolves ${tc.platform}/${tc.channel}${tc.env === "set" ? " with FLT_HOME" : ""}`, () => {
      stubPlatform(tc.platform);
      const env: NodeJS.ProcessEnv = { FLT_HOME: "" };

      // The base goes into the environment under the variable the resolver
      // reads for this branch; a HOME base is deliberately absent, because
      // os.homedir() answers from the real OS and no stub reaches it.
      switch (tc.base) {
        case "LOCALAPPDATA":
          env.LOCALAPPDATA = "C:\\parity-base";
          break;
        case "XDG_STATE_HOME":
          env.XDG_STATE_HOME = "/xdg-parity";
          break;
        case "FLT_HOME":
          // An absolute native path: path.resolve keeps it verbatim.
          env.FLT_HOME = "C:\\tmp\\flt-parity";
          break;
      }

      const got = resolveHome(tc.channel, env);
      if (tc.env === "set") {
        expect(got).toBe(env.FLT_HOME);
        expect(resolvePaths(tc.channel, env).token).toBe(path.join(got, "token"));
        return;
      }

      const base =
        tc.base === "HOME"
          ? os.homedir().replace(/[\\/]+$/, "")
          : (env[tc.base] as string);
      const expected = path.join(base, ...tc.suffix.split("/"));
      expect(got).toBe(expected);
      expect(resolvePaths(tc.channel, env).sessions).toBe(path.join(expected, "sessions"));
    });
  }

  it("carries a row for every platform value the resolver switches on", () => {
    const platforms = new Set(golden.cases.map((tc) => tc.platform));
    for (const platform of ["win32", "darwin", "linux"]) {
      expect(platforms.has(platform), `golden misses ${platform}`).toBe(true);
    }
  });
});
