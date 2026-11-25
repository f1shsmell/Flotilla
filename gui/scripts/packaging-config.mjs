// One packaging identity per channel. A preview build that reused the stable
// appId or product name would overwrite the stable install instead of sitting
// beside it, so the two identities differ in every field the OS keys an
// installed app on.
//
// The version lives here rather than in package.json because two consumers need
// the same number: electron-builder names the installer with it, and
// bundle-binaries.mjs stamps the Go binaries with it. Two copies would let a
// release ship an installer whose embedded `--version` disagrees with its own
// file name.
export function packagingConfig(channel) {
  switch (channel) {
    case "stable":
      return {
        version: "0.1.0",
        appId: "dev.flotilla.app",
        productName: "Flotilla",
        artifactName: "Flotilla-${version}-${arch}.${ext}",
        shortcutName: "Flotilla",
      };
    case "preview":
      return {
        version: "0.1.0-preview.2",
        appId: "dev.flotilla.app.preview",
        productName: "Flotilla Preview",
        artifactName: "Flotilla-Preview-${version}-${arch}.${ext}",
        shortcutName: "Flotilla Preview",
      };
    default:
      throw new Error(`unknown packaging channel: ${channel}`);
  }
}

// versionParts splits a packaging version into the two halves the Go binaries
// carry: the number and the pre-release suffix. A stable version has no suffix,
// which is what version.String() renders as the bare number.
export function versionParts(version) {
  const at = version.indexOf("-");
  if (at < 0) {
    return { version, preRelease: "" };
  }
  return { version: version.slice(0, at), preRelease: version.slice(at + 1) };
}
