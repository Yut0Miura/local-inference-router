package cli

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

type fakeShutdownServer struct {
	shutdownErr error
	closeErr    error
	closeCalls  int
}

func (s *fakeShutdownServer) Shutdown(context.Context) error {
	return s.shutdownErr
}

func (s *fakeShutdownServer) Close() error {
	s.closeCalls++
	return s.closeErr
}

func TestShutdownHTTPServerSuccess(t *testing.T) {
	server := &fakeShutdownServer{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := shutdownHTTPServer(server, logger); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	if server.closeCalls != 0 {
		t.Fatalf("Close calls = %d, want 0", server.closeCalls)
	}
}

func TestShutdownHTTPServerForcesCloseAfterFailure(t *testing.T) {
	wantErr := errors.New("shutdown timed out")

	server := &fakeShutdownServer{
		shutdownErr: wantErr,
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	err := shutdownHTTPServer(server, logger)

	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}

	if server.closeCalls != 1 {
		t.Fatalf("Close calls = %d, want 1", server.closeCalls)
	}
}

func TestShutdownHTTPServerPreservesShutdownFailureWhenCloseAlsoFails(t *testing.T) {
	shutdownErr := errors.New("shutdown timed out")

	server := &fakeShutdownServer{
		shutdownErr: shutdownErr,
		closeErr:    errors.New("close failed"),
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	err := shutdownHTTPServer(server, logger)

	if !errors.Is(err, shutdownErr) {
		t.Fatalf("error = %v, want original shutdown error", err)
	}

	if server.closeCalls != 1 {
		t.Fatalf("Close calls = %d, want 1", server.closeCalls)
	}
}
