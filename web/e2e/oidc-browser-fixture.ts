import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";

import { z } from "zod";

const startupTimeoutMs = 15 * 60 * 1000;
const shutdownTimeoutMs = 45 * 1000;

const handoffSchema = z.object({
  silo_url: z.string().url(),
  control_url: z.string().url(),
  control_token: z.string().min(32),
  provider: z.object({ id: z.string().min(1), display_name: z.string().min(1) }),
  branding: z.object({ server_name: z.string().min(1), login_subtitle: z.string().min(1) }),
  local_account: z.object({ username: z.string().min(1), password: z.string().min(1) }),
  oidc_profile_name: z.string().min(1),
});

export type OIDCBrowserHandoff = z.infer<typeof handoffSchema>;
export type OIDCFailureMode = "happy" | "wrong_nonce" | "wrong_signature" | "idp_failure";
export type OIDCBrowserPresentation = {
  readonly server_name: string;
  readonly login_subtitle: string;
  readonly provider_display_name: string;
};

type FixturePaths = {
  readonly root: string;
  readonly handoff: string;
  readonly stop: string;
};

export type OIDCBrowserFixture = {
  handoff: OIDCBrowserHandoff;
  setMode(mode: OIDCFailureMode): Promise<void>;
  setProviderEnabled(enabled: boolean): Promise<OIDCBrowserHandoff>;
  setPresentation(presentation: OIDCBrowserPresentation): Promise<OIDCBrowserHandoff>;
  stop(): Promise<void>;
};

function waitForExit(
  child: ChildProcessWithoutNullStreams,
  timeoutMs: number,
): Promise<number | null> {
  return new Promise((resolve, reject) => {
    if (child.exitCode !== null) {
      resolve(child.exitCode);
      return;
    }
    const timer = setTimeout(
      () => reject(new Error("OIDC browser fixture shutdown timed out")),
      timeoutMs,
    );
    child.once("exit", (code) => {
      clearTimeout(timer);
      resolve(code);
    });
  });
}

async function readHandoff(paths: FixturePaths): Promise<OIDCBrowserHandoff> {
  return handoffSchema.parse(JSON.parse(await readFile(paths.handoff, "utf8")));
}

async function waitForHandoff(
  paths: FixturePaths,
  child: ChildProcessWithoutNullStreams,
  output: () => string,
): Promise<OIDCBrowserHandoff> {
  const deadline = Date.now() + startupTimeoutMs;
  while (Date.now() < deadline) {
    try {
      return await readHandoff(paths);
    } catch (error) {
      if (child.exitCode !== null) {
        throw new Error(
          `OIDC browser fixture exited before readiness (${child.exitCode})\n${output()}`,
          {
            cause: error,
          },
        );
      }
      await new Promise((resolve) => setTimeout(resolve, 250));
    }
  }
  throw new Error(`OIDC browser fixture readiness timed out\n${output()}`);
}

async function postControl<T>(
  controlURL: string,
  controlToken: string,
  pathName: string,
  body: unknown,
): Promise<T> {
  const response = await fetch(new URL(pathName, controlURL), {
    method: "POST",
    headers: { Authorization: `Bearer ${controlToken}`, "Content-Type": "application/json" },
    body: JSON.stringify(body),
    signal: AbortSignal.timeout(90_000),
  });
  if (!response.ok) {
    throw new Error(`Fixture control ${pathName} failed with ${response.status}`);
  }
  return (await response.json()) as T;
}

export async function startOIDCBrowserFixture(
  evidenceDirectory: string,
): Promise<OIDCBrowserFixture> {
  const repositoryRoot = path.resolve(process.cwd(), "..");
  const root = await mkdtemp(path.join(os.tmpdir(), "silo-oidc-browser-"));
  const paths = {
    root,
    handoff: path.join(root, "handoff.json"),
    stop: path.join(root, "stop"),
  } satisfies FixturePaths;
  const child = spawn(
    "go",
    [
      "test",
      "-tags=integration",
      "./internal/auth/integration",
      "-run=^TestPackagedOIDC_BrowserFixture$",
      "-count=1",
      "-v",
    ],
    {
      cwd: repositoryRoot,
      detached: true,
      env: {
        ...process.env,
        SILO_OIDC_BROWSER_HANDOFF: paths.handoff,
        SILO_OIDC_BROWSER_STOP: paths.stop,
        SILO_OIDC_BROWSER_PARENT_PID: String(process.pid),
      },
    },
  );
  let output = "";
  child.stdout.on("data", (chunk: Buffer) => {
    output += chunk.toString("utf8");
  });
  child.stderr.on("data", (chunk: Buffer) => {
    output += chunk.toString("utf8");
  });

  let handoff: OIDCBrowserHandoff;
  try {
    handoff = await waitForHandoff(paths, child, () => output);
  } catch (error) {
    if (child.exitCode === null) process.kill(-child.pid, "SIGINT");
    try {
      await waitForExit(child, 10_000);
    } catch {
      process.kill(-child.pid, "SIGKILL");
      await waitForExit(child, 10_000);
    }
    await rm(root, { recursive: true, force: true });
    throw error;
  }
  let stopped = false;
  const fixture: OIDCBrowserFixture = {
    handoff,
    async setMode(mode) {
      await postControl(handoff.control_url, handoff.control_token, "/mode", { mode });
    },
    async setProviderEnabled(enabled) {
      handoff = handoffSchema.parse(
        await postControl(handoff.control_url, handoff.control_token, "/provider", { enabled }),
      );
      fixture.handoff = handoff;
      return handoff;
    },
    async setPresentation(presentation) {
      handoff = handoffSchema.parse(
        await postControl(
          handoff.control_url,
          handoff.control_token,
          "/presentation",
          presentation,
        ),
      );
      fixture.handoff = handoff;
      return handoff;
    },
    async stop() {
      if (stopped) return;
      stopped = true;
      let exitCode: number | null = null;
      try {
        await writeFile(paths.stop, "stop\n", { mode: 0o600 });
        exitCode = await waitForExit(child, shutdownTimeoutMs);
      } catch (error) {
        process.kill(-child.pid, "SIGINT");
        try {
          await waitForExit(child, 10_000);
        } catch {
          process.kill(-child.pid, "SIGKILL");
          await waitForExit(child, 10_000);
        }
        throw error;
      } finally {
        await writeFile(path.join(evidenceDirectory, "fixture.log"), output, { mode: 0o600 });
        await rm(root, { recursive: true, force: true });
      }
      if (exitCode !== 0) {
        throw new Error(`OIDC browser fixture exited with code ${String(exitCode)}`);
      }
      await writeFile(
        path.join(evidenceDirectory, "cleanup.json"),
        `${JSON.stringify({ child_exit: "clean", fixture_temp_removed: true }, null, 2)}\n`,
        { mode: 0o600 },
      );
    },
  };
  return fixture;
}
