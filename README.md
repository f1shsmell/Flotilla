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
- **Sessions outlive the app**: GUI restart and daemon restart keep sessions
  alive; the honest boundary is documented in the README at release.
- **BYO subscription**: no token resale, no model lock-in.
- **Adapters are data**: most new agents are one JSON definition.

## Development

- Standing instructions: [FLOTILLA.md](FLOTILLA.md) (pointed to by
  [AGENTS.md](AGENTS.md))
- Conventions and workflow: [CONTRIBUTING.md](CONTRIBUTING.md)
- Versioning: [VERSIONING.md](VERSIONING.md) · Releases:
  [RELEASING.md](RELEASING.md) · Security: [SECURITY.md](SECURITY.md)

## License

[MIT](LICENSE)