import { Plus } from "lucide-react";
import { useRef, useState } from "react";

import { ApiClientError } from "@/api/client";
import {
  externalGroupIdSchema,
  type PluginAuthGroupMapping,
  type ReplacePluginAuthGroupMappingsRequest,
} from "@/api/pluginAuthMappings";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useAccessGroups } from "@/hooks/queries/admin/accessGroups";
import {
  usePluginAuthGroupMappings,
  useReplacePluginAuthGroupMappings,
} from "@/hooks/queries/admin/plugins";

import { AuthGroupMappingRow, type MappingDraft } from "./AuthGroupMappingRow";
import { AuthGroupMappingPreview } from "./AuthGroupMappingPreview";
import { AdminMappingConfirmDialog } from "./AdminMappingConfirmDialog";

type Props = {
  readonly installationId: number;
  readonly capabilityId: string;
  readonly enabled: boolean;
};

function mappingDraft(mapping: PluginAuthGroupMapping, key: string): MappingDraft {
  return {
    key,
    externalGroupId: mapping.external_group_id,
    targetRole: mapping.target_role ?? "",
    accessGroupId: mapping.access_group_id ?? null,
  };
}

function errorMessage(error: unknown): string {
  if (error instanceof ApiClientError && error.status === 409) return error.message;
  if (error instanceof Error) return error.message;
  return "Could not save external group mappings.";
}

export function AuthGroupMappingsPanel({ installationId, capabilityId, enabled }: Props) {
  const mappings = usePluginAuthGroupMappings({ installationId, capabilityId, enabled });
  const accessGroups = useAccessGroups();
  const replaceMappings = useReplacePluginAuthGroupMappings();
  const nextKey = useRef(0);
  const [localDrafts, setLocalDrafts] = useState<readonly MappingDraft[] | null>(null);
  const [saveError, setSaveError] = useState("");
  const [confirmAdmin, setConfirmAdmin] = useState(false);
  const drafts =
    localDrafts ??
    (mappings.data ?? []).map((mapping) =>
      mappingDraft(mapping, `persisted-${mapping.external_group_id}`),
    );

  function updateDraft(next: MappingDraft) {
    setSaveError("");
    setLocalDrafts(drafts.map((draft) => (draft.key === next.key ? next : draft)));
  }

  function removeDraft(key: string) {
    setSaveError("");
    setLocalDrafts(drafts.filter((draft) => draft.key !== key));
  }

  function requestBody(): ReplacePluginAuthGroupMappingsRequest["mappings"] | null {
    const ids = drafts.map((draft) => draft.externalGroupId.trim());
    if (new Set(ids).size !== ids.length) {
      setSaveError("External group IDs must be unique");
      return null;
    }
    const invalid = ids.find((id) => !externalGroupIdSchema.safeParse(id).success);
    if (invalid !== undefined) {
      setSaveError("Enter a valid exact external group ID without wildcards.");
      return null;
    }
    const targetless = drafts.some(
      (draft) => draft.targetRole === "" && draft.accessGroupId === null,
    );
    if (targetless) {
      setSaveError("Choose a local role or access group for every mapping.");
      return null;
    }
    return drafts.map((draft) => ({
      external_group_id: externalGroupIdSchema.parse(draft.externalGroupId),
      ...(draft.targetRole === "" ? {} : { target_role: draft.targetRole }),
      ...(draft.accessGroupId === null ? {} : { access_group_id: draft.accessGroupId }),
    }));
  }

  async function save() {
    const body = requestBody();
    if (body === null) return;
    try {
      await replaceMappings.mutateAsync({
        installationId,
        capabilityId,
        mappings: body,
      });
      setLocalDrafts(null);
      setSaveError("");
    } catch (error) {
      setSaveError(
        error instanceof Error ? errorMessage(error) : "Could not save external group mappings.",
      );
    }
  }

  if (!enabled) {
    return (
      <p className="text-muted-foreground rounded-md border border-dashed p-3 text-xs">
        Enable this auth provider and choose authoritative external groups to manage mappings.
      </p>
    );
  }

  if (mappings.isLoading || accessGroups.isLoading) {
    return (
      <div
        role="status"
        aria-label="Loading external group mappings"
        className="space-y-2 rounded-md border border-dashed p-3"
      >
        <p className="text-muted-foreground text-xs">Loading external group mappings...</p>
        <Skeleton className="h-14 w-full" />
      </div>
    );
  }

  if (mappings.isError || accessGroups.isError) {
    return (
      <div role="alert" className="border-destructive/40 bg-destructive/10 rounded-md border p-3">
        <p className="text-sm">Could not load external group mappings.</p>
        <Button
          type="button"
          size="sm"
          variant="outline"
          className="mt-2"
          onClick={() => {
            if (mappings.isError) void mappings.refetch();
            if (accessGroups.isError) void accessGroups.refetch();
          }}
        >
          Try again
        </Button>
      </div>
    );
  }

  const hasAdmin = drafts.some((draft) => draft.targetRole === "admin");
  const accessGroupList = accessGroups.data ?? [];

  return (
    <section
      aria-labelledby={`mapping-title-${capabilityId}`}
      className="space-y-3 rounded-lg border p-3"
    >
      <div className="space-y-1">
        <h4 id={`mapping-title-${capabilityId}`} className="text-sm font-semibold">
          External group mappings
        </h4>
        <p className="text-muted-foreground text-xs">
          Saving replaces the complete list atomically. Changed authorization demotes affected users
          to safe defaults and revokes existing sessions.
        </p>
      </div>

      {drafts.length === 0 ? (
        <p className="text-muted-foreground rounded-md border border-dashed p-3 text-xs">
          No external group mappings. Unmatched users keep the user role and default access group.
        </p>
      ) : (
        drafts.map((draft) => (
          <AuthGroupMappingRow
            key={draft.key}
            draft={draft}
            accessGroups={accessGroupList}
            disabled={replaceMappings.isPending}
            onChange={updateDraft}
            onDelete={() => removeDraft(draft.key)}
          />
        ))
      )}

      {saveError ? (
        <p role="alert" aria-live="assertive" className="text-destructive text-sm">
          {saveError}
        </p>
      ) : null}

      <div className="flex flex-wrap gap-2">
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={replaceMappings.isPending}
          onClick={() => {
            nextKey.current += 1;
            setLocalDrafts([
              ...drafts,
              {
                key: `new-${nextKey.current}`,
                externalGroupId: "",
                targetRole: "user",
                accessGroupId: null,
              },
            ]);
          }}
        >
          <Plus className="size-4" />
          Add mapping
        </Button>
        <Button
          type="button"
          size="sm"
          disabled={replaceMappings.isPending}
          onClick={() => (hasAdmin ? setConfirmAdmin(true) : void save())}
        >
          {replaceMappings.isPending ? "Saving..." : "Save mappings"}
        </Button>
      </div>

      <AuthGroupMappingPreview
        installationId={installationId}
        capabilityId={capabilityId}
        accessGroups={accessGroupList}
        onError={setSaveError}
      />

      <AdminMappingConfirmDialog
        open={confirmAdmin}
        onOpenChange={setConfirmAdmin}
        onConfirm={() => void save()}
      />
    </section>
  );
}
