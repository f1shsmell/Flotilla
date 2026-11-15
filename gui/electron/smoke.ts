// The smoke driver: it renders the real window offscreen and drives the real UI,
// so the main process, the preload bridge, the wire and the daemon are all in
// the loop. It never runs unless FLT_GUI_SMOKE is set.

import type { BrowserWindow } from "electron";

const step = (ms = 200) => new Promise((resolve) => setTimeout(resolve, ms));

async function body(target: BrowserWindow): Promise<string> {
  return target.webContents.executeJavaScript("document.body.innerText");
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
  throw new Error(`${needle} never appeared; body was:\n${text}`);
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

export async function run(target: BrowserWindow): Promise<boolean> {
  const expect = process.env.FLT_GUI_SMOKE_EXPECT ?? "";
  const expectTerminal = process.env.FLT_GUI_SMOKE_TERMINAL ?? "";
  const open = process.env.FLT_GUI_SMOKE_OPEN ?? "";
  const prompt = process.env.FLT_GUI_SMOKE_PROMPT ?? "";
  const startRequest = process.env.FLT_GUI_SMOKE_START ?? "";
  const form = process.env.FLT_GUI_SMOKE_FORM ?? "";
  const deadline = Date.now() + 120000;

  target.webContents.on("render-process-gone", (_event, details) => {
    console.error(`smoke: the renderer is gone (${details.reason}, ${details.exitCode})`);
  });
  target.webContents.on("console-message", (...args: unknown[]) => {
    console.error("smoke: renderer says", ...args.slice(1, 4));
  });

  try {
    if (form !== "") {
      await waitFor(target, "Start", deadline);
      await target.webContents.executeJavaScript(`(() => {
        const fields = ${form};
        const setValue = (element, value) => {
          const proto = element.tagName === "SELECT"
            ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
          const setter = Object.getOwnPropertyDescriptor(proto, "value").set;
          setter.call(element, value);
          element.dispatchEvent(new Event(
            element.tagName === "SELECT" ? "change" : "input", { bubbles: true }));
        };
        const [command, args, dir] = document.querySelectorAll(".new-session input");
        setValue(command, fields.command);
        setValue(args, fields.args ?? "");
        setValue(dir, fields.dir ?? "");
        if (fields.mode) {
          setValue(document.querySelector(".new-session select"), fields.mode);
        }
        document.querySelector(".new-session").requestSubmit();
      })()`);
      // The form's submit opens the new session, so the composer appears only
      // once the start and the attach have both come back.
      await waitFor(target, "Cancel turn", deadline);
      console.log("smoke: submitted the form");
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
        return finish(target, started.id, expectTerminal, deadline);
      }
    }

    if (open !== "") {
      await waitFor(target, open, deadline);
      await clickSession(target, open);
      await waitFor(target, "Cancel turn", deadline);
    }

    if (prompt !== "") {
      await sendPrompt(target, prompt);
    }

    return finish(target, expect, expectTerminal, deadline);
  } catch (error) {
    console.error(`smoke: ${error instanceof Error ? error.message : String(error)}`);
    return false;
  }
}
