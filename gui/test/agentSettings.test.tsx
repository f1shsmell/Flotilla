import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { AgentSettings } from "../src/components/AgentSettings";
import type { AgentEntry, RegistryEntry } from "../src/store";

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

function settings(agents: AgentEntry[], registry = directory()): string {
  return renderToStaticMarkup(
    <AgentSettings
      agents={agents}
      registry={registry}
      problems={[]}
      onToggle={async () => {}}
      onRefresh={() => {}}
      onClose={() => {}}
    />,
  );
}

function directory(): RegistryEntry[] {
  return [
    {
      id: "kimi",
      name: "Kimi Code",
      version: "1.2.3",
      description: "A Kimi agent",
      repository: "https://example.com/kimi",
      license: "MIT",
      recipe: "uvx kimi-code@1.2.3",
    },
  ];
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

  it("lists the published directory without switches to flip", () => {
    const html = settings([entry({ tier: "available" })]);

    expect(html).toContain("ACP Registry");
    expect(html).toContain("Kimi Code");
    expect(html).toContain("uvx kimi-code@1.2.3");
    // A directory entry has no stored switches, so it has no row in the table:
    // a toggle here would write a switch nothing reads.
    expect(html).not.toContain("enable Kimi Code");
  });

  it("keeps the directory out of the page when there is none", () => {
    const html = settings([entry({ tier: "available" })], []);

    expect(html).not.toContain("ACP Registry");
  });
});
