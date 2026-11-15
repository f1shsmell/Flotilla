import { useState } from "react";

import type { Entry, SessionState } from "../store";
import { PermissionCard } from "./PermissionCard";
import { Terminal } from "./Terminal";

interface Props {
  state: SessionState;
  onPrompt: (text: string) => void;
  onCancel: () => void;
  onAnswer: (questionId: string, optionId: string) => void;
  onTerminalReady: (write: (bytes: Uint8Array) => void) => void;
  onInput: (data: string) => void;
  onResize: (cols: number, rows: number) => void;
}

function EntryLine({ entry }: { entry: Entry }): React.ReactElement {
  switch (entry.kind) {
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
        <button type="button" onClick={onCancel} disabled={!working}>
          Cancel turn
        </button>
      </header>

      {state.error ? <p className="error">{state.error}</p> : null}

      {state.permissions.map((permission) => (
        <PermissionCard
          key={permission.id}
          permission={permission}
          onAnswer={onAnswer}
        />
      ))}

      {state.mode === "pty" ? (
        <Terminal onInput={onInput} onResize={onResize} onReady={onTerminalReady} />
      ) : (
        <div className="log">
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
