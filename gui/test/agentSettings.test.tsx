import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { AgentSettings } from "../src/components/AgentSettings";
import type { AgentEntry } from "../src/store";

function entry(over: Partial<AgentEntry>): AgentEntry {
  return {
    id: "claude",
    name: "Claude Code",
    command: "claude-agent-acp",
    args: [],
    mode: "acp",
    needs: "",
    description: "",
    source: "builtin",
    installed: false,
    path: "",
    enabled: false,
    chat: true,
    cli: false,
    tier: "",
    ...over,
  };
}

function settings(agents: AgentEntry[]): string {
  return renderToStaticMarkup(
    <AgentSettings
      agents={agents}
      problems={[]}
      onToggle={async () => {}}
      onRefresh={() => {}}
      onClose={() => {}}
    />,
  );
}

describe("the agent settings page", () => {
  it("speaks the daemon's tier instead of calling a half-install not found", () => {
    const html = settings([entry({ tier: "installed-but-not-acp" })]);

    expect(html).toContain("installed-but-not-acp");
    expect(html).not.toContain("not found");
    expect(html).toContain("0 available");
  });

  it("falls back to the install flag for a daemon that predates tiers", () => {
    const html = settings([entry({ installed: true })]);

    expect(html).toContain("installed");
    expect(html).toContain("1 available");
  });
});
