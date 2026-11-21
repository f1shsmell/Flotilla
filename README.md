# 舰阵 / Flotilla

> Every agent in formation. / 百舸布阵,一人掌舵。

ACP-native fleet manager for AI coding agents. Windows-first. The daemon is a
restartable control plane; each session lives in its own host process, so the
GUI can die, the daemon can die, and your agents keep working.

**Status: pre-alpha.** The first milestone (ACP sessions, worktree
parallelism, PTY fallback) is under construction. This repo holds the code and
its discipline.

## Principles

- **ACP-first**: structured sessions for agents that speak the Agent Client
  Protocol; a full-interaction PTY fallback for any other CLI.
- **Sessions outlive the app**: GUI and daemon restarts keep sessions alive.
  Honest boundary: a session-host crash loses the session, PTY and ACP alike —
  the host holds the agent's handles. An agent process that exits on its own is
  not a lost session: the host relaunches it and resumes the ACP conversation
  (`session/resume`); a PTY has no handle to resume. OS restarts restore layout
  and history only.
- **BYO subscription**: no token resale, no model lock-in.
- **Adapters are data**: most new agents are one JSON definition.
- **Replay for demos**: a recorded turn answers from disk, so demos, GIFs and
  smoke runs cost nothing and need no login. `flt replay export <session>`
  turns a real session's log into one.

## Development

- Standing instructions: [FLOTILLA.md](FLOTILLA.md) (pointed to by
  [AGENTS.md](AGENTS.md))
- Positioning: [docs/positioning.md](docs/positioning.md)
- Support matrix, verified only: [docs/support-matrix.md](docs/support-matrix.md)
- Remote access and the phone client, design only:
  [docs/remote-access.md](docs/remote-access.md) ·
  [docs/ui-remote-design.md](docs/ui-remote-design.md) ·
  [docs/remote-access-review.md](docs/remote-access-review.md) ·
  [docs/remote-access-open-questions.md](docs/remote-access-open-questions.md)
- Conventions and workflow: [CONTRIBUTING.md](CONTRIBUTING.md)
- Versioning: [VERSIONING.md](VERSIONING.md) · Releases:
  [RELEASING.md](RELEASING.md) · Security: [SECURITY.md](SECURITY.md)

## License

[MIT](LICENSE)