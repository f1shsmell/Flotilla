import { useEffect, useState } from "react";

import { dirLabel, modeLabel } from "../fleetView";
import type { ArchivedEntry, Lifecycle, StatusEntry } from "../store";
import { IconStop } from "./Icons";

interface Props {
  sessions: StatusEntry[];
  // archived are the runs that are over. They render in their own section and
  // never carry a stop control: nothing is running to stop.
  archived: ArchivedEntry[];
  selected: string | null;
  onSelect: (id: string) => void;
  onStop: (id: string) => void;
  onSelectArchived: (id: string) => void;
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

// StopButton ends a session. Ending one is the single action in this window
// that loses work — the host goes away and the log is all that is left — so it
// arms first: one click turns the control into a question, and only a second
// click on the armed control answers it. Moving away, pressing Escape, or four
// seconds passing takes the question back, so a stray click costs nothing.
function StopButton({
  id,
  onStop,
}: {
  id: string;
  onStop: (id: string) => void;
}): React.ReactElement {
  const [armed, setArmed] = useState(false);

  useEffect(() => {
    if (!armed) {
      return;
    }
    const timer = setTimeout(() => setArmed(false), 4000);
    const onKey = (event: KeyboardEvent): void => {
      if (event.key === "Escape") {
        setArmed(false);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => {
      clearTimeout(timer);
      window.removeEventListener("keydown", onKey);
    };
  }, [armed]);

  if (!armed) {
    return (
      <button
        type="button"
        className="session-stop"
        title={`end ${id}`}
        aria-label={`end ${id}`}
        onClick={() => setArmed(true)}
      >
        <IconStop />
        Stop
      </button>
    );
  }

  return (
    <>
      {/* The quiet control keeps its place while the question is up. Hidden
          rather than removed: the row's second column is sized by its content,
          so removing it would let the card grow — and a row that widens under
          the cursor moves the button the user is aiming at. */}
      <button
        type="button"
        className="session-stop session-stop-ghost"
        aria-hidden="true"
        tabIndex={-1}
      >
        <IconStop />
        Stop
      </button>
      <span className="session-stop-confirm">
        <button
          type="button"
          className="session-stop armed"
          title={`confirm ending ${id}`}
          aria-label={`confirm ending ${id}`}
          onClick={() => onStop(id)}
        >
          End it?
        </button>
        <button
          type="button"
          className="session-stop-cancel"
          title="keep it running"
          aria-label={`keep ${id} running`}
          onClick={() => setArmed(false)}
        >
          Keep
        </button>
      </span>
    </>
  );
}

export function FleetList({
  sessions,
  archived,
  selected,
  onSelect,
  onStop,
  onSelectArchived,
}: Props): React.ReactElement {
  // An empty fleet with finished runs is not an empty window: the archive is
  // the only thing a restarted app has to show, and hiding it behind "sessions
  // you start will appear here" would be a lie about work that happened.
  if (sessions.length === 0 && archived.length === 0) {
    return (
      <div className="fleet-empty">
        <p className="fleet-empty-title">Sessions you start will appear here.</p>
        <p className="muted">Pick an agent in the composer card and press Start.</p>
      </div>
    );
  }

  return (
    <>
      {sessions.length > 0 ? (
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
                    <span className="chip">{modeLabel(session.mode)}</span>
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
              <StopButton id={session.id} onStop={onStop} />
            </li>
          ))}
        </ul>
      ) : null}

      {/* Finished runs sit below the live fleet, under their own heading. They
          are not sessions: the card has no lifecycle and no stop, and opening
          one reads a log. */}
      {archived.length > 0 ? (
        <section className="archive" aria-label="Finished sessions">
          <h2 className="archive-heading">Finished</h2>
          <p className="muted archive-caption">
            These ended. Opening one reads its log; it will not resume.
          </p>
          <ul className="sessions archive-list">
            {archived.map((run, index) => (
              <li
                key={run.id}
                className="session-card-wrap"
                style={{ "--i": index } as React.CSSProperties}
              >
                <button
                  type="button"
                  className={run.id === selected ? "session-row archived selected" : "session-row archived"}
                  onClick={() => onSelectArchived(run.id)}
                >
                  <span className="card-rail rail-ended" aria-hidden="true" />
                  <span className="card-main">
                    <span className="card-top">
                      <span className="session-id">{run.id}</span>
                      <span className="badge ended">ended</span>
                    </span>
                    <span className="card-meta">
                      {run.mode ? <span className="chip">{modeLabel(run.mode)}</span> : null}
                      {dirLabel(run.dir) ? (
                        <span className="chip chip-dir" title={run.dir}>
                          {dirLabel(run.dir)}
                        </span>
                      ) : null}
                      {run.command ? (
                        <span className="muted mono card-command" title={run.command}>
                          {run.command}
                        </span>
                      ) : null}
                    </span>
                  </span>
                </button>
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </>
  );
}
