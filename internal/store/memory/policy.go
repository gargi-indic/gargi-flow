package memory

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
)

type policyKey struct {
	graphID  string
	policyID string
	version  string
}

// PolicyStore is an in-memory implementation of the engine.PolicyStore interface.
type PolicyStore struct {
	mu          sync.RWMutex
	policies    map[policyKey]*engine.PolicyRecord
	deployments map[string]*engine.PolicyDeployment
	latestVer   map[string]int // key: graphID+policyID, val: latest version
}

// NewPolicyStore creates a new in-memory PolicyStore.
func NewPolicyStore() *PolicyStore {
	return &PolicyStore{
		policies:    make(map[policyKey]*engine.PolicyRecord),
		deployments: make(map[string]*engine.PolicyDeployment),
		latestVer:   make(map[string]int),
	}
}

// SavePolicy saves a new policy record. It assigns a monotonic Version and CreatedAt.
func (s *PolicyStore) SavePolicy(_ context.Context, record *engine.PolicyRecord) (*engine.PolicyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	keyPrefix := record.GraphID + ":" + record.ID
	currentVer := s.latestVer[keyPrefix]
	nextVer := currentVer + 1

	s.latestVer[keyPrefix] = nextVer

	verStr := strconv.Itoa(nextVer)
	now := time.Now().UTC()

	copied := *record
	copied.Version = verStr
	copied.CreatedAt = now

	key := policyKey{graphID: record.GraphID, policyID: record.ID, version: verStr}

	// Store a copy to avoid external mutation
	storeCopy := copied
	storeCopy.Document = make([]byte, len(copied.Document))
	copy(storeCopy.Document, copied.Document)

	s.policies[key] = &storeCopy

	// Return a copy to the caller
	retCopy := storeCopy
	retCopy.Document = make([]byte, len(storeCopy.Document))
	copy(retCopy.Document, storeCopy.Document)

	return &retCopy, nil
}

// GetPolicy retrieves a policy record. If version is empty, it returns the latest version.
func (s *PolicyStore) GetPolicy(_ context.Context, graphID string, policyID string, version string) (*engine.PolicyRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	verToFetch := version
	if version == "" {
		keyPrefix := graphID + ":" + policyID
		latest, ok := s.latestVer[keyPrefix]
		if !ok || latest == 0 {
			return nil, engine.ErrPolicyNotFound
		}
		verToFetch = strconv.Itoa(latest)
	}

	key := policyKey{graphID: graphID, policyID: policyID, version: verToFetch}
	record, ok := s.policies[key]
	if !ok {
		return nil, engine.ErrPolicyNotFound
	}

	retCopy := *record
	retCopy.Document = make([]byte, len(record.Document))
	copy(retCopy.Document, record.Document)

	return &retCopy, nil
}

// ListPolicies lists all policy records for a given graph.
func (s *PolicyStore) ListPolicies(_ context.Context, graphID string) ([]*engine.PolicyRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*engine.PolicyRecord
	for k, record := range s.policies {
		if k.graphID == graphID {
			retCopy := *record
			retCopy.Document = make([]byte, len(record.Document))
			copy(retCopy.Document, record.Document)
			result = append(result, &retCopy)
		}
	}

	// Deterministic sorting
	sort.Slice(result, func(i, j int) bool {
		if result[i].ID != result[j].ID {
			return result[i].ID < result[j].ID
		}
		vi, _ := strconv.Atoi(result[i].Version)
		vj, _ := strconv.Atoi(result[j].Version)
		return vi < vj
	})

	return result, nil
}

// SetDeployment sets the active deployment configuration for a graph.
func (s *PolicyStore) SetDeployment(_ context.Context, deployment *engine.PolicyDeployment) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	copied := *deployment
	copied.UpdatedAt = now

	s.deployments[deployment.GraphID] = &copied

	return nil
}

// GetDeployment retrieves the active deployment configuration for a graph.
func (s *PolicyStore) GetDeployment(_ context.Context, graphID string) (*engine.PolicyDeployment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	deployment, ok := s.deployments[graphID]
	if !ok {
		return nil, engine.ErrDeploymentNotFound
	}

	copied := *deployment
	return &copied, nil
}
