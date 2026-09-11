# Live TV API

Live TV is a native web API under `/api/v1/livetv`. It is authenticated by the
normal access-token middleware. Viewer reads also require `X-Profile-Id`; the
existing viewer-access middleware validates that the profile belongs to the
account and resolves its library allow-list. No Live TV route is mounted under
the Jellyfin-compatible API.

## Capability

`GET /api/v1/livetv/capability` returns `200`:

```json
{
  "schema_version": 1,
  "enabled": true,
  "playback_available": true,
  "library_types": ["livetv"],
  "features": ["library_sources", "channels", "programme_details", "guide_window"]
}
```

`enabled` is false when the server has no database-backed Live TV repository.
`playback_available` is true when the API has an authority-backed playback
service and an eligible proxy origin. It is false when those dependencies are
unavailable, in which case playback resolution returns `503`.

## Libraries and sources

An admin can create a Live TV library through the existing
`POST /api/v1/libraries/` endpoint with `type: "livetv"`, `name`, and an empty
`paths` array. Filesystem roots are required for every other library type.

Source administration requires the acting-admin gate (admin account and, when
declared, the account's primary profile). In the final native router composition
these routes are mounted outside the general `/libraries` route and retain the
top-level `/api/v1/livetv` prefix:

| Method | Route | Success |
|---|---|---|
| `GET` | `/api/v1/livetv/libraries/{library_id}/sources/` | `200` `{items,total}` redacted source list |
| `POST` | `/api/v1/livetv/libraries/{library_id}/sources/` | `201` source |
| `PUT` | `/api/v1/livetv/libraries/{library_id}/sources/{source_key}` | `200` source |
| `DELETE` | `/api/v1/livetv/libraries/{library_id}/sources/{source_key}` | `204` |
| `POST` | `/api/v1/livetv/libraries/{library_id}/sources/{source_key}/refresh` | `200` refreshed source |

The request body for create/update is:

```json
{
  "kind": "playlist",
  "source_key": "playlist-main",
  "name": "Main playlist",
  "location": "https://configured.example/playlist.m3u",
  "config": {},
  "enabled": true
}
```

`kind` is `playlist` or `epg`; `source_key`, `name`, and `location` are
required. Duplicate source keys return `409 conflict`; invalid input returns
`400 bad_request`; unknown library/source returns `404 not_found`; a manual
refresh without the configured runtime returns `503 unavailable`.

Source responses intentionally contain no `location` or `config`. Those values
remain server-side because they may contain private URLs or credentials. Source
refresh failure returns `503 refresh_failed` while the source remains available
with its previous last-known-good channels/programmes and stale status.

## Viewer reads

All viewer routes below require `X-Profile-Id` and return `404 not_found` for a
missing, non-Live-TV, disabled, disabled-by-policy, or unknown library. A restricted empty
allow-list grants access to no libraries.

| Method | Route | Success |
|---|---|---|
| `GET` | `/api/v1/livetv/libraries/{library_id}/` | `200` library and redacted sources |
| `GET` | `/api/v1/livetv/libraries/{library_id}/channels` | `200` paginated channels |
| `GET` | `/api/v1/livetv/libraries/{library_id}/channels/{channel_id}` | `200` channel detail |
| `GET` | `/api/v1/livetv/libraries/{library_id}/channels/{channel_id}/current-next` | `200` up to two programmes |
| `GET` | `/api/v1/livetv/libraries/{library_id}/guide` | `200` guide window |
| `GET` | `/api/v1/livetv/libraries/{library_id}/programmes/{programme_id}` | `200` programme detail |
| `GET` | `/api/v1/livetv/libraries/{library_id}/channels/{channel_id}/playback` | `200` live Silo URL, or `503` when proxy playback is unavailable |

Channel pagination uses `limit` and `offset`. The default limit is `50`, the
maximum is `200`, negative offsets become `0`, and oversized limits are capped
at `200`. The response is `{ "items": [], "total": 0, "limit": 50,
"offset": 0 }`; an empty library is a successful empty page.

Guide requests require RFC3339 `from` and `to` query parameters with a positive
window no longer than 24 hours. Programme selection uses overlap semantics:
`starts_at < to` and `ends_at > from`. Invalid windows return `400 bad_request`.

Guide and channel payloads include `stale: true` when any source is in `stale`
or `error` state. Guide responses also include the generic
`refresh_error: "Live TV source refresh failed"`; provider URLs, credentials,
and raw parser diagnostics are never returned. Existing rows remain readable
while stale.
Unknown channel/programme IDs return `404 not_found`; storage failures return
`500 internal_error`.

Playback resolution validates library and channel ownership before doing any
upstream work. When the controlled live-session/proxy service is wired, it
returns `200` with `{ "live": true, "playable": true, "url": "<silo URL>" }`.
Without that service it returns `503` with `{ "live": true, "playable": false,
"error_code": "live_playback_unavailable" }`. The response never contains a
provider URL, credentials, or caller-supplied headers. Clients must use
`playback_available` for feature detection.
