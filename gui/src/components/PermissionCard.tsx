import type { Permission } from "../store";

interface Props {
  permission: Permission;
  onAnswer: (questionId: string, optionId: string) => void;
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
            className={option.kind.startsWith("allow") ? "primary" : ""}
            onClick={() => onAnswer(permission.id, option.id)}
          >
            {option.id}
          </button>
        ))}
      </div>
    </div>
  );
}
