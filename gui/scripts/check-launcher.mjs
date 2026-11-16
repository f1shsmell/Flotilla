// Checks the launcher the way the app uses it: create the per-user task, run it,
// and connect to the daemon it started. The window's reason for the task is that
// Task Scheduler is outside Electron's job object, which no unit test can show —
// this is the run that shows the task path works at all.
//
// Needs FLT_DAEMON_BIN and a Windows runner with schtasks available.

import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import os from "node:os";
import path from "node:path";

import { connect } from "../dist/electron/daemon.js";
import { deleteTaskArgs, startDaemon, taskName } from "../dist/electron/launcher.js";

const binary = process.env.FLT_DAEMON_BIN;
if (!binary) {
  throw new Error("FLT_DAEMON_BIN is unset: point it at a built flt-daemon");
}

const channel = `launcher-check-${process.pid}`;
const home = mkdtempSync(path.join(os.tmpdir(), "flt-launcher-"));
process.env.FLT_HOME = home;

let daemonPID = 0;

try {
  const how = await startDaemon({ binary, channel });
  if (how !== "task") {
    throw new Error(`the launcher fell back to ${how}: the scheduler refused the task`);
  }

  const deadline = Date.now() + 20000;
  let client;
  for (;;) {
    try {
      client = await connect(channel);
      break;
    } catch (error) {
      if (Date.now() > deadline) {
        throw error;
      }
      await new Promise((resolve) => setTimeout(resolve, 200));
    }
  }

  const status = await client.call("daemon.status", {});
  daemonPID = status.pid;
  client.close();

  console.log(`launcher: task ${taskName(channel)} started the daemon (pid ${daemonPID})`);
  console.log(`launcher: daemon answers on channel ${channel}`);
} finally {
  try {
    execFileSync("schtasks", deleteTaskArgs(channel), { stdio: "ignore" });
  } catch {
    console.error("launcher: the task could not be deleted");
  }
  if (daemonPID > 0) {
    try {
      process.kill(daemonPID);
    } catch {
      // already gone
    }
  }
  rmSync(home, { recursive: true, force: true });
}
