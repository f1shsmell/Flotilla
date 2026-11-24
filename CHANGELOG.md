# Changelog

Notable changes, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[VERSIONING.md](VERSIONING.md): before 1.0 a MINOR carries new functionality and
any breaking change, and a PATCH carries fixes only.

Two channels ship from this repository: **stable** (`Flotilla`) and **preview**
(`Flotilla Preview`, versions ending `-preview.N`). Preview builds are not
covered by the compatibility promises in VERSIONING.md.

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
