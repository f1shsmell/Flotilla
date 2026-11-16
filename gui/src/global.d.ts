import type { Status } from "./store";

interface FlotillaBridge {
  smoke: boolean;
  fleet(): Promise<unknown>;
  start(request: unknown): Promise<unknown>;
  stop(sessionId: string): Promise<unknown>;
  detach(sessionId: string): Promise<unknown>;
  open(sessionId: string): Promise<unknown>;
  prompt(sessionId: string, text: string): Promise<unknown>;
  seen(sessionId: string): Promise<unknown>;
  cancel(sessionId: string): Promise<unknown>;
  answer(sessionId: string, questionId: string, optionId: string): Promise<unknown>;
  input(sessionId: string, text: string): Promise<unknown>;
  resize(sessionId: string, cols: number, rows: number): Promise<unknown>;
  diff(sessionId: string, scope: string, path?: string): Promise<unknown>;
  commit(sessionId: string, message: string): Promise<unknown>;
  push(sessionId: string): Promise<unknown>;
  pr(sessionId: string, request: unknown): Promise<unknown>;
  comments(sessionId: string): Promise<unknown>;
  addComment(sessionId: string, comment: unknown): Promise<unknown>;
  removeComment(sessionId: string, id: string): Promise<unknown>;
  layout(): Promise<unknown>;
  saveLayout(patch: unknown): Promise<unknown>;
  onEvent(handler: (event: unknown) => void): () => void;
}

declare global {
  interface Window {
    flotilla: FlotillaBridge;
  }
}

export type { Status };
