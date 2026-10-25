package engine

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrPolicyNotFound is returned when a policy record is not found in the store.
	ErrPolicyNotFound = errors.New("store: policy not found")

	// ErrDeploymentNotFound is returned when a policy deployment is not found in the store.
	ErrDeploymentNotFound = errors.New("store: deployment not found")
)

// DeploymentMode indicates how a deployed policy should be executed.
type DeploymentMode string

const (
	// DeploymentShadow means the policy is executed for observation, but its results do not affect the main flow.
	DeploymentShadow DeploymentMode = "shadow"

	// DeploymentLive means the policy is actively executed and affects the main flow.
	DeploymentLive DeploymentMode = "live"
)

// PolicyRecord represents a single version of a policy for a graph.
type PolicyRecord struct {
	ID        string
	GraphID   string
	Version   string
	Document  []byte
	Format    string
	CreatedAt time.Time
}

// PolicyDeployment represents the active deployment configuration for a graph's policy.
type PolicyDeployment struct {
	GraphID       string
	PolicyID      string
	PolicyVersion string
	Mode          DeploymentMode
	UpdatedAt     time.Time
}

// PolicyStore defines the contract for persisting and retrieving policies and their deployments.
type PolicyStore interface {
	// SavePolicy saves a new policy record. It assigns a monotonic Version and CreatedAt.
	SavePolicy(ctx context.Context, record *PolicyRecord) (*PolicyRecord, error)

	// GetPolicy retrieves a policy record. If version is empty, it returns the latest version.
	GetPolicy(ctx context.Context, graphID string, policyID string, version string) (*PolicyRecord, error)

	// ListPolicies lists all policy records for a given graph.
	ListPolicies(ctx context.Context, graphID string) ([]*PolicyRecord, error)

	// SetDeployment sets the active deployment configuration for a graph.
	SetDeployment(ctx context.Context, deployment *PolicyDeployment) error

	// GetDeployment retrieves the active deployment configuration for a graph.
	GetDeployment(ctx context.Context, graphID string) (*PolicyDeployment, error)
}
