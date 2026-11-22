// The smoke driver: it renders the real window offscreen and drives the real UI,
// so the main process, the preload bridge, the wire and the daemon are all in
// the loop. It never runs unless FLT_GUI_SMOKE is set.

import type { BrowserWindow } from "electron";

const step = (ms = 200) => new Promise((resolve) => setTimeout(resolve, ms));

async function body(target: BrowserWindow): Promise<string> {
  return target.webContents.executeJavaScript("document.body.innerText");
}

// rejectEchoNeedle guards the reply check: the log renders the user's own
// prompt as an echo before the agent says anything, so a needle the prompt
// already contains would pass the run without the agent ever answering.
export function rejectEchoNeedle(prompt: string, expect: string): void {
  if (expect !== "" && prompt.includes(expect)) {
    throw new Error(
      `the expected reply ${JSON.stringify(expect)} is part of the prompt; ` +
        "the user echo would satisfy it — choose a needle the prompt does not contain",
    );
  }
}

async function waitFor(target: BrowserWindow, needle: string, deadline: number): Promise<string> {
  let text = "";
  while (Date.now() < deadline) {
    text = await body(target);
    if (text.includes(needle)) {
      return text;
    }
    await step();
  }
  // Visibility is part of the failure: a hidden page stops painting frames,
  // which is the first thing to rule out when a log looks frozen.
  const visibility = (await target.webContents
    .executeJavaScript("document.visibilityState")
    .catch(() => "unknown")) as string;
  throw new Error(`${needle} never appeared (visibility: ${visibility}); body was:\n${text}`);
}

async function clickButton(target: BrowserWindow, text: string): Promise<void> {
  await target.webContents.executeJavaScript(`(() => {
    const buttons = [...document.querySelectorAll("button")];
    const button = buttons.find((candidate) => candidate.innerText.trim() === ${JSON.stringify(text)})
      ?? buttons.find((candidate) => candidate.innerText.includes(${JSON.stringify(text)}));
    if (!button) { throw new Error("no button " + ${JSON.stringify(text)}); }
    button.click();
  })()`);
}

async function clickSession(target: BrowserWindow, id: string): Promise<void> {
  await target.webContents.executeJavaScript(`(() => {
    const row = [...document.querySelectorAll(".session-row")]
      .find((button) => button.innerText.includes(${JSON.stringify(id)}));
    if (!row) { throw new Error("no row for " + ${JSON.stringify(id)}); }
    row.click();
  })()`);
}

async function sendPrompt(target: BrowserWindow, text: string): Promise<void> {
  // A controlled textarea ignores a plain value assignment: React only sees the
  // native setter followed by an input event.
  await target.webContents.executeJavaScript(`(() => {
    const area = document.querySelector("textarea");
    if (!area) { throw new Error("no composer"); }
    const setter = Object.getOwnPropertyDescriptor(
      HTMLTextAreaElement.prototype, "value").set;
    setter.call(area, ${JSON.stringify(text)});
    area.dispatchEvent(new Event("input", { bubbles: true }));
  })()`);

  await target.webContents.executeJavaScript(`(() => {
    const send = [...document.querySelectorAll("button")]
      .find((button) => button.textContent.trim() === "Send");
    if (!send) { throw new Error("no send button"); }
    send.click();
  })()`);
}

// terminalText reads xterm's buffer, which never reaches the DOM.
const terminalText = `(() => {
  const term = window.__fltTerminal;
  if (!term) { return ""; }
  const buffer = term.buffer.active;
  let text = "";
  for (let i = 0; i < buffer.length; i += 1) {
    const line = buffer.getLine(i);
    if (line) { text += line.translateToString(true) + "\\n"; }
  }
  return text;
})()`;

async function finish(
  target: BrowserWindow,
  expect: string,
  expectTerminal: string,
  deadline: number,
): Promise<boolean> {
  if (expectTerminal !== "") {
    const screen = await waitForTerminal(target, expectTerminal, deadline);
    console.log(`smoke: ok (terminal: ${expectTerminal})`);
    console.log(screen);
    return true;
  }

  const text = await waitFor(target, expect, deadline);
  console.log(`smoke: ok (${expect || "rendered"})`);
  console.log(text);
  return true;
}

// leaveNote comments on the last added line, the way a reviewer would: click the
// line number, type, submit.
async function leaveNote(target: BrowserWindow, text: string): Promise<void> {
  await target.webContents.executeJavaScript(`(() => {
    const numbers = [...document.querySelectorAll(".patch-line.add .line-no")];
    const target = numbers[numbers.length - 1];
    if (!target) { throw new Error("no added line to comment on"); }
    target.click();
  })()`);

  await target.webContents.executeJavaScript(`(() => {
    const input = document.querySelector(".note-compose input");
    if (!input) { throw new Error("no note composer"); }
    const setter = Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype, "value").set;
    setter.call(input, ${JSON.stringify(text)});
    input.dispatchEvent(new Event("input", { bubbles: true }));
    document.querySelector(".note-compose").requestSubmit();
  })()`);
}

async function waitForAddedLine(
  target: BrowserWindow,
  needle: string,
  deadline: number,
): Promise<string> {
  let text = "";
  while (Date.now() < deadline) {
    text = (await target.webContents.executeJavaScript(`(() => {
      const added = [...document.querySelectorAll(".diff-patch .add")]
        .map((line) => line.textContent ?? "");
      return added.some((line) => line.includes(${JSON.stringify(needle)}))
        ? added.join(" | ")
        : "";
    })()`)) as string;
    if (text !== "") {
      return text;
    }
    await step();
  }
  throw new Error(`${needle} never appeared as an added line; body was://n${await body(target)}`);
}

async function waitForTerminal(
  target: BrowserWindow,
  needle: string,
  deadline: number,
): Promise<string> {
  let text = "";
  while (Date.now() < deadline) {
    text = (await target.webContents.executeJavaScript(terminalText)) as string;
    if (text.includes(needle)) {
      return text;
    }
    await step();
  }
  throw new Error(`${needle} never reached the terminal; screen was:\n${text}`);
}

// waitForCondition polls one expression until it is truthy. Class changes are
// what the settings page uses to show a switch that has been stored, and they
// never reach innerText, so the body text waits above cannot see them.
async function waitForCondition(
  target: BrowserWindow,
  expression: string,
  what: string,
  deadline: number,
): Promise<void> {
  let last = "";
  while (Date.now() < deadline) {
    last = (await target.webContents.executeJavaScript(
      `(() => { try { return (${expression}) ? "yes" : ""; } catch (cause) { return "error: " + cause.message; } })()`,
    )) as string;
    if (last === "yes") {
      return;
    }
    await step();
  }
  throw new Error(`${what} never happened (last: ${last}); body was:\n${await body(target)}`);
}

// switchFor is the enabled checkbox of one agent on the settings page.
function switchFor(name: string): string {
  return `document.querySelector('input[aria-label="enable ${name}"]')`;
}

export async function run(target: BrowserWindow): Promise<boolean> {
  const expect = process.env.FLT_GUI_SMOKE_EXPECT ?? "";
  const expectTerminal = process.env.FLT_GUI_SMOKE_TERMINAL ?? "";
  const open = process.env.FLT_GUI_SMOKE_OPEN ?? "";
  // FLT_GUI_SMOKE_HEAL kills the daemon under the open session and waits for
  // the supervisor to respawn it and reattach: the whole self-heal chain.
  const heal = process.env.FLT_GUI_SMOKE_HEAL ?? "";
  const prompt = process.env.FLT_GUI_SMOKE_PROMPT ?? "";
  const compose = process.env.FLT_GUI_SMOKE_COMPOSE ?? "";
  const expand = process.env.FLT_GUI_SMOKE_EXPAND ?? "";
  // FLT_GUI_SMOKE_SET_CONTROL writes one setting as "Name=value" and waits for
  // the agent's own update to land it back in the select: the round trip a
  // live agent's composer bar performs, not a local DOM edit.
  const setControl = process.env.FLT_GUI_SMOKE_SET_CONTROL ?? "";
  const startRequest = process.env.FLT_GUI_SMOKE_START ?? "";
  // FLT_GUI_SMOKE_STARTS is a JSON array of start requests: a fleet of several
  // sessions for the walks (and screenshots) that need more than one card.
  const startRequests = process.env.FLT_GUI_SMOKE_STARTS ?? "";
  const form = process.env.FLT_GUI_SMOKE_FORM ?? "";
  const diffFile = process.env.FLT_GUI_SMOKE_DIFF ?? "";
  const diffLine = process.env.FLT_GUI_SMOKE_PATCH ?? "";
  const note = process.env.FLT_GUI_SMOKE_NOTE ?? "";
  // FLT_GUI_SMOKE_AGENTS names an agent whose Enabled switch the run flips on
  // the settings page, then flips back. It is the settings page's walk.
  const agentsAgent = process.env.FLT_GUI_SMOKE_AGENTS ?? "";
  // FLT_GUI_SMOKE_IME commits this text through composition events on xterm's
  // textarea: the IME path a Chinese user's keystrokes take, ending in the same
  // onData the PTY hears. FLT_GUI_SMOKE_RENDERER names the renderer that must
  // be live ("webgl" or "dom").
  const ime = process.env.FLT_GUI_SMOKE_IME ?? "";
  const renderer = process.env.FLT_GUI_SMOKE_RENDERER ?? "";
  // A real agent's turn can outlast the default budget (codex takes ~2 minutes
  // on a cold transport), so a run may name its own deadline.
  const budget = Number(process.env.FLT_GUI_SMOKE_DEADLINE_MS);
  const deadline = Date.now() + (Number.isFinite(budget) && budget > 0 ? budget : 120000);

  target.webContents.on("render-process-gone", (_event, details) => {
    console.error(`smoke: the renderer is gone (${details.reason}, ${details.exitCode})`);
  });
  target.webContents.on("console-message", (...args: unknown[]) => {
    console.error("smoke: renderer says", ...args.slice(1, 4));
  });

  try {
    rejectEchoNeedle(prompt, expect);
    // A terminal echoes typed input too, so the same guard covers its needle.
    rejectEchoNeedle(prompt, expectTerminal);

    // FLT_GUI_SMOKE_CLOSE walks the close button's meaning: the window must
    // hide (the tray owns the way back) while the app keeps living. It runs
    // alone because a hidden window can no longer serve the other walks. A
    // user closes a window they can see, so the walk waits for first paint:
    // closing before ready-to-show would race the show that follows it.
    if (process.env.FLT_GUI_SMOKE_CLOSE) {
      while (!target.isVisible() && Date.now() < deadline) {
        await step();
      }
      target.close();
      await step(500);
      if (target.isDestroyed()) {
        throw new Error("closing the window destroyed it; close must hide to the tray");
      }
      if (target.isVisible()) {
        throw new Error("closing the window left it visible; close must hide to the tray");
      }
      console.log("smoke: ok (close hides to the tray, the app stays alive)");
      return true;
    }
    if (ime !== "" && expectTerminal === "") {
      throw new Error(
        "the IME walk needs FLT_GUI_SMOKE_TERMINAL: without a needle the empty wait passes whether or not the committed text reached the screen",
      );
    }

    if (form !== "") {
      // The composer card is the first view's action point: with sessions on
      // screen it opens from the fleet header, so the form path asks for it
      // before waiting on any of its fields.
      await target.webContents.executeJavaScript(`(() => {
        if (document.querySelector(".new-session")) { return; }
        const open = [...document.querySelectorAll(".fleet header button")]
          .find((button) => button.textContent.trim() === "New session");
        if (!open) { throw new Error("no New session button in the fleet header"); }
        open.click();
      })()`);
      await step();
      await waitFor(target, "Start", deadline);

      // The picker replaced the two free-text command fields with a list, so a
      // form run names an agent (or asks for the custom form) instead of typing
      // into positional inputs. The mode is set first: the list it offers
      // depends on it.
      await target.webContents.executeJavaScript(`(() => {
        const fields = ${form};
        if (!fields.mode) { return; }
        const select = document.querySelector(".agent-picker select");
        if (!select) { return; }
        const setter = Object.getOwnPropertyDescriptor(
          HTMLSelectElement.prototype, "value").set;
        setter.call(select, fields.mode);
        select.dispatchEvent(new Event("change", { bubbles: true }));
      })()`);
      await step();

      const formMode = (await target.webContents.executeJavaScript(`(() => {
        const fields = ${form};
        if (fields.agent !== undefined) {
          // Match the name span exactly: a substring match would pick whichever
          // longer name happens to contain it first.
          const options = [...document.querySelectorAll(".agent-option")];
          const option = options.find(
            (button) => (button.querySelector("span")?.textContent ?? "").trim()
              === fields.agent);
          if (!option) {
            throw new Error("no agent option named " + fields.agent + "; options were: " +
              options.map((button) => button.innerText).join(", "));
          }
          option.click();
          return "agent";
        }
        const link = [...document.querySelectorAll(".new-session button")]
          .find((button) => button.textContent.includes("custom command"));
        if (!link) { throw new Error("no custom-command link"); }
        link.click();
        return "custom";
      })()`)) as string;
      await step();

      await target.webContents.executeJavaScript(`(() => {
        const fields = ${form};
        const setValue = (element, value) => {
          const setter = Object.getOwnPropertyDescriptor(
            HTMLInputElement.prototype, "value").set;
          setter.call(element, value);
          element.dispatchEvent(new Event("input", { bubbles: true }));
        };
        const inputs = [...document.querySelectorAll(".new-session input")];
        const set = (placeholder, value) => {
          if (value === undefined || value === null) { return; }
          const input = inputs.find(
            (element) => (element.placeholder ?? "").includes(placeholder));
          if (!input) { throw new Error("no input with placeholder " + placeholder); }
          setValue(input, value);
        };
        if (${JSON.stringify(formMode)} === "custom") {
          set("agent command", fields.command);
          set("arguments", fields.args ?? "");
        }
        set("working directory", fields.dir ?? "");
        set("worktree of repository", fields.repo ?? "");
        set("new branch", fields.branch ?? "");
        const submit = document.querySelector(".new-session button[type=submit]");
        if (!submit || submit.disabled) {
          // requestSubmit() on a disabled button does nothing, so a form the
          // smoke meant to submit would otherwise wait out the whole deadline.
          throw new Error("the form's Start button is disabled");
        }
        document.querySelector(".new-session").requestSubmit();
      })()`);
      // The form's submit opens the new session, so the composer appears only
      // once the start and the attach have both come back.
      await waitFor(target, "Cancel turn", deadline);
      console.log("smoke: submitted the form");
    }

    if (startRequests !== "") {
      let requests: unknown[];
      try {
        requests = JSON.parse(startRequests) as unknown[];
      } catch (cause) {
        throw new Error(`FLT_GUI_SMOKE_STARTS is not JSON: ${String(cause)}`);
      }
      for (const request of requests) {
        const started = (await target.webContents.executeJavaScript(
          `window.flotilla.start(${JSON.stringify(request)})`,
        )) as { id?: string };
        if (!started?.id) {
          throw new Error(`start request returned no session id: ${JSON.stringify(request)}`);
        }
        console.log(`smoke: started ${started.id}`);
        await waitFor(target, started.id, deadline);
      }
    }

    if (startRequest !== "") {
      const started = (await target.webContents.executeJavaScript(
        `window.flotilla.start(${startRequest})`,
      )) as { id?: string };
      if (!started?.id) {
        throw new Error("the start request returned no session id");
      }
      console.log(`smoke: started ${started.id}`);
      await waitFor(target, started.id, deadline);
      if (open === "") {
        return await finish(target, started.id, expectTerminal, deadline);
      }
    }

    if (open !== "") {
      await waitFor(target, open, deadline);
      await clickSession(target, open);
      // "Cancel turn" alone can already be on screen from a restored session:
      // the header naming this id is what proves the click's own open landed.
      await waitForCondition(
        target,
        `document.querySelector(".session-header h2")?.textContent === ${JSON.stringify(open)}`,
        `the view to open ${open}`,
        deadline,
      );
    }

    if (renderer !== "") {
      await waitForCondition(
        target,
        `window.__fltRenderer === ${JSON.stringify(renderer)}`,
        `the ${renderer} renderer to activate`,
        deadline,
      );
      console.log(`smoke: ok (renderer: ${renderer})`);
    }

    if (ime !== "") {
      // xterm commits what the textarea holds between compositionstart and
      // compositionend, not the events' data, so the walk fills the value the
      // way a real IME does; the text then travels the ordinary onData path.
      await target.webContents.executeJavaScript(`(() => {
        const area = window.__fltTerminal?.textarea;
        if (!area) { throw new Error("no terminal textarea"); }
        if (area.value !== "") { throw new Error("the terminal textarea is not empty before the IME walk"); }
        area.focus();
        area.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true, data: "" }));
        const setter = Object.getOwnPropertyDescriptor(
          HTMLTextAreaElement.prototype, "value").set;
        setter.call(area, ${JSON.stringify(ime)});
        area.dispatchEvent(new CompositionEvent("compositionupdate", { bubbles: true, data: ${JSON.stringify(ime)} }));
        area.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true, data: ${JSON.stringify(ime)} }));
      })()`);
    }

    if (heal !== "") {
      if (open === "") {
        throw new Error("the heal walk needs FLT_GUI_SMOKE_OPEN to name the session it watches");
      }
      const before = (await target.webContents.executeJavaScript(
        `window.flotilla.fleet()`,
      )) as { pid?: number };
      if (!before?.pid) {
        throw new Error("the fleet reported no daemon pid to kill");
      }
      if (expectTerminal !== "") {
        // The screen the user was reading must exist before the kill: the
        // heal is only proven if that same screen survives it.
        await waitForTerminal(target, expectTerminal, deadline);
      }
      // The host keeps running through this: a daemon death is exactly the
      // restart the supervisor exists for.
      process.kill(before.pid);

      // The closed event must reach the view first: a heal that skipped the
      // lost state would make the recovery check below vacuous.
      await waitForCondition(
        target,
        `document.body.innerText.includes("· lost")`,
        "the view to report the lost link",
        deadline,
      );

      let healed = false;
      while (Date.now() < deadline) {
        const status = (await target.webContents
          .executeJavaScript(`window.flotilla.fleet()`)
          .catch(() => null)) as {
          pid?: number;
          sessions?: { id: string; reachable: boolean }[];
        } | null;
        if (
          status?.pid &&
          status.pid !== before.pid &&
          status.sessions?.some((entry) => entry.id === open && entry.reachable)
        ) {
          healed = true;
          break;
        }
        await step();
      }
      if (!healed) {
        throw new Error(`a new daemon never came back serving ${open}`);
      }
      await waitForCondition(
        target,
        `!document.body.innerText.includes("· lost")`,
        "the view to leave the lost state",
        deadline,
      );
      console.log("smoke: healed after the daemon died");
    }

    if (prompt !== "") {
      await sendPrompt(target, prompt);
    }

    if (expand !== "") {
      if (prompt === "") {
        throw new Error("the expand walk needs FLT_GUI_SMOKE_PROMPT to produce the fold");
      }
      // Open the first folded run so the shot proves the fold carries its calls:
      // the summary names them, the body reveals each one. The turn may still be
      // in flight when the prompt leg returns, so wait for the fold itself.
      await waitForCondition(
        target,
        `!!document.querySelector(".entry-run summary")`,
        "a folded tool run to appear in the log",
        deadline,
      );
      await target.webContents.executeJavaScript(`(() => {
        const summary = document.querySelector(".entry-run summary");
        if (!summary) { throw new Error("no folded tool run in the log"); }
        summary.click();
      })()`);
      await waitForCondition(
        target,
        `!!document.querySelector(".entry-run[open]")`,
        "the folded tool run to open",
        deadline,
      );
      await waitForCondition(
        target,
        `document.querySelectorAll(".entry-run[open] .run-body .entry").length >= 2`,
        "the opened fold to reveal its calls",
        deadline,
      );
      // A view switch unmounts the log; the fold the reader opened must survive
      // the trip to the diff and back.
      await target.webContents.executeJavaScript(`(() => {
        const diff = [...document.querySelectorAll(".session-actions button")]
          .find((button) => button.textContent === "Diff");
        if (!diff) { throw new Error("no Diff button"); }
        diff.click();
      })()`);
      await waitForCondition(
        target,
        `!!document.querySelector(".diff")`,
        "the diff view to render before returning to the log",
        deadline,
      );
      await target.webContents.executeJavaScript(`(() => {
        const log = [...document.querySelectorAll(".session-actions button")]
          .find((button) => button.textContent === "Log");
        if (!log) { throw new Error("no Log button"); }
        log.click();
      })()`);
      await waitForCondition(
        target,
        `!!document.querySelector(".entry-run[open]")`,
        "the fold to stay open across the view switch",
        deadline,
      );
      console.log("smoke: ok (expanded tool run)");
    }

    if (setControl !== "") {
      const eq = setControl.indexOf("=");
      if (eq <= 0 || eq === setControl.length - 1) {
        throw new Error('FLT_GUI_SMOKE_SET_CONTROL must read "Name=value"');
      }
      if (open === "") {
        throw new Error("the set-control walk needs FLT_GUI_SMOKE_OPEN to name the session");
      }
      const name = setControl.slice(0, eq);
      const value = setControl.slice(eq + 1);
      const selector = `.surface-controls select[aria-label=${JSON.stringify(name)}]`;
      await waitForCondition(
        target,
        `!!document.querySelector(${JSON.stringify(selector)})`,
        `the composer bar to offer ${name}`,
        deadline,
      );
      await target.webContents.executeJavaScript(`(() => {
        const select = document.querySelector(${JSON.stringify(selector)});
        if (!select) { throw new Error("no select named " + ${JSON.stringify(name)}); }
        const offered = [...select.options].some((option) => option.value === ${JSON.stringify(value)});
        if (!offered) { throw new Error("the select does not offer " + ${JSON.stringify(value)}); }
        // A select already reading the target would satisfy the round-trip
        // wait without any write, so the walk refuses to prove nothing.
        if (select.value === ${JSON.stringify(value)}) {
          throw new Error("the select already reads " + ${JSON.stringify(value)});
        }
        const setter = Object.getOwnPropertyDescriptor(
          HTMLSelectElement.prototype, "value").set;
        setter.call(select, ${JSON.stringify(value)});
        select.dispatchEvent(new Event("change", { bubbles: true }));
      })()`);
      // React snaps a controlled select back to the stored current on the next
      // render, so after one step the value only reads new again once the
      // agent's surface update has round-tripped through the store.
      await step();
      await waitForCondition(
        target,
        `(() => {
          const select = document.querySelector(${JSON.stringify(selector)});
          return !!select && select.value === ${JSON.stringify(value)};
        })()`,
        `the agent's update to store ${name}=${value}`,
        deadline,
      );
      console.log("smoke: ok (set control round trip)");
    }

    if (compose !== "") {
      if (open === "") {
        throw new Error("the compose walk needs FLT_GUI_SMOKE_OPEN to name the session it covers");
      }
      if (expectTerminal === "") {
        throw new Error(
          "the compose walk needs FLT_GUI_SMOKE_TERMINAL: without a needle the survival read passes whether or not the covered terminal kept its screen",
        );
      }
      const clickNew = `(() => {
        const button = [...document.querySelectorAll(".fleet header button")]
          .find((entry) => entry.textContent.trim() === "New session");
        if (!button) { throw new Error("no New session button in the fleet header"); }
        button.click();
      })()`;
      const covered = `!!document.querySelector(".composer-overlay .new-session")`;
      await target.webContents.executeJavaScript(clickNew);
      await waitForCondition(target, covered, "the composer card to cover the session", deadline);
      // The settings page paints above the card: entering it while the card is
      // open must drop the card, and opening the card from under it must leave
      // the settings page.
      await target.webContents.executeJavaScript(`(() => {
        const button = [...document.querySelectorAll(".fleet header button")]
          .find((entry) => entry.textContent.trim() === "Agents");
        if (!button) { throw new Error("no Agents button in the fleet header"); }
        button.click();
      })()`);
      await waitForCondition(
        target,
        `!!document.querySelector(".agent-settings") && !document.querySelector(".composer-overlay")`,
        "the settings page to replace the composer card",
        deadline,
      );
      await target.webContents.executeJavaScript(clickNew);
      await waitForCondition(
        target,
        `${covered} && !document.querySelector(".agent-settings")`,
        "the composer card to replace the settings page",
        deadline,
      );
      await target.webContents.executeJavaScript(`(() => {
        const close = document.querySelector(".composer-close");
        if (!close) { throw new Error("no Close button on the composer card"); }
        close.click();
      })()`);
      await waitForCondition(
        target,
        `!document.querySelector(".composer-overlay")`,
        "the composer card to close after the settings round trip",
        deadline,
      );
      // Reopen for the shot, and read the terminal underneath again: the card
      // covers the session, so its screen must survive both toggles intact.
      await target.webContents.executeJavaScript(clickNew);
      await waitForCondition(target, covered, "the composer card to reopen", deadline);
      await waitForCondition(
        target,
        `!!document.querySelector(".session-view")`,
        "the session view to stay mounted under the card",
        deadline,
      );
      await waitForTerminal(target, expectTerminal, deadline);
      // A centered column that overflows must still reach both of its ends:
      // shrink the window and scroll the card column to each extreme.
      const bounds = target.getBounds();
      target.setBounds({ ...bounds, height: 560 });
      await step(300);
      await waitForCondition(
        target,
        `(() => {
          const harbor = document.querySelector(".composer-overlay .harbor-empty");
          harbor.scrollTop = 0;
          const first = harbor.firstElementChild;
          return first.getBoundingClientRect().top >= harbor.getBoundingClientRect().top;
        })()`,
        "the composer column's first child to stay reachable at the top of a short window",
        deadline,
      );
      await waitForCondition(
        target,
        `(() => {
          const harbor = document.querySelector(".composer-overlay .harbor-empty");
          harbor.scrollTop = harbor.scrollHeight;
          const start = document.querySelector(".composer-overlay button[type=submit]");
          return start.getBoundingClientRect().bottom <= harbor.getBoundingClientRect().bottom;
        })()`,
        "the composer's Start to stay reachable at the bottom of a short window",
        deadline,
      );
      target.setBounds(bounds);
      await step(300);
      console.log("smoke: ok (composer toggle)");
    }

    if (agentsAgent !== "") {
      // Open the settings page from the fleet header, flip the named agent's
      // Enabled switch, flip it back, and close the page. The switch is stored
      // through the daemon, so what this waits for is the round trip, not a
      // local state change.
      await target.webContents.executeJavaScript(`(() => {
        const open = [...document.querySelectorAll(".fleet header button")]
          .find((button) => button.textContent.trim() === "Agents");
        if (!open) { throw new Error("no Agents button in the fleet header"); }
        open.click();
      })()`);
      // Wait for the switch itself, not for the page's header: the header says
      // "0 installed · 0 known" before the catalogue has been read, so a text
      // wait would pass on an empty table and then look for a row that is not
      // there yet.
      await waitForCondition(
        target,
        `${switchFor(agentsAgent)} !== null`,
        `the settings page to list a switch for ${agentsAgent}`,
        deadline,
      );

      const before = (await target.webContents.executeJavaScript(
        `(() => { const input = ${switchFor(agentsAgent)}; return input ? input.checked : null; })()`,
      )) as boolean | null;
      if (before === null) {
        throw new Error(`the settings page has no enable switch for ${agentsAgent}`);
      }

      for (const wanted of [!before, before]) {
        await target.webContents.executeJavaScript(
          `(() => { ${switchFor(agentsAgent)}.click(); })()`,
        );
        await waitForCondition(
          target,
          `${switchFor(agentsAgent)}?.checked === ${wanted}`,
          `${agentsAgent}'s switch to read ${wanted}`,
          deadline,
        );
      }

      await target.webContents.executeJavaScript(`(() => {
        const back = [...document.querySelectorAll(".agent-settings header button")]
          .find((button) => button.textContent.trim() === "Back");
        if (!back) { throw new Error("no Back button on the settings page"); }
        back.click();
      })()`);
      await waitForCondition(
        target,
        `!document.querySelector(".agent-settings")`,
        "the settings page to close",
        deadline,
      );

      console.log(`smoke: ok (agents: ${agentsAgent})`);
      return true;
    }

    if (diffFile !== "") {
      await clickButton(target, "Diff");
      await waitFor(target, diffFile, deadline);
      await clickButton(target, diffFile);
      if (diffLine !== "") {
        // The renderer strips git's prefix, so an added line is found by the
        // class that colours it, not by a leading "+".
        const added = diffLine.replace(/^\+/, "");
        await waitForAddedLine(target, added, deadline);
      }

      if (note !== "") {
        await leaveNote(target, note);
        const text = await waitFor(target, note, deadline);
        console.log(`smoke: ok (note: ${note})`);
        console.log(text);
        return true;
      }

      if (diffLine !== "") {
        console.log(`smoke: ok (diff: ${diffLine})`);
        return true;
      }
    }

    return await finish(target, expect, expectTerminal, deadline);
  } catch (error) {
    console.error(`smoke: ${error instanceof Error ? error.message : String(error)}`);
    return false;
  }
}
