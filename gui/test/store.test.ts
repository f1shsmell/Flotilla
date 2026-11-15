import { describe, expect, it } from "vitest";

import {
  addPermission,
  answerPermission,
  applyUpdate,
  endTurn,
  loseSession,
  newSession,
  parsePatch,
  parseStatus,
  splitArgs,
  startTurn,
  worktreeOf,
  summarise,
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
      reachable: false,
    });
    expect(status.failures).toEqual([]);
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
});
