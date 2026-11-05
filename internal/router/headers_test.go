package router

import (
	"net/http"
	"testing"
)

func TestCopyResponseHeadersDropsConnectionNominatedHeaders(t *testing.T) {
	src := make(http.Header)

	src.Add("Connection", "keep-alive, X-Internal-Hop")
	src.Add("Connection", "x-second-hop")

	src.Set("X-Internal-Hop", "secret-one")
	src.Set("X-Second-Hop", "secret-two")
	src.Set("X-End-To-End", "safe")

	dst := make(http.Header)

	copyResponseHeaders(dst, src)

	for _, name := range []string{
		"Connection",
		"Keep-Alive",
		"X-Internal-Hop",
		"X-Second-Hop",
	} {
		if got := dst.Get(name); got != "" {
			t.Fatalf("%s was copied downstream: %q", name, got)
		}
	}

	if got := dst.Get("X-End-To-End"); got != "safe" {
		t.Fatalf("X-End-To-End = %q, want safe", got)
	}
}

func TestCopyResponseHeadersDropsAltSvc(t *testing.T) {
	src := make(http.Header)

	src.Set("Content-Type", "application/json")
	src.Set("Alt-Svc", `h2="backend.internal:443"`)

	dst := make(http.Header)

	copyResponseHeaders(dst, src)

	if got := dst.Get("Alt-Svc"); got != "" {
		t.Fatalf("Alt-Svc was copied downstream: %q", got)
	}

	if got := dst.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}
