import type { Status } from "./store";

interface FlotillaBridge {
  smoke: boolean;
  fleet(): Promise<unknown>;
  start(request: unknown): Promise<unknown>;
  open(sessionId: string): Promise<unknown>;
  prompt(sessionId: string, text: string): Promise<unknown>;
  cancel(sessionId: string): Promise<unknown>;
  answer(sessionId: string, questionId: string, optionId: string): Promise<unknown>;
  input(sessionId: string, text: string): Promise<unknown>;
  resize(sessionId: string, cols: number, rows: number): Promise<unknown>;
  onEvent(handler: (event: unknown) => void): () => void;
}

declare global {
  interface Window {
    flotilla: FlotillaBridge;
  }
}

export type { Status };
