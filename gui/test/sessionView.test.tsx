import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { SessionView } from "../src/components/SessionView";
import type { Control, Entry, Lifecycle, QueuedMessage, SessionState } from "../src/store";

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
    queue: [],
    subagents: [],
    ...over,
  };
}

function queued(id: string, text: string, error = ""): QueuedMessage {
  return { id, text, error };
}

function render(
  s: SessionState,
  view: "stream" | "diff" = "stream",
  lifecycle: Lifecycle = "idle",
  protectedSession = false,
  steerEnabled = false,
): string {
  return renderToStaticMarkup(
    <SessionView
      state={s}
      lifecycle={lifecycle}
      protected={protectedSession}
      steer={steerEnabled}
      view={view}
      onView={() => {}}
      onPrompt={() => {}}
      onCancel={() => {}}
      onSetControl={() => {}}
      onAnswer={() => {}}
      onQueueEdit={() => {}}
      onQueueRemove={() => {}}
      onQueueMove={() => {}}
      onProtect={() => {}}
      onTerminalReady={() => {}}
      onInput={() => {}}
      onResize={() => {}}
    />,
  );
}

describe("the session chrome keeps the smoke contract", () => {
  it("names the session in the header h2 and keeps the mode line", () => {
    const html = render(state({ turn: "lost" }));
    expect(html).toContain("<h2>sess-x</h2>");
    // The header names the protocol in a person's word, not "acp": the wire's
    // spelling is not what a window shows.
    expect(html).toContain("Chat");
    expect(html).not.toContain(">acp<");
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
    // The header line no longer repeats the raw turn word next to the pill:
    // the pill is the state's single reading, and "Chat" is the protocol.
    expect(html).not.toContain("· working");
    expect(html).toContain("Chat");
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

  it("offers to queue while a turn is in flight instead of disabling Send", () => {
    const html = render(state({ turn: "working" }));
    expect(html).not.toContain("Enter to send");
    expect(html).toContain("Send queues · the next turn starts when this one ends");
    expect(html).toMatch(/Queue<\/button>/);
  });

  it("offers to steer while a turn runs when the agent advertised it", () => {
    const html = render(state({ turn: "working" }), "stream", "idle", false, true);
    expect(html).toContain("Steer · the agent takes this into the running turn");
    expect(html).toMatch(/Steer<\/button>/);
  });

  it("lets Cancel reach a turn the daemon started from the queue", () => {
    // The queue delivers its first message with no call from this window, so
    // the fleet's lifecycle is the only thing that says the agent is running.
    const busy = render(state(), "stream", "working");
    expect(busy).toMatch(/Queue<\/button>/);
    expect(busy).not.toMatch(/disabled=""[^>]*>Cancel turn/);

    const quiet = render(state(), "stream", "idle");
    expect(quiet).toMatch(/disabled=""[^>]*>Cancel turn/);
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

describe("the queue strip above the composer", () => {
  it("lists the waiting messages in delivery order", () => {
    const html = render(
      state({ queue: [queued("q1", "first queued"), queued("q2", "second queued")] }),
    );
    expect(html).toContain("Queued for the next turn");
    expect(html.indexOf("first queued")).toBeGreaterThan(-1);
    expect(html.indexOf("first queued")).toBeLessThan(html.indexOf("second queued"));
    expect(html.indexOf("session-queue")).toBeLessThan(html.indexOf("session-composer"));
  });

  it("shows why a delivery failed instead of hiding the message", () => {
    const html = render(state({ queue: [queued("q1", "try again", "host_unreachable")] }));
    expect(html).toContain("try again");
    expect(html).toContain("the host is unreachable");
  });

  it("keeps an unknown failure visible as it came", () => {
    expect(render(state({ queue: [queued("q1", "odd", "something_new")] }))).toContain(
      "something_new",
    );
  });

  it("offers edit, remove and move on each row, with the ends pinned", () => {
    const html = render(
      state({ queue: [queued("q1", "first queued"), queued("q2", "second queued")] }),
    );
    expect(html).toContain(">Edit</button>");
    expect(html).toContain(">Remove</button>");
    expect(html).toMatch(/aria-label="Move queued message 1 up"[^>]*disabled/);
    expect(html).not.toMatch(/aria-label="Move queued message 1 down"[^>]*disabled/);
    expect(html).toMatch(/aria-label="Move queued message 2 down"[^>]*disabled/);
  });

  it("hides the strip when nothing waits", () => {
    expect(render(state())).not.toContain("session-queue");
  });
});

describe("the protect toggle", () => {
  it("offers to pin the session against idle reclamation", () => {
    const html = render(state());
    expect(html).toContain(">Protect</button>");
    expect(html).toContain('aria-pressed="false"');
  });

  it("shows a pinned session as pressed with the word a product uses", () => {
    const html = render(state(), "stream", "idle", true);
    expect(html).toContain('aria-pressed="true"');
    expect(html).toContain(">Protected</button>");
  });
});

describe("the subagent strip", () => {
  it("lists the child sessions an agent registered, with their work state", () => {
    const html = render(
      state({
        subagents: [
          {
            id: "child-1",
            parentId: "sess-x",
            title: "Test investigator",
            description: "Investigates.",
            state: "running",
          },
          {
            id: "grand-1",
            parentId: "child-1",
            title: "Helper",
            description: "",
            state: "idle",
          },
        ],
      }),
    );

    expect(html).toContain("Subagents");
    expect(html).toContain("Test investigator");
    expect(html).toContain('title="Investigates."');
    expect(html).toContain("working");
    expect(html).toContain("Helper");
    expect(html).toContain("idle");
    expect(html.indexOf("Test investigator")).toBeLessThan(html.indexOf("Helper"));
  });

  it("keeps an unknown state visible as it came", () => {
    const html = render(
      state({
        subagents: [
          {
            id: "child-1",
            parentId: "sess-x",
            title: "",
            description: "",
            state: "queued_v2",
          },
        ],
      }),
    );
    expect(html).toContain("child-1");
    expect(html).toContain("queued_v2");
  });

  it("hides the strip when no child was registered", () => {
    expect(render(state())).not.toContain("session-subagents");
  });
});
