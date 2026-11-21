import { describe, expect, it } from "vitest";

import { foldLog, runObjects, type ToolRun } from "../src/eventView";
import type { Entry } from "../src/store";

function entry(key: string, kind: Entry["kind"], toolTitle?: string): Entry {
  return { key, kind, text: `${kind} ${key}`, toolTitle };
}

describe("foldLog", () => {
  it("leaves a log with no tool calls as one row per entry", () => {
    const rows = foldLog([entry("a", "user"), entry("b", "message"), entry("c", "thought")]);
    expect(rows.map((row) => row.kind)).toEqual(["single", "single", "single"]);
  });

  it("keeps a lone tool call as a line, never hidden behind a click", () => {
    const rows = foldLog([entry("a", "message"), entry("t1", "tool", "Read"), entry("b", "message")]);
    expect(rows.map((row) => row.kind)).toEqual(["single", "single", "single"]);
  });

  it("collapses a consecutive run of tool calls into one expandable row", () => {
    const rows = foldLog([
      entry("a", "message"),
      entry("t1", "tool", "Read"),
      entry("t2", "tool", "Edit"),
      entry("t3", "tool", "Read"),
      entry("b", "message"),
    ]);
    expect(rows.map((row) => row.kind)).toEqual(["single", "run", "single"]);
    const run = rows[1] as ToolRun;
    expect(run.entries.map((e) => e.key)).toEqual(["t1", "t2", "t3"]);
    expect(run.key).toBe("t1");
  });

  it("splits runs that a non-tool entry interrupts", () => {
    const rows = foldLog([
      entry("t1", "tool"),
      entry("t2", "tool"),
      entry("m", "message"),
      entry("t3", "tool"),
      entry("t4", "tool"),
    ]);
    expect(rows.map((row) => row.kind)).toEqual(["run", "single", "run"]);
  });

  it("returns nothing for an empty log", () => {
    expect(foldLog([])).toEqual([]);
  });
});

describe("runObjects", () => {
  it("names the distinct objects a run touched, in first-seen order", () => {
    const run: ToolRun = {
      kind: "run",
      key: "t1",
      entries: [entry("t1", "tool", "Read"), entry("t2", "tool", "Edit"), entry("t3", "tool", "Read")],
    };
    expect(runObjects(run)).toEqual(["Read", "Edit"]);
  });

  it("falls back to the entry key when a call carries no title", () => {
    const run: ToolRun = { kind: "run", key: "t1", entries: [entry("t1", "tool"), entry("t2", "tool")] };
    expect(runObjects(run)).toEqual(["t1", "t2"]);
  });
});