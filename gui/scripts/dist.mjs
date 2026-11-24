// Runs one channel's packaging end to end: the Go binaries the window spawns,
// then electron-builder. The channel is set here rather than in a shell line
// that Windows cmd would eat.
//
// The binaries are built here, not by an earlier script in the pnpm chain,
// because they are stamped with the channel's version: a build that ran before
// FLT_CHANNEL was set would ship a preview installer whose daemon reports the
// stable identity.
import { spawnSync } from "node:child_process";
import { packagingConfig } from "./packaging-config.mjs";

const channel = process.argv[2] ?? "stable";
packagingConfig(channel);

const env = { ...process.env, FLT_CHANNEL: channel };
const shell = process.platform === "win32";

const binaries = spawnSync("node", ["scripts/bundle-binaries.mjs"], {
  stdio: "inherit",
  env,
  shell,
});
if (binaries.status !== 0) {
  process.exit(binaries.status ?? 1);
}

const result = spawnSync(
  "pnpm",
  ["exec", "electron-builder", "--win", "--publish", "never", "--config", "electron-builder.config.mjs"],
  { stdio: "inherit", env, shell },
);
process.exit(result.status ?? 1);
