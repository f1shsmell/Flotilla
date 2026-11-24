import type { Permission, PermissionOption } from "../store";

interface Props {
  permission: Permission;
  onAnswer: (questionId: string, optionId: string) => void;
}

// The four things an approval can mean, in the words a person uses. ACP names
// the option's kind, so the window never has to show a protocol id like
// "allow_once": the agent's own label is used when it gave one, and this maps
// the kind when it did not.
const allowLabels: Record<string, string> = {
  allow_once: "Allow once",
  allow_always: "Always allow",
  reject_once: "Deny",
  reject_always: "Always deny",
};

// optionLabel is what a button says. Order matters: the agent's own words win
// (it knows what this tool call is), then the kind's plain word, and only then
// the id — which is the last resort, because an id is a protocol detail.
export function optionLabel(option: PermissionOption): string {
  if (option.name.trim() !== "") {
    return option.name.trim();
  }
  const byKind = allowLabels[option.kind];
  if (byKind !== undefined) {
    return byKind;
  }
  return option.id;
}

// optionClass styles a choice by what it does, not by its id: a grant reads as
// the primary button and anything else stays quiet, so a denial is never
// mistaken for the expected answer.
export function optionClass(option: PermissionOption): string {
  return option.kind.startsWith("allow") ? "primary" : "";
}

export function PermissionCard({ permission, onAnswer }: Props): React.ReactElement {
  return (
    <div className="permission">
      <p>
        <strong>{permission.title || "The agent asks to proceed"}</strong>
        {permission.kind ? <span className="muted"> · {permission.kind}</span> : null}
      </p>
      <div className="permission-options">
        {permission.options.map((option) => (
          <button
            type="button"
            key={option.id}
            className={optionClass(option)}
            onClick={() => onAnswer(permission.id, option.id)}
          >
            {optionLabel(option)}
          </button>
        ))}
      </div>
    </div>
  );
}
