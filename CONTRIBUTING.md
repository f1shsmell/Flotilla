# Contributing to Flotilla

Flotilla (舰阵) is an ACP-native fleet manager for AI coding agents. This guide
covers the conventions the maintainers follow; the standing instructions for
agent sessions live in [FLOTILLA.md](FLOTILLA.md).

## Prerequisites

- **Go** (version pinned by `go.mod`; pure-Go dependencies keep cross-compilation)
- **Node.js 24+ and pnpm** — for the Electron/TS GUI
- **Git**
- Windows 10/11 is the primary development platform; macOS/Linux builds are
  kept compiling but are not the dogfooding target yet.

## Project structure

| Directory | Purpose |
|-----------|---------|
| `cmd/flt` | CLI entry point |
| `cmd/flt-daemon` + `internal/` | Control plane: session routing, worktrees, orchestration, state store |
| `cmd/flt-session-host` | Per-session handle holder: ConPTY / ACP stdio, survives daemon restart |
| `gui/` | Electron + TS client; never spawns agents |
| `adapters/` | JSON adapter definitions (data, not code) |
| `docs/` | Engineering docs |

Dependency direction: `gui → daemon → session-host`. Contract types live in one
Go package; TS types are generated from it (tygo/go2ts) or held by a wire-parity
gate. The GUI talks to the daemon over named pipes (JSON-RPC + token); the
daemon talks to each session host the same way. PTY bytes ride a separate
binary-framed data plane.

## Development workflow

```bash
go build ./...
go test ./...
gofmt -l .
golangci-lint run
pnpm --dir gui install && pnpm --dir gui test
```

### Isolated development environment

A source-built binary launched with `FLT_HOME` set shares no on-disk state
with a stable install: config, credentials, sessions and cache all live under
that directory, and fallback paths into the real home are skipped.

```powershell
$env:FLT_HOME = "C:\tmp\flt-dev"; go run ./cmd/flt-daemon
```

### Pre-push checks

Run the gates in [FLOTILLA.md](FLOTILLA.md#pre-push-ci-simulation) before
pushing. Docs-only changes may skip them and say so. CI runs the same gates on
Windows and skips docs-only changes via paths filters; the workflows land with
the first code.

## Commit convention

```
<type>(<scope>): <subject>
```

Types: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `chore`,
`ci`, `build`. Scopes name the package or area: `daemon`, `host`, `gui`,
`adapters`, `worktree`, `ipc`, `release`.

Rules:

- English, lowercase subject, imperative mood, no trailing period.
- The subject states the change **and its why** — a full sentence is fine.
  The incident, evidence and mechanism detail belong in the body, not in code
  comments.
- One feature point per commit. No generated-by or co-author trailers.

Examples:

```
feat(host): a session host holds the ConPTY so the daemon can die and reconnect: handles duplicated at spawn, pipe name registered for rediscovery
fix(ipc): a reconnecting daemon adopts the host's live turn instead of replaying it: replay double-fired prompts the agent had already answered
docs: contributing guide names the pre-push gates
```

## Submitting changes

1. `main` is protected — never commit or push to it directly. Cut a
   short-lived branch from the latest `main`.
2. Make the change with its test: a behavior change writes the failing test
   first; it must fail without the change.
3. Run the pre-push gates.
4. Open a pull request into `main`. It merges once CI is green, **with a merge
   commit, not a squash**; the branch is deleted after merge.

**Issue first for features.** A change that adds behavior rather than fixing a
defect links an issue the maintainers agreed to before work starts. A fix for a
reproducible defect may open directly, with the failing test in the PR.
Docs-only and test-only PRs are reviewed at lower priority.

**Every PR carries the template's sections**: Cause, Blast radius,
Neighbouring behaviours tested, Why this layer. UI changes add before/after
screenshots (light and dark, wide and narrow) and list entries moved, hidden
or removed; hiding or removing a visible control needs a linked issue and a
release-notes line.

**Sensitive paths** (declared in FLOTILLA.md) get full review plus security
review regardless of author.

## Reporting issues

Open an issue with:

- Flotilla version (or commit) and channel (stable/preview)
- Windows build, agent CLI and version involved
- Steps to reproduce, expected vs actual behavior
- Relevant logs (`FLT_HOME` logs directory) — redact credentials

## License

By contributing, you agree that your contributions are licensed under the
[MIT License](LICENSE) that covers the project (inbound = outbound). You retain
copyright in your contribution. If a contribution includes third-party code,
identify it and its license so it can be attributed correctly.