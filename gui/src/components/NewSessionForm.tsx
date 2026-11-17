import { useEffect, useMemo, useState } from "react";

import {
  filterAgents,
  splitArgs,
  usableAgents,
  worktreeOf,
  type AgentEntry,
  type StartRequest,
} from "../store";

interface Props {
  agents: AgentEntry[];
  // unavailable is set when the catalogue could not be read at all. The picker
  // then says the daemon is unreachable instead of claiming nothing is installed.
  unavailable?: string | null;
  onStart: (request: StartRequest) => Promise<void>;
  // onOpenAgentSettings is the picker's way out when nothing installed matches.
  // A window with no settings page yet just does not render it.
  onOpenAgentSettings?: () => void;
}

const policies = ["supervised", "accept-edits", "auto", "full-access"];

// NewSessionForm picks an agent by name. The catalogue is the daemon's answer,
// so the form never invents a command: it shows what this machine has and what
// the user switched on, and a custom entry covers everything else.
export function NewSessionForm({
  agents,
  unavailable,
  onStart,
  onOpenAgentSettings,
}: Props): React.ReactElement {
  const [query, setQuery] = useState("");
  const [chosenId, setChosenId] = useState<string | null>(null);
  // custom is the escape hatch: an agent the catalogue does not know, or one
  // the user wants to start with their own arguments.
  const [custom, setCustom] = useState(false);
  const [command, setCommand] = useState("");
  const [args, setArgs] = useState("");
  const [dir, setDir] = useState("");
  const [repo, setRepo] = useState("");
  const [branch, setBranch] = useState("");
  const [mode, setMode] = useState<"acp" | "pty">("acp");
  const [policy, setPolicy] = useState("supervised");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // usable is what this session mode can start, before the search box narrows
  // it. Selection is resolved against usable rather than available: a search
  // that hides the chosen agent must not clear it, but a switch or an install
  // that makes it unstartable must.
  const usable = useMemo(() => usableAgents(agents, mode), [agents, mode]);
  const available = useMemo(() => filterAgents(usable, query), [usable, query]);

  const chosen = useMemo(
    () => usable.find((agent) => agent.id === chosenId) ?? null,
    [usable, chosenId],
  );

  // A search that hides the chosen agent would leave an enabled Start with no
  // row to show for it. The chosen row stays at the top instead, so what will
  // start and what is on screen are the same agent.
  const visible = useMemo(() => {
    if (!chosen || available.some((agent) => agent.id === chosen.id)) {
      return available;
    }
    return [chosen, ...available];
  }, [available, chosen]);

  // The chosen agent can disappear under the form (it was disabled, uninstalled,
  // or its mode changed), and the id would then point at nothing. Clearing it
  // keeps the selected row and the Start button telling the same story.
  useEffect(() => {
    if (chosenId !== null && chosen === null) {
      setChosenId(null);
    }
  }, [chosenId, chosen]);

  const submit = async (): Promise<void> => {
    if (busy) {
      return;
    }

    const worktree = worktreeOf(repo, branch);
    if (worktree.error) {
      setError(worktree.error);
      return;
    }

    let request: Pick<StartRequest, "mode" | "command" | "args">;
    if (custom) {
      const name = command.trim();
      if (name === "") {
        return;
      }
      request = { mode, command: name, args: splitArgs(args) };
    } else {
      if (!chosen) {
        return;
      }
      // The preset supplies the command; the mode switch above stays the user's
      // choice, because the same agent is often usable both ways.
      request = { mode, command: chosen.command, args: chosen.args };
    }

    setBusy(true);
    setError(null);
    try {
      await onStart({
        id: `sess-${Date.now().toString(36)}`,
        ...request,
        dir: worktree.request ? "" : dir.trim(),
        policy: request.mode === "acp" ? policy : undefined,
        worktree: worktree.request,
      });
      setCommand("");
      setArgs("");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  const ready = custom ? command.trim() !== "" : chosen !== null;

  return (
    <form
      className="new-session"
      onSubmit={(event) => {
        event.preventDefault();
        void submit();
      }}
    >
      <p className="muted">New session</p>

      <div className="agent-picker">
        <div className="row">
          <select
            value={mode}
            onChange={(event) => setMode(event.target.value === "pty" ? "pty" : "acp")}
          >
            <option value="acp">acp</option>
            <option value="pty">pty</option>
          </select>
          <input
            value={query}
            placeholder="search agents"
            onChange={(event) => setQuery(event.target.value)}
          />
        </div>

        {custom ? (
          <>
            <input
              value={command}
              placeholder="agent command, e.g. reasonix"
              onChange={(event) => setCommand(event.target.value)}
            />
            <input
              value={args}
              placeholder="arguments, e.g. acp"
              onChange={(event) => setArgs(event.target.value)}
            />
          </>
        ) : (
          <>
            <ul className="agent-list" aria-label="agents">
              {visible.map((agent) => (
                <li key={agent.id}>
                  <button
                    type="button"
                    className={
                      agent.id === chosenId ? "agent-option selected" : "agent-option"
                    }
                    onClick={() => setChosenId(agent.id)}
                  >
                    <span>{agent.name}</span>
                    <span className="muted mono">{agent.command}</span>
                  </button>
                </li>
              ))}
            </ul>
            {visible.length === 0 ? (
              <p className="muted">
                {unavailable
                  ? `Agents unavailable: ${unavailable}`
                  : query.trim() === ""
                    ? `No ${mode === "acp" ? "chat" : "terminal"} agent is available.`
                    : "No available agent matches."}{" "}
                {onOpenAgentSettings ? (
                  <button type="button" className="link" onClick={onOpenAgentSettings}>
                    Manage agents
                  </button>
                ) : null}
              </p>
            ) : null}
            {/* The builtins all speak acp, so a terminal session with no
                adapter of its own would otherwise offer nothing and say only
                that nothing is available. */}
            {visible.length === 0 &&
            !unavailable &&
            query.trim() === "" &&
            mode === "pty" ? (
              <p className="muted">
                No built-in terminal agent: add an adapter, or use a custom command.
              </p>
            ) : null}
            {chosen?.needs ? <p className="muted">{chosen.needs}</p> : null}
            <button type="button" className="link" onClick={() => setCustom(true)}>
              Use a custom command…
            </button>
          </>
        )}

        {custom && !unavailable ? (
          <button type="button" className="link" onClick={() => setCustom(false)}>
            Back to the agent list
          </button>
        ) : null}
      </div>

      <input
        value={dir}
        placeholder="working directory (optional)"
        onChange={(event) => setDir(event.target.value)}
      />
      <input
        value={repo}
        placeholder="worktree of repository (optional)"
        onChange={(event) => setRepo(event.target.value)}
      />
      <input
        value={branch}
        placeholder="new branch for the worktree"
        onChange={(event) => setBranch(event.target.value)}
      />

      <div className="row">
        {(custom || chosen) && mode === "acp" ? (
          <select value={policy} onChange={(event) => setPolicy(event.target.value)}>
            {policies.map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </select>
        ) : null}
        <button type="submit" className="primary" disabled={busy || !ready}>
          {busy ? "Starting…" : "Start"}
        </button>
      </div>

      {error ? <p className="error">{error}</p> : null}
    </form>
  );
}
