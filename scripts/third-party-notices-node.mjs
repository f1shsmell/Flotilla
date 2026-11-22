// Emits the markdown notices section for the gui's node module graph, read
// from `pnpm licenses list --json` (run in gui/). The renderer bundle ships
// these modules' code inside the app, so dev-only is not a distinction the
// notices can afford; over-including a build-time tool costs a paragraph.
// A module whose license pnpm cannot read fails the run: the release stops
// before shipping unknown-licensed code.

const execFileSync = await import("node:child_process").then((m) => m.execFileSync);

let raw;
try {
  raw = execFileSync("pnpm", ["licenses", "list", "--json"], {
    cwd: new URL("../gui", import.meta.url),
    maxBuffer: 64 * 1024 * 1024,
    shell: process.platform === "win32",
  });
} catch (error) {
  console.error(`pnpm licenses list failed: ${error.message}`);
  process.exit(1);
}

let grouped;
try {
  grouped = JSON.parse(raw.toString("utf8"));
} catch (error) {
  console.error(`pnpm licenses output is not JSON: ${error.message}`);
  process.exit(1);
}

const licenses = Object.keys(grouped).sort();
for (const license of licenses) {
  for (const module of grouped[license]) {
    const versions = module.versions.join(", ");
    console.log(`## ${module.name} ${versions} — ${license}`);
    console.log();
  }
}
console.log(`${licenses.length} license families across the node graph.`);
