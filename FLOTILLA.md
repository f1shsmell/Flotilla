# Flotilla project memory

This file is loaded into every agent session, so keep it concise and durable —
it is the project's standing instructions to the agent.

## The four iron laws

1. **The GUI can die, the daemon can die, sessions must live.** Agent handles
   (ConPTY, ACP stdio) are held only by a per-session `session-host`. The daemon
   is a restartable control plane that reconnects to hosts. A host crash is the
   only session-loss source; the README states this boundary honestly.
2. **Adapters are data where possible.** Most new agents are one JSON
   definition. Heuristics JSON cannot express (blocked detection, paste
   boundaries) are JSON-described plus a generic host engine; complex cases are
   plugin packs. Never claim absolute "zero host changes".
3. **The GUI never spawns an agent.** Everything goes GUI → daemon →
   session-host.
4. **The orchestrator does no I/O.** Command → event → effect: the orchestrator
   is pure decision; events, projections, receipts and outbox commit in one
   transaction; effect workers run side effects and feed results back as
   commands. CLI, MCP, cron and GUI all call the same service layer. Every
   entry has an exit (reverse states — a one-way door is a bug).

## Glossary

One word per concept, repo-wide: **fleet** (the managed set of sessions),
**environment** (a machine plus its agent installations), **session** (one
agent conversation with its worktree and handles), **turn** (one
user-to-agent cycle, including follow-up work such as checkpointing),
**task** (a queued unit of work the orchestrator assigns), **account** (a
vendor credential, bridged from magpie, never stored by the core),
**daemon** (the control plane), **session host** (the per-session handle
holder), **adapter** (the JSON definition plus engine that speaks one agent's
protocol).

## Conventions

- Monorepo: one Go module with `cmd/{flt,flt-daemon,flt-session-host}` and
  `internal/`, plus `gui/` (Electron + React + TS), `adapters/`, `docs/`. Dependency
  direction: `gui → daemon → session-host`; adapters are data loaded by the
  host engine. Contract types (IPC messages, adapter schema) live in one Go
  package; TS types are generated from it (tygo/go2ts) or held by a wire-parity
  gate — a type two sides mirror by hand is a parity bug waiting to happen.
- Libraries: PTY via `charmbracelet/x/xpty` (fallback `aymanbagabas/go-pty`);
  ACP client via `coder/acp-go-sdk`; SQLite via `modernc.org/sqlite` (pure Go,
  keeps cross-compilation). creack/pty has no Windows backend; do not add it.
- The terminal data plane is separate from the control plane: control messages
  are JSON-RPC; PTY bytes ride length-prefixed binary frames with coalescing
  and backpressure.
- The handshake negotiates capabilities, never an exact version: `version` is a
  floor the peer must meet, and `capabilities` decides what crosses the wire.
  A newer peer connects and is sent less; only a peer below the floor is
  refused. Every capability names its removal condition beside it, so the set
  stays a list of live differences rather than a graveyard. This is what lets
  a stable window talk to a preview daemon.
- Daemon lifecycle: the GUI detached-spawns it on demand (optional login
  autostart), single-instance lock; the GUI detects daemon death and respawns
  it; hosts outlive the daemon.
- Electron baseline: renderer contextIsolation on, nodeIntegration off, preload
  bridge only; the pipe token lives in the main process.
- GUI rendering: React over one store, and components project canonical state
  rather than owning it. A streamed update flushes on a frame, never per chunk;
  xterm.js is held by a ref and never re-created by a render; no component or
  state library, so the UI stays dumb and the wire stays the only contract.
- Errors carry identity — a typed code or sentinel, never a sentence. Whoever
  first knows why an operation failed owns its class; a later layer projects
  the class, never re-derives it from prose.
- Judgements read structure, never wording. The exception is the PTY adapter
  boundary: approval-UI recognition is heuristic by nature, lives there as
  data, and never leaks into a gate, classification or routing decision.
- Session state is five states: working / blocked / done / idle / unknown.
  `unknown` is never treated as complete. A timeout or stall does not prove a
  prompt was undelivered — never blindly resend (double-fire).
- The UI renders canonical state and never redefines it; no continuous
  repaint animation, no lying spinner, no stale label.
- Dev isolation: a source-built binary launched with `FLT_HOME` set shares no
  on-disk state with a stable install — config, credentials, sessions, cache
  all live under it, and every fallback path into the real home is skipped.
- Dev and demo turns run on the Replay provider. Real agent turns cost money;
  use a real provider only when the change depends on its behavior, and then
  its cheapest model.

## Comments

Default is none — the code is the truth. Write one only when the **why** is
non-obvious: a hidden constraint, a workaround anchored to something verifiable,
an invariant the type system cannot express, an external-protocol quirk.

A comment states the constraint that holds now. It does not narrate how the
code got here; the incident belongs in the commit message.

- English, always — every comment, in every language the tree is written in.
- Declaration doc: ≤5 lines. Every other comment: ≤3 lines. Struct-field and
  trailing `//`: 1 line.
- Never: restatements of the code, phase/stage narrative, incident or
  conversation history, section banners, commented-out code, `@param` lists.
- `TODO(#nnn):` and `HACK(#nnn):` need the issue anchor. `FIXME` is banned.
- One responsibility per file; 800 lines is the ceiling. Test files are exempt.

## Tests

- A behavior change writes the failing test first; it must fail without the
  change. A refactor keeps existing tests green and owes no new failing test.
  A docs-only change owes no test. Say which gate was skipped.
- Event-sourcing tests drain the effect worker; no sleeps, no polling. A test
  that needs a timeout to pass is a wrong test.
- Local verification is headless: no GUI windows, no visible terminals. Daemon
  and host run in tests as subprocesses with piped stdio; GUI logic is tested
  without launching Electron.

## Pre-push CI simulation

Run these before pushing a branch or updating a PR, and before a commit that
changes Rust or TypeScript code. A docs-only change may skip them and say so
in the commit message or PR.

```bash
gofmt -l <changed Go files>           # formatting; empty output = clean
go vet ./...
golangci-lint run                     # at CI's pin
go test <changed packages>            # plus the packages that import them
go run ./cmd/flt-contract -out gui/electron/contract.ts -check
bash scripts/check-coverage.sh        # sensitive paths, when they change
# gui changes:
pnpm --dir gui lint && pnpm --dir gui test
```

The contract gate is the answer to "two sides mirror a type by hand". Go is the
source: `cmd/flt-contract` renders the method names, the wire constants and the
JSON shape of every type a client reads into `gui/electron/contract.ts`. A wire
change that does not regenerate that file fails here, and so does a renderer
interface that stopped matching it (`gui/test/contract.test.ts`).

The CI workflows land with the first code and run exactly these gates on
Windows; docs-only changes skip CI via paths filters. If a gate
cannot run locally, record which gate and why in the PR; do not report the
change as verified.

## Sensitive paths

A wrong change under these paths is a hole in a boundary, not a feature bug.
Every change under them gets full review plus security review regardless of
author, and holds a coverage floor that ratchets up, never down: the floor per
package lives in `coverage-floor.txt`, and CI fails below it. The list
activates as the code lands:

- sensitive: internal/ipc/** (named-pipe auth, token handling)
- sensitive: internal/host/** (ConPTY / stdio ownership)
- sensitive: internal/pty/** (input injection, blocked gate)
- sensitive: internal/worktree/** (git operations on user repos)
- sensitive: internal/gitops/** (commits, pushes and PRs on user repos)
- sensitive: internal/bridge/** (magpie credential bridge)
- sensitive: gui/src/updater/**

## Change discipline

**Assess before editing.** When a change touches shared state, a wire type, a
service capability or a user-visible flow: list callers, consumers of the same
state, sibling features, reload/replay/restart paths, error and edge states.
A docs, comment or local-only change needs only its own file.

A fix that breaks a neighbouring behaviour is a failed fix.

**Test the neighbourhood.** Run the changed package's tests plus every package
that imports it — not the whole repository. UI changes extend interaction
tests for adjacent functions of the same flow.

**Fix at the owning layer.** No special-case flags, duplicated paths or
workarounds at the symptom site. If the design is duplicated or wrong, make
the smallest unifying change: one path instead of two, deleting what becomes
dead.

## PR hygiene

- Keep the PR diff minimal: only files relevant to the purpose, including the
  deletions and call-site updates the smallest unifying change requires;
  excluding unrelated formatting, renames and other branches' changes.
- Rewrite history only when the user asks; then at most one force-push per
  review round, on the PR's own branch, never on a shared branch.
- Review feedback: one commit per round unless the user asks for a clean
  history. Creating commits and PRs still requires the user's request.

## Delivery workflow

- `main` is protected. Work on a short-lived branch cut from the latest
  `main`, push it, open a pull request; it merges once CI is green, with a
  merge commit, not a squash. The branch is deleted after merge.
- Releases are tagged on `main` after the release commit merges. Routine work
  never bumps versions, creates tags or publishes releases; release actions
  require an explicit user request. See VERSIONING.md and RELEASING.md.
- Do not add generated-by attribution or assistant co-author trailers to
  commits, pull requests or other deliverables.
- Third-party code keeps its license and attribution; Apache-2.0 code
  requires keeping its NOTICE.

## Scope is a contract

The milestone plan owns scope. A feature not in the current milestone goes to
the backlog, not into code. Marketing claims stay inside the published support
matrix; the keep-alive boundary is stated honestly; demotion ladders are
pre-committed, not improvised.