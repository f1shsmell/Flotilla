// Terminal bytes arrive one PTY read at a time. Sending each read across IPC on
// its own is what turns a build log flood into a frame-per-chunk cost in the
// renderer, so the bytes of one stream are merged within one turn of the event
// loop first. The cap keeps a single message bounded: a burst larger than that
// is split, in order, rather than copied into one unbounded array.
//
// The bytes travel as a Uint8Array, not as base64: structured clone copies the
// array as bytes, while base64 spent a third more bandwidth and one string
// allocation per chunk.

export interface Chunk {
  session: string;
  stream: number;
  bytes: Uint8Array;
}

export type Emit = (chunk: Chunk) => void;

// coalesceLimit bounds one emitted message. A burst past it becomes several
// messages, which costs a frame each but never an unbounded allocation.
export const coalesceLimit = 64 * 1024;

interface Pending {
  session: string;
  stream: number;
  parts: Uint8Array[];
  size: number;
}

// Coalescer merges the bytes of one (session, stream) pair until the next
// event-loop turn. Order is the arrival order, per key: two sessions interleave
// freely, one session's bytes never do.
export class Coalescer {
  private readonly pending = new Map<string, Pending>();
  private armed: unknown = null;
  private closed = false;

  constructor(
    private readonly emit: Emit,
    private readonly schedule: (fn: () => void) => unknown = (fn) => setImmediate(fn),
    private readonly cancel: (handle: unknown) => void = (handle) =>
      clearImmediate(handle as ReturnType<typeof setImmediate>),
  ) {}

  push(session: string, stream: number, payload: Uint8Array): void {
    if (this.closed || payload.length === 0) {
      return;
    }

    const key = `${session}\u0000${stream}`;
    const pending = this.pending.get(key);
    if (pending === undefined) {
      this.pending.set(key, { session, stream, parts: [payload], size: payload.length });
    } else {
      pending.parts.push(payload);
      pending.size += payload.length;
    }

    if (this.armed === null) {
      this.armed = this.schedule(() => {
        this.armed = null;
        this.flush();
      });
    }
  }

  // flush emits what is buffered. It is public so a shutdown can drain before
  // the window goes, and so a test does not have to wait on the event loop.
  flush(): void {
    if (this.armed !== null) {
      this.cancel(this.armed);
      this.armed = null;
    }

    const batches = [...this.pending.values()];
    this.pending.clear();

    for (const batch of batches) {
      let merged = concat(batch.parts, batch.size);
      while (merged.length > coalesceLimit) {
        this.emit({ session: batch.session, stream: batch.stream, bytes: merged.slice(0, coalesceLimit) });
        merged = merged.subarray(coalesceLimit);
      }
      if (merged.length > 0) {
        this.emit({ session: batch.session, stream: batch.stream, bytes: merged });
      }
    }
  }

  // close drains what is buffered and refuses anything after it. A connection
  // that is going away still owes the renderer the bytes it already accepted.
  close(): void {
    this.flush();
    this.closed = true;
  }
}

// concat copies the parts into one array. The copy is deliberate: a part comes
// from a socket read, and the merged bytes outlive the read that produced it.
// A single part is copied too, so the emitted value is always a plain
// Uint8Array and never a Node Buffer view over a socket's own memory.
function concat(parts: Uint8Array[], size: number): Uint8Array {
  if (parts.length === 1) {
    return new Uint8Array(parts[0]);
  }

  const out = new Uint8Array(size);
  let at = 0;
  for (const part of parts) {
    out.set(part, at);
    at += part.length;
  }
  return out;
}
