// Package httpapi exposes the router over HTTP.
package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/Yut0Miura/local-inference-router/internal/router"
)

// errorBody is the OpenAI-shaped error envelope the router returns.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// writeError renders a router error. Internal details, upstream URLs and
// network error text never reach the client.
func writeError(w http.ResponseWriter, apiErr *router.APIError) int {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(apiErr.Status)
	_ = json.NewEncoder(w).Encode(errorBody{Error: errorDetail{
		Message: apiErr.Message,
		Type:    apiErr.Type,
		Code:    apiErr.Code,
	}})
	return apiErr.Status
}

func invalidRequest(message, code string, status int) *router.APIError {
	return &router.APIError{Status: status, Message: message, Type: "invalid_request_error", Code: code}
}
