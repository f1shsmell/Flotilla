// The channel an installed app runs on decides its pipe, its home and its
// daemon. An installed copy cannot carry env, so the packaged identity must
// name the channel; a dev or smoke run names it through FLT_CHANNEL.

import { describe, expect, it, vi } from "vitest";

import { resolveChannel } from "../electron/channel";

describe("the channel a window runs on", () => {
  it("lets FLT_CHANNEL win: dev and smoke name their own", () => {
    expect(resolveChannel("preview", true, "stable")).toBe("preview");
    expect(resolveChannel("gui-test-1", true, "stable")).toBe("gui-test-1");
    expect(resolveChannel("pack-smoke", false, null)).toBe("pack-smoke");
  });

  it("reads an installed copy's channel from its packaging identity", () => {
    // A double-clicked (or login-launched) preview exe carries no env. Without
    // the identity it ran as stable: the preview install dialed the stable
    // pipe and wrote into the stable home.
    expect(resolveChannel(undefined, true, "preview")).toBe("preview");
    expect(resolveChannel(undefined, true, "stable")).toBe("stable");
  });

  it("falls back to stable outside a package and without env", () => {
    expect(resolveChannel(undefined, false, null)).toBe("stable");
    expect(resolveChannel(undefined, true, null)).toBe("stable");
    expect(resolveChannel(undefined, true, "beta")).toBe("stable");
  });
});

describe("the builder bakes the channel into the packaged manifest", () => {
  async function manifestFor(channel: string): Promise<string | undefined> {
    const saved = process.env.FLT_CHANNEL;
    process.env.FLT_CHANNEL = channel;
    try {
      // A fresh module registry re-reads FLT_CHANNEL at config time.
      vi.resetModules();
      const config = (await import("../electron-builder.config.mjs")) as {
        default: { extraMetadata?: { fltChannel?: string } };
      };
      return config.default.extraMetadata?.fltChannel;
    } finally {
      if (saved === undefined) {
        delete process.env.FLT_CHANNEL;
      } else {
        process.env.FLT_CHANNEL = saved;
      }
    }
  }

  it("names preview and stable exactly", async () => {
    expect(await manifestFor("preview")).toBe("preview");
    expect(await manifestFor("stable")).toBe("stable");
  });
});
