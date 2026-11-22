import { packagingConfig } from "./scripts/packaging-config.mjs";

const channel = process.env.FLT_CHANNEL ?? "stable";
const identity = packagingConfig(channel);

const config = {
  appId: identity.appId,
  productName: identity.productName,
  // The runtime reads its identity from the packaged manifest: an installed
  // app launches with no env (double-click, login item) and must still land
  // on its own pipe, home and userData. app.getName() reads this manifest's
  // productName, which is what splits the two channels' userData apart.
  extraMetadata: { fltChannel: channel, productName: identity.productName },
  copyright: "MIT",
  directories: {
    output: "release",
  },
  publish: null,
  files: ["dist/**/*", "package.json"],
  extraResources: [
    {
      from: "resources/bin",
      to: "bin",
    },
  ],
  win: {
    target: [
      {
        target: "nsis",
        arch: ["x64"],
      },
    ],
    artifactName: identity.artifactName,
  },
  nsis: {
    oneClick: false,
    perMachine: false,
    allowToChangeInstallationDirectory: true,
    shortcutName: identity.shortcutName,
    deleteAppDataOnUninstall: false,
    // The daemon outlives the window; the installer stops the processes that
    // belong to this install directory before touching its files.
    include: "build/installer.nsh",
  },
};

export default config;
