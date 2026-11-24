import { contextBridge, ipcRenderer } from "electron";

// The renderer gets exactly these calls and nothing else: no Node, no pipes, no
// token. Everything crosses through the main process, which owns the connection.
contextBridge.exposeInMainWorld("flotilla", {
  // A smoke run needs to read the terminal's buffer, which xterm keeps out of
  // the DOM. Nothing else sets this.
  smoke: process.env.FLT_GUI_SMOKE === "1",
  fleet: () => ipcRenderer.invoke("flotilla:fleet"),
  agents: () => ipcRenderer.invoke("flotilla:agents"),
  setAgent: (request: unknown) => ipcRenderer.invoke("flotilla:agent:set", request),
  start: (request: unknown) => ipcRenderer.invoke("flotilla:start", request),
  stop: (sessionId: string) => ipcRenderer.invoke("flotilla:stop", sessionId),
  detach: (sessionId: string) => ipcRenderer.invoke("flotilla:detach", sessionId),
  open: (sessionId: string) => ipcRenderer.invoke("flotilla:open", sessionId),
  // archive reads a session that is over. It is a different call from open on
  // purpose: open attaches a live session, and this one cannot.
  archive: (sessionId: string) => ipcRenderer.invoke("flotilla:archive", sessionId),
  prompt: (sessionId: string, text: string) =>
    ipcRenderer.invoke("flotilla:prompt", sessionId, text),
  seen: (sessionId: string) => ipcRenderer.invoke("flotilla:seen", sessionId),
  cancel: (sessionId: string) => ipcRenderer.invoke("flotilla:cancel", sessionId),
  surfaceSet: (sessionId: string, id: string, value: string) =>
    ipcRenderer.invoke("flotilla:surface:set", sessionId, id, value),
  answer: (sessionId: string, questionId: string, optionId: string) =>
    ipcRenderer.invoke("flotilla:answer", sessionId, questionId, optionId),
  input: (sessionId: string, text: string) =>
    ipcRenderer.invoke("flotilla:input", sessionId, text),
  resize: (sessionId: string, cols: number, rows: number) =>
    ipcRenderer.invoke("flotilla:resize", sessionId, cols, rows),
  diff: (sessionId: string, scope: string, path?: string) =>
    ipcRenderer.invoke("flotilla:diff", sessionId, scope, path),
  commit: (sessionId: string, message: string) =>
    ipcRenderer.invoke("flotilla:commit", sessionId, message),
  push: (sessionId: string) => ipcRenderer.invoke("flotilla:push", sessionId),
  pr: (sessionId: string, request: unknown) =>
    ipcRenderer.invoke("flotilla:pr", sessionId, request),
  comments: (sessionId: string) => ipcRenderer.invoke("flotilla:comments", sessionId),
  addComment: (sessionId: string, comment: unknown) =>
    ipcRenderer.invoke("flotilla:comment:add", sessionId, comment),
  removeComment: (sessionId: string, id: string) =>
    ipcRenderer.invoke("flotilla:comment:remove", sessionId, id),
  layout: () => ipcRenderer.invoke("flotilla:layout:read"),
  saveLayout: (patch: unknown) => ipcRenderer.invoke("flotilla:layout:write", patch),
  onEvent: (handler: (event: unknown) => void) => {
    const listener = (_event: unknown, payload: unknown) => handler(payload);
    ipcRenderer.on("flotilla:event", listener);
    return () => ipcRenderer.removeListener("flotilla:event", listener);
  },
});
