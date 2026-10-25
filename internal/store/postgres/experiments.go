package postgres

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
	"github.com/jackc/pgx/v5"
)

var _ sim.ExperimentStore = (*ExperimentStore)(nil)

// DefaultExperimentPageSize is the page size of ListExperiments when the
// caller passes no limit.
const DefaultExperimentPageSize = 50

// ExperimentStore is a PostgreSQL-backed sim.ExperimentStore over the
// sim_models, sim_model_policies, sim_experiments and sim_reports tables
// (migration 000002_simulation).
//
// Documents and reports are stored as BYTEA and round-trip byte for byte.
// Timestamps are stored and returned in UTC, truncated to microseconds
// (the precision of timestamptz). Model versions are integers in SQL and
// decimal strings in the contract.
type ExperimentStore struct {
	st  *Store
	now func() time.Time
}

// NewExperimentStore returns an ExperimentStore that shares the connection
// pool and schema of s. s keeps owning the pool: closing s closes the
// experiment store too. The tables are created by s's migrations
// (Config.AutoMigrate or Store.MigrateUp).
func NewExperimentStore(s *Store) *ExperimentStore {
	return &ExperimentStore{st: s, now: time.Now}
}

// normTime returns t in UTC truncated to microseconds; the zero time stays
// zero.
func normTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return t.UTC().Truncate(time.Microsecond)
}

// nullTime maps the zero time to SQL NULL.
func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = normTime(t)
	return &t
}

// fromNullTime maps SQL NULL to the zero time.
func fromNullTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return normTime(*t)
}

// nonNil returns b, or an empty slice when b is nil (pgx encodes a nil
// []byte as NULL).
func nonNil(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

func (e *ExperimentStore) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.st.closed.Load() {
		return e.st.errClosed()
	}
	return nil
}

func modelNotFound(id, version string) error {
	entity := fmt.Sprintf("model %q", id)
	if version != "" {
		entity = fmt.Sprintf("model %q version %q", id, version)
	}
	return &engine.StoreNotFoundError{Entity: entity, Err: engine.ErrStoreNotFound}
}

func experimentNotFound(id string) error {
	return &engine.StoreNotFoundError{Entity: fmt.Sprintf("experiment %q", id), Err: engine.ErrStoreNotFound}
}

// parseVersion parses a canonical decimal model version ("1", "2", ...).
func parseVersion(v string) (int32, bool) {
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || n < 1 || strconv.FormatInt(n, 10) != v {
		return 0, false
	}
	return int32(n), true
}

// SaveModel implements sim.ExperimentStore. It assigns the next version
// inside one transaction that holds a transaction-scoped advisory lock on
// the model ID, so concurrent saves of one ID get distinct consecutive
// versions.
func (e *ExperimentStore) SaveModel(ctx context.Context, b sim.ModelBundle) (sim.ModelBundle, error) {
	if err := e.check(ctx); err != nil {
		return sim.ModelBundle{}, err
	}
	if strings.TrimSpace(b.ID) == "" {
		return sim.ModelBundle{}, errors.New("postgres: model id is required")
	}
	tx, err := e.st.pool.Begin(ctx)
	if err != nil {
		return sim.ModelBundle{}, e.st.wrapErr(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Serialize version assignment per model ID (per schema). Under READ
	// COMMITTED the next statement's snapshot sees the previous holder's
	// committed version.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext(COALESCE(current_schema(), '') || '.sim_models'), hashtext($1))`, b.ID); err != nil {
		return sim.ModelBundle{}, e.st.wrapErr(err)
	}
	var version int32
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) + 1 FROM sim_models WHERE id = $1`, b.ID).Scan(&version); err != nil {
		return sim.ModelBundle{}, e.st.wrapErr(err)
	}
	createdAt := normTime(e.now())
	if _, err := tx.Exec(ctx, `
INSERT INTO sim_models (id, version, format, sim, graph, created_at)
VALUES ($1, $2, $3, $4, $5, $6)
`, b.ID, version, b.Format, nonNil(b.Sim), nonNil(b.Graph), createdAt); err != nil {
		return sim.ModelBundle{}, e.st.wrapErr(err)
	}
	for _, name := range slices.Sorted(maps.Keys(b.Policies)) {
		if _, err := tx.Exec(ctx, `
INSERT INTO sim_model_policies (model_id, model_version, name, document)
VALUES ($1, $2, $3, $4)
`, b.ID, version, name, nonNil(b.Policies[name])); err != nil {
			return sim.ModelBundle{}, e.st.wrapErr(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return sim.ModelBundle{}, e.st.wrapErr(err)
	}
	saved := cloneBundle(b)
	saved.Version = strconv.FormatInt(int64(version), 10)
	saved.CreatedAt = createdAt
	return saved, nil
}

func cloneBundle(b sim.ModelBundle) sim.ModelBundle {
	out := b
	out.Sim = slices.Clone(b.Sim)
	out.Graph = slices.Clone(b.Graph)
	if b.Policies != nil {
		out.Policies = make(map[string][]byte, len(b.Policies))
		for k, v := range b.Policies {
			out.Policies[k] = slices.Clone(v)
		}
	}
	return out
}

// GetModel implements sim.ExperimentStore.
func (e *ExperimentStore) GetModel(ctx context.Context, id, version string) (sim.ModelBundle, error) {
	if err := e.check(ctx); err != nil {
		return sim.ModelBundle{}, err
	}
	var row pgx.Row
	if version == "" {
		row = e.st.pool.QueryRow(ctx, `
SELECT id, version, format, sim, graph, created_at FROM sim_models
WHERE id = $1 ORDER BY version DESC LIMIT 1
`, id)
	} else {
		n, ok := parseVersion(version)
		if !ok {
			return sim.ModelBundle{}, modelNotFound(id, version)
		}
		row = e.st.pool.QueryRow(ctx, `
SELECT id, version, format, sim, graph, created_at FROM sim_models
WHERE id = $1 AND version = $2
`, id, n)
	}
	b, n, err := scanBundle(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return sim.ModelBundle{}, modelNotFound(id, version)
	}
	if err != nil {
		return sim.ModelBundle{}, e.st.wrapErr(err)
	}
	// Versions are immutable, so reading the policies outside the first
	// query's snapshot is consistent.
	rows, err := e.st.pool.Query(ctx, `
SELECT name, document FROM sim_model_policies
WHERE model_id = $1 AND model_version = $2
`, id, n)
	if err != nil {
		return sim.ModelBundle{}, e.st.wrapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			name string
			doc  []byte
		)
		if err := rows.Scan(&name, &doc); err != nil {
			return sim.ModelBundle{}, e.st.wrapErr(err)
		}
		if b.Policies == nil {
			b.Policies = make(map[string][]byte)
		}
		b.Policies[name] = doc
	}
	if err := rows.Err(); err != nil {
		return sim.ModelBundle{}, e.st.wrapErr(err)
	}
	return b, nil
}

func scanBundle(row pgx.Row) (sim.ModelBundle, int32, error) {
	var (
		b       sim.ModelBundle
		version int32
		created time.Time
	)
	if err := row.Scan(&b.ID, &version, &b.Format, &b.Sim, &b.Graph, &created); err != nil {
		return sim.ModelBundle{}, 0, err
	}
	b.Version = strconv.FormatInt(int64(version), 10)
	b.CreatedAt = normTime(created)
	return b, version, nil
}

// ListModels implements sim.ExperimentStore. Bundles and their policies are
// read in one repeatable-read snapshot.
func (e *ExperimentStore) ListModels(ctx context.Context) ([]sim.ModelBundle, error) {
	if err := e.check(ctx); err != nil {
		return nil, err
	}
	tx, err := e.st.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, e.st.wrapErr(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	rows, err := tx.Query(ctx, `
SELECT DISTINCT ON (id) id, version, format, sim, graph, created_at
FROM sim_models ORDER BY id, version DESC
`)
	if err != nil {
		return nil, e.st.wrapErr(err)
	}
	out := make([]sim.ModelBundle, 0)
	index := make(map[string]int)
	for rows.Next() {
		b, _, err := scanBundle(rows)
		if err != nil {
			rows.Close()
			return nil, e.st.wrapErr(err)
		}
		index[b.ID] = len(out)
		out = append(out, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, e.st.wrapErr(err)
	}

	prows, err := tx.Query(ctx, `
SELECT p.model_id, p.name, p.document
FROM sim_model_policies p
JOIN (SELECT id, MAX(version) AS version FROM sim_models GROUP BY id) l
  ON p.model_id = l.id AND p.model_version = l.version
`)
	if err != nil {
		return nil, e.st.wrapErr(err)
	}
	defer prows.Close()
	for prows.Next() {
		var (
			id, name string
			doc      []byte
		)
		if err := prows.Scan(&id, &name, &doc); err != nil {
			return nil, e.st.wrapErr(err)
		}
		i, ok := index[id]
		if !ok {
			continue
		}
		if out[i].Policies == nil {
			out[i].Policies = make(map[string][]byte)
		}
		out[i].Policies[name] = doc
	}
	if err := prows.Err(); err != nil {
		return nil, e.st.wrapErr(err)
	}
	return out, nil
}

// experimentColumns selects an experiment row joined with its report (the
// report alias is r).
const experimentColumns = `e.id, e.model_id, e.model_version, e.definition, e.format, e.status, e.error,
       e.progress, e.created_at, e.started_at, e.finished_at, r.report`

func scanExperiment(row pgx.Row) (sim.ExperimentRecord, error) {
	var (
		rec               sim.ExperimentRecord
		status            string
		progress          []byte
		created           time.Time
		started, finished *time.Time
	)
	if err := row.Scan(&rec.ID, &rec.Model.ID, &rec.Model.Version, &rec.Definition, &rec.Format, &status, &rec.Error,
		&progress, &created, &started, &finished, &rec.Report); err != nil {
		return sim.ExperimentRecord{}, err
	}
	rec.Status = sim.ExperimentStatus(status)
	if err := json.Unmarshal(progress, &rec.Progress); err != nil {
		return sim.ExperimentRecord{}, fmt.Errorf("postgres: decode experiment %q progress: %w", rec.ID, err)
	}
	rec.CreatedAt = normTime(created)
	rec.StartedAt = fromNullTime(started)
	rec.FinishedAt = fromNullTime(finished)
	return rec, nil
}

// normRecord returns a deep copy of rec with its timestamps normalized as
// stored.
func normRecord(rec sim.ExperimentRecord) sim.ExperimentRecord {
	out := rec
	out.Definition = slices.Clone(rec.Definition)
	out.Report = slices.Clone(rec.Report)
	out.CreatedAt = normTime(rec.CreatedAt)
	out.StartedAt = normTime(rec.StartedAt)
	out.FinishedAt = normTime(rec.FinishedAt)
	return out
}

// CreateExperiment implements sim.ExperimentStore.
func (e *ExperimentStore) CreateExperiment(ctx context.Context, rec sim.ExperimentRecord) error {
	if err := e.check(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(rec.ID) == "" {
		return errors.New("postgres: experiment id is required")
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = e.now()
	}
	rec = normRecord(rec)
	progress, err := json.Marshal(rec.Progress)
	if err != nil {
		return fmt.Errorf("postgres: encode experiment progress: %w", err)
	}
	tx, err := e.st.pool.Begin(ctx)
	if err != nil {
		return e.st.wrapErr(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	tag, err := tx.Exec(ctx, `
INSERT INTO sim_experiments (id, model_id, model_version, definition, format, status, error, progress, created_at, started_at, finished_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (id) DO NOTHING
`, rec.ID, rec.Model.ID, rec.Model.Version, nonNil(rec.Definition), rec.Format, string(rec.Status), rec.Error,
		progress, rec.CreatedAt, nullTime(rec.StartedAt), nullTime(rec.FinishedAt))
	if err != nil {
		return e.st.wrapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: experiment %q already exists", engine.ErrStoreConflict, rec.ID)
	}
	if len(rec.Report) > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO sim_reports (experiment_id, report) VALUES ($1, $2)`, rec.ID, rec.Report); err != nil {
			return e.st.wrapErr(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return e.st.wrapErr(err)
	}
	return nil
}

// GetExperiment implements sim.ExperimentStore.
func (e *ExperimentStore) GetExperiment(ctx context.Context, id string) (sim.ExperimentRecord, error) {
	if err := e.check(ctx); err != nil {
		return sim.ExperimentRecord{}, err
	}
	rec, err := scanExperiment(e.st.pool.QueryRow(ctx, `
SELECT `+experimentColumns+`
FROM sim_experiments e LEFT JOIN sim_reports r ON r.experiment_id = e.id
WHERE e.id = $1
`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return sim.ExperimentRecord{}, experimentNotFound(id)
	}
	if err != nil {
		return sim.ExperimentRecord{}, e.st.wrapErr(err)
	}
	return rec, nil
}

// pageToken is the decoded ListExperiments page token: the keyset position
// (created_at, id) of the last record of the previous page.
type pageToken struct {
	// CreatedMicros is created_at in Unix microseconds.
	CreatedMicros int64 `json:"c"`
	// ID is the experiment ID.
	ID string `json:"i"`
}

func encodePageToken(rec sim.ExperimentRecord) string {
	data, _ := json.Marshal(pageToken{CreatedMicros: rec.CreatedAt.UnixMicro(), ID: rec.ID})
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodePageToken(token string) (pageToken, error) {
	invalid := fmt.Errorf("%w: %q", sim.ErrInvalidPageToken, token)
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return pageToken{}, invalid
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var pt pageToken
	if err := dec.Decode(&pt); err != nil || dec.More() || pt.ID == "" {
		return pageToken{}, invalid
	}
	return pt, nil
}

// ListExperiments implements sim.ExperimentStore with keyset pagination
// over (created_at, id), both descending. The page token is opaque; it
// encodes the position of the last record returned.
func (e *ExperimentStore) ListExperiments(ctx context.Context, opts sim.ListExperimentsOptions) ([]sim.ExperimentRecord, string, error) {
	if err := e.check(ctx); err != nil {
		return nil, "", err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultExperimentPageSize
	}
	// One extra row tells whether a next page exists.
	fetch := int64(limit) + 1
	if limit == math.MaxInt {
		fetch = math.MaxInt64
	}
	var (
		rows pgx.Rows
		err  error
	)
	if opts.PageToken == "" {
		rows, err = e.st.pool.Query(ctx, `
SELECT `+experimentColumns+`
FROM sim_experiments e LEFT JOIN sim_reports r ON r.experiment_id = e.id
ORDER BY e.created_at DESC, e.id DESC
LIMIT $1
`, fetch)
	} else {
		pt, perr := decodePageToken(opts.PageToken)
		if perr != nil {
			return nil, "", perr
		}
		rows, err = e.st.pool.Query(ctx, `
SELECT `+experimentColumns+`
FROM sim_experiments e LEFT JOIN sim_reports r ON r.experiment_id = e.id
WHERE (e.created_at, e.id) < ($1, $2)
ORDER BY e.created_at DESC, e.id DESC
LIMIT $3
`, time.UnixMicro(pt.CreatedMicros).UTC(), pt.ID, fetch)
	}
	if err != nil {
		return nil, "", e.st.wrapErr(err)
	}
	defer rows.Close()
	page := make([]sim.ExperimentRecord, 0)
	for rows.Next() {
		rec, err := scanExperiment(rows)
		if err != nil {
			return nil, "", e.st.wrapErr(err)
		}
		page = append(page, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, "", e.st.wrapErr(err)
	}
	next := ""
	if len(page) > limit {
		page = page[:limit]
		next = encodePageToken(page[len(page)-1])
	}
	return page, next, nil
}

// UpdateExperiment implements sim.ExperimentStore. It locks the row with
// SELECT ... FOR UPDATE, applies fn to a copy and writes the result in the
// same transaction; when fn fails the transaction is rolled back.
func (e *ExperimentStore) UpdateExperiment(ctx context.Context, id string, fn func(*sim.ExperimentRecord) error) (sim.ExperimentRecord, error) {
	if err := e.check(ctx); err != nil {
		return sim.ExperimentRecord{}, err
	}
	if fn == nil {
		return sim.ExperimentRecord{}, errors.New("postgres: update function is nil")
	}
	tx, err := e.st.pool.Begin(ctx)
	if err != nil {
		return sim.ExperimentRecord{}, e.st.wrapErr(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	cur, err := scanExperiment(tx.QueryRow(ctx, `
SELECT `+experimentColumns+`
FROM sim_experiments e LEFT JOIN sim_reports r ON r.experiment_id = e.id
WHERE e.id = $1
FOR UPDATE OF e
`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return sim.ExperimentRecord{}, experimentNotFound(id)
	}
	if err != nil {
		return sim.ExperimentRecord{}, e.st.wrapErr(err)
	}
	work := normRecord(cur)
	if err := fn(&work); err != nil {
		return sim.ExperimentRecord{}, err
	}
	work.ID = id
	work = normRecord(work)
	progress, err := json.Marshal(work.Progress)
	if err != nil {
		return sim.ExperimentRecord{}, fmt.Errorf("postgres: encode experiment progress: %w", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE sim_experiments
SET model_id = $2, model_version = $3, definition = $4, format = $5, status = $6, error = $7,
    progress = $8, created_at = $9, started_at = $10, finished_at = $11
WHERE id = $1
`, id, work.Model.ID, work.Model.Version, nonNil(work.Definition), work.Format, string(work.Status), work.Error,
		progress, work.CreatedAt, nullTime(work.StartedAt), nullTime(work.FinishedAt)); err != nil {
		return sim.ExperimentRecord{}, e.st.wrapErr(err)
	}
	if !bytes.Equal(cur.Report, work.Report) {
		if len(work.Report) == 0 {
			_, err = tx.Exec(ctx, `DELETE FROM sim_reports WHERE experiment_id = $1`, id)
		} else {
			_, err = tx.Exec(ctx, `
INSERT INTO sim_reports (experiment_id, report) VALUES ($1, $2)
ON CONFLICT (experiment_id) DO UPDATE SET report = EXCLUDED.report
`, id, work.Report)
		}
		if err != nil {
			return sim.ExperimentRecord{}, e.st.wrapErr(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return sim.ExperimentRecord{}, e.st.wrapErr(err)
	}
	return work, nil
}

// IngestOutcome implements sim.ExperimentStore.
func (e *ExperimentStore) IngestOutcome(ctx context.Context, rec sim.OutcomeRecord) error {
	if err := e.check(ctx); err != nil {
		return err
	}
	if rec.ExecutionID == "" {
		return errors.New("postgres: execution id is required")
	}

	data, err := json.Marshal(rec.Latents)
	if err != nil {
		return fmt.Errorf("marshaling outcome latents: %w", err)
	}

	query := `
		INSERT INTO sim_outcomes (execution_id, graph_id, policy_version, latents, observed_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (execution_id) DO UPDATE SET
			graph_id = EXCLUDED.graph_id,
			policy_version = EXCLUDED.policy_version,
			latents = EXCLUDED.latents,
			observed_at = EXCLUDED.observed_at
	`

	_, err = e.st.pool.Exec(ctx, query,
		rec.ExecutionID,
		rec.GraphID,
		rec.PolicyVersion,
		data,
		rec.ObservedAt,
	)
	if err != nil {
		return fmt.Errorf("ingesting outcome: %w", err)
	}
	return nil
}

// ListOutcomes implements sim.ExperimentStore.
func (e *ExperimentStore) ListOutcomes(ctx context.Context, opts sim.OutcomeQueryOptions) ([]sim.OutcomeRecord, error) {
	if err := e.check(ctx); err != nil {
		return nil, err
	}

	var conditions []string
	var args []any
	argID := 1

	if opts.GraphID != "" {
		conditions = append(conditions, fmt.Sprintf("graph_id = $%d", argID))
		args = append(args, opts.GraphID)
		argID++
	}
	if opts.PolicyVersion != "" {
		conditions = append(conditions, fmt.Sprintf("policy_version = $%d", argID))
		args = append(args, opts.PolicyVersion)
	}

	query := `
		SELECT execution_id, graph_id, policy_version, latents, observed_at
		FROM sim_outcomes
	`
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY observed_at ASC, execution_id ASC"

	rows, err := e.st.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying outcomes: %w", err)
	}
	defer rows.Close()

	var out []sim.OutcomeRecord
	for rows.Next() {
		var rec sim.OutcomeRecord
		var data []byte
		if err := rows.Scan(&rec.ExecutionID, &rec.GraphID, &rec.PolicyVersion, &data, &rec.ObservedAt); err != nil {
			return nil, fmt.Errorf("scanning outcome: %w", err)
		}
		if err := json.Unmarshal(data, &rec.Latents); err != nil {
			return nil, fmt.Errorf("unmarshaling outcome latents: %w", err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}
