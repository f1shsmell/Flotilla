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

export async function run(target: BrowserWindow): Promise<boolean> {
  const expect = process.env.FLT_GUI_SMOKE_EXPECT ?? "";
  const open = process.env.FLT_GUI_SMOKE_OPEN ?? "";
  const prompt = process.env.FLT_GUI_SMOKE_PROMPT ?? "";
  const deadline = Date.now() + 120000;

  target.webContents.on("render-process-gone", (_event, details) => {
    console.error(`smoke: the renderer is gone (${details.reason}, ${details.exitCode})`);
  });
  target.webContents.on("console-message", (...args: unknown[]) => {
    console.error("smoke: renderer says", ...args.slice(1, 4));
  });

  try {
    if (open !== "") {
      await waitFor(target, open, deadline);
      await clickSession(target, open);
      await waitFor(target, "Cancel turn", deadline);
    }

    if (prompt !== "") {
      await sendPrompt(target, prompt);
    }

    const text = await waitFor(target, expect, deadline);
    console.log(`smoke: ok (${expect || "rendered"})`);
    console.log(text);
    return true;
  } catch (error) {
    console.error(`smoke: ${error instanceof Error ? error.message : String(error)}`);
    return false;
  }
}
