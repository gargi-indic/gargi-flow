package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
)

// RunPolicyStoreTests runs a suite of tests against a given PolicyStore implementation.
func RunPolicyStoreTests(t *testing.T, store engine.PolicyStore) {
	ctx := context.Background()
	graphID := "graph-test-123"
	policyID := "policy-test-abc"

	t.Run("SavePolicy increments version and sets CreatedAt", func(t *testing.T) {
		record1 := &engine.PolicyRecord{
			ID:       policyID,
			GraphID:  graphID,
			Document: []byte("rules: []"),
			Format:   "yaml",
		}

		saved1, err := store.SavePolicy(ctx, record1)
		if err != nil {
			t.Fatalf("failed to save policy 1: %v", err)
		}
		if saved1.Version != "1" {
			t.Errorf("expected version 1, got %q", saved1.Version)
		}
		if saved1.CreatedAt.IsZero() {
			t.Error("expected CreatedAt to be set, got zero time")
		}

		record2 := &engine.PolicyRecord{
			ID:       policyID,
			GraphID:  graphID,
			Document: []byte("rules: [allow]"),
			Format:   "yaml",
		}

		saved2, err := store.SavePolicy(ctx, record2)
		if err != nil {
			t.Fatalf("failed to save policy 2: %v", err)
		}
		if saved2.Version != "2" {
			t.Errorf("expected version 2, got %q", saved2.Version)
		}
		if saved2.CreatedAt.Before(saved1.CreatedAt) {
			t.Error("expected CreatedAt to be monotonic")
		}
	})

	t.Run("GetPolicy retrieves latest version when version is empty", func(t *testing.T) {
		policy, err := store.GetPolicy(ctx, graphID, policyID, "")
		if err != nil {
			t.Fatalf("failed to get latest policy: %v", err)
		}
		if policy.Version != "2" {
			t.Errorf("expected latest version 2, got %q", policy.Version)
		}
	})

	t.Run("GetPolicy retrieves specific version", func(t *testing.T) {
		policy, err := store.GetPolicy(ctx, graphID, policyID, "1")
		if err != nil {
			t.Fatalf("failed to get policy version 1: %v", err)
		}
		if policy.Version != "1" {
			t.Errorf("expected version 1, got %q", policy.Version)
		}
	})

	t.Run("GetPolicy returns ErrPolicyNotFound for missing policy", func(t *testing.T) {
		_, err := store.GetPolicy(ctx, "missing-graph", "missing-policy", "")
		if !errors.Is(err, engine.ErrPolicyNotFound) {
			t.Errorf("expected ErrPolicyNotFound, got %v", err)
		}

		_, err = store.GetPolicy(ctx, graphID, policyID, "999")
		if !errors.Is(err, engine.ErrPolicyNotFound) {
			t.Errorf("expected ErrPolicyNotFound for missing version, got %v", err)
		}
	})

	t.Run("ListPolicies retrieves all versions for a graph", func(t *testing.T) {
		policies, err := store.ListPolicies(ctx, graphID)
		if err != nil {
			t.Fatalf("failed to list policies: %v", err)
		}
		if len(policies) != 2 {
			t.Errorf("expected 2 policies, got %d", len(policies))
		}

		// Sort check could be added, but usually order isn't strictly defined by the interface unless specified
		var versions []string
		for _, p := range policies {
			versions = append(versions, p.Version)
		}
		if versions[0] != "1" && versions[0] != "2" || versions[1] != "1" && versions[1] != "2" || versions[0] == versions[1] {
			t.Errorf("expected versions 1 and 2, got %v", versions)
		}
	})

	t.Run("GetDeployment returns ErrDeploymentNotFound for missing deployment", func(t *testing.T) {
		_, err := store.GetDeployment(ctx, "missing-graph")
		if !errors.Is(err, engine.ErrDeploymentNotFound) {
			t.Errorf("expected ErrDeploymentNotFound, got %v", err)
		}
	})

	t.Run("SetDeployment and GetDeployment handle active deployment configuration", func(t *testing.T) {
		deployment := &engine.PolicyDeployment{
			GraphID:       graphID,
			PolicyID:      policyID,
			PolicyVersion: "1",
			Mode:          engine.DeploymentShadow,
		}

		err := store.SetDeployment(ctx, deployment)
		if err != nil {
			t.Fatalf("failed to set deployment: %v", err)
		}

		fetched, err := store.GetDeployment(ctx, graphID)
		if err != nil {
			t.Fatalf("failed to get deployment: %v", err)
		}
		if fetched.PolicyVersion != "1" {
			t.Errorf("expected version 1, got %q", fetched.PolicyVersion)
		}
		if fetched.Mode != engine.DeploymentShadow {
			t.Errorf("expected mode shadow, got %q", fetched.Mode)
		}
		if fetched.UpdatedAt.IsZero() {
			t.Error("expected UpdatedAt to be set, got zero time")
		}

		// Update to new deployment
		time.Sleep(10 * time.Millisecond) // Ensure UpdatedAt changes

		update := &engine.PolicyDeployment{
			GraphID:       graphID,
			PolicyID:      policyID,
			PolicyVersion: "2",
			Mode:          engine.DeploymentLive,
		}
		err = store.SetDeployment(ctx, update)
		if err != nil {
			t.Fatalf("failed to update deployment: %v", err)
		}

		fetched2, err := store.GetDeployment(ctx, graphID)
		if err != nil {
			t.Fatalf("failed to get updated deployment: %v", err)
		}
		if fetched2.PolicyVersion != "2" {
			t.Errorf("expected version 2, got %q", fetched2.PolicyVersion)
		}
		if fetched2.Mode != engine.DeploymentLive {
			t.Errorf("expected mode live, got %q", fetched2.Mode)
		}
		if !fetched2.UpdatedAt.After(fetched.UpdatedAt) {
			t.Error("expected UpdatedAt to advance")
		}
	})
}
