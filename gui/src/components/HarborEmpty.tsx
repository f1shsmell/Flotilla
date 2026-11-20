import type React from "react";

import { BrandLockup } from "./Brand";

export type HarborMode = "quiet" | "pick" | "compose";

const COPY: Record<HarborMode, { title: string; body: string }> = {
  quiet: {
    title: "The harbor is quiet.",
    body: "Send your first agent to sea — pick an agent below and start.",
  },
  pick: {
    title: "Pick a session to watch it work.",
    body: "Its log, terminal and diff will appear here.",
  },
  compose: {
    title: "New session",
    body: "One agent, one directory, one worktree if you like.",
  },
};

// The harbor's greeting is a projection of the fleet, not a screen of its own:
// the composer card rides inside it so an empty fleet lands on an action.
export function HarborEmpty({
  mode,
  children,
}: {
  mode: HarborMode;
  children?: React.ReactNode;
}): React.ReactElement {
  const copy = COPY[mode];
  return (
    <div className="harbor-empty">
      <BrandLockup />
      <h2>{copy.title}</h2>
      <p className="muted">{copy.body}</p>
      {children}
    </div>
  );
}