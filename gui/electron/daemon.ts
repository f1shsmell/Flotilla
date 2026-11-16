import fs from "node:fs/promises";
import path from "node:path";

import { DaemonClient, type ClientHandlers } from "./client";
import { startDaemon } from "./launcher";
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

// bundledDaemon is the daemon an installed build ships beside the app, which is
// what an installed copy must run: PATH belongs to the machine, not to us.
async function bundledDaemon(): Promise<string | null> {
  const resources = (process as NodeJS.Process & { resourcesPath?: string }).resourcesPath;
  if (!resources) {
    return null;
  }

  const candidate = path.join(resources, "bin", process.platform === "win32" ? "flt-daemon.exe" : "flt-daemon");
  try {
    await fs.access(candidate);
    return candidate;
  } catch {
    return null;
  }
}

// ensure returns a client, starting the daemon when nobody is listening. The
// daemon outlives the window, so a failure to start is reported, never hidden.
export async function ensure(options: EnsureOptions): Promise<DaemonClient> {
  try {
    return await connect(options.channel, options.handlers);
  } catch (first) {
    const binary =
      options.binary ?? process.env.FLT_DAEMON_BIN ?? (await bundledDaemon()) ?? "flt-daemon";

    // A child of Electron dies with it, so Windows starts the daemon through a
    // per-user task; the launcher falls back to a spawn when that is refused.
    try {
      await startDaemon({ binary, channel: options.channel });
    } catch (error) {
      throw new Error(
        `${binary} could not start: ${
          error instanceof Error ? error.message : String(error)
        }; set FLT_DAEMON_BIN to its path`,
      );
    }

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
