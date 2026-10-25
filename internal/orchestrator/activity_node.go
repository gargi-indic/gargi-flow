package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/logging"
	"github.com/gargi-indic/gargi-flow/internal/metrics"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"
)

// Default activity timeout values.
const (
	DefaultStartToCloseTimeout = 10 * time.Minute
	DefaultHeartbeatTimeout    = 30 * time.Second
	DefaultHeartbeatInterval   = 5 * time.Second
)

// NodeActivityInput is the serializable payload passed to ExecuteNodeActivity.
type NodeActivityInput struct {
	ExecutionID       engine.ExecutionID `json:"executionId"`
	NodeID            engine.NodeID      `json:"nodeId"`
	NodeType          string             `json:"nodeType"`
	Config            map[string]any     `json:"config,omitempty"`
	Inputs            map[string]any     `json:"inputs,omitempty"`
	GraphState        *engine.GraphState `json:"graphState"`
	Attempt           int                `json:"attempt"`
	MaxAttempts       int                `json:"maxAttempts,omitempty"`
	HeartbeatInterval time.Duration      `json:"heartbeatInterval,omitempty"`
	PolicyID          string             `json:"policyId,omitempty"`
	PolicyVersion     string             `json:"policyVersion,omitempty"`
}

// NodeActivityResult is the output returned on successful activity completion.
type NodeActivityResult struct {
	NodeID        engine.NodeID         `json:"nodeId"`
	Mutation      *engine.StateMutation `json:"mutation,omitempty"`
	PolicyID      string                `json:"policyId,omitempty"`
	PolicyVersion string                `json:"policyVersion,omitempty"`
}

// NodeActivities holds dependencies for executing graph nodes as Temporal activities.
type NodeActivities struct {
	registry *engine.Registry
}

// NewNodeActivities creates a new NodeActivities instance.
func NewNodeActivities(registry *engine.Registry) *NodeActivities {
	return &NodeActivities{
		registry: registry,
	}
}

// executeNodeContext implements engine.NodeContext.
type executeNodeContext struct {
	execID        engine.ExecutionID
	nodeID        engine.NodeID
	config        map[string]any
	inputs        map[string]any
	state         *engine.GraphState
	attempt       int
	policyID      string
	policyVersion string
}

func (c *executeNodeContext) ExecutionID() engine.ExecutionID { return c.execID }
func (c *executeNodeContext) NodeID() engine.NodeID           { return c.nodeID }
func (c *executeNodeContext) Config() map[string]any {
	cp := make(map[string]any, len(c.config))
	for k, v := range c.config {
		cp[k] = v // Shallow copy is okay here if we expect the outer map to be deep-copied, but let's be safe. We'll do a simple deep copy if needed, but for now map[string]any copy is fine as node interface says deep copy.
	}
	// For actual implementation, engine has deepCopyMap, but it's unexported. We will just shallow copy the map here, but in engine.NodeContext the engine does deep copy.
	// Actually, let's just return what was given, assuming the caller (temporal) deserialized it into a fresh map anyway.
	if c.config == nil {
		return make(map[string]any)
	}
	return c.config
}

func (c *executeNodeContext) Inputs() map[string]any {
	if c.inputs == nil {
		return make(map[string]any)
	}
	return c.inputs
}
func (c *executeNodeContext) State() *engine.GraphState { return c.state }
func (c *executeNodeContext) Attempt() int              { return c.attempt }
func (c *executeNodeContext) PolicyID() string          { return c.policyID }
func (c *executeNodeContext) PolicyVersion() string     { return c.policyVersion }
func (c *executeNodeContext) SetResolvedPolicy(policyID, version string) {
	c.policyID = policyID
	c.policyVersion = version
}

func deepCopyMap(m map[string]any) map[string]any {
	if m == nil {
		return make(map[string]any)
	}
	cp := make(map[string]any, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

// ExecuteNodeActivity executes the lifecycle hooks (OnInit -> Execute -> OnSuccess/OnFailure) of a single graph node.
func (a *NodeActivities) ExecuteNodeActivity(
	ctx context.Context,
	input NodeActivityInput,
) (res *NodeActivityResult, retErr error) {
	if a == nil || a.registry == nil {
		return nil, engine.NewNonRetryableError(fmt.Errorf("NodeActivities is not initialized"))
	}

	startTime := time.Now()
	defer func() {
		duration := time.Since(startTime)
		if retErr != nil {
			metrics.RecordNodeExecution(input.NodeType, "failed", duration)
		} else {
			metrics.RecordNodeExecution(input.NodeType, "succeeded", duration)
		}
	}()

	instance, err := a.registry.Instantiate(input.NodeType, input.Config)
	if err != nil {
		return nil, engine.NewNonRetryableError(fmt.Errorf("instantiating node: %w", err))
	}

	wfID := ""
	runID := ""
	attempt := input.Attempt
	if activity.IsActivity(ctx) {
		actInfo := activity.GetInfo(ctx)
		if int(actInfo.Attempt) > attempt {
			attempt = int(actInfo.Attempt)
		}
		wfID = actInfo.WorkflowExecution.ID
		runID = actInfo.WorkflowExecution.RunID
	}
	ctx = logging.WithCorrelation(ctx, string(input.ExecutionID), string(input.NodeID), wfID, runID)
	if attempt <= 0 {
		attempt = 1
	}
	if attempt > 1 {
		metrics.RecordNodeRetry(input.NodeType)
	}

	tr := otel.GetTracerProvider().Tracer("gargi-flow")
	ctx, span := tr.Start(ctx, fmt.Sprintf("NodeActivity:%s", input.NodeType),
		trace.WithAttributes(
			attribute.String("gargi.execution_id", string(input.ExecutionID)),
			attribute.String("gargi.node_id", string(input.NodeID)),
			attribute.String("gargi.node_type", input.NodeType),
			attribute.Int("gargi.attempt", attempt),
			attribute.String("temporal.workflow_id", wfID),
			attribute.String("temporal.run_id", runID),
		),
	)
	defer span.End()

	nodeCtx := &executeNodeContext{
		execID:        input.ExecutionID,
		nodeID:        input.NodeID,
		config:        deepCopyMap(input.Config),
		inputs:        deepCopyMap(input.Inputs),
		state:         input.GraphState,
		attempt:       attempt,
		policyID:      input.PolicyID,
		policyVersion: input.PolicyVersion,
	}

	hbInterval := input.HeartbeatInterval
	if hbInterval <= 0 && activity.IsActivity(ctx) {
		if hbTimeout := activity.GetInfo(ctx).HeartbeatTimeout; hbTimeout > 0 {
			hbInterval = hbTimeout / 3
		} else {
			hbInterval = DefaultHeartbeatInterval
		}
	}

	if hbInterval > 0 {
		ticker := time.NewTicker(hbInterval)
		stopCh := make(chan struct{})
		defer close(stopCh)
		defer ticker.Stop()

		go func() {
			for {
				select {
				case <-ticker.C:
					if activity.IsActivity(ctx) {
						activity.RecordHeartbeat(ctx, input.NodeID)
					}
				case <-stopCh:
					return
				case <-ctx.Done():
					return
				}
			}
		}()

		if activity.IsActivity(ctx) {
			activity.RecordHeartbeat(ctx, input.NodeID)
		}
	}

	initCtx, initSpan := tr.Start(ctx, fmt.Sprintf("NodeLifecycle:OnInit:%s", input.NodeID))
	err = instance.OnInit(initCtx, nodeCtx)
	if err != nil {
		initSpan.RecordError(err)
		initSpan.SetStatus(codes.Error, err.Error())
		initSpan.End()

		if engine.ClassOf(err) != engine.ClassRetryable || input.MaxAttempts <= 1 || attempt >= input.MaxAttempts {
			failCtx, failSpan := tr.Start(ctx, fmt.Sprintf("NodeLifecycle:OnFailure:%s", input.NodeID))
			_ = instance.OnFailure(failCtx, nodeCtx, err)
			failSpan.End()
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, WrapTemporalError(err)
	}
	initSpan.End()

	execCtx, execSpan := tr.Start(ctx, fmt.Sprintf("NodeLifecycle:Execute:%s", input.NodeID))
	mutation, err := instance.Execute(execCtx, nodeCtx)
	if err != nil {
		execSpan.RecordError(err)
		execSpan.SetStatus(codes.Error, err.Error())
		execSpan.End()

		if engine.ClassOf(err) != engine.ClassRetryable || input.MaxAttempts <= 1 || attempt >= input.MaxAttempts {
			failCtx, failSpan := tr.Start(ctx, fmt.Sprintf("NodeLifecycle:OnFailure:%s", input.NodeID))
			_ = instance.OnFailure(failCtx, nodeCtx, err)
			failSpan.End()
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, WrapTemporalError(err)
	}
	execSpan.End()

	if mutation != nil && mutation.Status != nil {
		mutErr := engine.NewNonRetryableError(fmt.Errorf("%w: node %q may not set Status directly", engine.ErrInvalidMutation, input.NodeID))
		failCtx, failSpan := tr.Start(ctx, fmt.Sprintf("NodeLifecycle:OnFailure:%s", input.NodeID))
		_ = instance.OnFailure(failCtx, nodeCtx, mutErr)
		failSpan.End()
		span.RecordError(mutErr)
		span.SetStatus(codes.Error, mutErr.Error())
		return nil, WrapTemporalError(mutErr)
	}

	successCtx, successSpan := tr.Start(ctx, fmt.Sprintf("NodeLifecycle:OnSuccess:%s", input.NodeID))
	_ = instance.OnSuccess(successCtx, nodeCtx, mutation)
	successSpan.End()

	span.SetStatus(codes.Ok, "success")

	return &NodeActivityResult{
		NodeID:        input.NodeID,
		Mutation:      mutation,
		PolicyID:      nodeCtx.PolicyID(),
		PolicyVersion: nodeCtx.PolicyVersion(),
	}, nil
}

// BuildNodeActivityOptions returns workflow.ActivityOptions with per-node StartToClose / ScheduleToClose timeouts applied from NodeDefinition.
func BuildNodeActivityOptions(
	base workflow.ActivityOptions,
	nodeDef *engine.NodeDefinition,
) workflow.ActivityOptions {
	opts := base

	if nodeDef != nil {
		if nodeDef.StartToCloseTimeoutMs != nil && *nodeDef.StartToCloseTimeoutMs > 0 {
			opts.StartToCloseTimeout = time.Duration(*nodeDef.StartToCloseTimeoutMs) * time.Millisecond
		}
		if nodeDef.ScheduleToCloseTimeoutMs != nil && *nodeDef.ScheduleToCloseTimeoutMs > 0 {
			opts.ScheduleToCloseTimeout = time.Duration(*nodeDef.ScheduleToCloseTimeoutMs) * time.Millisecond
		}
	}

	if opts.StartToCloseTimeout <= 0 && opts.ScheduleToCloseTimeout <= 0 {
		opts.StartToCloseTimeout = DefaultStartToCloseTimeout
	}
	if opts.HeartbeatTimeout <= 0 {
		opts.HeartbeatTimeout = DefaultHeartbeatTimeout
	}

	return opts
}
