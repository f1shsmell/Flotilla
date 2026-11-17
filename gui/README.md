# gui

The Electron window. It never spawns an agent and never holds a token: the main
process reads the channel's token, starts the daemon when nobody is listening,
and talks to it over the same pipe the CLI does. The renderer sees only the
calls `electron/preload.ts` exposes.

```
electron/wire.ts     frame codec and JSON-RPC shapes, byte-compatible with internal/ipc
electron/client.ts   one authenticated connection: handshake, calls, notifications, data
electron/daemon.ts   connect, or start the daemon and retry
electron/home.ts     FLT_HOME and the channel's paths, mirroring internal/home
electron/main.ts     window, IPC handlers, one connection per open session
electron/smoke.ts    offscreen run that drives the real UI (FLT_GUI_SMOKE)
src/store.ts         pure folding of the wire into what the view renders
src/App.tsx          one store, components project it; updates flush on a frame
```

## Scripts

```bash
pnpm install
pnpm lint     # tsc, both the renderer and the main process
pnpm test     # vitest; the daemon interop test runs when FLT_DAEMON_BIN is set
pnpm build    # tsc for electron/ and vite for the renderer
pnpm start    # electron ., which loads dist/, so build first
```

## Building an installer

```bash
pnpm dist     # tsc + vite, then the Go binaries, then electron-builder (NSIS)
```

`scripts/bundle-binaries.mjs` builds `flt-daemon` and `flt-session-host` into
`resources/bin`, which electron-builder copies beside the app; the daemon looks
for its session host in its own directory, so the two travel together. An
installed window starts the daemon from `resources/bin` and only falls back to
`PATH` when that is missing.

`dependencies` is empty on purpose: the renderer's packages are bundled by Vite
into `dist/renderer`, so shipping them again in the asar would only make the
installer bigger. A package the **main process** loads at run time has to be in
`dependencies` or it will not travel.

Two things bite a first build:

- electron-builder downloads NSIS and winCodeSign on first use. Behind a slow or
  filtered network, point it at a mirror with
  `ELECTRON_BUILDER_BINARIES_MIRROR=https://npmmirror.com/mirrors/electron-builder-binaries/`.
- pnpm 11 refuses to run a dependency's build script unless it is allowed;
  `gui/pnpm-workspace.yaml` names the two that need it and sets
  `strictDepBuilds: false`. pnpm reads that file from the directory the command
  runs in, which is why the CI job runs inside `gui/` rather than with `--dir`.

The installer is unsigned unless `CSC_LINK` and `CSC_KEY_PASSWORD` are set. An
unsigned build is what CI produces today; `RELEASING.md` records what has to be
true before a signed one ships.

## Environment

| variable | meaning |
| --- | --- |
| `FLT_HOME` | state root, same as the CLI's; must match the daemon's |
| `FLT_CHANNEL` | `stable` or `preview`; default `stable` |
| `FLT_DAEMON_BIN` | daemon to start when none is listening; default `flt-daemon` on `PATH` |
| `FLT_GUI_SMOKE` | render offscreen, drive the UI, print the DOM and exit |
| `FLT_GUI_SMOKE_OPEN` | session id to click before asserting |
| `FLT_GUI_SMOKE_PROMPT` | prompt to type and send |
| `FLT_GUI_SMOKE_EXPECT` | text that must appear before the smoke passes |
| `FLT_GUI_SMOKE_TERMINAL` | text that must reach the terminal's buffer |
| `FLT_GUI_SMOKE_START` | JSON start request to send through the bridge |
| `FLT_GUI_SMOKE_FORM` | JSON form fields: `agent` (a picker name) or `command`/`args`, plus `mode`, `dir`, `repo`, `branch` |
| `FLT_GUI_SMOKE_AGENTS` | agent display name whose Enabled switch is flipped on the settings page, then flipped back |
| `FLT_GUI_SMOKE_DIFF` | file to open in the diff panel |
| `FLT_GUI_SMOKE_PATCH` | added line to find in that file's patch |
| `FLT_GUI_SMOKE_NOTE` | note to leave on that line |

`FLT_GUI_SMOKE_FORM` and `FLT_GUI_SMOKE_AGENTS` are the two paths that walk this
build's agent catalogue: the first picks an agent by name in the new-session
form, the second opens the settings page and flips a switch. Either needs an
agent the daemon can actually start; an adapter file under
`$FLT_HOME/config/adapters` pointing at `flt-fake-acp-agent` is the cheap way to
get one without spending a real provider's tokens.

A headless run against a real daemon and a real agent:

```bash
go build -o /tmp/flt-daemon.exe ./cmd/flt-daemon
FLT_HOME=/tmp/flt-home /tmp/flt-daemon.exe --channel demo &
FLT_HOME=/tmp/flt-home FLT_CHANNEL=demo FLT_DAEMON_BIN=/tmp/flt-daemon.exe \
  FLT_GUI_SMOKE=1 FLT_GUI_SMOKE_OPEN=<session> \
  FLT_GUI_SMOKE_PROMPT="Reply with exactly: flotilla-ok" \
  FLT_GUI_SMOKE_EXPECT="flotilla-ok" pnpm start
```

## Two things that waste an afternoon

- If `electron .` complains that `app` is undefined, the shell has
  `ELECTRON_RUN_AS_NODE=1` set (IDEs built on Electron export it). Run with
  `env -u ELECTRON_RUN_AS_NODE`.
- `pnpm install` may skip the Electron binary download; if `dist/` is missing
  under `node_modules/.pnpm/electron*/node_modules/electron/`, run
  `node node_modules/.pnpm/electron*/node_modules/electron/install.js`.
