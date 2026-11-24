import { useEffect, useState } from "react";

import { applyHistory, newSession, type ArchivedEntry, type SessionState } from "../store";
import { dirLabel, modeLabel } from "../fleetView";
import { LogView } from "./LogView";
import { IconBack } from "./Icons";

// ArchiveView shows a run that is over. It reads the daemon's stored log and
// offers nothing else: no composer, no stop, no diff. A button here would be a
// promise about a process that no longer exists.
interface Props {
  entry: ArchivedEntry;
  onBack: () => void;
}

interface ArchiveAnswer {
  session?: unknown;
  updates?: unknown;
  more?: unknown;
}

// runTime renders one bound of a run. An empty value is a time nothing
// recorded, and saying so is the difference between an unknown and a claim.
function runTime(value: string): string {
  if (value === "") {
    return "unknown";
  }
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    return "unknown";
  }
  return parsed.toLocaleString();
}

export function ArchiveView({ entry, onBack }: Props): React.ReactElement {
  const [log, setLog] = useState<SessionState | null>(null);
  const [error, setError] = useState<string | null>(null);
  // Fold state belongs to this view for the same reason it belongs to the live
  // session view: a finished log is read, and the folds a reader opens are
  // theirs to keep while the view is mounted.
  const [openRuns, setOpenRuns] = useState<ReadonlySet<string>>(new Set());

  const toggleRun = (key: string, open: boolean): void => {
    setOpenRuns((current) => {
      const next = new Set(current);
      if (open) {
        next.add(key);
      } else {
        next.delete(key);
      }
      return next;
    });
  };

  // The log is read once per session: it cannot change, because nothing writes
  // to a finished run. A failure is reported as itself rather than as an empty
  // log, which would look like a session with no history.
  useEffect(() => {
    let cancelled = false;
    setLog(null);
    setError(null);

    void (async () => {
      try {
        const answer = (await window.flotilla.archive(entry.id)) as ArchiveAnswer;
        if (cancelled) {
          return;
        }
        setLog(
          applyHistory(newSession(entry.id, entry.mode || "acp"), {
            updates: answer.updates,
            more: answer.more === true,
          }),
        );
      } catch (cause) {
        if (!cancelled) {
          setError(cause instanceof Error ? cause.message : String(cause));
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [entry.id, entry.mode]);

  return (
    <div className="session-view archive-view">
      <header className="session-header">
        <div>
          <div className="session-title">
            <h2>{entry.id}</h2>
            {/* The pill is the honest part: this is not a lifecycle, and the
                word "ended" says nothing is listening. */}
            <span className="turn-pill life-ended">ended</span>
          </div>
          <p className="muted">
            {entry.mode ? modeLabel(entry.mode) : "Unknown"}
            {entry.command ? ` · ${entry.command}` : ""}
            {dirLabel(entry.dir) ? ` · ${dirLabel(entry.dir)}` : ""}
          </p>
          <p className="muted archive-bounds">
            started {runTime(entry.started)} · ended {runTime(entry.ended)}
          </p>
        </div>
        <div className="session-actions">
          <button type="button" onClick={onBack}>
            <IconBack />
            Back
          </button>
        </div>
      </header>

      {/* Said before the log, because it is the one thing a reader must know:
          this view is a record, and pressing anything will not restart it. */}
      <p className="banner banner-warn archive-notice" role="note">
        This session has ended. Its log is a record; the agent will not resume and
        nothing here can be continued.
      </p>

      {error ? <p className="error">{error}</p> : null}

      {log ? (
        <LogView
          entries={log.entries}
          more={log.moreHistory}
          empty="No stored updates."
          openRuns={openRuns}
          onToggleRun={toggleRun}
        />
      ) : error ? null : (
        <p className="muted">Reading the log…</p>
      )}
    </div>
  );
}
