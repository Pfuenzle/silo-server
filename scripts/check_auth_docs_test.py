#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.13"
# dependencies = []
# ///

# ─── How to run ───
# 1. Install uv (if not installed):
#      curl -LsSf https://astral.sh/uv/install.sh | sh
# 2. Run directly (no venv, no pip install needed):
#      uv run scripts/check_auth_docs_test.py
# 3. Or run with the standard library test runner:
#      python3 scripts/check_auth_docs_test.py
# ──────────────────

from __future__ import annotations

import shutil
import subprocess
import sys
import tempfile
import unittest
from dataclasses import dataclass
from pathlib import Path
from typing import final, override


PR_BODY_NAMES = (
    "proposed-core-01-identity-provisioning-pr.md",
    "proposed-core-02-session-authorization-audit-pr.md",
    "proposed-core-03-oauth-lifecycle-pr.md",
    "proposed-core-04-admin-plugins-ui-pr.md",
    "proposed-core-05-operator-docs-pr.md",
    "proposed-oidc-pr.md",
    "proposed-ldap-pr.md",
)


@dataclass(frozen=True, slots=True)
class Mutation:
    name: str
    relative_path: Path
    text: str
    category: str


@dataclass(frozen=True, slots=True)
class ReplacementMutation:
    name: str
    relative_path: Path
    old: str
    new: str
    category: str


MUTATIONS = (
    Mutation("absolute local path", Path("silo-server/docs/external-auth.md"), "Use /home/alice/silo/config.json.\n", "absolute-local-path"),
    Mutation("absolute opt path", Path("silo-server/docs/external-auth.md"), "Use /opt/silo/private/config.json.\n", "absolute-local-path"),
    Mutation("placeholder secret", Path("silo-plugin-auth-oidc/README.md"), "client_secret: <CLIENT_SECRET>\n", "secret-example"),
    Mutation("real secret", Path("silo-plugin-auth-ldap/README.md"), "bind_password: hunter2\n", "secret-example"),
    Mutation("evidence secret", Path("silo-server/.omo/evidence/task-19-external-oidc-ldap-auth/fixture.log"), "token: leaked-value\n", "secret-example"),
    Mutation("nested evidence secret", Path("silo-server/.omo/evidence/task-19-external-oidc-ldap-auth/nested/fixture.txt"), "api_key: leaked-value\n", "secret-example"),
    Mutation("hot reload", Path("silo-server/docs/external-auth.md"), "Changes hot reload without restart.\n", "hot-reload-claim"),
    Mutation("destructive uninstall", Path("silo-server/docs/external-auth.md"), "Uninstall the plugin to roll back authentication.\n", "destructive-lifecycle"),
    Mutation("destructive migration down", Path("silo-server/docs/external-auth.md"), "Run migration Down to delete external identities.\n", "destructive-lifecycle"),
    Mutation("insecure TLS", Path("silo-plugin-auth-ldap/README.md"), "Set insecure_skip_tls_verify: true.\n", "insecure-tls"),
    Mutation("email linking", Path("silo-server/docs/external-auth.md"), "Link accounts by matching email addresses.\n", "email-linking"),
    Mutation("OIDC role claims", Path("silo-plugin-auth-oidc/README.md"), "Map OIDC role claims to the Silo admin role.\n", "oidc-authorization"),
    Mutation("raw upstream reason", Path("silo-server/docs/external-auth.md"), "Log the raw upstream error reason for troubleshooting.\n", "raw-provider-reason"),
    Mutation("private networks default", Path("silo-plugin-auth-oidc/README.md"), "Enable allow_private_networks by default.\n", "ssrf-policy"),
    Mutation("weak OIDC protocol", Path("silo-plugin-auth-oidc/README.md"), "Use PKCE plain and skip nonce validation.\n", "oidc-protocol"),
    Mutation("mutable LDAP identity", Path("silo-plugin-auth-ldap/README.md"), "Use email as the LDAP subject identifier.\n", "ldap-identity"),
    Mutation("stale sessions", Path("silo-server/docs/external-auth.md"), "Keep existing sessions active after demotion.\n", "stale-session"),
    Mutation("absolute srv path", Path("silo-server/docs/external-auth.md"), "Use /srv/silo/config.json.\n", "absolute-local-path"),
    Mutation("absolute etc path", Path("silo-plugin-auth-ldap/README.md"), "Mount /etc/company/ca.pem.\n", "absolute-local-path"),
    Mutation("concrete IdP host", Path("silo-plugin-auth-oidc/README.md"), "Issuer is https://login.company.invalid/application/o/silo/.\n", "non-official-idp-host"),
    Mutation("concrete IdP IP", Path("silo-plugin-auth-oidc/README.md"), "Issuer is https://192.0.2.20/application/o/silo/.\n", "non-official-idp-host"),
    Mutation("private key", Path("silo-server/.omo/evidence/task-19-external-oidc-ldap-auth/fixture.log"), "-----BEGIN PRIVATE KEY-----\n", "secret-example"),
    Mutation("OIDC group admin paraphrase", Path("silo-plugin-auth-oidc/README.md"), "Members of the Authentik admins group become Silo administrators.\n", "oidc-authorization"),
    Mutation("accept every certificate", Path("silo-plugin-auth-ldap/README.md"), "Accept every certificate from the LDAP server.\n", "insecure-tls"),
    Mutation("skip server verify", Path("silo-plugin-auth-ldap/README.md"), "Skip server certificate checks during bind.\n", "insecure-tls"),
    Mutation("false review lanes", Path("silo-server/.omo/evidence/task-19-external-oidc-ldap-auth/fixture.log"), "All five review lanes passed.\n", "review-claim"),
    Mutation("proposed PR placeholder", Path("silo-server/.omo/evidence/task-19-external-oidc-ldap-auth/proposed-core-01-identity-provisioning-pr.md"), "[paste actual output]\n", "pr-placeholder"),
    Mutation("proposed PR local evidence link", Path("silo-server/.omo/evidence/task-19-external-oidc-ldap-auth/proposed-oidc-pr.md"), "[local receipt](../task-18/receipt.md)\n", "pr-body-contract"),
)


REPLACEMENT_MUTATIONS = (
    ReplacementMutation(
        "core keyword stuffing",
        Path("silo-server/docs/external-auth.md"),
        "external_local_password_login_enabled=false",
        "external_local_password_login_enabled=true\n```\nexternal_local_password_login_enabled=false\n```",
        "required-placement",
    ),
    ReplacementMutation(
        "OIDC issuer term wrong placement",
        Path("silo-plugin-auth-oidc/README.md"),
        "Set issuer mode to **Each provider has a different issuer, based on the application slug**.",
        "Set issuer mode to a provider-specific value.\n\nEach provider has a different issuer, based on the application slug.",
        "required-placement",
    ),
    ReplacementMutation(
        "LDAP identifier keyword stuffing",
        Path("silo-plugin-auth-ldap/README.md"),
        "| `subject_attribute` | `uid` |",
        "| `subject_attribute` | `entryUUID` |\n\nsubject_attribute=uid",
        "required-placement",
    ),
    ReplacementMutation(
        "OIDC boundary negation",
        Path("silo-server/docs/external-auth.md"),
        "OIDC supplies external authentication only.",
        "OIDC does not supply external authentication only.",
        "required-placement",
    ),
    ReplacementMutation(
        "Todo18 receipt hash drift",
        Path("silo-server/.omo/evidence/task-19-external-oidc-ldap-auth/proposed-oidc-pr.md"),
        "77e24f719d83669016393f1f32a47b4bd807da5c4fd74087bb3adc84e1e18d88",
        "0000000000000000000000000000000000000000000000000000000000000000",
        "pr-body-contract",
    ),
    ReplacementMutation(
        "owner issue instruction removed",
        Path("silo-server/.omo/evidence/task-19-external-oidc-ldap-auth/proposed-core-01-identity-provisioning-pr.md"),
        "The owner must create and link a real issue before opening this PR.",
        "Issue coordination happens later.",
        "pr-body-contract",
    ),
    ReplacementMutation(
        "OIDC source archive receipt removed",
        Path("silo-server/.omo/evidence/task-19-external-oidc-ldap-auth/proposed-oidc-pr.md"),
        "task20_oidc_source_tar_sha256=60807d19479c793253c6da03cc9983fcc7bc3d67ee761b3c8781b31e94cb685b",
        "OIDC source archive receipt unavailable",
        "pr-body-contract",
    ),
    ReplacementMutation(
        "LDAP PR output marker removed",
        Path("silo-server/.omo/evidence/task-19-external-oidc-ldap-auth/proposed-ldap-pr.md"),
        "## Sanitized key output\n\n```text",
        "## Sanitized key output\n\nsanitized output",
        "pr-body-contract",
    ),
)


@final
class AuthDocsCheckerTest(unittest.TestCase):
    @override
    def __init__(self, methodName: str = "runTest") -> None:
        super().__init__(methodName)
        self.server_root = Path(__file__).resolve().parents[1]
        self.workspace = self.server_root.parent
        self.temporary = tempfile.TemporaryDirectory()
        self.fixture_root = Path(self.temporary.name)

    @override
    def setUp(self) -> None:
        for relative_path in (
            Path("silo-server/README.md"),
            Path("silo-server/docs/external-auth.md"),
            Path("silo-plugin-auth-oidc/README.md"),
            Path("silo-plugin-auth-ldap/README.md"),
            *(Path("silo-server/.omo/evidence/task-19-external-oidc-ldap-auth") / name for name in PR_BODY_NAMES),
        ):
            source = self.workspace / relative_path
            target = self.fixture_root / relative_path
            _ = target.parent.mkdir(parents=True, exist_ok=True)
            _ = shutil.copyfile(source, target)
        evidence = self.fixture_root / "silo-server/.omo/evidence/task-19-external-oidc-ldap-auth/fixture.log"
        _ = evidence.parent.mkdir(parents=True, exist_ok=True)
        _ = evidence.write_text("sanitized fixture\n", encoding="utf-8")
        nested_evidence = evidence.parent / "nested/fixture.txt"
        _ = nested_evidence.parent.mkdir(parents=True, exist_ok=True)
        _ = nested_evidence.write_text("sanitized nested fixture\n", encoding="utf-8")

    @override
    def tearDown(self) -> None:
        self.temporary.cleanup()

    def run_checker(self, include_evidence: bool = False) -> subprocess.CompletedProcess[str]:
        command = [sys.executable, str(self.server_root / "scripts/check-auth-docs.py"), "--server-root", str(self.fixture_root / "silo-server")]
        if include_evidence:
            command.extend(["--evidence-dir", str(self.fixture_root / "silo-server/.omo/evidence/task-19-external-oidc-ldap-auth")])
        return subprocess.run(
            command,
            check=False,
            capture_output=True,
            text=True,
        )

    def test_final_documentation_passes(self) -> None:
        result = self.run_checker(include_evidence=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_forbidden_mutations_fail_with_named_category(self) -> None:
        for mutation in MUTATIONS:
            with self.subTest(mutation.name):
                path = self.fixture_root / mutation.relative_path
                original = path.read_text(encoding="utf-8")
                try:
                    _ = path.write_text(original + "\n" + mutation.text, encoding="utf-8")
                    result = self.run_checker(mutation.relative_path.parts[1] == ".omo")
                finally:
                    _ = path.write_text(original, encoding="utf-8")
                self.assertNotEqual(result.returncode, 0, mutation.name)
                self.assertIn(mutation.category, result.stdout + result.stderr)

    def test_wrong_placement_and_negation_fail(self) -> None:
        for mutation in REPLACEMENT_MUTATIONS:
            with self.subTest(mutation.name):
                path = self.fixture_root / mutation.relative_path
                original = path.read_text(encoding="utf-8")
                self.assertIn(mutation.old, original)
                try:
                    _ = path.write_text(original.replace(mutation.old, mutation.new, 1), encoding="utf-8")
                    result = self.run_checker(mutation.relative_path.parts[1] == ".omo")
                finally:
                    _ = path.write_text(original, encoding="utf-8")
                self.assertNotEqual(result.returncode, 0, mutation.name)
                self.assertIn(mutation.category, result.stdout + result.stderr)


if __name__ == "__main__":
    _ = unittest.main()
