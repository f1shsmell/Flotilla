import { packagingConfig } from "./scripts/packaging-config.mjs";

const channel = process.env.FLT_CHANNEL ?? "stable";
const identity = packagingConfig(channel);

const config = {
  appId: identity.appId,
  productName: identity.productName,
  // The runtime reads its channel from the packaged manifest: an installed app
  // launches with no env (double-click, login item) and must still land on its
  // own pipe and home.
  extraMetadata: { fltChannel: channel },
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
  },
};

export default config;
