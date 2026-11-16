// A cold start has more than one caller. The window polls the fleet and the
// first start both call ensure() before either has a daemon, so on a cold
// machine two of them spawn one. The second daemon loses the pipe and exits,
// which is wasted work and a line in the log that reads like a failure.
//
// The dedupe must not outlive the daemon it deduped: once that process is gone,
// the next caller has to start a new one.
//
// This test needs the real binary, so it only runs when FLT_DAEMON_BIN is set.

import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";

import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { ensure } from "../electron/daemon";

const binary = process.env.FLT_DAEMON_BIN;
const channel = `gui-ensure-${process.pid}`;

let home: string;

async function log(): Promise<string> {
  return fs.readFile(path.join(home, "logs", "flt-daemon.log"), "utf8");
}

async function starts(): Promise<string[]> {
  return (await log()).split("\n").filter((line) => line.includes('msg="daemon starting"'));
}

// listening is every pid that said it was listening, in the order they said it.
async function listening(): Promise<number[]> {
  const text = await log();
  return [...text.matchAll(/msg="daemon listening".*pid=(\d+)/g)].map((match) => Number(match[1]));
}

async function gone(pid: number): Promise<void> {
  process.kill(pid);
  const deadline = Date.now() + 5000;
  while (Date.now() < deadline) {
    try {
      process.kill(pid, 0);
    } catch {
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`the daemon ${pid} did not stop`);
}

describe.skipIf(!binary)("ensure", () => {
  beforeAll(async () => {
    home = await fs.mkdtemp(path.join(os.tmpdir(), "flt-ensure-"));
    process.env.FLT_HOME = home;
  });

  afterAll(async () => {
    // The daemon is detached, so nothing else holds it: stop it by the pid it
    // reported, then wait for it to be gone before the directory can go.
    for (const pid of await listening()) {
      try {
        await gone(pid);
      } catch {
        // it had already stopped
      }
    }

    for (let attempt = 0; home && attempt < 10; attempt += 1) {
      try {
        await fs.rm(home, { recursive: true, force: true });
        break;
      } catch {
        await new Promise((resolve) => setTimeout(resolve, 50));
      }
    }
  });

  it("starts one daemon when two callers arrive together", async () => {
    const [first, second] = await Promise.all([
      ensure({ channel, binary: binary! }),
      ensure({ channel, binary: binary! }),
    ]);

    expect(first.alive).toBe(true);
    expect(second.alive).toBe(true);
    expect(await starts()).toHaveLength(1);
  }, 20000);

  it("starts another once the daemon it deduped is gone", async () => {
    await ensure({ channel, binary: binary! });
    const before = await listening();
    const pid = before[before.length - 1];
    expect(pid).toBeTypeOf("number");

    await gone(pid);

    const client = await ensure({ channel, binary: binary! });
    expect(client.alive).toBe(true);

    const after = await listening();
    expect(after).toHaveLength(before.length + 1);
    expect(after[after.length - 1]).not.toBe(pid);
  }, 20000);
});
