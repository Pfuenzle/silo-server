import { expect, type Page, type Request, type Response } from "@playwright/test";
import { createHash } from "node:crypto";

const forbiddenText = [
  "exchange_failed",
  "integration-client-secret",
  "integration-code",
  "wrong-nonce",
  "client_secret",
  "code_verifier",
] as const;

export type BrowserEvidence = {
  readonly console: { readonly type: string; readonly text: string }[];
  readonly pageErrors: string[];
  readonly refreshTokenFingerprints: string[];
  readonly network: {
    readonly method: string;
    readonly path: string;
    readonly queryKeys: readonly string[];
    readonly status?: number;
    readonly error?: string;
    readonly unexpectedSensitiveQueryKeys?: readonly string[];
  }[];
};

type RawBrowserTelemetry = {
  readonly values: { readonly channel: string; readonly value: string }[];
  readonly fingerprints: Set<string>;
};

export type BrowserStoragePhase = "authenticated" | "cleared";

type BrowserFailureExpectations = {
  readonly allowServerRestart?: boolean;
};

const unauthorizedConsoleMessage =
  "Failed to load resource: the server responded with a status of 401 (Unauthorized)";

export function findUnexpectedBrowserFailures(
  evidence: BrowserEvidence,
  expectations: BrowserFailureExpectations = {},
): readonly string[] {
  const unexpected: string[] = [];
  const hasExpectedUnauthorizedResponse = evidence.network.some(
    (entry) => entry.method === "GET" && entry.path === "/api/v1/profiles" && entry.status === 401,
  );
  const hasSuccessfulLogout = evidence.network.some(
    (entry) =>
      entry.method === "POST" && entry.path === "/api/v1/auth/logout" && entry.status === 204,
  );

  for (const entry of evidence.network) {
    if (
      entry.status !== undefined &&
      entry.status >= 400 &&
      !(entry.method === "GET" && entry.path === "/api/v1/profiles" && entry.status === 401)
    ) {
      unexpected.push(`response ${entry.method} ${entry.path} ${entry.status}`);
    }
    if (entry.error === undefined) {
      continue;
    }
    const abortedLogout =
      entry.method === "POST" &&
      entry.path === "/api/v1/auth/logout" &&
      entry.status === 204 &&
      entry.error === "net::ERR_ABORTED";
    const expectedRestartRefusal =
      expectations.allowServerRestart === true && entry.error === "net::ERR_CONNECTION_REFUSED";
    if (!abortedLogout && !expectedRestartRefusal) {
      unexpected.push(`request ${entry.method} ${entry.path} ${entry.error}`);
    }
  }

  for (const message of evidence.console) {
    const expectedUnauthorized =
      hasExpectedUnauthorizedResponse &&
      message.type === "error" &&
      message.text === unauthorizedConsoleMessage;
    const expectedRestartConsole =
      expectations.allowServerRestart === true &&
      ((message.type === "error" && message.text.includes("net::ERR_CONNECTION_REFUSED")) ||
        (message.type === "warning" &&
          message.text.includes("WebSocket connection") &&
          message.text.includes("closed before the connection is established")));
    const expectedLogoutSocketClose =
      hasSuccessfulLogout &&
      message.type === "warning" &&
      message.text.includes("WebSocket connection") &&
      message.text.includes("closed before the connection is established");
    if (!expectedUnauthorized && !expectedRestartConsole && !expectedLogoutSocketClose) {
      unexpected.push(`console ${message.type}: ${message.text}`);
    }
  }
  for (const pageError of evidence.pageErrors) {
    unexpected.push(`pageerror: ${pageError}`);
  }
  return unexpected;
}

export function expectedRefreshStorageCount(phase: BrowserStoragePhase): number {
  return phase === "authenticated" ? 1 : 0;
}

export function isAllowedRefreshRequest(input: {
  readonly method: string;
  readonly origin: string;
  readonly siloOrigin: string;
  readonly path: string;
  readonly contentType: string | undefined;
  readonly body: string | null;
}): boolean {
  if (
    input.method !== "POST" ||
    input.origin !== input.siloOrigin ||
    input.path !== "/api/v1/auth/refresh" ||
    input.contentType?.split(";", 1)[0] !== "application/json" ||
    input.body === null
  ) {
    return false;
  }
  try {
    const body: unknown = JSON.parse(input.body);
    return (
      typeof body === "object" &&
      body !== null &&
      Object.keys(body).length === 1 &&
      typeof (body as { refresh_token?: unknown }).refresh_token === "string"
    );
  } catch {
    return false;
  }
}

const rawTelemetry = new WeakMap<BrowserEvidence, RawBrowserTelemetry>();

function recordRaw(evidence: BrowserEvidence, channel: string, value: string): void {
  rawTelemetry.get(evidence)?.values.push({ channel, value });
}

export function registerBrowserSecrets(evidence: BrowserEvidence, values: readonly string[]): void {
  const telemetry = rawTelemetry.get(evidence);
  if (!telemetry) throw new Error("browser evidence was not initialized");
  for (const value of values) {
    if (value) {
      telemetry.fingerprints.add(value);
      const fingerprint = refreshTokenFingerprint(value);
      if (!evidence.refreshTokenFingerprints.includes(fingerprint)) {
        evidence.refreshTokenFingerprints.push(fingerprint);
      }
    }
  }
}

export function refreshTokenFingerprint(value: string): string {
  return `sha256:${createHash("sha256").update(value).digest("hex").slice(0, 12)}`;
}

function sanitizeRequest(request: Request) {
  const url = new URL(request.url());
  const queryKeys = [...url.searchParams.keys()].sort();
  const sensitiveKeys = [
    "access_token",
    "code",
    "code_verifier",
    "error",
    "reason",
    "refresh_token",
    "state",
    "token",
  ];
  let expectedSensitiveKeys: readonly string[] = [];
  if (url.pathname === "/authorize") expectedSensitiveKeys = ["state"];
  if (url.pathname.startsWith("/api/v1/auth/oauth/") && url.pathname.endsWith("/callback")) {
    expectedSensitiveKeys = ["code", "state"];
  }
  if (url.pathname === "/login/oauth-complete") expectedSensitiveKeys = ["code"];
  if (url.pathname === "/login") expectedSensitiveKeys = ["error", "reason"];
  const unexpectedSensitiveQueryKeys = queryKeys.filter(
    (key) => sensitiveKeys.includes(key) && !expectedSensitiveKeys.includes(key),
  );
  return {
    method: request.method(),
    path: url.pathname,
    queryKeys,
    ...(unexpectedSensitiveQueryKeys.length > 0 ? { unexpectedSensitiveQueryKeys } : {}),
  };
}

function sanitizeConsoleText(text: string): string {
  return text
    .replace(
      /([?&])(?:access_token|code|code_verifier|refresh_token|state|ticket|token)=[^&'"\s]+/g,
      "$1[redacted]",
    )
    .replace(/eyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+/g, "[redacted-jwt]");
}

export function captureEvidence(page: Page): BrowserEvidence {
  const evidence: BrowserEvidence = {
    console: [],
    pageErrors: [],
    refreshTokenFingerprints: [],
    network: [],
  };
  rawTelemetry.set(evidence, { values: [], fingerprints: new Set() });
  page.on("console", (message) => {
    recordRaw(evidence, "console", message.text());
    evidence.console.push({ type: message.type(), text: sanitizeConsoleText(message.text()) });
  });
  page.on("pageerror", (error) => {
    recordRaw(evidence, "pageerror", error.message);
    evidence.pageErrors.push(sanitizeConsoleText(error.message));
  });
  page.on("request", (request) => {
    const url = new URL(request.url());
    const body = request.postData();
    const allowedRefresh = isAllowedRefreshRequest({
      method: request.method(),
      origin: url.origin,
      siloOrigin: new URL(page.url()).origin,
      path: url.pathname,
      contentType: request.headers()["content-type"],
      body,
    });
    recordRaw(evidence, "network:url", request.url());
    recordRaw(evidence, "network:headers", JSON.stringify(request.headers()));
    recordRaw(
      evidence,
      allowedRefresh ? "network:allowed-refresh-body" : "network:body",
      body ?? "",
    );
    evidence.network.push(sanitizeRequest(request));
  });
  page.on("requestfailed", (request) => {
    const sanitized = sanitizeRequest(request);
    const entry = [...evidence.network]
      .reverse()
      .find(
        (candidate) => candidate.method === sanitized.method && candidate.path === sanitized.path,
      );
    if (entry) Object.assign(entry, { error: request.failure()?.errorText ?? "request failed" });
  });
  page.on("response", (response: Response) => {
    const request = sanitizeRequest(response.request());
    const entry = [...evidence.network]
      .reverse()
      .find((candidate) => candidate.method === request.method && candidate.path === request.path);
    if (entry) Object.assign(entry, { status: response.status() });
  });
  return evidence;
}

export async function assertNoBrowserLeak(
  page: Page,
  evidence: BrowserEvidence,
  storagePhase: BrowserStoragePhase,
): Promise<void> {
  const storage = await page.evaluate(() => ({
    local: Object.entries(localStorage),
    session: Object.entries(sessionStorage),
    historyLength: history.length,
    location: location.href,
  }));
  const visible = await page.locator("body").innerText();
  const historyURL = page.url();
  for (const [key, value] of storage.local) recordRaw(evidence, `storage:local:${key}`, value);
  for (const [key, value] of storage.session) recordRaw(evidence, `storage:session:${key}`, value);
  recordRaw(evidence, "history", storage.location);
  recordRaw(evidence, "dom", visible);
  recordRaw(evidence, "page:url", historyURL);
  const telemetry = rawTelemetry.get(evidence);
  if (!telemetry) throw new Error("browser evidence was not initialized");
  for (const fingerprint of telemetry.fingerprints) {
    const leakingChannels = telemetry.values
      .filter(
        (entry) =>
          entry.value.includes(fingerprint) &&
          !(
            (entry.channel === "storage:local:refresh_token" ||
              entry.channel === "network:allowed-refresh-body") &&
            entry.value.includes(fingerprint)
          ),
      )
      .map((entry) => entry.channel);
    if (leakingChannels.length > 0) {
      const hash = createHash("sha256").update(fingerprint).digest("hex").slice(0, 12);
      throw new Error(
        `raw browser secret fingerprint sha256:${hash} leaked via ${[...new Set(leakingChannels)].join(",")}`,
      );
    }
  }
  const storageKeys = [...storage.local, ...storage.session].map(([key]) => key);
  expect(storageKeys).not.toContain("access_token");
  expect(storageKeys).not.toContain("code");
  expect(storage.local.filter(([key]) => key === "refresh_token")).toHaveLength(
    expectedRefreshStorageCount(storagePhase),
  );
  expect(storage.session.map(([key]) => key)).not.toContain("refresh_token");
  for (const forbidden of forbiddenText) {
    expect(JSON.stringify(storage)).not.toContain(forbidden);
    expect(visible).not.toContain(forbidden);
    expect(historyURL).not.toContain(forbidden);
    expect(JSON.stringify(evidence.console)).not.toContain(forbidden);
  }
  expect(JSON.stringify(evidence.console)).not.toMatch(
    /(?:token|state|code_verifier)=|eyJ[a-zA-Z0-9_-]+\./,
  );
  expect(evidence.network.filter((entry) => entry.unexpectedSensitiveQueryKeys)).toEqual([]);
  expect(historyURL).not.toMatch(/[?&](code|error|token)=/);
  expect(evidence.pageErrors).toEqual([]);
}
