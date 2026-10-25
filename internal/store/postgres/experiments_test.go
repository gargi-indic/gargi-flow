package postgres_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	postgresstore "github.com/gargi-indic/gargi-flow/internal/store/postgres"
	"github.com/gargi-indic/gargi-flow/internal/store/storetest"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newExperimentStore returns an ExperimentStore over a fresh, migrated
// schema that is dropped when the test ends.
func newExperimentStore(t *testing.T) (*postgresstore.ExperimentStore, *postgresstore.Store) {
	t.Helper()
	connStr := getPostgresConnString(t)
	schema := fmt.Sprintf("exp_%d_%d", time.Now().UnixNano(), rand.Intn(100000))
	st, err := postgresstore.New(context.Background(), postgresstore.Config{
		ConnString:  connStr,
		Schema:      schema,
		AutoMigrate: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		if st.Pool() != nil {
			_, _ = st.Pool().Exec(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema))
		}
		st.Close()
	})
	return postgresstore.NewExperimentStore(st), st
}

func TestExperimentStoreConformance(t *testing.T) {
	storetest.RunExperimentStore(t, func(t *testing.T) sim.ExperimentStore {
		es, _ := newExperimentStore(t)
		return es
	})
}

// TestExperimentStore_DocumentsVerbatim checks that documents and reports
// round-trip byte for byte (no JSON normalization) and that model IDs and
// policy names keep arbitrary bytes.
func TestExperimentStore_DocumentsVerbatim(t *testing.T) {
	ctx := context.Background()
	es, _ := newExperimentStore(t)

	simDoc := []byte("apiVersion: sim/v1\nid: m # comment kept\n\tweird: \xff\x00\n")
	graphDoc := []byte(`{"z": 1, "a":   2.50, "n": 1e2}`)
	policyDoc := []byte("{\n  \"b\": 1.0,\n  \"a\": [3, 2, 1]\n}")
	saved, err := es.SaveModel(ctx, sim.ModelBundle{
		ID:       "m",
		Sim:      simDoc,
		Graph:    graphDoc,
		Policies: map[string][]byte{"p.json": policyDoc, "empty.yaml": {}},
		Format:   "yaml",
	})
	require.NoError(t, err)
	got, err := es.GetModel(ctx, "m", saved.Version)
	require.NoError(t, err)
	assert.Equal(t, simDoc, got.Sim)
	assert.Equal(t, graphDoc, got.Graph)
	assert.Equal(t, policyDoc, got.Policies["p.json"])
	assert.Contains(t, got.Policies, "empty.yaml")
	assert.Empty(t, got.Policies["empty.yaml"])

	report := []byte(`{"worlds":[{"mean":460.0,"b":1,"a":2}],"x":1.10}`)
	rec := sim.ExperimentRecord{
		ID:         "e",
		Model:      sim.ModelRef{ID: "m", Version: saved.Version},
		Definition: []byte("apiVersion: experiment/v1\n"),
		Format:     "yaml",
		Status:     sim.ExperimentQueued,
		Progress:   sim.Progress{Done: 1, Total: 3, Best: "p", BestMean: 0.1 + 0.2, CIWidth: 1e-300},
	}
	require.NoError(t, es.CreateExperiment(ctx, rec))
	updated, err := es.UpdateExperiment(ctx, "e", func(r *sim.ExperimentRecord) error {
		r.Status = sim.ExperimentSucceeded
		r.Report = report
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, report, updated.Report)
	stored, err := es.GetExperiment(ctx, "e")
	require.NoError(t, err)
	assert.Equal(t, report, stored.Report)
	assert.Equal(t, rec.Progress, stored.Progress, "progress floats round-trip exactly")

	// Clearing the report removes it.
	_, err = es.UpdateExperiment(ctx, "e", func(r *sim.ExperimentRecord) error {
		r.Report = nil
		return nil
	})
	require.NoError(t, err)
	stored, err = es.GetExperiment(ctx, "e")
	require.NoError(t, err)
	assert.Nil(t, stored.Report)
}

// TestExperimentStore_TimesUTCMicroseconds checks that timestamps come back
// in UTC truncated to microseconds, and zero times stay zero.
func TestExperimentStore_TimesUTCMicroseconds(t *testing.T) {
	ctx := context.Background()
	es, _ := newExperimentStore(t)
	loc := time.FixedZone("UTC+5:30", 5*3600+1800)
	created := time.Date(2026, 10, 24, 9, 30, 0, 123456789, loc)
	require.NoError(t, es.CreateExperiment(ctx, sim.ExperimentRecord{ID: "e", Status: sim.ExperimentQueued, CreatedAt: created}))
	got, err := es.GetExperiment(ctx, "e")
	require.NoError(t, err)
	want := created.UTC().Truncate(time.Microsecond)
	assert.Equal(t, want, got.CreatedAt, "UTC, microseconds")
	assert.Equal(t, time.UTC, got.CreatedAt.Location())
	assert.True(t, got.StartedAt.IsZero())
	assert.True(t, got.FinishedAt.IsZero())

	started := created.Add(time.Second)
	updated, err := es.UpdateExperiment(ctx, "e", func(r *sim.ExperimentRecord) error {
		r.StartedAt = started
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, started.UTC().Truncate(time.Microsecond), updated.StartedAt, "the returned record is what was stored")
	got, err = es.GetExperiment(ctx, "e")
	require.NoError(t, err)
	assert.Equal(t, updated.StartedAt, got.StartedAt)

	b, err := es.SaveModel(ctx, sim.ModelBundle{ID: "m", Format: "yaml"})
	require.NoError(t, err)
	assert.Equal(t, time.UTC, b.CreatedAt.Location())
	assert.Equal(t, b.CreatedAt, b.CreatedAt.Truncate(time.Microsecond))
	m, err := es.GetModel(ctx, "m", "")
	require.NoError(t, err)
	assert.Equal(t, b.CreatedAt, m.CreatedAt)
}

func TestExperimentStore_PageTokens(t *testing.T) {
	ctx := context.Background()
	es, st := newExperimentStore(t)
	base := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	for i := range 3 {
		require.NoError(t, es.CreateExperiment(ctx, sim.ExperimentRecord{ID: fmt.Sprintf("e%d", i), Status: sim.ExperimentQueued, CreatedAt: base.Add(time.Duration(i) * time.Second)}))
	}
	page, next, err := es.ListExperiments(ctx, sim.ListExperimentsOptions{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, "e2", page[0].ID)
	require.NotEmpty(t, next)

	// The token is a keyset position, not a record reference: it keeps
	// working after the record it names is gone.
	_, err = st.Pool().Exec(ctx, "DELETE FROM sim_experiments WHERE id = 'e2'")
	require.NoError(t, err)
	page, _, err = es.ListExperiments(ctx, sim.ListExperimentsOptions{Limit: 5, PageToken: next})
	require.NoError(t, err)
	require.Len(t, page, 2)
	assert.Equal(t, "e1", page[0].ID)
	assert.Equal(t, "e0", page[1].ID)

	for _, token := range []string{
		"no-such-token",
		"!!!",
		base64.RawURLEncoding.EncodeToString([]byte("not json")),
		base64.RawURLEncoding.EncodeToString([]byte(`{"c":1}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"c":1,"i":"x","extra":true}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"c":"x","i":"x"}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"c":1,"i":"x"} {}`)),
		next + "=",
	} {
		_, _, err := es.ListExperiments(ctx, sim.ListExperimentsOptions{PageToken: token})
		assert.ErrorIs(t, err, sim.ErrInvalidPageToken, "token %q", token)
	}
}

func TestExperimentStore_ClosedIsUnavailable(t *testing.T) {
	ctx := context.Background()
	es, st := newExperimentStore(t)
	st.Close()
	_, err := es.SaveModel(ctx, sim.ModelBundle{ID: "m"})
	assert.True(t, engine.IsStoreUnavailable(err), "SaveModel: %v", err)
	_, err = es.GetModel(ctx, "m", "")
	assert.True(t, engine.IsStoreUnavailable(err), "GetModel: %v", err)
	_, err = es.ListModels(ctx)
	assert.True(t, engine.IsStoreUnavailable(err), "ListModels: %v", err)
	err = es.CreateExperiment(ctx, sim.ExperimentRecord{ID: "e"})
	assert.True(t, engine.IsStoreUnavailable(err), "CreateExperiment: %v", err)
	_, err = es.GetExperiment(ctx, "e")
	assert.True(t, engine.IsStoreUnavailable(err), "GetExperiment: %v", err)
	_, _, err = es.ListExperiments(ctx, sim.ListExperimentsOptions{})
	assert.True(t, engine.IsStoreUnavailable(err), "ListExperiments: %v", err)
	_, err = es.UpdateExperiment(ctx, "e", func(*sim.ExperimentRecord) error { return nil })
	assert.True(t, engine.IsStoreUnavailable(err), "UpdateExperiment: %v", err)
}

func TestExperimentStore_CancelledContext(t *testing.T) {
	es, _ := newExperimentStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := es.SaveModel(ctx, sim.ModelBundle{ID: "m"})
	assert.True(t, errors.Is(err, context.Canceled), "%v", err)
	_, _, err = es.ListExperiments(ctx, sim.ListExperimentsOptions{})
	assert.True(t, errors.Is(err, context.Canceled), "%v", err)
}

// TestExperimentStore_PersistsAcrossStores checks that a second Store over
// the same schema (a restarted server) reads what the first one wrote.
func TestExperimentStore_PersistsAcrossStores(t *testing.T) {
	ctx := context.Background()
	es, st := newExperimentStore(t)
	saved, err := es.SaveModel(ctx, sim.ModelBundle{ID: "m", Sim: []byte("s"), Graph: []byte("g"), Format: "yaml", Policies: map[string][]byte{"p.yaml": []byte("p")}})
	require.NoError(t, err)
	require.NoError(t, es.CreateExperiment(ctx, sim.ExperimentRecord{ID: "e", Model: sim.ModelRef{ID: "m", Version: saved.Version}, Status: sim.ExperimentSucceeded, Report: []byte(`{"a":1}`)}))

	var schema string
	require.NoError(t, st.Pool().QueryRow(ctx, "SELECT current_schema()").Scan(&schema))
	st2, err := postgresstore.New(ctx, postgresstore.Config{ConnString: getPostgresConnString(t), Schema: schema, AutoMigrate: true})
	require.NoError(t, err)
	defer st2.Close()
	es2 := postgresstore.NewExperimentStore(st2)

	m, err := es2.GetModel(ctx, "m", "")
	require.NoError(t, err)
	assert.Equal(t, "1", m.Version)
	assert.Equal(t, []byte("p"), m.Policies["p.yaml"])
	rec, err := es2.GetExperiment(ctx, "e")
	require.NoError(t, err)
	assert.Equal(t, sim.ExperimentSucceeded, rec.Status)
	assert.Equal(t, []byte(`{"a":1}`), rec.Report)
	next, err := es2.SaveModel(ctx, sim.ModelBundle{ID: "m", Format: "yaml"})
	require.NoError(t, err)
	assert.Equal(t, "2", next.Version, "versioning continues after a restart")
}

// TestMigrations_SimulationDown checks that rolling back 000002 drops only
// the sim_* tables.
func TestMigrations_SimulationDown(t *testing.T) {
	ctx := context.Background()
	_, st := newExperimentStore(t)
	var schema string
	require.NoError(t, st.Pool().QueryRow(ctx, "SELECT current_schema()").Scan(&schema))
	tableExists := func(name string) bool {
		var exists bool
		require.NoError(t, st.Pool().QueryRow(ctx, "SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2)", schema, name).Scan(&exists))
		return exists
	}
	ver, dirty, err := st.MigrationVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(3), ver)
	require.False(t, dirty)
	simTables := []string{"sim_models", "sim_model_policies", "sim_experiments", "sim_reports"}
	for _, name := range simTables {
		require.True(t, tableExists(name), name)
	}

	// Migrate down twice since we are at version 3
	require.NoError(t, st.MigrateDown(ctx, false))
	require.NoError(t, st.MigrateDown(ctx, false))
	ver, _, err = st.MigrationVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), ver)
	for _, name := range simTables {
		assert.False(t, tableExists(name), name)
	}
	assert.True(t, tableExists("executions"), "000001 tables are kept")

	require.NoError(t, st.MigrateUp(ctx))
	ver, _, err = st.MigrationVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(3), ver)
	for _, name := range simTables {
		assert.True(t, tableExists(name), name)
	}
}

// TestMigrations_ConcurrentUp starts several stores on one fresh schema at
// once (a server and workers booting together): every migration is applied
// exactly once and all of them succeed.
func TestMigrations_ConcurrentUp(t *testing.T) {
	ctx := context.Background()
	connStr := getPostgresConnString(t)
	schema := fmt.Sprintf("cmig_%d_%d", time.Now().UnixNano(), rand.Intn(100000))
	admin, err := postgresstore.New(ctx, postgresstore.Config{ConnString: connStr})
	require.NoError(t, err)
	defer admin.Close()
	_, err = admin.Pool().Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s", schema))
	require.NoError(t, err)
	defer func() {
		_, _ = admin.Pool().Exec(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema))
	}()

	const n = 6
	type result struct {
		st  *postgresstore.Store
		err error
	}
	results := make(chan result, n)
	start := make(chan struct{})
	for range n {
		go func() {
			<-start
			st, err := postgresstore.New(ctx, postgresstore.Config{ConnString: connStr, Schema: schema, AutoMigrate: true})
			results <- result{st, err}
		}()
	}
	close(start)
	for range n {
		r := <-results
		require.NoError(t, r.err)
		defer r.st.Close()
	}
	var count int
	require.NoError(t, admin.Pool().QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s.schema_migrations WHERE NOT dirty", schema)).Scan(&count))
	require.Equal(t, 3, count)
}
