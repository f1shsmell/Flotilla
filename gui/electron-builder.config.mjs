import { packagingConfig } from "./scripts/packaging-config.mjs";

const identity = packagingConfig(process.env.FLT_CHANNEL ?? "stable");

const config = {
  appId: identity.appId,
  productName: identity.productName,
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
