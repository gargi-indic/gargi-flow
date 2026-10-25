package postgres_test

import (
	"context"
	"testing"

	"github.com/gargi-indic/gargi-flow/internal/store/postgres"
	"github.com/gargi-indic/gargi-flow/internal/store/storetest"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestPolicyStore(t *testing.T) {
	connStr := getPostgresConnString(t)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	defer pool.Close()

	// Run migrations
	migrator, err := postgres.NewMigrator(pool)
	require.NoError(t, err)

	err = migrator.Up(ctx)
	require.NoError(t, err)

	// Clean up tables before tests
	_, err = pool.Exec(ctx, "TRUNCATE TABLE policy_deployments CASCADE")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "TRUNCATE TABLE policies CASCADE")
	require.NoError(t, err)

	store := postgres.NewPolicyStore(pool)

	storetest.RunPolicyStoreTests(t, store)
}
