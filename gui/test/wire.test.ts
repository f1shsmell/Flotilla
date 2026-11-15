import { describe, expect, it } from "vitest";

import {
  FrameDecoder,
  MAX_FRAME_SIZE,
  ProtocolError,
  encodeControl,
  encodeData,
  encodeFrame,
} from "../electron/wire";

describe("frame codec", () => {
  it("round-trips a control frame", () => {
    const decoder = new FrameDecoder();
    const frames = decoder.push(encodeControl({ jsonrpc: "2.0", method: "x" }));

    expect(frames).toHaveLength(1);
    expect(frames[0].kind).toBe("control");
    expect(frames[0].stream).toBe(0);
    expect(JSON.parse(frames[0].payload.toString())).toEqual({
      jsonrpc: "2.0",
      method: "x",
    });
  });

  it("round-trips a data frame on its own stream", () => {
    const decoder = new FrameDecoder();
    const frames = decoder.push(encodeData(2, Buffer.from("hi")));

    expect(frames[0]).toMatchObject({ kind: "data", stream: 2 });
    expect(frames[0].payload.toString()).toBe("hi");
  });

  it("reassembles a frame split across reads", () => {
    const wire = encodeData(1, Buffer.from("hello world"));
    const decoder = new FrameDecoder();

    expect(decoder.push(wire.subarray(0, 4))).toHaveLength(0);
    expect(decoder.push(wire.subarray(4, 9))).toHaveLength(0);

    const frames = decoder.push(wire.subarray(9));
    expect(frames).toHaveLength(1);
    expect(frames[0].payload.toString()).toBe("hello world");
  });

  it("decodes several frames from one read", () => {
    const decoder = new FrameDecoder();
    const frames = decoder.push(
      Buffer.concat([encodeData(1, Buffer.from("a")), encodeData(1, Buffer.from("b"))]),
    );

    expect(frames.map((frame) => frame.payload.toString())).toEqual(["a", "b"]);
  });

  it("refuses a frame past the limit instead of allocating it", () => {
    const header = Buffer.alloc(9);
    header[0] = 0x01;
    header.writeUInt32LE(MAX_FRAME_SIZE + 1, 5);

    expect(() => new FrameDecoder().push(header)).toThrow(ProtocolError);
  });

  it("refuses an unknown frame kind", () => {
    const header = Buffer.alloc(9);
    header[0] = 0x09;

    expect(() => new FrameDecoder().push(header)).toThrow(ProtocolError);
  });

  it("refuses to encode a payload past the limit", () => {
    expect(() =>
      encodeFrame({ kind: "data", stream: 1, payload: Buffer.alloc(MAX_FRAME_SIZE + 1) }),
    ).toThrow(ProtocolError);
  });
});
