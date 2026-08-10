import { Trash2 } from "lucide-react";
import { useId } from "react";

import type { AccessGroup } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

export type MappingDraft = {
  readonly key: string;
  readonly externalGroupId: string;
  readonly targetRole: "" | "user" | "admin";
  readonly accessGroupId: number | null;
};

type Props = {
  readonly draft: MappingDraft;
  readonly accessGroups: readonly AccessGroup[];
  readonly disabled: boolean;
  readonly onChange: (draft: MappingDraft) => void;
  readonly onDelete: () => void;
};

function roleFromValue(value: string): MappingDraft["targetRole"] {
  switch (value) {
    case "user":
    case "admin":
      return value;
    default:
      return "";
  }
}

export function AuthGroupMappingRow({ draft, accessGroups, disabled, onChange, onDelete }: Props) {
  const id = useId();
  const groupLabel = draft.externalGroupId.trim() || "new mapping";

  return (
    <div className="bg-surface grid gap-3 rounded-lg border p-3 sm:grid-cols-3">
      <div className="space-y-1.5 sm:col-span-3">
        <Label htmlFor={`${id}-external-id`}>Exact external group ID</Label>
        <Input
          id={`${id}-external-id`}
          autoFocus={draft.key.startsWith("new-")}
          value={draft.externalGroupId}
          disabled={disabled}
          autoComplete="off"
          spellCheck={false}
          placeholder="directory-team-id"
          onChange={(event) => onChange({ ...draft, externalGroupId: event.target.value })}
        />
        <p className="text-muted-foreground text-xs">
          Exact, case-sensitive ID only. Display names and wildcards are never matched.
        </p>
      </div>

      <div className="space-y-1.5">
        <Label htmlFor={`${id}-role`}>Local role</Label>
        <Select
          value={draft.targetRole || "none"}
          disabled={disabled}
          onValueChange={(value) => onChange({ ...draft, targetRole: roleFromValue(value) })}
        >
          <SelectTrigger id={`${id}-role`} className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="none">No role change</SelectItem>
            <SelectItem value="user">User</SelectItem>
            <SelectItem value="admin">Admin</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <div className="space-y-1.5">
        <Label htmlFor={`${id}-access-group`}>Local access group</Label>
        <Select
          value={draft.accessGroupId === null ? "none" : String(draft.accessGroupId)}
          disabled={disabled}
          onValueChange={(value) =>
            onChange({ ...draft, accessGroupId: value === "none" ? null : Number(value) })
          }
        >
          <SelectTrigger id={`${id}-access-group`} className="w-full">
            <SelectValue placeholder="No access-group change" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="none">No access-group change</SelectItem>
            {accessGroups.map((group) => (
              <SelectItem key={group.id} value={String(group.id)}>
                {group.name}
                {group.is_default ? " (default)" : ""}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      <div className="flex items-end sm:justify-end">
        <Button
          type="button"
          variant="ghost"
          disabled={disabled}
          aria-label={`Delete mapping ${groupLabel}`}
          onClick={onDelete}
        >
          <Trash2 className="size-4" />
          Delete
        </Button>
      </div>
    </div>
  );
}
