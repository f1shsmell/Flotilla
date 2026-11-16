// The layout file is the window's memory: which session was on screen, which
// panel was showing, and how big the window was. It must survive a partial
// write from an older version, and it must never resurrect a session id that was
// deliberately cleared.

import fs from "node:fs";
import os from "node:os";
import path from "node:path";

import { describe, expect, it } from "vitest";

import { readUiState, saveUiState, uiStatePath } from "../electron/uiState";

function fresh(): string {
  return fs.mkdtempSync(path.join(os.tmpdir(), "flt-ui-state-"));
}

describe("uiState", () => {
  it("treats a missing file as a first launch", () => {
    expect(readUiState(fresh())).toEqual({});
  });

  it("round-trips every field it stores", () => {
    const dir = fresh();
    saveUiState(dir, { session: "sess-1", view: "diff", window: { width: 900, height: 700 } });

    expect(readUiState(dir)).toEqual({
      session: "sess-1",
      view: "diff",
      window: { width: 900, height: 700 },
    });
  });

  it("merges a patch instead of replacing the file", () => {
    const dir = fresh();
    saveUiState(dir, { session: "sess-1", view: "diff" });
    saveUiState(dir, { window: { width: 800, height: 600 } });

    expect(readUiState(dir)).toEqual({
      session: "sess-1",
      view: "diff",
      window: { width: 800, height: 600 },
    });
  });

  it("forgets a session when the patch says null", () => {
    const dir = fresh();
    saveUiState(dir, { session: "sess-1", view: "stream" });
    saveUiState(dir, { session: null });

    const stored = readUiState(dir);
    expect(stored.session).toBeUndefined();
    // Clearing the session must not clear the rest of the layout.
    expect(stored.view).toBe("stream");
  });

  it("drops fields an older or newer version wrote", () => {
    const dir = fresh();
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(
      uiStatePath(dir),
      JSON.stringify({ session: 42, view: "sideways", window: { width: "wide" } }),
    );

    expect(readUiState(dir)).toEqual({});
  });

  it("survives a file that is not JSON at all", () => {
    const dir = fresh();
    fs.writeFileSync(uiStatePath(dir), "{ this is not json");

    expect(readUiState(dir)).toEqual({});
  });

  it("ignores a window with a non-positive size", () => {
    const dir = fresh();
    fs.writeFileSync(uiStatePath(dir), JSON.stringify({ window: { width: 0, height: 700 } }));

    expect(readUiState(dir).window).toBeUndefined();
  });
});