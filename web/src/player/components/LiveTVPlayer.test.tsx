import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LiveTVPlayer } from "./LiveTVPlayer";

const apiMock = vi.hoisted(() => vi.fn());
const profileMock = vi.hoisted(() => ({ profile: null as { language?: string } | null }));
vi.mock("@/api/client", () => ({ api: apiMock }));
vi.mock("@/hooks/useCurrentProfile", () => ({ useCurrentProfile: () => profileMock }));

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  apiMock.mockReset();
  profileMock.profile = null;
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
});
