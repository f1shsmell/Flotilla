// The daemon must outlive the window, and a child of Electron does not: the
// browser process puts its whole process tree in a job object that dies with it,
// so `detached: true` and `unref()` change nothing. Task Scheduler starts a
// process outside that job, which is why an installed window launches its daemon
// through a per-user task.

import { execFile } from "node:child_process";
import { promisify } from "node:util";

const run = promisify(execFile);

// taskFolder groups every task this app makes, so an uninstall can name them and
// a user can find them.
const taskFolder = "\\Flotilla";

// taskName is the per-channel task: two channels never share a daemon.
export function taskName(channel: string): string {
  return `${taskFolder}\\daemon-${channel}`;
}

// createTaskArgs builds the task that runs the daemon. It is created disabled:
// nothing should start a daemon at midnight, and `/run` starts it on demand.
export function createTaskArgs(binary: string, channel: string): string[] {
  return [
    "/create",
    "/tn",
    taskName(channel),
    "/tr",
    `"${binary}" --channel ${channel}`,
    "/sc",
    "once",
    "/st",
    "00:00",
    "/f",
  ];
}

// disableTaskArgs keeps the task from firing on its own schedule.
export function disableTaskArgs(channel: string): string[] {
  return ["/change", "/tn", taskName(channel), "/disable"];
}

// runTaskArgs starts the task now.
export function runTaskArgs(channel: string): string[] {
  return ["/run", "/tn", taskName(channel)];
}

// deleteTaskArgs removes it, which is what an uninstall does.
export function deleteTaskArgs(channel: string): string[] {
  return ["/delete", "/tn", taskName(channel), "/f"];
}

export interface StartOptions {
  binary: string;
  channel: string;
  /** Overridable so a test can watch what would be run. */
  platform?: NodeJS.Platform;
  scheduler?: (args: string[]) => Promise<void>;
  spawner?: (binary: string, args: string[]) => Promise<void>;
}

// startDaemon asks Task Scheduler to run the daemon on Windows, and falls back to
// a plain spawn elsewhere or when the scheduler refuses: a window that cannot
// outlive itself is still better than a window with no daemon.
export async function startDaemon(options: StartOptions): Promise<"task" | "spawn"> {
  const platform = options.platform ?? process.platform;
  const scheduler = options.scheduler ?? ((args: string[]) => run("schtasks", args).then(() => undefined));

  if (platform === "win32") {
    try {
      await scheduler(createTaskArgs(options.binary, options.channel));
      await scheduler(disableTaskArgs(options.channel));
      await scheduler(runTaskArgs(options.channel));
      return "task";
    } catch {
      // A locked-down machine may refuse a task; the spawn below is the old path.
    }
  }

  const spawner = options.spawner ?? spawnDetached;
  await spawner(options.binary, ["--channel", options.channel]);
  return "spawn";
}

// spawnDetached starts the daemon as a child, which is the fallback for a
// platform without a scheduler and for a machine that refuses one.
async function spawnDetached(binary: string, args: string[]): Promise<void> {
  const { spawn } = await import("node:child_process");
  await new Promise<void>((resolve, reject) => {
    const child = spawn(binary, args, { detached: true, stdio: "ignore", windowsHide: true });
    child.once("error", (error: NodeJS.ErrnoException) => {
      reject(new Error(`${binary} could not start (${error.code ?? error.message})`));
    });
    child.once("spawn", () => {
      child.unref();
      resolve();
    });
  });
}
