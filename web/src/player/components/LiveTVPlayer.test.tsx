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
    expect(screen.queryByRole("slider")).not.toBeInTheDocument();
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
    expect(apiMock).toHaveBeenCalledTimes(1);
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
