import { useState } from "react";

import { DiffPanel } from "./DiffPanel";
import { foldLog, runObjects, type ToolRun } from "../eventView";
import type { Choice, Control, Entry, Lifecycle, SessionState } from "../store";
import {
  IconMessage,
  IconPlan,
  IconSend,
  IconShield,
  IconThought,
  IconTool,
  IconUser,
} from "./Icons";
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
// their sections from the labels the daemon kept.
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
      <select
        aria-label={control.name}
        title={`${control.name}: ${currentName}`}
        value={control.current}
        onChange={(event) => onSet(control.id, event.target.value)}
      >
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

function EntryLine({ entry }: { entry: Entry }): React.ReactElement {
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
            foldLog(state.entries).map((row) =>
              row.kind === "run" ? (
                <ToolRunRow
                  key={row.key}
                  run={row}
                  open={openRuns.has(row.key)}
                  onToggle={toggleRun}
                />
              ) : (
                <EntryLine key={row.entry.key} entry={row.entry} />
              ),
            )
          )}
        </div>
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
            <span className="chip composer-mode">{state.mode}</span>
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
