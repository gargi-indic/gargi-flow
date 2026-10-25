package agent

import (
	"fmt"
	"sync"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
)

// Guardrail enforces rate and cost limits.
type Guardrail struct {
	mu          sync.Mutex
	maxRequests int
	maxCost     float64

	totalRequests int
	totalCost     float64
}

// NewGuardrail creates a new Guardrail.
func NewGuardrail(maxRequests int, maxCost float64) *Guardrail {
	return &Guardrail{
		maxRequests: maxRequests,
		maxCost:     maxCost,
	}
}

// AddRequest records a request and cost. If it exceeds limits, it returns a simulation failed error.
func (g *Guardrail) AddRequest(cost float64) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.maxRequests > 0 && g.totalRequests >= g.maxRequests {
		return engine.NewNonRetryableError(fmt.Errorf("agent max requests guardrail exceeded: %d >= %d", g.totalRequests+1, g.maxRequests))
	}

	if g.maxCost > 0 && g.totalCost+cost > g.maxCost {
		return engine.NewNonRetryableError(fmt.Errorf("agent max cost guardrail exceeded: %f > %f", g.totalCost+cost, g.maxCost))
	}

	g.totalRequests++
	g.totalCost += cost

	return nil
}
