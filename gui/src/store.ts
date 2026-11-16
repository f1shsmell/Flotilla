// The renderer's model of the wire. Everything here is pure: components project
// these values, they never reshape them, so what the daemon said stays the only
// source of truth.

// Lifecycle is the state the daemon reports for a session. The five values are
// a closed set (FLOTILLA.md); a value this build does not recognise becomes
// unknown, never done, because done would claim there is something to look at.
export type Lifecycle = "working" | "blocked" | "done" | "idle" | "unknown";

const lifecycles: readonly Lifecycle[] = ["working", "blocked", "done", "idle", "unknown"];

function asLifecycle(value: unknown): Lifecycle {
  return typeof value === "string" && (lifecycles as readonly string[]).includes(value)
    ? (value as Lifecycle)
    : "unknown";
}

export interface StatusEntry {
  id: string;
  mode: string;
  host_pid: number;
  command: string;
  dir: string;
  // lifecycle is what the host last reported. It is not derived from reachable:
  // a reachable session can be blocked, and an unreachable one is unknown.
  lifecycle: Lifecycle;
  reachable: boolean;
}

export interface Status {
  channel: string;
  pid: number;
  ready: boolean;
  sessions: StatusEntry[];
  failures: string[];
}

export interface StartRequest {
  id: string;
  mode: string;
  command: string;
  args: string[];
  dir: string;
  policy?: string;
  // worktree, when set, runs the session in a fresh worktree of repo.
  worktree?: { repo: string; branch: string };
}

// worktreeOf reads the form's two worktree fields. Both empty means none, both
// filled means one, and half of one is an error: falling back to the plain
// directory would start the session somewhere the user did not ask for.
export function worktreeOf(
  repo: string,
  branch: string,
): { request?: { repo: string; branch: string }; error?: string } {
  const repository = repo.trim();
  const name = branch.trim();

  if (repository === "" && name === "") {
    return {};
  }
  if (repository === "" || name === "") {
    return { error: "a worktree needs both a repository and a branch" };
  }
  return { request: { repo: repository, branch: name } };
}

// splitArgs turns the form's argument line into argv: spaces separate, double
// quotes group, and a quote with nothing in it still makes an empty argument.
export function splitArgs(line: string): string[] {
  const args: string[] = [];
  let current = "";
  let quoted = false;
  let started = false;

  for (const character of line) {
    if (character === '"') {
      quoted = !quoted;
      started = true;
      continue;
    }
    if (!quoted && /\s/.test(character)) {
      if (started) {
        args.push(current);
        current = "";
        started = false;
      }
      continue;
    }
    current += character;
    started = true;
  }

  if (started) {
    args.push(current);
  }
  return args;
}

export interface DiffFile {
  path: string;
  old_path?: string;
  status: string;
  added: number;
  removed: number;
  binary?: boolean;
  untracked?: boolean;
}

export type DiffLineKind = "add" | "del" | "hunk" | "meta" | "context";

export interface DiffLine {
  kind: DiffLineKind;
  text: string;
  // line is the number on side, which is what a comment anchors to; 0 for a
  // line that belongs to neither version (a header or a hunk marker).
  line: number;
  side: "new" | "old";
}

export interface Comment {
  id: string;
  path: string;
  line: number;
  side: string;
  text: string;
  author: string;
  at: string;
}

// parsePatch classifies a unified diff line by line, which is all a renderer
// needs: git's own prefixes carry the meaning.
export function parsePatch(patch: string): DiffLine[] {
  const out: DiffLine[] = [];
  const lines = patch.split("\n");
  let oldLine = 0;
  let newLine = 0;

  for (const [index, line] of lines.entries()) {
    // git ends the patch with a newline; the empty tail is not a line.
    if (index === lines.length - 1 && line === "") {
      continue;
    }

    if (line.startsWith("@@")) {
      const match = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(line);
      oldLine = match ? Number(match[1]) : 0;
      newLine = match ? Number(match[2]) : 0;
      out.push({ kind: "hunk", text: line, line: 0, side: "new" });
      continue;
    }

    if (
      line.startsWith("+++") ||
      line.startsWith("---") ||
      line.startsWith("diff --git") ||
      line.startsWith("index ") ||
      line.startsWith("new file") ||
      line.startsWith("deleted file") ||
      line.startsWith("similarity") ||
      line.startsWith("rename ") ||
      // "\ No newline at end of file" is a marker, not a line: counting it would
      // shift every note below it onto the wrong line.
      line.startsWith("\\")
    ) {
      out.push({ kind: "meta", text: line, line: 0, side: "new" });
      continue;
    }

    if (line.startsWith("+")) {
      out.push({ kind: "add", text: line.slice(1), line: newLine, side: "new" });
      newLine += 1;
      continue;
    }

    if (line.startsWith("-")) {
      out.push({ kind: "del", text: line.slice(1), line: oldLine, side: "old" });
      oldLine += 1;
      continue;
    }

    out.push({
      kind: "context",
      text: line.startsWith(" ") ? line.slice(1) : line,
      line: newLine,
      side: "new",
    });
    oldLine += 1;
    newLine += 1;
  }

  return out;
}

// commentsOn returns the notes anchored to one line.
export function commentsOn(
  list: Comment[],
  path: string,
  side: string,
  line: number,
): Comment[] {
  return list.filter(
    (comment) => comment.path === path && comment.side === side && comment.line === line,
  );
}

// commentPrompt renders notes as the text a reviewer pastes into a prompt, which
// is what makes a comment worth keeping.
export function commentPrompt(list: Comment[]): string {
  return list
    .map((comment) => `${comment.path}:${comment.line} (${comment.side}) — ${comment.text}`)
    .join("\n");
}

export type EntryKind = "message" | "thought" | "tool" | "plan" | "other";

export interface Entry {
  key: string;
  kind: EntryKind;
  text: string;
  toolTitle?: string;
  toolStatus?: string;
}

export interface Permission {
  id: string;
  title: string;
  kind: string;
  options: string[];
}

export type Turn = "idle" | "working" | "ended" | "lost";

export interface SessionState {
  id: string;
  mode: string;
  entries: Entry[];
  permissions: Permission[];
  turn: Turn;
  stopReason?: string;
  error?: string;
  // lastSeq is the newest stored update this log holds. The daemon numbers
  // every update as it stores it, so a live update that arrives after the
  // attach history is applied can be told apart from one already folded in.
  lastSeq: number;
  // moreHistory is true when the daemon kept older updates back. The log then
  // starts mid-turn, and saying so is the difference between a short log and a
  // lie about where the session began.
  moreHistory: boolean;
}

function asString(value: unknown, fallback = ""): string {
  return typeof value === "string" ? value : fallback;
}

function asBoolean(value: unknown, fallback = false): boolean {
  return typeof value === "boolean" ? value : fallback;
}

// parseStatus reads the daemon's answer without trusting it: a field the daemon
// did not send becomes a visible default rather than `undefined` in the UI.
export function parseStatus(raw: unknown): Status {
  const value = (raw ?? {}) as Record<string, unknown>;
  const sessions = Array.isArray(value.sessions) ? value.sessions : [];

  return {
    channel: asString(value.channel),
    pid: typeof value.pid === "number" ? value.pid : 0,
    ready: asBoolean(value.ready),
    failures: Array.isArray(value.failures)
      ? value.failures.filter((f): f is string => typeof f === "string")
      : [],
    sessions: sessions.map((entry) => {
      const session = (entry ?? {}) as Record<string, unknown>;
      return {
        id: asString(session.id),
        mode: asString(session.mode, "pty"),
        host_pid: typeof session.host_pid === "number" ? session.host_pid : 0,
        command: asString(session.command),
        dir: asString(session.dir),
        lifecycle: asLifecycle(session.state),
        reachable: asBoolean(session.reachable),
      };
    }),
  };
}

export function summarise(status: Status): { total: number; reachable: number } {
  return {
    total: status.sessions.length,
    reachable: status.sessions.filter((session) => session.reachable).length,
  };
}

// withLifecycle folds one reported lifecycle change into the fleet. A session
// this fleet does not list is ignored rather than added: a state for a session
// that is gone would resurrect a row with nothing behind it.
export function withLifecycle(
  status: Status | null,
  id: string,
  raw: unknown,
): Status | null {
  if (status === null) {
    return status;
  }
  const lifecycle = asLifecycle(raw);
  let changed = false;
  const sessions = status.sessions.map((session) => {
    if (session.id !== id || session.lifecycle === lifecycle) {
      return session;
    }
    changed = true;
    return { ...session, lifecycle };
  });
  // Returning the same object keeps React from re-rendering the fleet for a
  // state that did not change, which is most of them.
  return changed ? { ...status, sessions } : status;
}

// needsAttention is true for the states a user has to act on or should look at.
// It is the one place that decides, so the list and any badge cannot disagree.
export function needsAttention(lifecycle: Lifecycle): boolean {
  return lifecycle === "blocked" || lifecycle === "done";
}

export function newSession(id: string, mode: string): SessionState {
  return {
    id,
    mode,
    entries: [],
    permissions: [],
    turn: "idle",
    lastSeq: 0,
    moreHistory: false,
  };
}

// applyHistory folds the updates the daemon kept for a client that has just
// attached. They are the log the session already had, so they go in before any
// live update and in the order the daemon numbered them.
//
// An update arrives twice on a reattach: once in this history and once as live
// traffic if it was produced while the attach was in flight. The sequence is
// what tells them apart, which is why the log remembers the newest one it holds.
export function applyHistory(state: SessionState, raw: unknown): SessionState {
  const value = (raw ?? {}) as Record<string, unknown>;
  const updates = Array.isArray(value.updates) ? value.updates : [];

  let next = { ...state, moreHistory: value.more === true };
  for (const update of updates) {
    next = applyUpdate(next, update);
  }
  return next;
}

// applyUpdate folds one agent update into the log. A tool call arrives twice —
// once started, once finished — so it is keyed by its id and updated in place
// instead of appended twice.
//
// An update the log already holds is dropped rather than folded again: a
// reattach replays the recent past, and appending it would show the same
// paragraph twice with its text doubled.
export function applyUpdate(state: SessionState, raw: unknown): SessionState {
  const value = (raw ?? {}) as Record<string, unknown>;
  const seq = typeof value.seq === "number" ? value.seq : 0;
  if (seq > 0 && seq <= state.lastSeq) {
    return state;
  }

  const folded = foldUpdate(state, value);
  return { ...folded, lastSeq: Math.max(folded.lastSeq, seq) };
}

function foldUpdate(state: SessionState, value: Record<string, unknown>): SessionState {
  const kind = asString(value.kind, "other") as EntryKind;
  const text = asString(value.text);
  const toolCallId = asString(value.tool_call_id);
  const toolTitle = asString(value.tool_title);
  const toolStatus = asString(value.tool_status);

  if (kind === "tool" && toolCallId !== "") {
    const index = state.entries.findIndex((entry) => entry.key === toolCallId);
    if (index >= 0) {
      const entries = state.entries.slice();
      const previous = entries[index];
      entries[index] = {
        ...previous,
        toolTitle: toolTitle || previous.toolTitle,
        toolStatus: toolStatus || previous.toolStatus,
      };
      return { ...state, entries };
    }
    return {
      ...state,
      entries: [
        ...state.entries,
        { key: toolCallId, kind, text, toolTitle, toolStatus },
      ],
    };
  }

  // An update that carries nothing is noise: an agent's command list or a plan
  // it has not filled in yet would otherwise render as a blank line.
  if (text === "" && kind !== "plan") {
    return state;
  }

  // An agent streams one message in pieces. They belong in one paragraph, so a
  // chunk of the same kind as the last entry extends it instead of starting a
  // new one; anything else (a thought after a message, a tool call) breaks the
  // run.
  const last = state.entries[state.entries.length - 1];
  if (last && last.kind === kind && (kind === "message" || kind === "thought")) {
    const entries = state.entries.slice();
    entries[entries.length - 1] = { ...last, text: last.text + text };
    return { ...state, entries };
  }

  return {
    ...state,
    entries: [
      ...state.entries,
      { key: `${kind}-${state.entries.length}`, kind, text },
    ],
  };
}

export function addPermission(state: SessionState, raw: unknown): SessionState {
  const value = (raw ?? {}) as Record<string, unknown>;
  const id = asString(value.id);
  if (id === "" || state.permissions.some((permission) => permission.id === id)) {
    return state;
  }

  const options = Array.isArray(value.options)
    ? value.options.filter((option): option is string => typeof option === "string")
    : [];

  return {
    ...state,
    permissions: [
      ...state.permissions,
      {
        id,
        title: asString(value.title),
        kind: asString(value.kind),
        options,
      },
    ],
  };
}

export function answerPermission(state: SessionState, id: string): SessionState {
  return {
    ...state,
    permissions: state.permissions.filter((permission) => permission.id !== id),
  };
}

export function startTurn(state: SessionState): SessionState {
  return { ...state, turn: "working", stopReason: undefined, error: undefined };
}

export function endTurn(state: SessionState, stopReason: string): SessionState {
  return { ...state, turn: "idle", stopReason };
}

export function failTurn(state: SessionState, error: string): SessionState {
  return { ...state, turn: "idle", error };
}

export function loseSession(state: SessionState, error?: string): SessionState {
  return { ...state, turn: "lost", error };
}
