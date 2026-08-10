import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import CredentialProviderFallbackSettings from "./CredentialProviderFallbackSettings";

const mocks = vi.hoisted(() => ({
  useAuth: vi.fn(),
  useCredentialProviderFallback: vi.fn(),
  useUpdateCredentialProviderFallback: vi.fn(),
  mutateAsync: vi.fn(),
}));

vi.mock("@/hooks/useAuth", () => ({ useAuth: mocks.useAuth }));
vi.mock("@/hooks/queries/admin/settings", () => ({
  useCredentialProviderFallback: mocks.useCredentialProviderFallback,
  useUpdateCredentialProviderFallback: mocks.useUpdateCredentialProviderFallback,
}));

describe("CredentialProviderFallbackSettings", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.useAuth.mockReturnValue({
      providers: [
        { id: "local", display_name: "Local account", mode: "credentials", default: true },
        { id: "ldap", display_name: "Company directory", mode: "credentials", default: false },
        { id: "oidc", display_name: "Company SSO", mode: "oauth", default: false },
      ],
    });
    mocks.useCredentialProviderFallback.mockReturnValue({
      data: { provider_ids: ["local", "ldap"] },
      isLoading: false,
      isError: false,
    });
    mocks.mutateAsync.mockResolvedValue({ provider_ids: ["ldap", "local"] });
    mocks.useUpdateCredentialProviderFallback.mockReturnValue({
      mutateAsync: mocks.mutateAsync,
      isPending: false,
    });
  });

  afterEach(() => {
    cleanup();
  });

  it("shows loading and API errors without exposing provider internals", () => {
    mocks.useCredentialProviderFallback.mockReturnValue({ isLoading: true });
    const loading = render(<CredentialProviderFallbackSettings />);
    expect(screen.getByRole("status")).toHaveTextContent("Loading credential provider order");
    loading.unmount();

    mocks.useCredentialProviderFallback.mockReturnValue({ isLoading: false, isError: true });
    render(<CredentialProviderFallbackSettings />);
    expect(screen.getByRole("alert")).toHaveTextContent("Unable to load");
    expect(screen.queryByText("ldap")).not.toBeInTheDocument();
  });

  it("reorders providers with keyboard-accessible controls and saves the order", async () => {
    const user = userEvent.setup();
    render(<CredentialProviderFallbackSettings />);

    const moveDown = screen.getByRole("button", { name: "Move Local account down" });
    expect(moveDown).toBeEnabled();
    await user.click(moveDown);
    expect(screen.getByRole("button", { name: "Move Local account up" })).toBeEnabled();

    await user.click(screen.getByRole("button", { name: "Save provider order" }));
    expect(mocks.mutateAsync).toHaveBeenCalledWith({ provider_ids: ["ldap", "local"] });
  });

  it("restores automatic mode and saves an empty policy", async () => {
    const user = userEvent.setup();
    render(<CredentialProviderFallbackSettings />);

    await user.click(screen.getByRole("button", { name: "Use automatic server policy" }));
    await user.click(screen.getByRole("button", { name: "Save provider order" }));

    expect(mocks.mutateAsync).toHaveBeenCalledWith({ provider_ids: [] });
    expect(screen.getByRole("status")).toHaveTextContent("Automatic server policy");
  });
});
