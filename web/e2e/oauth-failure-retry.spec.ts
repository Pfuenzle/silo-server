import { expect, test, type Page, type Route } from "@playwright/test";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";

const evidenceDir = path.resolve(process.cwd(), "../.omo/evidence/task16-frontend");
const failureReason = "exchange_failed: upstream token=browser-secret";
const nextPath = "/library?tab=new";
const completionCode = "one-time-browser-secret";
const failurePath = `/login/oauth-complete?code=${completionCode}&next=${encodeURIComponent(nextPath)}`;
const retryPath = `/login?redirect=${encodeURIComponent(nextPath)}`;

function json(route: Route, body: unknown, status = 200) {
  return route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
}

async function installApiFixture(page: Page) {
  await page.route("**/api/v1/**", async (route) => {
    const pathname = new URL(route.request().url()).pathname;
    if (pathname === "/api/v1/auth/setup") {
      return json(route, { needs_setup: false });
    }
    if (pathname === "/api/v1/auth/providers") {
      return json(route, [
        {
          id: "plugin:41:oidc",
          display_name: "Company SSO",
          mode: "oauth",
          default: false,
          installation_id: 41,
        },
        {
          id: "local",
          display_name: "Local account",
          mode: "credentials",
          default: true,
        },
      ]);
    }
    if (pathname === "/api/v1/auth/oauth/complete") {
      return json(route, { error: "completion_expired", message: failureReason }, 410);
    }
    return json(route, {});
  });
}

test.beforeAll(async () => mkdir(evidenceDir, { recursive: true }));

test("shows a generic OAuth failure and safely returns to provider selection", async ({ page }) => {
  const consoleMessages: { readonly type: string; readonly text: string }[] = [];
  const pageErrors: string[] = [];
  page.on("console", (message) =>
    consoleMessages.push({ type: message.type(), text: message.text() }),
  );
  page.on("pageerror", (error) => pageErrors.push(error.message));
  await installApiFixture(page);

  for (const width of [375, 768, 1280]) {
    await page.setViewportSize({ width, height: width === 375 ? 812 : 900 });
    await page.goto(failurePath);

    const alert = page.getByRole("alert");
    await expect(alert).toContainText("We couldn't sign you in");
    await expect(alert).toContainText("Return to sign in and try again");
    await expect(page.getByText(failureReason, { exact: false })).toHaveCount(0);
    await expect(page.getByText("browser-secret", { exact: false })).toHaveCount(0);
    await expect(page.locator("body")).not.toContainText(completionCode);
    await expect(page).toHaveURL("/login/oauth-complete");
    expect(page.url()).not.toContain(completionCode);
    const retry = page.getByRole("button", { name: "Try again" });
    await expect(retry).toBeFocused();
    await expect(page.locator("html")).toHaveAttribute("data-theme", "midnight-cinema");
    await page.screenshot({
      path: path.join(evidenceDir, `${width}-failure-focus-dark.png`),
      fullPage: true,
    });

    if (width === 1280) {
      const aria = await page.locator("body").ariaSnapshot();
      expect(aria).toContain("alert");
      expect(aria).toContain("Try again");
      expect(aria).not.toContain("exchange_failed");
      await writeFile(path.join(evidenceDir, "oauth-failure.aria.yml"), aria);
    }

    await retry.press("Enter");
    await expect(page).toHaveURL(retryPath);
    await expect(page.getByRole("alert")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Company SSO" })).toBeFocused();
    await expect(
      page.locator(
        `form[action="/api/v1/auth/oauth/41/init?next=${encodeURIComponent(nextPath)}"]`,
      ),
    ).toHaveCount(1);
    await page.screenshot({
      path: path.join(evidenceDir, `${width}-retry-dark.png`),
      fullPage: true,
    });
  }

  expect(pageErrors).toEqual([]);
  for (const message of consoleMessages) {
    expect(message.text).not.toContain(failureReason);
    expect(message.text).not.toContain(completionCode);
    expect(message.text).not.toContain("browser-secret");
  }
  expect(consoleMessages.filter((message) => message.type === "warning")).toEqual([]);
  expect(
    consoleMessages.filter((message) => message.type === "error" && !message.text.includes("410")),
  ).toEqual([]);
});
