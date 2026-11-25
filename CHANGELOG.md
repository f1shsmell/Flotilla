# Changelog

Notable changes, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[VERSIONING.md](VERSIONING.md): before 1.0 a MINOR carries new functionality and
any breaking change, and a PATCH carries fixes only.

Two channels ship from this repository: **stable** (`Flotilla`) and **preview**
(`Flotilla Preview`, versions ending `-preview.N`). Preview builds are not
covered by the compatibility promises in VERSIONING.md.

## [Unreleased]

Scope is the opening of M2: the durable state layer the orchestrator needs, the
pieces it runs on, and the inbox that surfaces what waits on a person.

### Added

- **An agent can run its own workers.** Every ACP session is opened with
  `FLT_SESSION_ID`, `FLT_CHANNEL` and `FLT_POLICY` in its environment and a fleet
  tool server over MCP: the agent lists its own part of the fleet, starts a
  worker in place or in a fresh worktree, prompts it and reads the turn's
  answer, and stops it. The tools act only on the session itself and the workers
  it started, a worker cannot prompt or stop the session it runs inside and may
  not be given more permission than that session has, and `flt status` run from
  inside a session reports the same scope. `flt mcp serve` is the server; a
  build with no `flt` beside the session host registers none.
- **Idle sessions are closed, pinned ones never.** A session whose agent family
  has been quiet for an hour is reclaimed: its host process ends and the run
  moves to Finished with its log intact. Work in a child agent keeps the whole
  family alive, and so does an open window, a queued message or a finished turn
  nobody has read. Protect pins a session, and the pin survives a restart.
- **Child sessions an agent starts.** A session that announces subagents gets a
  list of them beside the conversation: each child's title and what it is
  doing, with a later state change moving the entry in place. The lines a child
  writes stay in the session's own log, attributed by the child's id; the list
  answers only which children exist, never what they said.
- **A checkpoint for every finished turn.** A turn that ends leaves a hidden git
  ref in the session's working tree: a commit of the tree exactly as the turn
  left it, recorded without moving HEAD, the branch or the user's index. The
  diff panel lists the turns; restoring one rewrites the working tree and
  removes what came after it, and is refused while a turn runs or another
  session holds the same checkout.
- **A queue for a running turn.** ACP's core has no mid-turn delivery — what a
  second prompt means is the agent's own choice — so a message typed while the
  agent works or waits on an answer waits in a queue the daemon owns instead.
  The finished turn starts the next message, the strip above the composer
  edits, reorders and removes what waits, and a message whose turn could not
  start says why rather than disappearing.
- **Steering a running turn.** An agent that advertises the steering extension
  takes a message into the turn already running: the composer offers Steer,
  and the message lands in the stream as the user's own line. An agent that
  does not advertise it is never sent one mid-turn — the message waits in the
  queue, and a steer that finds no running turn falls back to the same queue.
- **An inbox.** A session blocked on an answer and a finished turn this window
  has not read are listed together across the fleet. A blocked entry carries the
  agent's own question when it asked one; clicking opens the session where the
  answer is given. Seen stays per window, so one window reading a turn clears its
  own entry and leaves every other window's alone.
- **A state database.** Everything the daemon keeps now lives in one SQLite file
  under `$FLT_HOME/sessions/state.db`: the event log, the projections readers
  query, the receipts of effects already applied, the outbox of effects still
  owed, the transcripts, the per-client seen table and the review comments. An
  event, the view of it and the work it owes commit together, so a crash cannot
  leave one without the others. See [VERSIONING.md](VERSIONING.md): this layout
  is part of the state a newer build must open.
- **A headless execution layer**: an agent run once, unattended, with its output
  projected into the same updates an ACP session produces. Two dialects ship —
  `claude -p --output-format stream-json` and `codex exec --json` — and a new one
  is a data entry, not a code branch.
- **An orchestrator kernel**: a durable task queue with a budget gate, a hold
  state for work that must wait on a person, and a run cap. A held or waiting
  task spends nothing, and a run whose outcome is unknown — a timeout, or a
  daemon that died under it — is never retried on its own: it is parked for a
  person to look at.
- **The queue, served.** The daemon now owns the queue and the outbox: it parks
  a task its predecessor died under before it starts listening, drains the
  effects a previous run left owed, and lists the queue under `tasks` in
  `daemon.status` — which `flt status` prints after the fleet. A run's output
  reaches the database through an effect queued in the same transaction as the
  task change, so a crash between "the task finished" and "what it said" leaves
  work owed rather than work lost, and the receipt it leaves behind is what stops
  the next daemon doing it twice.
- **Agents are found, not guessed.** The agent listing now says what this machine
  shows about each agent: `available` when the command the daemon starts is
  there, `installed-but-not-acp` when only the vendor CLI is — claude and codex
  themselves do not speak ACP, so without their bridge the agent is installed and
  unreachable — `suspected` when only the agent's own config directory exists,
  and `missing` otherwise. `flt agents` prints the tier, and a daemon too old to
  send one still reads as installed or missing. An adapter file may say how its
  agent is recognised (`discovery.command`, `discovery.config`), so an agent this
  build does not ship gets the same tiers. `flt probe --handshake` stays the
  zero-token proof that an agent actually speaks ACP.
- **The published agent directory is cached.** The daemon fetches the official
  ACP Registry at startup when its cache is stale and hourly after that, into one
  file under the state root's cache directory. An answer that is not a registry
  is refused, an entry naming no pinned install is left out, and a failed refresh
  keeps the last copy serving, so an offline build still has a directory.
  `flt registry list` prints the cache and how old it is, and
  `flt registry refresh` refetches it; listing a recipe installs nothing, and
  nothing in it is run. The registry's
  `distribution` is the adapter file's `install` field — one recipe format for
  both: npx or uvx pinned to an exact version, or a binary archive per platform.
- **The directory reaches the picker.** `daemon.agents` now carries the cached
  registry's agents that this build does not already know, each with its pinned
  recipe. The new-session picker and the agent settings page list them under
  **ACP Registry**: searchable, with no switch and no Start, because a recipe is
  not an install. `flt agents` prints the same section after the catalog.
- **A version-drift report for the pins.** `flt registry drift` asks npm and
  PyPI for the latest version of every package the catalog and the cached
  directory pin, and prints only the pins that are behind — a match is counted,
  not listed, and a registry that cannot answer is a failed line naming why,
  never drift. `--json` prints every pin with its answer, and `--fail-on-drift`
  exits non-zero when any pin is behind, so a scheduled job can notice a stale
  recipe without rewriting one. A binary recipe, which names no package, is
  reported as skipped.

### Changed

- **`flt agents` speaks the discovery tier.** The state column now prints
  `available`, `installed-but-not-acp`, `suspected` or `missing` where it used
  to print `installed` or `missing`; a daemon that predates the tier still
  yields the old words. A script that matched `installed` should match
  `available` — the agent's start command is present; the switches still decide
  whether a session may launch it.
- The pre-database layout (`transcripts/*.jsonl`, `seen.json`, `comments/*.json`)
  is imported once on the first start of this build and **left in place**. An
  importer is not trusted with the only copy of a user's history, and a bug
  found later can be re-run against the originals. Nothing in the running product
  reads those files afterwards.

## [0.1.0-preview.1] - 2026-11-24

The first preview build. Scope is the M1 milestone: ACP sessions, worktree
parallelism, the PTY fallback and Windows packaging.

### Added

- **ACP sessions** with a live log: streaming messages, thoughts, tool calls and
  plans; permission cards answered from the window; cancel.
- **Surface controls** projected from what the agent announces — mode, model and
  thought level — read and written over the protocol, never by restarting the
  session. A control the agent does not offer is not shown.
- **Worktree parallelism**: a session can run in a fresh worktree of a
  repository, with `.worktreeinclude` copying the ignored files (`.env` and the
  like) it needs.
- **PTY fallback** for any CLI that does not speak ACP: full interaction,
  bracketed paste, approval-screen detection, Chinese IME.
- **Finished runs**: a session whose host is gone stays in the fleet under
  **Finished**, with its stored log readable and nothing to press. It is not
  resumed, restarted or prompted; the window says so.
- **Diff review** over three scopes (turn, branch, unstaged), line comments, and
  one-click commit, push and pull request through the user's own git and `gh`
  credentials.
- **Agents are data**: presets and adapters are JSON definitions under
  `$FLT_HOME/config/adapters`; the window offers what this machine can actually
  start.
- **Tray and login autostart** (off by default): closing the window parks the
  app, and a login launch starts hidden with the fleet warm.
- **Two channels side by side**: distinct app identity, state root and pipe
  namespace, so a preview install does not touch a stable one.

### Notes

- Verified against claude-agent-acp and codex-acp for full sessions, and against
  opencode for handshake and session creation (its own provider rejected the
  local credential at prompt time). Everything else is promised only as "any CLI
  can be opened and driven in a terminal".
- Honest boundaries: a session-host crash loses its session; an app upgrade ends
  live sessions, which then appear under **Finished**; an OS restart restores the
  layout and the logs, never a running process.
- Windows x64 only. The installer is unsigned; Windows will warn on first run.
- The clean-Windows install acceptance (ten minutes from installer to a working
  session) and the stable-window-to-preview-daemon downgrade check have not been
  run on a clean machine for this build.
