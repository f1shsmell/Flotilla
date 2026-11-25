// Prints the CHANGELOG section for one version, for use as a release's notes.
//
//   node scripts/release-notes.mjs 0.1.0-preview.2
//
// The CHANGELOG is the single source of what a version says about itself: the
// release workflow reads this rather than keeping a second copy of the notes
// that can drift from the file a reader opens.

import { readFileSync } from "node:fs";
import path from "node:path";

const here = import.meta.dirname;
const changelog = path.resolve(here, "..", "CHANGELOG.md");
const version = process.argv[2];

if (!version) {
  console.error("usage: release-notes.mjs <version>");
  process.exit(2);
}

const text = readFileSync(changelog, "utf8");
const lines = text.split(/\r?\n/);

// A version heading is `## [<version>]`; the section ends at the next `## `.
const start = lines.findIndex((line) => line.startsWith(`## [${version}]`));
if (start < 0) {
  console.error(`CHANGELOG.md has no section for ${version}: a release without notes is not published`);
  process.exit(1);
}

let end = lines.length;
for (let i = start + 1; i < lines.length; i++) {
  if (lines[i].startsWith("## ")) {
    end = i;
    break;
  }
}

// The heading names the version, which the release page already says; the body
// is what a reader needs.
const body = lines.slice(start + 1, end).join("\n").trim();
if (body === "") {
  console.error(`the CHANGELOG section for ${version} is empty`);
  process.exit(1);
}

console.log(body);
