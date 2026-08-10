# External authentication operator and security guide

This guide covers Silo's generic OpenID Connect (OIDC) and LDAP authentication plugins. Commands assume the repository root is the current working directory. Provider-specific fields are documented in each plugin README.

## Security and architecture boundaries

- The OIDC plugin performs Authorization Code flow and identity verification. The LDAP plugin performs TLS-protected bind and search operations and returns stable direct-group IDs.
- Silo owns external identity associations, account/profile provisioning, local roles, access groups, audit records, and session revocation. Plugins cannot assign Silo role IDs or access-group IDs.
- An external identity is the installation ID plus the provider's stable subject. OIDC uses verified `(issuer, sub)`; LDAP uses the configured immutable subject attribute.
- OIDC roles, groups, entitlements, and similarly named custom claims are identity-provider metadata only. Silo never uses them for authorization.
- Only an LDAP binding in `external_groups_v1` mode can reconcile authorization, and Silo maps exact stable external group IDs to local targets.
- External-provider failures return generic login errors. Logs, UI messages, support bundles, and evidence must not contain authorization codes, tokens, client or bind secrets, raw LDAP filters, directory payloads, external subjects, or upstream failure bodies.
- Externally provisioned accounts are created with `local_password_login_enabled=false`. They authenticate only through their bound external provider. The separate break-glass administrator uses the local provider.

<!-- auth-doc:runtime-boundaries:start -->
```text
global_runtime_config=lazy_plugin_reload
binding_enable_or_mode=full_silo_restart
external_local_password_login_enabled=false
break_glass_provider=local
oidc_authorization_mode=none
```
<!-- auth-doc:runtime-boundaries:end -->

## Prerequisites and break-glass access

Before installing an external provider:

1. Create and test a local Silo administrator with a strong, separately stored password.
2. Confirm that local login and `/api/v1/auth/me` work from the recovery network path.
3. Keep this account local-only. Do not reuse its email, username, or password in the identity provider.
4. Back up PostgreSQL and the plugin artifact/checksum currently in service.
5. Confirm TLS trust, DNS, time synchronization, and bounded connectivity from the Silo host to the provider.

Repeat the local login test after every binding change, restart, disable, re-enable, upgrade, or rollback. External authentication is not a replacement for tested local recovery access.

## Public URL and OIDC callback

Set `SILO_PUBLIC_URL` to the browser-visible HTTPS origin for Silo, including a deployment path prefix when one exists and excluding a trailing slash. The value must resolve back to Silo through the same trusted reverse-proxy route users access.

The callback registered at the provider is exactly:

```text
{SILO_PUBLIC_URL}/api/v1/auth/oauth/{installation_id}/callback
```

The scheme, authority, optional path prefix, installation ID, and path must match exactly. Silo derives this callback; neither the plugin nor an operator-configured redirect override is accepted. Without `SILO_PUBLIC_URL`, the OIDC provider is hidden and its initiation route fails closed.

## Install, configure, bind, and restart

Use **Administration → Plugins** for the supported workflow:

1. Verify the downloaded artifact against its published SHA-256 checksum, then upload and install it. Record the resulting installation ID.
2. Configure the plugin's global `oidc` or `ldap` section. Enter secrets only in Silo's secret controls; do not put them in commands, tickets, or evidence.
3. For OIDC, register the exact callback above and bind capability `oidc` with automatic provisioning and authorization mode `none`. OIDC never enables local-password authentication or grants a local role.
4. For LDAP, configure verified LDAPS or StartTLS and bind capability `ldap`. Use `external_groups_v1` only after exact mappings have been reviewed; otherwise leave external authorization disabled.
5. Save global runtime configuration. Silo stops that plugin process; the next provider use starts it lazily with the saved configuration. This action does not require a full Silo restart.
6. Save binding enablement or authorization-mode changes. A successful binding response reports `X-Silo-Restart-Required: true`.
7. Restart the full Silo server from the admin restart action or deployment supervisor after a binding enablement or mode change.
8. Confirm provider discovery on the login page, test one non-privileged external account, then re-test local break-glass login.

The equivalent API sequence, useful for audit correlation but not for embedding secrets in shell history, is:

```text
POST /api/v1/admin/plugins/uploads/chunked
PUT  /api/v1/admin/plugins/installations/{installation_id}/config
PUT  /api/v1/admin/plugins/installations/{installation_id}/auth-binding
PUT  /api/v1/admin/plugins/installations/{installation_id}/auth-group-mappings
POST /api/v1/admin/server/restart
```

The mapping request is LDAP-only and optional. Use the UI or an authenticated client that reads secrets from a protected secret store.

## First login and identity collisions

On the first successful external login, Silo creates exactly one login account with `local_password_login_enabled=false` and one primary household profile, then stores the immutable external identity association. A primary profile is not the server-wide administrator role. The external account cannot fall back to the local provider; only the separately created break-glass administrator has local-password access.

Silo never uses email, username, display name, LDAP DN, or an OIDC claim to link or merge accounts. If an asserted email is already owned by another account, that account remains unchanged and the new external identity receives a deterministic, non-PII synthetic address. Linking and unlinking are not supported. Repeated login uses the stored installation-and-subject association.

## OIDC authorization policy

OIDC supplies external authentication only. Keep the binding's authorization mode at `none`. Authentik groups, scope mappings, role claims, entitlements, and custom claims do not grant Silo access, a local role, local-password authentication, or administrator status. Manage local Silo roles and access groups in Silo.

## LDAP exact group mappings

The LDAP plugin emits direct groups in the `silo.external_groups.v1` envelope. Silo compares each emitted stable ID exactly; matching is case-sensitive and does not expand wildcards, names, aliases, or nested memberships.

Each mapping may target a local access group, the local `user` role, or the local `admin` role. No matching mapping means ordinary `user` plus the default access group. Conflicting targets fail login without changing authorization.

Treat an `admin` mapping as a privileged access-control rule:

- Map only a dedicated, tightly controlled directory group whose stable ID has been verified from the configured group ID attribute.
- Never map a display name, mutable DN, broad workforce group, wildcard, or nested-only group.
- Require independent review and test both membership addition and removal.
- Remember that LDAP is authoritative after every successful login. Manual local edits to an LDAP-managed account are overwritten by the next successful LDAP login.

## Promotion, demotion, audit, and sessions

On successful LDAP login, Silo computes the exact desired role/access group, writes any change and its old/new values to the durable external-authorization audit, revokes prior access, refresh, and compatibility sessions, then issues the new session. Mapping edits and provider disablement fail closed: affected authoritative accounts are demoted to `user` with the default access group and their sessions are revoked.

Review the external-authorization audit after every mapping change. Validate that an old administrator access token and refresh token are rejected immediately after demotion. Re-authentication is required after any authorization change.

## Rollout checklist

- [ ] Local break-glass administrator login and recovery route tested.
- [ ] Database backup and current plugin artifact/checksum retained.
- [ ] `SILO_PUBLIC_URL`, reverse proxy, DNS, TLS trust, and clock verified.
- [ ] Provider configured with a dedicated client or least-privilege service bind.
- [ ] OIDC exact callback or LDAP stable subject/group attributes reviewed.
- [ ] OIDC authorization mode is `none`; LDAP exact mappings have a second reviewer.
- [ ] Global runtime configuration saved and lazy plugin reload tested.
- [ ] Binding enablement/mode saved and the full Silo server restarted.
- [ ] One ordinary external user produced one account and one primary profile.
- [ ] Email collision test left the existing local account unchanged.
- [ ] Promotion/demotion, audit rows, and stale-session rejection tested when LDAP mappings are enabled.
- [ ] Provider outage and generic error behavior tested without recording sensitive details.
- [ ] Disable, re-enable, and same-installation binary rollback rehearsed.

## Disable, re-enable, and rollback

To stop new external login, disable the auth binding or installation and restart Silo. Provider sessions are revoked; durable accounts, profiles, external identity associations, mappings, and audits remain. Confirm local break-glass access before and after the restart.

To re-enable, use the same installation, re-check configuration and mappings, enable the binding, restart the full Silo server, and test a non-privileged login. Keeping the installation ID preserves identity ownership.

For package rollback, replace only the plugin binary with the previous checksum-verified compatible artifact on the same installation, then restart. Do not uninstall and reinstall: a new installation identity is not a rollback and may orphan the existing external identity relationship.

Core database rollback is not part of provider rollback. Migration `Down` operations that could remove external identity, provenance, mapping, audit, or encrypted OAuth state data must fail closed and must not be used as an authentication recovery procedure. Restore the compatible Silo binary and database backup together under the core release rollback procedure.

## Guarded uninstall

Silo rejects uninstall while an installation has external identities, provider sessions, mappings, or audit records. This guard is intentional. Disable or replace the version instead. Destructive remediation requires a separately reviewed data-retention and identity migration procedure outside this guide; never delete rows merely to bypass the guard.

## Troubleshooting safely

| Symptom | Safe checks |
| --- | --- |
| OIDC provider missing | Confirm `SILO_PUBLIC_URL`, saved global config, lazy plugin startup, enabled binding, successful full restart after binding changes, and local provider list. |
| Redirect mismatch | Compare the provider entry byte-for-byte with the callback pattern and current installation ID. |
| Discovery or JWKS failure | Confirm HTTPS issuer, provider-specific discovery document, trusted CA, DNS, clock, and bounded egress. |
| LDAP unavailable | Confirm LDAPS/StartTLS mode, TLS server name, CA chain, service account read scope, bases, filters, and timeout budget. |
| LDAP user denied | Confirm exactly one user search result, stable subject attribute, direct groups, and non-conflicting exact mappings. |
| Access changed unexpectedly | Review mapping history and external-authorization audit; verify current direct group IDs and stale-session rejection. |
| Provider rollback failed | Confirm the prior verified binary is on the same installation and that Silo was restarted. |

Record timestamps, Silo/plugin versions, installation ID, status class, and a correlation ID. Do not record raw upstream reasons or copy provider responses. Redact cookies, authorization headers, codes, state, tokens, secrets, LDAP DNs/filters, directory entries, and synthetic addresses before sharing logs.

## Local documentation verification

These retained checks use local fake OIDC/LDAP fixtures and no production identity provider:

```bash
make verify-local-paths
python3 scripts/check-auth-docs.py
python3 scripts/check_auth_docs_test.py
go test -tags=integration -count=1 -v ./internal/auth/integration -run '^TestDocumentedAuthSetup_'
```

The integration test requires the same bounded local container/build prerequisites as the packaged auth integration suite.

## Pull-request order and evidence

Keep repositories independently reviewable and merge in dependency order:

1. Silo core identity/provisioning, session provenance, authorization/audit, lifecycle guard, and migrations.
2. OIDC plugin protocol implementation and package.
3. LDAP plugin protocol, group envelope, and package.
4. This operator documentation and plugin README documentation after the behavior it describes is available.
5. Catalog entries only after both canonical repositories and checksum-bearing releases are owner-approved and available.

Canonical repository ownership, release publication, and catalog publication remain blocked until a Silo owner explicitly authorizes them. Documentation and local artifacts do not imply publication.

Each repository contains a complete `.github/PULL_REQUEST_TEMPLATE.md`. Completed proposed bodies and actual Todo18 receipts are retained under the Todo19 evidence directory. The disclosure fields and evidence requirements come from [AI-assisted contributions](ai-contributions.md). Never claim a test, release, repository, catalog entry, owner approval, or review lane that did not occur.
