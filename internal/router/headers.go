package router

import (
	"net/http"
	"strings"
)

// hopByHopAndUnsafe are response headers the router never copies downstream.
// Transfer framing is left to Go; Set-Cookie and Server are dropped so the
// backend cannot set state or advertise itself through the router. Alt-Svc is
// also dropped so an upstream cannot advertise an alternate backend endpoint.
var hopByHopAndUnsafe = map[string]struct{}{
	"Alt-Svc":             {},
	"Connection":          {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
	"Content-Length":      {},
	"Set-Cookie":          {},
	"Server":              {},
}

// setUpstreamHeaders builds the upstream request headers.
//
// Client headers are not forwarded blindly. Authorization and Cookie are never
// copied: backend credentials are configured, not supplied by callers. The
// router also does not forward or add Forwarded and X-Forwarded-* headers.
func setUpstreamHeaders(upstream *http.Request, client *http.Request, route *Route) {
	upstream.Header.Set("Content-Type", "application/json")
	if accept := client.Header.Get("Accept"); accept != "" {
		upstream.Header.Set("Accept", accept)
	}
	route.Backend.Authorize(upstream)
}

// copyResponseHeaders copies safe end-to-end response headers downstream.
// Headers nominated by Connection are removed in addition to the fixed denylist.
func copyResponseHeaders(dst http.Header, src http.Header) {
	connectionHeaders := connectionNominatedHeaders(src)

	for name, values := range src {
		canonicalName := http.CanonicalHeaderKey(name)

		if _, skip := hopByHopAndUnsafe[canonicalName]; skip {
			continue
		}
		if _, skip := connectionHeaders[canonicalName]; skip {
			continue
		}

		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

func connectionNominatedHeaders(header http.Header) map[string]struct{} {
	nominated := make(map[string]struct{})

	for _, value := range header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			name := strings.TrimSpace(token)
			if name == "" {
				continue
			}

			nominated[http.CanonicalHeaderKey(name)] = struct{}{}
		}
	}

	return nominated
}
