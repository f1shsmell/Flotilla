import { constants } from "../electron/contract";

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
  // protected is the user's pin: the daemon never reclaims this session as idle.
  protected: boolean;
  // steer is whether the agent takes a message into the running turn.
  steer: boolean;
  // parent is the session that started this one; empty for a root session.
  parent?: string;
}

// ArchivedEntry is a session that is over: nothing holds its agent, and its log
// is all that is left. It is deliberately not a StatusEntry: a finished run has
// no lifecycle, and giving it one would let the list act on a process that does
// not exist.
export interface ArchivedEntry {
  id: string;
  mode: string;
  command: string;
  dir: string;
  // started and ended are the run's bounds. An empty string means this build
  // never recorded it — an older log, or a host that died unwatched — and the
  // view says so rather than showing a time nothing observed.
  started: string;
  ended: string;
}

// TaskEntry is one unattended job: an agent run once with nobody watching. It is
// not a session and deliberately is not shaped like one — there is no host to
// attach to and no terminal to draw — so the window lists it beside the fleet
// rather than inside it.
export interface TaskEntry {
  id: string;
  dialect: string;
  // state is queued, running, waiting, done, failed or cancelled. It is kept as
  // the daemon's own word rather than mapped onto a Lifecycle: a task is not a
  // session, and calling a queued job "idle" would say the queue is empty.
  state: string;
  prompt: string;
  dir: string;
  runs: number;
  spent_usd: number;
  budget_usd: number;
  last_error: string;
}

// InboxKind is what an inbox entry waits on: an answer to a blocked agent, or a
// finished turn this window has not read. The values come from the generated
// contract, so a rename in Go reaches the parser instead of emptying the inbox.
export type InboxKind =
  | typeof constants.daemon.InboxInput
  | typeof constants.daemon.InboxUnread;

// InboxEntry is one item waiting on a person. It names a live session so a
// click can open it; it is not a session and has no view of its own.
export interface InboxEntry {
  id: string;
  kind: InboxKind;
  title: string;
  command: string;
  dir: string;
}

export interface Status {
  channel: string;
  pid: number;
  ready: boolean;
  sessions: StatusEntry[];
  archived: ArchivedEntry[];
  tasks: TaskEntry[];
  // inbox is projected per client: the same finished turn is unread for one window and idle for another.
  inbox?: InboxEntry[];
  failures: string[];
}

export interface StartRequest {
  id: string;
  mode: string;
  command: string;
  args: string[];
  dir: string;
  policy?: string;
  // cols and rows size the initial PTY. They are optional because a structured
  // session has no terminal, and a terminal that names none gets the default.
  cols?: number;
  rows?: number;
  // worktree, when set, runs the session in a fresh worktree of repo.
  worktree?: { repo: string; branch: string };
  // parent is the session that started this one, which scopes its fleet view.
  parent?: string;
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

// worktreeRequest is what the form submits: the same rules as worktreeOf, plus
// the one thing a person should not have to type. A repository with no branch
// is unambiguous — a worktree needs a new branch — so one is named for them
// rather than answered with an error they must then fix by hand.
export function worktreeRequest(
  repo: string,
  branch: string,
  now: Date = new Date(),
): { request?: { repo: string; branch: string }; error?: string } {
  if (repo.trim() !== "" && branch.trim() === "") {
    return { request: { repo: repo.trim(), branch: defaultBranch(now) } };
  }
  return worktreeOf(repo, branch);
}

// defaultBranch names the branch a worktree gets when the user did not name
// one: the date and time it was made, so two worktrees never collide and the
// name still says when the work started.
export function defaultBranch(now: Date): string {
  const pad = (n: number): string => String(n).padStart(2, "0");
  return `feat/${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}-${pad(now.getHours())}${pad(now.getMinutes())}`;
}

// defaultAgent is who the form starts with when the user has not chosen yet, and
// the answer is unambiguous: with exactly one startable agent there is nothing to
// decide, so the form decides it. With several, picking one for the user would be
// a guess about intent, and the list stays a question.
export function defaultAgent(usable: readonly { id: string }[]): string | null {
  return usable.length === 1 ? usable[0].id : null;
}

// modeForAgent is the mode a chosen agent runs in. A preset is one complete
// recipe — command, args and mode together — so picking one settles the mode;
// asking the user to also flip a switch leaves a way to build a request the
// agent cannot serve.
export function modeForAgent(agent: { mode: string } | null): "acp" | "pty" | null {
  if (agent === null) {
    return null;
  }
  return agent.mode === "pty" ? "pty" : "acp";
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

// AgentEntry is one agent as the daemon describes it. The record is a preset
// plus this machine's and the user's answers, and the GUI shows it as given: a
// picker that reshaped it could offer an agent the daemon cannot start.
export interface AgentEntry {
  id: string;
  name: string;
  command: string;
  args: string[];
  mode: string;
  // needs names what the agent requires besides its binary, such as a login.
  needs: string;
  description: string;
  // source is "builtin" or the adapter file that defined the preset.
  source: string;
  installed: boolean;
  path: string;
  enabled: boolean;
  chat: boolean;
  cli: boolean;
}

export interface AgentListing {
  agents: AgentEntry[];
  // problems are adapter or switch files the daemon could not read. They are
  // reported beside the list rather than instead of it.
  problems: string[];
}

// AgentChange is one switch update. An absent field keeps the stored value, so a
// click sends only what it flipped.
export interface AgentChange {
  enabled?: boolean;
  chat?: boolean;
  cli?: boolean;
}

// SetAgentRequest names the agent a change applies to.
export interface SetAgentRequest {
  id: string;
  change: AgentChange;
}

function asStringList(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}

// parseAgents reads the daemon's listing without trusting it. A missing switch
// becomes the conservative value: not installed, not enabled, because offering
// an agent this build cannot start is worse than a shorter list.
export function parseAgents(raw: unknown): AgentListing {
  const value = (raw ?? {}) as Record<string, unknown>;
  const agents = Array.isArray(value.agents) ? value.agents : [];

  return {
    agents: agents.map((entry) => {
      const agent = (entry ?? {}) as Record<string, unknown>;
      const id = asString(agent.id);
      return {
        id,
        name: asString(agent.name, id),
        command: asString(agent.command),
        args: asStringList(agent.args),
        mode: asString(agent.mode, "acp"),
        needs: asString(agent.needs),
        description: asString(agent.description),
        source: asString(agent.source, "builtin"),
        installed: asBoolean(agent.installed),
        path: asString(agent.path),
        enabled: asBoolean(agent.enabled),
        chat: asBoolean(agent.chat),
        cli: asBoolean(agent.cli),
      };
    }),
    problems: asStringList(value.problems),
  };
}

// matchesAgent is what the picker's search box means: every word the user typed
// has to appear somewhere they can see or would type — the name, the id, the
// command they might already know, the one-line description — in any case.
function matchesAgent(agent: AgentEntry, words: string[]): boolean {
  const haystack = [agent.name, agent.id, agent.command, agent.description]
    .join(" ")
    .toLowerCase();
  return words.every((word) => haystack.includes(word));
}

// filterAgents narrows what a search box typed. An empty query keeps the order
// the daemon chose, which is the order the builtins were listed in.
export function filterAgents(agents: readonly AgentEntry[], query: string): AgentEntry[] {
  const words = query.toLowerCase().split(/\s+/).filter((word) => word !== "");
  if (words.length === 0) {
    return agents.slice();
  }
  return agents.filter((agent) => matchesAgent(agent, words));
}

// usableAgents is what a new session of this mode may start. A preset is one
// complete recipe — command, args and mode together — so an agent whose own mode
// differs is not offered: its args would launch the other protocol.
//
// The switch has to agree too: the purpose follows the mode, and the user can
// turn an agent off for it. This is the only place that decides, so the picker
// and the settings page cannot disagree about what is available.
export function usableAgents(
  agents: readonly AgentEntry[],
  mode: "acp" | "pty",
): AgentEntry[] {
  const purpose = mode === "acp" ? "chat" : "cli";
  return agents.filter(
    (agent) =>
      agent.installed &&
      agent.enabled &&
      agent.mode === mode &&
      (purpose === "chat" ? agent.chat : agent.cli),
  );
}

// Choice is one value a control can take. group is the section label the agent
// gave it, absent for a flat list.
export interface Choice {
  value: string;
  name: string;
  group?: string;
}

// Control is one setting the agent exposes: a mode, a select or a boolean. The
// daemon projects the agent's own shapes into this flat list, and the renderer
// shows it as given.
export interface Control {
  id: string;
  name: string;
  kind: string;
  current: string;
  choices?: Choice[];
}

// parseControls reads the projected list without trusting it: a field the
// daemon did not send becomes a visible default rather than undefined. The
// controls sort by id because the agent re-announces its list in whatever
// order it likes — the composer bar must not reshuffle under the cursor.
export function parseControls(raw: unknown): Control[] {
  const list = Array.isArray(raw) ? raw : [];
  const controls = list.map((entry) => {
    const control = (entry ?? {}) as Record<string, unknown>;
    const choices = Array.isArray(control.choices) ? control.choices : [];
    return {
      id: asString(control.id),
      name: asString(control.name),
      kind: asString(control.kind),
      current: asString(control.current),
      choices: choices.map((raw_choice) => {
        const choice = (raw_choice ?? {}) as Record<string, unknown>;
        return {
          value: asString(choice.value),
          name: asString(choice.name),
          group: asString(choice.group),
        };
      }),
    };
  });
  return controls.sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
}

// withControls folds one surface event into the session. The event carries the
// whole list, so it replaces rather than merges: a control the agent stopped
// offering must disappear, not linger with a stale value.
export function withControls(state: SessionState, raw: unknown): SessionState {
  return { ...state, controls: parseControls(raw) };
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

export type EntryKind = "message" | "thought" | "tool" | "plan" | "user" | "other";

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
  options: PermissionOption[];
}

export interface PermissionOption {
  id: string;
  kind: string;
  // name is the label the agent gave for a person to read. Empty when it
  // offered none, in which case the card shows a word of its own rather than
  // the protocol's id.
  name: string;
}

export type Turn = "idle" | "working" | "ended" | "lost";

// QueuedMessage is one message waiting for the running turn to end. The daemon
// owns the queue; the window renders the order it answered with.
export interface QueuedMessage {
  id: string;
  text: string;
  // error names why the last delivery failed, or empty when untried.
  error: string;
}

// Subagent is a child session the agent registered. It answers which children
// exist and what they are doing; the child's own lines stay in the log,
// attributed by their session id.
export interface Subagent {
  id: string;
  // parentId is the session that announced the child: this session, or another
  // subagent for a grandchild.
  parentId: string;
  title: string;
  description: string;
  // state is the agent's own word for the child's work; an unknown one is
  // shown as it came.
  state: string;
}

export interface SessionState {
  id: string;
  mode: string;
  entries: Entry[];
  permissions: Permission[];
  // controls are the settings the agent exposes right now. Empty means the
  // agent offers none (or is a terminal), and the header shows no selectors.
  controls: Control[];
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
  // queue holds the messages waiting for the running turn, in delivery order.
  queue: QueuedMessage[];
  // subagents are the child sessions the agent registered, in the order it
  // announced them. Registrations are metadata about the children, not lines
  // they said, so they never become entries.
  subagents: Subagent[];
}

function asString(value: unknown, fallback = ""): string {
  return typeof value === "string" ? value : fallback;
}

function asBoolean(value: unknown, fallback = false): boolean {
  return typeof value === "boolean" ? value : fallback;
}

// attentionRank orders the fleet by what needs the user: a blocked session
// first, a finished turn next, an unclassified one before quiet work, and idle
// last. The list renders this order and never invents its own.
function attentionRank(lifecycle: Lifecycle): number {
  switch (lifecycle) {
    case "blocked":
      return 0;
    case "done":
      return 1;
    case "unknown":
      return 2;
    case "working":
      return 3;
    default:
      return 4;
  }
}

// orderSessions is a stable sort, so sessions the daemon reported in one state
// keep the order it gave them.
function orderSessions(sessions: StatusEntry[]): StatusEntry[] {
  return [...sessions].sort(
    (a, b) => attentionRank(a.lifecycle) - attentionRank(b.lifecycle),
  );
}

function asInboxKind(value: unknown): InboxKind | null {
  if (value === constants.daemon.InboxInput) {
    return constants.daemon.InboxInput;
  }
  if (value === constants.daemon.InboxUnread) {
    return constants.daemon.InboxUnread;
  }
  return null;
}

// parseInbox reads the cross-session list without inventing an entry: a kind
// this build cannot name, or a row with no session to open, is dropped.
function parseInbox(raw: unknown): InboxEntry[] {
  if (!Array.isArray(raw)) {
    return [];
  }

  const entries: InboxEntry[] = [];
  for (const item of raw) {
    const entry = (item ?? {}) as Record<string, unknown>;
    const id = asString(entry.id);
    const kind = asInboxKind(entry.kind);
    if (id === "" || kind === null) {
      continue;
    }
    entries.push({
      id,
      kind,
      title: asString(entry.title),
      command: asString(entry.command),
      dir: asString(entry.dir),
    });
  }
  return entries;
}

// parseMessageQueue reads the daemon's answer to a queue call. A row with no id
// could not be edited or removed, so it is dropped rather than rendered dead.
export function parseMessageQueue(raw: unknown): QueuedMessage[] {
  const value = (raw ?? {}) as Record<string, unknown>;
  const messages = Array.isArray(value.messages) ? value.messages : [];

  const out: QueuedMessage[] = [];
  for (const item of messages) {
    const message = (item ?? {}) as Record<string, unknown>;
    const id = asString(message.id);
    if (id === "") {
      continue;
    }
    out.push({
      id,
      text: asString(message.text),
      error: asString(message.error),
    });
  }
  return out;
}

export interface Checkpoint {
  turn: number;
  commit: string;
  // at is the RFC 3339 time the snapshot was taken.
  at: string;
}

// parseCheckpoints reads the daemon's answer to a session's snapshot list. A row
// without a turn cannot be rolled back to, so it is dropped rather than rendered
// dead.
export function parseCheckpoints(raw: unknown): Checkpoint[] {
  const value = (raw ?? {}) as Record<string, unknown>;
  const listed = Array.isArray(value.checkpoints) ? value.checkpoints : [];

  const out: Checkpoint[] = [];
  for (const item of listed) {
    const checkpoint = (item ?? {}) as Record<string, unknown>;
    const turn = typeof checkpoint.turn === "number" ? checkpoint.turn : 0;
    if (turn <= 0) {
      continue;
    }
    out.push({
      turn,
      commit: asString(checkpoint.commit),
      at: asString(checkpoint.at),
    });
  }
  return out;
}

// parseStatus reads the daemon's answer without trusting it: a field the daemon
// did not send becomes a visible default rather than `undefined` in the UI.
export function parseStatus(raw: unknown): Status {
  const value = (raw ?? {}) as Record<string, unknown>;
  const sessions = Array.isArray(value.sessions) ? value.sessions : [];
  const archived = Array.isArray(value.archived) ? value.archived : [];
  const tasks = Array.isArray(value.tasks) ? value.tasks : [];

  return {
    channel: asString(value.channel),
    pid: typeof value.pid === "number" ? value.pid : 0,
    ready: asBoolean(value.ready),
    inbox: parseInbox(value.inbox),
    failures: Array.isArray(value.failures)
      ? value.failures.filter((f): f is string => typeof f === "string")
      : [],
    // The archive is parsed separately and never merged into `sessions`: a
    // finished run has no process, and a list that mixed the two would let the
    // window offer a stop button for something that already stopped.
    archived: archived.map((entry) => {
      const run = (entry ?? {}) as Record<string, unknown>;
      return {
        id: asString(run.id),
        mode: asString(run.mode),
        command: asString(run.command),
        dir: asString(run.dir),
        started: asString(run.started),
        ended: asString(run.ended),
      };
    }).filter((entry) => entry.id !== ""),
    // The queue is parsed apart from the fleet for the same reason the archive
    // is: a task has no host, and a list that mixed the two would offer to
    // attach to something with no endpoint.
    tasks: tasks.map((entry) => {
      const task = (entry ?? {}) as Record<string, unknown>;
      return {
        id: asString(task.id),
        dialect: asString(task.dialect),
        state: asString(task.state, "unknown"),
        prompt: asString(task.prompt),
        dir: asString(task.dir),
        runs: typeof task.runs === "number" ? task.runs : 0,
        spent_usd: typeof task.spent_usd === "number" ? task.spent_usd : 0,
        budget_usd: typeof task.budget_usd === "number" ? task.budget_usd : 0,
        last_error: asString(task.last_error),
      };
    }).filter((task) => task.id !== ""),
    sessions: orderSessions(sessions.map((entry) => {
      const session = (entry ?? {}) as Record<string, unknown>;
      return {
        id: asString(session.id),
        mode: asString(session.mode, "pty"),
        host_pid: typeof session.host_pid === "number" ? session.host_pid : 0,
        command: asString(session.command),
        dir: asString(session.dir),
        lifecycle: asLifecycle(session.state),
        reachable: asBoolean(session.reachable),
        protected: asBoolean(session.protected),
        steer: asBoolean(session.steer),
      };
    })),
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
  return changed ? { ...status, sessions: orderSessions(sessions) } : status;
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
    controls: [],
    turn: "idle",
    lastSeq: 0,
    moreHistory: false,
    queue: [],
    subagents: [],
  };
}

// applyHistory folds the attach answer into the log. The answer is the
// authoritative tail of the session's stored log: the caller folds it into a
// fresh state, so an entry the answer does not hold is an entry the view does
// not hold, and one it does hold is appended in the order the daemon numbered.
// `more` admits the tail is cut mid-log rather than pretending the session
// started where this page starts.
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
// paragraph twice with its text doubled. An unseen one is appended in order.
//
// A sequence past the one expected names a hole in what this log holds — the
// stored log trims its oldest half and moves the window under a long-lived
// client. Dropping the update would hide the gap; recording a warning about it
// is the most the projector may do, so the fact is logged and the view
// continues from what did arrive.
export function applyUpdate(state: SessionState, raw: unknown): SessionState {
  const value = (raw ?? {}) as Record<string, unknown>;
  const seq = typeof value.seq === "number" ? value.seq : 0;
  if (seq > 0 && seq <= state.lastSeq) {
    return state;
  }
  if (seq > 0 && state.lastSeq > 0 && seq > state.lastSeq + 1) {
    console.warn(
      `update sequence has a hole: expected ${state.lastSeq + 1}, got ${seq}`,
    );
  }

  const folded = foldUpdate(state, value);
  return { ...folded, lastSeq: Math.max(folded.lastSeq, seq) };
}

function foldUpdate(state: SessionState, value: Record<string, unknown>): SessionState {
  // rawKind is the wire's word, wider than EntryKind: "subagent" is a
  // registration about a child, never a line of the log.
  const rawKind = asString(value.kind, "other");
  const kind = rawKind as EntryKind;
  const text = asString(value.text);
  const toolCallId = asString(value.tool_call_id);
  const toolTitle = asString(value.tool_title);
  const toolStatus = asString(value.tool_status);
  // cont marks a piece of one update that was split to fit a frame. The wire says
  // so explicitly because the kind and the text cannot: a second prompt is the
  // same shape as the second piece of a long one.
  const continued = value.cont === true;

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

  // A registration names a child and what is known about it; each update
  // carries only what changed, so a later state patch keeps the title it does
  // not repeat. A registration with no child to name is noise.
  if (rawKind === "subagent") {
    const id = asString(value.subagent_id);
    if (id === "") {
      return state;
    }
    const parentId = asString(value.session_id) || state.id;
    const title = asString(value.subagent_title);
    const description = asString(value.subagent_description);
    const childState = asString(value.subagent_state);
    const index = state.subagents.findIndex((child) => child.id === id);
    if (index >= 0) {
      const subagents = state.subagents.slice();
      const previous = subagents[index];
      subagents[index] = {
        ...previous,
        title: title || previous.title,
        description: description || previous.description,
        state: childState || previous.state,
      };
      return { ...state, subagents };
    }
    return {
      ...state,
      subagents: [
        ...state.subagents,
        { id, parentId, title, description, state: childState },
      ],
    };
  }

  // An update that carries nothing is noise: an agent's command list or a plan
  // it has not filled in yet would otherwise render as a blank line.
  if (text === "" && kind !== "plan") {
    return state;
  }

  // An agent streams one message in pieces, and one that is too large for a
  // frame is split on the wire. Both belong in one paragraph, so a chunk that
  // says it continues the line before extends it instead of starting a new one;
  // anything else (a thought after a message, a tool call, a second prompt)
  // breaks the run.
  const last = state.entries[state.entries.length - 1];
  if (last && last.kind === kind && (continued || kind === "message" || kind === "thought")) {
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

  // An older daemon sends bare option ids; they degrade to an unknown kind,
  // which is never styled as a grant, rather than vanishing into a card with
  // no buttons.
  const options = Array.isArray(value.options)
    ? value.options
        .map((option) =>
          typeof option === "string"
            ? { id: option, kind: "", name: "" }
            : typeof option === "object" && option !== null
              ? {
                  id: asString((option as Record<string, unknown>).id),
                  kind: asString((option as Record<string, unknown>).kind),
                  name: asString((option as Record<string, unknown>).name),
                }
              : { id: "", kind: "", name: "" },
        )
        .filter((option) => option.id !== "")
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

// controlError reports a refused control write without touching the turn: the
// agent may still be working, and a rejected setting is not a failed turn.
export function controlError(state: SessionState, error: string): SessionState {
  return { ...state, error };
}

export function loseSession(state: SessionState, error?: string): SessionState {
  return { ...state, turn: "lost", error };
}
