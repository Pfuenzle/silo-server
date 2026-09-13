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

  it("rejects non-HTTPS artwork URLs", () => {
    expect(liveTVArtwork({ logo: "http://provider.example/logo.png" })).toBeNull();
    expect(liveTVArtwork({ logo: "data:image/png;base64,abc" })).toBeNull();
    expect(liveTVArtwork({ url: "javascript:alert(1)" })).toBeNull();
  });

  it("accepts HTTPS object URLs authorized by the API", () => {
    expect(liveTVArtwork({ url: "https://cdn.example/logo.png?X-Amz-Signature=abc" })).toBe(
      "https://cdn.example/logo.png?X-Amz-Signature=abc",
    );
    expect(liveTVArtwork({ url: "http://cdn.example/logo.png?X-Amz-Signature=abc" })).toBeNull();
  });

  it("renders HTTPS artwork URLs authorized by the API public CDN", () => {
    expect(liveTVArtwork({ url: "https://cdn.example/logo.png" })).toBe("https://cdn.example/logo.png");
  });
});
