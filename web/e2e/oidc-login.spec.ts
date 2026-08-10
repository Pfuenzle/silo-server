import { expect, test, type Page } from "@playwright/test";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";

import {
  startOIDCBrowserFixture,
  type OIDCBrowserFixture,
  type OIDCFailureMode,
} from "./oidc-browser-fixture";
import {
  assertNoBrowserLeak,
  captureEvidence,
  findUnexpectedBrowserFailures,
  registerBrowserSecrets,
} from "./oidc-browser-evidence";

const evidenceDir = path.resolve(
  process.cwd(),
  process.env.SILO_OIDC_EVIDENCE_DIR ?? "../.omo/evidence/task16-browser",
);
const safeNext = "/profiles";
const cjkPresentation = {
  server_name: "별빛 시네마",
  login_subtitle: "家族のプロフィールで安全にサインインしてください。",
  provider_display_name: "会社アカウントで続ける",
} as const;
type CJKLayout = {
  readonly viewport: number;
  readonly bodyClientWidth: number;
  readonly bodyScrollWidth: number;
  readonly visibleText: readonly {
    readonly text: string;
    readonly left: number;
    readonly right: number;
    readonly top: number;
    readonly bottom: number;
    readonly fontFamily: string;
    readonly lineHeight: string;
  }[];
};
let fixture: OIDCBrowserFixture;

test.describe.configure({ timeout: 90_000 });

async function beginOIDCLogin(page: Page): Promise<void> {
  await page.goto(`${fixture.handoff.silo_url}/login?redirect=${encodeURIComponent(safeNext)}`);
  const provider = page.getByRole("button", { name: fixture.handoff.provider.display_name });
  await expect(provider).toBeVisible();
  await provider.click();
}

async function expectProfilesSettled(page: Page): Promise<void> {
  await expect(page.getByRole("heading", { name: "Who's watching?" })).toBeVisible();
  await expect(page.getByText(fixture.handoff.oidc_profile_name, { exact: true })).toBeVisible();
  await expect(page.locator('[data-slot="skeleton"]')).toHaveCount(0);
}

async function recordRefreshToken(
  page: Page,
  evidence: ReturnType<typeof captureEvidence>,
): Promise<string> {
  const refreshToken = await page.evaluate(() => localStorage.getItem("refresh_token"));
  expect(refreshToken).toBeTruthy();
  registerBrowserSecrets(evidence, [refreshToken ?? ""]);
  return refreshToken ?? "";
}

async function localLogin(page: Page, evidence: ReturnType<typeof captureEvidence>): Promise<void> {
  await page.goto(`${fixture.handoff.silo_url}/login?redirect=${encodeURIComponent(safeNext)}`);
  await page.getByLabel("Username").fill(fixture.handoff.local_account.username);
  await page
    .getByRole("textbox", { name: "Password" })
    .fill(fixture.handoff.local_account.password);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page).toHaveURL(/\/profiles$/);
  await expect(page.getByRole("button", { name: "Sign out" })).toBeVisible({ timeout: 15_000 });
  await recordRefreshToken(page, evidence);
}

async function signOut(page: Page): Promise<void> {
  await page.goto(new URL("/profiles", page.url()).toString());
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page).toHaveURL(/\/login(?:\?|$)/);
}

async function activeElementSnapshot(page: Page) {
  return page.evaluate(() => {
    const active = document.activeElement;
    return {
      tagName: active?.tagName ?? null,
      id: active?.id ?? null,
      text: active instanceof HTMLElement ? active.innerText.trim() : "",
    };
  });
}

test.beforeAll(async () => {
  test.setTimeout(20 * 60 * 1000);
  expect(process.env.PLAYWRIGHT_CHROME_EXECUTABLE_PATH).toBe(
    "/blyatflix/.nix-profile/bin/chromium",
  );
  await mkdir(evidenceDir, { recursive: true });
  fixture = await startOIDCBrowserFixture(evidenceDir);
});

test.afterAll(async () => {
  test.setTimeout(60_000);
  await fixture?.stop();
});

test("completes packaged OIDC login, refresh, logout, revocation, and relogin without leaks", async ({
  page,
  request,
}) => {
  const evidence = captureEvidence(page);
  await beginOIDCLogin(page);
  await expect(page).toHaveURL(new RegExp(`${safeNext}$`));
  await expectProfilesSettled(page);
  await assertNoBrowserLeak(page, evidence, "authenticated");

  for (const width of [375, 768, 1280]) {
    await page.setViewportSize({ width, height: width === 375 ? 812 : 900 });
    await expectProfilesSettled(page);
    await page.screenshot({
      path: path.join(evidenceDir, `${width}-success-dark.png`),
      fullPage: true,
    });
  }
  await writeFile(
    path.join(evidenceDir, "success.aria.yml"),
    await page.locator("body").ariaSnapshot(),
  );

  const refreshTokenBeforeReload = await recordRefreshToken(page, evidence);
  await page.reload();
  await expectProfilesSettled(page);
  const refreshToken = await recordRefreshToken(page, evidence);
  expect(refreshToken).not.toBe(refreshTokenBeforeReload);
  registerBrowserSecrets(evidence, [refreshToken ?? ""]);

  await signOut(page);
  const revoked = await request.post(`${fixture.handoff.silo_url}/api/v1/auth/refresh`, {
    data: { refresh_token: refreshToken },
  });
  expect(revoked.status()).toBe(401);
  await beginOIDCLogin(page);
  await expect(page.getByText(fixture.handoff.oidc_profile_name, { exact: true })).toBeVisible();
  await recordRefreshToken(page, evidence);
  const historyBeforeBack = await page.evaluate(() => history.length);
  const previous = await page.goBack();
  expect(previous).not.toBeNull();
  await expect(page).toHaveURL(/\/login(?:\?|$)/);
  expect(await page.evaluate(() => history.length)).toBeLessThanOrEqual(historyBeforeBack);
  await assertNoBrowserLeak(page, evidence, "authenticated");

  expect(findUnexpectedBrowserFailures(evidence)).toEqual([]);
  await writeFile(
    path.join(evidenceDir, "success-browser.json"),
    JSON.stringify(evidence, null, 2),
  );
});

for (const [mode, width] of [
  ["wrong_nonce", 375],
  ["wrong_signature", 768],
  ["idp_failure", 1280],
] as const satisfies readonly (readonly [OIDCFailureMode, number])[]) {
  test(`${mode} is generic, retryable, and preserves local break-glass login`, async ({ page }) => {
    const evidence = captureEvidence(page);
    await fixture.setMode(mode);
    await beginOIDCLogin(page);
    await page.setViewportSize({ width, height: width === 375 ? 812 : 900 });

    const alert = page.getByRole("alert");
    await expect(alert).toContainText("We couldn't sign you in");
    const retry = page.getByRole("button", { name: "Try again" });
    await expect(retry).toBeFocused();
    await assertNoBrowserLeak(page, evidence, "cleared");
    await page.screenshot({
      path: path.join(evidenceDir, `${width}-${mode}-failure-focus-dark.png`),
      fullPage: true,
    });
    await writeFile(
      path.join(evidenceDir, `${mode}.aria.yml`),
      await page.locator("body").ariaSnapshot(),
    );

    await retry.click();
    await expect(page).toHaveURL((url) => {
      return (
        url.pathname === "/login" &&
        url.searchParams.get("redirect") === safeNext &&
        [...url.searchParams.keys()].join(",") === "redirect"
      );
    });
    const provider = page.getByRole("button", { name: fixture.handoff.provider.display_name });
    await expect(provider).toBeFocused();
    const providerForm = provider.locator("xpath=ancestor::form");
    const formAction = await providerForm.getAttribute("action");
    expect(formAction).not.toBeNull();
    const formActionURL = new URL(formAction ?? "", page.url());
    expect(formActionURL.pathname).toMatch(/^\/api\/v1\/auth\/oauth\/\d+\/init$/);
    expect([...formActionURL.searchParams.entries()]).toEqual([["next", safeNext]]);
    const retryFocus = await activeElementSnapshot(page);
    expect(retryFocus).toEqual({
      tagName: "BUTTON",
      id: "",
      text: fixture.handoff.provider.display_name,
    });
    await writeFile(
      path.join(evidenceDir, `${mode}-retry-focus.json`),
      `${JSON.stringify(retryFocus, null, 2)}\n`,
    );
    await page.screenshot({
      path: path.join(evidenceDir, `${width}-${mode}-retry-dark.png`),
      fullPage: true,
    });
    await fixture.setMode("happy");
    const initiation = page.waitForRequest((request) => {
      const url = new URL(request.url());
      return request.method() === "POST" && url.pathname.endsWith("/init");
    });
    await provider.click();
    const initiationURL = new URL((await initiation).url());
    expect([...initiationURL.searchParams.entries()]).toEqual([["next", safeNext]]);
    await expect(page).toHaveURL(new RegExp(`${safeNext}$`));
    await expect(page.getByText(fixture.handoff.oidc_profile_name, { exact: true })).toBeVisible();
    await recordRefreshToken(page, evidence);
    await signOut(page);
    await localLogin(page, evidence);
    await signOut(page);
    await assertNoBrowserLeak(page, evidence, "cleared");
    expect(findUnexpectedBrowserFailures(evidence)).toEqual([]);
    await writeFile(
      path.join(evidenceDir, `${mode}-browser.json`),
      JSON.stringify(evidence, null, 2),
    );
  });
}

test("provider disable hides OIDC while local login works and re-enable recovers", async ({
  page,
}) => {
  const evidence = captureEvidence(page);
  fixture.handoff = await fixture.setProviderEnabled(false);
  await page.goto(`${fixture.handoff.silo_url}/login?redirect=${encodeURIComponent(safeNext)}`);
  await expect(
    page.getByRole("button", { name: fixture.handoff.provider.display_name }),
  ).toHaveCount(0);
  await expect(page.getByLabel("Username")).toBeFocused();
  const localFocus = await activeElementSnapshot(page);
  expect(localFocus).toEqual({ tagName: "INPUT", id: "username", text: "" });
  await writeFile(
    path.join(evidenceDir, "provider-disabled-focus.json"),
    `${JSON.stringify(localFocus, null, 2)}\n`,
  );
  await writeFile(
    path.join(evidenceDir, "provider-disabled.aria.yml"),
    await page.locator("body").ariaSnapshot(),
  );
  await localLogin(page, evidence);
  await signOut(page);

  fixture.handoff = await fixture.setProviderEnabled(true);
  await page.goto(`${fixture.handoff.silo_url}/login?redirect=${encodeURIComponent(safeNext)}`);
  await expect(
    page.getByRole("button", { name: fixture.handoff.provider.display_name }),
  ).toBeVisible();
  await beginOIDCLogin(page);
  await expect(page.getByText(fixture.handoff.oidc_profile_name, { exact: true })).toBeVisible();
  await recordRefreshToken(page, evidence);
  await signOut(page);
  await assertNoBrowserLeak(page, evidence, "cleared");
  expect(findUnexpectedBrowserFailures(evidence, { allowServerRestart: true })).toEqual([]);
  await writeFile(
    path.join(evidenceDir, "provider-lifecycle-browser.json"),
    JSON.stringify(evidence, null, 2),
  );
});

test("CJK fixture branding and provider text remain readable at every evidence viewport", async ({
  page,
}) => {
  const original = {
    server_name: fixture.handoff.branding.server_name,
    login_subtitle: fixture.handoff.branding.login_subtitle,
    provider_display_name: fixture.handoff.provider.display_name,
  };
  try {
    fixture.handoff = await fixture.setPresentation(cjkPresentation);
    const evidence = captureEvidence(page);
    await page.goto(`${fixture.handoff.silo_url}/login?redirect=${encodeURIComponent(safeNext)}`);

    const provider = page.getByRole("button", { name: cjkPresentation.provider_display_name });
    await expect(page.getByText(cjkPresentation.server_name, { exact: true })).toBeVisible();
    await expect(page.getByText(cjkPresentation.login_subtitle, { exact: true })).toBeVisible();
    await expect(provider).toBeVisible();

    const layouts: CJKLayout[] = [];
    for (const width of [375, 768, 1280]) {
      await page.setViewportSize({ width, height: width === 375 ? 812 : 900 });
      await expect(provider).toBeVisible();
      await expect(page.locator('[data-slot="skeleton"]')).toHaveCount(0);
      const layout = await page.evaluate(() => {
        const body = document.body;
        const visibleText = [
          ...body.querySelectorAll<HTMLElement>(
            "h1, [data-slot='card-title'], [data-slot='card-description'], button span",
          ),
        ]
          .filter((element) => element.offsetParent !== null)
          .map((element) => {
            const rect = element.getBoundingClientRect();
            return {
              text: element.innerText,
              left: rect.left,
              right: rect.right,
              top: rect.top,
              bottom: rect.bottom,
              fontFamily: getComputedStyle(element).fontFamily,
              lineHeight: getComputedStyle(element).lineHeight,
            };
          });
        return {
          viewport: innerWidth,
          bodyClientWidth: body.clientWidth,
          bodyScrollWidth: body.scrollWidth,
          visibleText,
        };
      });
      expect(layout.bodyScrollWidth).toBeLessThanOrEqual(layout.bodyClientWidth);
      layouts.push(layout);
      await page.screenshot({
        path: path.join(evidenceDir, `${width}-cjk-login-dark.png`),
        fullPage: true,
      });
    }
    await writeFile(
      path.join(evidenceDir, "cjk-layout.json"),
      `${JSON.stringify(layouts, null, 2)}\n`,
    );
    await writeFile(
      path.join(evidenceDir, "cjk.aria.yml"),
      await page.locator("body").ariaSnapshot(),
    );
    await assertNoBrowserLeak(page, evidence, "cleared");
    expect(findUnexpectedBrowserFailures(evidence)).toEqual([]);
    await writeFile(path.join(evidenceDir, "cjk-browser.json"), JSON.stringify(evidence, null, 2));
  } finally {
    fixture.handoff = await fixture.setPresentation(original);
  }
});
