import { describe, expect, it } from "vitest";

import { validateLiveTVSources } from "./liveTVSources";

describe("Live TV source validation", () => {
  it("rejects missing names, keys, locations, unsupported schemes, and duplicate keys", () => {
    const result = validateLiveTVSources([
      { kind: "playlist", source_key: "main", name: "", location: "ftp://provider", enabled: true },
      { kind: "epg", source_key: "main", name: "Guide", location: "https://guide", enabled: true },
    ]);

    expect(result).toEqual({
      0: {
        name: "Enter a source name.",
        location: "Use an HTTP or HTTPS source URL.",
      },
      1: { source_key: "Source keys must be unique." },
    });
  });
});
