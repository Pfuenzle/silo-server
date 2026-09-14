// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { WatchPlaybackControllerContext } from "@/playback/watchPlaybackContext";
import type { WatchPlaybackControllerValue } from "@/playback/watchPlaybackContext";
import { createEmptyPlaybackState } from "@/playback/watchPlaybackReducer";
import WatchRoute from "./WatchRoute";
import LiveTVWatchRoute from "./LiveTVWatchRoute";

describe("WatchRoute", () => {
  let container: HTMLDivElement;
  let root: Root;
  let controller: WatchPlaybackControllerValue;

  beforeEach(() => {
    (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT = true;

    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);

    controller = {
      state: createEmptyPlaybackState(),
      hasDetachedPlayback: false,
      isBackgroundBarVisible: false,
      startPlayback: vi.fn(),
      minimizePlayback: vi.fn(),
      exitPlayback: vi.fn(),
      stopPlayback: vi.fn(),
      enterPostRoll: vi.fn(),
      returnToWatch: vi.fn(),
      syncRouteRequest: vi.fn(),
      handleRouteExit: vi.fn(),
      setPictureInPictureActive: vi.fn(),
      clearPendingReturnNavigation: vi.fn(),
      updatePlaybackSnapshot: vi.fn(),
      setTransportControls: vi.fn(),
    };
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
  });

  it("syncs the parsed watch request and reports the request key on unmount", async () => {
    await act(async () => {
      root.render(
        <WatchPlaybackControllerContext.Provider value={controller}>
          <MemoryRouter initialEntries={["/watch/movie-1?libraryId=7&restart=1"]}>
            <Routes>
              <Route path="/watch/:id" element={<WatchRoute />} />
            </Routes>
          </MemoryRouter>
        </WatchPlaybackControllerContext.Provider>,
      );
    });

    expect(controller.syncRouteRequest).toHaveBeenCalledTimes(1);
    expect(controller.syncRouteRequest).toHaveBeenCalledWith(
      expect.objectContaining({
        contentId: "movie-1",
        libraryId: 7,
        restart: true,
      }),
    );

    const requestKey = (controller.syncRouteRequest as ReturnType<typeof vi.fn>).mock.calls[0]?.[0]
      ?.requestKey;
    expect(requestKey).toEqual(expect.any(String));

    await act(async () => {
      root.unmount();
    });

    expect(controller.handleRouteExit).toHaveBeenCalledWith(requestKey);
  });

  it("restores the live route request from history state and cleans it on back", async () => {
    const request = {
      kind: "live-tv" as const,
      contentId: "",
      channelId: "source:news-1",
      title: "News",
      streamUrl: "/api/v1/stream/live/grant-1/manifest?live_token=token-1",
      grantId: "grant-1",
      mode: "hls" as const,
      returnHref: "/livetv/libraries/4",
      requestKey: "live-tv:grant-1",
      restart: false,
    };
    await act(async () => {
      root.render(
        <WatchPlaybackControllerContext.Provider value={controller}>
          <MemoryRouter initialEntries={[{ pathname: "/watch/live", state: { livePlayback: request } }]}>
            <Routes>
              <Route path="/watch/live" element={<LiveTVWatchRoute />} />
            </Routes>
          </MemoryRouter>
        </WatchPlaybackControllerContext.Provider>,
      );
    });

    expect(controller.syncRouteRequest).toHaveBeenCalledWith(request);
    await act(async () => {
      root.unmount();
    });
    expect(controller.handleRouteExit).toHaveBeenCalledWith(request.requestKey);
  });

  it("redirects a direct live route without playback state instead of rendering a blank host", async () => {
    await act(async () => {
      root.render(
        <WatchPlaybackControllerContext.Provider value={controller}>
          <MemoryRouter initialEntries={["/watch/live"]}>
            <Routes>
              <Route path="/watch/live" element={<LiveTVWatchRoute />} />
              <Route path="/" element={<div data-testid="safe-live-fallback" />} />
            </Routes>
          </MemoryRouter>
        </WatchPlaybackControllerContext.Provider>,
      );
    });

    expect(screen.getByTestId("safe-live-fallback")).toBeInTheDocument();
  });
});
