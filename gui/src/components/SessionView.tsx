import { useState } from "react";

import { DiffPanel } from "./DiffPanel";

import type { Entry, SessionState } from "../store";
import { PermissionCard } from "./PermissionCard";
import { Terminal } from "./Terminal";

interface Props {
  state: SessionState;
  view: "stream" | "diff";
  onView: (view: "stream" | "diff") => void;
  onPrompt: (text: string) => void;
  onCancel: () => void;
  onAnswer: (questionId: string, optionId: string) => void;
  onTerminalReady: (write: (bytes: Uint8Array) => void) => void;
  onInput: (data: string) => void;
  onResize: (cols: number, rows: number) => void;
}

function EntryLine({ entry }: { entry: Entry }): React.ReactElement {
  switch (entry.kind) {
    case "user":
      // The person's own words: the only line in the log the agent did not
      // produce, so it reads as a distinct turn rather than agent output.
      return <p className="entry user">{entry.text}</p>;
    case "message":
      return <p className="entry message">{entry.text}</p>;
    case "thought":
      return <p className="entry thought">{entry.text}</p>;
    case "tool":
      return (
        <p className="entry tool">
          <span className="mono">{entry.toolTitle || entry.key}</span>
          {entry.toolStatus ? <span className="muted"> · {entry.toolStatus}</span> : null}
        </p>
      );
    default:
      return (
        <p className="entry other">
          <span className="muted">{entry.kind}</span> {entry.text}
        </p>
      );
  }
}

export function SessionView({
  state,
  view,
  onView,
  onPrompt,
  onCancel,
  onAnswer,
  onTerminalReady,
  onInput,
  onResize,
}: Props): React.ReactElement {
  const [draft, setDraft] = useState("");
  const working = state.turn === "working";

  const submit = (): void => {
    const text = draft.trim();
    if (text === "" || working) {
      return;
    }
    setDraft("");
    onPrompt(text);
  };

  return (
    <div className="session-view">
      <header className="session-header">
        <div>
          <h2>{state.id}</h2>
          <p className="muted">
            {state.mode} · {state.turn}
            {state.stopReason ? ` · ${state.stopReason}` : ""}
          </p>
        </div>
        <div className="session-actions">
          <button
            type="button"
            className={view === "stream" ? "primary" : ""}
            onClick={() => onView("stream")}
          >
            {state.mode === "pty" ? "Terminal" : "Log"}
          </button>
          <button
            type="button"
            className={view === "diff" ? "primary" : ""}
            onClick={() => onView("diff")}
          >
            Diff
          </button>
          <button type="button" onClick={onCancel} disabled={!working}>
            Cancel turn
          </button>
        </div>
      </header>

      {state.error ? <p className="error">{state.error}</p> : null}

      {state.permissions.map((permission) => (
        <PermissionCard
          key={permission.id}
          permission={permission}
          onAnswer={onAnswer}
        />
      ))}

      {/* The terminal is keyed by session: a reused xterm would keep the
          previous session's screen and never call onReady again, so the next
          session's replay would wait in the renderer forever. It also stays
          mounted while the diff panel shows, because unmounting it would throw
          away scrollback the host has no way to replay without an attach.
          The pane is moved out of the way rather than hidden: xterm measures
          its container, and display:none would leave it with no size. */}
      {state.mode === "pty" ? (
        <div
          className={
            view === "stream" ? "terminal-pane" : "terminal-pane terminal-pane-behind"
          }
          aria-hidden={view !== "stream"}
        >
          <Terminal
            key={state.id}
            onInput={onInput}
            onResize={onResize}
            onReady={onTerminalReady}
          />
        </div>
      ) : null}

      {view === "diff" ? (
        <DiffPanel sessionId={state.id} />
      ) : state.mode === "pty" ? null : (
        <div className="log">
          {/* The log can start mid-turn: the daemon keeps a bounded transcript,
              and presenting the first line it has as the session's first line
              would be a claim about work nobody can see. */}
          {state.moreHistory ? (
            <p className="muted log-more">Earlier updates are not shown.</p>
          ) : null}
          {state.entries.length === 0 ? (
            <p className="empty">Nothing yet. Send a prompt.</p>
          ) : (
            state.entries.map((entry) => <EntryLine key={entry.key} entry={entry} />)
          )}
        </div>
      )}

      {state.mode === "acp" ? (
        <div className="composer">
          <textarea
            value={draft}
            placeholder="Prompt the agent…"
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter" && !event.shiftKey) {
                event.preventDefault();
                submit();
              }
            }}
          />
          <button type="button" className="primary" onClick={submit} disabled={working}>
            {working ? "Working…" : "Send"}
          </button>
        </div>
      ) : null}
    </div>
  );
}
