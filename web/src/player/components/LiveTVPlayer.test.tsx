import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LiveTVPlayer } from "./LiveTVPlayer";

const apiMock = vi.hoisted(() => vi.fn());
const profileMock = vi.hoisted(() => ({ profile: null as { language?: string } | null }));
const hlsConstructorMock = vi.hoisted(() => vi.fn());
const hlsCallsMock = vi.hoisted(() => vi.fn());
const hlsSupportedMock = vi.hoisted(() => vi.fn(() => false));
vi.mock("@/api/client", () => ({ api: apiMock }));
vi.mock("@/hooks/useCurrentProfile", () => ({ useCurrentProfile: () => profileMock }));
vi.mock("hls.js", () => ({
  default: class MockHls {
    static isSupported() {
      return hlsSupportedMock();
    }

    constructor() {
      hlsConstructorMock();
    }

    attachMedia() {
      hlsCallsMock("attachMedia");
    }
    destroy() {}
    loadSource() {
      hlsCallsMock("loadSource");
    }
    startLoad() {
      hlsCallsMock("startLoad");
    }
  },
}));

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  apiMock.mockReset();
  profileMock.profile = null;
  hlsConstructorMock.mockReset();
  hlsCallsMock.mockReset();
  hlsSupportedMock.mockReset();
  hlsSupportedMock.mockReturnValue(false);
});

describe("LiveTVPlayer", () => {
  it("shows live status without a finite duration or normal completion", () => {
    render(
      <LiveTVPlayer
        channelId="source:news-1"
        title="News"
        streamUrl="/api/v1/stream/live/grant-1/manifest"
        grantId="grant-1"
      />,
    );

    expect(screen.getByRole("status", { name: "Live" })).toBeInTheDocument();
    expect(screen.queryByText(/\d+:\d+/)).not.toBeInTheDocument();
    expect(screen.getByRole("slider", { name: "Volume" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Stop live playback" })).toBeInTheDocument();
    expect(document.querySelector("video")).not.toHaveAttribute("controls");
    expect(screen.getByTestId("player-controls")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Quality unavailable" })).toBeDisabled();
  });

  it("uses the normal player chrome without VOD transport controls", () => {
    render(
      <LiveTVPlayer
        channelId="source:news-1"
        title="News"
        streamUrl="/api/v1/stream/live/grant-1/manifest"
        grantId="grant-1"
      />,
    );

    expect(screen.getByTestId("player-controls")).toHaveClass("player-controls");
    expect(screen.getByRole("button", { name: "Play" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Fullscreen" })).toBeInTheDocument();
    expect(screen.getByRole("slider", { name: "Volume" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /seconds/i })).not.toBeInTheDocument();
    expect(screen.queryByText(/\/\s*\d+:/)).not.toBeInTheDocument();
  });

  it("shows an enabled quality control in compact mode and sends the selected server option", async () => {
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(1024);
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(callback: ResizeObserverCallback) {
          callback([], {} as ResizeObserver);
        }
        observe() {}
        disconnect() {}
      },
    );
    apiMock.mockImplementation((path: string) => {
      if (path.endsWith("/qualities")) {
        return Promise.resolve({
          options: [{ id: "q-720-2400000", label: "720p", height: 720, bitrate_kbps: 2400 }],
          active_id: "q-720-2400000",
          transcoding_supported: false,
        });
      }
      return Promise.resolve({
        options: [{ id: "q-720-2400000", label: "720p", height: 720, bitrate_kbps: 2400 }],
        active_id: "q-720-2400000",
        transcoding_supported: false,
      });
    });

    render(
      <LiveTVPlayer
        channelId="source:news-1"
        title="News"
        streamUrl="/api/v1/stream/live/grant-1/manifest"
        grantId="grant-1"
      />,
    );

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Quality" })).toBeInTheDocument(),
    );
    expect(screen.getByTestId("player-controls")).toHaveAttribute("data-compact", "true");
    expect(screen.getByRole("button", { name: "Quality" })).not.toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Quality" }));
    fireEvent.click(screen.getByRole("menuitem", { name: /720p/ }));

    await waitFor(() =>
      expect(apiMock).toHaveBeenCalledWith("/livetv/playback/grant-1/quality", {
        method: "POST",
        body: JSON.stringify({ id: "q-720-2400000" }),
        headers: { "Content-Type": "application/json" },
      }),
    );
    expect(screen.queryByRole("button", { name: /seconds/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Chapters" })).not.toBeInTheDocument();
  });

  it("disables the unavailable control after a successful empty quality response", async () => {
    apiMock.mockResolvedValue({
      options: [],
      transcoding_supported: false,
      unsupported_reason: "no_variants",
    });

    render(
      <LiveTVPlayer
        channelId="source:news-1"
        title="News"
        streamUrl="/api/v1/stream/live/grant-1/manifest"
        grantId="grant-1"
      />,
    );

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Quality unavailable" })).toBeDisabled(),
    );
    expect(screen.queryByRole("button", { name: "Quality" })).not.toBeInTheDocument();
  });

  it("shows server-owned live quality options without adding seek controls", async () => {
    apiMock.mockImplementation((path: string) => {
      if (path.endsWith("/qualities")) {
        return Promise.resolve({
          options: [{ id: "q-720-2400000", label: "720p", height: 720, bitrate_kbps: 2400 }],
          active_id: "q-720-2400000",
          transcoding_supported: false,
        });
      }
      return Promise.resolve({
        options: [{ id: "q-720-2400000", label: "720p", height: 720, bitrate_kbps: 2400 }],
        active_id: "q-720-2400000",
        transcoding_supported: false,
      });
    });

    render(
      <LiveTVPlayer
        channelId="source:news-1"
        title="News"
        streamUrl="/api/v1/stream/live/grant-1/manifest"
        grantId="grant-1"
      />,
    );

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Quality" })).toBeInTheDocument(),
    );
    expect(screen.getByRole("button", { name: "Quality" })).not.toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Quality" }));
    fireEvent.click(screen.getByRole("menuitem", { name: /720p/ }));
    await waitFor(() =>
      expect(apiMock).toHaveBeenCalledWith("/livetv/playback/grant-1/quality", {
        method: "POST",
        body: JSON.stringify({ id: "q-720-2400000" }),
        headers: { "Content-Type": "application/json" },
      }),
    );
    expect(screen.queryByRole("button", { name: /seconds/i })).not.toBeInTheDocument();
  });

  it("keeps quality unavailable for direct MPEG-TS even when the server reports variants", async () => {
    apiMock.mockImplementation((path: string) => {
      if (path.endsWith("/qualities")) {
        return Promise.resolve({
          options: [{ id: "q-720-2400000", label: "720p", height: 720, bitrate_kbps: 2400 }],
          active_id: "q-720-2400000",
          transcoding_supported: false,
        });
      }
      return Promise.resolve(undefined);
    });

    render(
      <LiveTVPlayer
        channelId="source:news-1"
        title="News"
        streamUrl="/api/v1/stream/live/grant-1/manifest"
        grantId="grant-1"
        mode="direct"
      />,
    );

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Quality unavailable" })).toBeDisabled(),
    );
    expect(screen.queryByRole("button", { name: "Quality" })).not.toBeInTheDocument();
  });

  it("keeps the localized stop action available in the compact player", () => {
    render(
      <LiveTVPlayer
        channelId="source:news-1"
        title="News"
        streamUrl="/api/v1/stream/live/grant-1/manifest"
        grantId="grant-1"
      />,
    );

    window.dispatchEvent(new Event("resize"));
    expect(screen.getByRole("button", { name: "Stop live playback" })).toBeInTheDocument();
  });

  it("deletes the live grant on stop and unmount", async () => {
    apiMock.mockResolvedValue(undefined);
    const { unmount } = render(
      <LiveTVPlayer
        channelId="source:news-1"
        title="News"
        streamUrl="/api/v1/stream/live/grant-1/manifest"
        grantId="grant-1"
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Stop live playback" }));
    await waitFor(() =>
      expect(apiMock).toHaveBeenCalledWith("/livetv/playback/grant-1", {
        method: "DELETE",
        keepalive: true,
      }),
    );

    unmount();
    expect(apiMock).toHaveBeenCalledWith("/livetv/playback/grant-1", {
      method: "DELETE",
      keepalive: true,
    });
    expect(apiMock.mock.calls.filter(([path]) => path === "/livetv/playback/grant-1").length).toBe(
      1,
    );
  });

  it("disables seeking and reports startup failure with retry", async () => {
    vi.useFakeTimers();
    try {
      render(
        <LiveTVPlayer
          channelId="source:news-1"
          title="News"
          streamUrl="/api/v1/stream/live/grant-1/manifest"
          grantId="grant-1"
          startupTimeoutMs={10}
        />,
      );
      expect(screen.queryByRole("button", { name: /seek/i })).not.toBeInTheDocument();
      act(() => vi.advanceTimersByTime(11));
      expect(screen.getByText("Live playback could not start")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Retry live playback" })).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("localizes player status and controls for German profiles", () => {
    profileMock.profile = { language: "de-DE" };

    render(
      <LiveTVPlayer
        channelId="source:news-1"
        title="News"
        streamUrl="/api/v1/stream/live/grant-1/manifest"
        grantId="grant-1"
      />,
    );

    expect(screen.getByRole("button", { name: "Live-Wiedergabe beenden" })).toBeInTheDocument();
    expect(screen.getByText("Live-Wiedergabe wird gestartet…")).toBeInTheDocument();
  });

  it("keeps native stream URLs limited to the opaque server binding", async () => {
    render(
      <LiveTVPlayer
        channelId="source:news-1"
        title="News"
        streamUrl="/api/v1/stream/live/grant-1/manifest"
        grantId="grant-1"
      />,
    );

    await waitFor(() => {
      const source = document.querySelector("video")?.src ?? "";
      expect(source).not.toContain("access-token");
      expect(source).not.toContain("profile_id");
    });
  });

  it("attaches the media element before loading the manifest", async () => {
    hlsSupportedMock.mockReturnValue(true);
    render(
      <LiveTVPlayer
        channelId="source:news-1"
        title="News"
        streamUrl="/api/v1/stream/live/grant-1/manifest"
        grantId="grant-1"
      />,
    );

    await waitFor(() => expect(hlsConstructorMock).toHaveBeenCalled());
    expect(hlsCallsMock.mock.calls.map(([name]) => name)).toEqual(["attachMedia", "loadSource"]);
  });

  it("restarts the negotiated HLS engine from the retry control", async () => {
    hlsSupportedMock.mockReturnValue(true);
    vi.useFakeTimers();
    try {
      render(
        <LiveTVPlayer
          channelId="source:news-1"
          title="News"
          streamUrl="/api/v1/stream/live/grant-1/manifest"
          grantId="grant-1"
          startupTimeoutMs={10}
        />,
      );

      await act(async () => {
        await Promise.resolve();
      });
      act(() => vi.advanceTimersByTime(11));
      fireEvent.click(screen.getByRole("button", { name: "Retry live playback" }));

      expect(hlsCallsMock).toHaveBeenCalledWith("startLoad");
    } finally {
      vi.useRealTimers();
    }
  });

  it("reports media errors through the normal retry control", () => {
    render(
      <LiveTVPlayer
        channelId="source:news-1"
        title="News"
        streamUrl="/api/v1/stream/live/grant-1/manifest"
        grantId="grant-1"
      />,
    );

    const video = document.querySelector("video");
    expect(video).not.toBeNull();
    if (video) fireEvent.error(video);

    expect(screen.getByRole("button", { name: "Retry live playback" })).toBeInTheDocument();
  });

  it("loads direct MPEG-TS through the authenticated Silo URL", async () => {
    render(
      <LiveTVPlayer
        channelId="source:news-1"
        title="News"
        streamUrl="/api/v1/stream/live/grant-1/manifest"
        grantId="grant-1"
        mode="direct"
      />,
    );

    await waitFor(() => expect(screen.getByRole("status", { name: "Live" })).toBeInTheDocument());
  });

  it("does not initialize HLS after the player unmounts", async () => {
    hlsSupportedMock.mockReturnValue(true);
    const { unmount } = render(
      <LiveTVPlayer
        channelId="source:news-1"
        title="News"
        streamUrl="/api/v1/stream/live/grant-1/manifest"
        grantId="grant-1"
      />,
    );

    unmount();
    await Promise.resolve();
    expect(hlsConstructorMock).not.toHaveBeenCalled();
  });
});
