// Prints one channel's packaging version, so the release workflow and any other
// caller read the same number the installer and the Go binaries carry.
//
//   node scripts/print-version.mjs preview   -> 0.1.0-preview.2

import { packagingConfig } from "../gui/scripts/packaging-config.mjs";

const channel = process.argv[2] ?? "stable";
console.log(packagingConfig(channel).version);
