// Terminal bytes arrive one PTY read at a time, and the renderer pays for every
// message it receives. The main process merges them per stream within one turn
// of the event loop, so what a window sees is one write per burst instead of one
// per read.

import { describe, expect, it } from "vitest";

import { Coalescer, coalesceLimit, type Chunk } from "../electron/stream";

// manual returns a coalescer whose schedule is driven by the test, so an
// assertion never depends on the event loop's timing.
function manual(): {
  coalescer: Coalescer;
  chunks: Chunk[];
  run: () => void;
  pending: () => boolean;
} {
  const chunks: Chunk[] = [];
  let armed: (() => void) | null = null;

  const coalescer = new Coalescer(
    (chunk) => chunks.push(chunk),
    (fn) => {
      armed = fn;
      return fn;
    },
    () => {
      armed = null;
    },
  );

  return {
    coalescer,
    chunks,
    run: () => {
      const fn = armed;
      armed = null;
      fn?.();
    },
    pending: () => armed !== null,
  };
}

function bytes(text: string): Uint8Array {
  return new TextEncoder().encode(text);
}

function text(chunk: Chunk): string {
  return new TextDecoder().decode(chunk.bytes);
}

describe("Coalescer", () => {
  it("holds bytes until the scheduled turn", () => {
    const { coalescer, chunks, run, pending } = manual();

    coalescer.push("sess-1", 1, bytes("hello"));

    expect(chunks).toHaveLength(0);
    expect(pending()).toBe(true);

    run();

    expect(chunks).toHaveLength(1);
    expect(text(chunks[0])).toBe("hello");
    expect(chunks[0].session).toBe("sess-1");
    expect(chunks[0].stream).toBe(1);
  });

  it("merges the reads of one stream into one message", () => {
    const { coalescer, chunks, run } = manual();

    coalescer.push("sess-1", 1, bytes("hel"));
    coalescer.push("sess-1", 1, bytes("lo "));
    coalescer.push("sess-1", 1, bytes("world"));

    run();

    expect(chunks).toHaveLength(1);
    expect(text(chunks[0])).toBe("hello world");
  });

  it("keeps two sessions apart", () => {
    const { coalescer, chunks, run } = manual();

    coalescer.push("sess-a", 1, bytes("a"));
    coalescer.push("sess-b", 1, bytes("b"));

    run();

    expect(chunks).toHaveLength(2);
    expect(chunks.map((chunk) => chunk.session).sort()).toEqual(["sess-a", "sess-b"]);
  });

  it("keeps two streams of one session apart", () => {
    const { coalescer, chunks, run } = manual();

    coalescer.push("sess-a", 1, bytes("out"));
    coalescer.push("sess-a", 2, bytes("in"));

    run();

    expect(chunks).toHaveLength(2);
    expect(chunks.map((chunk) => chunk.stream).sort()).toEqual([1, 2]);
  });

  it("splits a burst that is larger than one message", () => {
    const { coalescer, chunks, run } = manual();

    const big = new Uint8Array(coalesceLimit + 10);
    big.fill(7);
    coalescer.push("sess-1", 1, big);

    run();

    expect(chunks).toHaveLength(2);
    expect(chunks[0].bytes).toHaveLength(coalesceLimit);
    expect(chunks[1].bytes).toHaveLength(10);
    // Both halves are the same byte, in order: a split never reorders.
    expect(chunks[0].bytes[0]).toBe(7);
    expect(chunks[1].bytes[0]).toBe(7);
  });

  it("drops an empty read instead of sending it", () => {
    const { coalescer, chunks, run, pending } = manual();

    coalescer.push("sess-1", 1, new Uint8Array(0));

    expect(pending()).toBe(false);
    run();
    expect(chunks).toHaveLength(0);
  });

  it("drains what it holds on close and refuses what follows", () => {
    const { coalescer, chunks } = manual();

    coalescer.push("sess-1", 1, bytes("before the close"));
    coalescer.close();

    // close drains synchronously: a window that is going away still paints its
    // last frame.
    expect(chunks).toHaveLength(1);
    expect(text(chunks[0])).toBe("before the close");

    coalescer.push("sess-1", 1, bytes("after the close"));
    coalescer.flush();
    expect(chunks).toHaveLength(1);
  });

  it("flush emits without waiting for the scheduled turn", () => {
    const { coalescer, chunks } = manual();

    coalescer.push("sess-1", 1, bytes("now"));
    coalescer.flush();

    expect(chunks).toHaveLength(1);
    expect(text(chunks[0])).toBe("now");
  });

  it("copies the bytes it emits", () => {
    const { coalescer, chunks, run } = manual();

    const source = bytes("abc");
    coalescer.push("sess-1", 1, source);
    run();

    // Mutating the read buffer after the fact must not change what was sent:
    // the renderer holds these bytes after the socket read is long gone.
    source.fill(0);

    expect(text(chunks[0])).toBe("abc");
  });
});
