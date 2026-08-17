package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"stackpilot/internal/api"
)

const (
	defaultServerPort = 32100
	shutdownTimeout   = 15 * time.Second
)

type serverConfig struct {
	port int
}

func runServer(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	config, err := parseServerConfig(args, stderr)
	if err != nil {
		return 2
	}
	logger := newLogger(stderr)
	handler, err := api.NewHandler()
	if err != nil {
		logger.Error("web console initialization failed", "error", err)
		return 1
	}

	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(config.port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		logger.Error("http listener creation failed", "address", address, "error", err)
		return 1
	}
	server := newHTTPServer(address, handler)
	logger.Info("http server started", "address", address)
	fmt.Fprintf(stdout, "StackPilot is available at http://%s\n", address)

	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	return waitForServer(ctx, server, serveErrors, logger)
}

func newLogger(output io.Writer) *slog.Logger {
	options := &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, attribute slog.Attr) slog.Attr {
			if attribute.Key == slog.TimeKey {
				attribute.Value = slog.TimeValue(attribute.Value.Time().UTC())
			}
			return attribute
		},
	}
	return slog.New(slog.NewJSONHandler(output, options))
}

func parseServerConfig(args []string, output io.Writer) (serverConfig, error) {
	config := serverConfig{port: defaultServerPort}
	flags := flag.NewFlagSet("server", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.IntVar(&config.port, "port", defaultServerPort, "loopback HTTP port")
	if err := flags.Parse(args); err != nil {
		return serverConfig{}, err
	}
	if flags.NArg() != 0 {
		return serverConfig{}, fmt.Errorf("server does not accept positional arguments")
	}
	if config.port < 1 || config.port > 65535 {
		return serverConfig{}, fmt.Errorf("port must be between 1 and 65535")
	}
	return config, nil
}

func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

func waitForServer(ctx context.Context, server *http.Server, serveErrors <-chan error, logger *slog.Logger) int {
	select {
	case err := <-serveErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server stopped", "error", err)
			return 1
		}
		return 0
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			logger.Error("http server shutdown failed", "error", err)
			return 1
		}
		if err := <-serveErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server stopped during shutdown", "error", err)
			return 1
		}
		logger.Info("http server stopped")
		return 0
	}
}
