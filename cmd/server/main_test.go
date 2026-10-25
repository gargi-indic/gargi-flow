package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRun_Server(t *testing.T) {
	t.Run("invalid flag", func(t *testing.T) {
		err := run(context.Background(), []string{"-invalid-flag"}, os.LookupEnv, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("missing config file", func(t *testing.T) {
		err := run(context.Background(), []string{"-config", "nonexistent.yaml"}, os.LookupEnv, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "loading config") {
			t.Fatalf("expected loading config error, got: %v", err)
		}
	})

	t.Run("invalid config validation", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfgFile := filepath.Join(tmpDir, "config.yaml")
		_ = os.WriteFile(cfgFile, []byte("store:\n  driver: unknown_driver\n"), 0o644)

		err := run(context.Background(), []string{"-config", cfgFile}, os.LookupEnv, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "validating config") {
			t.Fatalf("expected validating config error, got: %v", err)
		}
	})

	t.Run("successful initialization with memory store and graceful shutdown", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfgFile := filepath.Join(tmpDir, "config.yaml")
		yamlContent := `
store:
  driver: memory
temporal:
  host_port: localhost:7233
  namespace: default
  task_queue: gargi_tasks
server:
  http_address: 127.0.0.1:0
  grpc_address: 127.0.0.1:0
`
		_ = os.WriteFile(cfgFile, []byte(yamlContent), 0o644)

		var stdout bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		err := run(ctx, []string{"-config", cfgFile}, os.LookupEnv, &stdout, &bytes.Buffer{})
		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}
		if !strings.Contains(stdout.String(), "Starting server") {
			t.Errorf("expected log output, got: %s", stdout.String())
		}
	})
}
