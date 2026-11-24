import { describe, expect, it } from "vitest";

import { dirLabel, fleetSummary, mainEmpty, modeLabel, stopLabel, windowTitle } from "../src/fleetView";
import type { Status, StatusEntry } from "../src/store";

function entry(id: string, lifecycle: StatusEntry["lifecycle"], reachable = true): StatusEntry {
  return { id, mode: "pty", host_pid: 1, command: "cmd.exe", dir: "", lifecycle, reachable };
}

function status(sessions: StatusEntry[]): Status {
  return { channel: "stable", pid: 7, ready: true, sessions, archived: [], failures: [] };
}

describe("fleetSummary", () => {
  it("reads zero from a fleet that has not arrived", () => {
    expect(fleetSummary(null)).toEqual({ total: 0, reachable: 0, attention: 0, archived: 0 });
  });

  it("counts reachability and attention separately", () => {
    const fleet = status([
      entry("a", "blocked"),
      entry("b", "done"),
      entry("c", "working"),
      entry("d", "idle", false),
      entry("e", "unknown"),
    ]);
    expect(fleetSummary(fleet)).toEqual({ total: 5, reachable: 4, attention: 2, archived: 0 });
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

describe("windowTitle", () => {
  it("keeps stable and the unknown at plain Flotilla", () => {
    expect(windowTitle(null)).toBe("Flotilla");
    expect(windowTitle("")).toBe("Flotilla");
    expect(windowTitle("stable")).toBe("Flotilla");
  });

  it("names the preview the way its installer does", () => {
    expect(windowTitle("preview")).toBe("Flotilla Preview");
  });

  it("carries a dev channel so two smoke windows stay apart", () => {
    expect(windowTitle("tray-shot")).toBe("Flotilla tray-shot");
  });
});

describe("modeLabel", () => {
  it("names a protocol in the window's word", () => {
    // One concept, one word: the picker, the fleet chip and the session header
    // all say "Chat" or "Terminal", never "acp" or "pty".
    expect(modeLabel("acp")).toBe("Chat");
    expect(modeLabel("pty")).toBe("Terminal");
  });

  it("says unknown rather than showing nothing", () => {
    expect(modeLabel("")).toBe("Unknown");
  });

  it("passes an unrecognised mode through instead of guessing", () => {
    expect(modeLabel("weird")).toBe("weird");
  });
});

describe("stopLabel", () => {
  it("translates the protocol's reason into a sentence", () => {
    expect(stopLabel("end_turn")).toBe("finished");
    expect(stopLabel("cancelled")).toBe("cancelled");
    expect(stopLabel("max_tokens")).toBe("hit the token limit");
  });

  it("shows an unknown reason as it came, rather than inventing one", () => {
    expect(stopLabel("something_new")).toBe("something_new");
  });
});
