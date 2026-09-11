import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { useProfiles } from "./profiles";

const apiMock = vi.hoisted(() => vi.fn().mockResolvedValue({ profiles: [] }));
const authMock = vi.hoisted(() => vi.fn());

vi.mock("@/api/client", () => ({ api: apiMock }));
vi.mock("@/hooks/useAuth", () => ({ useOptionalAuth: authMock }));

function wrapper({ children }: { children: ReactNode }) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
}

afterEach(() => {
  cleanup();
  apiMock.mockClear();
  authMock.mockReset();
});

describe("useProfiles", () => {
  it.each([
    ["outside AuthProvider", null],
    ["while auth is loading", { loading: true, user: null }],
    ["without an authenticated user", { loading: false, user: null }],
  ])("does not request profiles %s", (_state, authState) => {
    authMock.mockReturnValue(authState);

    renderHook(() => useProfiles(), { wrapper });

    expect(apiMock).not.toHaveBeenCalled();
  });

  it("requests profiles after auth transitions from loading to an authenticated user", async () => {
    authMock.mockReturnValue({ loading: true, user: null });

    const hook = renderHook(() => useProfiles(), { wrapper });
    expect(apiMock).not.toHaveBeenCalled();

    authMock.mockReturnValue({ loading: false, user: { id: 1 } });
    hook.rerender();
    await waitFor(() => expect(apiMock).toHaveBeenCalledWith("/profiles"));
  });
});
