// The wire between a client and the daemon. One authenticated pipe carries two
// planes: control frames are JSON-RPC, data frames are terminal bytes. The
// header is the Go side's, byte for byte: kind(1) + stream(4 LE) + length(4 LE).
//
// The numbers are read from contract.ts, which cmd/flt-contract generates from
// the Go constants: a frame size or a protocol version that changes on one side
// fails the contract gate instead of drifting here.

import { constants } from "./contract";

export const PROTOCOL_VERSION = Number(constants.ipc.ProtocolVersion);
export const MAX_FRAME_SIZE = Number(constants.ipc.MaxFrameSize);
export const STREAM_OUTPUT = Number(constants.session.StreamOutput);
export const STREAM_INPUT = Number(constants.session.StreamInput);

// The kind bytes are written in the source as hex literals; they are parsed so
// the contract can keep them in the form the Go side declares them.
const KIND_CONTROL = Number(constants.ipc.KindControl);
const KIND_DATA = Number(constants.ipc.KindData);
const HEADER_SIZE = Number(constants.ipc.HeaderSize);

export type Frame = { kind: "control" | "data"; stream: number; payload: Buffer };

export class ProtocolError extends Error {}

export function encodeFrame(frame: Frame): Buffer {
  if (frame.payload.length > MAX_FRAME_SIZE) {
    throw new ProtocolError(
      `frame of ${frame.payload.length} bytes exceeds ${MAX_FRAME_SIZE}`,
    );
  }

  const header = Buffer.alloc(HEADER_SIZE);
  header[0] = frame.kind === "control" ? KIND_CONTROL : KIND_DATA;
  header.writeUInt32LE(frame.stream, 1);
  header.writeUInt32LE(frame.payload.length, 5);

  return Buffer.concat([header, frame.payload]);
}

export function encodeControl(payload: unknown): Buffer {
  return encodeFrame({
    kind: "control",
    stream: 0,
    payload: Buffer.from(JSON.stringify(payload), "utf8"),
  });
}

export function encodeData(stream: number, payload: Buffer): Buffer {
  return encodeFrame({ kind: "data", stream, payload });
}

// FrameDecoder turns one stream of bytes into frames. A frame may be split
// across reads, and one read may carry several: the decoder owns the boundary,
// so callers never parse bytes themselves.
export class FrameDecoder {
  private buffer: Buffer = Buffer.alloc(0);

  push(chunk: Buffer): Frame[] {
    this.buffer = this.buffer.length === 0 ? chunk : Buffer.concat([this.buffer, chunk]);

    const frames: Frame[] = [];
    while (this.buffer.length >= HEADER_SIZE) {
      const kind = this.buffer[0];
      const stream = this.buffer.readUInt32LE(1);
      const length = this.buffer.readUInt32LE(5);

      if (kind !== KIND_CONTROL && kind !== KIND_DATA) {
        throw new ProtocolError(`unknown frame kind ${kind}`);
      }
      if (length > MAX_FRAME_SIZE) {
        throw new ProtocolError(`frame of ${length} bytes exceeds ${MAX_FRAME_SIZE}`);
      }
      if (this.buffer.length < HEADER_SIZE + length) {
        break;
      }

      const payload = this.buffer.subarray(HEADER_SIZE, HEADER_SIZE + length);
      this.buffer = this.buffer.subarray(HEADER_SIZE + length);
      frames.push({
        kind: kind === KIND_CONTROL ? "control" : "data",
        stream,
        payload: Buffer.from(payload),
      });
    }

    return frames;
  }
}

export interface Handshake {
  version: number;
  token?: string;
  role?: string;
  // client names the window behind the connection. It is the same for every
  // connection one window opens, so the daemon can answer "has this window
  // looked at the finished turn" without confusing one window for another.
  client?: string;
  session?: string;
  error?: string;
}

export interface RpcMessage {
  jsonrpc: string;
  id?: number;
  method?: string;
  params?: unknown;
  result?: unknown;
  error?: { code: number; message: string };
}

export class RpcError extends Error {
  constructor(
    readonly method: string,
    readonly code: number,
    message: string,
  ) {
    super(`${method} failed (${code}): ${message}`);
  }

  // is reports whether this failure is the class the code names. A caller
  // branches on this rather than on the message: the wording is for a log, and
  // the code is the contract.
  is(code: number): boolean {
    return this.code === code;
  }

  // isUnknownSession, isUnknownMethod and isLocked are the three classes a
  // window acts on: a session that is gone, a method this daemon does not speak
  // (an older build), and another daemon holding the channel.
  isUnknownSession(): boolean {
    return this.is(Number(constants.ipc.CodeUnknownSession));
  }

  isUnknownMethod(): boolean {
    return this.is(Number(constants.ipc.CodeUnknownMethod));
  }

  isLocked(): boolean {
    return this.is(Number(constants.ipc.CodeAlreadyRunning));
  }
}
