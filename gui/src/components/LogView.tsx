import { foldLog, runObjects, type ToolRun } from "../eventView";
import type { Entry } from "../store";
import { IconMessage, IconPlan, IconThought, IconTool, IconUser } from "./Icons";

// The log is one projection: a live session and a finished one render the same
// stored updates, so both read through this file. A second renderer would drift
// the moment one of them learned about a new entry kind.

function KindIcon({ kind }: { kind: Entry["kind"] }): React.ReactElement {
  switch (kind) {
    case "user":
      return <IconUser />;
    case "thought":
      return <IconThought />;
    case "tool":
      return <IconTool />;
    case "plan":
      return <IconPlan />;
    default:
      return <IconMessage />;
  }
}

export function EntryLine({ entry }: { entry: Entry }): React.ReactElement {
  switch (entry.kind) {
    case "user":
      // The person's own words: the only line in the log the agent did not
      // produce, so it reads as a distinct turn rather than agent output.
      return (
        <p className="entry user">
          <KindIcon kind={entry.kind} />
          {entry.text}
        </p>
      );
    case "message":
      return (
        <p className="entry message">
          <KindIcon kind={entry.kind} />
          {entry.text}
        </p>
      );
    case "thought":
      return (
        <p className="entry thought">
          <KindIcon kind={entry.kind} />
          {entry.text}
        </p>
      );
    case "tool":
      return (
        <p className="entry tool">
          <KindIcon kind={entry.kind} />
          <span className="mono">{entry.toolTitle || entry.key}</span>
          {entry.toolStatus ? <span className="muted"> · {entry.toolStatus}</span> : null}
        </p>
      );
    default:
      return (
        <p className="entry other">
          <KindIcon kind={entry.kind} />
          <span className="muted">{entry.kind}</span> {entry.text}
        </p>
      );
  }
}

// A run of tool calls reads as one event with its objects named on the summary;
// opening it reveals each call. Native details keeps the fold cheap, and the
// open flag lives in the view so a switch to the diff and back does not reset
// every fold the reader opened.
function ToolRunRow({
  run,
  open,
  onToggle,
}: {
  run: ToolRun;
  open: boolean;
  onToggle: (key: string, open: boolean) => void;
}): React.ReactElement {
  return (
    <details
      className="entry-run"
      open={open}
      onToggle={(event) => onToggle(run.key, event.currentTarget.open)}
    >
      <summary>
        <IconTool />
        <span className="run-verb">{run.entries.length} tool calls</span>
        {runObjects(run).map((name) => (
          <span key={name} className="chip run-object mono">
            {name}
          </span>
        ))}
      </summary>
      <div className="run-body">
        {run.entries.map((entry) => (
          <EntryLine key={entry.key} entry={entry} />
        ))}
      </div>
    </details>
  );
}

// LogView renders a session's stored updates. `more` admits the log starts
// mid-run: presenting its first line as the run's first line would be a claim
// about work nobody can see.
//
// The fold state is the caller's, not this component's: a live session swaps
// this pane for the diff and back, and a component that owned the state would
// forget every fold the reader opened on the way.
export function LogView({
  entries,
  more,
  empty,
  openRuns,
  onToggleRun,
}: {
  entries: Entry[];
  more: boolean;
  empty: string;
  openRuns: ReadonlySet<string>;
  onToggleRun: (key: string, open: boolean) => void;
}): React.ReactElement {
  return (
    <div className="log">
      {more ? <p className="muted log-more">Earlier updates are not shown.</p> : null}
      {entries.length === 0 ? (
        <p className="empty">{empty}</p>
      ) : (
        foldLog(entries).map((row) =>
          row.kind === "run" ? (
            <ToolRunRow
              key={row.key}
              run={row}
              open={openRuns.has(row.key)}
              onToggle={onToggleRun}
            />
          ) : (
            <EntryLine key={row.entry.key} entry={row.entry} />
          ),
        )
      )}
    </div>
  );
}
