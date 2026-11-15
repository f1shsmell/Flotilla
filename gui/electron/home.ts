// resolveHome mirrors internal/home: FLT_HOME wins, otherwise the platform
// default for the channel. The GUI must land on the same files the daemon does,
// so the two are kept in step by hand and pinned by a test.

import os from "node:os";
import path from "node:path";

export const ENV_HOME = "FLT_HOME";

export interface Paths {
  root: string;
  sessions: string;
  token: string;
}

export function resolveHome(channel: string, env: NodeJS.ProcessEnv = process.env): string {
  const override = env[ENV_HOME];
  if (override) {
    return path.resolve(override);
  }

  const name = channel === "preview" ? "FlotillaPreview" : "Flotilla";

  switch (process.platform) {
    case "win32": {
      const base = env.LOCALAPPDATA;
      if (!base) {
        throw new Error("LOCALAPPDATA is unset and FLT_HOME is not set");
      }
      return path.join(base, name);
    }
    case "darwin":
      return path.join(os.homedir(), "Library", "Application Support", name);
    default: {
      const stateHome = env.XDG_STATE_HOME;
      if (stateHome) {
        return path.join(stateHome, name);
      }
      return path.join(os.homedir(), ".local", "state", name);
    }
  }
}

export function resolvePaths(channel: string, env: NodeJS.ProcessEnv = process.env): Paths {
  const root = resolveHome(channel, env);
  return {
    root,
    sessions: path.join(root, "sessions"),
    token: path.join(root, "token"),
  };
}

// endpoint is the daemon's address: a named pipe on Windows, a unix socket
// elsewhere, matching ipc.Address.
export function endpoint(channel: string, name: string, sessions: string): string {
  if (process.platform === "win32") {
    return `\\\\.\\pipe\\flotilla-${channel}-${name}`;
  }
  return path.join(sessions, "ipc", `${channel}-${name}.sock`);
}
