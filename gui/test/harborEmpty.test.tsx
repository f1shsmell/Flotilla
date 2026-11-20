import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { HarborEmpty } from "../src/components/HarborEmpty";

describe("HarborEmpty", () => {
  it("greets an empty harbor with the lockup and an invitation to start", () => {
    const html = renderToStaticMarkup(<HarborEmpty mode="quiet" />);
    expect(html).toMatch(/class="harbor-empty"/);
    expect(html).toMatch(/class="brand-lockup"/);
    expect(html).toContain("The harbor is quiet.");
    expect(html).toContain("Send your first agent to sea");
  });

  it("asks the user to pick when sessions exist", () => {
    const html = renderToStaticMarkup(<HarborEmpty mode="pick" />);
    expect(html).toContain("Pick a session to watch it work.");
    expect(html).toContain("Its log, terminal and diff will appear here.");
  });

  it("names the composer view and carries the card the caller passes", () => {
    const html = renderToStaticMarkup(
      <HarborEmpty mode="compose">
        <div className="composer-card">card-body</div>
      </HarborEmpty>,
    );
    expect(html).toContain("New session");
    expect(html).toContain("card-body");
  });

  it("shows no card slot content when the caller passes none", () => {
    const html = renderToStaticMarkup(<HarborEmpty mode="pick" />);
    expect(html).not.toContain("composer-card");
  });
});