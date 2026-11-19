// reconnect is the retry loop behind a dropped session link. It is pure: the
// caller supplies the dial, the sleep and the outcomes, so the backoff policy
// is testable without a daemon or timers.

export type Attempt = { status: "attached"; attached: unknown } | { status: "gone" };

export interface ReconnectOptions {
  // attempt dials once: a throw is retryable, "gone" is terminal.
  attempt: () => Promise<Attempt>;
  onAttached: (attached: unknown) => void;
  onGone: () => void;
  sleep?: (ms: number) => Promise<void>;
}

export interface ReconnectTask {
  done: Promise<void>;
  cancel: () => void;
}

const backoffMs = [250, 500, 1000, 2000, 5000];

const wait = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));

export function reconnect(options: ReconnectOptions): ReconnectTask {
  const sleep = options.sleep ?? wait;
  let cancelled = false;

  const run = async (): Promise<void> => {
    for (let i = 0; !cancelled; i += 1) {
      // The wait comes before the dial, including the first: a link usually
      // drops because the daemon died, and an instant redial would only race
      // its respawn.
      await sleep(backoffMs[Math.min(i, backoffMs.length - 1)]);
      // cancel stops future attempts only. An attempt already in flight runs
      // to completion so the link it opens is delivered, never orphaned.
      if (cancelled) {
        return;
      }
      let result: Attempt;
      try {
        result = await options.attempt();
      } catch {
        continue;
      }
      if (result.status === "gone") {
        options.onGone();
        return;
      }
      options.onAttached(result.attached);
      return;
    }
  };

  return {
    done: run(),
    cancel: () => {
      cancelled = true;
    },
  };
}