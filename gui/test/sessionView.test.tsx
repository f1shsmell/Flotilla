import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { SessionView } from "../src/components/SessionView";
import type { Control, Entry, Lifecycle, SessionState } from "../src/store";

function entry(key: string, kind: Entry["kind"], toolTitle?: string): Entry {
  return { key, kind, text: `${kind} ${key}`, toolTitle };
}

function modelControl(): Control {
  return {
    id: "model",
    name: "Model",
    kind: "select",
    current: "sonnet",
    choices: [
      { value: "sonnet", name: "Sonnet" },
      { value: "opus", name: "Opus" },
    ],
  };
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

function render(
  s: SessionState,
  view: "stream" | "diff" = "stream",
  lifecycle: Lifecycle = "idle",
): string {
  return renderToStaticMarkup(
    <SessionView
      state={s}
      lifecycle={lifecycle}
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
  it("wears the fleet's lifecycle word in the pill, not the stream's turn", () => {
    const html = render(state({ turn: "working" }), "stream", "blocked");
    expect(html).toContain('class="turn-pill life-blocked"');
    // The pill carries the lifecycle word in the session header; the fleet
    // card repeats it in nav context, so both readings are wanted.
    expect(html).toContain('class="turn-pill life-blocked">blocked</span>');
    expect(html).toMatch(/life-blocked"[^>]*>blocked</);
    // The stream's turn still reads in the header line the heal probe uses.
    expect(html).toContain("· working");
  });

  it("colours done and idle with the fleet's own palette", () => {
    expect(render(state(), "stream", "done")).toContain('class="turn-pill life-done"');
    expect(render(state(), "stream", "idle")).toContain('class="turn-pill life-idle"');
    expect(render(state(), "stream", "unknown")).toContain('class="turn-pill life-unknown"');
  });

  it("names each setting select for assistive tech and the smoke harness", () => {
    const html = render(state({ controls: [modelControl()] }));
    expect(html).toContain('aria-label="Model"');
  });

  it("keeps the settings bar below the input, inside the composer", () => {
    const html = render(state({ controls: [modelControl()] }));
    const composer = html.indexOf('class="session-composer"');
    expect(composer).toBeGreaterThan(-1);
    // The composer is the view's last block, so its close is the first
    // </div></div> after it opens: the select must sit inside that span.
    const inside = html.slice(composer);
    const close = inside.indexOf("</div></div>");
    const field = inside.indexOf("<textarea");
    const select = inside.indexOf('aria-label="Model"');
    expect(field).toBeGreaterThan(-1);
    expect(select).toBeGreaterThan(field);
    expect(select).toBeLessThan(close);
    expect(html.slice(0, composer)).not.toContain('aria-label="Model"');
  });

  it("names each select on hover, with the value a truncation would hide", () => {
    const html = render(state({ controls: [modelControl()] }));
    expect(html).toContain('title="Model: Sonnet"');
  });

  it("marks the permission dial apart from the other ghost selects", () => {
    const html = render(
      state({
        controls: [{ ...modelControl(), id: "mode", name: "Permission mode", kind: "mode" }],
      }),
    );
    expect(html).toContain('class="surface-control kind-mode"');
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

  it("gives the composer a bar with an accurate send hint", () => {
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
describe("the composer dials carry their names and honest state", () => {
  it("shows each dial's name as visible text, not only a tooltip", () => {
    const html = render(state({ controls: [modelControl()] }));
    expect(html).toContain('class="surface-control-name">Model<');
  });

  it("keeps two dials that share a value distinguishable", () => {
    const html = render(
      state({
        controls: [
          modelControl(),
          { id: "voice", name: "Voice style", kind: "select", current: "sonnet", choices: modelControl().choices },
        ],
      }),
    );
    expect(html).toContain(">Voice style<");
    expect(html).toContain(">Model<");
  });

  it("keeps a current the agent did not list visible and marked", () => {
    const html = render(
      state({
        controls: [{ id: "theme", name: "Theme", kind: "select", current: "light", choices: [{ value: "dark", name: "Dark" }] }],
      }),
    );
    expect(html).toContain('value="light" disabled');
    expect(html).toContain("(current)");
  });

  it("renders a control with no alternatives as a read-only chip", () => {
    const html = render(
      state({
        controls: [{ id: "theme", name: "Theme", kind: "select", current: "light", choices: [] }],
      }),
    );
    expect(html).toContain("surface-control-static");
    expect(html).toContain("Theme: light");
    expect(html).not.toContain("<select");
  });
});
