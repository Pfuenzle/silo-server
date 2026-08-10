import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { storage } from "@/utils/storage";
import OAuthComplete from "./OAuthComplete";

const apiMock = vi.hoisted(() => vi.fn());
const completeLoginMock = vi.hoisted(() => vi.fn());
const navigateMock = vi.hoisted(() => vi.fn());
const resetAuthMock = vi.hoisted(() => vi.fn());
const setAccessTokenMock = vi.hoisted(() => vi.fn());
const setRefreshTokenMock = vi.hoisted(() => vi.fn());

vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return { ...actual, useNavigate: () => navigateMock };
});

vi.mock("@/api/client", () => ({
  api: apiMock,
  setAccessToken: setAccessTokenMock,
  setRefreshToken: setRefreshTokenMock,
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ completeLogin: completeLoginMock, resetAuth: resetAuthMock }),
}));

vi.mock("@/components/auth/AuthBackground", () => ({ AuthBackground: () => null }));

function completionResponse(next = "/") {
  return new Response(
    JSON.stringify({
      access_token: "partial-access-token",
      refresh_token: "partial-refresh-token",
      expires_in: 3600,
      next,
    }),
    { status: 200, headers: { "Content-Type": "application/json" } },
  );
}

function renderCompletion(search: string) {
  window.history.replaceState(null, "", `/login/oauth-complete${search}`);
  return render(
    <MemoryRouter initialEntries={[`/login/oauth-complete${search}`]}>
      <OAuthComplete />
    </MemoryRouter>,
  );
}

async function expectGenericFailure(upstreamText: string) {
  const alert = await screen.findByRole("alert");
  expect(alert).toHaveTextContent("We couldn't sign you in");
  expect(alert).toHaveTextContent("Return to sign in and try again");
  expect(alert).not.toHaveTextContent(upstreamText);
  expect(document.body).not.toHaveTextContent("partial-access-token");
  expect(document.body).not.toHaveTextContent("partial-refresh-token");
  expect(screen.getByRole("button", { name: "Try again" })).toHaveFocus();
}

describe("OAuthComplete failure", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    Object.values(storage.KEYS).forEach((key) => storage.remove(key));
    for (const key of [
      storage.KEYS.ACCESS_TOKEN,
      storage.KEYS.REFRESH_TOKEN,
      storage.KEYS.PROFILE_ID,
      storage.KEYS.PROFILE_TOKEN,
      storage.KEYS.CURRENT_PROFILE,
    ]) {
      storage.set(key, `stale-${key}`);
    }
    resetAuthMock.mockImplementation(() => {
      setAccessTokenMock(null);
      setRefreshTokenMock(null);
      storage.remove(storage.KEYS.ACCESS_TOKEN);
      storage.remove(storage.KEYS.REFRESH_TOKEN);
      storage.remove(storage.KEYS.PROFILE_ID);
      storage.remove(storage.KEYS.PROFILE_TOKEN);
      storage.remove(storage.KEYS.CURRENT_PROFILE);
    });
  });

  it("shows a generic retry for an expired completion code and removes it from history", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response('{"error":"completion_expired","message":"code 8d7-secret expired"}', {
          status: 410,
        }),
      ),
    );

    const user = userEvent.setup();
    renderCompletion("?code=8d7-secret&next=%2Fprofiles");

    await expectGenericFailure("completion_expired");
    expect(window.location.search).toBe("");
    expect(window.location.href).not.toContain("8d7-secret");
    expect(resetAuthMock).toHaveBeenCalledTimes(1);

    await user.click(screen.getByRole("button", { name: "Try again" }));

    expect(navigateMock).toHaveBeenCalledWith("/login?redirect=%2Fprofiles", { replace: true });
  });

  it("shows the same generic retry for a network failure", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockRejectedValue(new Error("network upstream oauth.exchange_failed at 10.0.0.8")),
    );

    renderCompletion("?code=network-code");

    await expectGenericFailure("oauth.exchange_failed");
    expect(document.body).not.toHaveTextContent("10.0.0.8");
  });

  it("clears partial auth state and preserves safe next when loading the user fails", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(completionResponse("/library?tab=new")));
    apiMock.mockRejectedValue(new Error("/auth/me failed: signature_invalid token=backend-token"));
    const user = userEvent.setup();

    renderCompletion("?code=me-code");

    await expectGenericFailure("signature_invalid");
    expect(document.body).not.toHaveTextContent("backend-token");
    expect(setAccessTokenMock).toHaveBeenCalledWith("partial-access-token");
    expect(setRefreshTokenMock).toHaveBeenCalledWith("partial-refresh-token");
    expect(setAccessTokenMock).toHaveBeenLastCalledWith(null);
    expect(setRefreshTokenMock).toHaveBeenLastCalledWith(null);
    for (const key of [
      storage.KEYS.ACCESS_TOKEN,
      storage.KEYS.REFRESH_TOKEN,
      storage.KEYS.PROFILE_ID,
      storage.KEYS.PROFILE_TOKEN,
      storage.KEYS.CURRENT_PROFILE,
    ]) {
      expect(storage.get(key)).toBeNull();
    }

    const retry = screen.getByRole("button", { name: "Try again" });
    await user.dblClick(retry);

    expect(navigateMock).toHaveBeenCalledTimes(1);
    expect(navigateMock).toHaveBeenCalledWith("/login?redirect=%2Flibrary%3Ftab%3Dnew", {
      replace: true,
    });
    await waitFor(() => expect(retry).toBeDisabled());
  });

  it("clears persisted tokens when the completion page unmounts before loading the user", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(completionResponse("/library")));
    apiMock.mockReturnValue(new Promise(() => {}));

    const view = renderCompletion("?code=slow-me-code");
    await waitFor(() => expect(setAccessTokenMock).toHaveBeenCalledWith("partial-access-token"));

    view.unmount();

    expect(resetAuthMock).toHaveBeenCalledTimes(1);
  });
});
