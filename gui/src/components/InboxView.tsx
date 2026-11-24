import { constants } from "../../electron/contract";
import { dirLabel } from "../fleetView";
import type { InboxEntry } from "../store";
import { IconBack } from "./Icons";

interface Props {
  entries: InboxEntry[];
  // known is false while the fleet cannot be read: an empty list then means
  // unknown, and "Nothing needs you" would be a claim about work nobody saw.
  known: boolean;
  onOpen: (id: string) => void;
  onClose: () => void;
}

// rowState names what the entry's badge says. The kind comes from the daemon;
// the wording belongs to the window, which is the layer that talks to a person.
function rowState(entry: InboxEntry): { badge: string; label: string; title: string } {
  if (entry.kind === constants.daemon.InboxInput) {
    return {
      badge: "blocked",
      label: entry.title !== "" ? "needs input" : "waiting",
      title: entry.title !== "" ? entry.title : "Waiting for your answer",
    };
  }
  return { badge: "done", label: "unread", title: "Turn finished" };
}

function Group({
  heading,
  entries,
  onOpen,
}: {
  heading: string;
  entries: InboxEntry[];
  onOpen: (id: string) => void;
}): React.ReactElement {
  return (
    <section className="inbox-group">
      <h3>{heading}</h3>
      <ul className="inbox-list">
        {entries.map((entry) => {
          const state = rowState(entry);
          return (
            <li key={entry.id}>
              <button type="button" className="inbox-row" onClick={() => onOpen(entry.id)}>
                <span className={`badge ${state.badge}`}>
                  <span className="badge-dot" aria-hidden="true" />
                  {state.label}
                </span>
                <span className="inbox-main">
                  <span className="inbox-title">{state.title}</span>
                  <span className="inbox-meta">
                    <span className="session-id">{entry.id}</span>
                    {dirLabel(entry.dir) !== "" ? (
                      <span className="chip chip-dir" title={entry.dir}>
                        {dirLabel(entry.dir)}
                      </span>
                    ) : null}
                    {entry.command !== "" ? (
                      <span className="muted mono">{entry.command}</span>
                    ) : null}
                  </span>
                </span>
              </button>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

// InboxView is the cross-session list of work waiting on the user: a session
// blocked on an answer, and a finished turn this window has not read. One click
// opens the session, which is the only place the answer can be given.
export function InboxView({ entries, known, onOpen, onClose }: Props): React.ReactElement {
  const waiting = entries.filter((entry) => entry.kind === constants.daemon.InboxInput);
  const unread = entries.filter((entry) => entry.kind === constants.daemon.InboxUnread);

  return (
    <section className="inbox">
      <header>
        <div>
          <h2>Inbox</h2>
          <p className="muted">
            {known
              ? `${waiting.length} waiting on you · ${unread.length} unread`
              : "The fleet cannot be read right now"}
          </p>
        </div>
        <button type="button" onClick={onClose}>
          <IconBack />
          Back
        </button>
      </header>

      {!known ? (
        <p className="muted inbox-empty">
          What waits on you is unknown until the daemon answers again.
        </p>
      ) : entries.length === 0 ? (
        <p className="muted inbox-empty">Nothing needs you.</p>
      ) : null}
      {known && waiting.length > 0 ? (
        <Group heading="Needs your answer" entries={waiting} onOpen={onOpen} />
      ) : null}
      {known && unread.length > 0 ? (
        <Group heading="Unread" entries={unread} onOpen={onOpen} />
      ) : null}
    </section>
  );
}
