import type React from "react";

// One stroke language for the whole shell: 16px grid, currentColor, round caps.
// Icons ride beside button text, never instead of it — the text is the label.
function Svg({ children }: { children: React.ReactNode }): React.ReactElement {
  return (
    <svg
      className="icon"
      width={16}
      height={16}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.5}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {children}
    </svg>
  );
}

export function IconPlus(): React.ReactElement {
  return (
    <Svg>
      <path d="M8 3v10M3 8h10" />
    </Svg>
  );
}

export function IconRefresh(): React.ReactElement {
  return (
    <Svg>
      <path d="M13 8a5 5 0 1 1-1.7-3.75" />
      <path d="M13 2.5V5h-2.5" />
    </Svg>
  );
}

export function IconSliders(): React.ReactElement {
  return (
    <Svg>
      <path d="M2.5 5h11M2.5 11h11" />
      <circle cx={6} cy={5} r={1.75} />
      <circle cx={10} cy={11} r={1.75} />
    </Svg>
  );
}

export function IconBack(): React.ReactElement {
  return (
    <Svg>
      <path d="M10 3 5 8l5 5" />
    </Svg>
  );
}

export function IconStop(): React.ReactElement {
  return (
    <Svg>
      <rect x={4} y={4} width={8} height={8} rx={1.5} />
    </Svg>
  );
}