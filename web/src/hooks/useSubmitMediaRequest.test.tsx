import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { RequestMediaResult } from "@/api/types";

const mocks = vi.hoisted(() => ({
  create: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
}));

vi.mock("@/api/v2/requests", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/v2/requests")>()),
  createMediaRequestV2: (...args: unknown[]) => mocks.create(...args),
}));
vi.mock("sonner", () => ({
  toast: { error: mocks.toastError, success: mocks.toastSuccess },
}));

import { useSubmitMediaRequest } from "./useSubmitMediaRequest";

function result(tmdbID: number, title: string): RequestMediaResult {
  return {
    media_type: "movie",
    tmdb_id: tmdbID,
    title,
    availability: "missing",
    request: { requestable: true },
  };
}

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

describe("useSubmitMediaRequest", () => {
  beforeEach(() => {
    mocks.create.mockReset();
    mocks.toastError.mockReset();
    mocks.toastSuccess.mockReset();
  });

  it("clears each card's pending state when overlapping requests both fail", async () => {
    const rejects: Array<(error: Error) => void> = [];
    mocks.create.mockImplementation(() => new Promise((_resolve, reject) => rejects.push(reject)));
    const heat = result(1, "Heat");
    const ronin = result(2, "Ronin");
    const { result: hook } = renderHook(() => useSubmitMediaRequest(), { wrapper });

    act(() => {
      hook.current.submit(heat);
      hook.current.submit(ronin);
    });
    await waitFor(() => expect(rejects).toHaveLength(2));
    expect(hook.current.isSubmitting(heat)).toBe(true);
    expect(hook.current.isSubmitting(ronin)).toBe(true);

    // The first call fails while the second is still in flight.
    await act(async () => rejects[0]!(new Error("quota reached")));
    await waitFor(() => expect(hook.current.isSubmitting(heat)).toBe(false));
    expect(hook.current.isSubmitting(ronin)).toBe(true);

    await act(async () => rejects[1]!(new Error("quota reached")));
    await waitFor(() => expect(hook.current.isSubmitting(ronin)).toBe(false));

    // The shared mutation still reports every failure.
    expect(mocks.toastError).toHaveBeenCalledTimes(2);
  });

  it("submits audiobook editions independently using their provider identities", async () => {
    const resolves: Array<(value: { id: string }) => void> = [];
    mocks.create.mockImplementation(() => {
      return new Promise<{ id: string }>((resolve) => resolves.push(resolve));
    });
    const first: RequestMediaResult = {
      ...result(0, "Dune"),
      media_type: "audiobook",
      provider: "audiobook-metadata",
      provider_item_id: "edition-1",
    };
    const second = { ...first, provider_item_id: "edition-2" };
    const { result: hook } = renderHook(() => useSubmitMediaRequest(), { wrapper });

    act(() => hook.current.submit(first));
    expect(hook.current.isSubmitting(first)).toBe(true);
    expect(hook.current.isSubmitting(second)).toBe(false);
    act(() => hook.current.submit(second));
    await waitFor(() => expect(resolves).toHaveLength(2));
    expect(mocks.create).toHaveBeenNthCalledWith(
      1,
      expect.objectContaining({ provider: "audiobook-metadata", provider_item_id: "edition-1" }),
    );
    expect(mocks.create.mock.calls[0]![0]).not.toHaveProperty("tmdb_id");

    await act(async () => resolves[0]!({ id: "request-1" }));
    await waitFor(() => expect(hook.current.isSubmitting(first)).toBe(false));
    expect(hook.current.isSubmitting(second)).toBe(true);
    await act(async () => resolves[1]!({ id: "request-2" }));
    await waitFor(() => expect(hook.current.isSubmitting(second)).toBe(false));
  });

  it("clears a card once its request succeeds", async () => {
    mocks.create.mockResolvedValue({ id: "r1" });
    const heat = result(1, "Heat");
    const { result: hook } = renderHook(() => useSubmitMediaRequest(), { wrapper });

    act(() => hook.current.submit(heat));
    expect(hook.current.isSubmitting(heat)).toBe(true);

    await waitFor(() => expect(hook.current.isSubmitting(heat)).toBe(false));
    expect(mocks.create).toHaveBeenCalledWith(
      expect.objectContaining({ media_type: "movie", tmdb_id: 1, title: "Heat" }),
    );
    expect(mocks.toastSuccess).toHaveBeenCalledOnce();
  });
});
