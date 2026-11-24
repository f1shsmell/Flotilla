import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { PermissionCard, optionLabel } from "../src/components/PermissionCard";
import type { Permission } from "../src/store";

// The card colours a choice by what it does, not by its name: an option named
// "allow-me" that refuses must not wear the grant button, and a grant named
// "approve" must.
function card(options: Permission["options"]): string {
  const permission: Permission = { id: "perm-1", title: "edit config.json", kind: "edit", options };
  return renderToStaticMarkup(<PermissionCard permission={permission} onAnswer={() => {}} />);
}

describe("the permission card reads the option kind", () => {
  it("marks the grant by kind, not by id", () => {
    const html = card([
      { id: "approve", kind: "allow_once", name: "" },
      { id: "allow-me", kind: "reject_once", name: "" },
    ]);

    expect(html).toContain('<button type="button" class="primary">Allow once</button>');
    expect(html).toContain('<button type="button" class="">Deny</button>');
  });

  it("leaves an unknown kind unmarked: it is never styled as a grant", () => {
    expect(card([{ id: "maybe", kind: "", name: "" }])).not.toContain("primary");
    expect(card([{ id: "allow-ish", kind: "someday_maybe", name: "" }])).not.toContain("primary");
  });
});

describe("the permission card speaks a person's language", () => {
  it("uses the agent's own label when it gave one", () => {
    // The agent knows what this tool call is; its words beat any mapping.
    expect(card([{ id: "o1", kind: "allow_once", name: "Yes, and remember it" }])).toContain(
      "Yes, and remember it",
    );
  });

  it("never shows a protocol id when the kind has a plain word", () => {
    const html = card([
      { id: "allow_once", kind: "allow_once", name: "" },
      { id: "reject_once", kind: "reject_once", name: "" },
    ]);
    expect(html).not.toContain(">allow_once<");
    expect(html).not.toContain(">reject_once<");
    expect(html).toContain("Allow once");
    expect(html).toContain("Deny");
  });

  it("falls back to the id only when nothing better exists", () => {
    // An unknown kind with no label: the id is all there is, and showing
    // nothing would leave a button with no words.
    expect(optionLabel({ id: "custom-choice", kind: "someday_maybe", name: "" })).toBe(
      "custom-choice",
    );
  });

  it("prefers a label even over a known kind", () => {
    expect(optionLabel({ id: "x", kind: "allow_always", name: "Always allow this tool" })).toBe(
      "Always allow this tool",
    );
  });
});
