import { useState } from "react";

import { splitArgs, type StartRequest } from "../store";

interface Props {
  onStart: (request: StartRequest) => Promise<void>;
}

const policies = ["supervised", "accept-edits", "auto", "full-access"];

export function NewSessionForm({ onStart }: Props): React.ReactElement {
  const [command, setCommand] = useState("");
  const [args, setArgs] = useState("");
  const [dir, setDir] = useState("");
  const [mode, setMode] = useState("acp");
  const [policy, setPolicy] = useState("supervised");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (): Promise<void> => {
    const name = command.trim();
    if (name === "" || busy) {
      return;
    }

    setBusy(true);
    setError(null);
    try {
      await onStart({
        id: `sess-${Date.now().toString(36)}`,
        mode,
        command: name,
        args: splitArgs(args),
        dir: dir.trim(),
        policy: mode === "acp" ? policy : undefined,
      });
      setCommand("");
      setArgs("");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form
      className="new-session"
      onSubmit={(event) => {
        event.preventDefault();
        void submit();
      }}
    >
      <p className="muted">New session</p>

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
      <input
        value={dir}
        placeholder="working directory (optional)"
        onChange={(event) => setDir(event.target.value)}
      />

      <div className="row">
        <select value={mode} onChange={(event) => setMode(event.target.value)}>
          <option value="acp">acp</option>
          <option value="pty">pty</option>
        </select>
        {mode === "acp" ? (
          <select value={policy} onChange={(event) => setPolicy(event.target.value)}>
            {policies.map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </select>
        ) : null}
        <button type="submit" className="primary" disabled={busy || command.trim() === ""}>
          {busy ? "Starting…" : "Start"}
        </button>
      </div>

      {error ? <p className="error">{error}</p> : null}
    </form>
  );
}
