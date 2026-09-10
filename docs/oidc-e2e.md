# Packaged OIDC end-to-end gate

Run `scripts/test-oidc-e2e.sh` from the repository root. It runs all six backend integration tests and the six Chromium lifecycle scenarios from a clean harness state.

The gate requires Docker, the sibling packaged OIDC plugin executable, Corepack with pnpm, and Chromium. It sets `SILO_OIDC_E2E=1`; override `OIDC_PLUGIN_BIN` and `PLAYWRIGHT_CHROME_EXECUTABLE_PATH` when their locations differ. If Chromium is not installed on the host path, run the gate with Nix, for example `nix-shell -p chromium playwright-driver --run 'scripts/test-oidc-e2e.sh'`. Command logs are retained by the test runner, while browser evidence is redacted before persistence.
