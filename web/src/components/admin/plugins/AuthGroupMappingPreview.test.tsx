import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";

import { AuthGroupMappingPreview } from "./AuthGroupMappingPreview";

const previewMutateAsyncMock = vi.fn();

vi.mock("@/hooks/queries/admin/plugins", () => ({
  usePreviewPluginAuthGroupMappings: () => ({
    mutateAsync: previewMutateAsyncMock,
    isPending: false,
  }),
}));

it("shows the unmatched default access group and clears stale results on input", async () => {
  previewMutateAsyncMock.mockResolvedValue([
    { external_group_id: "unknown-team", target_role: "user", matched: false },
  ]);
  const user = userEvent.setup();
  render(
    <AuthGroupMappingPreview
      installationId={17}
      capabilityId="ldap"
      accessGroups={[{ id: 1, name: "Default viewers", is_default: true }]}
      onError={vi.fn()}
    />,
  );

  const input = screen.getByLabelText("Preview saved authorization");
  await user.type(input, "unknown-team");
  await user.click(screen.getByRole("button", { name: "Preview" }));
  expect(
    screen.getByText(/Unmatched: role user, access group Default viewers \(default\)/),
  ).toBeVisible();

  await user.type(input, "-changed");
  expect(screen.queryByText(/Unmatched: role user/)).not.toBeInTheDocument();
});
