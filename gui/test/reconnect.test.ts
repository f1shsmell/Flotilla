import { describe, expect, it } from "vitest";

import { reconnect, type Attempt } from "../electron/reconnect";

// instantSleep records the backoff schedule and resolves at once, so the loop
// is tested without timers: the delays it asks for are the behavior.
function instantSleep(log: number[]): (ms: number) => Promise<void> {
  return (ms) => {
    log.push(ms);
    return Promise.resolve();
  };
}

describe("reconnect", () => {
  it("retries with backoff until the link is back", async () => {
    const delays: number[] = [];
    let calls = 0;
    let attachedWith: unknown = null;

    const task = reconnect({
      attempt: async () => {
        calls += 1;
        if (calls < 3) {
          throw new Error("the daemon is not up yet");
        }
        return { status: "attached", attached: { id: "sess-1" } };
      },
      onAttached: (attached) => {
        attachedWith = attached;
      },
      onGone: () => {
        throw new Error("a retryable failure must not read as gone");
      },
      sleep: instantSleep(delays),
    });

    await task.done;
    expect(calls).toBe(3);
    expect(delays).toEqual([250, 500, 1000]);
    expect(attachedWith).toEqual({ id: "sess-1" });
  });

  it("caps the backoff at the last delay", async () => {
    const delays: number[] = [];
    let calls = 0;

    const task = reconnect({
      attempt: async () => {
        calls += 1;
        if (calls < 8) {
          throw new Error("still down");
        }
        return { status: "attached", attached: null };
      },
      onAttached: () => {},
      onGone: () => {},
      sleep: instantSleep(delays),
    });

    await task.done;
    expect(delays).toEqual([250, 500, 1000, 2000, 5000, 5000, 5000, 5000]);
  });

  it("stops when the session is gone", async () => {
    const delays: number[] = [];
    let calls = 0;
    let gone = 0;

    const task = reconnect({
      attempt: async () => {
        calls += 1;
        return { status: "gone" };
      },
      onAttached: () => {
        throw new Error("a gone session must not read as attached");
      },
      onGone: () => {
        gone += 1;
      },
      sleep: instantSleep(delays),
    });

    await task.done;
    expect(calls).toBe(1);
    expect(gone).toBe(1);
    expect(delays).toEqual([250]);
  });

  it("makes no further attempt once cancelled", async () => {
    let calls = 0;
    const task = reconnect({
      attempt: async () => {
        calls += 1;
        return { status: "attached", attached: null };
      },
      onAttached: () => {
        throw new Error("a cancelled reconnect must not attach");
      },
      onGone: () => {},
      sleep: () => Promise.resolve().then(() => task.cancel()),
    });

    await task.done;
    expect(calls).toBe(0);
  });

  it("delivers an attempt already in flight when cancelled", async () => {
    // The gates hold the loop at a known point without timers: one release
    // moves it exactly one await forward.
    let releaseSleep!: () => void;
    const sleepGate = new Promise<void>((resolve) => {
      releaseSleep = resolve;
    });
    let releaseAttempt!: (value: Attempt) => void;
    const attemptGate = new Promise<Attempt>((resolve) => {
      releaseAttempt = resolve;
    });

    let calls = 0;
    let attachedWith: unknown = null;
    const task = reconnect({
      attempt: () => {
        calls += 1;
        return attemptGate;
      },
      onAttached: (attached) => {
        attachedWith = attached;
      },
      onGone: () => {
        throw new Error("an attached attempt must not read as gone");
      },
      sleep: () => sleepGate,
    });

    releaseSleep();
    await Promise.resolve();
    expect(calls).toBe(1);

    task.cancel();
    releaseAttempt({ status: "attached", attached: { id: "sess-1" } });
    await task.done;
    // The dial finished after the cancel: its link must still be delivered,
    // or the caller would never learn there is one to close.
    expect(attachedWith).toEqual({ id: "sess-1" });
  });
});