package api

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/config"
	"github.com/gargi-indic/gargi-flow/internal/graph"
	"github.com/gargi-indic/gargi-flow/internal/metrics"
	"github.com/gargi-indic/gargi-flow/internal/nodes"
	"github.com/gargi-indic/gargi-flow/internal/orchestrator"
	gargiv1 "github.com/gargi-indic/gargi-flow/pkg/api/v1"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Server implements gargiv1.GargiServiceServer.
type Server struct {
	gargiv1.UnimplementedGargiServiceServer
	temporalClient client.Client
	temporalConfig config.TemporalConfig
	store          engine.StateStore
	logger         *slog.Logger
	parser         *graph.Parser
	bridge         *FanoutBridge
	registry       *engine.Registry
}

// NewServer creates a new GargiServiceServer instance.
func NewServer(
	tc client.Client,
	tCfg config.TemporalConfig,
	st engine.StateStore,
	logger *slog.Logger,
) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	var policyStore engine.PolicyStore
	if ps, ok := st.(engine.PolicyStore); ok {
		policyStore = ps
	}
	reg := engine.NewRegistry()
	_ = nodes.RegisterDefaults(reg, nodes.Dependencies{
		PolicyStore: policyStore,
	})

	return &Server{
		temporalClient: tc,
		temporalConfig: tCfg,
		store:          st,
		logger:         logger,
		parser:         graph.NewParser(),
		bridge:         NewFanoutBridge(st, logger),
		registry:       reg,
	}
}

// HealthStatus represents the health check report for the server dependencies.
type HealthStatus struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
	Errors map[string]string `json:"errors,omitempty"`
}

// CheckLiveness performs a basic liveness check ensuring the process is running.
func (s *Server) CheckLiveness(_ context.Context) HealthStatus {
	return HealthStatus{
		Status: "SERVING",
		Checks: map[string]string{
			"server": "UP",
		},
	}
}

// CheckReadiness checks connectivity to downstream dependencies (Temporal and StateStore).
func (s *Server) CheckReadiness(ctx context.Context) (HealthStatus, bool) {
	checks := make(map[string]string)
	errs := make(map[string]string)
	ready := true

	// Check StateStore connectivity
	if s.store != nil {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := s.store.GetState(probeCtx, "healthcheck-probe")
		cancel()
		if err != nil && !engine.IsStoreNotFound(err) && !errors.Is(err, engine.ErrStoreNotFound) {
			checks["store"] = "DOWN"
			errs["store"] = err.Error()
			ready = false
		} else {
			checks["store"] = "UP"
		}
	} else {
		checks["store"] = "UP"
	}

	// Check Temporal connectivity
	if s.temporalClient != nil {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := s.temporalClient.CheckHealth(probeCtx, &client.CheckHealthRequest{})
		cancel()
		if err != nil {
			checks["temporal"] = "DOWN"
			errs["temporal"] = err.Error()
			ready = false
		} else {
			checks["temporal"] = "UP"
		}
	} else {
		// Degraded mode without Temporal client
		checks["temporal"] = "DEGRADED"
	}

	overallStatus := "READY"
	if !ready {
		overallStatus = "NOT_READY"
	}

	res := HealthStatus{
		Status: overallStatus,
		Checks: checks,
	}
	if len(errs) > 0 {
		res.Errors = errs
	}

	return res, ready
}

// SubmitExecution submits a new graph definition for asynchronous execution.
func (s *Server) SubmitExecution(ctx context.Context, req *gargiv1.SubmitExecutionRequest) (*gargiv1.SubmitExecutionResponse, error) {
	if req == nil || req.GetGraphDefinition() == "" {
		return nil, status.Error(codes.InvalidArgument, "graph_definition is required")
	}

	rawDef := []byte(req.GetGraphDefinition())
	def, err := s.parser.Parse(rawDef, engine.FormatJSON)
	if err != nil {
		def, err = s.parser.Parse(rawDef, engine.FormatYAML)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "failed to parse graph definition: %v", err)
		}
	}

	// 1. Structural schema validation
	valErrs := graph.ValidateStructure(def)
	if valErrs.Len() > 0 {
		return nil, status.Errorf(codes.InvalidArgument, "invalid graph structure: %s", valErrs.Error())
	}

	// 2. Semantic node validation against registry
	if s.registry != nil {
		semErrs := graph.ValidateSemantics(def, s.registry)
		if semErrs.Len() > 0 {
			return nil, status.Errorf(codes.InvalidArgument, "invalid graph semantics: %s", semErrs.Error())
		}
	}

	// 3. Topology & cycle validation
	g, err := def.ToGraph()
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid graph topology: %v", err)
	}

	// Idempotency key mapping to execution ID / Temporal workflow ID
	execID := req.GetExecutionId()
	if execID == "" {
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if vals := md.Get("idempotency-key"); len(vals) > 0 && vals[0] != "" {
				execID = vals[0]
			} else if vals := md.Get("x-idempotency-key"); len(vals) > 0 && vals[0] != "" {
				execID = vals[0]
			}
		}
	}
	if execID == "" {
		execID = generateUUID()
	}

	// Check if already executed / running in store for idempotent return
	if s.store != nil {
		if existingSt, err := s.store.GetState(ctx, engine.ExecutionID(execID)); err == nil && existingSt != nil {
			s.logger.Info("idempotent submission: execution already exists in store", "execution_id", execID)
			if !req.GetWait() {
				return &gargiv1.SubmitExecutionResponse{
					ExecutionId: execID,
					Status:      toPBExecutionStatus(existingSt.Status()),
					StartedAt:   timestamppb.Now(),
				}, nil
			}
			if isTerminalExecutionStatus(existingSt.Status()) {
				return toSubmitExecutionResponse(execID, existingSt, nil), nil
			}
		}
	}

	var inputs map[string]any
	if req.GetInputs() != nil {
		inputs = req.GetInputs().AsMap()
	}

	var pinnedPolicies map[string]string
	if policyStore, ok := s.store.(engine.PolicyStore); ok {
		if dep, err := policyStore.GetDeployment(ctx, def.ID); err == nil && dep != nil {
			if dep.Mode == engine.DeploymentLive {
				pinnedPolicies = map[string]string{
					dep.PolicyID: dep.PolicyVersion,
				}
			}
		}
	}

	wfInput := orchestrator.GraphWorkflowInput{
		ExecutionID:    engine.ExecutionID(execID),
		Definition:     def,
		InitialState:   inputs,
		PinnedPolicies: pinnedPolicies,
	}

	wfOptions := client.StartWorkflowOptions{
		ID:        execID,
		TaskQueue: s.temporalConfig.EffectiveWorkflowTaskQueue(),
	}

	if s.store != nil {
		if initialState, err := engine.NewGraphState(engine.ExecutionID(execID), g); err == nil {
			_ = s.store.SetState(ctx, engine.ExecutionID(execID), initialState, 0)
		}
	}

	var run client.WorkflowRun
	if s.temporalClient != nil {
		var err error
		run, err = s.temporalClient.ExecuteWorkflow(ctx, wfOptions, orchestrator.GraphExecutionWorkflowName, wfInput)
		if err != nil {
			var executionAlreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
			if errors.As(err, &executionAlreadyStarted) {
				s.logger.Info("idempotent submission: workflow already started in temporal", "execution_id", execID)
				if !req.GetWait() {
					return &gargiv1.SubmitExecutionResponse{
						ExecutionId: execID,
						Status:      gargiv1.ExecutionStatus_EXECUTION_STATUS_RUNNING,
						StartedAt:   timestamppb.Now(),
					}, nil
				}
				run = s.temporalClient.GetWorkflow(ctx, execID, "")
			} else {
				return nil, ToGRPCError(err)
			}
		}
	}

	if !req.GetWait() {
		return &gargiv1.SubmitExecutionResponse{
			ExecutionId: execID,
			Status:      gargiv1.ExecutionStatus_EXECUTION_STATUS_PENDING,
			StartedAt:   timestamppb.Now(),
		}, nil
	}

	// Synchronous submit-and-wait mode
	waitCtx := ctx
	if req.GetTimeout() != nil && req.GetTimeout().AsDuration() > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, req.GetTimeout().AsDuration())
		defer cancel()
	}

	if run != nil {
		var wfResult orchestrator.GraphWorkflowResult
		waitErr := run.Get(waitCtx, &wfResult)
		if waitErr != nil {
			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) || errors.Is(waitErr, context.DeadlineExceeded) {
				return nil, status.Error(codes.DeadlineExceeded, "timed out waiting for execution completion")
			}
			if errors.Is(waitCtx.Err(), context.Canceled) || errors.Is(waitErr, context.Canceled) {
				return nil, status.Error(codes.Canceled, "context canceled while waiting for execution completion")
			}
			st, _ := orchestrator.FetchStateWithFallback(ctx, s.store, s.temporalClient, execID, "")
			return toSubmitExecutionResponse(execID, st, waitErr), nil
		}

		st := wfResult.FinalState
		if st == nil {
			st, _ = orchestrator.FetchStateWithFallback(ctx, s.store, s.temporalClient, execID, "")
		}
		if st != nil && s.store != nil {
			_ = s.store.SetState(ctx, engine.ExecutionID(execID), st, 0)
		}
		return toSubmitExecutionResponse(execID, st, nil), nil
	}

	// Without a live Temporal client (e.g. testing with memory store), poll store until terminal or timeout
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.store != nil {
			st, err := s.store.GetState(waitCtx, engine.ExecutionID(execID))
			if err == nil && st != nil && isTerminalExecutionStatus(st.Status()) {
				return toSubmitExecutionResponse(execID, st, nil), nil
			}
		}
		select {
		case <-waitCtx.Done():
			return nil, status.Error(codes.DeadlineExceeded, "timed out waiting for execution completion")
		case <-ticker.C:
		}
	}
}

// GetExecution queries the state of a graph execution.
func (s *Server) GetExecution(ctx context.Context, req *gargiv1.GetExecutionRequest) (*gargiv1.GetExecutionResponse, error) {
	if req == nil || req.GetExecutionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "execution_id is required")
	}

	st, err := orchestrator.FetchStateWithFallback(ctx, s.store, s.temporalClient, req.GetExecutionId(), "")
	if err != nil {
		return nil, ToGRPCError(err)
	}

	return toGetExecutionResponse(req.GetExecutionId(), st), nil
}

// GetNodeState queries the execution state of an individual node in the graph.
func (s *Server) GetNodeState(ctx context.Context, req *gargiv1.GetNodeStateRequest) (*gargiv1.GetNodeStateResponse, error) {
	if req == nil || req.GetExecutionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "execution_id is required")
	}
	if req.GetNodeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id is required")
	}

	st, err := orchestrator.FetchStateWithFallback(ctx, s.store, s.temporalClient, req.GetExecutionId(), "")
	if err != nil {
		return nil, ToGRPCError(err)
	}

	ns := st.NodeState(engine.NodeID(req.GetNodeId()))
	if ns == nil {
		return nil, status.Errorf(codes.NotFound, "node %q not found in execution %q", req.GetNodeId(), req.GetExecutionId())
	}

	return &gargiv1.GetNodeStateResponse{
		ExecutionId: req.GetExecutionId(),
		NodeState:   toNodeStateDetail(ns),
	}, nil
}

// CancelExecution cancels an in-flight graph execution.
func (s *Server) CancelExecution(ctx context.Context, req *gargiv1.CancelExecutionRequest) (*gargiv1.CancelExecutionResponse, error) {
	if req == nil || req.GetExecutionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "execution_id is required")
	}

	if s.temporalClient != nil {
		if err := s.temporalClient.CancelWorkflow(ctx, req.GetExecutionId(), ""); err != nil {
			return nil, ToGRPCError(err)
		}
	}

	return &gargiv1.CancelExecutionResponse{
		ExecutionId: req.GetExecutionId(),
		Status:      gargiv1.ExecutionStatus_EXECUTION_STATUS_CANCELLED,
		CancelledAt: timestamppb.Now(),
	}, nil
}

// SignalNode delivers an external event or signal to a node.
func (s *Server) SignalNode(ctx context.Context, req *gargiv1.SignalNodeRequest) (*gargiv1.SignalNodeResponse, error) {
	if req == nil || req.GetExecutionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "execution_id is required")
	}
	if req.GetNodeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id is required")
	}
	if req.GetSignalName() == "" {
		return nil, status.Error(codes.InvalidArgument, "signal_name is required")
	}

	var payloadMap map[string]any
	if req.GetPayload() != nil {
		payloadMap = map[string]any{"value": req.GetPayload().AsInterface()}
	}

	sig := orchestrator.ExternalSignalPayload{
		TargetNodeID: engine.NodeID(req.GetNodeId()),
		SignalName:   req.GetSignalName(),
		Payload:      payloadMap,
	}

	if s.temporalClient != nil {
		if err := orchestrator.SendNodeSignal(ctx, s.temporalClient, req.GetExecutionId(), "", sig); err != nil {
			return nil, ToGRPCError(err)
		}
	}

	return &gargiv1.SignalNodeResponse{
		Accepted: true,
		Message:  "signal accepted for delivery",
	}, nil
}

// StreamExecutionUpdates streams execution updates to subscribers using the fan-out bridge.
func (s *Server) StreamExecutionUpdates(
	req *gargiv1.StreamExecutionUpdatesRequest,
	stream gargiv1.GargiService_StreamExecutionUpdatesServer,
) error {
	if req == nil || req.GetExecutionId() == "" {
		return status.Error(codes.InvalidArgument, "execution_id is required")
	}

	metrics.RecordStreamConnected("grpc")
	defer metrics.RecordStreamDisconnected("grpc")

	ctx := stream.Context()
	return s.bridge.Stream(ctx, req, stream)
}

// Helpers for model conversion.

func toSubmitExecutionResponse(execID string, st *engine.GraphState, err error) *gargiv1.SubmitExecutionResponse {
	resp := &gargiv1.SubmitExecutionResponse{
		ExecutionId: execID,
		Status:      gargiv1.ExecutionStatus_EXECUTION_STATUS_SUCCEEDED,
		StartedAt:   timestamppb.Now(),
		CompletedAt: timestamppb.Now(),
		NodeStates:  make(map[string]*gargiv1.NodeStateDetail),
	}

	if st != nil {
		resp.Status = toPBExecutionStatus(st.Status())
		for _, ns := range st.NodeStates() {
			resp.NodeStates[string(ns.NodeID())] = toNodeStateDetail(ns)
		}
	}

	if err != nil {
		resp.Status = gargiv1.ExecutionStatus_EXECUTION_STATUS_FAILED
		resp.ErrorMessage = err.Error()
	}

	return resp
}

func toGetExecutionResponse(execID string, st *engine.GraphState) *gargiv1.GetExecutionResponse {
	resp := &gargiv1.GetExecutionResponse{
		ExecutionId: execID,
		Status:      toPBExecutionStatus(st.Status()),
		Version:     st.Version(),
		NodeStates:  make(map[string]*gargiv1.NodeStateDetail),
		CreatedAt:   timestamppb.Now(),
		UpdatedAt:   timestamppb.Now(),
	}

	for _, ns := range st.NodeStates() {
		resp.NodeStates[string(ns.NodeID())] = toNodeStateDetail(ns)
	}

	return resp
}

func toNodeStateDetail(ns *engine.NodeState) *gargiv1.NodeStateDetail {
	if ns == nil {
		return nil
	}

	var outputVal *structpb.Value
	if outMap := ns.Output(); len(outMap) > 0 {
		outputVal, _ = structpb.NewValue(outMap)
	}

	return &gargiv1.NodeStateDetail{
		NodeId:       string(ns.NodeID()),
		Status:       toPBNodeStatus(ns.Status()),
		Output:       outputVal,
		ErrorMessage: ns.ErrorMessage(),
		Attempt:      uint32(ns.Version()),
		StartedAt:    timestamppb.Now(),
		CompletedAt:  timestamppb.Now(),
	}
}

func toPBExecutionStatus(s engine.Status) gargiv1.ExecutionStatus {
	switch s {
	case engine.StatusPending:
		return gargiv1.ExecutionStatus_EXECUTION_STATUS_PENDING
	case engine.StatusReady, engine.StatusRunning:
		return gargiv1.ExecutionStatus_EXECUTION_STATUS_RUNNING
	case engine.StatusSucceeded:
		return gargiv1.ExecutionStatus_EXECUTION_STATUS_SUCCEEDED
	case engine.StatusFailed:
		return gargiv1.ExecutionStatus_EXECUTION_STATUS_FAILED
	default:
		return gargiv1.ExecutionStatus_EXECUTION_STATUS_UNSPECIFIED
	}
}

func toPBNodeStatus(s engine.Status) gargiv1.NodeStatus {
	switch s {
	case engine.StatusPending:
		return gargiv1.NodeStatus_NODE_STATUS_PENDING
	case engine.StatusReady:
		return gargiv1.NodeStatus_NODE_STATUS_READY
	case engine.StatusRunning:
		return gargiv1.NodeStatus_NODE_STATUS_RUNNING
	case engine.StatusSucceeded:
		return gargiv1.NodeStatus_NODE_STATUS_SUCCEEDED
	case engine.StatusFailed:
		return gargiv1.NodeStatus_NODE_STATUS_FAILED
	case engine.StatusSkipped:
		return gargiv1.NodeStatus_NODE_STATUS_SKIPPED
	case engine.StatusCompensating:
		return gargiv1.NodeStatus_NODE_STATUS_COMPENSATING
	case engine.StatusCompensated:
		return gargiv1.NodeStatus_NODE_STATUS_COMPENSATED
	default:
		return gargiv1.NodeStatus_NODE_STATUS_UNSPECIFIED
	}
}

func generateUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func isTerminalExecutionStatus(s engine.Status) bool {
	switch s {
	case engine.StatusSucceeded, engine.StatusFailed, engine.StatusSkipped, engine.StatusCompensated:
		return true
	default:
		return false
	}
}

// ListExecutions retrieves a list of execution summaries matching the given filter.
func (s *Server) ListExecutions(ctx context.Context, req *gargiv1.ListExecutionsRequest) (*gargiv1.ListExecutionsResponse, error) {
	filter := engine.ExecutionFilter{
		GraphID: req.GetGraphId(),
		Limit:   int(req.GetLimit()),
		Offset:  int(req.GetOffset()),
	}
	if req.GetStatus() != gargiv1.ExecutionStatus_EXECUTION_STATUS_UNSPECIFIED {
		filter.Status = engine.Status(req.GetStatus().String())
	}

	if req.GetStartTime() != nil {
		t := req.GetStartTime().AsTime()
		filter.StartTime = &t
	}
	if req.GetEndTime() != nil {
		t := req.GetEndTime().AsTime()
		filter.EndTime = &t
	}

	summaries, err := s.store.ListExecutions(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list executions: %v", err)
	}

	var executions []*gargiv1.ExecutionSummary
	for _, summary := range summaries {
		executions = append(executions, &gargiv1.ExecutionSummary{
			ExecutionId: string(summary.ExecutionID),
			GraphId:     summary.GraphID,
			Status:      toPBExecutionStatus(summary.Status),
			Version:     summary.Version,
			CreatedAt:   timestamppb.New(summary.CreatedAt),
			UpdatedAt:   timestamppb.New(summary.UpdatedAt),
		})
	}

	return &gargiv1.ListExecutionsResponse{
		Executions: executions,
	}, nil
}
