package httpapi

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// authenticator checks the inbound bearer token. An empty token means inbound
// authentication is disabled.
type authenticator struct {
	token string
}

// enabled reports whether inbound authentication is configured.
func (a authenticator) enabled() bool { return a.token != "" }

// authorized reports whether a request carries the configured bearer token.
// The comparison is constant time. Tokens are accepted only from the
// Authorization header: never from a query string, cookie or body.
func (a authenticator) authorized(r *http.Request) bool {
	if !a.enabled() {
		return true
	}
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	supplied := header[len(prefix):]
	return subtle.ConstantTimeCompare([]byte(supplied), []byte(a.token)) == 1
}

// writeUnauthorized answers a failed authentication without revealing whether
// the header was missing, malformed or simply wrong.
func writeUnauthorized(w http.ResponseWriter) int {
	w.Header().Set("WWW-Authenticate", "Bearer")
	return writeError(w, invalidRequest("unauthorized", "unauthorized", http.StatusUnauthorized))
}
