import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { api } from "@/api/client";
import {
  pluginAuthGroupMappingPreviewSchema,
  pluginAuthGroupMappingsSchema,
  previewPluginAuthGroupMappingsRequestSchema,
  replacePluginAuthGroupMappingsRequestSchema,
  type PluginAuthGroupMappingPreview,
  type ReplacePluginAuthGroupMappingsRequest,
} from "@/api/pluginAuthMappings";
import { adminKeys } from "../keys";

type MappingScope = {
  readonly installationId: number;
  readonly capabilityId: string;
};

type MappingQueryScope = MappingScope & {
  readonly enabled: boolean;
};

type ReplaceMappingsVariables = MappingScope & {
  readonly mappings: ReplacePluginAuthGroupMappingsRequest["mappings"];
};

type PreviewMappingsVariables = MappingScope & {
  readonly externalGroupIds: readonly string[];
};

export function usePluginAuthGroupMappings({
  installationId,
  capabilityId,
  enabled,
}: MappingQueryScope) {
  return useQuery({
    queryKey: adminKeys.pluginAuthGroupMappings(installationId, capabilityId),
    queryFn: async () => {
      const data = await api<unknown>(
        `/admin/plugins/installations/${installationId}/auth-group-mappings?capability_id=${encodeURIComponent(capabilityId)}`,
      );
      return pluginAuthGroupMappingsSchema.parse(data);
    },
    enabled,
  });
}

export function useReplacePluginAuthGroupMappings() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ installationId, capabilityId, mappings }: ReplaceMappingsVariables) => {
      const body = replacePluginAuthGroupMappingsRequestSchema.parse({
        capability_id: capabilityId,
        mappings,
      });
      const data = await api<unknown>(
        `/admin/plugins/installations/${installationId}/auth-group-mappings`,
        { method: "PUT", body: JSON.stringify(body) },
      );
      return pluginAuthGroupMappingsSchema.parse(data);
    },
    onSuccess: (mappings, variables) => {
      queryClient.setQueryData(
        adminKeys.pluginAuthGroupMappings(variables.installationId, variables.capabilityId),
        mappings,
      );
      toast.success("External group mappings saved");
    },
  });
}

export function usePreviewPluginAuthGroupMappings() {
  return useMutation<PluginAuthGroupMappingPreview[], Error, PreviewMappingsVariables>({
    mutationFn: async ({ installationId, capabilityId, externalGroupIds }) => {
      const body = previewPluginAuthGroupMappingsRequestSchema.parse({
        capability_id: capabilityId,
        external_group_ids: externalGroupIds,
      });
      const data = await api<unknown>(
        `/admin/plugins/installations/${installationId}/auth-group-mappings/preview`,
        { method: "POST", body: JSON.stringify(body) },
      );
      return pluginAuthGroupMappingPreviewSchema.parse(data);
    },
  });
}
