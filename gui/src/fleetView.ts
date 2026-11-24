// Pure projections of the fleet for the window's chrome. The store stays the
// wire model; these read it the way the sidebar and the empty harbor do.

import { constants } from "../electron/contract";
import { needsAttention, type Lifecycle, type Status } from "./store";

export interface FleetSummary {
  total: number;
  reachable: number;
  attention: number;
  // archived counts finished runs. They are not sessions: the summary keeps
  // them apart so a footer cannot read "3 sessions" when one of them is a log.
  archived: number;
}

export function fleetSummary(status: Status | null): FleetSummary {
  if (status === null) {
    return { total: 0, reachable: 0, attention: 0, archived: 0 };
  }
  return {
    total: status.sessions.length,
    reachable: status.sessions.filter((session) => session.reachable).length,
    attention: status.sessions.filter((session) => needsAttention(session.lifecycle)).length,
    archived: status.archived.length,
  };
}

// windowTitle names the window per channel: the installed preview must read
// differently from stable in the taskbar and in alt-tab, where neither the
// header badge nor the tray label travels. An unknown channel still says
// Flotilla.
export function windowTitle(channel: string | null): string {
  if (channel === null || channel === "" || channel === "stable") {
    return "Flotilla";
  }
  if (channel === "preview") {
    return "Flotilla Preview";
  }
  return `Flotilla ${channel}`;
}

// dirLabel is the last segment of a working directory, Windows or POSIX: the
// card names the project, the full path stays in the tooltip.
export function dirLabel(dir: string): string {
  const trimmed = dir.replace(/[\\/]+$/, "");
  if (trimmed === "") {
    return "";
  }
  const cut = Math.max(trimmed.lastIndexOf("/"), trimmed.lastIndexOf("\\"));
  return cut >= 0 ? trimmed.slice(cut + 1) : trimmed;
}

// mainEmpty picks the harbor's greeting: a fleet with sessions asks the user to
// pick one, an absent or empty fleet says the harbor is quiet.
export function mainEmpty(status: Status | null): "quiet" | "pick" {
  return status !== null && status.sessions.length > 0 ? "pick" : "quiet";
}

// watchedFinish is whether a lifecycle change belongs to the session the pane
// shows finishing: only then is the turn seen for this window. A done event for
// a session the user is not looking at keeps its unread badge.
export function watchedFinish(id: string, watching: string | null, lifecycle: unknown): boolean {
  return id === watching && lifecycle === "done";
}

// modeLabel names a session's protocol in the window's words. "acp" and "pty"
// are what crosses the wire; a person reads "Chat" and "Terminal", the same
// words the mode switch and the view buttons already use. One concept, one
// word, everywhere.
export function modeLabel(mode: string): string {
  if (mode === "pty") {
    return "Terminal";
  }
  if (mode === "acp") {
    return "Chat";
  }
  return mode === "" ? "Unknown" : mode;
}

// stopLabel names why a turn ended. The agent reports these in the protocol's
// spelling; an unknown reason is shown as it came, because inventing a friendlier
// one would be a claim about what happened.
const stopLabels: Record<string, string> = {
  end_turn: "finished",
  cancelled: "cancelled",
  refused: "refused",
  max_tokens: "hit the token limit",
  max_turn_requests: "hit the request limit",
};

export function stopLabel(reason: string): string {
  return stopLabels[reason] ?? reason;
}

// subagentStateLabel names what a child session is doing. The agent reports the
// protocol's spelling, where "running" is the fleet's "working"; a state this
// build does not know is shown as it came.
const subagentStateLabels: Record<string, string> = {
  running: "working",
  idle: "idle",
  requires_action: "needs input",
  unknown: "unknown",
};

export function subagentStateLabel(state: string): string {
  return subagentStateLabels[state] ?? state;
}

// promptRoute is where the composer's Send travels. An agent that advertised
// steering takes a message into the running turn; one that did not queues it —
// ACP's core has no mid-turn delivery, so a second prompt is the agent's call.
export function promptRoute(
  mode: string,
  lifecycle: Lifecycle,
  steer = false,
): "queue" | "steer" | "prompt" {
  if (mode !== "acp") {
    return "prompt";
  }
  if (lifecycle === "working") {
    return steer ? "steer" : "queue";
  }
  return lifecycle === "blocked" ? "queue" : "prompt";
}

// deliveryLabel names why a queued message could not be delivered. The daemon
// sends the identity; an unknown one is shown as it came, like a stop reason.
export function deliveryLabel(reason: string): string {
  if (reason === constants.session.DeliveryHostUnreachable) {
    return "the host is unreachable";
  }
  if (reason === constants.session.DeliveryPromptFailed) {
    return "the turn could not start";
  }
  return reason;
}

// scopeLabel names what a diff compares. The wire says "unstaged", "branch" and
// "turn"; a person reads what the comparison is, in the words the review uses.
const scopeLabels: Record<string, string> = {
  unstaged: "Uncommitted changes",
  branch: "The whole branch",
  turn: "Since I started",
};

export function scopeLabel(scope: string): string {
  return scopeLabels[scope] ?? scope;
}

// fileStatusLabel names what happened to one file. git reports these in its own
// vocabulary; an unknown status is shown as it came rather than dropped.
const fileStatusLabels: Record<string, string> = {
  modified: "modified",
  added: "added",
  deleted: "deleted",
  renamed: "renamed",
  copied: "copied",
  untracked: "new",
  typechange: "type changed",
};

export function fileStatusLabel(status: string): string {
  return fileStatusLabels[status] ?? status;
}
