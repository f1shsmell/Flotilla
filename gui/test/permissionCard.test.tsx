import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { PermissionCard } from "../src/components/PermissionCard";
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
      { id: "approve", kind: "allow_once" },
      { id: "allow-me", kind: "reject_once" },
    ]);

    expect(html).toContain('<button type="button" class="primary">approve</button>');
    expect(html).toContain('<button type="button" class="">allow-me</button>');
  });

  it("leaves an unknown kind unmarked: it is never styled as a grant", () => {
    expect(card([{ id: "maybe", kind: "" }])).not.toContain("primary");
    expect(card([{ id: "allow-ish", kind: "someday_maybe" }])).not.toContain("primary");
  });
});