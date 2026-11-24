import { useState } from "react";

import { DiffPanel } from "./DiffPanel";
import { LogView } from "./LogView";
import { modeLabel, stopLabel } from "../fleetView";
import type { Choice, Control, Lifecycle, SessionState } from "../store";
import { IconSend, IconShield } from "./Icons";
import { PermissionCard } from "./PermissionCard";
import { Terminal } from "./Terminal";

interface Props {
  state: SessionState;
  // lifecycle is the fleet's word for this session, so pill and fleet card never disagree.
  lifecycle: Lifecycle;
  view: "stream" | "diff";
  onView: (view: "stream" | "diff") => void;
  onPrompt: (text: string) => void;
  onCancel: () => void;
  onSetControl: (id: string, value: string) => void;
  onAnswer: (questionId: string, optionId: string) => void;
  onTerminalReady: (write: (bytes: Uint8Array) => void) => void;
  onInput: (data: string) => void;
  onResize: (cols: number, rows: number) => void;
}

// ControlSelect renders one agent setting. A boolean has no choices on the
// wire — on and off are the renderer's words — and grouped choices rebuild
// their sections from the labels the daemon kept. The control's name is
// visible text: two dials can show the same value, and a bare "Balanced" tells
// the user nothing. A current the agent did not list stays visible as a
// disabled entry rather than vanishing into something the select cannot show,
// and a control that offers no choices at all renders as a read-only chip.
function ControlSelect({
  control,
  onSet,
}: {
  control: Control;
  onSet: (id: string, value: string) => void;
}): React.ReactElement {
  const choices: Choice[] =
    control.kind === "boolean"
      ? [
          { value: "true", name: "On" },
          { value: "false", name: "Off" },
        ]
      : (control.choices ?? []);

  if (control.kind !== "boolean" && choices.length === 0) {
    return (
      <span
        className="surface-control surface-control-static"
        title={`${control.name}: ${control.current} (the agent offers no alternatives)`}
      >
        {control.name}: {control.current}
      </span>
    );
  }

  const listed = choices.some((choice) => choice.value === control.current);
  const loose = choices.filter((choice) => !choice.group);
  const groups: { name: string; choices: Choice[] }[] = [];
  for (const choice of choices) {
    if (!choice.group) {
      continue;
    }
    const existing = groups.find((group) => group.name === choice.group);
    if (existing) {
      existing.choices.push(choice);
    } else {
      groups.push({ name: choice.group, choices: [choice] });
    }
  }

  const currentName =
    control.kind === "boolean"
      ? control.current === "true"
        ? "On"
        : "Off"
      : (choices.find((choice) => choice.value === control.current)?.name || control.current);

  return (
    <label className={`surface-control kind-${control.kind}`}>
      
      <span className="surface-control-name">{control.name}</span>
      <select
        aria-label={control.name}
        title={`${control.name}: ${currentName}`}
        value={control.current}
        onChange={(event) => onSet(control.id, event.target.value)}
      >
        {!listed && control.current !== "" ? (
          <option value={control.current} disabled>
            {currentName} (current)
          </option>
        ) : null}
        {loose.map((choice) => (
          <option key={choice.value} value={choice.value}>
            {choice.name || choice.value}
          </option>
        ))}
        {groups.map((group) => (
          <optgroup key={group.name} label={group.name}>
            {group.choices.map((choice) => (
              <option key={choice.value} value={choice.value}>
                {choice.name || choice.value}
              </option>
            ))}
          </optgroup>
        ))}
      </select>
    </label>
  );
}

export function SessionView({
  state,
  lifecycle,
  view,
  onView,
  onPrompt,
  onCancel,
  onSetControl,
  onAnswer,
  onTerminalReady,
  onInput,
  onResize,
}: Props): React.ReactElement {
  const [draft, setDraft] = useState("");
  // The fold state lives here rather than in LogView: the log is swapped for
  // the diff and back, and a reader's opened runs must survive the trip.
  const [openRuns, setOpenRuns] = useState<ReadonlySet<string>>(new Set());
  const working = state.turn === "working";

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
          <div className="session-title">
            <h2>{state.id}</h2>
            <span className={`turn-pill life-${lifecycle}`}>{lifecycle}</span>
          </div>
          <p className="muted">
            {modeLabel(state.mode)}
            {state.stopReason ? ` · ${stopLabel(state.stopReason)}` : ""}
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
        <LogView
          entries={state.entries}
          more={state.moreHistory}
          empty="Nothing yet. Send a prompt."
          openRuns={openRuns}
          onToggleRun={toggleRun}
        />
      )}

      {state.mode === "acp" ? (
        <div className="session-composer">
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
          {/* The settings bar sits under the field: the selects read as the
              turn's dials beside the send they qualify, not as a second
              header competing with the session's own. */}
          <div className="composer-bar">
            {/* The mode is already in the header line above the log. Repeating
                it here would be the same word twice on one screen, so the bar
                starts with the controls that are actually adjustable. */}
            {state.controls.length > 0 ? (
              <div className="surface-controls">
                {state.controls.map((control) => (
                  <ControlSelect key={control.id} control={control} onSet={onSetControl} />
                ))}
              </div>
            ) : null}
            {state.permissions.length > 0 ? (
              <span
                className="composer-shield"
                aria-label={`${state.permissions.length} pending permission${state.permissions.length === 1 ? "" : "s"}`}
                title={`${state.permissions.length} pending permission${state.permissions.length === 1 ? "" : "s"}`}
              >
                <IconShield />
                {state.permissions.length}
              </span>
            ) : null}
            <span className="muted composer-hint">
              {working
                ? "Send unlocks when the turn ends"
                : "Enter to send · Shift+Enter for newline"}
            </span>
            <button type="button" className="primary" onClick={submit} disabled={working}>
              <IconSend />
              {working ? "Working…" : "Send"}
            </button>
          </div>
        </div>
      ) : null}
    </div>
  );
}
