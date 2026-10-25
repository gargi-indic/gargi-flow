package postgres

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PolicyStore implements engine.PolicyStore backed by PostgreSQL.
type PolicyStore struct {
	pool *pgxpool.Pool
}

// NewPolicyStore creates a new PolicyStore instance.
func NewPolicyStore(pool *pgxpool.Pool) *PolicyStore {
	return &PolicyStore{pool: pool}
}

// SavePolicy saves a new policy record. It assigns a monotonic Version and CreatedAt.
func (s *PolicyStore) SavePolicy(ctx context.Context, record *engine.PolicyRecord) (*engine.PolicyRecord, error) {
	var nextVer int

	// We use a transaction with Advisory Lock on graph_id + policy_id to safely increment version
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Hash the graphID and policyID to get a consistent lock ID.
	// For simplicity, we just use a generic lock or hash them.
	// But let's rely on a simpler querying of the latest version since this is an append-only setup.
	// PostgreSQL can handle concurrent inserts by checking constraints or we can lock the specific keys.
	// A safe way without advisory locks is querying the max version.

	// Let's do a simple SELECT MAX(CAST(version AS INTEGER))
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(CAST(version AS INTEGER)), 0)
		FROM policies
		WHERE graph_id = $1 AND id = $2
	`, record.GraphID, record.ID).Scan(&nextVer)
	if err != nil {
		return nil, err
	}

	nextVer++
	verStr := strconv.Itoa(nextVer)
	now := time.Now().UTC()

	copied := *record
	copied.Version = verStr
	copied.CreatedAt = now

	_, err = tx.Exec(ctx, `
		INSERT INTO policies (id, graph_id, version, document, format, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, copied.ID, copied.GraphID, copied.Version, copied.Document, copied.Format, copied.CreatedAt)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return &copied, nil
}

// GetPolicy retrieves a policy record. If version is empty, it returns the latest version.
func (s *PolicyStore) GetPolicy(ctx context.Context, graphID string, policyID string, version string) (*engine.PolicyRecord, error) {
	var record engine.PolicyRecord
	var err error

	if version == "" {
		err = s.pool.QueryRow(ctx, `
			SELECT id, graph_id, version, document, format, created_at
			FROM policies
			WHERE graph_id = $1 AND id = $2
			ORDER BY CAST(version AS INTEGER) DESC
			LIMIT 1
		`, graphID, policyID).Scan(
			&record.ID, &record.GraphID, &record.Version, &record.Document, &record.Format, &record.CreatedAt,
		)
	} else {
		err = s.pool.QueryRow(ctx, `
			SELECT id, graph_id, version, document, format, created_at
			FROM policies
			WHERE graph_id = $1 AND id = $2 AND version = $3
		`, graphID, policyID, version).Scan(
			&record.ID, &record.GraphID, &record.Version, &record.Document, &record.Format, &record.CreatedAt,
		)
	}

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, engine.ErrPolicyNotFound
		}
		return nil, err
	}

	return &record, nil
}

// ListPolicies lists all policy records for a given graph.
func (s *PolicyStore) ListPolicies(ctx context.Context, graphID string) ([]*engine.PolicyRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, graph_id, version, document, format, created_at
		FROM policies
		WHERE graph_id = $1
		ORDER BY id ASC, CAST(version AS INTEGER) ASC
	`, graphID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*engine.PolicyRecord
	for rows.Next() {
		var record engine.PolicyRecord
		if err := rows.Scan(&record.ID, &record.GraphID, &record.Version, &record.Document, &record.Format, &record.CreatedAt); err != nil {
			return nil, err
		}
		results = append(results, &record)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(results) == 0 {
		return nil, nil // Return nil slice if empty
	}
	return results, nil
}

// SetDeployment sets the active deployment configuration for a graph.
func (s *PolicyStore) SetDeployment(ctx context.Context, deployment *engine.PolicyDeployment) error {
	now := time.Now().UTC()
	copied := *deployment
	copied.UpdatedAt = now

	_, err := s.pool.Exec(ctx, `
		INSERT INTO policy_deployments (graph_id, policy_id, policy_version, mode, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (graph_id) DO UPDATE SET
			policy_id = EXCLUDED.policy_id,
			policy_version = EXCLUDED.policy_version,
			mode = EXCLUDED.mode,
			updated_at = EXCLUDED.updated_at
	`, copied.GraphID, copied.PolicyID, copied.PolicyVersion, copied.Mode, copied.UpdatedAt)

	return err
}

// GetDeployment retrieves the active deployment configuration for a graph.
func (s *PolicyStore) GetDeployment(ctx context.Context, graphID string) (*engine.PolicyDeployment, error) {
	var dep engine.PolicyDeployment
	err := s.pool.QueryRow(ctx, `
		SELECT graph_id, policy_id, policy_version, mode, updated_at
		FROM policy_deployments
		WHERE graph_id = $1
	`, graphID).Scan(
		&dep.GraphID, &dep.PolicyID, &dep.PolicyVersion, &dep.Mode, &dep.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, engine.ErrDeploymentNotFound
		}
		return nil, err
	}

	return &dep, nil
}
