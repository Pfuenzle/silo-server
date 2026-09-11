import { describe, expect, it } from "vitest";
import { isSiloPlaybackUrl, liveTVPlaybackResponseSchema } from "./livetv";

describe("Live TV playback contract", () => {
  it("accepts a live plan without a finite duration", () => {
    const response = liveTVPlaybackResponseSchema.parse({
      channel_id: "source:news-1",
      live: true,
      playable: true,
      url: "/api/v1/stream/live/grant-1/manifest",
      grant_id: "grant-1",
    });

    expect(response.live).toBe(true);
    expect(response).not.toHaveProperty("duration");
  });

  it("accepts only Silo-owned live stream URLs", () => {
    expect(isSiloPlaybackUrl("/api/v1/stream/live/grant-1/manifest")).toBe(true);
    expect(isSiloPlaybackUrl("https://provider.example/live.m3u8")).toBe(false);
    expect(isSiloPlaybackUrl("/api/v1/playback/transcode/session/master.m3u8")).toBe(false);
  });
});
