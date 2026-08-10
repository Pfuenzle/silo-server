import { useState } from "react";

import type { AccessGroup } from "@/api/types";
import {
  externalGroupIdSchema,
  type PluginAuthGroupMappingPreview as PreviewResult,
} from "@/api/pluginAuthMappings";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { usePreviewPluginAuthGroupMappings } from "@/hooks/queries/admin/plugins";

type Props = {
  readonly installationId: number;
  readonly capabilityId: string;
  readonly accessGroups: readonly Pick<AccessGroup, "id" | "name" | "is_default">[];
  readonly onError: (message: string) => void;
};

export function AuthGroupMappingPreview({
  installationId,
  capabilityId,
  accessGroups,
  onError,
}: Props) {
  const previewMappings = usePreviewPluginAuthGroupMappings();
  const [externalGroupId, setExternalGroupId] = useState("");
  const [result, setResult] = useState<PreviewResult | null>(null);
  const resultAccessGroup =
    result === null
      ? undefined
      : result.access_group_id === undefined
        ? result.matched
          ? undefined
          : accessGroups.find((group) => group.is_default)
        : accessGroups.find((group) => group.id === result.access_group_id);

  async function preview() {
    setResult(null);
    const parsed = externalGroupIdSchema.safeParse(externalGroupId);
    if (!parsed.success) {
      onError("Enter a valid exact external group ID to preview.");
      return;
    }
    try {
      const results = await previewMappings.mutateAsync({
        installationId,
        capabilityId,
        externalGroupIds: [parsed.data],
      });
      setResult(results[0] ?? null);
      onError("");
    } catch (error) {
      onError(error instanceof Error ? error.message : "Could not preview saved authorization.");
    }
  }

  return (
    <div className="space-y-2 border-t pt-3">
      <Label htmlFor={`mapping-preview-${capabilityId}`}>Preview saved authorization</Label>
      <div className="flex flex-col gap-2 sm:flex-row">
        <Input
          id={`mapping-preview-${capabilityId}`}
          value={externalGroupId}
          placeholder="Exact external group ID"
          onChange={(event) => {
            setExternalGroupId(event.target.value);
            setResult(null);
          }}
        />
        <Button
          type="button"
          variant="outline"
          disabled={previewMappings.isPending}
          onClick={() => void preview()}
        >
          {previewMappings.isPending ? "Previewing..." : "Preview"}
        </Button>
      </div>
      {result ? (
        <p aria-live="polite" className="text-muted-foreground text-xs">
          {result.matched ? "Matched" : "Unmatched"}: role {result.target_role}
          {resultAccessGroup
            ? `, access group ${resultAccessGroup.name}${resultAccessGroup.is_default ? " (default)" : ""}`
            : result.access_group_id
              ? `, access group ${result.access_group_id}`
              : ""}
        </p>
      ) : null}
    </div>
  );
}
