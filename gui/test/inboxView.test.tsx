import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { constants } from "../electron/contract";
import { InboxView } from "../src/components/InboxView";
import type { InboxEntry } from "../src/store";

const entries: InboxEntry[] = [
  {
    id: "sess-a",
    kind: constants.daemon.InboxInput,
    title: "edit config.json",
    command: "claude-agent-acp",
    dir: "C:\\work",
  },
  {
    id: "sess-b",
    kind: constants.daemon.InboxUnread,
    title: "",
    command: "codex",
    dir: "",
  },
];

function view(list: InboxEntry[] = entries, known = true): string {
  return renderToStaticMarkup(
    <InboxView entries={list} known={known} onOpen={() => {}} onClose={() => {}} />,
  );
}

describe("the inbox", () => {
  it("lists what waits on an answer under its own heading", () => {
    const html = view();
    expect(html).toContain("Needs your answer");
    expect(html).toContain("edit config.json");
    expect(html).toContain("needs input");
    expect(html).toContain("sess-a");
  });

  it("keeps unread finishes apart from questions", () => {
    const html = view();
    expect(html).toContain("Unread");
    expect(html).toContain("Turn finished");
    expect(html).toContain("sess-b");
    expect(html.indexOf("Needs your answer")).toBeLessThan(html.indexOf(">Unread<"));
  });

  it("says a blocked session without a structured question is waiting", () => {
    const html = view([
      { id: "sess-pty", kind: "input", title: "", command: "cmd.exe", dir: "" },
    ]);
    expect(html).toContain("Waiting for your answer");
    expect(html).toContain("waiting");
  });

  it("opens a session from the row, not from a nested control", () => {
    const html = view();
    expect(html).toMatch(/<button[^>]*class="inbox-row"/);
    expect(html).not.toMatch(/<button(?:(?!<\/button>)[\s\S])*<button/);
  });

  it("says nothing needs you when the list is empty", () => {
    const html = view([]);
    expect(html).toContain("Nothing needs you.");
    expect(html).not.toContain("Needs your answer");
    expect(html).not.toContain(">Unread<");
  });

  it("says the fleet is unknown rather than empty when it cannot be read", () => {
    const html = view([], false);
    expect(html).toContain("The fleet cannot be read right now");
    expect(html).not.toContain("Nothing needs you.");
  });

  it("groups the kinds the contract generates", () => {
    const html = view([
      { id: "sess-c", kind: constants.daemon.InboxInput, title: "", command: "", dir: "" },
      { id: "sess-d", kind: constants.daemon.InboxUnread, title: "", command: "", dir: "" },
    ]);
    expect(html).toContain("Needs your answer");
    expect(html).toContain(">Unread<");
  });
});
