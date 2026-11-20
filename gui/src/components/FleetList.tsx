import { dirLabel } from "../fleetView";
import type { Lifecycle, StatusEntry } from "../store";
import { IconStop } from "./Icons";

interface Props {
  sessions: StatusEntry[];
  selected: string | null;
  onSelect: (id: string) => void;
  onStop: (id: string) => void;
}

// lifecycleClass maps a state to the class that colours it. Unknown and idle
// are quiet on purpose: only the states that need the user should draw the eye.
function lifecycleClass(lifecycle: Lifecycle): string {
  switch (lifecycle) {
    case "blocked":
      return "badge blocked";
    case "done":
      return "badge done";
    case "working":
      return "badge working";
    case "idle":
      return "badge idle";
    default:
      return "badge unknown";
  }
}

export function FleetList({
  sessions,
  selected,
  onSelect,
  onStop,
}: Props): React.ReactElement {
  if (sessions.length === 0) {
    return (
      <div className="fleet-empty">
        <p className="fleet-empty-title">Sessions you start will appear here.</p>
        <p className="muted">Pick an agent in the composer card and press Start.</p>
      </div>
    );
  }

  return (
    <ul className="sessions">
      {sessions.map((session, index) => (
        // A row holds two actions, so the row itself is not a button: nested
        // buttons are invalid, and the stop control must not also select.
        <li
          key={session.id}
          className="session-card-wrap"
          style={{ "--i": index } as React.CSSProperties}
        >
          <button
            type="button"
            className={session.id === selected ? "session-row selected" : "session-row"}
            onClick={() => onSelect(session.id)}
          >
            <span className={`card-rail rail-${session.lifecycle}`} aria-hidden="true" />
            <span className="card-main">
              <span className="card-top">
                <span className="session-id">{session.id}</span>
                <span className={lifecycleClass(session.lifecycle)}>
                  <span className="badge-dot" aria-hidden="true" />
                  {session.lifecycle}
                </span>
              </span>
              <span className="card-meta">
                <span className="chip">{session.mode}</span>
                {dirLabel(session.dir) ? (
                  <span className="chip chip-dir" title={session.dir}>
                    {dirLabel(session.dir)}
                  </span>
                ) : null}
                <span className="muted mono card-command" title={session.command}>
                  {session.command}
                </span>
                {session.reachable ? null : (
                  <span className="badge down">unreachable</span>
                )}
              </span>
            </span>
          </button>
          <button
            type="button"
            className="session-stop"
            title={`end ${session.id}`}
            aria-label={`end ${session.id}`}
            onClick={() => onStop(session.id)}
          >
            <IconStop />
            Stop
          </button>
        </li>
      ))}
    </ul>
  );
}
