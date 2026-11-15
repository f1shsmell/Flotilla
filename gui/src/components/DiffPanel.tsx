import { useCallback, useEffect, useState } from "react";

import { parsePatch, type DiffFile, type DiffLine } from "../store";

interface Props {
  sessionId: string;
}

const scopes = ["unstaged", "branch", "turn"] as const;

export function DiffPanel({ sessionId }: Props): React.ReactElement {
  const [scope, setScope] = useState<(typeof scopes)[number]>("unstaged");
  const [files, setFiles] = useState<DiffFile[] | null>(null);
  const [open, setOpen] = useState<{ path: string; lines: DiffLine[] } | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(
    async (next: (typeof scopes)[number]) => {
      setError(null);
      setOpen(null);
      try {
        const listed = (await window.flotilla.diff(sessionId, next)) as {
          files?: DiffFile[];
        };
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

  const openFile = async (path: string): Promise<void> => {
    try {
      const patched = (await window.flotilla.diff(sessionId, scope, path)) as {
        patch?: string;
      };
      setOpen({ path, lines: parsePatch(patched.patch ?? "") });
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
        </span>
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
          <pre className="diff-patch">
            {open.lines.map((line, index) => (
              <div key={`${index}-${line.kind}`} className={`line ${line.kind}`}>
                {line.text}
              </div>
            ))}
          </pre>
        ) : (
          <p className="empty">
            {files && files.length > 0 ? "Pick a file to read its patch." : "Nothing changed here."}
          </p>
        )}
      </div>
    </div>
  );
}
