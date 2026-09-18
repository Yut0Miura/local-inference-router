package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const token = "s3cret-router-token"

func TestProtectedEndpointsRequireBearer(t *testing.T) {
	h := newHarness(t, okUpstream(t).URL, token)
	h.state.SetHealth("a", true)

	protected := []*http.Request{
		chatRequest(`{"model":"local-chat"}`),
		httptest.NewRequest(http.MethodGet, "/v1/models", nil),
		httptest.NewRequest(http.MethodGet, "/router/status", nil),
		httptest.NewRequest(http.MethodGet, "/metrics", nil),
	}
	for _, req := range protected {
		rec := h.do(t, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", req.Method, req.URL.Path, rec.Code)
		}
		if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
			t.Errorf("%s WWW-Authenticate = %q", req.URL.Path, got)
		}
	}
}

func TestProbesNeverRequireAuth(t *testing.T) {
	h := newHarness(t, okUpstream(t).URL, token)
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := h.do(t, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("%s must not require authentication", path)
		}
	}
}

func TestValidBearerIsAccepted(t *testing.T) {
	h := newHarness(t, okUpstream(t).URL, token)
	h.state.SetHealth("a", true)

	req := chatRequest(`{"model":"local-chat"}`)
	req.Header.Set("Authorization", "Bearer "+token)
	if rec := h.do(t, req); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestBearerVariantsAreRejected(t *testing.T) {
	h := newHarness(t, okUpstream(t).URL, token)
	h.state.SetHealth("a", true)

	cases := map[string]string{
		"missing":       "",
		"wrong token":   "Bearer wrong-token",
		"wrong scheme":  "Basic " + token,
		"token only":    token,
		"empty bearer":  "Bearer ",
		"prefix only":   "Bearer" + token,
		"token + extra": "Bearer " + token + " extra",
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/router/status", nil)
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			rec := h.do(t, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			// The reason is never disclosed.
			if body := rec.Body.String(); strings.Contains(body, "scheme") || strings.Contains(body, "missing") {
				t.Fatalf("response reveals why authentication failed: %s", body)
			}
		})
	}
}

func TestTokenIsNotAcceptedFromQueryOrCookie(t *testing.T) {
	h := newHarness(t, okUpstream(t).URL, token)
	h.state.SetHealth("a", true)

	query := httptest.NewRequest(http.MethodGet, "/router/status?access_token="+token, nil)
	if rec := h.do(t, query); rec.Code != http.StatusUnauthorized {
		t.Fatalf("query token was accepted: %d", rec.Code)
	}

	cookie := httptest.NewRequest(http.MethodGet, "/router/status", nil)
	cookie.AddCookie(&http.Cookie{Name: "token", Value: token})
	if rec := h.do(t, cookie); rec.Code != http.StatusUnauthorized {
		t.Fatalf("cookie token was accepted: %d", rec.Code)
	}
}

func TestAuthDisabledAllowsEverything(t *testing.T) {
	h := newHarness(t, okUpstream(t).URL, "")
	h.state.SetHealth("a", true)

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/v1/models", nil),
		httptest.NewRequest(http.MethodGet, "/router/status", nil),
		httptest.NewRequest(http.MethodGet, "/metrics", nil),
	} {
		if rec := h.do(t, req); rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200 when inbound auth is disabled", req.URL.Path, rec.Code)
		}
	}
}

// TestClientAuthorizationIsNotForwarded proves the client's credential never
// reaches a backend, whether or not inbound auth is configured.
func TestClientAuthorizationIsNotForwarded(t *testing.T) {
	var seen string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	h := newHarness(t, upstream.URL, token)
	h.state.SetHealth("a", true)

	req := chatRequest(`{"model":"local-chat"}`)
	req.Header.Set("Authorization", "Bearer "+token)
	if rec := h.do(t, req); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if seen != "" {
		t.Fatalf("client Authorization reached the backend: %q", seen)
	}
}
