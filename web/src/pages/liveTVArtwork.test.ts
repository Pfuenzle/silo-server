import { describe, expect, it } from "vitest";

import { liveTVArtwork } from "./liveTVArtwork";

describe("Live TV artwork", () => {
  it("renders authorized API artwork paths", () => {
    expect(liveTVArtwork({ path: "/api/v1/images/channel-1" })).toBe("/api/v1/images/channel-1");
    expect(liveTVArtwork({ url: "/api/v1/stream/live/artwork.png" })).toBe(
      "/api/v1/stream/live/artwork.png",
    );
  });

  it("keeps already-authorized artwork paths unchanged", () => {
    expect(liveTVArtwork({ path: "/api/v1/images/channel-1" })).toBe("/api/v1/images/channel-1");
  });

  it("does not expose arbitrary remote artwork URLs", () => {
    expect(liveTVArtwork({ logo: "https://provider.example/logo.png" })).toBeNull();
    expect(liveTVArtwork({ logo: "http://provider.example/logo.png" })).toBeNull();
    expect(liveTVArtwork({ logo: "data:image/png;base64,abc" })).toBeNull();
    expect(liveTVArtwork({ url: "javascript:alert(1)" })).toBeNull();
  });

  it("accepts only signed HTTPS object URLs", () => {
    expect(liveTVArtwork({ url: "https://cdn.example/logo.png?X-Amz-Signature=abc" })).toBe(
      "https://cdn.example/logo.png?X-Amz-Signature=abc",
    );
    expect(liveTVArtwork({ url: "http://cdn.example/logo.png?X-Amz-Signature=abc" })).toBeNull();
  });
});
