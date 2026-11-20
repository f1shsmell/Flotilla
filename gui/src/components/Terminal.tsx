import { useEffect, useRef } from "react";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal as XTerm } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { attachGpuRenderer } from "./renderer";

export interface TerminalCallbacks {
  onInput: (data: string) => void;
  onResize: (cols: number, rows: number) => void;
  onReady: (write: (bytes: Uint8Array) => void) => void;
}

// resizeDebounce keeps a dragged window from turning into a stream of resize
// calls; the PTY only needs the size it ends up at.
const resizeDebounce = 150;

// Terminal is the imperative half of the view: xterm owns its canvas, its
// selection and its IME, so it is created once and never re-created by a render.
export function Terminal(callbacks: TerminalCallbacks): React.ReactElement {
  const host = useRef<HTMLDivElement>(null);

  // The callbacks are read through a ref so a new identity from the parent does
  // not tear the terminal down and lose its scrollback.
  const latest = useRef(callbacks);
  latest.current = callbacks;

  useEffect(() => {
    const element = host.current;
    if (!element) {
      return;
    }

    const term = new XTerm({
      fontFamily: 'ui-monospace, "Cascadia Mono", Consolas, monospace',
      fontSize: 13,
      cursorBlink: true,
      theme: {
        background: "#101216",
        foreground: "#e6e9ef",
        cursor: "#6aa9ff",
      },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(element);

    if (window.flotilla.smoke) {
      (window as unknown as { __fltTerminal?: XTerm }).__fltTerminal = term;
    }
    attachGpuRenderer(term, (name) => {
      if (window.flotilla.smoke) {
        (window as unknown as { __fltRenderer?: string }).__fltRenderer = name;
      }
    });

    latest.current.onReady((bytes) => term.write(bytes));
    const input = term.onData((data) => latest.current.onInput(data));

    let timer: ReturnType<typeof setTimeout> | undefined;
    const measure = (): void => {
      fit.fit();
      if (timer) {
        clearTimeout(timer);
      }
      timer = setTimeout(() => {
        latest.current.onResize(term.cols, term.rows);
      }, resizeDebounce);
    };

    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);

    return () => {
      if (timer) {
        clearTimeout(timer);
      }
      observer.disconnect();
      input.dispose();
      term.dispose();
    };
  }, []);

  return <div className="terminal" ref={host} />;
}
