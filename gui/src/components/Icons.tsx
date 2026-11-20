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

export function IconSend(): React.ReactElement {
  return (
    <Svg>
      <path d="M8 13V3M4 7l4-4 4 4" />
    </Svg>
  );
}

export function IconShield(): React.ReactElement {
  return (
    <Svg>
      <path d="M8 2l5 2v4c0 3-2.2 5.2-5 6-2.8-.8-5-3-5-6V4z" />
    </Svg>
  );
}

export function IconTool(): React.ReactElement {
  return (
    <Svg>
      <path d="M9.5 6.5a3 3 0 0 1-4 4L3 13l-.5-.5L5 10a3 3 0 0 1 4-4L7.5 7.5l1 1z" />
    </Svg>
  );
}

export function IconMessage(): React.ReactElement {
  return (
    <Svg>
      <path d="M2.5 3.5h11v7h-6l-3 2.5v-2.5h-2z" />
    </Svg>
  );
}

export function IconThought(): React.ReactElement {
  return (
    <Svg>
      <path d="M5 13h6M6 11a4 4 0 1 1 4 0c-.6.5-.8 1-.8 2H6.8c0-1-.2-1.5-.8-2z" />
    </Svg>
  );
}

export function IconUser(): React.ReactElement {
  return (
    <Svg>
      <circle cx={8} cy={5.5} r={2.5} />
      <path d="M3.5 13c.6-2.4 2.3-3.5 4.5-3.5S11.9 10.6 12.5 13" />
    </Svg>
  );
}

export function IconPlan(): React.ReactElement {
  return (
    <Svg>
      <path d="M3 4h10M3 8h10M3 12h6" />
    </Svg>
  );
}
