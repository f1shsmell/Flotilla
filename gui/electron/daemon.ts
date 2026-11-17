import { spawn, type ChildProcess } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";

import { DaemonClient, type ClientHandlers } from "./client";
import { endpoint, resolvePaths } from "./home";

export interface EnsureOptions {
  channel: string;
  // client names this window on every connection it opens. The daemon answers
  // per-client questions (has this window read the finished turn) from it, so
  // the control connection and the session connections must share one name.
  client: string;
  handlers?: ClientHandlers;
  timeoutMs?: number;
  binary?: string;
}

// connect opens one authenticated client on the channel's daemon.
export async function connect(
  channel: string,
  client: string,
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
    client,
    handlers,
  });
}

// start spawns the daemon detached and resolves once the process exists. A
// missing binary reports as an error event, not as a throw, so the event is what
// this waits on: an unhandled one would take the whole main process down.
async function start(binary: string, channel: string): Promise<ChildProcess> {
  return new Promise<ChildProcess>((resolve, reject) => {
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
      resolve(child);
    });
  });
}

// started remembers the daemon this process has running per channel, and
// starting covers the window before one exists. A cold window has more than one
// caller -- the fleet poll and the first start -- and both find no daemon, so
// both used to spawn one; the second lost the pipe and exited, which is wasted
// work and a log line that reads like a failure.
const started = new Map<string, ChildProcess>();
const starting = new Map<string, Promise<ChildProcess>>();

// alive asks the OS, not the child object: the exit event arrives a tick after
// the process is gone, and ensure must not hand out a daemon that already died.
function alive(child: ChildProcess): boolean {
  if (child.pid === undefined) {
    return false;
  }
  try {
    process.kill(child.pid, 0);
    return true;
  } catch {
    return false;
  }
}

function startOnce(binary: string, channel: string): Promise<ChildProcess> {
  const key = `${channel}\u0000${binary}`;

  const pending = starting.get(key);
  if (pending) {
    return pending;
  }

  const running = started.get(key);
  if (running && alive(running)) {
    return Promise.resolve(running);
  }

  const attempt = start(binary, channel).then((child) => {
    started.set(key, child);
    child.once("exit", () => {
      if (started.get(key) === child) {
        started.delete(key);
      }
    });
    return child;
  });

  starting.set(key, attempt);
  const settle = (): void => {
    if (starting.get(key) === attempt) {
      starting.delete(key);
    }
  };
  void attempt.then(settle, settle);

  return attempt;
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
    return await connect(options.channel, options.client, options.handlers);
  } catch (first) {
    const binary =
      options.binary ?? process.env.FLT_DAEMON_BIN ?? (await bundledDaemon()) ?? "flt-daemon";
    await startOnce(binary, options.channel);

    const deadline = Date.now() + (options.timeoutMs ?? 10000);
    let last: unknown = first;
    while (Date.now() < deadline) {
      await new Promise((resolve) => setTimeout(resolve, 100));
      try {
        return await connect(options.channel, options.client, options.handlers);
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
