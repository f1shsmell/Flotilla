import { describe, expect, it } from "vitest";

import { rejectEchoNeedle } from "../electron/smoke";

describe("rejectEchoNeedle", () => {
  it("rejects a needle the prompt contains", () => {
    // The log renders the user's own prompt as an echo before the agent says
    // anything, so a needle inside the prompt passes without a reply.
    expect(() =>
      rejectEchoNeedle("Reply with exactly: flotilla-gui-codex-ok", "flotilla-gui-codex-ok"),
    ).toThrow(/echo/);
  });

  it("rejects a needle equal to the prompt", () => {
    expect(() => rejectEchoNeedle("hello", "hello")).toThrow(/echo/);
  });

  it("rejects a terminal needle the prompt contains", () => {
    // A PTY echoes typed input onto the screen, so the terminal check needs
    // the same guard as the log check.
    expect(() => rejectEchoNeedle("echo flotilla-pty-ok", "flotilla-pty-ok")).toThrow(/echo/);
  });

  it("accepts a needle the prompt does not contain", () => {
    expect(() =>
      rejectEchoNeedle("What is 17 plus 25? Reply with only the number.", "42"),
    ).not.toThrow();
  });

  it("accepts an empty needle", () => {
    expect(() => rejectEchoNeedle("anything", "")).not.toThrow();
  });
});