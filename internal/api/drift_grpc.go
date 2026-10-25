package api

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/gargi-indic/gargi-flow/internal/sim/calibrate"
	"github.com/gargi-indic/gargi-flow/internal/sim/jobs"
	gargiv1 "github.com/gargi-indic/gargi-flow/pkg/api/v1"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

type stateStoreReader struct {
	stateStore engine.StateStore
}

func (r *stateStoreReader) ListExecutions(ctx context.Context, filter engine.ExecutionFilter) ([]*engine.ExecutionSummary, error) {
	return r.stateStore.ListExecutions(ctx, filter)
}

func (r *stateStoreReader) GetState(ctx context.Context, executionID engine.ExecutionID) (*engine.GraphState, error) {
	return r.stateStore.GetState(ctx, executionID)
}

func (r *stateStoreReader) GetLogs(ctx context.Context, executionID engine.ExecutionID, fromSequence uint64, limit int) ([]*engine.ExecutionLogEntry, error) {
	if el, ok := r.stateStore.(engine.ExecutionLog); ok {
		return el.GetLogs(ctx, executionID, fromSequence, limit)
	}
	return nil, nil
}

// GetDriftReport returns the drift report comparing predicted vs observed metrics per policy version.
func (s *SimulationServer) GetDriftReport(ctx context.Context, req *gargiv1.GetDriftReportRequest) (*gargiv1.GetDriftReportResponse, error) {
	if req.GetModelId() == "" {
		return nil, status.Error(codes.InvalidArgument, "model_id is required")
	}

	policyVersion := req.GetPolicyVersion()
	if policyVersion == "" {
		policyVersion = "1"
	}

	// Fetch latest model version
	bundle, err := s.store.GetModel(ctx, req.GetModelId(), "")
	if err != nil {
		return nil, simulationError("getting model", err)
	}

	mod, errs := jobs.ValidateModel(bundle, s.registry)
	if errs != nil && errs.Len() > 0 {
		var msgs []string
		for _, e := range errs.Issues() {
			msgs = append(msgs, e.Message)
		}
		return nil, status.Errorf(codes.FailedPrecondition, "invalid model: %s", strings.Join(msgs, ", "))
	}
	if mod == nil {
		return nil, status.Errorf(codes.Internal, "model validation failed but no issues returned")
	}

	// Set up reader
	var reader calibrate.ExecutionReader
	if rr, ok := s.store.(calibrate.ExecutionReader); ok {
		reader = rr
	} else if ss, ok := s.stateStore.(calibrate.ExecutionReader); ok {
		reader = ss
	} else if s.stateStore != nil {
		reader = &stateStoreReader{stateStore: s.stateStore}
	} else {
		reader = calibrate.NewEmptyReader()
	}

	// Fetch outcomes for this model
	outcomes, err := s.store.ListOutcomes(ctx, sim.OutcomeQueryOptions{
		GraphID:       req.GetModelId(),
		PolicyVersion: policyVersion,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "fetching outcomes: %v", err)
	}

	report, err := calibrate.CheckDrift(ctx, mod, reader, outcomes, policyVersion)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "checking drift: %v", err)
	}

	// Convert DriftReport to JSON struct
	b, err := json.Marshal(report)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshaling drift report: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, status.Errorf(codes.Internal, "unmarshaling drift report to map: %v", err)
	}

	v, err := structpb.NewValue(doc)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encoding drift report to structpb: %v", err)
	}

	return &gargiv1.GetDriftReportResponse{
		ModelId:       req.GetModelId(),
		PolicyVersion: policyVersion,
		Report:        v,
	}, nil
}
