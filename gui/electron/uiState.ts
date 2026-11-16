// The window remembers how it was laid out between launches: which session was
// on screen, which panel was showing, and how big the window was. This module
// is the file half of that, kept out of main.ts so it can be tested without an
// Electron process.

import fs from "node:fs";
import path from "node:path";

export interface UiState {
  // null clears a remembered session; undefined leaves what is stored alone.
  session?: string | null;
  view?: "stream" | "diff";
  window?: { width: number; height: number };
}

export function uiStatePath(userData: string): string {
  return path.join(userData, "ui-state.json");
}

// readUiState treats every failure as a first launch. A layout file that cannot
// be parsed is not worth an error dialog: the window opens on its defaults, and
// the next write replaces the file.
export function readUiState(userData: string): UiState {
  try {
    const raw = fs.readFileSync(uiStatePath(userData), "utf8");
    const parsed = JSON.parse(raw) as unknown;
    if (parsed == null || typeof parsed !== "object") {
      return {};
    }
    const value = parsed as Record<string, unknown>;

    const out: UiState = {};
    if (typeof value.session === "string" && value.session !== "") {
      out.session = value.session;
    }
    if (value.view === "stream" || value.view === "diff") {
      out.view = value.view;
    }
    const bounds = value.window as Record<string, unknown> | undefined;
    if (
      bounds != null &&
      typeof bounds.width === "number" &&
      typeof bounds.height === "number" &&
      bounds.width > 0 &&
      bounds.height > 0
    ) {
      out.window = { width: bounds.width, height: bounds.height };
    }
    return out;
  } catch {
    return {};
  }
}

// saveUiState merges a patch into what is on disk. Layout is written from a few
// independent places (a session click, a panel switch, a window close), and each
// must not erase what the others stored.
//
// The write is synchronous on purpose: the process may exit right after a window
// close, and an async write would race that exit. The payload is a few hundred
// bytes.
export function saveUiState(userData: string, patch: UiState): void {
  const next: UiState = { ...readUiState(userData), ...patch };
  // A null session is a request to forget one, not a value to store: writing it
  // through would restore a session named "null".
  if (patch.session === null) {
    delete next.session;
  }
  try {
    fs.mkdirSync(userData, { recursive: true });
    fs.writeFileSync(uiStatePath(userData), JSON.stringify(next, null, 2) + "\n");
  } catch {
    // A window that cannot remember its layout still works; reporting this on
    // every click would be noise, and there is nothing the user could do.
  }
}