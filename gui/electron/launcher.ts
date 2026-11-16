// The daemon must outlive the window, and a child of Electron does not: the
// browser process puts its whole process tree in a job object that dies with it,
// so `detached: true` and `unref()` change nothing. Task Scheduler starts a
// process outside that job, which is why an installed window launches its daemon
// through a per-user task.

import { execFile } from "node:child_process";
import { writeFile } from "node:fs/promises";
import path from "node:path";
import { promisify } from "node:util";

import { resolvePaths } from "./home";

const run = promisify(execFile);

// taskFolder groups every task this app makes, so an uninstall can name them and
// a user can find them.
const taskFolder = "\\Flotilla";

// taskName is the per-channel task: two channels never share a daemon.
export function taskName(channel: string): string {
  return `${taskFolder}\\daemon-${channel}`;
}

// wrapperPath is the script a task runs. A task does not inherit the caller's
// environment, so the state root has to be written into the command itself.
export function wrapperPath(home: string, channel: string): string {
  return path.join(home, `daemon-${channel}.cmd`);
}

// wrapperScript is that script: the state root, then the daemon.
export function wrapperScript(home: string, binary: string, channel: string): string {
  return [
    "@echo off",
    `set "FLT_HOME=${home}"`,
    `"${binary}" --channel ${channel}`,
    "",
  ].join("\r\n");
}

// createTaskArgs builds the task that runs the wrapper. It is created enabled
// because `/run` refuses a disabled task, and disabled again once it has run, so
// its own schedule never starts a second daemon.
export function createTaskArgs(script: string, channel: string): string[] {
  return ["/create", "/tn", taskName(channel), "/tr", `"${script}"`, "/sc", "once", "/st", "00:00", "/f"];
}

// disableTaskArgs keeps the task from firing on its own schedule after the run.
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

export interface StartResult {
  // how names what actually started the daemon.
  how: "task" | "spawn";
  // why carries the scheduler's refusal, which is the only way to learn why an
  // installed window fell back to a child that dies with it.
  why?: string;
}

// startDaemon asks Task Scheduler to run the daemon on Windows, and falls back to
// a plain spawn elsewhere or when the scheduler refuses: a window that cannot
// outlive itself is still better than a window with no daemon.
export async function startDaemon(options: StartOptions): Promise<StartResult> {
  const platform = options.platform ?? process.platform;
  const scheduler = options.scheduler ?? ((args: string[]) => run("schtasks", args).then(() => undefined));

  let why: string | undefined;

  if (platform === "win32") {
    try {
      const home = resolvePaths(options.channel).root;
      const script = wrapperPath(home, options.channel);
      await writeFile(script, wrapperScript(home, options.binary, options.channel), "utf8");

      // Enabled to be run, then disabled: `/run` refuses a disabled task, and a
      // schedule nobody wants must not start a second daemon later.
      await scheduler(createTaskArgs(script, options.channel));
      await scheduler(runTaskArgs(options.channel));
      await scheduler(disableTaskArgs(options.channel));
      return { how: "task" };
    } catch (error) {
      // A locked-down machine may refuse a task; the spawn below is the old path.
      why = error instanceof Error ? error.message : String(error);
    }
  }

  const spawner = options.spawner ?? spawnDetached;
  await spawner(options.binary, ["--channel", options.channel]);
  return { how: "spawn", why };
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
