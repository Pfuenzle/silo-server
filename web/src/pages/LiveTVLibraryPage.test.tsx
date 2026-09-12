// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LiveTVProgramme } from "@/api/livetv";

(
  globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

const mocks = vi.hoisted(() => ({
  searchParams: new URLSearchParams("tab=favorites&view=grid"),
  setSearchParams: vi.fn(),
  playback: vi.fn(),
  toastError: vi.fn(),
  toggleChannel: vi.fn(),
  channels: [
    { id: "source|one", name: "One", category: "News", artwork: { url: "/api/artwork/one" } },
  ],
  favoriteChannels: [] as readonly { id: string }[],
  favoriteProgrammes: [] as readonly LiveTVProgramme[],
  guide: {
    data: undefined as { items: readonly LiveTVProgramme[]; stale: boolean } | undefined,
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
    data: { items: mocks.channels },
    isLoading: false,
    isError: false,
  }),
  useLiveTVGuide: () => mocks.guide,
  useLiveTVFavorites: (_libraryId: number, kind: string) => ({
    data: kind === "programmes" ? mocks.favoriteProgrammes : mocks.favoriteChannels,
  }),
  useToggleLiveTVFavorite: (_libraryId: number, kind: string) => ({
    mutate: kind === "channels" ? mocks.toggleChannel : vi.fn(),
  }),
}));
vi.mock("@/components/LibraryHeader", () => ({
  default: ({
    availableTabs,
    tabLabels,
    onValueChange,
  }: {
    availableTabs: readonly string[];
    tabLabels: Record<string, string>;
    onValueChange?: (value: string) => void;
  }) => (
    <nav>
      {availableTabs.map((value) => (
        <button key={value} type="button" onClick={() => onValueChange?.(value)}>
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
    mocks.toggleChannel.mockReset();
    mocks.channels = [
      { id: "source|one", name: "One", category: "News", artwork: { url: "/api/artwork/one" } },
    ];
    mocks.favoriteProgrammes = [];
    mocks.favoriteChannels = [];
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

    expect(mocks.setSearchParams).toHaveBeenCalledWith(
      new URLSearchParams("tab=favorites&view=list"),
      { replace: true },
    );
  });

  it("renders a favorite programme in the Favorites tab", async () => {
    const programme = {
      id: "programme-1",
      channel_id: "source|one",
      channel_name: "One",
      title: "Morning News",
      description: "The latest headlines.",
      starts_at: new Date(Date.now() - 60_000).toISOString(),
      ends_at: new Date(Date.now() + 60_000).toISOString(),
      artwork: { url: "/api/artwork/programme" },
      rating: 4.5,
    } satisfies LiveTVProgramme;
    mocks.favoriteProgrammes = [programme];
    mocks.guide = {
      data: { items: [programme], stale: false },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    };
    await renderPage();

    expect(container.textContent).toContain("Morning News");
  });

  it("renders the programme channel name instead of its stable ID in the guide", async () => {
    mocks.searchParams = new URLSearchParams("tab=program&view=grid");
    const programme = {
      id: "programme-nikola",
      channel_id: "TS|86a-stable-channel-id",
      channel_name: "Nikola",
      title: "Evening News",
      starts_at: new Date(Date.now() - 60_000).toISOString(),
      ends_at: new Date(Date.now() + 60_000).toISOString(),
    } satisfies LiveTVProgramme;
    mocks.guide = {
      data: { items: [programme], stale: false },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    };

    await renderPage();

    expect(container.querySelector('[data-testid="live-tv-guide"]')?.textContent).toContain("Nikola");
    expect(container.querySelector('[data-testid="live-tv-guide"]')?.textContent).not.toContain(
      "TS|86a-stable-channel-id",
    );
  });

  it("keeps the selected tab visible when favorite data invalidates and rerenders", async () => {
    mocks.searchParams = new URLSearchParams("tab=favorites&view=grid");
    mocks.favoriteChannels = [{ id: "source|one" }];
    await renderPage();

    const favoriteButton = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Remove One from favorites"]',
    );
    await act(async () => favoriteButton?.click());
    mocks.favoriteChannels = [];
    await renderPage();

    expect(container.querySelector('[data-testid="live-tv-library"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="live-tv-empty"]')).toBeNull();
    expect(mocks.toggleChannel).toHaveBeenCalledWith({ id: "source|one", favorite: false });
    expect(mocks.setSearchParams).not.toHaveBeenCalled();
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
    expect(container.querySelector('[role="alert"]')?.textContent).toContain(
      "This channel is currently unavailable.",
    );
    expect(
      Array.from(container.querySelectorAll("button")).some(
        (button) => button.textContent === "Try again",
      ),
    ).toBe(true);
    expect(container.querySelector('[data-testid="player"]')).toBeNull();
  });

  it("opens the shared detail popup from an All Channels card", async () => {
    mocks.searchParams = new URLSearchParams("tab=channels&view=grid");
    mocks.guide = {
      data: {
        items: [
          {
            id: "programme-1",
            channel_id: "source|one",
            channel_name: "One",
            title: "Morning News",
            description: "The latest headlines.",
            starts_at: new Date(Date.now() - 60_000).toISOString(),
            ends_at: new Date(Date.now() + 60_000).toISOString(),
            artwork: { url: "/api/artwork/programme" },
            rating: 4.5,
          },
        ],
        stale: false,
      },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    };
    await renderPage();

    await act(async () =>
      container
        .querySelector('[data-testid="live-tv-channel-source|one"]')
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true })),
    );

    expect(document.body.querySelector('[role="dialog"]')?.textContent).toContain("Morning News");
    expect(document.body.querySelector('[role="dialog"]')?.textContent).toContain("One");
  });

  it("opens the same shared detail popup from a programme card", async () => {
    mocks.searchParams = new URLSearchParams("tab=program&view=grid");
    mocks.guide = {
      data: {
        items: [
          {
            id: "programme-1",
            channel_id: "source|one",
            channel_name: "One",
            title: "Morning News",
            description: "The latest headlines.",
            starts_at: new Date(Date.now() - 60_000).toISOString(),
            ends_at: new Date(Date.now() + 60_000).toISOString(),
            artwork: { url: "/api/artwork/programme" },
            rating: 4.5,
          },
        ],
        stale: false,
      },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    };
    await renderPage();

    await act(async () =>
      container
        .querySelector('[data-testid="live-tv-programme-programme-1"]')
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true })),
    );

    expect(document.body.querySelector('[role="dialog"]')?.textContent).toContain("Morning News");
  });

  it("shows the current programme in the channel card and no duplicate rail in All Channels", async () => {
    mocks.searchParams = new URLSearchParams("tab=channels&view=grid");
    mocks.guide = {
      data: {
        items: [
          {
            id: "programme-1",
            channel_id: "source|one",
            channel_name: "One",
            title: "Morning News",
            description: "The latest headlines.",
            starts_at: new Date(Date.now() - 60_000).toISOString(),
            ends_at: new Date(Date.now() + 60_000).toISOString(),
            artwork: { url: "/api/artwork/programme" },
            rating: 4.5,
          },
        ],
        stale: false,
      },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    };
    await renderPage();

    expect(
      container.querySelector('[data-testid="live-tv-channel-source|one"]')?.textContent,
    ).toContain("Morning News");
    expect(container.querySelector('[data-testid="live-tv-programme-row"]')).toBeNull();
  });

  it("starts playback from the shared popup Watch Channel action", async () => {
    mocks.searchParams = new URLSearchParams("tab=channels&view=grid");
    mocks.playback.mockResolvedValue({
      playable: true,
      url: "/api/v1/stream/live/grant-1/manifest",
      grant_id: "grant-1",
    });
    await renderPage();

    await act(async () =>
      container
        .querySelector('[data-testid="live-tv-channel-source|one"]')
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true })),
    );
    const watch = Array.from(document.body.querySelectorAll("button")).find(
      (button) => button.textContent === "Watch Channel",
    );
    await act(async () => watch?.click());

    expect(mocks.playback).toHaveBeenCalledWith(7, "source|one");
    expect(container.querySelector('[data-testid="player"]')).not.toBeNull();
  });

  it("keeps the popup open and reports a Watch Channel failure", async () => {
    mocks.searchParams = new URLSearchParams("tab=channels&view=grid");
    mocks.playback.mockResolvedValue({ playable: false, error_code: "source_unavailable" });
    await renderPage();

    await act(async () =>
      container
        .querySelector('[data-testid="live-tv-channel-source|one"]')
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true })),
    );
    const watch = Array.from(document.body.querySelectorAll("button")).find(
      (button) => button.textContent === "Watch Channel",
    );
    await act(async () => watch?.click());

    expect(document.body.querySelector('[role="dialog"]')).not.toBeNull();
    expect(container.querySelector('[role="alert"]')?.textContent).toContain(
      "This channel is currently unavailable.",
    );
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
    expect(container.querySelector('[role="alert"]')).not.toBeNull();
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
