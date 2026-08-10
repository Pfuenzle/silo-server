import { ApiClientError } from "@/api/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { authMappingInstallation } from "./AdminPlugins.auth-mappings.fixtures";
import AdminPlugins from "./AdminPlugins";

const saveAuthBindingMutateMock = vi.fn();
const replaceMappingsMutateAsyncMock = vi.fn();
const useAdminPluginsMock = vi.fn();
const useAccessGroupsMock = vi.fn();
const useMappingsMock = vi.fn();
const useSaveAuthBindingMock = vi.fn();

vi.mock("@/hooks/queries/admin/plugins", () => ({
  CHECK_PLUGIN_UPDATES_TASK_KEY: "check_plugin_updates",
  useAdminPlugins: () => useAdminPluginsMock(),
  useApplyPluginUpdate: () => ({ mutate: vi.fn(), isPending: false }),
  useCheckPluginUpdates: () => ({ mutate: vi.fn(), isPending: false }),
  useCreatePluginRepository: () => ({ mutate: vi.fn(), isPending: false }),
  useDeletePluginInstallation: () => ({ mutate: vi.fn(), isPending: false }),
  useDeletePluginRepository: () => ({ mutate: vi.fn(), isPending: false }),
  useInstallPlugin: () => ({ mutate: vi.fn(), isPending: false }),
  usePluginAuthGroupMappings: () => useMappingsMock(),
  usePluginUpload: () => ({ upload: vi.fn(), progress: null, isPending: false }),
  usePreviewPluginAuthGroupMappings: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useReplacePluginAuthGroupMappings: () => ({
    mutateAsync: replaceMappingsMutateAsyncMock,
    isPending: false,
  }),
  useSavePluginAuthBinding: () => useSaveAuthBindingMock(),
  useSavePluginConfig: () => ({ mutate: vi.fn(), isPending: false }),
  useSavePluginTaskBinding: () => ({ mutate: vi.fn(), isPending: false }),
  useTestPluginConfig: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useUpdatePluginCatalogSettings: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdatePluginInstallation: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdatePluginRepository: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock("@/hooks/queries/admin/accessGroups", () => ({
  useAccessGroups: () => useAccessGroupsMock(),
}));

vi.mock("@/hooks/queries/admin/tasks", () => ({
  useTask: () => ({ data: { key: "check_plugin_updates", state: "idle" } }),
}));

async function openAuthProviders() {
  const user = userEvent.setup();
  const queryClient = new QueryClient();
  const page = () => (
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <AdminPlugins />
      </MemoryRouter>
    </QueryClientProvider>
  );
  const view = render(page());
  await user.click(screen.getByRole("button", { name: "Configure" }));
  await user.click(screen.getByRole("button", { name: /Auth Providers/i }));
  return { user, rerender: () => view.rerender(page()) };
}

function setAuthorizationMode(mode: "none" | "external_groups_v1") {
  useAdminPluginsMock.mockReturnValue({
    repositories: [],
    catalog: [],
    catalogSettings: undefined,
    installations: [authMappingInstallation(mode)],
    isLoading: false,
  });
}

describe("AdminPlugins authoritative auth mappings", () => {
  beforeEach(() => {
    saveAuthBindingMutateMock.mockReset();
    replaceMappingsMutateAsyncMock.mockReset();
    replaceMappingsMutateAsyncMock.mockResolvedValue([]);
    useAccessGroupsMock.mockReturnValue({
      data: [
        { id: 1, name: "Default viewers", is_default: true },
        { id: 7, name: "Restricted library", is_default: false },
      ],
      isLoading: false,
      isError: false,
    });
    useMappingsMock.mockReturnValue({ data: [], isLoading: false, isError: false });
    useSaveAuthBindingMock.mockReturnValue({
      mutate: saveAuthBindingMutateMock,
      isPending: false,
      isError: false,
      error: null,
    });
    setAuthorizationMode("external_groups_v1");
  });

  it("explains authoritative mode, restart, and revocation risk", async () => {
    await openAuthProviders();

    expect(screen.getByLabelText("Authorization mode")).toHaveTextContent(
      "Authoritative external groups",
    );
    expect(screen.getByText(/overwrites local role and access-group edits/i)).toBeVisible();
    expect(screen.getByText(/revokes existing sessions/i)).toBeVisible();
    expect(screen.getByText(/restart the server/i)).toBeVisible();
    expect(screen.getAllByRole("button", { name: "Close" })).toHaveLength(1);
  });

  it("replaces the complete mapping list with exact external IDs", async () => {
    const { user } = await openAuthProviders();

    await user.click(screen.getByRole("button", { name: "Add mapping" }));
    await user.type(screen.getByLabelText("Exact external group ID"), "directory-team-a");
    await user.click(screen.getByRole("button", { name: "Save mappings" }));

    expect(replaceMappingsMutateAsyncMock).toHaveBeenCalledWith({
      installationId: 17,
      capabilityId: "ldap",
      mappings: [
        {
          external_group_id: "directory-team-a",
          target_role: "user",
        },
      ],
    });
  });

  it("deletes through atomic replacement and exposes the local access-group selector", async () => {
    useMappingsMock.mockReturnValue({
      data: [
        {
          external_group_id: "directory-team-a",
          target_role: "user",
          created_at: "2026-07-30T00:00:00Z",
          updated_at: "2026-07-30T00:00:00Z",
        },
      ],
      isLoading: false,
      isError: false,
    });
    const { user } = await openAuthProviders();

    expect(screen.getByLabelText("Local access group")).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "Delete mapping directory-team-a" }));
    await user.click(screen.getByRole("button", { name: "Save mappings" }));

    expect(replaceMappingsMutateAsyncMock).toHaveBeenCalledWith({
      installationId: 17,
      capabilityId: "ldap",
      mappings: [],
    });
  });

  it("keeps the draft and announces a 409 conflict", async () => {
    replaceMappingsMutateAsyncMock.mockRejectedValue(
      new ApiClientError(409, "conflict", "External group IDs must be unique"),
    );
    const { user } = await openAuthProviders();

    await user.click(screen.getByRole("button", { name: "Add mapping" }));
    const groupId = screen.getByLabelText("Exact external group ID");
    await user.type(groupId, "directory-team-a");
    await user.click(screen.getByRole("button", { name: "Save mappings" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("External group IDs must be unique");
    expect(groupId).toHaveValue("directory-team-a");
  });

  it.each([
    [{ data: undefined, isLoading: true, isError: false }, "Loading external group mappings"],
    [{ data: [], isLoading: false, isError: false }, "No external group mappings"],
    [{ data: undefined, isLoading: false, isError: true, refetch: vi.fn() }, "Could not load"],
  ])("renders the mapping query state", async (query, expected) => {
    useMappingsMock.mockReturnValue(query);
    await openAuthProviders();
    const state =
      expected === "Loading external group mappings"
        ? screen.getByLabelText(expected)
        : screen.getByText(new RegExp(expected, "i"));
    expect(state).toBeVisible();
  });

  it("disables mappings until the provider opts into authoritative mode", async () => {
    setAuthorizationMode("none");
    await openAuthProviders();

    expect(screen.getByLabelText("Authorization mode")).toHaveTextContent("Local authorization");
    expect(
      screen.getByText(/choose authoritative external groups to manage mappings/i),
    ).toBeVisible();
    expect(screen.queryByRole("button", { name: "Add mapping" })).not.toBeInTheDocument();
  });

  it("retries access groups when their query fails", async () => {
    const refetch = vi.fn();
    useAccessGroupsMock.mockReturnValue({ isLoading: false, isError: true, refetch });
    const { user } = await openAuthProviders();

    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(refetch).toHaveBeenCalledTimes(1);
  });

  it("announces auth-provider save failures", async () => {
    useSaveAuthBindingMock.mockReturnValue({
      mutate: saveAuthBindingMutateMock,
      isPending: false,
      isError: true,
      error: new Error("Binding update failed"),
    });
    await openAuthProviders();

    expect(screen.getByRole("alert")).toHaveTextContent("Binding update failed");
  });

  it("discards a dirty mapping after leaving authoritative mode", async () => {
    const { user, rerender } = await openAuthProviders();
    await user.click(screen.getByRole("button", { name: "Add mapping" }));
    await user.type(screen.getByLabelText("Exact external group ID"), "stale-directory-team");

    setAuthorizationMode("none");
    rerender();
    expect(
      await screen.findByText(/choose authoritative external groups to manage mappings/i),
    ).toBeVisible();
    setAuthorizationMode("external_groups_v1");
    rerender();

    expect(screen.queryByDisplayValue("stale-directory-team")).not.toBeInTheDocument();
    expect(screen.getByText(/No external group mappings/i)).toBeVisible();
  });

  it("blocks duplicate drafts before sending a replacement", async () => {
    const { user } = await openAuthProviders();
    await user.click(screen.getByRole("button", { name: "Add mapping" }));
    await user.click(screen.getByRole("button", { name: "Add mapping" }));
    for (const input of screen.getAllByLabelText("Exact external group ID")) {
      await user.type(input, "directory-team-a");
    }
    await user.click(screen.getByRole("button", { name: "Save mappings" }));

    expect(screen.getByRole("alert")).toHaveTextContent("External group IDs must be unique");
    expect(replaceMappingsMutateAsyncMock).not.toHaveBeenCalled();
  });

  it("requires danger confirmation before saving an admin mapping", async () => {
    useMappingsMock.mockReturnValue({
      data: [
        {
          external_group_id: "directory-admins",
          target_role: "admin",
          created_at: "2026-07-30T00:00:00Z",
          updated_at: "2026-07-30T00:00:00Z",
        },
      ],
      isLoading: false,
      isError: false,
    });
    const { user } = await openAuthProviders();
    await user.click(screen.getByRole("button", { name: "Save mappings" }));

    expect(screen.getByRole("alertdialog")).toHaveTextContent("administrator access");
    expect(replaceMappingsMutateAsyncMock).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Confirm admin mapping" }));
    expect(replaceMappingsMutateAsyncMock).toHaveBeenCalledTimes(1);
  });
});
