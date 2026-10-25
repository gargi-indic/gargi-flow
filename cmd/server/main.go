// Package main provides the entry point for the server.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/api"
	"github.com/gargi-indic/gargi-flow/internal/config"
	"github.com/gargi-indic/gargi-flow/internal/logging"
	"github.com/gargi-indic/gargi-flow/internal/orchestrator"
	"github.com/gargi-indic/gargi-flow/internal/sim/jobs"
	"github.com/gargi-indic/gargi-flow/internal/store"
	"github.com/gargi-indic/gargi-flow/internal/telemetry"
	"go.temporal.io/sdk/client"
)

// runnerShutdownTimeout bounds how long shutdown waits for queued and
// running simulation experiments to finish before cancelling them.
const runnerShutdownTimeout = 10 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "server failed: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) (string, bool), stdout io.Writer, _ io.Writer) error {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	configPath := fs.String("config", "", "Path to configuration file")

	if err := fs.Parse(args); err != nil {
		return err
	}

	path := *configPath
	if path == "" {
		if envPath, ok := getenv("GARGI_CONFIG"); ok {
			path = envPath
		} else {
			path = "config/config.example.yaml"
		}
	}

	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validating config: %w", err)
	}

	logger := logging.New(logging.Config{
		Level:  cfg.Logging.Level,
		Format: cfg.Logging.Format,
		Output: stdout,
	})
	logger.Info("Starting server", "store_driver", cfg.Store.Driver, "grpc_addr", cfg.Server.GRPCAddress, "http_addr", cfg.Server.HTTPAddress)

	shutdownTracer, err := telemetry.SetupTracer(ctx, cfg.Tracing)
	if err != nil {
		return fmt.Errorf("setting up tracer: %w", err)
	}
	defer func() {
		_ = shutdownTracer(context.Background())
	}()

	st, err := store.New(ctx, cfg.Store)
	if err != nil {
		return fmt.Errorf("initializing state store: %w", err)
	}
	defer func() { _ = st.Close() }()

	var temporalClient client.Client
	if tc, err := orchestrator.NewClient(cfg.Temporal, logger); err == nil {
		temporalClient = tc
		defer temporalClient.Close()
	} else {
		logger.Warn("Temporal client unavailable; running in degraded mode", "error", err)
	}

	// Simulation: the configured experiment store (store.capabilities.
	// experiments) and the in-process job runner (ADR-0004 manual DI;
	// ADR-0009 keeps internal/sim free of store and API imports).
	simRegistry, err := jobs.NewRegistry()
	if err != nil {
		return fmt.Errorf("initializing simulation registry: %w", err)
	}
	simStore := st.Experiments()
	simRunner, err := jobs.New(simStore, simRegistry, jobs.Options{
		Now:    func() time.Time { return time.Now().UTC() },
		Logger: logger,
	})
	if err != nil {
		return fmt.Errorf("initializing simulation job runner: %w", err)
	}
	// Close is idempotent; this covers the early returns below, the
	// shutdown path closes the runner before stopping the servers.
	defer closeRunner(simRunner, logger)
	// The in-process queue does not survive a restart: fail what a previous
	// process left queued or running before accepting new submissions.
	recovered, err := simRunner.Recover(ctx)
	if err != nil {
		return fmt.Errorf("recovering interrupted simulation experiments: %w", err)
	}
	logger.Info("Recovered interrupted simulation experiments", "count", recovered, "experiments_driver", cfg.Store.EffectiveExperimentsDriver())
	simService := api.NewSimulationServer(simStore, simRunner, simRegistry, logger, api.WithStateStore(st))

	grpcServer := api.NewGRPCServer(temporalClient, cfg.Temporal, st, logger)
	grpcServer.RegisterSimulation(simService)
	httpServer, err := api.NewHTTPServer(ctx, grpcServer.Service(), cfg.Server.HTTPAddress, cfg.Server.HTTP.CORS.AllowedOrigins, logger, api.WithSimulationService(simService))
	if err != nil {
		return fmt.Errorf("initializing HTTP REST server: %w", err)
	}

	grpcLis, err := net.Listen("tcp", cfg.Server.GRPCAddress)
	if err != nil {
		return fmt.Errorf("listening on gRPC address %s: %w", cfg.Server.GRPCAddress, err)
	}
	defer func() { _ = grpcLis.Close() }()

	httpLis, err := net.Listen("tcp", cfg.Server.HTTPAddress)
	if err != nil {
		return fmt.Errorf("listening on HTTP address %s: %w", cfg.Server.HTTPAddress, err)
	}
	defer func() { _ = httpLis.Close() }()

	errCh := make(chan error, 2)
	go func() {
		errCh <- grpcServer.Serve(grpcLis)
	}()
	go func() {
		errCh <- httpServer.Serve(httpLis)
	}()

	var serveErr error
	pending := 2
	select {
	case <-ctx.Done():
	case serveErr = <-errCh:
		pending--
	}
	// Drain the simulation runner first so experiment progress streams
	// (gRPC and SSE) end and do not hold up the graceful server stops.
	closeRunner(simRunner, logger)
	grpcServer.GracefulStop()
	_ = httpServer.Shutdown(context.Background())
	// Wait for the remaining Serve goroutines to return.
	for range pending {
		<-errCh
	}
	return serveErr
}

// closeRunner drains the simulation job runner, cancelling whatever is
// still queued or running after runnerShutdownTimeout.
func closeRunner(r *jobs.Runner, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), runnerShutdownTimeout)
	defer cancel()
	if err := r.Close(ctx); err != nil {
		logger.Warn("Simulation runner did not drain before the shutdown timeout; cancelled remaining experiments", "error", err)
	}
}
