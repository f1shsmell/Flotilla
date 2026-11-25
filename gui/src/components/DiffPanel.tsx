import { useCallback, useEffect, useRef, useState } from "react";

import { GitActions } from "./GitActions";
import { RefreshButton } from "./RefreshButton";

import { fileStatusLabel, scopeLabel } from "../fleetView";
import {
  commentPrompt,
  commentsOn,
  parseCheckpoints,
  parsePatch,
  type Checkpoint,
  type Comment,
  type DiffFile,
  type DiffLine,
} from "../store";

// diffEmptyLabel is what the panel says under its file list. A failure and a
// pending read both leave no files, so they are told apart by `failed`: a
// refusal that kept the reading label up would claim an answer is coming.
export function diffEmptyLabel(failed: boolean, files: DiffFile[] | null): string {
  if (failed) {
    return "The diff could not be read; the error above says why.";
  }
  if (files === null) {
    return "Reading the diff…";
  }
  return files.length > 0 ? "Pick a file to read its patch." : "Nothing changed here.";
}

interface Props {
  sessionId: string;
}

const scopes = ["unstaged", "branch", "turn"] as const;

interface Composing {
  line: number;
  side: string;
}

interface CheckpointStripProps {
  checkpoints: Checkpoint[];
  // confirming is the turn whose restore button was pressed, or null.
  confirming: number | null;
  // busy is true while a restore runs, so a second click cannot start another.
  busy: boolean;
  onAsk: (turn: number) => void;
  onCancel: () => void;
  onRestore: (turn: number) => void;
}

// CheckpointStrip lists the session's finished turns newest first. A rollback
// rewrites the working tree and drops what came after the turn, so it asks once
// before it discards work.
export function CheckpointStrip({
  checkpoints,
  confirming,
  busy,
  onAsk,
  onCancel,
  onRestore,
}: CheckpointStripProps): React.ReactElement | null {
  if (checkpoints.length === 0) {
    return null;
  }

  return (
    <section className="checkpoints" aria-label="Checkpoints">
      <span className="checkpoints-head">Checkpoints</span>
      <ol className="checkpoint-list">
        {[...checkpoints].reverse().map((checkpoint) => (
          <li className="checkpoint-row" key={checkpoint.turn}>
            <span className="checkpoint-label">
              Turn {checkpoint.turn} · {checkpoint.commit.slice(0, 7)}
            </span>
            {confirming === checkpoint.turn ? (
              <>
                <button
                  type="button"
                  className="primary"
                  disabled={busy}
                  onClick={() => onRestore(checkpoint.turn)}
                >
                  Restore and discard changes
                </button>
                <button type="button" disabled={busy} onClick={onCancel}>
                  Cancel
                </button>
              </>
            ) : (
              <button
                type="button"
                aria-label={`Restore turn ${checkpoint.turn}`}
                disabled={busy}
                onClick={() => onAsk(checkpoint.turn)}
              >
                Restore
              </button>
            )}
          </li>
        ))}
      </ol>
    </section>
  );
}

export function DiffPanel({ sessionId }: Props): React.ReactElement {
  const [scope, setScope] = useState<(typeof scopes)[number]>("unstaged");
  const [files, setFiles] = useState<DiffFile[] | null>(null);
  const [open, setOpen] = useState<{ path: string; lines: DiffLine[] } | null>(null);
  const [notes, setNotes] = useState<Comment[]>([]);
  const [composing, setComposing] = useState<Composing | null>(null);
  const [draft, setDraft] = useState("");
  const [error, setError] = useState<string | null>(null);
  // failed separates "the answer has not arrived" from "the answer was a
  // refusal": both used to be files === null, so a failure kept the reading
  // label up forever beside its own error.
  const [failed, setFailed] = useState(false);
  const [copied, setCopied] = useState(false);
  const [checkpoints, setCheckpoints] = useState<Checkpoint[]>([]);
  const [confirming, setConfirming] = useState<number | null>(null);
  const [restoring, setRestoring] = useState(false);

  const loadNotes = useCallback(async () => {
    try {
      const listed = (await window.flotilla.comments(sessionId)) as { comments?: Comment[] };
      setNotes(listed.comments ?? []);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  }, [sessionId]);

  // latest names the read the state should follow. A slow answer for the scope
  // the user left must not overwrite the one they are looking at, and a failed
  // earlier read must not claim the current scope could not be read.
  const latest = useRef(0);
  const load = useCallback(
    async (next: (typeof scopes)[number]) => {
      const ticket = ++latest.current;
      setError(null);
      setFailed(false);
      setOpen(null);
      setComposing(null);
      try {
        const listed = (await window.flotilla.diff(sessionId, next)) as { files?: DiffFile[] };
        if (ticket !== latest.current) {
          return;
        }
        setFiles(listed.files ?? []);
      } catch (cause) {
        if (ticket !== latest.current) {
          return;
        }
        setFiles(null);
        setFailed(true);
        setError(cause instanceof Error ? cause.message : String(cause));
      }
    },
    [sessionId],
  );

  useEffect(() => {
    void load(scope);
  }, [load, scope]);

  useEffect(() => {
    void loadNotes();
  }, [loadNotes]);

  const loadCheckpoints = useCallback(async () => {
    try {
      setCheckpoints(parseCheckpoints(await window.flotilla.checkpoints(sessionId)));
    } catch {
      // A session outside a repository has no snapshots, and the diff's own
      // error already says why there is nothing to compare.
      setCheckpoints([]);
    }
  }, [sessionId]);

  useEffect(() => {
    void loadCheckpoints();
  }, [loadCheckpoints]);

  const openFile = async (path: string): Promise<void> => {
    setError(null);
    setComposing(null);
    setCopied(false);
    try {
      const patched = (await window.flotilla.diff(sessionId, scope, path)) as { patch?: string };
      setOpen({ path, lines: parsePatch(patched.patch ?? "") });
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const addNote = async (): Promise<void> => {
    if (!open || !composing) {
      return;
    }

    const text = draft.trim();
    if (text === "") {
      return;
    }

    try {
      await window.flotilla.addComment(sessionId, {
        path: open.path,
        line: composing.line,
        side: composing.side,
        text,
      });
      setDraft("");
      setComposing(null);
      await loadNotes();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const removeNote = async (id: string): Promise<void> => {
    try {
      await window.flotilla.removeComment(sessionId, id);
      await loadNotes();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const restore = async (turn: number): Promise<void> => {
    setError(null);
    setConfirming(null);
    setRestoring(true);
    try {
      await window.flotilla.restoreCheckpoint(sessionId, turn);
      await load(scope);
      await loadNotes();
      await loadCheckpoints();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setRestoring(false);
    }
  };

  const copyPrompt = async (): Promise<void> => {
    try {
      await navigator.clipboard.writeText(commentPrompt(notes));
      setCopied(true);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  };

  return (
    <div className="diff">
      <div className="diff-bar">
        <select
          value={scope}
          onChange={(event) => setScope(event.target.value as (typeof scopes)[number])}
        >
          {scopes.map((name) => (
            <option key={name} value={name}>
              {scopeLabel(name)}
            </option>
          ))}
        </select>
        <RefreshButton
          onRefresh={() => {
            void load(scope);
            void loadCheckpoints();
          }}
        />
        <span className="muted">
          {files === null ? "" : `${files.length} file${files.length === 1 ? "" : "s"}`}
          {notes.length > 0 ? ` · ${notes.length} note${notes.length === 1 ? "" : "s"}` : ""}
        </span>
        {notes.length > 0 ? (
          <button type="button" onClick={() => void copyPrompt()}>
            {copied ? "Copied" : "Copy notes as prompt"}
          </button>
        ) : null}
      </div>

      <CheckpointStrip
        checkpoints={checkpoints}
        confirming={confirming}
        busy={restoring}
        onAsk={setConfirming}
        onCancel={() => setConfirming(null)}
        onRestore={(turn) => void restore(turn)}
      />

      {/* The writes sit under the diff they describe: what is on screen is what
          a commit would record, so the message can be written while reading it. */}
      <GitActions sessionId={sessionId} onWrote={() => void load(scope)} />

      {error ? <p className="error">{error}</p> : null}

      <div className="diff-body">
        <ul className="diff-files">
          {(files ?? []).map((file) => (            <li key={file.path}>
              <button
                type="button"
                className={open?.path === file.path ? "diff-file selected" : "diff-file"}
                onClick={() => void openFile(file.path)}
              >
                <span className="diff-path">{file.path}</span>
                <span className="diff-status">{fileStatusLabel(file.status)}</span>
                <span className="diff-counts">
                  {file.binary ? "binary" : `+${file.added} −${file.removed}`}
                </span>
              </button>
            </li>
          ))}
        </ul>

        {open ? (
          <div className="diff-patch">
            {open.lines.map((line, index) => {
              const anchored = commentsOn(notes, open.path, line.side, line.line);
              const composingHere =
                composing?.line === line.line && composing.side === line.side && line.line > 0;

              return (
                <div key={`${index}-${line.kind}`} className={`patch-line ${line.kind}`}>
                  {line.line > 0 ? (
                    <button
                      type="button"
                      className="line-no"
                      title="Comment on this line"
                      onClick={() => {
                        setComposing({ line: line.line, side: line.side });
                        setDraft("");
                      }}
                    >
                      {line.line}
                    </button>
                  ) : (
                    <span className="line-no" />
                  )}
                  <span className="line-text">{line.text}</span>

                  {anchored.map((note) => (
                    <div className="note" key={note.id}>
                      <span className="note-text">{note.text}</span>
                      <button
                        type="button"
                        className="note-remove"
                        onClick={() => void removeNote(note.id)}
                      >
                        ×
                      </button>
                    </div>
                  ))}

                  {composingHere ? (
                    <form
                      className="note-compose"
                      onSubmit={(event) => {
                        event.preventDefault();
                        void addNote();
                      }}
                    >
                      <input
                        autoFocus
                        value={draft}
                        placeholder={`Note on ${open.path}:${line.line}`}
                        onChange={(event) => setDraft(event.target.value)}
                      />
                      <button type="submit" className="primary" disabled={draft.trim() === ""}>
                        Add
                      </button>
                      <button type="button" onClick={() => setComposing(null)}>
                        Cancel
                      </button>
                    </form>
                  ) : null}
                </div>
              );
            })}
          </div>
        ) : (
          <p className="empty">{diffEmptyLabel(failed, files)}</p>
        )}
      </div>
    </div>
  );
}
