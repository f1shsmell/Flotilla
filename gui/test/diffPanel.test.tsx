import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { CheckpointStrip, diffEmptyLabel } from "../src/components/DiffPanel";
import type { Checkpoint, DiffFile } from "../src/store";

const listed: Checkpoint[] = [
  { turn: 1, commit: "1111111aaaaa", at: "2026-01-01T10:00:00Z" },
  { turn: 2, commit: "2222222bbbbb", at: "2026-01-01T10:05:00Z" },
];

function strip(
  checkpoints: Checkpoint[],
  confirming: number | null = null,
  busy = false,
): string {
  return renderToStaticMarkup(
    <CheckpointStrip
      checkpoints={checkpoints}
      confirming={confirming}
      busy={busy}
      onAsk={() => {}}
      onCancel={() => {}}
      onRestore={() => {}}
    />,
  );
}

describe("the checkpoint strip", () => {
  it("lists the finished turns newest first with their commits", () => {
    const html = strip(listed);
    expect(html).toContain("Checkpoints");
    expect(html.indexOf("Turn 2")).toBeGreaterThan(-1);
    expect(html.indexOf("Turn 2")).toBeLessThan(html.indexOf("Turn 1"));
    expect(html).toContain("2222222");
  });

  it("asks before a rollback and names what it discards", () => {
    const ask = strip(listed);
    expect(ask).toContain('aria-label="Restore turn 1"');
    expect(ask).not.toContain("discard");

    const confirming = strip(listed, 1);
    expect(confirming).toContain("Restore and discard changes");
    expect(confirming).toContain(">Cancel<");
    // Only the turn being confirmed asks; the other keeps its plain button, so a
    // second rollback cannot be armed behind the first.
    expect(confirming).toContain('aria-label="Restore turn 2"');
    expect(confirming).not.toContain('aria-label="Restore turn 1"');
  });

  it("disables every button while a rollback runs", () => {
    const busy = strip(listed, 1, true);
    expect(busy).toMatch(/disabled=""[^>]*>Restore and discard changes/);
    expect(busy).toMatch(/disabled=""[^>]*>Cancel</);
  });

  it("draws nothing for a session with no snapshots", () => {
    expect(strip([])).toBe("");
  });
});

describe("the diff panel's empty label", () => {
  const one: DiffFile = { path: "a.go", status: "M", added: 1, removed: 0 };

  it("keeps the reading label only while the answer is pending", () => {
    expect(diffEmptyLabel(false, null)).toContain("Reading the diff");
  });

  it("stops reading when the answer was a refusal", () => {
    // A failed read leaves no files, exactly like a pending one; without the
    // flag the panel would keep claiming an answer is on its way forever.
    expect(diffEmptyLabel(true, null)).not.toContain("Reading the diff");
    expect(diffEmptyLabel(true, null)).toContain("could not be read");
    expect(diffEmptyLabel(true, [])).toContain("could not be read");
  });

  it("distinguishes an empty answer from one with files", () => {
    expect(diffEmptyLabel(false, [])).toBe("Nothing changed here.");
    expect(diffEmptyLabel(false, [one])).toContain("Pick a file");
  });
});
