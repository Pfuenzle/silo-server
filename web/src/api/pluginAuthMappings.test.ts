import { describe, expect, it } from "vitest";

import {
  externalGroupIdSchema,
  pluginAuthGroupMappingsSchema,
  replacePluginAuthGroupMappingsRequestSchema,
} from "./pluginAuthMappings";

describe("plugin auth group mapping schemas", () => {
  it.each(["", "*", "team-*", "x".repeat(257)])("rejects malformed exact group ID %j", (id) => {
    expect(externalGroupIdSchema.safeParse(id).success).toBe(false);
  });

  it("rejects malformed API mapping data", () => {
    expect(
      pluginAuthGroupMappingsSchema.safeParse([
        {
          external_group_id: "directory-team-a",
          target_role: "owner",
          created_at: "2026-07-30T00:00:00Z",
          updated_at: "2026-07-30T00:00:00Z",
        },
      ]).success,
    ).toBe(false);
  });

  it("rejects a targetless replacement instead of issuing a partial write", () => {
    expect(
      replacePluginAuthGroupMappingsRequestSchema.safeParse({
        capability_id: "ldap",
        mappings: [{ external_group_id: "directory-team-a" }],
      }).success,
    ).toBe(false);
  });
});
