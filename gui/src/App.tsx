import { useCallback, useEffect, useRef, useState } from "react";

import { constants } from "../electron/contract";
import { AgentSettings } from "./components/AgentSettings";
import { ArchiveView } from "./components/ArchiveView";
import { BrandMark } from "./components/Brand";
import { FleetList } from "./components/FleetList";
import { HarborEmpty } from "./components/HarborEmpty";
import { IconBack, IconInbox, IconPlus, IconSliders } from "./components/Icons";
import { InboxView } from "./components/InboxView";
import { NewSessionForm } from "./components/NewSessionForm";
import { RefreshButton } from "./components/RefreshButton";
import { SessionView } from "./components/SessionView";
import { StatusBar } from "./components/StatusBar";
import { mainEmpty, promptRoute, watchedFinish, windowTitle } from "./fleetView";
import {
  addPermission,
  answerPermission,
  applyHistory,
  applyUpdate,
  controlError,
  endTurn,
  failTurn,
  loseSession,
  newSession,
  parseAgents,
  parseMessageQueue,
  parseStatus,
  startTurn,
  withControls,
  withLifecycle,
  type AgentChange,
  type AgentListing,
  type SessionState,
  type StartRequest,
  type Status,
} from "./store";

interface WireEvent {
  type: "update" | "permission" | "state" | "surface" | "data" | "closed" | "reattached";
  session: string;
  update?: unknown;
  question?: unknown;
  state?: unknown;
  controls?: unknown;
  // attached is the fresh attach answer a heal delivers, shaped like open's.
  attached?: unknown;
  // bytes is one coalesced run of terminal output. It crosses the preload
  // boundary as a Uint8Array: structured clone copies the bytes, and nothing
  // has to decode them.
  bytes?: Uint8Array;
  error?: string;
}

interface Attached {
  info?: { mode?: string; state?: unknown };
  history?: unknown;
  history_more?: boolean;
  surface?: unknown;
}

// attachedState folds an attach answer into a view: the log the daemon kept
// goes in before any live update, and the surface rides along so the selectors
// are filled on the first frame. Open and heal read one definition.
function attachedState(id: string, attached: Attached): SessionState {
  return withControls(
    applyHistory(newSession(id, attached.info?.mode ?? "pty"), {
      updates: attached.history,
      more: attached.history_more === true,
    }),
    attached.surface,
  );
}

export function App(): React.ReactElement {
  const [fleet, setFleet] = useState<Status | null>(null);
  const [fleetError, setFleetError] = useState<string | null>(null);
  // agents is the daemon's catalogue. It changes when the user edits a switch,
  // so it is read again after every write rather than patched in the renderer.
  const [agents, setAgents] = useState<AgentListing>({ agents: [], problems: [], registry: [] });
  // agentsError is why the catalogue is empty, when it is empty because the
  // daemon could not be reached rather than because the machine has no agents.
  const [agentsError, setAgentsError] = useState<string | null>(null);
  // agentsLoaded separates "not read yet" from "read, and the machine has
  // none": an empty list means the second only once an answer arrived.
  const [agentsLoaded, setAgentsLoaded] = useState(false);
  // route is which main panel the window shows: the session view, or the agent
  // settings the picker sends the user to when nothing installed matches.
  const [route, setRoute] = useState<"session" | "agents">("session");
  // inboxOpen is the cross-session list of what waits on the user. It is a
  // layer over the session pane, like the agent settings page.
  const [inboxOpen, setInboxOpen] = useState(false);
  const [selected, setSelected] = useState<string | null>(null);
  // archivedId is the finished run on screen. It is its own field rather than a
  // second meaning for `selected`: a live session and a log are read by
  // different calls, and one id cannot say which view to build.
  const [archivedId, setArchivedId] = useState<string | null>(null);
  const [composing, setComposing] = useState(false);
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
  // sessionLive is the id whose state the pane holds on a live link: a second
  // click on that row must not dial again and rebuild from a cached answer.
  const sessionLive = useRef<string | null>(null);
  // paneShowing is whether the session pane is the visible layer. The event
  // subscription is installed once, so it reads that through a ref.
  const paneShowing = useRef(true);

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
          case "surface":
            next = withControls(next, event.controls);
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
    paneShowing.current = route === "session" && !inboxOpen && !composing;
  }, [route, inboxOpen, composing]);

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

  // The queue changes on two clocks: this window's edits and a finished turn
  // releasing the next message. The reads are serialized, so a stale answer
  // cannot land after a newer one and put back a row already delivered.
  const queueTurn = useRef<Promise<unknown>>(Promise.resolve());
  const serialQueue = useCallback(<T,>(task: () => Promise<T>): Promise<T> => {
    const next = queueTurn.current.then(task);
    queueTurn.current = next.catch(() => undefined);
    return next;
  }, []);

  const applyQueue = useCallback((id: string, response: unknown) => {
    const messages = parseMessageQueue(response);
    setSession((current) =>
      current && current.id === id ? { ...current, queue: messages } : current,
    );
  }, []);

  // refreshQueue reads the messages waiting behind the running turn. Delivery
  // starts one without a call from this window, so the queue is read again on
  // every lifecycle change and after a rebuild, not only after an edit.
  const refreshQueue = useCallback(
    (id: string) => {
      void serialQueue(async () => {
        try {
          applyQueue(id, await window.flotilla.queue(id));
        } catch {
          // A link that is gone reports itself elsewhere; the strip keeps what
          // it last knew rather than flashing empty.
        }
      });
    },
    [applyQueue, serialQueue],
  );

  useEffect(() => {
    return window.flotilla.onEvent((payload) => {
      const event = payload as WireEvent;

      if (event.type === "data") {
        if (event.session === selectedId.current && event.bytes) {
          // The bytes are already a Uint8Array over the preload boundary, so
          // there is nothing to decode here: one copy in the main process, no
          // base64 on either side.
          const write = terminalWrite.current;
          if (write) {
            write(event.bytes);
          } else {
            pendingOutput.current.push(event.bytes);
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
        // A finished turn releases the next queued message with no call from
        // this window, so the strip is read again on every change of the
        // session it shows.
        if (sessionLive.current === event.session) {
          void refreshQueue(event.session);
        }
        // A turn that finishes while its session is on screen has been watched,
        // so it is not unread for this window. Only this event knows both
        // facts; a click sees the attach answer, which predates the turn.
        if (paneShowing.current && watchedFinish(event.session, selectedId.current, update.state)) {
          void window.flotilla
            .seen(event.session)
            .catch(() => {
              // A host that went away cannot record seen; the row keeps its badge.
            })
            // The inbox and its count are projected by the daemon, so read them
            // once the look is recorded rather than behind the next poll.
            .finally(() => void refresh());
          return;
        }
        void refresh();
        return;
      }

      // A closed link means the pane no longer holds a live session: opening it
      // again must dial a new one instead of taking the early return.
      if (event.type === "closed" && event.session === selectedId.current) {
        sessionLive.current = null;
      }

      // A heal rebuilt the link. The view starts over from the fresh attach
      // answer, exactly as an open would: the history it carries already
      // contains anything the dead link delivered.
      if (event.type === "reattached") {
        if (event.session !== selectedId.current) {
          return;
        }
        const attached = (event.attached ?? {}) as Attached;
        // Events queued from the dead link must not land on the rebuilt state:
        // the history already has them, and a stale "closed" would re-lose it.
        queue.current = queue.current.filter((queued) => queued.session !== event.session);
        setSession(attachedState(event.session, attached));
        sessionLive.current = event.session;
        void refreshQueue(event.session);
        if ((attached.info?.state ?? "") === "done") {
          void window.flotilla.seen(event.session).catch(() => {
            // A host that went away cannot record seen; the row keeps its badge.
          });
        }
        return;
      }

      queue.current.push(event);
      schedule();
    });
  }, [schedule, refresh, refreshQueue]);

  useEffect(() => {
    void refresh(true);
    const timer = setInterval(() => void refresh(), 2000);
    return () => clearInterval(timer);
  }, [refresh]);

  // The title names the channel the way the badge does: two installed
  // channels must be told apart in the taskbar, where nothing else travels.
  const channel = fleet?.channel ?? null;
  useEffect(() => {
    document.title = windowTitle(channel);
  }, [channel]);

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
          setAgents({ agents: [], problems: [], registry: [] });
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
    // Opening a session is what the window shows, and a covering panel would
    // hide the session the user just asked for.
    setRoute("session");
    setInboxOpen(false);
    setComposing(false);
    // A live session replaces the log on screen: two views of two sessions in
    // one pane would leave the header describing neither.
    setArchivedId(null);

    // The pane already holds this session on a live link. Its attach answer is
    // cached from that dial, so opening again would rewind the log and leave
    // the terminal's writer unregistered; clicking it is still looking at it.
    if (sessionLive.current === id) {
      void window.flotilla.seen(id).catch(() => {
        // A host that went away cannot record seen; the row keeps its badge.
      });
      return;
    }

    const previous = selectedId.current;
    if (previous !== null && previous !== id) {
      // Leaving a session detaches it instead of keeping a silent subscriber:
      // the host keeps running, but it stops streaming to a view that is no
      // longer on screen, which is what lets a terminal hold its recent output
      // for the next visit.
      void window.flotilla.detach(previous);
      sessionLive.current = null;
      terminalWrite.current = null;
      // Bytes queued for the previous session belong to that session's screen,
      // not this one's.
      pendingOutput.current = [];
    }

    setSelected(id);
    selectedId.current = id;
    setOpenError(null);
    try {
      const attached = (await window.flotilla.open(id)) as Attached;

      // The log the daemon kept for this session goes in with the state, before
      // any live update: a window that opens a turn an hour old should show how
      // the agent got there, not start at its own arrival.
      setSession(attachedState(id, attached));
      sessionLive.current = id;
      void refreshQueue(id);

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
        sessionLive.current = null;
        setSelected(null);
      }
      setOpenError(error instanceof Error ? error.message : String(error));
    }
  }, [refreshQueue]);

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
      const lifecycle =
        fleet?.sessions.find((entry) => entry.id === id)?.lifecycle ?? "unknown";

      // A running turn takes no second prompt: an agent without steering gets
      // the message queued by the daemon instead. An agent that advertised
      // steering takes it into the turn.
      const steer = fleet?.sessions.find((entry) => entry.id === id)?.steer ?? false;
      const route = promptRoute(
        session.mode,
        session.turn === "working" ? "working" : lifecycle,
        steer,
      );

      if (route === "steer") {
        try {
          const delivery = (await window.flotilla.steer(id, text)) as {
            route?: string;
          };
          if (delivery?.route === constants.session.SteerQueued) {
            // The turn ended before the message reached it; the daemon kept it.
            applyQueue(id, delivery);
          }
        } catch (error) {
          const message = error instanceof Error ? error.message : String(error);
          setSession((current) => (current ? { ...current, error: message } : current));
        }
        return;
      }

      if (route === "queue") {
        await serialQueue(async () => {
          try {
            applyQueue(id, await window.flotilla.queueAdd(id, text));
          } catch (error) {
            const message = error instanceof Error ? error.message : String(error);
            setSession((current) => (current ? { ...current, error: message } : current));
          }
        });
        return;
      }

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
    [session, fleet, serialQueue, applyQueue],
  );

  // A queue mutation answers with the whole queue, so the strip renders the
  // daemon's order. A refusal is usually a row that started its turn: the
  // error is shown and the queue read again, so the row stops claiming it.
  const queueRefused = useCallback(
    async (id: string, error: unknown) => {
      const message = error instanceof Error ? error.message : String(error);
      setSession((current) => (current ? { ...current, error: message } : current));
      try {
        applyQueue(id, await window.flotilla.queue(id));
      } catch {
        // The link is gone; the row keeps the last place it was given.
      }
    },
    [applyQueue],
  );

  const queueEdit = useCallback(
    (queueId: string, text: string) => {
      if (!session) {
        return;
      }
      const id = session.id;
      void serialQueue(async () => {
        try {
          applyQueue(id, await window.flotilla.queueEdit(id, queueId, text));
        } catch (error) {
          await queueRefused(id, error);
        }
      });
    },
    [session, serialQueue, applyQueue, queueRefused],
  );

  const queueRemove = useCallback(
    (queueId: string) => {
      if (!session) {
        return;
      }
      const id = session.id;
      void serialQueue(async () => {
        try {
          applyQueue(id, await window.flotilla.queueRemove(id, queueId));
        } catch (error) {
          await queueRefused(id, error);
        }
      });
    },
    [session, serialQueue, applyQueue, queueRefused],
  );

  const queueMove = useCallback(
    (queueId: string, to: number) => {
      if (!session) {
        return;
      }
      const id = session.id;
      void serialQueue(async () => {
        try {
          applyQueue(id, await window.flotilla.queueMove(id, queueId, to));
        } catch (error) {
          await queueRefused(id, error);
        }
      });
    },
    [session, serialQueue, applyQueue, queueRefused],
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

  // A control write is not a turn: the agent's own notification moves the
  // selectors, and a refusal only reports itself. The select keeps showing the
  // last value the agent confirmed, never the one the click asked for.
  const setControl = useCallback(
    async (controlId: string, value: string) => {
      if (!session) {
        return;
      }
      try {
        await window.flotilla.surfaceSet(session.id, controlId, value);
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        setSession((current) => (current ? controlError(current, message) : current));
      }
    },
    [session],
  );

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

  // Opening a finished run leaves whatever live session was on screen. The
  // detach is the same one a switch between sessions does: the host keeps
  // running, and this window simply stops watching it.
  const openArchived = useCallback((id: string) => {
    setRoute("session");
    setInboxOpen(false);
    setComposing(false);

    const previous = selectedId.current;
    if (previous !== null) {
      void window.flotilla.detach(previous);
      selectedId.current = null;
      sessionLive.current = null;
      terminalWrite.current = null;
      pendingOutput.current = [];
      setSelected(null);
      setSession(null);
    }
    setOpenError(null);
    setArchivedId(id);
  }, []);

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
      sessionLive.current = null;
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

  // The daemon owns the pin, so the window asks and then reads the status it
  // answered with rather than flipping a local flag the daemon might refuse.
  const protect = useCallback(async (id: string, on: boolean) => {
    try {
      await window.flotilla.protect(id, on);
    } catch (error) {
      setFleetError(error instanceof Error ? error.message : String(error));
    }
    await refresh(true);
  }, [refresh]);

  const inboxCount = fleet?.inbox?.length ?? 0;

  const composerCard = (
    <div className="composer-card">
      {composing ? (
        <button
          type="button"
          className="link composer-close"
          onClick={() => setComposing(false)}
        >
          Close
        </button>
      ) : null}
      <NewSessionForm
        agents={agents.agents}
        registry={agents.registry}
        unavailable={agentsError}
        loaded={agentsLoaded}
        onStart={start}
        embedded={composing}
        onOpenAgentSettings={() => {
          setComposing(false);
          setInboxOpen(false);
          setRoute("agents");
        }}
      />
    </div>
  );

  return (
    <div className="app">
      <aside className="fleet">
        <header>
          <div className="brand">
            <BrandMark />
            <h1>Flotilla</h1>
            <span className="chip chip-channel">{fleet ? fleet.channel : "…"}</span>
          </div>
          <div className="fleet-tools">
            {/* With an empty fleet the composer card is already the first
                view's action point, so a second button for it would be dead. */}
            {session !== null || mainEmpty(fleet) === "pick" ? (
              <button
                type="button"
                className="primary fleet-new"
                aria-pressed={composing}
                onClick={() => {
                  // A covering panel paints above the composer card, so opening
                  // the card must also leave the panels.
                  setRoute("session");
                  setInboxOpen(false);
                  setComposing((open) => !open);
                }}
              >
                <IconPlus />
                New session
              </button>
            ) : null}
            <RefreshButton onRefresh={() => refresh(true)} />
            <button
              type="button"
              aria-pressed={inboxOpen}
              onClick={() => {
                setComposing(false);
                setRoute("session");
                setInboxOpen((open) => !open);
              }}
            >
              <IconInbox />
              Inbox{inboxCount > 0 ? ` · ${inboxCount}` : ""}
            </button>
            <button
              type="button"
              onClick={() => {
                setComposing(false);
                setInboxOpen(false);
                setRoute(route === "agents" ? "session" : "agents");
              }}
            >
              {route === "agents" ? <IconBack /> : <IconSliders />}
              {route === "agents" ? "Back" : "Agents"}
            </button>
          </div>
        </header>

        {fleetError ? (
          <div className="banner banner-error" role="alert">
            {fleetError}
          </div>
        ) : null}
        {fleet?.failures.map((failure) => (
          <div className="banner banner-warn" key={failure}>
            {failure}
          </div>
        ))}

        <FleetList
          sessions={fleet?.sessions ?? []}
          archived={fleet?.archived ?? []}
          selected={archivedId ?? selected}
          onSelect={(id) => void open(id)}
          onStop={(id) => void stop(id)}
          onSelectArchived={openArchived}
        />
      </aside>

      <main className="session">
        {openError ? (
          <div className="banner banner-error" role="alert">
            {openError}
          </div>
        ) : null}
        {session ? (
          <SessionView
            state={session}
            lifecycle={
              fleet?.sessions.find((entry) => entry.id === session.id)?.lifecycle ?? "unknown"
            }
            protected={
              fleet?.sessions.find((entry) => entry.id === session.id)?.protected ?? false
            }
            steer={fleet?.sessions.find((entry) => entry.id === session.id)?.steer ?? false}
            view={view}
            onView={setView}
            onPrompt={(text) => void prompt(text)}
            onCancel={() => void cancel()}
            onQueueEdit={(queueId, text) => void queueEdit(queueId, text)}
            onQueueRemove={(queueId) => void queueRemove(queueId)}
            onQueueMove={(queueId, to) => void queueMove(queueId, to)}
            onProtect={(on) => void protect(session.id, on)}
            onSetControl={(controlId, value) => void setControl(controlId, value)}
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
            onResize={(cols, rows) => {
              // A resize while the link is healing has no pipe to travel on.
              // It is best-effort: the PTY keeps the size it last heard, and
              // the next measure sends the new one.
              void window.flotilla.resize(session.id, cols, rows).catch(() => {});
            }}
          />
        ) : archivedId !== null ? (
          <ArchiveView
            key={archivedId}
            entry={
              fleet?.archived.find((run) => run.id === archivedId) ?? {
                id: archivedId,
                mode: "",
                command: "",
                dir: "",
                started: "",
                ended: "",
              }
            }
            onBack={() => setArchivedId(null)}
          />
        ) : (
          <HarborEmpty mode={composing ? "compose" : mainEmpty(fleet)}>
            {composing || mainEmpty(fleet) === "quiet" ? composerCard : null}
          </HarborEmpty>
        )}
        {/* Composing over a live session covers it like the settings panel does:
            the terminal stays mounted underneath with its scrollback intact. */}
        {session !== null && composing ? (
          <div className="composer-overlay">
            <HarborEmpty mode="compose">{composerCard}</HarborEmpty>
          </div>
        ) : null}
        {/* The settings overlay covers the session instead of replacing it:
            xterm keeps its scrollback and its measured size, so closing the
            panel returns to the same view rather than a re-created term. */}
        {route === "agents" ? (
          <div className="agent-settings-overlay">
            <AgentSettings
              agents={agents.agents}
              registry={agents.registry}
              problems={agents.problems}
              unavailable={agentsError}
              loaded={agentsLoaded}
              onToggle={toggleAgent}
              onRefresh={() => void refreshAgents()}
              onClose={() => setRoute("session")}
            />
          </div>
        ) : null}
        {inboxOpen ? (
          <div className="inbox-overlay">
            <InboxView
              entries={fleet?.inbox ?? []}
              known={fleet !== null}
              onOpen={(id) => {
                setInboxOpen(false);
                void open(id);
              }}
              onClose={() => setInboxOpen(false)}
            />
          </div>
        ) : null}
      </main>
      <StatusBar status={fleet} />
    </div>
  );
}
