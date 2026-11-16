import { describe, expect, it } from "vitest";

import {
  createTaskArgs,
  deleteTaskArgs,
  disableTaskArgs,
  runTaskArgs,
  startDaemon,
  taskName,
} from "../electron/launcher";

describe("task names", () => {
  it("scopes the task to the channel and groups it under one folder", () => {
    expect(taskName("stable")).toBe("\\Flotilla\\daemon-stable");
    expect(taskName("preview")).not.toBe(taskName("stable"));
  });
});

describe("schtasks arguments", () => {
  it("quotes the binary, because an install path has spaces", () => {
    const args = createTaskArgs("C:\\Program Files\\Flotilla\\resources\\bin\\flt-daemon.exe", "stable");

    expect(args).toContain("/create");
    expect(args[args.indexOf("/tr") + 1]).toBe(
      '"C:\\Program Files\\Flotilla\\resources\\bin\\flt-daemon.exe" --channel stable',
    );
    expect(args).toContain("/f");
  });

  it("creates the task for a schedule it is then told to ignore", () => {
    const args = createTaskArgs("d.exe", "stable");
    expect(args[args.indexOf("/sc") + 1]).toBe("once");
    expect(args[args.indexOf("/st") + 1]).toBe("00:00");
    expect(disableTaskArgs("stable")).toEqual([
      "/change",
      "/tn",
      "\\Flotilla\\daemon-stable",
      "/disable",
    ]);
    expect(runTaskArgs("stable")).toEqual(["/run", "/tn", "\\Flotilla\\daemon-stable"]);
    expect(deleteTaskArgs("stable")).toEqual(["/delete", "/tn", "\\Flotilla\\daemon-stable", "/f"]);
  });
});

describe("startDaemon", () => {
  it("goes through the scheduler on Windows", async () => {
    const calls: string[][] = [];

    const started = await startDaemon({
      binary: "d.exe",
      channel: "stable",
      platform: "win32",
      scheduler: async (args) => {
        calls.push(args);
      },
      spawner: async () => {
        throw new Error("the spawn should not be reached");
      },
    });

    expect(started).toBe("task");
    expect(calls.map((args) => args[0])).toEqual(["/create", "/change", "/run"]);
  });

  it("falls back to a spawn when the scheduler refuses", async () => {
    const spawned: string[][] = [];

    const started = await startDaemon({
      binary: "d.exe",
      channel: "stable",
      platform: "win32",
      scheduler: async () => {
        throw new Error("access denied");
      },
      spawner: async (_binary, args) => {
        spawned.push(args);
      },
    });

    expect(started).toBe("spawn");
    expect(spawned).toEqual([["--channel", "stable"]]);
  });

  it("does not touch a scheduler off Windows", async () => {
    const started = await startDaemon({
      binary: "d",
      channel: "stable",
      platform: "linux",
      scheduler: async () => {
        throw new Error("the scheduler should not be called");
      },
      spawner: async () => undefined,
    });

    expect(started).toBe("spawn");
  });
});
