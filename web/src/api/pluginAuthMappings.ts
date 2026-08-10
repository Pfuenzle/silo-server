import { z } from "zod";

export const pluginAuthorizationModeSchema = z.enum(["none", "external_groups_v1"]);
export type PluginAuthorizationMode = z.infer<typeof pluginAuthorizationModeSchema>;

export const externalGroupIdSchema = z
  .string()
  .trim()
  .min(1, "Enter an exact external group ID.")
  .refine(
    (value) => new TextEncoder().encode(value).byteLength <= 256,
    "External group IDs must be 256 bytes or fewer.",
  )
  .refine((value) => !value.includes("*"), "Wildcards are not allowed.")
  .brand("ExternalGroupId");

export const pluginAuthGroupMappingSchema = z.object({
  external_group_id: externalGroupIdSchema,
  target_role: z.enum(["user", "admin"]).optional(),
  access_group_id: z.number().int().positive().optional(),
  created_at: z.string(),
  updated_at: z.string(),
});

export const pluginAuthGroupMappingsSchema = z.array(pluginAuthGroupMappingSchema);

export const replacePluginAuthGroupMappingsRequestSchema = z.object({
  capability_id: z.string().trim().min(1),
  mappings: z.array(
    z
      .object({
        external_group_id: externalGroupIdSchema,
        target_role: z.enum(["user", "admin"]).optional(),
        access_group_id: z.number().int().positive().optional(),
      })
      .refine(
        (mapping) => mapping.target_role !== undefined || mapping.access_group_id !== undefined,
        "Choose a role or local access group.",
      ),
  ),
});

export const previewPluginAuthGroupMappingsRequestSchema = z.object({
  capability_id: z.string().trim().min(1),
  external_group_ids: z.array(externalGroupIdSchema).min(1),
});

export const pluginAuthGroupMappingPreviewSchema = z.array(
  z.object({
    external_group_id: externalGroupIdSchema,
    target_role: z.enum(["user", "admin"]),
    access_group_id: z.number().int().positive().optional(),
    matched: z.boolean(),
  }),
);

export type PluginAuthGroupMapping = z.infer<typeof pluginAuthGroupMappingSchema>;
export type ReplacePluginAuthGroupMappingsRequest = z.infer<
  typeof replacePluginAuthGroupMappingsRequestSchema
>;
export type PluginAuthGroupMappingPreview = z.infer<
  typeof pluginAuthGroupMappingPreviewSchema
>[number];
