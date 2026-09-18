package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/Yut0Miura/local-inference-router/internal/router"
)

// MaxChatRequestBytes bounds the inbound chat request body.
const MaxChatRequestBytes = 4194304 // 4 MiB

// handleChat validates the request just enough to route it, then hands it to
// the proxy. The full OpenAI schema is deliberately not validated: unknown
// fields belong to the backend and are passed through untouched.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) int {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		return writeError(w, invalidRequest("method not allowed", "method_not_allowed", http.StatusMethodNotAllowed))
	}
	if apiErr := checkChatHeaders(r); apiErr != nil {
		return writeError(w, apiErr)
	}

	payload, apiErr := decodeChatBody(w, r)
	if apiErr != nil {
		return writeError(w, apiErr)
	}

	var alias string
	if err := json.Unmarshal(payload["model"], &alias); err != nil || strings.TrimSpace(alias) == "" {
		return writeError(w, invalidRequest("model must be a non-empty string", "invalid_model", http.StatusBadRequest))
	}

	status, apiErr := s.proxy.Serve(w, r, alias, payload)
	if apiErr != nil {
		return writeError(w, apiErr)
	}
	return status
}

func checkChatHeaders(r *http.Request) *router.APIError {
	// Compressed request bodies are not supported; the router does not decode
	// them and must not silently forward a body it did not validate.
	if encoding := strings.TrimSpace(r.Header.Get("Content-Encoding")); encoding != "" {
		return invalidRequest("content encoding is not supported", "unsupported_media_type",
			http.StatusUnsupportedMediaType)
	}

	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return invalidRequest("content type must be application/json", "unsupported_media_type",
			http.StatusUnsupportedMediaType)
	}
	return nil
}

func decodeChatBody(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, *router.APIError) {
	limited := http.MaxBytesReader(w, r.Body, MaxChatRequestBytes)
	defer limited.Close()

	body, err := io.ReadAll(limited)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, invalidRequest("request body is too large", "request_too_large",
				http.StatusRequestEntityTooLarge)
		}
		return nil, invalidRequest("request body could not be read", "invalid_body", http.StatusBadRequest)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return nil, invalidRequest("request body must be a JSON object", "invalid_body", http.StatusBadRequest)
	}
	if _, ok := payload["model"]; !ok {
		return nil, invalidRequest("model is required", "invalid_model", http.StatusBadRequest)
	}
	return payload, nil
}
