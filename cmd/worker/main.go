// Package main provides the entry point for the worker.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/gargi-indic/gargi-flow/internal/config"
	"github.com/gargi-indic/gargi-flow/internal/logging"
	"github.com/gargi-indic/gargi-flow/internal/nodes"
	"github.com/gargi-indic/gargi-flow/internal/orchestrator"
	"github.com/gargi-indic/gargi-flow/internal/store"
	"github.com/gargi-indic/gargi-flow/internal/telemetry"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"go.temporal.io/sdk/activity"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "worker failed: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) (string, bool), stdout io.Writer, _ io.Writer) error {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
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
	logger.Info("Starting worker", "store_driver", cfg.Store.Driver)

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

	temporalClient, err := orchestrator.NewClient(cfg.Temporal, logger)
	if err != nil {
		return fmt.Errorf("creating temporal client: %w", err)
	}
	defer temporalClient.Close()

	var policyStore engine.PolicyStore
	if ps, ok := st.(engine.PolicyStore); ok {
		policyStore = ps
	}

	reg := engine.NewRegistry()
	if err := nodes.RegisterDefaults(reg, nodes.Dependencies{
		Signals:     orchestrator.NewTemporalSignalSource(),
		PolicyStore: policyStore,
	}); err != nil {
		return fmt.Errorf("registering default nodes: %w", err)
	}

	na := orchestrator.NewNodeActivities(reg)
	sa := orchestrator.NewStateActivities(st)

	wm := orchestrator.NewWorkerManager(temporalClient, cfg.Temporal, logger)
	wm.RegisterWorkflow(orchestrator.GraphExecutionWorkflow)
	wm.RegisterActivity(sa.PersistStateActivity)
	wm.RegisterActivity(sa.WriteStateActivity)
	wm.RegisterActivityWithOptions(na.ExecuteNodeActivity, activity.RegisterOptions{
		Name: orchestrator.ExecuteNodeActivityName,
	})

	return wm.Run(ctx)
}
