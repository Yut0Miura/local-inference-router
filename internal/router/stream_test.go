package router_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Yut0Miura/local-inference-router/internal/router"
)

// nonFlushingWriter is an http.ResponseWriter without http.Flusher.
type nonFlushingWriter struct {
	header http.Header
	status int
	body   strings.Builder
}

func (w *nonFlushingWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}
func (w *nonFlushingWriter) Write(p []byte) (int, error) { return w.body.Write(p) }
func (w *nonFlushingWriter) WriteHeader(status int)      { w.status = status }

func sseHandler(events []string, pause time.Duration) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, event := range events {
			_, _ = io.WriteString(w, event)
			if flusher != nil {
				flusher.Flush()
			}
			if pause > 0 {
				time.Sleep(pause)
			}
		}
	}
}

func TestStreamingIsRelayedIncrementally(t *testing.T) {
	events := []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"two\"}}]}\n\n",
		"data: [DONE]\n\n",
	}
	up := newUpstream(t, sseHandler(events, 0))
	proxy, _ := buildProxy(t, []string{up.server.URL}, nil, nil)

	rec, status, apiErr := serve(t, proxy, chatPayload(t, `{"model":"chat","stream":true}`))
	if apiErr != nil || status != http.StatusOK {
		t.Fatalf("status = %d, err = %+v", status, apiErr)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Body.String(); got != strings.Join(events, "") {
		t.Fatalf("stream was altered:\n%q", got)
	}
	if !rec.Flushed {
		t.Fatal("events must be flushed as they arrive")
	}
}

// TestStreamingReachesClientBeforeUpstreamFinishes proves the router does not
// buffer the whole stream: the first event is readable while the backend is
// still sending.
func TestStreamingReachesClientBeforeUpstreamFinishes(t *testing.T) {
	up := newUpstream(t, sseHandler([]string{
		"data: first\n\n",
		"data: second\n\n",
	}, 300*time.Millisecond))

	proxy, _ := buildProxy(t, []string{up.server.URL}, nil, nil)

	// A real HTTP server is needed to observe streaming on the wire.
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = proxy.Serve(w, r, "chat", chatPayload(t, `{"model":"chat","stream":true}`))
	}))
	defer front.Close()

	resp, err := http.Post(front.URL, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	started := time.Now()
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read first event: %v", err)
	}
	elapsed := time.Since(started)

	if !strings.Contains(line, "first") {
		t.Fatalf("first line = %q", line)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("first event took %v: the response was buffered", elapsed)
	}
}

func TestStreamWithoutFlusherIsRejectedBeforeCommit(t *testing.T) {
	up := newUpstream(t, sseHandler([]string{"data: x\n\n"}, 0))
	proxy, _ := buildProxy(t, []string{up.server.URL}, nil, nil)

	writer := &nonFlushingWriter{}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	_, apiErr := proxy.Serve(writer, req, "chat", chatPayload(t, `{"model":"chat","stream":true}`))

	if apiErr == nil || apiErr.Status != http.StatusInternalServerError {
		t.Fatalf("error = %+v, want a 500 router error", apiErr)
	}
	if writer.status != 0 || writer.body.Len() != 0 {
		t.Fatal("nothing may be written when the stream cannot be flushed")
	}
}

// TestNoFailoverAfterCommit proves that a failure while the body is being
// relayed does not move the request to another backend.
func TestNoFailoverAfterCommit(t *testing.T) {
	truncating := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: partial\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		// Drop the connection mid-stream.
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, err := hijacker.Hijack()
		if err == nil {
			_ = conn.Close()
		}
	})
	fallback := newUpstream(t, statusHandler(http.StatusOK, `{"ok":true}`))

	proxy, state := buildProxy(t, []string{truncating.server.URL, fallback.server.URL}, nil, []int{10, 20})

	rec, status, apiErr := serve(t, proxy, chatPayload(t, `{"model":"chat","stream":true}`))
	if apiErr != nil {
		t.Fatalf("a committed response must not become a router error: %+v", apiErr)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want the committed 200", status)
	}
	if !strings.Contains(rec.Body.String(), "partial") {
		t.Fatalf("body = %q, want the partial stream", rec.Body.String())
	}
	if fallback.requests.Load() != 0 {
		t.Fatal("no failover is allowed after the response is committed")
	}
	for _, backendStatus := range state.Snapshot().Backends {
		if backendStatus.Inflight != 0 {
			t.Fatalf("backend %q kept a reservation", backendStatus.Name)
		}
	}
}

func TestNonStreamingResponseIsRelayed(t *testing.T) {
	payload := `{"id":"1","model":"real","choices":[{"message":{"content":"hello"}}]}`
	up := newUpstream(t, statusHandler(http.StatusOK, payload))
	proxy, _ := buildProxy(t, []string{up.server.URL}, nil, nil)

	rec, _, apiErr := serve(t, proxy, chatPayload(t, `{"model":"chat"}`))
	if apiErr != nil {
		t.Fatalf("serve: %+v", apiErr)
	}
	var decoded map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if decoded["model"] != "real" {
		t.Fatalf("model = %v, want the upstream value untouched", decoded["model"])
	}
	if rec.Flushed {
		t.Log("a non-streaming response may be flushed by the recorder; content is what matters")
	}
}

var _ http.ResponseWriter = (*nonFlushingWriter)(nil)
var _ = router.StatusClientClosed
