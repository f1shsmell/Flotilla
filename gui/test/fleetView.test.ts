import { describe, expect, it } from "vitest";

import { dirLabel, fleetSummary, mainEmpty } from "../src/fleetView";
import type { Status, StatusEntry } from "../src/store";

function entry(id: string, lifecycle: StatusEntry["lifecycle"], reachable = true): StatusEntry {
  return { id, mode: "pty", host_pid: 1, command: "cmd.exe", dir: "", lifecycle, reachable };
}

function status(sessions: StatusEntry[]): Status {
  return { channel: "stable", pid: 7, ready: true, sessions, failures: [] };
}

describe("fleetSummary", () => {
  it("reads zero from a fleet that has not arrived", () => {
    expect(fleetSummary(null)).toEqual({ total: 0, reachable: 0, attention: 0 });
  });

  it("counts reachability and attention separately", () => {
    const fleet = status([
      entry("a", "blocked"),
      entry("b", "done"),
      entry("c", "working"),
      entry("d", "idle", false),
      entry("e", "unknown"),
    ]);
    expect(fleetSummary(fleet)).toEqual({ total: 5, reachable: 4, attention: 2 });
  });

  it("does not call quiet work attention: only blocked and done ask for the user", () => {
    const fleet = status([entry("a", "working"), entry("b", "idle"), entry("c", "unknown")]);
    expect(fleetSummary(fleet).attention).toBe(0);
  });
});

describe("dirLabel", () => {
  it("takes the last segment of a Windows path", () => {
    expect(dirLabel("C:\\Dev\\Flotilla")).toBe("Flotilla");
  });

  it("takes the last segment of a POSIX path", () => {
    expect(dirLabel("/home/crew/proj")).toBe("proj");
  });

  it("ignores trailing separators", () => {
    expect(dirLabel("C:\\Dev\\Flotilla\\")).toBe("Flotilla");
    expect(dirLabel("/home/crew/proj/")).toBe("proj");
  });

  it("says nothing for an empty directory", () => {
    expect(dirLabel("")).toBe("");
    expect(dirLabel("\\")).toBe("");
  });

  it("keeps a bare name and a drive root readable", () => {
    expect(dirLabel("proj")).toBe("proj");
    expect(dirLabel("C:\\")).toBe("C:");
  });
});

describe("mainEmpty", () => {
  it("calls an absent or empty fleet quiet", () => {
    expect(mainEmpty(null)).toBe("quiet");
    expect(mainEmpty(status([]))).toBe("quiet");
  });

  it("asks the user to pick once the fleet has sessions", () => {
    expect(mainEmpty(status([entry("a", "idle")]))).toBe("pick");
  });
});
