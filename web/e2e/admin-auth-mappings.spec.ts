import { expect, test, type Page, type Route } from "@playwright/test";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";

import { replacePluginAuthGroupMappingsRequestSchema } from "../src/api/pluginAuthMappings";

const evidenceDir = path.resolve(process.cwd(), "../.omo/evidence/task-7-external-oidc-ldap-auth");

type MappingMode = "ok" | "loading" | "error";

type FixtureState = {
  mappingMode: MappingMode;
  conflictOnReplace: boolean;
  mappings: readonly {
    readonly external_group_id: string;
    readonly target_role?: "user" | "admin";
    readonly access_group_id?: number;
    readonly created_at: string;
    readonly updated_at: string;
  }[];
  releaseLoading: (() => void) | null;
};

function json(route: Route, body: unknown, status = 200) {
  return route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
}

async function installApiFixture(page: Page): Promise<FixtureState> {
  const state: FixtureState = {
    mappingMode: "ok",
    conflictOnReplace: false,
    mappings: [],
    releaseLoading: null,
  };
  await page.addInitScript(() => localStorage.setItem("refresh_token", "e2e-refresh-token"));
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;

    if (pathname === "/api/v1/auth/setup") return json(route, { needs_setup: false });
    if (pathname === "/api/v1/auth/providers") return json(route, []);
    if (pathname === "/api/v1/auth/refresh") {
      return json(route, {
        access_token: "e2e-admin-token",
        refresh_token: "e2e-refresh-token",
      });
    }
    if (pathname === "/api/v1/auth/me") {
      return json(route, {
        id: 1,
        username: "fixture-admin",
        email: "admin@example.invalid",
        role: "admin",
        permissions: [],
        download_allowed: true,
      });
    }
    if (pathname === "/api/v1/profiles") return json(route, { profiles: [] });
    if (pathname === "/api/v1/admin/sessions") return json(route, []);
    if (pathname === "/api/v1/admin/tasks") return json(route, []);
    if (pathname === "/api/v1/libraries") return json(route, []);
    if (pathname === "/api/v1/settings") return json(route, { settings: [] });
    if (pathname === "/api/v1/theme/admin-css") return json(route, { vars: "{}", raw_css: "" });
    if (pathname === "/api/v1/admin/plugins/repositories") return json(route, []);
    if (pathname === "/api/v1/admin/plugins/catalog") return json(route, []);
    if (pathname === "/api/v1/admin/plugins/catalog-settings") {
      return json(route, {
        include_approved_community_plugins: false,
        approved_community_plugin_count: 0,
        installed_community_plugin_count: 0,
        migrated_plugin_count: 0,
        community_updates_paused: false,
      });
    }
    if (pathname === "/api/v1/admin/plugins/installations") {
      return json(route, [
        {
          id: 17,
          plugin_id: "silo.auth.ldap",
          version: "1.0.0",
          install_path: "/plugins/ldap",
          enabled: true,
          source_kind: "silo",
          updates_paused: false,
          capabilities: [{ type: "auth_provider.v1", id: "ldap", display_name: "LDAP directory" }],
          global_config_schema: [],
          user_config_schema: [],
          routes: [],
          assets: [],
          global_configs: [],
          auth_bindings: [
            {
              capability_id: "ldap",
              enabled: true,
              display_order: 1,
              auto_provision: true,
              default_login: true,
              authorization_mode: "external_groups_v1",
              created_at: "2026-07-30T00:00:00Z",
              updated_at: "2026-07-30T00:00:00Z",
            },
          ],
          task_bindings: [],
          update_policy: "auto",
        },
      ]);
    }
    if (pathname === "/api/v1/admin/access-groups") {
      return json(route, [
        {
          id: 7,
          name: "Restricted library",
          description: "Fixture access group",
          library_ids: null,
          max_playback_quality: "original",
          download_allowed: true,
          download_transcode_allowed: true,
          max_streams: 0,
          max_transcodes: 0,
          allowed_permissions: null,
          requests_allowed: true,
          is_default: false,
          member_count: 0,
          created_at: "2026-07-30T00:00:00Z",
          updated_at: "2026-07-30T00:00:00Z",
        },
      ]);
    }
    if (pathname.endsWith("/auth-group-mappings/preview")) {
      return json(route, [
        {
          external_group_id: "directory-team-a",
          target_role: "user",
          matched: state.mappings.length > 0,
        },
      ]);
    }
    if (pathname.endsWith("/auth-group-mappings") && request.method() === "GET") {
      if (state.mappingMode === "error") {
        return json(route, { error: "unavailable", message: "Fixture mapping failure" }, 503);
      }
      if (state.mappingMode === "loading") {
        await new Promise<void>((resolve) => {
          state.releaseLoading = resolve;
        });
      }
      return json(route, state.mappings);
    }
    if (pathname.endsWith("/auth-group-mappings") && request.method() === "PUT") {
      if (state.conflictOnReplace) {
        return json(
          route,
          { error: "conflict", message: "External group IDs must be unique" },
          409,
        );
      }
      const parsed = replacePluginAuthGroupMappingsRequestSchema.parse(
        JSON.parse(request.postData() ?? "{}"),
      );
      state.mappings = parsed.mappings.map((mapping) => ({
        ...mapping,
        created_at: "2026-07-30T00:00:00Z",
        updated_at: "2026-07-30T00:00:00Z",
      }));
      return json(route, state.mappings);
    }
    if (pathname.includes("/api/v1/admin/tasks/")) {
      return json(route, { key: "check_plugin_updates", state: "idle" });
    }
    return json(route, {});
  });
  return state;
}

async function openMappings(page: Page) {
  await page.goto("/admin/plugins");
  await page.getByRole("button", { name: "Configure" }).click();
  await page.getByRole("button", { name: "Auth Providers" }).click();
}

test.beforeAll(async () => mkdir(evidenceDir, { recursive: true }));

test("creates, previews, deletes, and retains a conflicting draft", async ({ page }) => {
  const fixture = await installApiFixture(page);
  await openMappings(page);

  await page.getByRole("button", { name: "Add mapping" }).click();
  const groupId = page.getByLabel("Exact external group ID");
  await expect(groupId).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(page.getByLabel("Local role")).toBeFocused();
  await groupId.fill("directory-team-a");
  await page.getByLabel("Local access group").click();
  await page.getByRole("option", { name: "Restricted library" }).click();
  await page.getByRole("button", { name: "Save mappings" }).click();
  await expect(page.getByText("External group mappings saved")).toBeVisible();
  expect(fixture.mappings).toEqual([
    expect.objectContaining({ external_group_id: "directory-team-a", access_group_id: 7 }),
  ]);

  await page.getByLabel("Preview saved authorization").fill("directory-team-a");
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  await expect(page.getByText(/Matched: role user/)).toBeVisible();

  fixture.conflictOnReplace = true;
  await groupId.fill("conflict-group");
  await page.getByRole("button", { name: "Save mappings" }).click();
  await expect(page.getByRole("alert")).toContainText("External group IDs must be unique");
  await expect(groupId).toHaveValue("conflict-group");
  fixture.conflictOnReplace = false;

  await page.getByRole("button", { name: "Delete mapping conflict-group" }).click();
  await page.getByRole("button", { name: "Save mappings" }).click();
  await expect(page.getByText(/No external group mappings/)).toBeVisible();
});

test("captures responsive loading, error, dark-mode, and accessibility evidence", async ({
  page,
}) => {
  const fixture = await installApiFixture(page);
  fixture.mappingMode = "loading";
  await page.setViewportSize({ width: 375, height: 812 });
  await openMappings(page);
  await expect(page.getByLabel("Loading external group mappings")).toBeVisible();
  await page.getByRole("region", { name: "Auth Providers" }).evaluate(async (element) => {
    await Promise.all(element.getAnimations().map((animation) => animation.finished));
  });
  await page.screenshot({ path: path.join(evidenceDir, "mobile-375-loading.png"), fullPage: true });
  fixture.releaseLoading?.();
  await expect(page.getByText(/No external group mappings/)).toBeVisible();

  for (const width of [768, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    await page.screenshot({ path: path.join(evidenceDir, `mapping-${width}.png`), fullPage: true });
  }
  await expect(page.locator("html")).toHaveAttribute("data-theme", "midnight-cinema");
  const aria = await page.locator("body").ariaSnapshot();
  expect(aria).toContain("Authorization mode");
  await writeFile(path.join(evidenceDir, "admin-auth-mappings.aria.yml"), aria);

  fixture.mappingMode = "error";
  await page.reload();
  await page.getByRole("button", { name: "Configure" }).click();
  await page.getByRole("button", { name: "Auth Providers" }).click();
  await expect(page.getByRole("alert")).toContainText("Could not load external group mappings");
  await page.screenshot({ path: path.join(evidenceDir, "desktop-1280-error.png"), fullPage: true });
});
