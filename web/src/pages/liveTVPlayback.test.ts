import { describe, expect, it } from "vitest";

import { liveTVPlaybackOutcome } from "./liveTVPlayback";

describe("Live TV playback outcomes", () => {
  it("accepts a playable response with a URL and grant", () => {
    expect(
      liveTVPlaybackOutcome({
        playable: true,
        url: "/api/v1/stream/live/grant-1/manifest",
        grant_id: "grant-1",
      }),
    ).toEqual({
      kind: "playable",
      url: "/api/v1/stream/live/grant-1/manifest",
      grantId: "grant-1",
    });
  });

  it("surfaces an unavailable response instead of silently doing nothing", () => {
    expect(liveTVPlaybackOutcome({ playable: false, error_code: "source_unavailable" })).toEqual({
      kind: "unavailable",
    });
  });

  it("classifies rejected playback requests as errors", () => {
    expect(liveTVPlaybackOutcome(new Error("request failed"))).toEqual({
      kind: "error",
      error: "request failed",
    });
  });

  it("rejects playable responses with an untrusted manifest URL", () => {
    expect(
      liveTVPlaybackOutcome({
        playable: true,
        url: "https://attacker.example/manifest.m3u8",
        grant_id: "grant-1",
      }),
    ).toEqual({ kind: "unavailable" });
  });
});
