import { useEffect, useMemo, useState } from "react";

import { modeLabel } from "../fleetView";
import {
  defaultAgent,
  defaultBranch,
  filterAgents,
  filterRegistry,
  splitArgs,
  usableAgents,
  worktreeRequest,
  type AgentEntry,
  type RegistryEntry,
  type StartRequest,
} from "../store";

interface Props {
  agents: AgentEntry[];
  // registry is the published directory's agents this build does not know. They
  // are shown so a person can see what exists and how it installs; starting one
  // is not offered, because nothing on this machine can run it yet.
  registry?: RegistryEntry[];
  // unavailable is set when the catalogue could not be read at all. The picker
  // then says the daemon is unreachable instead of claiming nothing is installed.
  unavailable?: string | null;
  // loaded is false until the first answer arrives, so an empty list is not
  // presented as "this machine has no agents" before anyone has asked.
  loaded?: boolean;
  onStart: (request: StartRequest) => Promise<void>;
  // onOpenAgentSettings is the picker's way out when nothing installed matches.
  // A window with no settings page yet just does not render it.
  onOpenAgentSettings?: () => void;
  // embedded is set when a greeting above the card already names the form, so
  // the overline would read the same words twice on one screen.
  embedded?: boolean;
}

const policies = ["supervised", "accept-edits", "auto", "full-access"];

// policyLabel names a permission policy in words a person reads, not in the
// protocol's spelling: the value crosses the wire, the label is what the
// window shows. Four steps from "ask me" to "don't ask".
const policyLabels: Record<string, string> = {
  supervised: "Ask me every time",
  "accept-edits": "Auto-accept edits",
  auto: "Auto",
  "full-access": "Full access",
};

// NewSessionForm picks an agent by name. The catalogue is the daemon's answer,
// so the form never invents a command: it shows what this machine has and what
// the user switched on, and a custom entry covers everything else.
export function NewSessionForm({
  agents,
  registry = [],
  unavailable,
  loaded = true,
  onStart,
  onOpenAgentSettings,
  embedded = false,
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
  // worktreeOpen is the one optional section the form hides until asked for.
  // A person starting a session in a directory does not want three empty fields
  // about worktrees on the first screen; a person who wants a worktree gets
  // them in one click.
  const [worktreeOpen, setWorktreeOpen] = useState(false);
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
  // The directory is narrowed by the same search box, so a term that finds
  // nothing startable can still find an agent this machine could install.
  const directory = useMemo(() => filterRegistry(registry, query), [registry, query]);

  // A second list, resolved in the other mode, answers one question the form
  // could otherwise only answer by making the user flip a switch and watch an
  // empty list appear: does anything exist for the other kind of session? When
  // nothing does, the mode switch is not offered at all.
  const otherMode: "acp" | "pty" = mode === "acp" ? "pty" : "acp";
  const otherUsable = useMemo(() => usableAgents(agents, otherMode), [agents, otherMode]);
  const modeChoice = otherUsable.length > 0;

  const chosen = useMemo(
    () => usable.find((agent) => agent.id === chosenId) ?? null,
    [usable, chosenId],
  );

  // A mode with nothing in it is not a choice, it is a dead end. When the list
  // for the current mode is empty and the other mode has agents, the form moves
  // to the one that can actually start something — the user never sees "no chat
  // agent can be started" while a terminal agent sits behind a switch. A custom
  // command is exempt: it is the user deliberately typing their own, in the mode
  // they want.
  useEffect(() => {
    if (custom || !loaded || usable.length > 0 || otherUsable.length === 0) {
      return;
    }
    setMode(otherMode);
  }, [custom, loaded, usable.length, otherUsable.length, otherMode]);

  // A single startable agent is not a choice: selecting it is what the user
  // would do anyway, and leaving it unselected makes Start grey with no reason
  // on screen. Several agents stay a question, so nothing is picked for them.
  useEffect(() => {
    if (!loaded || custom || chosenId !== null) {
      return;
    }
    const only = defaultAgent(usable);
    if (only !== null) {
      setChosenId(only);
    }
  }, [loaded, custom, chosenId, usable]);

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

    const worktree = worktreeRequest(repo, branch);
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
      {embedded ? null : <p className="muted overline">New session</p>}

      <div className="agent-picker">
        <div className="row">
          {/* The mode is a protocol, not a preference: it only appears when both
              kinds of session have something to start. Otherwise it is a switch
              with one position, which is a question the form can answer. */}
          {modeChoice ? (
            <select
              value={mode}
              aria-label="session mode"
              onChange={(event) => setMode(event.target.value === "pty" ? "pty" : "acp")}
            >
              <option value="acp">Chat (ACP)</option>
              <option value="pty">Terminal</option>
            </select>
          ) : (
            <input type="hidden" name="mode" value={mode} />
          )}
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
                {!loaded
                  ? "Reading the agent list…"
                  : unavailable
                    ? `Agents unavailable: ${unavailable}`
                    : query.trim() === ""
                      ? `No ${modeLabel(mode).toLowerCase()} agent can be started.`
                      : "No agent that can be started matches."}{" "}
                {loaded && onOpenAgentSettings ? (
                  <button type="button" className="link" onClick={onOpenAgentSettings}>
                    Manage agents
                  </button>
                ) : null}
              </p>
            ) : null}
            {/* The builtins all speak acp, so a terminal session with no
                adapter of its own would otherwise offer nothing and say only
                that nothing can be started. */}
            {loaded &&
            visible.length === 0 &&
            !unavailable &&
            query.trim() === "" &&
            mode === "pty" ? (
              <p className="muted">
                No built-in terminal agent: add an adapter, or use a custom command.
              </p>
            ) : null}
            {/* The directory follows what this machine can start: a person who
                typed the name of an agent the catalog lacks finds the recipe
                below the empty search rather than a dead end. */}
            {directory.length > 0 ? (
              <>
                <p className="muted overline">ACP Registry (not installed)</p>
                <ul className="agent-list registry-list" aria-label="published agents">
                  {directory.map((agent) => (
                    <li key={agent.id} className="registry-option">
                      <span>{agent.name}</span>
                      <span className="muted mono">{agent.version}</span>
                      <span className="muted mono registry-recipe">{agent.recipe}</span>
                    </li>
                  ))}
                </ul>
              </>
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

      {/* The directory is the common case — most sessions run in a project the
          user already has — so it stays on screen. The worktree fields below
          are the uncommon ones and are folded away until asked for. */}
      <input
        value={dir}
        placeholder="working directory (optional)"
        onChange={(event) => setDir(event.target.value)}
      />

      {/* One optional section instead of three bare fields: a worktree is the
          uncommon case, and the first screen is for the common one. The opener
          is also the closer: a section that can only be opened traps a person
          who clicked it by accident. */}
      {worktreeOpen ? (
        <>
          <div className="row worktree-head">
            <span className="muted overline">New worktree</span>
            <button
              type="button"
              className="link"
              onClick={() => {
                setWorktreeOpen(false);
                setRepo("");
                setBranch("");
              }}
            >
              Cancel
            </button>
          </div>
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
          {repo.trim() !== "" && branch.trim() === "" ? (
            <p className="muted new-session-hint">
              A branch will be named for you: {defaultBranch(new Date())}.
            </p>
          ) : null}
        </>
      ) : (
        <button type="button" className="link" onClick={() => setWorktreeOpen(true)}>
          Run in a new worktree…
        </button>
      )}

      <div className="row">
        {(custom || chosen) && mode === "acp" ? (
          <select
            value={policy}
            aria-label="permission policy"
            onChange={(event) => setPolicy(event.target.value)}
          >
            {policies.map((name) => (
              <option key={name} value={name}>
                {policyLabels[name] ?? name}
              </option>
            ))}
          </select>
        ) : null}
        <button type="submit" className="primary" disabled={busy || !ready}>
          {busy ? "Starting…" : "Start"}
        </button>
      </div>

      {/* A disabled Start is a question the form owes an answer to. It says which
          one thing is missing, in the same place, instead of leaving a grey
          button and no reason on screen. */}
      {!ready && !busy ? (
        <p className="muted new-session-hint">
          {custom ? "Type a command to start." : "Pick an agent above to start."}
        </p>
      ) : null}

      {error ? <p className="error">{error}</p> : null}
    </form>
  );
}
