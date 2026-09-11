# Live playback lifecycle

Live TV playback uses a server-owned, infinite session rather than a finite media
file. A playback request names a library and stable channel ID. The source
authority resolves that channel, verifies that it belongs to the requested library
and an enabled playlist source, and returns the provider stream URL only to the
server-side playback service.

The browser receives an opaque Silo grant URL. HLS manifests are fetched by Silo
and their media entries are replaced with opaque per-session resource IDs. The
provider URL, source configuration, and credentials never enter the browser
payload, route, node recipe, or logs. Every request is bound to the authenticated
user and profile, and the proxy revalidates the login session before forwarding.

The supported execution path is server-side direct/progressive or HLS proxying.
Transcode-node execution is intentionally unsupported until a node-local source
authority exists that can resolve an opaque source reference without accepting a
provider URL or filesystem path. The existing transcode-node catalog path
authorizer is not weakened.

Client cancellation propagates through the upstream request context and revokes
the grant. Provider errors also revoke the current grant. Reconnect is explicit:
it resolves the channel again and creates a fresh grant, with a maximum of three
reconnects per active ownership/session group. Idle and maximum-lifetime sweeps
remove abandoned sessions and emit lifecycle termination telemetry.
