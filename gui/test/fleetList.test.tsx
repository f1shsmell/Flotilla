import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { FleetList } from "../src/components/FleetList";
import type { StatusEntry } from "../src/store";

const sessions: StatusEntry[] = [
  {
    id: "sess-a",
    mode: "pty",
    host_pid: 1,
    command: "cmd.exe",
    dir: "C:\\Dev\\Flotilla",
    lifecycle: "blocked",
    reachable: true,
  },
  {
    id: "sess-b",
    mode: "acp",
    host_pid: 2,
    command: "claude-agent-acp",
    dir: "",
    lifecycle: "done",
    reachable: false,
  },
];

function list(entries: StatusEntry[] = sessions): string {
  return renderToStaticMarkup(
    <FleetList
      sessions={entries}
      archived={[]}
      selected="sess-a"
      onSelect={() => {}}
      onStop={() => {}}
      onSelectArchived={() => {}}
    />,
  );
}

describe("the fleet card", () => {
  it("keeps the row button the smoke harness clicks", () => {
    const html = list();
    expect(html).toMatch(/<button[^>]*class="session-row[^"]*"/);
    expect(html).toContain("sess-a");
    expect(html).toContain("sess-b");
  });

  it("wears its lifecycle and its working directory", () => {
    const html = list();
    expect(html).toContain("blocked");
    expect(html).toContain("done");
    expect(html).toContain("Flotilla");
  });

  it("marks an unreachable session without hiding its state", () => {
    const html = list();
    expect(html).toContain("unreachable");
    expect(html).toContain("done");
  });

  it("keeps stop a separate action, never nested in the row", () => {
    const html = list();
    expect(html).toContain('title="end sess-a"');
    expect(html).toContain('title="end sess-b"');
    expect(html).not.toMatch(/<button(?:(?!<\/button>)[\s\S])*<button/);
  });
});

describe("the empty fleet", () => {
  it("says where sessions will appear instead of naming a channel", () => {
    const html = list([]);
    expect(html).toContain("Sessions you start will appear here.");
  });

  it("points at the composer card, not at a form that left the sidebar", () => {
    const html = list([]);
    expect(html).toContain("composer card");
    expect(html).not.toContain("above");
  });
});
