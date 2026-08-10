import { defineConfig } from "@playwright/test";

const chromeExecutable = process.env.PLAYWRIGHT_CHROME_EXECUTABLE_PATH;
const fullSystemOIDC = process.env.SILO_OIDC_E2E === "1";

export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  expect: { timeout: 5_000 },
  fullyParallel: false,
  workers: 1,
  use: {
    baseURL: "http://127.0.0.1:4177",
    ...(chromeExecutable
      ? { launchOptions: { executablePath: chromeExecutable } }
      : { channel: "chrome" }),
    trace: "retain-on-failure",
    ignoreHTTPSErrors: fullSystemOIDC,
  },
  projects: [{ name: "chromium", use: { browserName: "chromium" } }],
  webServer: fullSystemOIDC
    ? undefined
    : {
        command:
          "npx --yes pnpm@10.32.1 run build && npx --yes pnpm@10.32.1 preview --host 127.0.0.1 --port 4177",
        url: "http://127.0.0.1:4177",
        reuseExistingServer: false,
        timeout: 180_000,
      },
});
