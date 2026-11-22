// One packaging identity per channel. A preview build that reused the stable
// appId or product name would overwrite the stable install instead of sitting
// beside it, so the two identities differ in every field the OS keys an
// installed app on.
export function packagingConfig(channel) {
  switch (channel) {
    case "stable":
      return {
        appId: "dev.flotilla.app",
        productName: "Flotilla",
        artifactName: "Flotilla-${version}-${arch}.${ext}",
        shortcutName: "Flotilla",
      };
    case "preview":
      return {
        appId: "dev.flotilla.app.preview",
        productName: "Flotilla Preview",
        artifactName: "Flotilla-Preview-${version}-${arch}.${ext}",
        shortcutName: "Flotilla Preview",
      };
    default:
      throw new Error(`unknown packaging channel: ${channel}`);
  }
}
