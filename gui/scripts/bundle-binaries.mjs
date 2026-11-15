// Copies the Go binaries the window spawns into resources/bin, which is where
// electron-builder puts them next to the app. The daemon looks for its session
// host in its own directory, so the two have to travel together.

import { execFileSync } from "node:child_process";
import { mkdirSync, statSync } from "node:fs";
import path from "node:path";

const here = import.meta.dirname;
const repo = path.resolve(here, "..", "..");
const out = path.resolve(here, "..", "resources", "bin");
const suffix = process.platform === "win32" ? ".exe" : "";

mkdirSync(out, { recursive: true });

for (const name of ["flt-daemon", "flt-session-host"]) {
  const target = path.join(out, name + suffix);
  execFileSync("go", ["build", "-o", target, `./cmd/${name}`], { cwd: repo, stdio: "inherit" });
  console.log(`${name}: ${statSync(target).size} bytes`);
}
