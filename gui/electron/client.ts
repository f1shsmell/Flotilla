import net from "node:net";

import {
  FrameDecoder,
  ProtocolError,
  PROTOCOL_VERSION,
  RpcError,
  encodeControl,
  encodeData,
  type Handshake,
  type RpcMessage,
} from "./wire";

export interface ClientHandlers {
  onNotification?: (method: string, params: unknown) => void;
  onData?: (stream: number, payload: Buffer) => void;
  onClose?: (error?: Error) => void;
}

export interface ConnectOptions {
  path: string;
  token: string;
  role: string;
  session?: string;
  handlers?: ClientHandlers;
  timeoutMs?: number;
}

interface Pending {
  method: string;
  resolve: (value: unknown) => void;
  reject: (error: Error) => void;
}

// DaemonClient is one authenticated connection to the daemon. It owns the
// framing and the request bookkeeping; callers only name methods.
export class DaemonClient {
  private readonly decoder = new FrameDecoder();
  private readonly pending = new Map<number, Pending>();
  private nextId = 1;
  private closed = false;
  private onHandshake?: (hello: Handshake) => void;

  private constructor(
    private readonly socket: net.Socket,
    private readonly handlers: ClientHandlers,
  ) {}

  static connect(options: ConnectOptions): Promise<DaemonClient> {
    const timeoutMs = options.timeoutMs ?? 5000;

    return new Promise((resolve, reject) => {
      const socket = net.connect(options.path);
      const client = new DaemonClient(socket, options.handlers ?? {});
      let settled = false;

      const timer = setTimeout(() => {
        socket.destroy();
        if (!settled) {
          settled = true;
          reject(new Error(`handshake with ${options.path} timed out`));
        }
      }, timeoutMs);

      client.onHandshake = (hello) => {
        clearTimeout(timer);
        if (settled) {
          return;
        }
        settled = true;
        if (hello.error) {
          socket.destroy();
          reject(new Error(`${options.path} rejected the connection: ${hello.error}`));
          return;
        }
        if (hello.version !== PROTOCOL_VERSION) {
          socket.destroy();
          reject(
            new Error(
              `peer speaks protocol ${hello.version}, this build speaks ${PROTOCOL_VERSION}`,
            ),
          );
          return;
        }
        resolve(client);
      };

      socket.once("connect", () => {
        socket.write(
          encodeControl({
            version: PROTOCOL_VERSION,
            token: options.token,
            role: options.role,
            session: options.session,
          }),
        );
      });

      socket.on("data", (chunk: Buffer) => client.onChunk(chunk));

      socket.on("error", (error: Error) => {
        if (!settled) {
          settled = true;
          clearTimeout(timer);
          reject(error);
        }
        client.fail(error);
      });

      socket.on("close", () => {
        if (!settled) {
          settled = true;
          clearTimeout(timer);
          reject(new Error(`${options.path} closed before the handshake`));
        }
        client.fail(new Error("connection closed"));
      });
    });
  }

  get alive(): boolean {
    return !this.closed;
  }

  call<T>(method: string, params?: unknown, timeoutMs = 15000): Promise<T> {
    if (this.closed) {
      return Promise.reject(new Error(`${method}: the connection is closed`));
    }

    const id = this.nextId++;
    return new Promise<T>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new RpcError(method, -1, "timed out"));
      }, timeoutMs);

      this.pending.set(id, {
        method,
        resolve: (value) => {
          clearTimeout(timer);
          resolve(value as T);
        },
        reject: (error) => {
          clearTimeout(timer);
          reject(error);
        },
      });

      this.socket.write(encodeControl({ jsonrpc: "2.0", id, method, params }));
    });
  }

  notify(method: string, params?: unknown): void {
    if (this.closed) {
      return;
    }
    this.socket.write(encodeControl({ jsonrpc: "2.0", method, params }));
  }

  sendData(stream: number, payload: Buffer): void {
    if (this.closed) {
      return;
    }
    this.socket.write(encodeData(stream, payload));
  }

  close(): void {
    if (this.closed) {
      return;
    }
    this.closed = true;
    this.socket.destroy();
  }

  private onChunk(chunk: Buffer): void {
    let frames;
    try {
      frames = this.decoder.push(chunk);
    } catch (error) {
      this.fail(error instanceof Error ? error : new Error(String(error)));
      return;
    }

    for (const frame of frames) {
      if (frame.kind === "data") {
        this.handlers.onData?.(frame.stream, frame.payload);
        continue;
      }
      this.onControl(frame.payload);
    }
  }

  private onControl(payload: Buffer): void {
    let message: RpcMessage & Handshake;
    try {
      message = JSON.parse(payload.toString("utf8"));
    } catch {
      this.fail(new ProtocolError("control frame is not JSON"));
      return;
    }

    if (message.method !== undefined) {
      this.handlers.onNotification?.(message.method, message.params);
      return;
    }

    if (message.id !== undefined) {
      const waiting = this.pending.get(message.id);
      if (!waiting) {
        return;
      }
      this.pending.delete(message.id);
      if (message.error) {
        waiting.reject(new RpcError(waiting.method, message.error.code, message.error.message));
      } else {
        waiting.resolve(message.result);
      }
      return;
    }

    this.onHandshake?.(message);
  }

  private fail(error: Error): void {
    if (this.closed) {
      return;
    }
    this.closed = true;

    for (const [, waiting] of this.pending) {
      waiting.reject(error);
    }
    this.pending.clear();
    this.handlers.onClose?.(error);
  }
}
