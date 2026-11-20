import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { NewSessionForm } from "../src/components/NewSessionForm";

function form(embedded?: boolean): string {
  return renderToStaticMarkup(
    <NewSessionForm agents={[]} loaded onStart={async () => {}} embedded={embedded} />,
  );
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