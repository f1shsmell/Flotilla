// Copies the Go binaries the window spawns into resources/bin, which is where
// electron-builder puts them next to the app. The daemon looks for its session
// host in its own directory, so the two have to travel together.
//
// flt-replay-agent travels for the same reason: the catalogue offers it as a
// preset and looks for it beside the daemon, so a release that left it out would
// list an agent it cannot start.
//
// Each binary is stamped with the channel's version. Without it a released
// daemon reports the source tree's placeholder, which makes `--version` useless
// in a bug report.

import { execFileSync } from "node:child_process";
import { mkdirSync, statSync } from "node:fs";
import path from "node:path";

import { packagingConfig, versionParts } from "./packaging-config.mjs";

const here = import.meta.dirname;
const repo = path.resolve(here, "..", "..");
const out = path.resolve(here, "..", "resources", "bin");
const suffix = process.platform === "win32" ? ".exe" : "";

// The channel is the one dist.mjs is packaging; FLT_CHANNEL is set by the dist
// scripts and by the release workflow.
const channel = process.env.FLT_CHANNEL ?? "stable";
const identity = packagingConfig(channel);
const { version, preRelease } = versionParts(identity.version);

const module = "github.com/f1shsmell/flotilla/internal/version";
const ldflags = [
  `-X ${module}.Version=${version}`,
  `-X ${module}.PreRelease=${preRelease}`,
  `-X ${module}.Channel=${channel}`,
].join(" ");

mkdirSync(out, { recursive: true });

for (const name of ["flt-daemon", "flt-session-host", "flt-replay-agent"]) {
  const target = path.join(out, name + suffix);
  execFileSync(
    "go",
    ["build", "-ldflags", ldflags, "-o", target, `./cmd/${name}`],
    { cwd: repo, stdio: "inherit" },
  );
  console.log(`${name}: ${statSync(target).size} bytes, ${version}${preRelease ? "-" + preRelease : ""} ${channel}`);
}
