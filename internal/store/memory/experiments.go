package memory

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

var _ sim.ExperimentStore = (*ExperimentStore)(nil)

// DefaultExperimentPageSize is the page size of ListExperiments when the
// caller passes no limit.
const DefaultExperimentPageSize = 50

// ExperimentStore is an in-memory, thread-safe sim.ExperimentStore. Bundles
// and records are deep-copied on every read and write. It also implements engine.PolicyStore.
type ExperimentStore struct {
	mu          sync.RWMutex
	models      map[string][]sim.ModelBundle // versions in order, index = version-1
	experiments map[string]sim.ExperimentRecord
	now         func() time.Time
	*PolicyStore
	outcomes []sim.OutcomeRecord
}

// NewExperimentStore returns an empty in-memory experiment store.
func NewExperimentStore() *ExperimentStore {
	return &ExperimentStore{
		models:      make(map[string][]sim.ModelBundle),
		experiments: make(map[string]sim.ExperimentRecord),
		now:         func() time.Time { return time.Now().UTC() },
		PolicyStore: NewPolicyStore(),
		outcomes:    make([]sim.OutcomeRecord, 0),
	}
}

func modelNotFound(id, version string) error {
	entity := fmt.Sprintf("model %q", id)
	if version != "" {
		entity = fmt.Sprintf("model %q version %q", id, version)
	}
	return &engine.StoreNotFoundError{Entity: entity}
}

func experimentNotFound(id string) error {
	return &engine.StoreNotFoundError{Entity: fmt.Sprintf("experiment %q", id)}
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

func cloneRecord(r sim.ExperimentRecord) sim.ExperimentRecord {
	out := r
	out.Definition = slices.Clone(r.Definition)
	out.Report = slices.Clone(r.Report)
	return out
}

// SaveModel implements sim.ExperimentStore.
func (s *ExperimentStore) SaveModel(ctx context.Context, b sim.ModelBundle) (sim.ModelBundle, error) {
	if err := ctx.Err(); err != nil {
		return sim.ModelBundle{}, err
	}
	if strings.TrimSpace(b.ID) == "" {
		return sim.ModelBundle{}, errors.New("memory: model id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	saved := cloneBundle(b)
	saved.Version = strconv.Itoa(len(s.models[b.ID]) + 1)
	saved.CreatedAt = s.now()
	s.models[b.ID] = append(s.models[b.ID], saved)
	if s.PolicyStore != nil {
		for name, doc := range b.Policies {
			format := "yaml"
			if strings.HasSuffix(strings.ToLower(name), ".json") {
				format = "json"
			}
			policyID := strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".json")
			_, _ = s.SavePolicy(ctx, &engine.PolicyRecord{
				ID:       policyID,
				GraphID:  b.ID,
				Document: doc,
				Format:   format,
			})
			if policyID != name {
				_, _ = s.SavePolicy(ctx, &engine.PolicyRecord{
					ID:       name,
					GraphID:  b.ID,
					Document: doc,
					Format:   format,
				})
			}
		}
	}
	return cloneBundle(saved), nil
}

// GetModel implements sim.ExperimentStore.
func (s *ExperimentStore) GetModel(ctx context.Context, id, version string) (sim.ModelBundle, error) {
	if err := ctx.Err(); err != nil {
		return sim.ModelBundle{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	versions := s.models[id]
	if len(versions) == 0 {
		return sim.ModelBundle{}, modelNotFound(id, version)
	}
	if version == "" {
		return cloneBundle(versions[len(versions)-1]), nil
	}
	n, err := strconv.Atoi(version)
	if err != nil || n < 1 || n > len(versions) || strconv.Itoa(n) != version {
		return sim.ModelBundle{}, modelNotFound(id, version)
	}
	return cloneBundle(versions[n-1]), nil
}

// ListModels implements sim.ExperimentStore.
func (s *ExperimentStore) ListModels(ctx context.Context) ([]sim.ModelBundle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := slices.Sorted(maps.Keys(s.models))
	out := make([]sim.ModelBundle, 0, len(ids))
	for _, id := range ids {
		versions := s.models[id]
		out = append(out, cloneBundle(versions[len(versions)-1]))
	}
	return out, nil
}

// CreateExperiment implements sim.ExperimentStore.
func (s *ExperimentStore) CreateExperiment(ctx context.Context, rec sim.ExperimentRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(rec.ID) == "" {
		return errors.New("memory: experiment id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.experiments[rec.ID]; ok {
		return fmt.Errorf("%w: experiment %q already exists", engine.ErrStoreConflict, rec.ID)
	}
	stored := cloneRecord(rec)
	if stored.CreatedAt.IsZero() {
		stored.CreatedAt = s.now()
	}
	s.experiments[rec.ID] = stored
	return nil
}

// GetExperiment implements sim.ExperimentStore.
func (s *ExperimentStore) GetExperiment(ctx context.Context, id string) (sim.ExperimentRecord, error) {
	if err := ctx.Err(); err != nil {
		return sim.ExperimentRecord{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.experiments[id]
	if !ok {
		return sim.ExperimentRecord{}, experimentNotFound(id)
	}
	return cloneRecord(rec), nil
}

// newerFirst reports whether a sorts before b in ListExperiments order.
func newerFirst(a, b sim.ExperimentRecord) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.ID > b.ID
}

// ListExperiments implements sim.ExperimentStore. The page token is the ID
// of the last record of the previous page.
func (s *ExperimentStore) ListExperiments(ctx context.Context, opts sim.ListExperimentsOptions) ([]sim.ExperimentRecord, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultExperimentPageSize
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := make([]sim.ExperimentRecord, 0, len(s.experiments))
	for _, rec := range s.experiments {
		all = append(all, rec)
	}
	sort.Slice(all, func(i, j int) bool { return newerFirst(all[i], all[j]) })
	start := 0
	if opts.PageToken != "" {
		after, ok := s.experiments[opts.PageToken]
		if !ok {
			return nil, "", fmt.Errorf("%w: %q", sim.ErrInvalidPageToken, opts.PageToken)
		}
		start = sort.Search(len(all), func(i int) bool { return !newerFirst(all[i], after) })
		if start < len(all) && all[start].ID == after.ID {
			start++
		}
	}
	end := min(start+limit, len(all))
	page := make([]sim.ExperimentRecord, 0, end-start)
	for _, rec := range all[start:end] {
		page = append(page, cloneRecord(rec))
	}
	next := ""
	if end < len(all) && len(page) > 0 {
		next = page[len(page)-1].ID
	}
	return page, next, nil
}

// UpdateExperiment implements sim.ExperimentStore.
func (s *ExperimentStore) UpdateExperiment(ctx context.Context, id string, fn func(*sim.ExperimentRecord) error) (sim.ExperimentRecord, error) {
	if err := ctx.Err(); err != nil {
		return sim.ExperimentRecord{}, err
	}
	if fn == nil {
		return sim.ExperimentRecord{}, errors.New("memory: update function is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.experiments[id]
	if !ok {
		return sim.ExperimentRecord{}, experimentNotFound(id)
	}
	work := cloneRecord(rec)
	if err := fn(&work); err != nil {
		return sim.ExperimentRecord{}, err
	}
	work.ID = id
	s.experiments[id] = cloneRecord(work)
	return work, nil
}

func cloneOutcome(o sim.OutcomeRecord) sim.OutcomeRecord {
	out := o
	if o.Latents != nil {
		out.Latents = make(map[string]any, len(o.Latents))
		for k, v := range o.Latents {
			out.Latents[k] = v
		}
	}
	return out
}

// IngestOutcome implements sim.ExperimentStore.
func (s *ExperimentStore) IngestOutcome(ctx context.Context, rec sim.OutcomeRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if rec.ExecutionID == "" {
		return errors.New("memory: execution id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	cloned := cloneOutcome(rec)
	for i, existing := range s.outcomes {
		if existing.ExecutionID == rec.ExecutionID {
			s.outcomes[i] = cloned
			return nil
		}
	}
	s.outcomes = append(s.outcomes, cloned)
	return nil
}

// ListOutcomes implements sim.ExperimentStore.
func (s *ExperimentStore) ListOutcomes(ctx context.Context, opts sim.OutcomeQueryOptions) ([]sim.OutcomeRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []sim.OutcomeRecord
	for _, rec := range s.outcomes {
		if opts.GraphID != "" && rec.GraphID != opts.GraphID {
			continue
		}
		if opts.PolicyVersion != "" && rec.PolicyVersion != opts.PolicyVersion {
			continue
		}
		out = append(out, cloneOutcome(rec))
	}
	return out, nil
}
