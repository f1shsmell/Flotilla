import { useCallback, useEffect, useRef, useState } from "react";

import { decodeBase64 } from "./base64";
import { FleetList } from "./components/FleetList";
import { NewSessionForm } from "./components/NewSessionForm";
import { SessionView } from "./components/SessionView";
import {
  addPermission,
  answerPermission,
  applyUpdate,
  endTurn,
  failTurn,
  loseSession,
  newSession,
  parseStatus,
  startTurn,
  summarise,
  type SessionState,
  type StartRequest,
  type Status,
} from "./store";

interface WireEvent {
  type: "update" | "permission" | "data" | "closed";
  session: string;
  update?: unknown;
  question?: unknown;
  base64?: string;
  error?: string;
}

export function App(): React.ReactElement {
  const [fleet, setFleet] = useState<Status | null>(null);
  const [fleetError, setFleetError] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [session, setSession] = useState<SessionState | null>(null);
  const [openError, setOpenError] = useState<string | null>(null);

  // Updates arrive per chunk; folding them into state per chunk would re-render
  // the whole log at the agent's token rate, so they queue and flush on a frame.
  const queue = useRef<WireEvent[]>([]);
  const frame = useRef<number | null>(null);

  // Terminal bytes bypass the store: xterm keeps its own scrollback and writes
  // are already cheap, so they go straight to the view that owns them.
  const terminalWrite = useRef<((bytes: Uint8Array) => void) | null>(null);
  const selectedId = useRef<string | null>(null);

  const flush = useCallback(() => {
    frame.current = null;
    const events = queue.current;
    queue.current = [];
    if (events.length === 0) {
      return;
    }

    setSession((current) => {
      if (!current) {
        return current;
      }
      let next = current;
      for (const event of events) {
        if (event.session !== next.id) {
          continue;
        }
        switch (event.type) {
          case "update":
            next = applyUpdate(next, event.update);
            break;
          case "permission":
            next = addPermission(next, event.question);
            break;
          case "closed":
            next = loseSession(next, event.error);
            break;
        }
      }
      return next;
    });
  }, []);

  const schedule = useCallback(() => {
    if (frame.current === null) {
      frame.current = requestAnimationFrame(flush);
    }
  }, [flush]);

  useEffect(() => {
    return window.flotilla.onEvent((payload) => {
      const event = payload as WireEvent;

      if (event.type === "data") {
        if (event.session === selectedId.current && event.base64) {
          terminalWrite.current?.(decodeBase64(event.base64));
        }
        return;
      }

      queue.current.push(event);
      schedule();
    });
  }, [schedule]);

  // A daemon that cannot start would be spawned again every tick, so a failure
  // backs the poll off instead of hammering the same missing binary.
  const retryAt = useRef(0);

  const refresh = useCallback(async (force = false) => {
    if (!force && Date.now() < retryAt.current) {
      return;
    }
    try {
      setFleet(parseStatus(await window.flotilla.fleet()));
      setFleetError(null);
      retryAt.current = 0;
    } catch (error) {
      setFleet(null);
      setFleetError(error instanceof Error ? error.message : String(error));
      retryAt.current = Date.now() + 10000;
    }
  }, []);

  useEffect(() => {
    void refresh(true);
    const timer = setInterval(() => void refresh(), 2000);
    return () => clearInterval(timer);
  }, [refresh]);

  const open = useCallback(async (id: string) => {
    setSelected(id);
    selectedId.current = id;
    terminalWrite.current = null;
    setOpenError(null);
    try {
      const attached = (await window.flotilla.open(id)) as {
        info?: { mode?: string };
      };
      setSession(newSession(id, attached.info?.mode ?? "pty"));
    } catch (error) {
      setSession(null);
      setOpenError(error instanceof Error ? error.message : String(error));
    }
  }, []);

  const prompt = useCallback(
    async (text: string) => {
      if (!session) {
        return;
      }
      const id = session.id;
      setSession((current) => (current ? startTurn(current) : current));
      try {
        const response = (await window.flotilla.prompt(id, text)) as {
          stop_reason?: string;
        };
        setSession((current) =>
          current ? endTurn(current, response.stop_reason ?? "") : current,
        );
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        setSession((current) => (current ? failTurn(current, message) : current));
      }
    },
    [session],
  );

  const cancel = useCallback(async () => {
    if (!session) {
      return;
    }
    try {
      await window.flotilla.cancel(session.id);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      setSession((current) => (current ? failTurn(current, message) : current));
    }
  }, [session]);

  const answer = useCallback(
    async (questionId: string, optionId: string) => {
      if (!session) {
        return;
      }
      const id = session.id;
      setSession((current) =>
        current ? answerPermission(current, questionId) : current,
      );
      try {
        await window.flotilla.answer(id, questionId, optionId);
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        setSession((current) => (current ? failTurn(current, message) : current));
      }
    },
    [session],
  );

  const start = useCallback(
    async (request: StartRequest) => {
      const started = (await window.flotilla.start(request)) as { id?: string };
      await refresh(true);
      if (started?.id) {
        await open(started.id);
      }
    },
    [open, refresh],
  );

  const counts = fleet ? summarise(fleet) : { total: 0, reachable: 0 };

  return (
    <div className="app">
      <aside className="fleet">
        <header>
          <h1>Flotilla</h1>
          <p className="muted">
            {fleet ? `${counts.reachable}/${counts.total} reachable` : "…"}
            {fleet ? ` · pid ${fleet.pid}` : ""}
          </p>
          <button type="button" onClick={() => void refresh(true)}>
            Refresh
          </button>
        </header>

        {fleetError ? <p className="error">{fleetError}</p> : null}
        {fleet?.failures.map((failure) => (
          <p className="warn" key={failure}>
            {failure}
          </p>
        ))}

        <NewSessionForm onStart={start} />

        <FleetList
          sessions={fleet?.sessions ?? []}
          selected={selected}
          onSelect={(id) => void open(id)}
        />
      </aside>

      <main className="session">
        {openError ? <p className="error">{openError}</p> : null}
        {session ? (
          <SessionView
            state={session}
            onPrompt={(text) => void prompt(text)}
            onCancel={() => void cancel()}
            onAnswer={(questionId, optionId) => void answer(questionId, optionId)}
            onTerminalReady={(write) => {
              terminalWrite.current = write;
            }}
            onInput={(data) => void window.flotilla.input(session.id, data)}
            onResize={(cols, rows) => void window.flotilla.resize(session.id, cols, rows)}
          />
        ) : (
          <p className="empty">
            {fleet && fleet.sessions.length > 0
              ? "Pick a session to watch it work."
              : "No sessions yet. Start one on the left."}
          </p>
        )}
      </main>
    </div>
  );
}
