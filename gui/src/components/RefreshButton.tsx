import { useEffect, useRef, useState } from "react";

import { IconRefresh } from "./Icons";

// RefreshButton is the one refresh control in the window. The fleet, the agent
// catalogue and a diff all offer the same action, so they offer it with the same
// words, the same icon and the same answer: a press runs the read, and the
// icon turns while it is running.
//
// The spin is deliberately small and short. A refresh usually finishes in a
// few milliseconds, and a button that flashes a full rotation for that is a
// button that looks broken; this turns once, then stops.
export function RefreshButton({
  onRefresh,
  label = "Refresh",
}: {
  onRefresh: () => void | Promise<void>;
  label?: string;
}): React.ReactElement {
  const [running, setRunning] = useState(false);
  const alive = useRef(true);

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  const run = (): void => {
    if (running) {
      return;
    }
    setRunning(true);
    void Promise.resolve(onRefresh()).finally(() => {
      // A read that finished after the component unmounted has nothing to
      // update; setting state here would be a warning for no benefit.
      if (alive.current) {
        setRunning(false);
      }
    });
  };

  return (
    <button
      type="button"
      className={running ? "refresh running" : "refresh"}
      aria-busy={running}
      onClick={run}
    >
      <IconRefresh />
      {label}
    </button>
  );
}
