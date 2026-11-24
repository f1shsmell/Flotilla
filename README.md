# 舰阵 / Flotilla

> Every agent in formation. / 百舸布阵,一人掌舵。

ACP-native fleet manager for AI coding agents. Windows-first. The daemon is a
restartable control plane; each session lives in its own host process, so the
GUI can die, the daemon can die, and your agents keep working.

**Status: pre-alpha.** The first milestone (ACP sessions, worktree
parallelism, PTY fallback, Windows packaging) is built and being verified
against its acceptance; the v0.1 release is in preparation. This repo holds
the code and its discipline.

## Principles

- **ACP-first**: structured sessions for agents that speak the Agent Client
  Protocol; a full-interaction PTY fallback for any other CLI.
- **Sessions outlive the app**: GUI and daemon restarts keep sessions alive.
  Closing the window parks the app in the tray; login autostart (off by
  default) starts it hidden with the fleet warm. Honest boundary: a
  session-host crash loses the session, PTY and ACP alike — the host holds the
  agent's handles. An agent process that exits on its own is not a lost
  session: the host relaunches it and resumes the ACP conversation
  (`session/resume`); a PTY has no handle to resume. When a session does end —
  the app closed overnight, an upgrade, or a host that died — its log stays on
  disk and the fleet lists it under **Finished**: readable, and nothing more.
  A finished run is not resumed, not restarted and cannot be prompted; an OS
  restart restores the layout and that history, and no process is revived.
- **BYO subscription**: no token resale, no model lock-in.
- **Adapters are data**: most new agents are one JSON definition.
- **Replay for demos**: a recorded turn answers from disk, so demos, GIFs and
  smoke runs cost nothing and need no login. `flt replay export <session>`
  turns a real session's log into one.

## Development

- Standing instructions: [FLOTILLA.md](FLOTILLA.md) (pointed to by
  [AGENTS.md](AGENTS.md))
- Conventions and workflow: [CONTRIBUTING.md](CONTRIBUTING.md)
- Versioning: [VERSIONING.md](VERSIONING.md) · Releases:
  [RELEASING.md](RELEASING.md) · Security: [SECURITY.md](SECURITY.md)

## License

[MIT](LICENSE)