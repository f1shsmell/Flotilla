// The three writes a review ends with: commit what the agent changed, send the
// branch, and open a pull request.
//
// Each is a separate button because each is a separate decision. Nothing here
// runs on its own, and nothing invents a message: the person pressing the button
// has read what goes into history.

import { useState } from "react";

interface Props {
  sessionId: string;
  // onWrote lets the panel refresh what it shows: a commit makes the working
  // tree clean, and a stale diff would claim otherwise.
  onWrote: () => void;
}

interface WriteResult {
  summary?: string;
  ref?: string;
  output?: string;
}

function asResult(raw: unknown): WriteResult {
  const value = (raw ?? {}) as Record<string, unknown>;
  return {
    summary: typeof value.summary === "string" ? value.summary : undefined,
    ref: typeof value.ref === "string" ? value.ref : undefined,
    output: typeof value.output === "string" ? value.output : undefined,
  };
}

export function GitActions({ sessionId, onWrote }: Props): React.ReactElement {
  const [message, setMessage] = useState("");
  const [title, setTitle] = useState("");
  const [busy, setBusy] = useState<"commit" | "push" | "pr" | null>(null);
  const [done, setDone] = useState<WriteResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  const run = async (
    which: "commit" | "push" | "pr",
    action: () => Promise<unknown>,
  ): Promise<void> => {
    setBusy(which);
    setError(null);
    setDone(null);
    try {
      const raw = await action();
      setDone(asResult(raw));
      onWrote();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(null);
    }
  };

  const commit = (): void => {
    const subject = message.trim();
    void run("commit", async () => {
      const result = await window.flotilla.commit(sessionId, subject);
      // The message is now history, so it is cleared: a second click must not
      // commit an identical subject onto whatever comes next. It becomes the
      // pull request title instead, which is the next thing that needs one.
      setTitle((current) => (current.trim() === "" ? subject : current));
      setMessage("");
      return result;
    });
  };

  // A pull request needs a title, and the commit message is usually the right
  // one. An empty title is refused by the daemon, so it is filled here rather
  // than discovered after a round trip.
  const pullRequestTitle = (): string => title.trim() || message.trim();

  const openPullRequest = (): void => {
    void run("pr", () =>
      window.flotilla.pr(sessionId, { title: pullRequestTitle(), body: "" }),
    );
  };

  return (
    <div className="git-actions">
      <input
        value={message}
        placeholder="commit message"
        onChange={(event) => setMessage(event.target.value)}
      />
      <button
        type="button"
        className="primary"
        disabled={busy !== null || message.trim() === ""}
        onClick={commit}
      >
        {busy === "commit" ? "Committing…" : "Commit"}
      </button>

      <button
        type="button"
        disabled={busy !== null}
        onClick={() => void run("push", () => window.flotilla.push(sessionId))}
      >
        {busy === "push" ? "Pushing…" : "Push"}
      </button>

      <input
        value={title}
        placeholder="pull request title (default: the commit message)"
        onChange={(event) => setTitle(event.target.value)}
      />
      <button
        type="button"
        disabled={busy !== null || (title.trim() === "" && message.trim() === "")}
        onClick={openPullRequest}
      >
        {busy === "pr" ? "Opening…" : "Open PR"}
      </button>

      {error ? <p className="error">{error}</p> : null}
      {done?.summary ? (
        <p className="git-result">
          {done.ref && /^https?:/.test(done.ref) ? (
            <a href={done.ref} target="_blank" rel="noreferrer">
              {done.summary}
            </a>
          ) : (
            done.summary
          )}
        </p>
      ) : null}
    </div>
  );
}