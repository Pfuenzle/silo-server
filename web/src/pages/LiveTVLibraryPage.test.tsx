// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

(
  globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

const mocks = vi.hoisted(() => ({
  searchParams: new URLSearchParams("tab=favorites&view=grid"),
  setSearchParams: vi.fn(),
  playback: vi.fn(),
  toastError: vi.fn(),
  guide: {
    data: undefined as { items: readonly never[]; stale: boolean } | undefined,
    isLoading: false,
    isError: false,
    refetch: vi.fn(),
  },
}));

vi.mock("react-router", () => ({
  useSearchParams: () => [mocks.searchParams, mocks.setSearchParams],
}));
vi.mock("sonner", () => ({ toast: { error: mocks.toastError } }));
vi.mock("@/hooks/useCurrentProfile", () => ({ useCurrentProfile: () => ({ profile: null }) }));
vi.mock("@/hooks/queries/livetv", () => ({
  resolveLiveTVPlayback: (...args: unknown[]) => mocks.playback(...args),
  useLiveTVChannels: () => ({
    data: { items: [{ id: "source|one", name: "One" }] },
    isLoading: false,
    isError: false,
  }),
  useLiveTVGuide: () => mocks.guide,
  useLiveTVFavorites: () => ({ data: [] }),
  useToggleLiveTVFavorite: () => ({ mutate: vi.fn() }),
}));
vi.mock("@/components/LibraryHeader", () => ({
  default: ({
    availableTabs,
    tabLabels,
  }: {
    availableTabs: readonly string[];
    tabLabels: Record<string, string>;
  }) => (
    <nav>
      {availableTabs.map((value) => (
        <button key={value} type="button">
          {tabLabels[value]}
        </button>
      ))}
    </nav>
  ),
}));
vi.mock("@/components/ui/tabs", () => ({
  Tabs: ({ children }: { children: ReactNode }) => <div>{children}</div>,
}));
vi.mock("@/player/components/LiveTVPlayer", () => ({
  LiveTVPlayer: () => <div data-testid="player" />,
}));

import { LiveTVLibraryPage } from "./LiveTVLibraryPage";

describe("LiveTVLibraryPage", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    mocks.searchParams = new URLSearchParams("tab=favorites&view=grid");
    mocks.setSearchParams.mockReset();
    mocks.playback.mockReset();
    mocks.toastError.mockReset();
    mocks.guide = {
      data: { items: [], stale: false },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    };
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
  });

  async function renderPage() {
    await act(async () => {
      root.render(<LiveTVLibraryPage libraryId={7} libraryName="News" />);
    });
  }

  it("preserves the selected tab when a view update races with a URL remount", async () => {
    await renderPage();
    const listButton = container.querySelector<HTMLButtonElement>('button[aria-label="List view"]');
    expect(listButton).not.toBeNull();

    await act(async () => listButton?.click());

    const updater = mocks.setSearchParams.mock.calls[0]?.[0] as (
      current: URLSearchParams,
    ) => URLSearchParams;
    const next = updater(new URLSearchParams("tab=program&view=grid"));
    expect(next.toString()).toBe("tab=program&view=list");
  });

  it("shows a localized error when playback is unavailable", async () => {
    mocks.playback.mockResolvedValue({ playable: false, error_code: "source_unavailable" });
    mocks.searchParams = new URLSearchParams("tab=channels&view=grid");
    await renderPage();
    const watchButton = Array.from(container.querySelectorAll("button")).find(
      (button) => button.textContent === "Watch live",
    );
    expect(watchButton).not.toBeUndefined();

    await act(async () => watchButton?.click());

    expect(mocks.toastError).toHaveBeenCalledWith("This channel is currently unavailable.");
    expect(container.querySelector('[data-testid="player"]')).toBeNull();
  });

  it("opens the player when playback returns a playable grant", async () => {
    mocks.searchParams = new URLSearchParams("tab=channels&view=grid");
    mocks.playback.mockResolvedValue({
      playable: true,
      url: "/api/v1/stream/live/grant-1/manifest",
      grant_id: "grant-1",
    });
    await renderPage();
    const watchButton = Array.from(container.querySelectorAll("button")).find(
      (button) => button.textContent === "Watch live",
    );

    await act(async () => {
      watchButton?.click();
      await Promise.resolve();
    });

    expect(container.querySelector('[data-testid="player"]')).not.toBeNull();
    expect(mocks.toastError).not.toHaveBeenCalled();
  });

  it("shows the localized error when playback resolution is rejected", async () => {
    mocks.searchParams = new URLSearchParams("tab=channels&view=grid");
    mocks.playback.mockRejectedValue(new Error("request failed"));
    await renderPage();
    const watchButton = Array.from(container.querySelectorAll("button")).find(
      (button) => button.textContent === "Watch live",
    );

    await act(async () => {
      watchButton?.click();
      await Promise.resolve();
    });

    expect(mocks.toastError).toHaveBeenCalledWith("Live playback could not be started.");
    expect(container.querySelector('[data-testid="player"]')).toBeNull();
  });

  it("shows a guide loading state", async () => {
    mocks.searchParams = new URLSearchParams("tab=program&view=grid");
    mocks.guide = { data: undefined, isLoading: true, isError: false, refetch: vi.fn() };
    await renderPage();

    expect(container.querySelector('[data-testid="live-tv-guide-loading"]')).not.toBeNull();
  });

  it("shows a guide error with retry instead of an empty guide", async () => {
    mocks.searchParams = new URLSearchParams("tab=program&view=grid");
    const refetch = vi.fn();
    mocks.guide = { data: undefined, isLoading: false, isError: true, refetch };
    await renderPage();

    const retry = Array.from(container.querySelectorAll("button")).find(
      (button) => button.textContent === "Try again",
    );
    expect(retry).not.toBeUndefined();
    await act(async () => retry?.click());
    expect(refetch).toHaveBeenCalledTimes(1);
    expect(container.querySelector('[data-testid="live-tv-guide-error"]')).not.toBeNull();
  });

  it("shows the localized no-programmes state for an empty guide", async () => {
    mocks.searchParams = new URLSearchParams("tab=program&view=grid");
    await renderPage();

    expect(container.textContent).toContain("No programmes in this window.");
  });
});
