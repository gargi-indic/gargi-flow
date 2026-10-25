//go:build integration

package store_test

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/gargi-indic/gargi-flow/internal/config"
	"github.com/gargi-indic/gargi-flow/internal/store"
	"github.com/gargi-indic/gargi-flow/internal/store/memory"
	"github.com/gargi-indic/gargi-flow/internal/store/postgres"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// integrationPostgresBase returns a PostgreSQL URL: GARGI_POSTGRES_URL /
// POSTGRES_URL when set, else an embedded PostgreSQL with its own runtime
// directory (stopped when the test ends).
func integrationPostgresBase(t *testing.T) string {
	t.Helper()
	for _, env := range []string{"GARGI_POSTGRES_URL", "POSTGRES_URL"} {
		if v := os.Getenv(env); v != "" {
			return v
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := uint32(l.Addr().(*net.TCPAddr).Port)
	require.NoError(t, l.Close())
	runtime := t.TempDir()
	db := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Port(port).
		Database("postgres").
		Username("postgres").
		Password("postgres").
		RuntimePath(runtime).
		DataPath(filepath.Join(runtime, "data")).
		Logger(io.Discard))
	require.NoError(t, db.Start(), "starting embedded-postgres")
	t.Cleanup(func() { _ = db.Stop() })
	return fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", port)
}

// schemaDSN creates a fresh schema on base and returns a DSN whose
// search_path selects it; the schema is dropped when the test ends.
func schemaDSN(t *testing.T, base string) string {
	t.Helper()
	ctx := context.Background()
	schema := fmt.Sprintf("fac_%d_%d", time.Now().UnixNano(), rand.Intn(100000))
	conn, err := pgx.Connect(ctx, base)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize())
	require.NoError(t, err)
	require.NoError(t, conn.Close(ctx))
	t.Cleanup(func() {
		conn, err := pgx.Connect(context.Background(), base)
		if err != nil {
			return
		}
		_, _ = conn.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		_ = conn.Close(context.Background())
	})
	u, err := url.Parse(base)
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

func TestFactory_PostgresExperiments(t *testing.T) {
	ctx := context.Background()
	base := integrationPostgresBase(t)

	t.Run("driver postgres shares one adapter for state and experiments", func(t *testing.T) {
		st, err := store.New(ctx, config.StoreConfig{Driver: "postgres", Postgres: &config.PostgresConfig{DSN: schemaDSN(t, base)}})
		require.NoError(t, err)
		state, ok := st.State().(*postgres.Store)
		require.True(t, ok, "state store is %T", st.State())
		exps, ok := st.Experiments().(*postgres.ExperimentStore)
		require.True(t, ok, "experiment store is %T", st.Experiments())

		// The factory applied the simulation migration.
		ver, dirty, err := state.MigrationVersion(ctx)
		require.NoError(t, err)
		require.Equal(t, int64(4), ver)
		require.False(t, dirty)

		saved, err := exps.SaveModel(ctx, sim.ModelBundle{ID: "m", Format: "yaml"})
		require.NoError(t, err)
		require.Equal(t, "1", saved.Version)

		// Closing the composite closes the one shared pool: the experiment
		// store is closed with it.
		require.NoError(t, st.Close())
		_, err = exps.GetModel(ctx, "m", "")
		require.True(t, engine.IsStoreUnavailable(err), "GetModel after Close: %v", err)
	})

	t.Run("postgres experiments with a memory state store", func(t *testing.T) {
		dsn := schemaDSN(t, base)
		cfg := config.StoreConfig{
			Driver:       "memory",
			Capabilities: config.StoreCapabilitiesConfig{Experiments: "postgres"},
			Postgres:     &config.PostgresConfig{DSN: dsn},
		}
		st, err := store.New(ctx, cfg)
		require.NoError(t, err)
		_, ok := st.State().(*memory.Store)
		require.True(t, ok, "state store is %T", st.State())
		_, ok = st.Experiments().(*postgres.ExperimentStore)
		require.True(t, ok, "experiment store is %T", st.Experiments())
		require.NoError(t, st.Experiments().CreateExperiment(ctx, sim.ExperimentRecord{ID: "e", Status: sim.ExperimentQueued}))
		require.NoError(t, st.Close())

		// A second composite (a restarted process) reads it back.
		st2, err := store.New(ctx, cfg)
		require.NoError(t, err)
		defer func() { _ = st2.Close() }()
		rec, err := st2.Experiments().GetExperiment(ctx, "e")
		require.NoError(t, err)
		require.Equal(t, sim.ExperimentQueued, rec.Status)
	})

	t.Run("postgres driver with memory experiments", func(t *testing.T) {
		st, err := store.New(ctx, config.StoreConfig{
			Driver:       "postgres",
			Capabilities: config.StoreCapabilitiesConfig{Experiments: "memory"},
			Postgres:     &config.PostgresConfig{DSN: schemaDSN(t, base)},
		})
		require.NoError(t, err)
		defer func() { _ = st.Close() }()
		_, ok := st.Experiments().(*memory.ExperimentStore)
		require.True(t, ok, "experiment store is %T", st.Experiments())
	})
}
