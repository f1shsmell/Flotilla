import { useState } from "react";

import { DiffPanel } from "./DiffPanel";
import { LogView } from "./LogView";
import {
  deliveryLabel,
  modeLabel,
  promptRoute,
  stopLabel,
  subagentStateLabel,
} from "../fleetView";
import type {
  Choice,
  Control,
  Lifecycle,
  QueuedMessage,
  SessionState,
  Subagent,
} from "../store";
import { IconSend, IconShield } from "./Icons";
import { PermissionCard } from "./PermissionCard";
import { Terminal } from "./Terminal";

interface Props {
  state: SessionState;
  // lifecycle is the fleet's word for this session, so pill and fleet card never disagree.
  lifecycle: Lifecycle;
  // protected is the daemon's pin: this session is never reclaimed as idle.
  protected: boolean;
  // steer is the agent's capability: a message typed mid-turn reaches the turn.
  steer: boolean;
  view: "stream" | "diff";
  onView: (view: "stream" | "diff") => void;
  onPrompt: (text: string) => void;
  onCancel: () => void;
  onSetControl: (id: string, value: string) => void;
  onAnswer: (questionId: string, optionId: string) => void;
  onQueueEdit: (id: string, text: string) => void;
  onQueueRemove: (id: string) => void;
  onQueueMove: (id: string, to: number) => void;
  onProtect: (protectedSession: boolean) => Promise<void> | void;
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

// QueueStrip is the messages waiting for the running turn to end. The daemon
// owns their order: the strip asks it to change one and renders its answer.
function QueueStrip({
  messages,
  onEdit,
  onRemove,
  onMove,
}: {
  messages: QueuedMessage[];
  onEdit: (id: string, text: string) => void;
  onRemove: (id: string) => void;
  onMove: (id: string, to: number) => void;
}): React.ReactElement {
  const [editing, setEditing] = useState<string | null>(null);
  const [draft, setDraft] = useState("");

  const save = (id: string): void => {
    const text = draft.trim();
    if (text === "") {
      return;
    }
    onEdit(id, text);
    setEditing(null);
  };

  return (
    <section className="session-queue" aria-label="Queued messages">
      <div className="queue-head">Queued for the next turn</div>
      <ol className="queue-list">
        {messages.map((message, index) => (
          <li key={message.id} className="queue-row">
            {editing === message.id ? (
              <>
                <textarea
                  className="queue-edit"
                  aria-label={`Edit queued message ${index + 1}`}
                  value={draft}
                  onChange={(event) => setDraft(event.target.value)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter" && !event.shiftKey) {
                      event.preventDefault();
                      save(message.id);
                    }
                    if (event.key === "Escape") {
                      setEditing(null);
                    }
                  }}
                />
                <span className="queue-actions">
                  <button type="button" className="link" onClick={() => save(message.id)}>
                    Save
                  </button>
                  <button type="button" className="link" onClick={() => setEditing(null)}>
                    Cancel
                  </button>
                </span>
              </>
            ) : (
              <>
                <span className="queue-text">{message.text}</span>
                {message.error ? (
                  <span className="queue-error">{deliveryLabel(message.error)}</span>
                ) : null}
                <span className="queue-actions">
                  <button
                    type="button"
                    className="link"
                    aria-label={`Move queued message ${index + 1} up`}
                    disabled={index === 0}
                    onClick={() => onMove(message.id, index - 1)}
                  >
                    Up
                  </button>
                  <button
                    type="button"
                    className="link"
                    aria-label={`Move queued message ${index + 1} down`}
                    disabled={index === messages.length - 1}
                    onClick={() => onMove(message.id, index + 1)}
                  >
                    Down
                  </button>
                  <button
                    type="button"
                    className="link"
                    onClick={() => {
                      setEditing(message.id);
                      setDraft(message.text);
                    }}
                  >
                    Edit
                  </button>
                  <button type="button" className="link" onClick={() => onRemove(message.id)}>
                    Remove
                  </button>
                </span>
              </>
            )}
          </li>
        ))}
      </ol>
    </section>
  );
}

// subagentDepth counts how many of a child's ancestors this list also holds, so
// a grandchild indents under the child that announced it. Bounded by the list
// length, so a cycle in what the agent reported cannot hang the render.
function subagentDepth(subagents: Subagent[], child: Subagent): number {
  let depth = 0;
  let parentId = child.parentId;
  while (depth < subagents.length) {
    const parent = subagents.find((candidate) => candidate.id === parentId);
    if (!parent) {
      return depth;
    }
    depth += 1;
    parentId = parent.parentId;
  }
  return depth;
}

// SubagentStrip is the children the agent registered: which ones exist and what
// they are doing. Their own lines stay in the log, so this never repeats them.
function SubagentStrip({ subagents }: { subagents: Subagent[] }): React.ReactElement {
  return (
    <section className="session-subagents" aria-label="Subagents">
      <div className="subagents-head">Subagents</div>
      <ul className="subagent-list">
        {subagents.map((child) => (
          <li
            key={child.id}
            className="subagent-row"
            title={child.description || undefined}
            style={{ marginLeft: `${subagentDepth(subagents, child) * 16}px` }}
          >
            <span className="subagent-title mono">{child.title || child.id}</span>
            <span className="subagent-state">{subagentStateLabel(child.state)}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

export function SessionView({
  state,
  lifecycle,
  protected: pinned,
  steer,
  view,
  onView,
  onPrompt,
  onCancel,
  onSetControl,
  onAnswer,
  onQueueEdit,
  onQueueRemove,
  onQueueMove,
  onProtect,
  onTerminalReady,
  onInput,
  onResize,
}: Props): React.ReactElement {
  const [draft, setDraft] = useState("");
  // A pin is a round trip; the button waits for the daemon's answer so a
  // second click cannot ask for the state the first one is already setting.
  const [protectPending, setProtectPending] = useState(false);
  // The fold state lives here rather than in LogView: the log is swapped for
  // the diff and back, and a reader's opened runs must survive the trip.
  const [openRuns, setOpenRuns] = useState<ReadonlySet<string>>(new Set());
  // The fleet's word wins when it says working: a turn the daemon started from
  // the queue runs without this window's prompt call, and Cancel must reach it.
  const working = state.turn === "working" || lifecycle === "working";
  // A turn this window just started is already busy, even before the fleet has
  // said so: a second Send must never reach the running turn as a fresh prompt.
  const route = promptRoute(
    state.mode,
    state.turn === "working" ? "working" : lifecycle,
    steer,
  );

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
    if (text === "") {
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
          <button
            type="button"
            aria-pressed={pinned}
            title="Keep this session out of idle reclamation"
            disabled={protectPending}
            onClick={() => {
              setProtectPending(true);
              void Promise.resolve(onProtect(!pinned)).finally(() => setProtectPending(false));
            }}
          >
            {pinned ? "Protected" : "Protect"}
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

      {state.mode === "acp" && state.subagents.length > 0 ? (
        <SubagentStrip subagents={state.subagents} />
      ) : null}

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
        <>
          {state.queue.length > 0 ? (
            <QueueStrip
              messages={state.queue}
              onEdit={onQueueEdit}
              onRemove={onQueueRemove}
              onMove={onQueueMove}
            />
          ) : null}
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
                {route === "steer"
                  ? "Steer · the agent takes this into the running turn"
                  : route === "queue"
                    ? "Send queues · the next turn starts when this one ends"
                    : "Enter to send · Shift+Enter for newline"}
              </span>
              <button type="button" className="primary" onClick={submit}>
                <IconSend />
                {route === "steer" ? "Steer" : route === "queue" ? "Queue" : "Send"}
              </button>
            </div>
          </div>
        </>
      ) : null}
    </div>
  );
}
