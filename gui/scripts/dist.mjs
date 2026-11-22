// Runs electron-builder for one channel: the config reads FLT_CHANNEL, so the
// channel is set here rather than in a shell line that Windows cmd would eat.
import { spawnSync } from "node:child_process";
import { packagingConfig } from "./packaging-config.mjs";

const channel = process.argv[2] ?? "stable";
packagingConfig(channel);

const result = spawnSync(
  "pnpm",
  ["exec", "electron-builder", "--win", "--publish", "never", "--config", "electron-builder.config.mjs"],
  { stdio: "inherit", env: { ...process.env, FLT_CHANNEL: channel }, shell: process.platform === "win32" },
);
process.exit(result.status ?? 1);
