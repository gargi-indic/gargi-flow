package nodes

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
)

// HTTPDoer abstracts making HTTP requests, allowing fakes for testing.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// SignalSource abstracts waiting for an external signal.
type SignalSource interface {
	Wait(ctx context.Context, execution engine.ExecutionID, node engine.NodeID, signal string) (map[string]any, error)
}

// Dependencies provides the external dependencies for the built-in reference nodes.
type Dependencies struct {
	// HTTPClient is used by the http_call node. If nil, each node instance will
	// build its own &http.Client{}.
	HTTPClient HTTPDoer

	// Signals is used by the wait_for_signal node to wait for external signals.
	Signals SignalSource

	// PolicyStore is used by the decision node to evaluate policies.
	PolicyStore engine.PolicyStore
}

// RegisterDefaults registers the default reference node implementations into the provided registry.
// Supported nodes: passthrough, transform, conditional_branch, http_call, wait_for_signal, decision (when PolicyStore is provided).
// Returns an error if the registry is nil or if any registration fails.
func RegisterDefaults(reg *engine.Registry, deps Dependencies) error {
	if reg == nil {
		return errors.New("registry cannot be nil")
	}

	if deps.PolicyStore != nil {
		chooser := NewPolicyDecisionChooser(deps.PolicyStore)
		if err := RegisterDecision(reg, chooser); err != nil {
			return fmt.Errorf("failed to register decision: %w", err)
		}
	}

	if err := reg.Register("passthrough", func(_ any) (engine.NodeInterface, error) {
		return &passthroughNode{
			BaseNode: &engine.BaseNode{},
		}, nil
	}, passthroughConfig{}); err != nil {
		return fmt.Errorf("failed to register passthrough: %w", err)
	}

	if err := reg.Register("transform", func(config any) (engine.NodeInterface, error) {
		return &transformNode{
			BaseNode: &engine.BaseNode{},
			cfg:      *config.(*transformConfig),
		}, nil
	}, &transformConfig{}); err != nil {
		return fmt.Errorf("failed to register transform: %w", err)
	}

	if err := reg.Register("conditional_branch", func(config any) (engine.NodeInterface, error) {
		return &branchNode{
			BaseNode: &engine.BaseNode{},
			cfg:      *config.(*branchConfig),
		}, nil
	}, &branchConfig{}); err != nil {
		return fmt.Errorf("failed to register conditional_branch: %w", err)
	}

	if err := reg.Register("http_call", func(config any) (engine.NodeInterface, error) {
		client := deps.HTTPClient
		if client == nil {
			client = &http.Client{}
		}
		return &httpCallNode{
			BaseNode: &engine.BaseNode{},
			cfg:      *config.(*httpCallConfig),
			client:   client,
		}, nil
	}, &httpCallConfig{}); err != nil {
		return fmt.Errorf("failed to register http_call: %w", err)
	}

	if err := reg.Register("wait_for_signal", func(config any) (engine.NodeInterface, error) {
		if deps.Signals == nil {
			return nil, engine.NewNonRetryableError(errors.New("signal source not configured"))
		}
		return &waitSignalNode{
			BaseNode: &engine.BaseNode{},
			cfg:      *config.(*waitSignalConfig),
			signals:  deps.Signals,
		}, nil
	}, &waitSignalConfig{}); err != nil {
		return fmt.Errorf("failed to register wait_for_signal: %w", err)
	}

	return nil
}
