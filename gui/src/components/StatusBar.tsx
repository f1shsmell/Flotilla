import type React from "react";

import { fleetSummary } from "../fleetView";
import type { Status } from "../store";

// The shell's one global readout: which daemon, how much of the fleet answers,
// and how many sessions are waiting on the user. The sidebar cards stay local.
export function StatusBar({ status }: { status: Status | null }): React.ReactElement {
  const counts = fleetSummary(status);
  return (
    <footer className="status-bar">
      <span className="status-left">
        <span className="status-daemon" title={status ? `daemon pid ${status.pid}` : undefined}>
          {status ? `daemon ${status.pid}` : "Hailing the daemon…"}
        </span>
        {status ? (
          <span className="status-reach">
            {counts.reachable}/{counts.total} reachable
          </span>
        ) : null}
      </span>
      {counts.attention > 0 ? (
        <span className="attention">{counts.attention} need you</span>
      ) : null}
    </footer>
  );
}