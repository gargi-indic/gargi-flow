package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	postgresstore "github.com/gargi-indic/gargi-flow/internal/store/postgres"
	"github.com/gargi-indic/gargi-flow/internal/store/storetest"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	sharedConnString string
	sharedEmbeddedDB *embeddedpostgres.EmbeddedPostgres
	sharedInitOnce   sync.Once
	sharedInitErr    error
)

func getFreePort(t *testing.T) uint32 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	return uint32(l.Addr().(*net.TCPAddr).Port)
}

func getPostgresConnString(t *testing.T) string {
	t.Helper()
	sharedInitOnce.Do(func() {
		if env := os.Getenv("GARGI_POSTGRES_URL"); env != "" {
			sharedConnString = env
			return
		}
		if env := os.Getenv("POSTGRES_URL"); env != "" {
			sharedConnString = env
			return
		}

		port := getFreePort(t)
		cfg := embeddedpostgres.DefaultConfig().
			Port(port).
			Database("postgres").
			Username("postgres").
			Password("postgres")

		db := embeddedpostgres.NewDatabase(cfg)
		if err := db.Start(); err != nil {
			sharedInitErr = fmt.Errorf("failed to start embedded-postgres: %w", err)
			return
		}

		sharedEmbeddedDB = db
		sharedConnString = fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", port)
	})

	if sharedInitErr != nil {
		t.Fatalf("postgres test environment setup failed: %v", sharedInitErr)
	}

	return sharedConnString
}

func createIsolatedStore(t *testing.T) (*postgresstore.Store, func()) {
	t.Helper()
	connStr := getPostgresConnString(t)
	schema := fmt.Sprintf("t_%d_%d", time.Now().UnixNano(), rand.Intn(100000))
	ctx := context.Background()

	store, err := postgresstore.New(ctx, postgresstore.Config{
		ConnString:  connStr,
		Schema:      schema,
		AutoMigrate: true,
	})
	require.NoError(t, err)

	cleanup := func() {
		if store.Pool() != nil {
			_, _ = store.Pool().Exec(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema))
		}
		store.Close()
	}

	return store, cleanup
}

func TestConformance(t *testing.T) {
	connStr := getPostgresConnString(t)

	storetest.Run(t, storetest.Config{
		NewStore: func(t *testing.T) engine.StateStore {
			schema := fmt.Sprintf("conf_%d_%d", time.Now().UnixNano(), rand.Intn(100000))
			store, err := postgresstore.New(context.Background(), postgresstore.Config{
				ConnString:  connStr,
				Schema:      schema,
				AutoMigrate: true,
			})
			require.NoError(t, err)

			t.Cleanup(func() {
				if store.Pool() != nil {
					_, _ = store.Pool().Exec(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema))
				}
				store.Close()
			})

			return store
		},
	})
}

func TestMigrations_UpDown(t *testing.T) {
	connStr := getPostgresConnString(t)
	schema := fmt.Sprintf("mig_%d_%d", time.Now().UnixNano(), rand.Intn(100000))
	ctx := context.Background()

	// 1. Create with AutoMigrate: false
	store, err := postgresstore.New(ctx, postgresstore.Config{
		ConnString:  connStr,
		Schema:      schema,
		AutoMigrate: false,
	})
	require.NoError(t, err)
	defer func() {
		if store.Pool() != nil {
			_, _ = store.Pool().Exec(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema))
		}
		store.Close()
	}()

	// Initial version should be 0
	ver, dirty, err := store.MigrationVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), ver)
	assert.False(t, dirty)

	// 2. Migrate Up
	err = store.MigrateUp(ctx)
	require.NoError(t, err)

	ver, dirty, err = store.MigrationVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(3), ver)
	assert.False(t, dirty)

	// Verify executions table exists
	var exists bool
	err = store.Pool().QueryRow(ctx, "SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_schema = $1 AND table_name = 'executions')", schema).Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists)

	// 3. Migrate Down (every version)
	err = store.MigrateDown(ctx, true)
	require.NoError(t, err)

	ver, dirty, err = store.MigrationVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), ver)
	assert.False(t, dirty)

	// Verify executions table no longer exists
	err = store.Pool().QueryRow(ctx, "SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_schema = $1 AND table_name = 'executions')", schema).Scan(&exists)
	require.NoError(t, err)
	assert.False(t, exists)

	// 4. Re-apply Migrate Up
	err = store.MigrateUp(ctx)
	require.NoError(t, err)

	ver, dirty, err = store.MigrationVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(3), ver)
	assert.False(t, dirty)
}

func TestStore_ErrorClassifications(t *testing.T) {
	ctx := context.Background()
	store, cleanup := createIsolatedStore(t)
	defer cleanup()

	execID, err := engine.NewExecutionID()
	require.NoError(t, err)

	// 1. StoreNotFoundError
	_, err = store.GetState(ctx, execID)
	require.Error(t, err)
	assert.True(t, engine.IsStoreNotFound(err))
	var notFoundErr *engine.StoreNotFoundError
	assert.True(t, errors.As(err, &notFoundErr))
	assert.Equal(t, execID, notFoundErr.ExecutionID)

	// 2. StoreConflictError
	n, _ := engine.NewNode("n1", "worker", nil)
	g, _ := engine.NewGraph("g1", []*engine.Node{n}, nil)
	initState, _ := engine.NewGraphState(execID, g)

	// Wrong expectedVersion on initial save
	err = store.SetState(ctx, execID, initState, 5)
	require.Error(t, err)
	assert.True(t, engine.IsStoreConflict(err))
	var conflictErr *engine.StoreConflictError
	assert.True(t, errors.As(err, &conflictErr))
	assert.Equal(t, uint64(5), conflictErr.ExpectedVersion)
	assert.Equal(t, uint64(0), conflictErr.ActualVersion)

	// Save valid initial state
	err = store.SetState(ctx, execID, initState, 0)
	require.NoError(t, err)

	// Wrong expectedVersion on update
	s1, _ := initState.WithStatus(0, engine.StatusReady)
	err = store.SetState(ctx, execID, s1, 99)
	require.Error(t, err)
	assert.True(t, engine.IsStoreConflict(err))

	// PatchState with wrong version
	readyStatus := engine.StatusReady
	m := engine.NewStateMutation("n1", 0, &readyStatus, nil, nil, nil)
	_, err = store.PatchState(ctx, execID, &m, 99)
	require.Error(t, err)
	assert.True(t, engine.IsStoreConflict(err))

	// 3. StoreUnavailableError on unreachable server
	_, err = postgresstore.New(ctx, postgresstore.Config{
		ConnString: "postgres://postgres:postgres@127.0.0.1:59999/postgres?connect_timeout=1",
	})
	require.Error(t, err)
	assert.True(t, engine.IsStoreUnavailable(err))
	var unavailErr *engine.StoreUnavailableError
	assert.True(t, errors.As(err, &unavailErr))
	assert.Equal(t, "postgres", unavailErr.Backend)

	// 4. StoreUnavailableError when store is closed
	storeClosed, cleanupClosed := createIsolatedStore(t)
	defer cleanupClosed()
	storeClosed.Close()

	_, err = storeClosed.GetState(ctx, execID)
	assert.True(t, engine.IsStoreUnavailable(err))
	err = storeClosed.SetState(ctx, execID, initState, 0)
	assert.True(t, engine.IsStoreUnavailable(err))
	_, err = storeClosed.AcquireLock(ctx, "res", time.Second)
	assert.True(t, engine.IsStoreUnavailable(err))
}

func TestDistributedLock_ConcurrencyAndExpiration(t *testing.T) {
	ctx := context.Background()
	store, cleanup := createIsolatedStore(t)
	defer cleanup()

	resource := fmt.Sprintf("lock-test-%d", time.Now().UnixNano())

	// 1. Concurrency: 10 goroutines competing for the same lock
	const workers = 10
	var successCount atomic.Int32
	var failCount atomic.Int32
	startChan := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(workers)

	var winningHandle *engine.LockHandle
	var handleMu sync.Mutex

	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			<-startChan
			h, err := store.AcquireLock(ctx, resource, 5*time.Second)
			if err == nil {
				successCount.Add(1)
				handleMu.Lock()
				winningHandle = h
				handleMu.Unlock()
			} else {
				failCount.Add(1)
			}
		}()
	}

	close(startChan)
	wg.Wait()

	assert.Equal(t, int32(1), successCount.Load())
	assert.Equal(t, int32(workers-1), failCount.Load())
	require.NotNil(t, winningHandle)

	// 2. Release with wrong token fails
	invalidHandle := &engine.LockHandle{
		Resource: resource,
		Token:    "wrong-token",
	}
	err := store.ReleaseLock(ctx, invalidHandle)
	assert.Error(t, err)

	// 3. Extend with wrong token fails
	err = store.ExtendLock(ctx, invalidHandle, 2*time.Second)
	assert.Error(t, err)

	// 4. Release with winning token succeeds
	err = store.ReleaseLock(ctx, winningHandle)
	require.NoError(t, err)

	// 5. Expiration: Acquire with 50ms TTL, wait 80ms, ensure re-acquirable
	hShort, err := store.AcquireLock(ctx, resource, 50*time.Millisecond)
	require.NoError(t, err)
	require.NotNil(t, hShort)

	time.Sleep(90 * time.Millisecond)

	// Releasing expired handle should fail
	err = store.ReleaseLock(ctx, hShort)
	assert.Error(t, err)

	// Another caller can now acquire the expired lock
	hNew, err := store.AcquireLock(ctx, resource, 2*time.Second)
	require.NoError(t, err)
	require.NotNil(t, hNew)
	_ = store.ReleaseLock(ctx, hNew)
}

func TestAuditQueries(t *testing.T) {
	ctx := context.Background()
	store, cleanup := createIsolatedStore(t)
	defer cleanup()

	now := time.Now()
	n1, _ := engine.NewNode("nodeA", "worker", nil)
	n2, _ := engine.NewNode("nodeB", "worker", nil)
	g, _ := engine.NewGraph("audit-graph-1", []*engine.Node{n1, n2}, nil)

	// Execution 1
	e1ID, _ := engine.NewExecutionID()
	s1, _ := engine.NewGraphState(e1ID, g)
	require.NoError(t, store.SetState(ctx, e1ID, s1, 0))

	// Execution 2
	e2ID, _ := engine.NewExecutionID()
	s2, _ := engine.NewGraphState(e2ID, g)
	require.NoError(t, store.SetState(ctx, e2ID, s2, 0))
	s2Ready, _ := s2.WithStatus(0, engine.StatusReady)
	require.NoError(t, store.SetState(ctx, e2ID, s2Ready, 0))

	// Execution 3 with different graph ID
	g2, _ := engine.NewGraph("audit-graph-2", []*engine.Node{n1}, nil)
	e3ID, _ := engine.NewExecutionID()
	s3, _ := engine.NewGraphState(e3ID, g2)
	s3Ready, err := s3.WithStatus(0, engine.StatusReady)
	require.NoError(t, err)
	require.NoError(t, store.SetState(ctx, e3ID, s3Ready, 0))

	s3Running, err := s3Ready.WithStatus(1, engine.StatusRunning)
	require.NoError(t, err)
	require.NoError(t, store.SetState(ctx, e3ID, s3Running, 1))

	s3Success, err := s3Running.WithStatus(2, engine.StatusSucceeded)
	require.NoError(t, err)
	require.NoError(t, store.SetState(ctx, e3ID, s3Success, 2))

	// 1. ListExecutions by Status
	summaries, err := store.ListExecutions(ctx, engine.ExecutionFilter{
		Status: engine.StatusSucceeded,
	})
	require.NoError(t, err)
	assert.Len(t, summaries, 1)
	assert.Equal(t, e3ID, summaries[0].ExecutionID)
	assert.Equal(t, "audit-graph-2", summaries[0].GraphID)

	// ListExecutions by GraphID
	summaries, err = store.ListExecutions(ctx, engine.ExecutionFilter{
		GraphID: "audit-graph-1",
	})
	require.NoError(t, err)
	assert.Len(t, summaries, 2)

	// ListExecutions by Time Range
	start := now.Add(-10 * time.Minute)
	end := now.Add(10 * time.Minute)
	summaries, err = store.ListExecutions(ctx, engine.ExecutionFilter{
		StartTime: &start,
		EndTime:   &end,
		Limit:     2,
	})
	require.NoError(t, err)
	assert.Len(t, summaries, 2)

	// 2. QueryNodeStates
	// Patch nodeA in e1ID
	readyStatus := engine.StatusReady
	m1 := engine.NewStateMutation("nodeA", 0, &readyStatus, map[string]any{"result": "ok", "count": 42}, nil, nil)
	_, err = store.PatchState(ctx, e1ID, &m1, 0)
	require.NoError(t, err)

	records, err := store.QueryNodeStates(ctx, e1ID, "nodeA", engine.StatusReady)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, e1ID, records[0].ExecutionID)
	assert.Equal(t, engine.NodeID("nodeA"), records[0].NodeID)
	assert.Equal(t, engine.StatusReady, records[0].Status)
	assert.Equal(t, "ok", records[0].Output["result"])

	// 3. QueryAuditTrail
	t1 := now.Add(-3 * time.Second)
	t2 := now.Add(-2 * time.Second)
	t3 := now.Add(-1 * time.Second)

	require.NoError(t, store.AppendLog(ctx, &engine.ExecutionLogEntry{
		ExecutionID: e1ID,
		Sequence:    1,
		Timestamp:   t1,
		Message:     "Step 1 started",
		Details:     map[string]any{"info": "init"},
	}))
	require.NoError(t, store.AppendLog(ctx, &engine.ExecutionLogEntry{
		ExecutionID: e1ID,
		Sequence:    2,
		Timestamp:   t2,
		Message:     "Step 2 processed",
	}))
	require.NoError(t, store.AppendLog(ctx, &engine.ExecutionLogEntry{
		ExecutionID: e1ID,
		Sequence:    3,
		Timestamp:   t3,
		Message:     "Step 3 completed",
	}))

	// Query entire audit trail
	trail, err := store.QueryAuditTrail(ctx, e1ID, nil, nil)
	require.NoError(t, err)
	assert.Len(t, trail, 3)

	// Query with time range [t2, t3]
	trailSub, err := store.QueryAuditTrail(ctx, e1ID, &t2, &t3)
	require.NoError(t, err)
	assert.Len(t, trailSub, 2)
	assert.Equal(t, uint64(2), trailSub[0].Sequence)
	assert.Equal(t, uint64(3), trailSub[1].Sequence)
}

func TestEventPublisher(t *testing.T) {
	ctx := context.Background()
	store, cleanup := createIsolatedStore(t)
	defer cleanup()

	execID, err := engine.NewExecutionID()
	require.NoError(t, err)

	ev1, err := engine.NewEvent(execID, 1, time.Now(), engine.EventTypeNodeStateChanged, "node1", engine.NodeStateChangedPayload{
		OldStatus: engine.StatusPending,
		NewStatus: engine.StatusRunning,
	})
	require.NoError(t, err)

	err = store.PublishEvent(ctx, ev1)
	require.NoError(t, err)

	ev2, err := engine.NewEvent(execID, 2, time.Now(), engine.EventTypeNodeCompleted, "node1", engine.NodeCompletedPayload{})
	require.NoError(t, err)
	ev3, err := engine.NewEvent(execID, 3, time.Now(), engine.EventTypeGraphCompleted, "", engine.GraphCompletedPayload{
		FinalStatus: engine.StatusSucceeded,
	})
	require.NoError(t, err)

	err = store.PublishBatch(ctx, []*engine.Event{ev2, ev3})
	require.NoError(t, err)

	// Verify events count in table
	var count int
	err = store.Pool().QueryRow(ctx, "SELECT count(*) FROM execution_events WHERE execution_id = $1", string(execID)).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 3, count)
}

func TestStore_ConstructorsAndPool(t *testing.T) {
	connStr := getPostgresConnString(t)
	ctx := context.Background()
	schema := fmt.Sprintf("cp_%d_%d", time.Now().UnixNano(), rand.Intn(100000))

	s1, err := postgresstore.NewStore(ctx, postgresstore.Config{
		ConnString:  connStr,
		Schema:      schema,
		AutoMigrate: true,
	})
	require.NoError(t, err)
	defer func() {
		if s1.Pool() != nil {
			_, _ = s1.Pool().Exec(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema))
		}
		s1.Close()
	}()

	assert.NotNil(t, s1.Pool())

	// Test passing ExistingPool
	poolCfg, err := pgxpool.ParseConfig(connStr)
	require.NoError(t, err)
	externalPool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	require.NoError(t, err)
	defer externalPool.Close()

	s2, err := postgresstore.New(ctx, postgresstore.Config{
		ExistingPool: externalPool,
		AutoMigrate:  false,
	})
	require.NoError(t, err)
	defer s2.Close()
	assert.Equal(t, externalPool, s2.Pool())
}
