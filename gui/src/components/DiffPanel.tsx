import { useCallback, useEffect, useState } from "react";

import {
  commentPrompt,
  commentsOn,
  parsePatch,
  type Comment,
  type DiffFile,
  type DiffLine,
} from "../store";

interface Props {
  sessionId: string;
}

const scopes = ["unstaged", "branch", "turn"] as const;

interface Composing {
  line: number;
  side: string;
}

export function DiffPanel({ sessionId }: Props): React.ReactElement {
  const [scope, setScope] = useState<(typeof scopes)[number]>("unstaged");
  const [files, setFiles] = useState<DiffFile[] | null>(null);
  const [open, setOpen] = useState<{ path: string; lines: DiffLine[] } | null>(null);
  const [notes, setNotes] = useState<Comment[]>([]);
  const [composing, setComposing] = useState<Composing | null>(null);
  const [draft, setDraft] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const loadNotes = useCallback(async () => {
    try {
      const listed = (await window.flotilla.comments(sessionId)) as { comments?: Comment[] };
      setNotes(listed.comments ?? []);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  }, [sessionId]);

  const load = useCallback(
    async (next: (typeof scopes)[number]) => {
      setError(null);
      setOpen(null);
      setComposing(null);
      try {
        const listed = (await window.flotilla.diff(sessionId, next)) as { files?: DiffFile[] };
        setFiles(listed.files ?? []);
      } catch (cause) {
        setFiles(null);
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
              {name}
            </option>
          ))}
        </select>
        <button type="button" onClick={() => void load(scope)}>
          Refresh
        </button>
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

      {error ? <p className="error">{error}</p> : null}

      <div className="diff-body">
        <ul className="diff-files">
          {(files ?? []).map((file) => (
            <li key={file.path}>
              <button
                type="button"
                className={open?.path === file.path ? "diff-file selected" : "diff-file"}
                onClick={() => void openFile(file.path)}
              >
                <span className="diff-path">{file.path}</span>
                <span className="diff-status">{file.status}</span>
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
          <p className="empty">
            {files && files.length > 0 ? "Pick a file to read its patch." : "Nothing changed here."}
          </p>
        )}
      </div>
    </div>
  );
}
