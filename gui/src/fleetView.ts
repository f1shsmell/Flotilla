// Pure projections of the fleet for the window's chrome. The store stays the
// wire model; these read it the way the sidebar and the empty harbor do.

import { needsAttention, type Status } from "./store";

export interface FleetSummary {
  total: number;
  reachable: number;
  attention: number;
}

export function fleetSummary(status: Status | null): FleetSummary {
  if (status === null) {
    return { total: 0, reachable: 0, attention: 0 };
  }
  return {
    total: status.sessions.length,
    reachable: status.sessions.filter((session) => session.reachable).length,
    attention: status.sessions.filter((session) => needsAttention(session.lifecycle)).length,
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
