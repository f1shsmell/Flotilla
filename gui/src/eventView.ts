// Pure projection of a session log into readable rows. The store keeps every
// update; this decides how the log presents them without touching the wire.

import type { Entry } from "./store";

export interface ToolRun {
  kind: "run";
  key: string;
  entries: Entry[];
}

export interface SingleRow {
  kind: "single";
  entry: Entry;
}

export type LogRow = ToolRun | SingleRow;

// foldLog collapses a consecutive run of tool calls into one expandable row: a
// turn can emit dozens and each as a full line buries the agent's own words. A
// run of one stays a line, so a lone call is never hidden behind a click.
export function foldLog(entries: Entry[]): LogRow[] {
  const rows: LogRow[] = [];
  let run: Entry[] = [];
  const flush = (): void => {
    if (run.length === 0) {
      return;
    }
    if (run.length === 1) {
      rows.push({ kind: "single", entry: run[0] });
    } else {
      rows.push({ kind: "run", key: run[0].key, entries: run });
    }
    run = [];
  };
  for (const entry of entries) {
    if (entry.kind === "tool") {
      run.push(entry);
      continue;
    }
    flush();
    rows.push({ kind: "single", entry });
  }
  flush();
  return rows;
}

// runObjects names the distinct things a run touched, in first-seen order, so a
// collapsed row still says what happened rather than only how many times.
export function runObjects(run: ToolRun): string[] {
  const names: string[] = [];
  for (const entry of run.entries) {
    const name = entry.toolTitle || entry.key;
    if (!names.includes(name)) {
      names.push(name);
    }
  }
  return names;
}