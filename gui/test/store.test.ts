import { describe, expect, it, vi } from "vitest";

import { constants } from "../electron/contract";
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
  parseCheckpoints,
  parseControls,
  parseMessageQueue,
  parsePatch,
  parseStatus,
  splitArgs,
  startTurn,
  worktreeOf,
  worktreeRequest,
  defaultBranch,
  modeForAgent,
  defaultAgent,
  summarise,
  needsAttention,
  usableAgents,
  filterAgents,
  filterRegistry,
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
          protected: true,
          steer: true,
        },
      ],
    });

    expect(status.pid).toBe(42);
    expect(status.sessions[0]).toMatchObject({
      id: "sess-1",
      mode: "acp",
      reachable: true,
      protected: true,
      steer: true,
    });
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
      protected: false,
      steer: false,
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

  it("reads the queue apart from the fleet", () => {
    const status = parseStatus({
      sessions: [{ id: "sess-1", state: "working", reachable: true }],
      tasks: [
        {
          id: "task-1",
          dialect: "claude",
          state: "queued",
          prompt: "summarise the branch",
          dir: "C:\\work",
          runs: 2,
          spent_usd: 0.25,
          budget_usd: 2,
          last_error: "the agent exited 1",
        },
      ],
    });

    // A task never becomes a session: the fleet list must not offer to attach to
    // something with no host, even when a task is in the same report.
    expect(status.sessions).toHaveLength(1);
    expect(status.tasks).toHaveLength(1);
    expect(status.tasks[0]).toEqual({
      id: "task-1",
      dialect: "claude",
      state: "queued",
      prompt: "summarise the branch",
      dir: "C:\\work",
      runs: 2,
      spent_usd: 0.25,
      budget_usd: 2,
      last_error: "the agent exited 1",
    });
  });

  it("fills a task's blanks rather than leaking undefined", () => {
    const status = parseStatus({ tasks: [{ id: "task-2" }] });

    expect(status.tasks[0]).toEqual({
      id: "task-2",
      dialect: "",
      // A state the daemon did not send is named unknown rather than mapped onto
      // a lifecycle: a task is not a session, and "idle" would claim the queue
      // is empty.
      state: "unknown",
      prompt: "",
      dir: "",
      runs: 0,
      spent_usd: 0,
      budget_usd: 0,
      last_error: "",
    });
  });

  it("drops a task with no id instead of listing a row that cannot be named", () => {
    const status = parseStatus({ tasks: [{ prompt: "orphan" }, { id: "task-3" }] });

    expect(status.tasks.map((task) => task.id)).toEqual(["task-3"]);
  });

  it("reads the inbox apart from the fleet and drops rows it cannot open", () => {
    const status = parseStatus({
      inbox: [
        {
          id: "sess-a",
          kind: constants.daemon.InboxInput,
          title: "edit config.json",
          command: "claude-agent-acp",
          dir: "C:\\work",
        },
        { id: "sess-b", kind: constants.daemon.InboxUnread },
        { id: "", kind: constants.daemon.InboxUnread },
        { id: "sess-c", kind: "finished" },
      ],
    });

    expect(status.inbox).toEqual([
      {
        id: "sess-a",
        kind: constants.daemon.InboxInput,
        title: "edit config.json",
        command: "claude-agent-acp",
        dir: "C:\\work",
      },
      // A kind this build does not know is not guessed onto a heading: the
      // entry is dropped, and the session stays reachable from the fleet.
      { id: "sess-b", kind: constants.daemon.InboxUnread, title: "", command: "", dir: "" },
    ]);
  });

  it("keeps the kinds the contract generates", () => {
    const status = parseStatus({
      inbox: [
        { id: "sess-a", kind: constants.daemon.InboxInput },
        { id: "sess-b", kind: constants.daemon.InboxUnread },
      ],
    });

    expect(status.inbox?.map((entry) => entry.kind)).toEqual([
      constants.daemon.InboxInput,
      constants.daemon.InboxUnread,
    ]);
  });

  it("survives a shape it did not expect", () => {
    expect(parseStatus(null)).toEqual({
      channel: "",
      pid: 0,
      ready: false,
      sessions: [],
      archived: [],
      tasks: [],
      inbox: [],
      failures: [],
    });
  });
});

describe("parseMessageQueue", () => {
  it("keeps the daemon's order, text and delivery failure", () => {
    expect(
      parseMessageQueue({
        messages: [
          { id: "q1", text: "first" },
          { id: "q2", text: "second", error: "host_unreachable" },
        ],
      }),
    ).toEqual([
      { id: "q1", text: "first", error: "" },
      { id: "q2", text: "second", error: "host_unreachable" },
    ]);
  });

  it("drops a row that could not be edited or removed", () => {
    expect(parseMessageQueue({ messages: [{ text: "no id" }, { id: "q1", text: "kept" }] })).toEqual([
      { id: "q1", text: "kept", error: "" },
    ]);
  });

  it("reads an absent queue as empty", () => {
    expect(parseMessageQueue(undefined)).toEqual([]);
    expect(parseMessageQueue({ messages: "not a list" })).toEqual([]);
  });
});

describe("parseCheckpoints", () => {
  it("keeps the daemon's turns and commits", () => {
    expect(
      parseCheckpoints({
        checkpoints: [
          { turn: 1, commit: "abc123", at: "2026-01-01T10:00:00Z" },
          { turn: 2, commit: "def456", at: "2026-01-01T10:05:00Z" },
        ],
      }),
    ).toEqual([
      { turn: 1, commit: "abc123", at: "2026-01-01T10:00:00Z" },
      { turn: 2, commit: "def456", at: "2026-01-01T10:05:00Z" },
    ]);
  });

  it("drops a row that could not be restored", () => {
    expect(
      parseCheckpoints({ checkpoints: [{ commit: "abc" }, { turn: 3, commit: "def" }] }),
    ).toEqual([{ turn: 3, commit: "def", at: "" }]);
  });

  it("reads an absent list as empty", () => {
    expect(parseCheckpoints(undefined)).toEqual([]);
    expect(parseCheckpoints({ checkpoints: "not a list" })).toEqual([]);
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

describe("worktreeRequest", () => {
  const now = new Date(2026, 10, 24, 9, 5);

  it("names a branch when only the repository was given", () => {
    // A worktree needs a new branch, and the form already knows that: making
    // the user type one is a question with a right answer.
    expect(worktreeRequest("C:/repo", "", now)).toEqual({
      request: { repo: "C:/repo", branch: "feat/20261124-0905" },
    });
  });

  it("keeps a branch the user named", () => {
    expect(worktreeRequest("C:/repo", "feat/mine", now)).toEqual({
      request: { repo: "C:/repo", branch: "feat/mine" },
    });
  });

  it("still refuses a branch with no repository", () => {
    expect(worktreeRequest("", "feat/x", now).error).toMatch(/both/);
  });

  it("asks for nothing when neither field is filled", () => {
    expect(worktreeRequest("", "", now)).toEqual({});
  });

  it("pads a single-digit month, day, hour and minute", () => {
    expect(defaultBranch(new Date(2026, 0, 2, 3, 4))).toBe("feat/20260102-0304");
  });
});

describe("modeForAgent", () => {
  it("runs the agent in the mode its own recipe declares", () => {
    // A preset is one complete recipe, so its mode is settled by choosing it.
    expect(modeForAgent({ mode: "pty" })).toBe("pty");
    expect(modeForAgent({ mode: "acp" })).toBe("acp");
  });

  it("has no opinion with no agent chosen", () => {
    expect(modeForAgent(null)).toBeNull();
  });
});

describe("defaultAgent", () => {
  it("picks the only startable agent, because there is nothing to decide", () => {
    expect(defaultAgent([{ id: "replay" }])).toBe("replay");
  });

  it("leaves several agents as a question", () => {
    // Two candidates means the user has a preference the form cannot know.
    expect(defaultAgent([{ id: "a" }, { id: "b" }])).toBeNull();
  });

  it("picks nothing when nothing can start", () => {
    expect(defaultAgent([])).toBeNull();
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
    const question = {
      id: "perm-1",
      title: "edit config.json",
      kind: "edit",
      options: [{ id: "allow", kind: "allow_once" }],
    };

    state = addPermission(state, question);
    state = addPermission(state, question);
    expect(state.permissions).toHaveLength(1);

    state = answerPermission(state, "perm-1");
    expect(state.permissions).toHaveLength(0);
  });

  it("keeps each permission option's kind beside its id", () => {
    let state = newSession("s", "acp");
    state = addPermission(state, {
      id: "perm-1",
      title: "edit config.json",
      kind: "edit",
      options: [
        { id: "approve", kind: "allow_once" },
        { id: "allow-me", kind: "reject_once" },
      ],
    });

    expect(state.permissions[0].options).toEqual([
      { id: "approve", kind: "allow_once", name: "" },
      { id: "allow-me", kind: "reject_once", name: "" },
    ]);
  });

  it("still answers a question from an older daemon that sends bare ids", () => {
    let state = newSession("s", "acp");
    state = addPermission(state, {
      id: "perm-1",
      title: "edit config.json",
      kind: "edit",
      options: ["allow", "reject"],
    });

    expect(state.permissions[0].options).toEqual([
      { id: "allow", kind: "", name: "" },
      { id: "reject", kind: "", name: "" },
    ]);
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

  it("records a hole in the sequence instead of accepting it silently", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    try {
      let state = newSession("s", "acp");
      state = applyUpdate(state, { seq: 5, kind: "message", text: "before the hole" });
      state = applyUpdate(state, { seq: 8, kind: "user", text: "after it" });

      expect(warn).toHaveBeenCalledTimes(1);
      const said = warn.mock.calls.map((call) => call.join(" ")).join("\n");
      expect(said).toContain("6");
      expect(said).toContain("8");
      // A hole is a fact about the log, not a reason to drop what arrived.
      expect(state.entries.map((entry) => entry.text)).toEqual([
        "before the hole",
        "after it",
      ]);
      expect(state.lastSeq).toBe(8);
    } finally {
      warn.mockRestore();
    }
  });

  it("records nothing when the sequence has no hole", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    try {
      let state = newSession("s", "acp");
      // The first record of an attach starts mid-log, so a seq past 1 is not
      // a hole; contiguity from there is.
      state = applyHistory(state, {
        updates: [{ seq: 40, kind: "message", text: "mid-turn" }],
      });
      state = applyUpdate(state, { seq: 41, kind: "message", text: "still going" });

      expect(warn).not.toHaveBeenCalled();
      expect(state.lastSeq).toBe(41);
    } finally {
      warn.mockRestore();
    }
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

describe("subagent registrations", () => {
  it("registers a child with its title and work state, off the conversation", () => {
    let state = newSession("s", "acp");
    state = applyUpdate(state, {
      kind: "subagent",
      session_id: "s",
      subagent_id: "child-1",
      subagent_title: "Test investigator",
      subagent_description: "Investigates.",
      subagent_state: "running",
    });

    expect(state.subagents).toEqual([
      {
        id: "child-1",
        parentId: "s",
        title: "Test investigator",
        description: "Investigates.",
        state: "running",
      },
    ]);
    // A registration is metadata about the child, not a line it said.
    expect(state.entries).toHaveLength(0);
  });

  it("moves a child's state without dropping what it already said", () => {
    let state = newSession("s", "acp");
    state = applyUpdate(state, {
      kind: "subagent",
      session_id: "s",
      subagent_id: "child-1",
      subagent_title: "Helper",
      subagent_state: "running",
    });
    state = applyUpdate(state, {
      kind: "subagent",
      session_id: "s",
      subagent_id: "child-1",
      subagent_state: "idle",
    });

    expect(state.subagents).toHaveLength(1);
    expect(state.subagents[0].title).toBe("Helper");
    expect(state.subagents[0].state).toBe("idle");
  });

  it("keeps a grandchild under the subagent that announced it", () => {
    let state = newSession("s", "acp");
    state = applyUpdate(state, {
      kind: "subagent",
      session_id: "s",
      subagent_id: "child-1",
      subagent_title: "Helper",
    });
    state = applyUpdate(state, {
      kind: "subagent",
      session_id: "child-1",
      subagent_id: "grand-1",
      subagent_title: "Helper's helper",
    });

    expect(state.subagents.map((child) => [child.id, child.parentId])).toEqual([
      ["child-1", "s"],
      ["grand-1", "child-1"],
    ]);
  });

  it("drops a registration with no child to name", () => {
    let state = newSession("s", "acp");
    state = applyUpdate(state, {
      kind: "subagent",
      session_id: "s",
      subagent_title: "No id",
    });

    expect(state.subagents).toEqual([]);
  });
});

describe("fleet lifecycle", () => {
  function fleet(): Status {
    return {
      channel: "stable",
      pid: 1,
      ready: true,
      failures: [],
      archived: [],
      tasks: [],
      sessions: [
        {
          id: "a",
          mode: "acp",
          host_pid: 10,
          command: "kimi",
          dir: "/w",
          lifecycle: "idle",
          reachable: true,
          protected: false,
          steer: false,
        },
        {
          id: "b",
          mode: "acp",
          host_pid: 11,
          command: "kimi",
          dir: "/w",
          lifecycle: "idle",
          reachable: true,
          protected: false,
          steer: false,
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
          tier: "available",
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
      tier: "available",
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
      tier: "",
    });
    expect(listing.problems).toEqual([]);
  });

  it("survives a reply that is not a listing", () => {
    expect(parseAgents(null)).toEqual({ agents: [], problems: [], registry: [] });
    expect(parseAgents({ agents: "nope" }).agents).toEqual([]);
  });

  it("reads the published directory beside the catalog", () => {
    const listing = parseAgents({
      agents: [],
      registry: [
        {
          id: "kimi",
          name: "Kimi Code",
          version: "1.2.3",
          description: "A Kimi agent",
          repository: "https://example.com/kimi",
          license: "MIT",
          recipe: "uvx kimi-code@1.2.3",
        },
      ],
    });

    expect(listing.registry).toEqual([
      {
        id: "kimi",
        name: "Kimi Code",
        version: "1.2.3",
        description: "A Kimi agent",
        repository: "https://example.com/kimi",
        license: "MIT",
        recipe: "uvx kimi-code@1.2.3",
      },
    ]);
  });

  it("drops a directory entry a session could not be started from", () => {
    const listing = parseAgents({
      agents: [],
      // No name to search for, and no recipe to show: an entry with a hole is
      // an entry the picker cannot describe.
      registry: [{ version: "1.0.0", recipe: "npx x@1.0.0" }, { id: "ok", name: "OK" }],
    });

    expect(listing.registry).toEqual([]);
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

describe("filterRegistry", () => {
  const directory = () => [
    {
      id: "kimi",
      name: "Kimi Code",
      version: "1.2.3",
      description: "A Kimi agent",
      repository: "https://example.com/kimi",
      license: "MIT",
      recipe: "uvx kimi-code@1.2.3",
    },
    {
      id: "zeta",
      name: "Zeta",
      version: "9.0.0",
      description: "A shell runner",
      repository: "",
      license: "",
      recipe: "npx zeta@9.0.0",
    },
  ];

  it("keeps the directory's order for an empty query", () => {
    expect(filterRegistry(directory(), "  ").map((entry) => entry.id)).toEqual(["kimi", "zeta"]);
  });

  it("matches the name, id, description, version and recipe without case", () => {
    expect(filterRegistry(directory(), "KIMI").map((entry) => entry.id)).toEqual(["kimi"]);
    expect(filterRegistry(directory(), "shell").map((entry) => entry.id)).toEqual(["zeta"]);
    expect(filterRegistry(directory(), "9.0").map((entry) => entry.id)).toEqual(["zeta"]);
    // The recipe is a thing someone copies in from a page, so it is searched.
    expect(filterRegistry(directory(), "uvx").map((entry) => entry.id)).toEqual(["kimi"]);
  });

  it("requires every word the user typed", () => {
    expect(filterRegistry(directory(), "kimi code").map((entry) => entry.id)).toEqual(["kimi"]);
    expect(filterRegistry(directory(), "kimi zeta")).toEqual([]);
  });

  it("returns a copy so a caller cannot reorder the daemon's list", () => {
    const entries = directory();
    filterRegistry(entries, "").reverse();
    expect(entries[0].id).toBe("kimi");
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

  it("orders the controls by id no matter what order the agent announced", () => {
    // An agent re-announces its options after every mode change, sometimes in
    // a different order: the composer bar would reshuffle under the cursor.
    // The id is the one key that stays put.
    const announce = (order: string[]) =>
      parseControls(
        order.map((id) => ({
          id,
          name: `Name ${id}`,
          kind: "select",
          current: "x",
          choices: [{ value: "x", name: "X" }],
        })),
      );

    const first = announce(["voice", "alpha", "mode", "budget"]);
    const second = announce(["budget", "mode", "voice", "alpha"]);
    expect(first.map((control) => control.id)).toEqual(["alpha", "budget", "mode", "voice"]);
    expect(second.map((control) => control.id)).toEqual(first.map((control) => control.id));
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

// The W5-6 acceptance scale: ten sessions, and the one waiting for the user is
// always the first row, wherever it started and whatever the others are doing.
describe("fleet order at ten sessions", () => {
  const nine = [
    { id: "s0", state: "idle" },
    { id: "s1", state: "working" },
    { id: "s2", state: "done" },
    { id: "s3", state: "unknown" },
    { id: "s4", state: "idle" },
    { id: "s5", state: "working" },
    { id: "s6", state: "done" },
    { id: "s7", state: "idle" },
    { id: "s8", state: "working" },
  ];

  it("puts the blocked session on top even when reported last", () => {
    const status = parseStatus({
      sessions: [...nine, { id: "decide", state: "blocked" }],
    });

    expect(status.sessions).toHaveLength(10);
    expect(status.sessions[0].id).toBe("decide");
  });

  it("lifts a session that becomes blocked above the other nine", () => {
    const current = parseStatus({ sessions: [...nine, { id: "s9", state: "idle" }] });
    expect(current.sessions[0].id).toBe("s2");

    const next = withLifecycle(current, "s5", "blocked");
    expect(next?.sessions[0].id).toBe("s5");
    expect(next?.sessions).toHaveLength(10);
  });

  it("keeps two blocked sessions in report order above the other eight", () => {
    const status = parseStatus({
      sessions: [
        { id: "b2", state: "blocked" },
        ...nine.slice(0, 8),
        { id: "b1", state: "blocked" },
      ],
    });

    expect(status.sessions.map((s) => s.id).slice(0, 2)).toEqual(["b2", "b1"]);
  });

  it("sinks an answered session below the finished turns", () => {
    const current = parseStatus({
      sessions: [...nine, { id: "decide", state: "blocked" }],
    });

    const next = withLifecycle(current, "decide", "idle");
    expect(next?.sessions[0].id).toBe("s2");
    // The sort is stable: the answered session keeps its place among the idle
    // rows it joins, below every finished turn, unknown and working session.
    expect(next?.sessions.map((s) => s.id)).toEqual([
      "s2", "s6", "s3", "s1", "s5", "s8", "decide", "s0", "s4", "s7",
    ]);
  });
});
