import { useState } from "react";

import { agentReady } from "../store";
import type { AgentChange, AgentEntry, RegistryEntry } from "../store";
import { IconBack } from "./Icons";
import { RefreshButton } from "./RefreshButton";

interface Props {
  agents: AgentEntry[];
  // registry is the published directory's agents the catalog does not know.
  // They are shown apart from the table: no switches, no start, only a recipe.
  registry: RegistryEntry[];
  problems: string[];
  // unavailable is set when the catalogue could not be read at all, so an empty
  // list is not mistaken for a machine with no agents defined.
  unavailable?: string | null;
  // loaded is false until the first answer arrives, so an empty table is not
  // presented as "no agent is defined" before anyone has asked.
  loaded?: boolean;
  // onToggle applies one switch and re-reads the catalogue, so the row shows
  // what the daemon stored rather than what the click hoped.
  onToggle: (id: string, change: AgentChange) => Promise<void>;
  onRefresh: () => void;
  onClose: () => void;
}

// agentState is the row's word for it: the tier itself, because "not found"
// would be false for an agent whose CLI is installed without its ACP bridge.
function agentState(agent: AgentEntry): string {
  if (agent.tier !== "") {
    return agent.tier;
  }
  return agent.installed ? "installed" : "not found";
}

// AgentSettings is the list the picker draws from: what this machine has, what
// the user switched on, and which file defined each preset. It never installs
// anything: an agent that is not on PATH says so and stays listed.
export function AgentSettings({
  agents,
  registry,
  problems,
  unavailable,
  loaded = true,
  onToggle,
  onRefresh,
  onClose,
}: Props): React.ReactElement {
  // saving holds every id with a write in flight, so one agent's toggle does not
  // re-enable another row whose write has not settled.
  const [saving, setSaving] = useState<ReadonlySet<string>>(new Set());
  const [error, setError] = useState<string | null>(null);

  const available = agents.filter(agentReady).length;

  const toggle = async (id: string, change: AgentChange): Promise<void> => {
    setSaving((current) => new Set(current).add(id));
    setError(null);
    try {
      await onToggle(id, change);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSaving((current) => {
        const next = new Set(current);
        next.delete(id);
        return next;
      });
    }
  };

  return (
    <section className="agent-settings">
      <header>
        <div>
          <h2>Agents</h2>
          <p className="muted">
            {available} available · {agents.length} known
          </p>
        </div>
        <div className="row">
          <RefreshButton onRefresh={onRefresh} />
          <button type="button" onClick={onClose}>
            <IconBack />
            Back
          </button>
        </div>
      </header>

      {error ? <p className="error">{error}</p> : null}
      {problems.map((problem) => (
        <p className="warn" key={problem}>
          {problem}
        </p>
      ))}

      {unavailable ? <p className="error">Agents unavailable: {unavailable}</p> : null}

      {agents.length === 0 ? (
        <p className="empty">
          {!loaded
            ? "Reading the agent list…"
            : unavailable
              ? "The daemon could not be reached."
              : "No agent is defined yet."}
        </p>
      ) : (
        <table className="agent-table">
          <thead>
            <tr>
              <th scope="col">Agent</th>
              <th scope="col">State</th>
              <th scope="col">Enabled</th>
              <th scope="col">Chat</th>
              <th scope="col">CLI</th>
            </tr>
          </thead>
          <tbody>
            {agents.map((agent) => (
              <tr key={agent.id} className={agent.enabled ? undefined : "off"}>
                <td>
                  <span className="agent-name">{agent.name}</span>
                  <span className="muted mono">{agent.command}</span>
                  {agent.description ? (
                    <span className="muted agent-description">{agent.description}</span>
                  ) : null}
                  {agent.needs ? <span className="muted agent-needs">{agent.needs}</span> : null}
                </td>
                <td>
                  <span className={agentReady(agent) ? "badge ok" : "badge down"}>
                    {agentState(agent)}
                  </span>
                  <span className="muted agent-source">{agent.source}</span>
                </td>
                <td>
                  <Toggle
                    label={`enable ${agent.name}`}
                    checked={agent.enabled}
                    busy={saving.has(agent.id)}
                    onChange={(enabled) => toggle(agent.id, { enabled })}
                  />
                </td>
                <td>
                  {agent.mode === "acp" ? (
                    <Toggle
                      label={`offer ${agent.name} to chat sessions`}
                      checked={agent.chat}
                      busy={saving.has(agent.id)}
                      onChange={(chat) => toggle(agent.id, { chat })}
                    />
                  ) : (
                    // A preset is one recipe: its mode decides the protocol, so
                    // the other purpose has no switch to offer. The reason is
                    // visible text rather than a tooltip: a Chromium tooltip is
                    // unreachable by keyboard and invisible to touch, and this
                    // is the answer to "why can't I turn this on".
                    <span className="agent-unsupported">{unsupported(agent, "chat")}</span>
                  )}
                </td>
                <td>
                  {agent.mode === "pty" ? (
                    <Toggle
                      label={`offer ${agent.name} to terminal sessions`}
                      checked={agent.cli}
                      busy={saving.has(agent.id)}
                      onChange={(cli) => toggle(agent.id, { cli })}
                    />
                  ) : (
                    <span className="agent-unsupported">{unsupported(agent, "cli")}</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {registry.length > 0 ? (
        <section className="agent-registry">
          <h3>ACP Registry</h3>
          <p className="muted">
            {registry.length} {registry.length === 1 ? "agent" : "agents"} the published
            directory offers. Listing one installs nothing, and none can be started
            until its recipe is installed.
          </p>
          <ul className="agent-registry-list" aria-label="published agents">
            {registry.map((agent) => (
              <li key={agent.id}>
                <span className="agent-name">{agent.name}</span>
                <span className="muted mono">
                  {agent.id} {agent.version}
                </span>
                <span className="muted mono">{agent.recipe}</span>
                {agent.description ? (
                  <span className="muted agent-description">{agent.description}</span>
                ) : null}
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </section>
  );
}

interface ToggleProps {
  label: string;
  checked: boolean;
  busy: boolean;
  onChange: (value: boolean) => void;
}

function Toggle({ label, checked, busy, onChange }: ToggleProps): React.ReactElement {
  return (
    <input
      type="checkbox"
      aria-label={label}
      checked={checked}
      disabled={busy}
      onChange={(event) => onChange(event.target.checked)}
    />
  );
}

// unsupported says why a purpose has no switch for a preset: its mode is the
// protocol its arguments launch, and it serves only the session kind that speaks
// it.
// unsupported says why a purpose has no switch for a preset: its mode is the
// protocol its arguments launch, and it serves only the session kind that
// speaks it. The cell is narrow, so this is the short form — the full sentence
// belongs in a tooltip nobody can reach, which is why it is not there.
function unsupported(agent: AgentEntry, purpose: "chat" | "cli"): string {
  const other = purpose === "chat" ? "terminal" : "chat";
  return `runs as ${agent.mode}: ${other} sessions only`;
}
