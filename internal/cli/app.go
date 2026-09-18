// Package cli implements the two commands of the router.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/Yut0Miura/local-inference-router/internal/backend"
	"github.com/Yut0Miura/local-inference-router/internal/config"
	"github.com/Yut0Miura/local-inference-router/internal/httpapi"
	"github.com/Yut0Miura/local-inference-router/internal/metrics"
	"github.com/Yut0Miura/local-inference-router/internal/router"
)

// Exit codes.
const (
	ExitSuccess = 0
	ExitError   = 1
	ExitUsage   = 2
)

const usage = "usage: local-inference-router <serve|validate> -config <path>\n"

// Run executes one command. It returns the process exit code.
func Run(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	_ = stdin

	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return ExitUsage
	}

	command := args[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "path to the configuration file")

	switch command {
	case "serve", "validate":
		if err := flags.Parse(args[1:]); err != nil {
			return ExitUsage
		}
		if *configPath == "" || flags.NArg() > 0 {
			fmt.Fprint(stderr, usage)
			return ExitUsage
		}
	default:
		fmt.Fprint(stderr, usage)
		return ExitUsage
	}

	logger := slog.New(slog.NewTextHandler(stderr, nil))

	if command == "validate" {
		return runValidate(*configPath, stdout, stderr)
	}
	return runServe(*configPath, stderr, logger)
}

// runValidate checks configuration syntax and semantics. It performs no
// network access and does not require secrets to exist.
func runValidate(path string, stdout, stderr io.Writer) int {
	if _, err := config.Load(path); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return ExitError
	}
	fmt.Fprintln(stdout, "config valid")
	return ExitSuccess
}

func runServe(path string, stderr io.Writer, logger *slog.Logger) int {
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return ExitError
	}

	inboundToken := ""
	if cfg.BearerTokenEnv != "" {
		value, ok := os.LookupEnv(cfg.BearerTokenEnv)
		if !ok || value == "" {
			fmt.Fprintf(stderr, "config.bearer_token_env: environment variable %s is not set\n", cfg.BearerTokenEnv)
			return ExitError
		}
		inboundToken = value
	}

	backends, err := backend.Build(cfg.Backends, backend.EnvSecrets)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return ExitError
	}

	recorder := metrics.New()
	state, err := router.NewState(cfg, backends, recorder)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return ExitError
	}

	proxy := router.NewProxy(state, recorder, logger)
	api := httpapi.NewServer(state, proxy, recorder, inboundToken, logger)
	server := httpapi.NewHTTPServer(cfg.Listen, api.Handler())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Health checks start before the listener: an offline backend must not
	// prevent the router from starting.
	checker := backend.NewChecker(backends, state, logger)
	var checks sync.WaitGroup
	checks.Add(1)
	go func() {
		defer checks.Done()
		checker.Run(ctx)
	}()

	logger.Info("starting", "listen", cfg.Listen, "backends", len(backends), "aliases", len(cfg.Aliases))

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(stderr, "server: %v\n", err)
			stop()
			checks.Wait()
			return ExitError
		}
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), httpapi.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}

	// Stop the health loops and wait for them, so no goroutine outlives serve.
	stop()
	checks.Wait()
	logger.Info("stopped")
	return ExitSuccess
}
