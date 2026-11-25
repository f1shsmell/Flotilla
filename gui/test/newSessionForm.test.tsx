import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { NewSessionForm } from "../src/components/NewSessionForm";
import type { RegistryEntry } from "../src/store";

function form(embedded?: boolean, registry: RegistryEntry[] = []): string {
  return renderToStaticMarkup(
    <NewSessionForm
      agents={[]}
      registry={registry}
      loaded
      onStart={async () => {}}
      embedded={embedded}
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

describe("the form's overline", () => {
  it("names the form when it stands alone", () => {
    expect(form()).toContain("New session");
  });

  it("stays quiet when a greeting above the card already names it", () => {
    const html = form(true);
    expect(html).not.toContain("New session");
    // The error paragraph is a sibling of the overline, not part of it: hiding
    // the label must never hide a failed start's reason.
    expect(html).toContain('type="submit"');
  });
});

describe("the form's published directory", () => {
  it("shows what the directory has to install, without offering a start", () => {
    const html = form(false, directory());

    expect(html).toContain("ACP Registry");
    const start = html.indexOf('<ul class="agent-list registry-list"');
    expect(start).toBeGreaterThan(-1);
    const list = html.slice(start, html.indexOf("</ul>", start));
    expect(list).toContain("Kimi Code");
    expect(list).toContain("uvx kimi-code@1.2.3");
    // The directory is a catalogue, not an install: a button inside the list
    // would start a command this machine does not have.
    expect(list).not.toContain("<button");
  });

  it("says nothing when the directory is empty or missing", () => {
    const html = form(false, []);

    expect(html).not.toContain("ACP Registry");
  });
});
