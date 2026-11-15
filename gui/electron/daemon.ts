import { spawn } from "node:child_process";
import fs from "node:fs/promises";

import { DaemonClient, type ClientHandlers } from "./client";
import { endpoint, resolvePaths } from "./home";

export interface EnsureOptions {
  channel: string;
  handlers?: ClientHandlers;
  timeoutMs?: number;
  binary?: string;
}

// connect opens one authenticated client on the channel's daemon.
export async function connect(
  channel: string,
  handlers?: ClientHandlers,
): Promise<DaemonClient> {
  const paths = resolvePaths(channel);

  const token = (await fs.readFile(paths.token, "utf8")).trim();
  if (!token) {
    throw new Error(`the token file ${paths.token} is empty`);
  }

  return DaemonClient.connect({
    path: endpoint(channel, "daemon", paths.sessions),
    token,
    role: "gui",
    handlers,
  });
}

// start spawns the daemon detached and resolves once the process exists. A
// missing binary reports as an error event, not as a throw, so the event is what
// this waits on: an unhandled one would take the whole main process down.
async function start(binary: string, channel: string): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const child = spawn(binary, ["--channel", channel], {
      detached: true,
      stdio: "ignore",
      windowsHide: true,
    });

    child.once("error", (error: NodeJS.ErrnoException) => {
      reject(
        new Error(
          `${binary} could not start (${error.code ?? error.message}); set FLT_DAEMON_BIN to its path`,
        ),
      );
    });

    child.once("spawn", () => {
      child.unref();
      resolve();
    });
  });
}

// ensure returns a client, starting the daemon when nobody is listening. The
// daemon outlives the window, so a failure to start is reported, never hidden.
export async function ensure(options: EnsureOptions): Promise<DaemonClient> {
  try {
    return await connect(options.channel, options.handlers);
  } catch (first) {
    const binary = options.binary ?? process.env.FLT_DAEMON_BIN ?? "flt-daemon";
    await start(binary, options.channel);

    const deadline = Date.now() + (options.timeoutMs ?? 10000);
    let last: unknown = first;
    while (Date.now() < deadline) {
      await new Promise((resolve) => setTimeout(resolve, 100));
      try {
        return await connect(options.channel, options.handlers);
      } catch (error) {
        last = error;
      }
    }

    throw new Error(
      `the daemon never answered on channel ${options.channel}: ${
        last instanceof Error ? last.message : String(last)
      }`,
    );
  }
}
