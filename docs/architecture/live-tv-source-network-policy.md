# Live TV Source Network Policy

Live TV source locations are server-side configuration. The fetch service accepts
only HTTP and HTTPS locations from a configured `livetv` source; browser requests
cannot provide an upstream URL or arbitrary headers.

Private RFC1918 destinations are deliberately supported for trusted household
and xTeVe deployments when the server policy enables private networks. Loopback,
link-local, unspecified, multicast, and metadata destinations remain blocked.
The hostname is resolved before the request and resolved again immediately
before every connection, so redirects and DNS rebinding cannot bypass the IP
policy. Redirect targets are validated independently and redirect count is
bounded.

The service permits HTTP/HTTPS ports and unprivileged configured ports, rejects
invalid and privileged non-web ports, limits response headers and bodies, and
uses request context cancellation plus a response deadline. Errors expose only
typed policy/status information; credentials, query values, and provider URLs
are not included in diagnostics.

## Production construction seam

`cmd/silo` constructs `livetv.NewPostgresRepository(pool)` and passes it to
`livetv.NewRuntime` while assembling `api.Dependencies` for integrated and API
servers. Runtime construction creates the fetch service and reconciler only; it
does not start a goroutine or fetch a configured source. A later owner invokes
`Runtime.RefreshSource` with a persisted source and optional mappings. Scheduled
refresh, manual refresh routes, playback, and UI are separate follow-up seams.
