import { describe, expect, it } from "vitest";

import {
  expectedRefreshStorageCount,
  findUnexpectedBrowserFailures,
  isAllowedRefreshRequest,
  refreshTokenFingerprint,
} from "./e2e/oidc-browser-evidence";

const validRequest = {
  method: "POST",
  origin: "https://silo.test",
  siloOrigin: "https://silo.test",
  path: "/api/v1/auth/refresh",
  contentType: "application/json",
  body: '{"refresh_token":"token"}',
} as const;

describe("isAllowedRefreshRequest", () => {
  it("allows only the designated refresh exchange", () => {
    expect(isAllowedRefreshRequest(validRequest)).toBe(true);
    expect(
      isAllowedRefreshRequest({ ...validRequest, contentType: "application/json; charset=utf-8" }),
    ).toBe(true);
  });

  it.each([
    { ...validRequest, method: "GET" },
    { ...validRequest, origin: "https://other.test" },
    { ...validRequest, path: "/api/v1/auth/me" },
    { ...validRequest, contentType: "text/plain" },
    { ...validRequest, body: '{"refresh_token":"token","extra":"value"}' },
  ])("rejects an unsafe refresh-token channel", (request) => {
    expect(isAllowedRefreshRequest(request)).toBe(false);
  });
});

describe("expectedRefreshStorageCount", () => {
  it("requires refresh storage only while authenticated", () => {
    expect(expectedRefreshStorageCount("authenticated")).toBe(1);
    expect(expectedRefreshStorageCount("cleared")).toBe(0);
  });
});

describe("refreshTokenFingerprint", () => {
  it("records a deterministic non-secret refresh-token fingerprint", () => {
    expect(refreshTokenFingerprint("minted-refresh-token")).toBe("sha256:2cc5806c1786");
  });
});

describe("findUnexpectedBrowserFailures", () => {
  it("accepts only asserted unauthenticated profile bootstrap failures", () => {
    expect(
      findUnexpectedBrowserFailures({
        console: [
          {
            type: "error",
            text: "Failed to load resource: the server responded with a status of 401 (Unauthorized)",
          },
          {
            type: "warning",
            text: "WebSocket connection to 'ws://silo.test/api/v1/events/ws?ticket=redacted' failed: WebSocket is closed before the connection is established.",
          },
        ],
        pageErrors: [],
        refreshTokenFingerprints: [],
        network: [
          { method: "GET", path: "/api/v1/profiles", queryKeys: [], status: 401 },
          {
            method: "POST",
            path: "/api/v1/auth/logout",
            queryKeys: [],
            status: 204,
            error: "net::ERR_ABORTED",
          },
        ],
      }),
    ).toEqual([]);
  });

  it("reports a libraries 500 instead of filtering it", () => {
    expect(
      findUnexpectedBrowserFailures({
        console: [],
        pageErrors: [],
        refreshTokenFingerprints: [],
        network: [{ method: "GET", path: "/api/v1/user/libraries", queryKeys: [], status: 500 }],
      }),
    ).toEqual(["response GET /api/v1/user/libraries 500"]);
  });

  it("allows restart refusal only when the scenario asserts a restart", () => {
    const evidence = {
      console: [{ type: "error", text: "Failed to load resource: net::ERR_CONNECTION_REFUSED" }],
      pageErrors: [],
      refreshTokenFingerprints: [],
      network: [
        {
          method: "GET",
          path: "/api/v1/profiles",
          queryKeys: [],
          error: "net::ERR_CONNECTION_REFUSED",
        },
      ],
    };

    expect(findUnexpectedBrowserFailures(evidence)).not.toEqual([]);
    expect(findUnexpectedBrowserFailures(evidence, { allowServerRestart: true })).toEqual([]);
  });
});
