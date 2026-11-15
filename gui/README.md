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
