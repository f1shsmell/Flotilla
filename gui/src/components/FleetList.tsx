import type { StatusEntry } from "../store";

interface Props {
  sessions: StatusEntry[];
  selected: string | null;
  onSelect: (id: string) => void;
}

export function FleetList({ sessions, selected, onSelect }: Props): React.ReactElement {
  if (sessions.length === 0) {
    return <p className="empty">No sessions on this channel.</p>;
  }

  return (
    <ul className="sessions">
      {sessions.map((session) => (
        <li key={session.id}>
          <button
            type="button"
            className={session.id === selected ? "session-row selected" : "session-row"}
            onClick={() => onSelect(session.id)}
          >
            <span className="session-id">{session.id}</span>
            <span className={`badge ${session.reachable ? "ok" : "down"}`}>
              {session.reachable ? "reachable" : "unreachable"}
            </span>
            <span className="muted">{session.mode}</span>
            <span className="muted mono">{session.command}</span>
          </button>
        </li>
      ))}
    </ul>
  );
}
