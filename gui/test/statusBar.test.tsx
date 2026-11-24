import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { StatusBar } from "../src/components/StatusBar";
import type { Status, StatusEntry } from "../src/store";

function entry(id: string, lifecycle: StatusEntry["lifecycle"], reachable = true): StatusEntry {
  return { id, mode: "pty", host_pid: 1, command: "cmd.exe", dir: "", lifecycle, reachable };
}

function status(sessions: StatusEntry[]): Status {
  return { channel: "stable", pid: 4242, ready: true, sessions, archived: [], tasks: [], failures: [] };
}

describe("StatusBar", () => {
  it("says it is hailing while the fleet has not arrived", () => {
    const html = renderToStaticMarkup(<StatusBar status={null} />);
    expect(html).toMatch(/class="status-bar"/);
    expect(html).toContain("Hailing the daemon…");
    expect(html).not.toContain("reachable");
  });

  it("reports daemon pid and reachability once the fleet is in", () => {
    const html = renderToStaticMarkup(
      <StatusBar status={status([entry("a", "working"), entry("b", "idle", false)])} />,
    );
    expect(html).toContain("daemon 4242");
    expect(html).toContain("1/2 reachable");
    expect(html).not.toContain("need you");
    // The channel already wears a chip in the brand row; one fact, one readout.
    expect(html).not.toContain("chip-channel");
  });

  it("surfaces attention as its own readout", () => {
    const html = renderToStaticMarkup(
      <StatusBar status={status([entry("a", "blocked"), entry("b", "done"), entry("c", "working")])} />,
    );
    expect(html).toContain("2 need you");
    expect(html).toContain("3/3 reachable");
  });
});