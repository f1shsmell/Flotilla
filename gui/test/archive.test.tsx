import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { ArchiveView } from "../src/components/ArchiveView";
import { FleetList } from "../src/components/FleetList";
import { fleetSummary } from "../src/fleetView";
import { parseStatus, type ArchivedEntry, type StatusEntry } from "../src/store";

// A finished run as the daemon reports it: no host, no lifecycle, and a log the
// window reads instead of a session it attaches to.
const run: ArchivedEntry = {
  id: "sess-finished",
  mode: "acp",
  command: "claude-agent-acp",
  dir: "C:\\Dev\\Flotilla",
  started: "2026-11-24T09:00:00Z",
  ended: "2026-11-24T09:12:00Z",
};

const live: StatusEntry = {
  id: "sess-live",
  mode: "acp",
  host_pid: 7,
  command: "claude-agent-acp",
  dir: "C:\\Dev\\Flotilla",
  lifecycle: "working",
  reachable: true,
  protected: false,
  steer: false,
};

function list(sessions: StatusEntry[], archived: ArchivedEntry[]): string {
  return renderToStaticMarkup(
    <FleetList
      sessions={sessions}
      archived={archived}
      selected={null}
      onSelect={() => {}}
      onStop={() => {}}
      onSelectArchived={() => {}}
    />,
  );
}

describe("parseStatus and the archive", () => {
  it("keeps finished runs out of the session list", () => {
    const status = parseStatus({ sessions: [live], archived: [run] });

    expect(status.sessions.map((s) => s.id)).toEqual(["sess-live"]);
    expect(status.archived.map((a) => a.id)).toEqual(["sess-finished"]);
    // A finished run has no lifecycle to report; folding one in would let the
    // list offer a stop control for a process that does not exist.
    expect(status.sessions.some((s) => s.id === run.id)).toBe(false);
  });

  it("reads a missing archive as empty rather than as a failure", () => {
    expect(parseStatus({ sessions: [live] }).archived).toEqual([]);
    expect(parseStatus(null).archived).toEqual([]);
  });

  it("keeps what a run never recorded empty instead of inventing it", () => {
    const [bare] = parseStatus({ archived: [{ id: "sess-old" }] }).archived;

    expect(bare.id).toBe("sess-old");
    expect(bare.mode).toBe("");
    expect(bare.command).toBe("");
    expect(bare.started).toBe("");
    expect(bare.ended).toBe("");
  });

  it("drops an archived entry with no id", () => {
    // A row with no id cannot be opened, and rendering it would be a card that
    // does nothing.
    expect(parseStatus({ archived: [{ mode: "acp" }] }).archived).toEqual([]);
  });

  it("counts finished runs apart from sessions", () => {
    const status = parseStatus({ sessions: [live], archived: [run] });
    expect(fleetSummary(status)).toEqual({
      total: 1,
      reachable: 1,
      attention: 0,
      archived: 1,
    });
  });
});

describe("the finished section", () => {
  it("renders finished runs under their own heading", () => {
    const html = list([live], [run]);

    expect(html).toContain("Finished");
    expect(html).toContain("sess-finished");
    expect(html).toContain("ended");
  });

  it("offers no stop control for a run that already stopped", () => {
    const html = list([live], [run]);

    expect(html).toContain('title="end sess-live"');
    expect(html).not.toContain('title="end sess-finished"');
  });

  it("says the run will not resume", () => {
    expect(list([live], [run])).toContain("will not resume");
  });

  it("shows the archive when no session is live", () => {
    // The restarted app's case: the fleet is empty and the archive is all there
    // is. Saying "sessions you start will appear here" would hide the work.
    const html = list([], [run]);

    expect(html).toContain("sess-finished");
    expect(html).not.toContain("Sessions you start will appear here.");
  });

  it("still greets an empty fleet with no history", () => {
    expect(list([], [])).toContain("Sessions you start will appear here.");
  });

  it("renders the archive after the live fleet, never before it", () => {
    // A record must not sit above work that still needs the user: the order is
    // the reading order of the column.
    const html = list([live], [run]);
    expect(html.indexOf("sess-live")).toBeGreaterThanOrEqual(0);
    expect(html.indexOf("sess-finished")).toBeGreaterThan(html.indexOf("sess-live"));
    expect(html.indexOf("Finished")).toBeGreaterThan(html.indexOf("sess-live"));
  });
});

describe("the archive view", () => {
  it("is read-only: no composer and no stop", () => {
    // The log is fetched in an effect, which static markup never runs; what
    // this pins is that the view itself offers nothing to press but Back.
    const html = renderToStaticMarkup(<ArchiveView entry={run} onBack={() => {}} />);

    expect(html).not.toContain("<textarea");
    expect(html).not.toContain("Prompt the agent");
    expect(html).not.toContain("Stop");
    expect(html).not.toContain("Cancel turn");
    // One word for "leave this panel", the same one the settings page uses.
    expect(html).toMatch(/>Back<\/button>/);
  });

  it("says the agent will not resume", () => {
    const html = renderToStaticMarkup(<ArchiveView entry={run} onBack={() => {}} />);
    expect(html).toContain("will not resume");
    expect(html).toContain("ended");
  });

  it("names an unrecorded time as unknown instead of showing a blank", () => {
    const html = renderToStaticMarkup(
      <ArchiveView entry={{ ...run, started: "", ended: "" }} onBack={() => {}} />,
    );
    expect(html).toContain("started unknown");
    expect(html).toContain("ended unknown");
  });
});
