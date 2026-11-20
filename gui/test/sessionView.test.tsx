import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { SessionView } from "../src/components/SessionView";
import type { Entry, SessionState } from "../src/store";

function entry(key: string, kind: Entry["kind"], toolTitle?: string): Entry {
  return { key, kind, text: `${kind} ${key}`, toolTitle };
}

function state(over: Partial<SessionState> = {}): SessionState {
  return {
    id: "sess-x",
    mode: "acp",
    entries: [
      entry("a", "user"),
      entry("t1", "tool", "Read"),
      entry("t2", "tool", "Edit"),
      entry("t3", "tool", "Read"),
      entry("c", "message"),
    ],
    permissions: [],
    controls: [],
    turn: "idle",
    lastSeq: 0,
    moreHistory: false,
    ...over,
  };
}

function render(s: SessionState, view: "stream" | "diff" = "stream"): string {
  return renderToStaticMarkup(
    <SessionView
      state={s}
      view={view}
      onView={() => {}}
      onPrompt={() => {}}
      onCancel={() => {}}
      onSetControl={() => {}}
      onAnswer={() => {}}
      onTerminalReady={() => {}}
      onInput={() => {}}
      onResize={() => {}}
    />,
  );
}

describe("the session chrome keeps the smoke contract", () => {
  it("names the session in the header h2 and keeps the mode/turn line", () => {
    const html = render(state({ turn: "lost" }));
    expect(html).toContain("<h2>sess-x</h2>");
    expect(html).toContain("· lost");
    expect(html).toContain("Cancel turn");
    expect(html).toContain("Diff");
  });

  it("keeps Send a text button and the composer textarea", () => {
    const html = render(state());
    expect(html).toMatch(/Send<\/button>/);
    expect(html).toMatch(/<textarea/);
  });

  it("offers Terminal rather than Log for a pty session", () => {
    const html = render(state({ mode: "pty", entries: [] }));
    expect(html).toContain("Terminal");
  });
});

describe("the session chrome reads as a product", () => {
  it("wears a turn state pill beside the session name", () => {
    const html = render(state({ turn: "working" }));
    expect(html).toMatch(/class="turn-pill[^"]*"/);
    expect(html).toContain("working");
  });

  it("folds a run of tool calls into one expandable row that still names them", () => {
    const html = render(state());
    expect(html).toMatch(/<details/);
    // The summary itself must carry the count and the objects: the body's lines
    // would satisfy a loose needle even if the summary said nothing.
    expect(html).toContain('class="run-verb">3 tool calls</span>');
    expect(html).toContain('class="chip run-object mono">Read</span>');
    expect(html).toContain('class="chip run-object mono">Edit</span>');
  });

  it("gives the composer a toolbar with an accurate send hint", () => {
    const html = render(state());
    expect(html).toContain("Enter to send");
    expect(html).toMatch(/class="session-composer"/);
  });

  it("does not promise Enter sends while a turn is in flight", () => {
    const html = render(state({ turn: "working" }));
    expect(html).not.toContain("Enter to send");
    expect(html).toContain("Send unlocks when the turn ends");
  });

  it("shows a permission shield count only while a question is pending", () => {
    const quiet = render(state());
    expect(quiet).not.toMatch(/composer-shield/);
    const pending = render(
      state({
        permissions: [{ id: "p1", title: "Run command?", kind: "execute", options: [] }],
      }),
    );
    expect(pending).toMatch(/composer-shield/);
  });
});