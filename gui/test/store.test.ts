import { describe, expect, it } from "vitest";

import {
  addPermission,
  answerPermission,
  applyHistory,
  applyUpdate,
  endTurn,
  loseSession,
  newSession,
  commentPrompt,
  commentsOn,
  parseAgents,
  parseControls,
  parsePatch,
  parseStatus,
  splitArgs,
  startTurn,
  worktreeOf,
  summarise,
  needsAttention,
  usableAgents,
  filterAgents,
  withLifecycle,
  withControls,
  type Status,
} from "../src/store";

describe("parseStatus", () => {
  it("reads what the daemon sends", () => {
    const status = parseStatus({
      channel: "stable",
      pid: 42,
      ready: true,
      sessions: [
        {
          id: "sess-1",
          mode: "acp",
          host_pid: 7,
          command: "reasonix",
          dir: "C:\\work",
          reachable: true,
        },
      ],
    });

    expect(status.pid).toBe(42);
    expect(status.sessions[0]).toMatchObject({ id: "sess-1", mode: "acp", reachable: true });
    expect(summarise(status)).toEqual({ total: 1, reachable: 1 });
  });

  it("fills defaults instead of leaking undefined into the UI", () => {
    const status = parseStatus({ sessions: [{ id: "sess-2" }] });

    expect(status.sessions[0]).toEqual({
      id: "sess-2",
      mode: "pty",
      host_pid: 0,
      command: "",
      dir: "",
      // A session whose state the daemon did not send is unknown, never idle:
      // idle would claim it is ready for input.
      lifecycle: "unknown",
      reachable: false,
    });
    expect(status.failures).toEqual([]);
  });

  it("reads the lifecycle the daemon reports", () => {
    const status = parseStatus({
      sessions: [
        { id: "a", state: "blocked" },
        { id: "b", state: "done" },
        { id: "c", state: "working" },
        { id: "d", state: "idle" },
      ],
    });

    expect(status.sessions.map((session) => session.lifecycle)).toEqual([
      "blocked",
      "done",
      "working",
      "idle",
    ]);
  });

  it("turns a state it does not know into unknown, not done", () => {
    const status = parseStatus({
      sessions: [{ id: "a", state: "finished" }, { id: "b", state: 7 }],
    });

    expect(status.sessions.map((session) => session.lifecycle)).toEqual([
      "unknown",
      "unknown",
    ]);
  });

  it("survives a shape it did not expect", () => {
    expect(parseStatus(null)).toEqual({
      channel: "",
      pid: 0,
      ready: false,
      sessions: [],
      failures: [],
    });
  });
});

describe("splitArgs", () => {
  it("splits on spaces", () => {
    expect(splitArgs("acp --verbose")).toEqual(["acp", "--verbose"]);
  });

  it("keeps a quoted argument together", () => {
    expect(splitArgs('/c "echo hello & ping -n 3 127.0.0.1"')).toEqual([
      "/c",
      "echo hello & ping -n 3 127.0.0.1",
    ]);
  });

  it("ignores the spaces around the line", () => {
    expect(splitArgs("   acp   ")).toEqual(["acp"]);
    expect(splitArgs("")).toEqual([]);
  });

  it("keeps an empty quoted argument", () => {
    expect(splitArgs('--name ""')).toEqual(["--name", ""]);
  });
});

describe("parsePatch", () => {
  it("classifies the lines a renderer colours", () => {
    const patch = [
      "diff --git a/readme.md b/readme.md",
      "--- a/readme.md",
      "+++ b/readme.md",
      "@@ -1 +1,2 @@",
      " hello",
      "+world",
      "-gone",
    ].join("\n");

    expect(parsePatch(patch).map((line) => line.kind)).toEqual([
      "meta",
      "meta",
      "meta",
      "hunk",
      "context",
      "add",
      "del",
    ]);
    expect(parsePatch(patch)[5].text).toBe("world");
  });

  it("drops the trailing newline git leaves", () => {
    expect(parsePatch("+one\n")).toHaveLength(1);
  });

  it("does not count the no-newline marker as a line", () => {
    const patch = ["@@ -1,1 +1,2 @@", " kept", "+added", "\\ No newline at end of file"].join("\n");

    const lines = parsePatch(patch).filter((line) => line.kind === "add" || line.kind === "context");
    expect(lines.map((line) => [line.kind, line.line])).toEqual([
      ["context", 1],
      ["add", 2],
    ]);
  });

  it("numbers the lines a comment can anchor to", () => {
    const patch = [
      "diff --git a/readme.md b/readme.md",
      "@@ -10,3 +20,4 @@",
      " context",
      "+added",
      "-removed",
      " context again",
    ].join("\n");

    const lines = parsePatch(patch).filter((line) => line.kind !== "meta" && line.kind !== "hunk");
    expect(lines.map((line) => [line.kind, line.side, line.line])).toEqual([
      ["context", "new", 20],
      ["add", "new", 21],
      ["del", "old", 11],
      ["context", "new", 22],
    ]);
  });
});

describe("comments", () => {
  const notes = [
    { id: "1", path: "a.txt", line: 2, side: "new", text: "first", author: "user", at: "" },
    { id: "2", path: "a.txt", line: 2, side: "old", text: "second", author: "user", at: "" },
    { id: "3", path: "a.txt", line: 3, side: "new", text: "third", author: "user", at: "" },
  ];

  it("keeps the notes of one line apart from the other side's", () => {
    expect(commentsOn(notes, "a.txt", "new", 2).map((note) => note.id)).toEqual(["1"]);
    expect(commentsOn(notes, "a.txt", "old", 2).map((note) => note.id)).toEqual(["2"]);
    expect(commentsOn(notes, "b.txt", "new", 2)).toEqual([]);
  });

  it("renders notes as the text a prompt can carry", () => {
    expect(commentPrompt(notes)).toBe(
      ["a.txt:2 (new) — first", "a.txt:2 (old) — second", "a.txt:3 (new) — third"].join("\n"),
    );
  });
});

describe("worktreeOf", () => {
  it("asks for nothing when both fields are empty", () => {
    expect(worktreeOf("", "")).toEqual({});
  });

  it("asks for a worktree when both fields are filled", () => {
    expect(worktreeOf(" C:/repo ", " feat/x ")).toEqual({
      request: { repo: "C:/repo", branch: "feat/x" },
    });
  });

  it("refuses half a worktree instead of falling back", () => {
    expect(worktreeOf("C:/repo", "").error).toMatch(/both/);
    expect(worktreeOf("", "feat/x").error).toMatch(/both/);
  });
});

describe("session log", () => {
  it("joins the chunks of one streamed message", () => {
    let state = newSession("s", "acp");
    state = applyUpdate(state, { kind: "message", text: "fl" });
    state = applyUpdate(state, { kind: "message", text: "otilla-ok" });

    expect(state.entries).toHaveLength(1);
    expect(state.entries[0].text).toBe("flotilla-ok");
  });

  it("starts a new entry when the kind changes", () => {
    let state = newSession("s", "acp");
    state = applyUpdate(state, { kind: "message", text: "a" });
    state = applyUpdate(state, { kind: "thought", text: "b" });
    state = applyUpdate(state, { kind: "message", text: "c" });

    expect(state.entries.map((entry) => entry.kind)).toEqual([
      "message",
      "thought",
      "message",
    ]);
  });

  it("keeps the user's message as its own entry, ahead of the reply", () => {
    // The daemon stores the prompt before the turn runs, so it arrives first
    // and the agent's reply follows it.
    let state = newSession("s", "acp");
    state = applyUpdate(state, { seq: 1, kind: "user", text: "why" });
    state = applyUpdate(state, { seq: 2, kind: "message", text: "because" });
    state = applyUpdate(state, { seq: 3, kind: "user", text: "and then" });
    state = applyUpdate(state, { seq: 4, kind: "message", text: "that" });

    expect(state.entries.map((entry) => [entry.kind, entry.text])).toEqual([
      ["user", "why"],
      ["message", "because"],
      ["user", "and then"],
      ["message", "that"],
    ]);
  });

  it("does not merge a user message into a streamed reply", () => {
    // Both carry text, so a merge rule that keyed on text alone would run the
    // question into the answer as one paragraph.
    let state = newSession("s", "acp");
    state = applyUpdate(state, { kind: "message", text: "answer" });
    state = applyUpdate(state, { kind: "user", text: "question" });

    expect(state.entries.map((entry) => entry.kind)).toEqual(["message", "user"]);
    expect(state.entries[0].text).toBe("answer");
    expect(state.entries[1].text).toBe("question");
  });

  it("puts two prompts in a row in separate entries", () => {
    let state = newSession("s", "acp");
    state = applyUpdate(state, { kind: "user", text: "first" });
    state = applyUpdate(state, { kind: "user", text: "second" });

    expect(state.entries.map((entry) => entry.text)).toEqual(["first", "second"]);
  });

  it("joins the pieces of one message the wire had to split", () => {
    // cont is the wire's own statement that the piece belongs to the line
    // above; the kind alone cannot tell a split from a second message.
    let state = newSession("s", "acp");
    state = applyUpdate(state, { kind: "user", text: "a long prom" });
    state = applyUpdate(state, { kind: "user", text: "pt, split", cont: true });
    state = applyUpdate(state, { kind: "user", text: " on a frame", cont: true });

    expect(state.entries).toHaveLength(1);
    expect(state.entries[0].text).toBe("a long prompt, split on a frame");
  });

  it("does not join a continuation to a line of another kind", () => {
    // cont says the piece belongs to the line above, not that it belongs to
    // whatever the last entry happens to be: a split message that follows a
    // thought still starts its own line.
    let state = newSession("s", "acp");
    state = applyUpdate(state, { kind: "thought", text: "thinking" });
    state = applyUpdate(state, { kind: "message", text: "the reply", cont: true });

    expect(state.entries.map((entry) => entry.kind)).toEqual(["thought", "message"]);
    expect(state.entries[1].text).toBe("the reply");
  });

  it("ignores an update that carries nothing", () => {
    const state = applyUpdate(newSession("s", "acp"), { kind: "other" });
    expect(state.entries).toHaveLength(0);
  });

  it("updates a tool call in place instead of appending it twice", () => {
    let state = newSession("s", "acp");
    state = applyUpdate(state, {
      kind: "tool",
      tool_call_id: "call-1",
      tool_title: "reading",
      tool_status: "pending",
    });
    state = applyUpdate(state, {
      kind: "tool",
      tool_call_id: "call-1",
      tool_status: "completed",
    });

    expect(state.entries).toHaveLength(1);
    expect(state.entries[0]).toMatchObject({
      toolTitle: "reading",
      toolStatus: "completed",
    });
  });

  it("keeps one card per permission question", () => {
    let state = newSession("s", "acp");
    const question = { id: "perm-1", title: "edit config.json", kind: "edit", options: ["allow"] };

    state = addPermission(state, question);
    state = addPermission(state, question);
    expect(state.permissions).toHaveLength(1);

    state = answerPermission(state, "perm-1");
    expect(state.permissions).toHaveLength(0);
  });

  it("tracks the turn and the loss of a session", () => {
    let state = newSession("s", "acp");
    state = startTurn(state);
    expect(state.turn).toBe("working");

    state = endTurn(state, "end_turn");
    expect(state).toMatchObject({ turn: "idle", stopReason: "end_turn" });

    state = loseSession(state, "host gone");
    expect(state).toMatchObject({ turn: "lost", error: "host gone" });
  });

  it("folds the history a window is given when it attaches", () => {
    const state = applyHistory(newSession("s", "acp"), {
      updates: [
        { seq: 1, kind: "message", text: "reading" },
        { seq: 2, kind: "tool", tool_call_id: "call-1", tool_title: "grep" },
      ],
      more: false,
    });

    expect(state.entries.map((entry) => entry.kind)).toEqual(["message", "tool"]);
    expect(state.lastSeq).toBe(2);
    expect(state.moreHistory).toBe(false);
  });

  it("says so when the history starts after the session did", () => {
    const state = applyHistory(newSession("s", "acp"), {
      updates: [{ seq: 40, kind: "message", text: "still working" }],
      more: true,
    });

    expect(state.moreHistory).toBe(true);
    expect(state.lastSeq).toBe(40);
  });

  it("folds a replayed update once", () => {
    // A reattach hands the window the stored log and then live traffic from
    // before it attached. Folding both would double the paragraph.
    let state = applyHistory(newSession("s", "acp"), {
      updates: [{ seq: 1, kind: "message", text: "hel" }],
    });
    state = applyUpdate(state, { seq: 1, kind: "message", text: "hel" });
    state = applyUpdate(state, { seq: 2, kind: "message", text: "lo" });

    expect(state.entries).toHaveLength(1);
    expect(state.entries[0].text).toBe("hello");
    // The fold is not ordered by arrival: an update older than the newest one
    // already in the log is one the window has.
    state = applyUpdate(state, { seq: 1, kind: "thought", text: "old" });
    expect(state.entries).toHaveLength(1);
  });

  it("keeps folding updates that carry no sequence", () => {
    // The host says nothing about sequence; only the daemon numbers an update.
    // A window talking to a host directly must still show the log.
    let state = newSession("s", "acp");
    state = applyUpdate(state, { kind: "message", text: "a" });
    state = applyUpdate(state, { kind: "message", text: "b" });

    expect(state.entries[0].text).toBe("ab");
    expect(state.lastSeq).toBe(0);
  });
});

describe("fleet lifecycle", () => {
  function fleet(): Status {
    return {
      channel: "stable",
      pid: 1,
      ready: true,
      failures: [],
      sessions: [
        {
          id: "a",
          mode: "acp",
          host_pid: 10,
          command: "kimi",
          dir: "/w",
          lifecycle: "idle",
          reachable: true,
        },
        {
          id: "b",
          mode: "acp",
          host_pid: 11,
          command: "kimi",
          dir: "/w",
          lifecycle: "idle",
          reachable: true,
        },
      ],
    };
  }

  it("updates one session without touching the others", () => {
    const next = withLifecycle(fleet(), "b", "blocked");

    // The fleet is ordered by attention: a newly blocked session rises to the
    // top, and the row itself survives the move intact.
    expect(next?.sessions.map((s) => s.lifecycle)).toEqual(["blocked", "idle"]);
    expect(next?.sessions[0]).toMatchObject({ id: "b", host_pid: 11, reachable: true });
  });

  it("returns the same fleet when nothing changed", () => {
    const current = fleet();
    // Identity matters: React re-renders on a new object, and most state
    // notifications repeat the state the row already shows.
    expect(withLifecycle(current, "a", "idle")).toBe(current);
  });

  it("ignores a state for a session this fleet does not list", () => {
    const current = fleet();
    expect(withLifecycle(current, "ghost", "done")).toBe(current);
    expect(withLifecycle(current, "ghost", "done")?.sessions).toHaveLength(2);
  });

  it("does nothing before the fleet has arrived", () => {
    expect(withLifecycle(null, "a", "done")).toBeNull();
  });

  it("treats a value it cannot classify as unknown", () => {
    const next = withLifecycle(fleet(), "a", "finished");
    expect(next?.sessions[0].lifecycle).toBe("unknown");
  });

  it("says which states ask something of the user", () => {
    expect(needsAttention("blocked")).toBe(true);
    expect(needsAttention("done")).toBe(true);
    // unknown is never treated as complete, so it is not an invitation to look.
    expect(needsAttention("unknown")).toBe(false);
    expect(needsAttention("idle")).toBe(false);
    expect(needsAttention("working")).toBe(false);
  });
});

function agentEntries(): ReturnType<typeof parseAgents>["agents"] {
  return parseAgents({
    agents: [
      {
        id: "reasonix",
        name: "Reasonix Studio",
        command: "reasonix",
        args: ["acp"],
        mode: "acp",
        installed: true,
        enabled: true,
        chat: true,
        cli: false,
      },
      {
        id: "gemini",
        name: "Gemini CLI",
        command: "gemini",
        args: ["--acp"],
        mode: "acp",
        installed: false,
        enabled: true,
        chat: true,
        cli: false,
      },
      {
        id: "my-shell",
        name: "My Shell",
        command: "pwsh",
        mode: "pty",
        installed: true,
        enabled: true,
        chat: false,
        cli: true,
      },
      {
        id: "off",
        name: "Turned Off",
        command: "off",
        installed: true,
        enabled: false,
        chat: true,
        cli: true,
      },
    ],
  }).agents;
}

describe("parseAgents", () => {
  it("reads the daemon's listing and keeps its order", () => {
    const listing = parseAgents({
      agents: [
        {
          id: "reasonix",
          name: "Reasonix Studio",
          command: "reasonix",
          args: ["acp"],
          mode: "acp",
          needs: "a login",
          source: "builtin",
          installed: true,
          path: "C:\\bin\\reasonix.exe",
          enabled: true,
          chat: true,
          cli: false,
        },
      ],
      problems: ["adapter \"broken.json\": empty command"],
    });

    expect(listing.agents).toHaveLength(1);
    expect(listing.agents[0]).toMatchObject({
      id: "reasonix",
      args: ["acp"],
      installed: true,
      path: "C:\\bin\\reasonix.exe",
      chat: true,
      cli: false,
    });
    expect(listing.problems).toEqual(["adapter \"broken.json\": empty command"]);
  });

  it("fills what the daemon left out with conservative values", () => {
    const listing = parseAgents({ agents: [{ id: "x", command: "x" }] });

    // Nothing says this agent can be started, so the picker must not offer it.
    expect(listing.agents[0]).toMatchObject({
      id: "x",
      name: "x",
      mode: "acp",
      source: "builtin",
      installed: false,
      enabled: false,
      chat: false,
      cli: false,
    });
    expect(listing.problems).toEqual([]);
  });

  it("survives a reply that is not a listing", () => {
    expect(parseAgents(null)).toEqual({ agents: [], problems: [] });
    expect(parseAgents({ agents: "nope" }).agents).toEqual([]);
  });
});

describe("filterAgents", () => {
  const agents = agentEntries();

  it("keeps the daemon's order for an empty query", () => {
    expect(filterAgents(agents, "  ").map((agent) => agent.id)).toEqual([
      "reasonix",
      "gemini",
      "my-shell",
      "off",
    ]);
  });

  it("matches the name, the id and the command without case", () => {
    expect(filterAgents(agents, "GEM").map((agent) => agent.id)).toEqual(["gemini"]);
    // A command someone already knows is a name they will type.
    expect(filterAgents(agents, "pwsh").map((agent) => agent.id)).toEqual(["my-shell"]);
  });

  it("requires every word the user typed", () => {
    expect(filterAgents(agents, "gemini cli").map((agent) => agent.id)).toEqual(["gemini"]);
    expect(filterAgents(agents, "gemini shell")).toEqual([]);
  });

  it("returns a copy so a caller cannot reorder the daemon's list", () => {
    const kept = filterAgents(agents, "");
    kept.reverse();
    expect(agents[0].id).toBe("reasonix");
  });
});

describe("usableAgents", () => {
  const agents = agentEntries();

  it("offers only what is installed, enabled and on for the session mode", () => {
    expect(usableAgents(agents, "acp").map((agent) => agent.id)).toEqual(["reasonix"]);
    expect(usableAgents(agents, "pty").map((agent) => agent.id)).toEqual(["my-shell"]);
  });

  it("never offers an agent the user turned off", () => {
    // "off" is installed and on for both purposes, but disabled wins.
    expect(usableAgents(agents, "acp").map((agent) => agent.id)).not.toContain("off");
  });

  it("never offers a preset in the protocol its args do not speak", () => {
    // The switch alone is not enough: a preset's args are written for its own
    // mode, so offering a pty preset to an acp session would contradict them.
    const ptyOnly = agents.map((agent) =>
      agent.id === "my-shell" ? { ...agent, chat: true } : agent,
    );
    expect(usableAgents(ptyOnly, "acp").map((agent) => agent.id)).not.toContain("my-shell");
  });
});

describe("surface controls", () => {
  it("reads the controls the daemon projected", () => {
    const controls = parseControls([
      {
        id: "mode",
        name: "Mode",
        kind: "mode",
        current: "code",
        choices: [
          { value: "plan", name: "Plan" },
          { value: "code", name: "Code" },
        ],
      },
      { id: "toggle", name: "Toggle", kind: "boolean", current: "true" },
    ]);

    expect(controls).toHaveLength(2);
    expect(controls[0].choices?.map((choice) => choice.value)).toEqual(["plan", "code"]);
    expect(controls[1]).toEqual({
      id: "toggle",
      name: "Toggle",
      kind: "boolean",
      current: "true",
      choices: [],
    });
  });

  it("keeps a grouped choice's group label", () => {
    const controls = parseControls([
      {
        id: "model",
        name: "Model",
        kind: "select",
        current: "a",
        choices: [{ value: "a", name: "A", group: "Fast" }],
      },
    ]);
    expect(controls[0].choices?.[0].group).toBe("Fast");
  });

  it("does not trust a malformed list", () => {
    expect(parseControls(null)).toEqual([]);
    expect(parseControls("nope")).toEqual([]);
    expect(parseControls([null, {}])).toEqual([
      { id: "", name: "", kind: "", current: "", choices: [] },
      { id: "", name: "", kind: "", current: "", choices: [] },
    ]);
  });

  it("replaces the surface wholesale on an event", () => {
    // A surface event carries the whole list, so a control the agent stopped
    // offering disappears rather than lingering with a stale value.
    let state = newSession("s", "acp");
    expect(state.controls).toEqual([]);

    state = withControls(state, [{ id: "mode", name: "Mode", kind: "mode", current: "code" }]);
    state = withControls(state, [{ id: "mode", name: "Mode", kind: "mode", current: "plan" }]);
    expect(state.controls).toHaveLength(1);
    expect(state.controls[0].current).toBe("plan");
  });
});

describe("fleet order", () => {
  it("sorts by attention: blocked, done, unknown, working, idle", () => {
    const status = parseStatus({
      sessions: [
        { id: "i", state: "idle" },
        { id: "w", state: "working" },
        { id: "u" },
        { id: "d", state: "done" },
        { id: "b", state: "blocked" },
      ],
    });

    expect(status.sessions.map((s) => s.id)).toEqual(["b", "d", "u", "w", "i"]);
  });

  it("keeps the daemon's order within one state", () => {
    const status = parseStatus({
      sessions: [
        { id: "b2", state: "blocked" },
        { id: "i1", state: "idle" },
        { id: "b1", state: "blocked" },
        { id: "i2", state: "idle" },
      ],
    });

    expect(status.sessions.map((s) => s.id)).toEqual(["b2", "b1", "i1", "i2"]);
  });

  it("re-sorts when a lifecycle change moves a session's rank", () => {
    const current = parseStatus({
      sessions: [
        { id: "a", state: "done" },
        { id: "b", state: "working" },
      ],
    });

    const next = withLifecycle(current, "b", "blocked");
    expect(next?.sessions.map((s) => s.id)).toEqual(["b", "a"]);

    // Sinking is the same rule: an idle session drops below a finished turn.
    const after = withLifecycle(next, "b", "idle");
    expect(after?.sessions.map((s) => s.id)).toEqual(["a", "b"]);
  });
});
