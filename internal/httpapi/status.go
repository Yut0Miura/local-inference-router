package httpapi

import (
	"encoding/json"
	"net/http"
)

// handleHealthz reports process liveness. If the server answers, it is alive.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) int {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		return writeError(w, invalidRequest("method not allowed", "method_not_allowed", http.StatusMethodNotAllowed))
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
	return http.StatusOK
}

// handleReadyz reports whether every alias has at least one healthy route.
//
// Readiness says nothing about whether an upstream model is loaded, and a
// backend at full capacity still counts as ready.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) int {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		return writeError(w, invalidRequest("method not allowed", "method_not_allowed", http.StatusMethodNotAllowed))
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if !s.state.Ready() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready\n"))
		return http.StatusServiceUnavailable
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready\n"))
	return http.StatusOK
}

// handleRouterStatus returns the current routing state.
//
// The body carries no base URL, upstream model name, token or environment
// value: knowing the topology is not required to operate the router.
func (s *Server) handleRouterStatus(w http.ResponseWriter, r *http.Request) int {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		return writeError(w, invalidRequest("method not allowed", "method_not_allowed", http.StatusMethodNotAllowed))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(s.state.Snapshot())
	return http.StatusOK
}
