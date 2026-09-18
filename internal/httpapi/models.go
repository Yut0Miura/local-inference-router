package httpapi

import (
	"encoding/json"
	"net/http"
)

// modelEntry is one configured alias in the /v1/models response.
type modelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type modelList struct {
	Object string       `json:"object"`
	Data   []modelEntry `json:"data"`
}

// handleModels lists the configured aliases in configuration order.
//
// This is router configuration, not model discovery: the router never queries a
// backend's model list, and an alias appears here regardless of backend health.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) int {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		return writeError(w, invalidRequest("method not allowed", "method_not_allowed", http.StatusMethodNotAllowed))
	}

	names := s.state.AliasNames()
	list := modelList{Object: "list", Data: make([]modelEntry, 0, len(names))}
	for _, name := range names {
		list.Data = append(list.Data, modelEntry{
			ID:      name,
			Object:  "model",
			Created: 0,
			OwnedBy: "local-inference-router",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(list)
	return http.StatusOK
}
