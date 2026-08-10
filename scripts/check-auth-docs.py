#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.13"
# dependencies = []
# ///

# ─── How to run ───
# 1. Install uv (if not installed):
#      curl -LsSf https://astral.sh/uv/install.sh | sh
# 2. Run directly (no venv, no pip install needed):
#      uv run scripts/check-auth-docs.py
# 3. Or run with Python:
#      python3 scripts/check-auth-docs.py
# ──────────────────

from __future__ import annotations

import re
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Final, override


@dataclass(frozen=True, slots=True)
class Document:
    label: str
    path: Path
    text: str


@dataclass(frozen=True, slots=True)
class Rule:
    category: str
    pattern: re.Pattern[str]


@dataclass(frozen=True, slots=True)
class Placement:
    label: str
    start: str
    end: str
    snippets: tuple[str, ...]


@dataclass(frozen=True, slots=True)
class DocumentReadError(Exception):
    path: Path
    cause: OSError | UnicodeError

    @override
    def __str__(self) -> str:
        return f"missing auth document: {self.path}: {self.cause}"


REQUIRED: Final[dict[str, tuple[str, ...]]] = {
    "core": (
        "{SILO_PUBLIC_URL}/api/v1/auth/oauth/{installation_id}/callback",
        "local Silo administrator",
        "local_password_login_enabled=false",
        "deterministic, non-PII synthetic address",
        "OIDC roles, groups, entitlements",
        "external_groups_v1",
        "X-Silo-Restart-Required: true",
        "same installation",
        "Migration `Down` operations",
        "Guarded uninstall",
        "owner explicitly authorizes",
    ),
    "oidc": (
        "https://docs.goauthentik.io/add-secure-apps/providers/oauth2/create-oauth2-provider/",
        "https://docs.goauthentik.io/add-secure-apps/providers/property-mappings/",
        "https://<AUTHENTIK_HOST>/application/o/<application_slug>/",
        "{SILO_PUBLIC_URL}/api/v1/auth/oauth/{installation_id}/callback",
        "openid`, `profile`, and `email`",
        "offline_access",
        "PKCE S256",
        "nonce",
        "JWKS",
        "authorization mode at `none`",
        "Applications → Applications → New Provider",
        "Each provider has a different issuer, based on the application slug",
    ),
    "ldap": (
        "https://docs.goauthentik.io/add-secure-apps/providers/ldap/create-ldap-provider/",
        "https://docs.goauthentik.io/add-secure-apps/outposts/",
        "LDAPS or StartTLS",
        "<AUTHENTIK_BASE_DN>",
        "Search full LDAP directory",
        "https://docs.goauthentik.io/add-secure-apps/providers/ldap/create-ldap-provider/#assign-the-ldap-search-permission-to-the-service-account",
        "| `subject_attribute` | `uid` |",
        "| `group_id_attribute` | `uid` |",
        "<CA_CERT_PATH>",
        "external_groups_v1",
        "least privilege",
        "exact `admin` mapping",
        "CA rotation",
        "timeout",
    ),
}

PR_BODY_NAMES: Final[tuple[str, ...]] = (
    "proposed-core-01-identity-provisioning-pr.md",
    "proposed-core-02-session-authorization-audit-pr.md",
    "proposed-core-03-oauth-lifecycle-pr.md",
    "proposed-core-04-admin-plugins-ui-pr.md",
    "proposed-core-05-operator-docs-pr.md",
    "proposed-oidc-pr.md",
    "proposed-ldap-pr.md",
)

PR_BODY_REQUIRED: Final[tuple[str, ...]] = (
    "Part of: BLOCKED_OWNER_INPUT",
    "The owner must create and link a real issue before opening this PR.",
    "## Dependencies and blocks",
    "## One concern",
    "## Risks",
    "## Migration and binary rollback",
    "## Exact verification",
    "## Sanitized key output",
    "## Source receipts",
    "task18_source_hash=77e24f719d83669016393f1f32a47b4bd807da5c4fd74087bb3adc84e1e18d88",
    "## Owner and catalog blockers",
    "No commit, push, release, PR, or publication was performed.",
    "### AI Disclosure",
)

PR_BODY_SPECIFIC: Final[dict[str, tuple[str, ...]]] = {
    **{name: ("task20_core_proof_sha256=89ea38a15bdd1572ad23b8dd7e5fd622cc17db3c84b16d966352a4b11eca7d66",) for name in PR_BODY_NAMES[:5]},
    "proposed-oidc-pr.md": ("task20_oidc_source_tar_sha256=60807d19479c793253c6da03cc9983fcc7bc3d67ee761b3c8781b31e94cb685b", "task20_qemu_smoke_sha256=27a718b601b899ae9e489675dc9234993a7f31fe3ccf9eea61b900e31e99550f"),
    "proposed-ldap-pr.md": ("task20_ldap_source_tar_sha256=d3126add5dee5fb00bc0ae9b4951a190d4bd9d15d9564175e094756c2ca2f962", "task20_qemu_smoke_sha256=27a718b601b899ae9e489675dc9234993a7f31fe3ccf9eea61b900e31e99550f"),
}


PLACEMENTS: Final[tuple[Placement, ...]] = (
    Placement("core", "<!-- auth-doc:runtime-boundaries:start -->", "<!-- auth-doc:runtime-boundaries:end -->", ("global_runtime_config=lazy_plugin_reload", "binding_enable_or_mode=full_silo_restart", "external_local_password_login_enabled=false", "break_glass_provider=local", "oidc_authorization_mode=none")),
    Placement("core", "## First login and identity collisions", "## OIDC authorization policy", ("exactly one login account with `local_password_login_enabled=false`", "one primary household profile", "only the separately created break-glass administrator has local-password access")),
    Placement("core", "## OIDC authorization policy", "## LDAP exact group mappings", ("OIDC supplies external authentication only.", "do not grant Silo access, a local role, local-password authentication, or administrator status")),
    Placement("oidc", "## Authentik confidential client setup", "## Silo configuration fields", ("Applications → Applications → New Provider", "Select **OAuth2/OIDC**", "Set issuer mode to **Each provider has a different issuer, based on the application slug**")),
    Placement("oidc", "<!-- auth-doc:oidc-values:start -->", "<!-- auth-doc:oidc-values:end -->", ("issuer_url=https://<AUTHENTIK_HOST>/application/o/<application_slug>/", "discovery_url=https://<AUTHENTIK_HOST>/application/o/<application_slug>/.well-known/openid-configuration", "callback_url={SILO_PUBLIC_URL}/api/v1/auth/oauth/{installation_id}/callback", "client_id=<AUTHENTIK_CLIENT_ID>")),
    Placement("ldap", "<!-- auth-doc:authentik-ldap-config:start -->", "<!-- auth-doc:authentik-ldap-config:end -->", ("| `user_base_dn` | `ou=users,<AUTHENTIK_BASE_DN>` |", "| `group_base_dn` | `ou=groups,<AUTHENTIK_BASE_DN>` |", "| `user_filter` | `(&(objectClass=user)(cn={username}))` |", "| `group_filter` | `(&(objectClass=group)(member=cn={username},ou=users,<AUTHENTIK_BASE_DN>))` |", "| `username_attribute` | `cn` |", "| `subject_attribute` | `uid` |", "| `group_id_attribute` | `uid` |", "| `ca_file` | `<CA_CERT_PATH>` |")),
)


RULES: Final[tuple[Rule, ...]] = (
    Rule("absolute-local-path", re.compile(r"(?<![\w`])/(?:home|Users|blyatflix|tmp|private/tmp|var/tmp|opt|srv|etc)/[^\s`]+", re.IGNORECASE)),
    Rule("secret-example", re.compile(r"(?:\b(?:client_secret|bind_password|password|api[_ -]?key|token)\s*[:=]\s*(?:<[^>\n]+>|['\"]?[A-Za-z0-9][^\s`,'\"]*)|\bAuthorization:\s*(?:Bearer|Basic)\s+\S+|-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----)", re.IGNORECASE)),
    Rule("hot-reload-claim", re.compile(r"\b(?:hot[ -]?reload(?:s|ed)?|without (?:a )?restart|no restart (?:is )?required|appl(?:y|ies|ied) immediately)\b", re.IGNORECASE)),
    Rule("destructive-lifecycle", re.compile(r"\b(?:uninstall(?: the plugin)? to (?:roll back|disable)|delete (?:external )?(?:identities|auth data|audit records) to|run (?:the )?migration Down to (?:delete|remove|drop))\b", re.IGNORECASE)),
    Rule("insecure-tls", re.compile(r"\b(?:insecure_skip_tls_verify|skip (?:TLS|server certificate) (?:verification|checks?)|disable certificate verification|accept (?:every|all|any) certificates?|trust (?:every|all|any) certificates?|ignore certificate errors?|plaintext LDAP in production)\b", re.IGNORECASE)),
    Rule("email-linking", re.compile(r"\b(?:link|merge|attach) accounts? by (?:matching )?email(?: addresses)?\b", re.IGNORECASE)),
    Rule("oidc-authorization", re.compile(r"(?:\b(?:map|trust|use) OIDC (?:role|roles|group|groups|claim|claims|entitlement|entitlements).{0,60}(?:admin|role|access|authoriz)|\bAuthentik\s+\w*(?:admin|group)\w*\s+(?:group\s+)?(?:become|grant|map to).{0,40}(?:Silo\s+)?admin)", re.IGNORECASE)),
    Rule("raw-provider-reason", re.compile(r"^\s*(?:[-*]\s*)?(?:log|display|expose|record|include) (?:the )?raw (?:upstream|provider|LDAP) (?:error )?(?:reason|response|body|details)", re.IGNORECASE | re.MULTILINE)),
    Rule("ssrf-policy", re.compile(r"\b(?:enable|set) allow_private_networks (?:to true )?(?:by default|for all|globally)\b", re.IGNORECASE)),
    Rule("oidc-protocol", re.compile(r"\b(?:PKCE plain|skip nonce validation|disable (?:nonce|JWKS|signature) validation|use HTTP discovery)\b", re.IGNORECASE)),
    Rule("ldap-identity", re.compile(r"\buse (?:email|username|display name|DN) as (?:the )?LDAP subject", re.IGNORECASE)),
    Rule("stale-session", re.compile(r"\b(?:keep|leave) (?:existing|old|stale) sessions active after (?:demotion|mapping changes?|provider disablement)\b", re.IGNORECASE)),
    Rule("non-official-idp-host", re.compile(r"(?:https?://(?!(?:docs\.goauthentik\.io|github\.com|<AUTHENTIK_HOST>)(?:[/:>]))(?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,}(?=[/:])|\b(?:\d{1,3}\.){3}\d{1,3}\b)", re.IGNORECASE)),
    Rule("required-placement", re.compile(r"external_local_password_login_enabled\s*=\s*true", re.IGNORECASE)),
    Rule("review-claim", re.compile(r"\b(?:all )?five (?:(?:independent|read-only) )?(?:review )?lanes? (?:passed|reviewed)\b", re.IGNORECASE)),
    Rule("pr-placeholder", re.compile(r"(?:\[(?:paste|replace|insert)[^\]]*\]|\[TODO(?:\]|:|\s))", re.IGNORECASE)),
)


def load_documents(server_root: Path, evidence_dir: Path | None) -> tuple[Document, ...]:
    workspace = server_root.parent
    paths = (
        ("server-readme", server_root / "README.md"),
        ("core", server_root / "docs/external-auth.md"),
        ("oidc", workspace / "silo-plugin-auth-oidc/README.md"),
        ("ldap", workspace / "silo-plugin-auth-ldap/README.md"),
    )
    documents: list[Document] = []
    for label, path in paths:
        try:
            text = path.read_text(encoding="utf-8")
        except OSError as error:
            raise DocumentReadError(path=path, cause=error) from error
        documents.append(Document(label=label, path=path, text=text))
    if evidence_dir is not None:
        evidence_root = evidence_dir
        for path in sorted(candidate for candidate in evidence_root.rglob("*") if candidate.is_file()):
            try:
                text = path.read_text(encoding="utf-8")
            except (OSError, UnicodeError) as error:
                raise DocumentReadError(path=path, cause=error) from error
            documents.append(Document(label=f"evidence:{path.name}", path=path, text=text))
    return tuple(documents)


def check_documents(documents: tuple[Document, ...]) -> tuple[str, ...]:
    failures: list[str] = []
    by_label = {document.label: document for document in documents}
    if any(f"evidence:{name}" in by_label for name in PR_BODY_NAMES):
        for name in PR_BODY_NAMES:
            label = f"evidence:{name}"
            if label not in by_label:
                failures.append(f"pr-body-contract: missing proposed body {name}")
                continue
            document = by_label[label]
            for snippet in PR_BODY_REQUIRED:
                if snippet not in document.text:
                    failures.append(f"pr-body-contract: {document.path}: missing {snippet!r}")
            for snippet in PR_BODY_SPECIFIC[name]:
                if snippet not in document.text:
                    failures.append(f"pr-body-contract: {document.path}: missing {snippet!r}")
            for start, end, fence in (("## Exact verification", "## Sanitized key output", "```bash"), ("## Sanitized key output", "## Source receipts", "```text")):
                section_start = document.text.find(start)
                section_end = document.text.find(end, section_start + len(start))
                if section_start < 0 or section_end < 0 or fence not in document.text[section_start:section_end]:
                    failures.append(f"pr-body-contract: {document.path}: missing {fence!r} from {start!r}")
            if ".omo" in document.text or re.search(r"\]\([^)]*task-(?:18|19|20)[^)]*\)", document.text):
                failures.append(f"pr-body-contract: {document.path}: local evidence link is forbidden")
            if name in {"proposed-oidc-pr.md", "proposed-ldap-pr.md"}:
                for snippet in ("linux_arm64_qemu_runtime=pass", "darwin_amd64_runtime=unverified", "darwin_arm64_runtime=unverified"):
                    if snippet not in document.text:
                        failures.append(f"pr-body-contract: {document.path}: missing {snippet!r}")
    readme = by_label["server-readme"]
    if "[external authentication operator and security guide](docs/external-auth.md)" not in readme.text:
        failures.append(f"required-content: {readme.path}: missing core guide link")
    for label, snippets in REQUIRED.items():
        if label.startswith("evidence:") and label not in by_label:
            continue
        document = by_label[label]
        for snippet in snippets:
            if snippet not in document.text:
                failures.append(f"required-content: {document.path}: missing {snippet!r}")
    for placement in PLACEMENTS:
        if placement.label.startswith("evidence:") and placement.label not in by_label:
            continue
        document = by_label[placement.label]
        startIndex = document.text.find(placement.start)
        endIndex = document.text.find(placement.end, startIndex + len(placement.start))
        if startIndex < 0 or endIndex < 0:
            failures.append(f"required-placement: {document.path}: missing boundary {placement.start!r}")
            continue
        section = document.text[startIndex:endIndex]
        for snippet in placement.snippets:
            if snippet not in section:
                failures.append(f"required-placement: {document.path}: {snippet!r} is missing from {placement.start!r}")
    for document in documents:
        if document.label == "server-readme":
            continue
        for rule in RULES:
            match = rule.pattern.search(document.text)
            if match is not None:
                line = document.text.count("\n", 0, match.start()) + 1
                failures.append(f"{rule.category}: {document.path}:{line}: {match.group(0)!r}")
    return tuple(failures)


def parse_paths(arguments: list[str]) -> tuple[Path, Path | None]:
    server_root = Path(__file__).resolve().parents[1]
    evidence_dir: Path | None = None
    while arguments:
        flag, *arguments = arguments
        if not arguments or flag not in {"--server-root", "--evidence-dir"}:
            print("usage: check-auth-docs.py [--server-root PATH] [--evidence-dir PATH]", file=sys.stderr)
            raise SystemExit(2)
        value, *arguments = arguments
        if flag == "--server-root":
            server_root = Path(value)
        else:
            evidence_dir = Path(value)
    return server_root, evidence_dir


def main() -> int:
    server_root, evidence_dir = parse_paths(sys.argv[1:])
    try:
        failures = check_documents(load_documents(server_root.resolve(), evidence_dir.resolve() if evidence_dir else None))
    except DocumentReadError as error:
        print(f"auth docs check failed: {error}", file=sys.stderr)
        return 1
    if failures:
        print("auth docs check failed:", file=sys.stderr)
        for failure in failures:
            print(f"- {failure}", file=sys.stderr)
        return 1
    print("auth docs check passed: core, OIDC, and LDAP documentation satisfy the security contract")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
