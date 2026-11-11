package backend_test

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/Yut0Miura/local-inference-router/internal/backend"
)

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

func TestDrainAndCloseConsumesSmallBody(t *testing.T) {
	body := &trackedBody{
		Reader: bytes.NewBufferString("small response"),
	}

	resp := &http.Response{Body: body}

	backend.DrainAndClose(resp)

	if !body.closed {
		t.Fatal("body was not closed")
	}

	remaining, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read remaining body: %v", err)
	}

	if len(remaining) != 0 {
		t.Fatalf("remaining body = %d bytes, want 0", len(remaining))
	}
}

func TestDrainAndCloseBoundsLargeBody(t *testing.T) {
	const large = 128 * 1024

	body := &trackedBody{
		Reader: bytes.NewReader(make([]byte, large)),
	}

	resp := &http.Response{Body: body}

	backend.DrainAndClose(resp)

	if !body.closed {
		t.Fatal("body was not closed")
	}

	remaining, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read remaining body: %v", err)
	}

	if len(remaining) == 0 {
		t.Fatal("large body was drained completely; drain must remain bounded")
	}
}
