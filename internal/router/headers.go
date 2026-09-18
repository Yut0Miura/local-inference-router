package router

import "net/http"

// hopByHopAndUnsafe are response headers the router never copies downstream.
// Transfer framing is left to Go; Set-Cookie and Server are dropped so the
// backend cannot set state or advertise itself through the router.
var hopByHopAndUnsafe = map[string]struct{}{
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

// copyResponseHeaders copies end-to-end response headers downstream.
func copyResponseHeaders(dst http.Header, src http.Header) {
	for name, values := range src {
		if _, skip := hopByHopAndUnsafe[http.CanonicalHeaderKey(name)]; skip {
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}
