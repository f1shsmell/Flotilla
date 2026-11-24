# Changelog

Notable changes, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[VERSIONING.md](VERSIONING.md): before 1.0 a MINOR carries new functionality and
any breaking change, and a PATCH carries fixes only.

Two channels ship from this repository: **stable** (`Flotilla`) and **preview**
(`Flotilla Preview`, versions ending `-preview.N`). Preview builds are not
covered by the compatibility promises in VERSIONING.md.

## [Unreleased]

Scope is the first tranche of M2: the durable state layer the orchestrator needs,
plus the two pieces it runs on.

### Added

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

### Changed

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
