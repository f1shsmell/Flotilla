// The renderer's interfaces are hand-written, and this is what keeps them
// honest. Go is the contract: cmd/flt-contract renders its wire types into
// contract.ts, and every interface the renderer reads is compared against that
// file's field names here. A rename on either side fails in this test instead of
// rendering undefined at run time.

import fs from "node:fs";
import path from "node:path";

import { describe, expect, it } from "vitest";

import { wireTypes } from "../electron/contract";

const storeSource = fs.readFileSync(path.join(__dirname, "..", "src", "store.ts"), "utf8");

// Interfaces the renderer reads off the wire, and the Go type each one mirrors.
// A renderer interface that is not in this list is a view model the daemon never
// sends, so it has no Go counterpart to compare with.
//
// The renamed field is the one exception: Go calls the lifecycle `state`, and the
// renderer calls it `lifecycle` so a `session.state` notification and a status
// entry cannot be confused for each other. The mapping is named here rather than
// hidden in a normalization step.
const wired: { go: string; ts: string; renamed?: Record<string, string> }[] = [
  { go: "daemon.Status", ts: "Status" },
  { go: "daemon.StatusEntry", ts: "StatusEntry", renamed: { state: "lifecycle" } },
  { go: "daemon.StartRequest", ts: "StartRequest" },
  { go: "daemon.SetAgentRequest", ts: "SetAgentRequest" },
  { go: "agent.Entry", ts: "AgentEntry" },
  { go: "agent.Listing", ts: "AgentListing" },
  { go: "agent.Change", ts: "AgentChange" },
  { go: "diff.File", ts: "DiffFile" },
  { go: "comments.Comment", ts: "Comment" },
  { go: "session.Control", ts: "Control" },
  { go: "session.Choice", ts: "Choice" },
  { go: "session.PermissionQuestion", ts: "Permission" },
  { go: "session.PermissionOption", ts: "PermissionOption" },
];

// interfaceFields reads one interface body. Every field has to be written as
// `name: type` or `name?: type`, which is the style the file already uses.
function interfaceFields(source: string, name: string): string[] {
  const header = `export interface ${name} {`;
  const start = source.indexOf(header);
  if (start < 0) {
    throw new Error(`store.ts has no interface ${name}`);
  }

  const body = source.slice(start + header.length);
  const end = body.indexOf("\n}");
  if (end < 0) {
    throw new Error(`interface ${name} is never closed`);
  }

  const fields: string[] = [];
  for (const raw of body.slice(0, end).split("\n")) {
    const line = raw.trim();
    if (line === "" || line.startsWith("//")) {
      continue;
    }
    const match = /^([A-Za-z_][A-Za-z0-9_]*)\??\s*:/.exec(line);
    if (match === null) {
      throw new Error(`interface ${name} has a line this gate cannot read: ${line}`);
    }
    fields.push(match[1]);
  }
  return fields.sort();
}

describe("the renderer interfaces match the wire", () => {
  for (const { go, ts, renamed } of wired) {
    it(`${ts} matches ${go}`, () => {
      const shape = (wireTypes as Record<string, Record<string, unknown>>)[go];
      expect(shape, `${go} is missing from contract.ts`).toBeDefined();

      const want = Object.keys(shape)
        .map((field) => renamed?.[field] ?? field)
        .sort();
      expect(interfaceFields(storeSource, ts)).toEqual(want);
    });
  }
});
