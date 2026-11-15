// The interop test: a real daemon binary, the real wire, the real framing. It
// only runs when FLT_DAEMON_BIN points at a build, so a checkout without Go
// still has a green suite.

import { spawn, type ChildProcess } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";

import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { connect } from "../electron/daemon";

const binary = process.env.FLT_DAEMON_BIN;
const channel = `gui-test-${process.pid}`;

let daemon: ChildProcess | undefined;
let home: string;

async function waitForToken(file: string, timeoutMs = 10000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const token = await fs.readFile(file, "utf8");
      if (token.trim() !== "") {
        return;
      }
    } catch {
      // not written yet
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`the daemon never wrote ${file}`);
}

describe.skipIf(!binary)("daemon interop", () => {
  beforeAll(async () => {
    home = await fs.mkdtemp(path.join(os.tmpdir(), "flt-gui-"));
    process.env.FLT_HOME = home;

    daemon = spawn(binary!, ["--channel", channel], { stdio: "ignore", windowsHide: true });
    await waitForToken(path.join(home, "token"));
  });

  afterAll(async () => {
    daemon?.kill();
    if (home) {
      await fs.rm(home, { recursive: true, force: true });
    }
    delete process.env.FLT_HOME;
  });

  it("lists the fleet of a real daemon", async () => {
    const client = await connect(channel);
    try {
      const status = (await client.call("daemon.status", {})) as {
        channel: string;
        ready: boolean;
        sessions: unknown[];
      };

      expect(status.channel).toBe(channel);
      expect(status.ready).toBe(true);
      expect(status.sessions).toEqual([]);
    } finally {
      client.close();
    }
  });

  it("fails a call the daemon does not serve, and stays usable", async () => {
    const client = await connect(channel);
    try {
      await expect(client.call("daemon.nonsense", {})).rejects.toThrow(/unknown method/);
      const status = (await client.call("daemon.status", {})) as { ready: boolean };
      expect(status.ready).toBe(true);
    } finally {
      client.close();
    }
  });

  it("refuses a wrong token", async () => {
    const paths = path.join(home, "token");
    const real = (await fs.readFile(paths, "utf8")).trim();

    await fs.writeFile(paths, "not-the-token\n");
    await expect(connect(channel)).rejects.toThrow(/rejected/);

    await fs.writeFile(paths, `${real}\n`);
  });
});
