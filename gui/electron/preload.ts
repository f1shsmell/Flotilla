import { contextBridge, ipcRenderer } from "electron";

// The renderer gets exactly these calls and nothing else: no Node, no pipes, no
// token. Everything crosses through the main process, which owns the connection.
contextBridge.exposeInMainWorld("flotilla", {
  fleet: () => ipcRenderer.invoke("flotilla:fleet"),
  open: (sessionId: string) => ipcRenderer.invoke("flotilla:open", sessionId),
  prompt: (sessionId: string, text: string) =>
    ipcRenderer.invoke("flotilla:prompt", sessionId, text),
  cancel: (sessionId: string) => ipcRenderer.invoke("flotilla:cancel", sessionId),
  answer: (sessionId: string, questionId: string, optionId: string) =>
    ipcRenderer.invoke("flotilla:answer", sessionId, questionId, optionId),
  input: (sessionId: string, text: string) =>
    ipcRenderer.invoke("flotilla:input", sessionId, text),
  onEvent: (handler: (event: unknown) => void) => {
    const listener = (_event: unknown, payload: unknown) => handler(payload);
    ipcRenderer.on("flotilla:event", listener);
    return () => ipcRenderer.removeListener("flotilla:event", listener);
  },
});
