import { useCallback, useEffect, useRef, useState } from "react";

import { decodeBase64 } from "./base64";
import { AgentSettings } from "./components/AgentSettings";
import { FleetList } from "./components/FleetList";
import { NewSessionForm } from "./components/NewSessionForm";
import { SessionView } from "./components/SessionView";
import {
  addPermission,
  answerPermission,
  applyHistory,
  applyUpdate,
  endTurn,
  failTurn,
  loseSession,
  newSession,
  needsAttention,
  parseAgents,
  parseStatus,
  startTurn,
  summarise,
  withLifecycle,
  type AgentChange,
  type AgentListing,
  type SessionState,
  type StartRequest,
  type Status,
} from "./store";

interface WireEvent {
  type: "update" | "permission" | "state" | "data" | "closed";
  session: string;
  update?: unknown;
  question?: unknown;
  state?: unknown;
  base64?: string;
  error?: string;
}

export function App(): React.ReactElement {
  const [fleet, setFleet] = useState<Status | null>(null);
  const [fleetError, setFleetError] = useState<string | null>(null);
  // agents is the daemon's catalogue. It changes when the user edits a switch,
  // so it is read again after every write rather than patched in the renderer.
  const [agents, setAgents] = useState<AgentListing>({ agents: [], problems: [] });
  // agentsError is why the catalogue is empty, when it is empty because the
  // daemon could not be reached rather than because the machine has no agents.
  const [agentsError, setAgentsError] = useState<string | null>(null);
  // agentsLoaded separates "not read yet" from "read, and the machine has
  // none": an empty list means the second only once an answer arrived.
  const [agentsLoaded, setAgentsLoaded] = useState(false);
  // route is which main panel the window shows: the session view, or the agent
  // settings the picker sends the user to when nothing installed matches.
  const [route, setRoute] = useState<"session" | "agents">("session");
  const [selected, setSelected] = useState<string | null>(null);
  const [session, setSession] = useState<SessionState | null>(null);
  const [openError, setOpenError] = useState<string | null>(null);
  // The panel a session opens on is the window's state, not the session view's:
  // it has to survive the view unmounting when the window closes.
  const [view, setView] = useState<"stream" | "diff">("stream");

  // Layout is restored once: the stored session is opened only after the fleet
  // arrives, and only if the daemon still serves it. A remembered id whose
  // session ended is forgotten instead of reported as an error.
  const restore = useRef<{ session?: string } | null>(null);
  const restored = useRef(false);
  // layoutReady guards the write path: a renderer that has not read the stored
  // layout yet would overwrite it with its defaults on the first commit.
  const layoutReady = useRef(false);
  // layoutLoaded is the render-facing half of the same fact: restoring a
  // session has to wait for the stored layout, and a ref alone does not wake an
  // effect when it changes.
  const [layoutLoaded, setLayoutLoaded] = useState(false);

  // Updates arrive per chunk; folding them into state per chunk would re-render
  // the whole log at the agent's token rate, so they queue and flush on a frame.
  const queue = useRef<WireEvent[]>([]);
  const frame = useRef<number | null>(null);

  // Terminal bytes bypass the store: xterm keeps its own scrollback and writes
  // are already cheap, so they go straight to the view that owns them.
  const terminalWrite = useRef<((bytes: Uint8Array) => void) | null>(null);
  // A host replays its scrollback as soon as attach succeeds, which can happen
  // before the renderer has built its xterm. Dropping those bytes would defeat
  // the replay, so they wait here until the terminal can take them.
  const pendingOutput = useRef<Uint8Array[]>([]);
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
          const bytes = decodeBase64(event.base64);
          const write = terminalWrite.current;
          if (write) {
            write(bytes);
          } else {
            pendingOutput.current.push(bytes);
          }
        }
        return;
      }

      // A lifecycle change belongs to the fleet, not to the open session's log:
      // the row that says "blocked" is the one the user needs to see, whether
      // or not that session is the one on screen.
      if (event.type === "state") {
        const update = (event.state ?? {}) as { state?: unknown };
        setFleet((current) => withLifecycle(current, event.session, update.state));
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

  // The catalogue is read once and then after every switch the user flips. It
  // is not polled: an adapter file lands on the next daemon start, and an
  // install is discovered when the picker is opened again.
  //
  // A failed read is a state, not a rejection: the picker must be able to say
  // the daemon is unreachable instead of claiming this machine has no agents.
  //
  // One read at a time: the retry timer and a click's re-read can overlap, and
  // an older answer landing last would undo the newer one. A caller that arrives
  // while a read is in flight does not get dropped either — its answer would be
  // the newer one — so the read runs again before it settles.
  const agentsInFlight = useRef(false);
  const agentsReread = useRef(false);
  const refreshAgents = useCallback(async () => {
    if (agentsInFlight.current) {
      agentsReread.current = true;
      return;
    }
    agentsInFlight.current = true;
    try {
      do {
        agentsReread.current = false;
        try {
          setAgents(parseAgents(await window.flotilla.agents()));
          setAgentsError(null);
        } catch (error) {
          setAgents({ agents: [], problems: [] });
          setAgentsError(error instanceof Error ? error.message : String(error));
        }
      } while (agentsReread.current);
    } finally {
      agentsInFlight.current = false;
      setAgentsLoaded(true);
    }
  }, []);

  useEffect(() => {
    void refreshAgents();
  }, [refreshAgents]);

  // A failed read is retried on its own timer: the daemon can come up after the
  // window, and an empty picker that never asks again would stay empty.
  useEffect(() => {
    if (agentsError === null) {
      return;
    }
    const timer = setInterval(() => void refreshAgents(), 5000);
    return () => clearInterval(timer);
  }, [agentsError, refreshAgents]);

  // A switch writes through the daemon and the answer is re-read, so the row
  // shows what was stored rather than what the click asked for.
  const toggleAgent = useCallback(
    async (id: string, change: AgentChange) => {
      await window.flotilla.setAgent({ id, change });
      await refreshAgents();
    },
    [refreshAgents],
  );

  // Read the stored layout once. Until it lands, nothing may write: the
  // defaults would replace what is on disk before it was ever read.
  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const stored = (await window.flotilla.layout()) as {
          session?: string;
          view?: "stream" | "diff";
        };
        if (cancelled) {
          return;
        }
        restore.current = { session: stored.session };
        if (stored.view === "diff" || stored.view === "stream") {
          setView(stored.view);
        }
      } catch {
        // A window with no remembered layout opens on its defaults.
      } finally {
        if (!cancelled) {
          layoutReady.current = true;
          setLayoutLoaded(true);
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  // The panel choice is the window's, so it is written whenever it changes
  // rather than on close: a crash must not cost the user their view.
  useEffect(() => {
    if (!layoutLoaded) {
      return;
    }
    void window.flotilla.saveLayout({ view });
  }, [view, layoutLoaded]);

  const open = useCallback(async (id: string) => {
    // Opening a session is what the window shows, and a settings panel on top
    // would hide the session the user just asked for.
    setRoute("session");

    const previous = selectedId.current;
    if (previous !== null && previous !== id) {
      // Leaving a session detaches it instead of keeping a silent subscriber:
      // the host keeps running, but it stops streaming to a view that is no
      // longer on screen, which is what lets a terminal hold its recent output
      // for the next visit.
      void window.flotilla.detach(previous);
    }

    setSelected(id);
    selectedId.current = id;
    terminalWrite.current = null;
    // Bytes queued for the previous session belong to that session's screen,
    // not this one's.
    pendingOutput.current = [];
    setOpenError(null);
    try {
      const attached = (await window.flotilla.open(id)) as {
        info?: { mode?: string; state?: unknown };
        history?: unknown;
        history_more?: boolean;
      };

      // The log the daemon kept for this session goes in with the state, before
      // any live update: a window that opens a turn an hour old should show how
      // the agent got there, not start at its own arrival.
      setSession(
        applyHistory(newSession(id, attached.info?.mode ?? "pty"), {
          updates: attached.history,
          more: attached.history_more === true,
        }),
      );

      // Opening a session is looking at it, which is what separates done from
      // idle. Only a finished turn is marked: marking a working session seen
      // would be a lie about work that has not arrived.
      const finished = (attached.info?.state ?? "") === "done";
      if (finished) {
        void window.flotilla.seen(id).catch(() => {
          // A host that went away cannot record seen; the row keeps its badge.
        });
      }

      if (layoutReady.current) {
        void window.flotilla.saveLayout({ session: id });
      }
    } catch (error) {
      // The click could not be honoured. Only the session this call selected is
      // cleared: a stale attempt must not take down a view the user opened (or
      // the restore opened) after it started.
      if (selectedId.current === id) {
        setSession(null);
        selectedId.current = null;
        setSelected(null);
      }
      setOpenError(error instanceof Error ? error.message : String(error));
    }
  }, []);

  // Reopen the remembered session once the fleet is known. It runs once, and it
  // never takes over a session the window already opened: the user's click (or
  // a fresh start) happening first is the newer intent, and restoring over it
  // would replace a live view with a stale one.
  useEffect(() => {
    if (!layoutLoaded || fleet === null || restored.current) {
      return;
    }
    restored.current = true;

    const wanted = restore.current?.session;
    if (!wanted) {
      return;
    }
    if (selectedId.current !== null) {
      // Something is already on screen; the stored id is history, not intent.
      if (selectedId.current !== wanted && layoutReady.current) {
        void window.flotilla.saveLayout({ session: selectedId.current });
      }
      return;
    }
    if (fleet.sessions.some((entry) => entry.id === wanted)) {
      void open(wanted);
      return;
    }
    void window.flotilla.saveLayout({ session: null });
  }, [fleet, layoutLoaded, open]);

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

  // Stopping clears the view when it was showing that session: the log it held
  // has no host behind it any more, so keeping it on screen would be a lie.
  const stop = useCallback(async (id: string) => {
    try {
      await window.flotilla.stop(id);
    } catch (error) {
      setFleetError(error instanceof Error ? error.message : String(error));
    }
    if (selectedId.current === id) {
      selectedId.current = null;
      terminalWrite.current = null;
      setSelected(null);
      setSession(null);
      // A session that no longer exists must not be restored on the next
      // launch, so the remembered one is cleared along with the view.
      if (layoutReady.current) {
        void window.flotilla.saveLayout({ session: null });
      }
    }
    setOpenError(null);
    await refresh(true);
  }, [refresh]);

  const counts = fleet ? summarise(fleet) : { total: 0, reachable: 0 };
  // How many sessions are waiting on the user. It is a count of rows the list
  // will colour, not a second opinion about their state.
  const attention = fleet
    ? fleet.sessions.filter((session) => needsAttention(session.lifecycle)).length
    : 0;

  return (
    <div className="app">
      <aside className="fleet">
        <header>
          <h1>Flotilla</h1>
          <p className="muted">
            {fleet ? `${counts.reachable}/${counts.total} reachable` : "…"}
            {fleet ? ` · pid ${fleet.pid}` : ""}
            {attention > 0 ? ` · ${attention} need you` : ""}
          </p>
          <button type="button" onClick={() => void refresh(true)}>
            Refresh
          </button>
          <button
            type="button"
            onClick={() => setRoute(route === "agents" ? "session" : "agents")}
          >
            {route === "agents" ? "Back" : "Agents"}
          </button>
        </header>

        {fleetError ? <p className="error">{fleetError}</p> : null}
        {fleet?.failures.map((failure) => (
          <p className="warn" key={failure}>
            {failure}
          </p>
        ))}

        <NewSessionForm
          agents={agents.agents}
          unavailable={agentsError}
          loaded={agentsLoaded}
          onStart={start}
          onOpenAgentSettings={() => setRoute("agents")}
        />

        <FleetList
          sessions={fleet?.sessions ?? []}
          selected={selected}
          onSelect={(id) => void open(id)}
          onStop={(id) => void stop(id)}
        />
      </aside>

      <main className="session">
        {openError ? <p className="error">{openError}</p> : null}
        {session ? (
          <SessionView
            state={session}
            view={view}
            onView={setView}
            onPrompt={(text) => void prompt(text)}
            onCancel={() => void cancel()}
            onAnswer={(questionId, optionId) => void answer(questionId, optionId)}
            onTerminalReady={(write) => {
              terminalWrite.current = write;
              // Whatever the host replayed before the terminal existed is
              // written now, in arrival order.
              const queued = pendingOutput.current;
              pendingOutput.current = [];
              for (const bytes of queued) {
                write(bytes);
              }
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
        {/* The settings overlay covers the session instead of replacing it:
            xterm keeps its scrollback and its measured size, so closing the
            panel returns to the same view rather than a re-created term. */}
        {route === "agents" ? (
          <div className="agent-settings-overlay">
            <AgentSettings
              agents={agents.agents}
              problems={agents.problems}
              unavailable={agentsError}
              loaded={agentsLoaded}
              onToggle={toggleAgent}
              onRefresh={() => void refreshAgents()}
              onClose={() => setRoute("session")}
            />
          </div>
        ) : null}
      </main>
    </div>
  );
}
