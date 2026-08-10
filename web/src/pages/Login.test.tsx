import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import Login from "./Login";

const navigateMock = vi.hoisted(() => vi.fn());
const useAuthMock = vi.hoisted(() => vi.fn());
const apiMock = vi.hoisted(() => vi.fn());

vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return { ...actual, useNavigate: () => navigateMock };
});

vi.mock("@/hooks/useAuth", () => ({
  getBootstrapProfile: vi.fn(),
  useAuth: useAuthMock,
}));
vi.mock("@/api/client", () => ({ api: apiMock }));

vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: vi.fn() }));
vi.mock("@/hooks/useServerBranding", () => ({
  useServerBranding: () => ({ serverName: "Silo", loginSubtitle: "Your media, your server." }),
}));
vi.mock("@/components/auth/AuthBackground", () => ({ AuthBackground: () => null }));

afterEach(() => {
  cleanup();
});

Object.defineProperties(Element.prototype, {
  hasPointerCapture: { configurable: true, value: () => false },
  setPointerCapture: { configurable: true, value: () => undefined },
  releasePointerCapture: { configurable: true, value: () => undefined },
  scrollIntoView: { configurable: true, value: vi.fn() },
});

const FAILURE_REASONS = [
  "state_invalid",
  "nonce_mismatch: provider nonce rejected",
  "signature_invalid: signed state was tampered",
  "signing_key_unavailable: kid=private-key-42",
  "exchange_failed: upstream token=secret-token",
] as const;

function renderLogin(entry: string) {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Login />
    </MemoryRouter>,
  );
}

describe("Login OAuth failure", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useAuthMock.mockReturnValue({
      login: vi.fn(),
      completeLogin: vi.fn(),
      profile: null,
      selectProfile: vi.fn(),
      user: null,
      loading: false,
      setupLoading: false,
      setupRequired: false,
      providers: [
        {
          id: "plugin:41:oidc",
          display_name: "Company SSO",
          mode: "oauth",
          default: false,
          installation_id: 41,
        },
      ],
    });
  });

  it.each(FAILURE_REASONS)(
    "shows the same actionable message without exposing the %s failure",
    async (reason) => {
      renderLogin(
        `/login?error=oauth_failed&reason=${encodeURIComponent(reason)}&next=${encodeURIComponent("/library?tab=new")}`,
      );

      const alert = screen.getByRole("alert");
      expect(alert).toHaveTextContent("We couldn't sign you in");
      expect(alert).toHaveTextContent("Try again");
      expect(alert).not.toHaveTextContent(reason);
      expect(document.body).not.toHaveTextContent("secret-token");
      expect(document.body).not.toHaveTextContent("private-key-42");
      expect(screen.getByRole("button", { name: "Try again" })).toHaveFocus();
    },
  );

  it("returns to provider selection once, preserving only a safe same-origin next path", async () => {
    const user = userEvent.setup();
    renderLogin(
      `/login?error=oauth_failed&reason=exchange_failed&next=${encodeURIComponent("/library?tab=new")}`,
    );

    const retry = screen.getByRole("button", { name: "Try again" });
    await user.dblClick(retry);

    expect(navigateMock).toHaveBeenCalledTimes(1);
    expect(navigateMock).toHaveBeenCalledWith("/login?redirect=%2Flibrary%3Ftab%3Dnew", {
      replace: true,
    });
    expect(retry).toBeDisabled();
    expect(retry).toHaveTextContent("Returning to sign in...");
  });

  it("activates retry from the keyboard", async () => {
    const user = userEvent.setup();
    renderLogin("/login?error=oauth_failed&reason=exchange_failed");

    await user.keyboard("{Enter}");

    expect(navigateMock).toHaveBeenCalledWith("/login", { replace: true });
  });

  it("removes OAuth failure details from browser history", async () => {
    const replaceState = vi.spyOn(window.history, "replaceState");
    renderLogin("/login?error=oauth_failed&reason=exchange_failed");

    await waitFor(() =>
      expect(replaceState).toHaveBeenCalledWith(null, "", window.location.pathname),
    );
  });

  it("drops an unsafe next target when retrying", async () => {
    const user = userEvent.setup();
    renderLogin(
      `/login?error=oauth_failed&reason=state_invalid&next=${encodeURIComponent("//evil.example/steal")}`,
    );

    await user.click(screen.getByRole("button", { name: "Try again" }));

    expect(navigateMock).toHaveBeenCalledWith("/login", { replace: true });
  });

  it("keeps a valid redirect when a conflicting next target is unsafe", async () => {
    const user = userEvent.setup();
    renderLogin(
      `/login?error=oauth_failed&reason=state_invalid&redirect=${encodeURIComponent("/library")}&next=${encodeURIComponent("/\\evil.example")}`,
    );

    await user.click(screen.getByRole("button", { name: "Try again" }));

    expect(navigateMock).toHaveBeenCalledWith("/login?redirect=%2Flibrary", { replace: true });
  });

  it("sends the exact safe retry target when starting the next provider attempt", () => {
    renderLogin("/login?redirect=%2Fprofiles");

    const provider = screen.getByRole("button", { name: "Company SSO" });
    const form = provider.closest("form");

    expect(form).not.toBeNull();
    expect(form).toHaveAttribute("method", "post");
    expect(form).toHaveAttribute("action", "/api/v1/auth/oauth/41/init?next=%2Fprofiles");
    expect(form?.getAttribute("action")).not.toMatch(/(?:code|state|redirect)=/);
  });
});

describe("Login initial focus", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("focuses the first external provider after providers load", () => {
    useAuthMock.mockReturnValue({
      login: vi.fn(),
      completeLogin: vi.fn(),
      profile: null,
      selectProfile: vi.fn(),
      user: null,
      loading: false,
      setupLoading: false,
      setupRequired: false,
      providers: [
        {
          id: "plugin:41:oidc",
          display_name: "Company SSO",
          mode: "oauth",
          default: false,
          installation_id: 41,
        },
        {
          id: "local",
          display_name: "Local account",
          mode: "credentials",
          default: true,
        },
      ],
    });

    renderLogin("/login");

    expect(screen.getByRole("button", { name: "Company SSO" })).toHaveFocus();
  });

  it("focuses username when provider loading finishes without an external provider", async () => {
    const authState = {
      login: vi.fn(),
      completeLogin: vi.fn(),
      profile: null,
      selectProfile: vi.fn(),
      user: null,
      loading: false,
      setupLoading: true,
      setupRequired: false,
      providers: [
        {
          id: "local",
          display_name: "Local account",
          mode: "credentials",
          default: true,
        },
      ],
    };
    useAuthMock.mockReturnValue(authState);

    const view = renderLogin("/login");
    useAuthMock.mockReturnValue({ ...authState, setupLoading: false });
    view.rerender(
      <MemoryRouter initialEntries={["/login"]}>
        <Login />
      </MemoryRouter>,
    );

    await waitFor(() => expect(screen.getByLabelText("Username")).toHaveFocus());
  });
});

describe("Login credential provider selection", () => {
  const loginMock = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    apiMock.mockResolvedValue({ profiles: [] });
    useAuthMock.mockReturnValue({
      login: loginMock,
      completeLogin: vi.fn(),
      profile: null,
      selectProfile: vi.fn(),
      user: null,
      loading: false,
      setupLoading: false,
      setupRequired: false,
      providers: [
        { id: "local", display_name: "Local account", mode: "credentials", default: true },
        { id: "ldap", display_name: "Company directory", mode: "credentials", default: false },
        {
          id: "oidc",
          display_name: "Company SSO",
          mode: "oauth",
          default: false,
          installation_id: 41,
        },
      ],
    });
  });

  it("hides the credential selector and omits provider by default", async () => {
    const user = userEvent.setup();
    renderLogin("/login");

    expect(screen.getByRole("group")).not.toHaveAttribute("open");
    expect(screen.getByRole("combobox", { name: "Credential provider" })).not.toBeVisible();
    await user.type(screen.getByLabelText("Username"), "alice");
    await user.type(screen.getByLabelText("Password"), "secret");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    await waitFor(() => expect(loginMock).toHaveBeenCalledWith("alice", "secret", undefined));
  });

  it("reveals an accessible selector, sends an explicit override, and resets to automatic", async () => {
    const user = userEvent.setup();
    renderLogin("/login");

    await user.click(screen.getByText("Use a different sign-in method"));
    expect(screen.getByRole("combobox", { name: "Credential provider" })).toBeVisible();
    const selector = screen.getByRole("combobox", { name: "Credential provider" });
    selector.focus();
    await user.keyboard("{Enter}");
    await user.click(screen.getByRole("option", { name: "Company directory" }));
    expect(screen.getByRole("button", { name: "Reset to automatic" })).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Reset to automatic" }));

    await user.type(screen.getByLabelText("Username"), "alice");
    await user.type(screen.getByLabelText("Password"), "secret");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    await waitFor(() => expect(loginMock).toHaveBeenCalledWith("alice", "secret", undefined));
  });

  it("keeps OAuth providers in their separate flow", () => {
    renderLogin("/login");

    const oauthButton = screen.getByRole("button", { name: "Company SSO" });
    expect(oauthButton.closest("form")).toHaveAttribute("action", "/api/v1/auth/oauth/41/init");
    expect(screen.queryByRole("option", { name: "Company SSO" })).not.toBeInTheDocument();
  });

  it("shows a safe login error when the server rejects credentials", async () => {
    loginMock.mockRejectedValue(new Error("Invalid credentials"));
    const user = userEvent.setup();
    renderLogin("/login");

    await user.type(screen.getByLabelText("Username"), "alice");
    await user.type(screen.getByLabelText("Password"), "secret");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    await waitFor(() => expect(loginMock).toHaveBeenCalled());
  });
});
