package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/sim/experiment"
	"github.com/gargi-indic/gargi-flow/internal/sim/jobs"
	gargiv1 "github.com/gargi-indic/gargi-flow/pkg/api/v1"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ReportSummary is the GetReport kind of the full experiment report.
const ReportSummary = "summary"

// reportKinds lists the GetReport kinds besides ReportSummary, in the order
// of experiment.ReportSet (its JSON field names).
var reportKinds = []string{"regret", "robust", "tournament", "pareto", "voi", "flipPoints", "pathHeatmap", "policyTree"}

// DefaultExperimentPollInterval is how often StreamExperimentProgress polls
// the store for an experiment this server's runner is not running (for
// example one started by another server instance).
const DefaultExperimentPollInterval = 500 * time.Millisecond

// SimulationServer implements gargiv1.SimulationServiceServer over a
// sim.ExperimentStore and a jobs.Runner.
type SimulationServer struct {
	gargiv1.UnimplementedSimulationServiceServer
	store        sim.ExperimentStore
	runner       *jobs.Runner
	registry     *engine.Registry
	logger       *slog.Logger
	pollInterval time.Duration
}

var _ gargiv1.SimulationServiceServer = (*SimulationServer)(nil)

// NewSimulationServer returns a SimulationService implementation. store
// persists models and experiments, runner executes experiments submitted to
// it (it must use the same store), and reg compiles models (see
// jobs.NewRegistry).
func NewSimulationServer(store sim.ExperimentStore, runner *jobs.Runner, reg *engine.Registry, logger *slog.Logger) *SimulationServer {
	if logger == nil {
		logger = slog.Default()
	}
	return &SimulationServer{
		store:        store,
		runner:       runner,
		registry:     reg,
		logger:       logger,
		pollInterval: DefaultExperimentPollInterval,
	}
}

// toPBIssues converts validation errors to their proto form.
func toPBIssues(errs *engine.ValidationErrors) []*gargiv1.ValidationIssue {
	issues := errs.Issues()
	out := make([]*gargiv1.ValidationIssue, len(issues))
	for i, is := range issues {
		out[i] = &gargiv1.ValidationIssue{Path: is.Path, Code: string(is.Code), Message: is.Message}
	}
	return out
}

// validationStatus returns an INVALID_ARGUMENT error carrying every issue
// of errs as a gargiv1.ValidationIssues detail, which the REST gateway
// renders as the error body's "errors" list.
func validationStatus(what string, errs *engine.ValidationErrors) error {
	st := status.New(codes.InvalidArgument, fmt.Sprintf("%s: %s", what, errs.Error()))
	if withDetails, err := st.WithDetails(&gargiv1.ValidationIssues{Errors: toPBIssues(errs)}); err == nil {
		st = withDetails
	}
	return st.Err()
}

// simulationError maps runner, replay and store errors to gRPC status
// errors.
func simulationError(what string, err error) error {
	var ve *engine.ValidationErrors
	switch {
	case errors.As(err, &ve) && ve.Len() > 0:
		return validationStatus(what, ve)
	case errors.Is(err, jobs.ErrQueueFull):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, jobs.ErrClosed):
		return status.Error(codes.Unavailable, err.Error())
	case errors.Is(err, jobs.ErrRunIndex), errors.Is(err, jobs.ErrUnknownWorld):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, jobs.ErrUnknownPolicy):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, jobs.ErrNoReport):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, engine.ErrPolicyNotFound), errors.Is(err, engine.ErrDeploymentNotFound), engine.IsStoreNotFound(err):
		return status.Error(codes.NotFound, err.Error())
	default:
		return ToGRPCError(err)
	}
}

// requestFormat parses a request's document format ("yaml" by default).
func requestFormat(f string) (engine.Format, error) {
	format, err := jobs.ParseFormat(f)
	if err != nil {
		return "", status.Errorf(codes.InvalidArgument, "format: %v", err)
	}
	return format, nil
}

// bundleOf returns the model bundle carried by a request.
func bundleOf(id, simDoc, graphDoc string, policies map[string]string, format string) (sim.ModelBundle, error) {
	if strings.TrimSpace(simDoc) == "" {
		return sim.ModelBundle{}, status.Error(codes.InvalidArgument, "sim is required")
	}
	f, err := requestFormat(format)
	if err != nil {
		return sim.ModelBundle{}, err
	}
	b := sim.ModelBundle{ID: id, Sim: []byte(simDoc), Graph: []byte(graphDoc), Format: string(f)}
	if len(policies) > 0 {
		b.Policies = make(map[string][]byte, len(policies))
		for k, v := range policies {
			b.Policies[k] = []byte(v)
		}
	}
	return b, nil
}

func timestampOrNil(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func toPBModel(b sim.ModelBundle) *gargiv1.Model {
	m := &gargiv1.Model{
		Id:        b.ID,
		Version:   b.Version,
		Sim:       string(b.Sim),
		Graph:     string(b.Graph),
		Format:    b.Format,
		CreatedAt: timestampOrNil(b.CreatedAt),
	}
	if len(b.Policies) > 0 {
		m.Policies = make(map[string]string, len(b.Policies))
		for k, v := range b.Policies {
			m.Policies[k] = string(v)
		}
	}
	return m
}

// ValidateModel validates the inline model. Validation problems are
// returned in the response, not as an error.
func (s *SimulationServer) ValidateModel(_ context.Context, req *gargiv1.ValidateModelRequest) (*gargiv1.ValidateModelResponse, error) {
	b, err := bundleOf("", req.GetSim(), req.GetGraph(), req.GetPolicies(), req.GetFormat())
	if err != nil {
		return nil, err
	}
	_, errs := jobs.ValidateModel(b, s.registry)
	return &gargiv1.ValidateModelResponse{Valid: errs.Len() == 0, Errors: toPBIssues(errs)}, nil
}

// SaveModel validates the model and stores it as the next version of its
// id (the request's id, else the sim document's id). An invalid model is
// rejected with INVALID_ARGUMENT and every issue in the error details.
func (s *SimulationServer) SaveModel(ctx context.Context, req *gargiv1.SaveModelRequest) (*gargiv1.SaveModelResponse, error) {
	b, err := bundleOf(req.GetId(), req.GetSim(), req.GetGraph(), req.GetPolicies(), req.GetFormat())
	if err != nil {
		return nil, err
	}
	m, errs := jobs.ValidateModel(b, s.registry)
	if errs.Len() > 0 {
		return nil, validationStatus("invalid model", errs)
	}
	if b.ID == "" {
		b.ID = m.Def.ID
	}
	if strings.TrimSpace(b.ID) == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required (the sim document has no id)")
	}
	if strings.ContainsAny(b.ID, "/?#") {
		return nil, status.Errorf(codes.InvalidArgument, "model id %q must not contain '/', '?' or '#'", b.ID)
	}
	saved, err := s.store.SaveModel(ctx, b)
	if err != nil {
		return nil, simulationError("saving model", err)
	}
	return &gargiv1.SaveModelResponse{Model: toPBModel(saved)}, nil
}

// GetModel returns one model version, the latest by default.
func (s *SimulationServer) GetModel(ctx context.Context, req *gargiv1.GetModelRequest) (*gargiv1.GetModelResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	b, err := s.store.GetModel(ctx, req.GetId(), req.GetVersion())
	if err != nil {
		return nil, simulationError("getting model", err)
	}
	return &gargiv1.GetModelResponse{Model: toPBModel(b)}, nil
}

// ListModels lists the latest version of every model, sorted by id.
func (s *SimulationServer) ListModels(ctx context.Context, _ *gargiv1.ListModelsRequest) (*gargiv1.ListModelsResponse, error) {
	models, err := s.store.ListModels(ctx)
	if err != nil {
		return nil, simulationError("listing models", err)
	}
	resp := &gargiv1.ListModelsResponse{Models: make([]*gargiv1.ModelSummary, 0, len(models))}
	for _, b := range models {
		resp.Models = append(resp.Models, &gargiv1.ModelSummary{Id: b.ID, Version: b.Version, CreatedAt: timestampOrNil(b.CreatedAt)})
	}
	return resp, nil
}

// CreateExperiment validates the experiment against the model version and
// queues it. It returns RESOURCE_EXHAUSTED when the queue is full.
func (s *SimulationServer) CreateExperiment(ctx context.Context, req *gargiv1.CreateExperimentRequest) (*gargiv1.CreateExperimentResponse, error) {
	if req.GetModel().GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "model.id is required")
	}
	if strings.TrimSpace(req.GetExperiment()) == "" {
		return nil, status.Error(codes.InvalidArgument, "experiment is required")
	}
	f, err := requestFormat(req.GetFormat())
	if err != nil {
		return nil, err
	}
	rec, err := s.runner.Submit(ctx, sim.ExperimentRecord{
		ID:         generateUUID(),
		Model:      sim.ModelRef{ID: req.GetModel().GetId(), Version: req.GetModel().GetVersion()},
		Definition: []byte(req.GetExperiment()),
		Format:     string(f),
	})
	if err != nil {
		return nil, simulationError("invalid experiment", err)
	}
	return &gargiv1.CreateExperimentResponse{Id: rec.ID, Status: toPBExperimentStatus(rec.Status)}, nil
}

// GetExperiment returns the experiment's status, progress and, once it
// succeeded, the best policy per world and the available report kinds.
func (s *SimulationServer) GetExperiment(ctx context.Context, req *gargiv1.GetExperimentRequest) (*gargiv1.GetExperimentResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	rec, err := s.store.GetExperiment(ctx, req.GetId())
	if err != nil {
		return nil, simulationError("getting experiment", err)
	}
	return &gargiv1.GetExperimentResponse{Experiment: s.toPBExperiment(rec)}, nil
}

// CancelExperiment cancels a queued or running experiment and returns its
// status afterwards (cancelled, unless it had already finished). It waits
// for a running experiment to stop, up to the request deadline.
func (s *SimulationServer) CancelExperiment(ctx context.Context, req *gargiv1.CancelExperimentRequest) (*gargiv1.CancelExperimentResponse, error) {
	id := req.GetId()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	updates, unsubscribe := s.runner.Subscribe(id)
	defer unsubscribe()
	err := s.runner.Cancel(ctx, id)
	switch {
	case errors.Is(err, jobs.ErrNotActive):
		rec, err := s.store.GetExperiment(ctx, id)
		if err != nil {
			return nil, simulationError("cancelling experiment", err)
		}
		if !rec.Status.Terminal() {
			// Not running here (for example a record left behind by a
			// stopped server): mark it cancelled.
			rec, err = s.store.UpdateExperiment(ctx, id, func(r *sim.ExperimentRecord) error {
				if !r.Status.Terminal() {
					r.Status = sim.ExperimentCancelled
					r.Error = jobs.MsgCancelled
					r.FinishedAt = time.Now().UTC()
				}
				return nil
			})
			if err != nil {
				return nil, simulationError("cancelling experiment", err)
			}
		}
		return &gargiv1.CancelExperimentResponse{Id: id, Status: toPBExperimentStatus(rec.Status)}, nil
	case err != nil:
		return nil, simulationError("cancelling experiment", err)
	}
	// Wait until the runner has recorded the final state.
	for open := true; open; {
		select {
		case <-ctx.Done():
			open = false
		case _, open = <-updates:
		}
	}
	rec, err := s.store.GetExperiment(ctx, id)
	if err != nil {
		return nil, simulationError("cancelling experiment", err)
	}
	return &gargiv1.CancelExperimentResponse{Id: id, Status: toPBExperimentStatus(rec.Status)}, nil
}

const defaultGraphID = "default"

// DeployPolicy sets the deployment mode of a policy version.
func (s *SimulationServer) DeployPolicy(ctx context.Context, req *gargiv1.DeployPolicyRequest) (*gargiv1.DeployPolicyResponse, error) {
	ref := req.GetId()
	modeStr := req.GetMode()

	if ref == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	var mode engine.DeploymentMode
	switch modeStr {
	case "live":
		mode = engine.DeploymentLive
	case "shadow":
		mode = engine.DeploymentShadow
	default:
		return nil, status.Errorf(codes.InvalidArgument, "invalid mode %q, want 'live' or 'shadow'", modeStr)
	}

	// Ensure the store is a PolicyStore
	ps, ok := s.store.(engine.PolicyStore)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "store does not support policy deployment")
	}

	var graphID, policyID, version string
	if strings.Contains(ref, ":") {
		graphID, policyID, _ = strings.Cut(ref, ":")
	} else {
		graphID = defaultGraphID
		policyID = ref
	}

	// Remove any version specifier from graph ID, if present
	if gID, gVer, ok := strings.Cut(graphID, "@"); ok {
		graphID = gID
		if version == "" {
			version = gVer
		}
	}

	// Remove any version specifier from policy ID, if present
	if pID, pVer, ok := strings.Cut(policyID, "@"); ok {
		policyID = pID
		if version == "" {
			version = pVer
		}
	}

	// Explicit version in request takes precedence
	if req.GetVersion() != "" {
		version = req.GetVersion()
	}

	if graphID == "" || policyID == "" {
		return nil, status.Errorf(codes.InvalidArgument, "invalid policy id format %q", ref)
	}

	// Validate policy exists in PolicyStore
	rec, err := ps.GetPolicy(ctx, graphID, policyID, version)
	if errors.Is(err, engine.ErrPolicyNotFound) {
		// Check if policy exists in a stored model bundle
		if mb, mErr := s.store.GetModel(ctx, graphID, ""); mErr == nil {
			var doc []byte
			var docName string
			for k, v := range mb.Policies {
				base := strings.TrimSuffix(strings.TrimSuffix(k, ".yaml"), ".json")
				if k == policyID || base == policyID {
					doc = v
					docName = k
					break
				}
			}
			if doc != nil {
				format := "yaml"
				if strings.HasSuffix(strings.ToLower(docName), ".json") {
					format = "json"
				}
				rec, err = ps.SavePolicy(ctx, &engine.PolicyRecord{
					ID:       policyID,
					GraphID:  graphID,
					Document: doc,
					Format:   format,
				})
			}
		}
	}
	if err != nil {
		return nil, simulationError("deploying policy", err)
	}

	dep := &engine.PolicyDeployment{
		GraphID:       graphID,
		PolicyID:      policyID,
		PolicyVersion: rec.Version,
		Mode:          mode,
	}
	if err := ps.SetDeployment(ctx, dep); err != nil {
		return nil, simulationError("deploying policy", err)
	}

	return &gargiv1.DeployPolicyResponse{Id: ref, Version: rec.Version, Mode: modeStr}, nil
}

// GetReport returns one report of a succeeded experiment as JSON:
// ReportSummary is the whole experiment.Report, any other kind the
// matching field of its reports set. It returns FAILED_PRECONDITION before
// the experiment succeeds and NOT_FOUND for a kind the experiment did not
// request.
func (s *SimulationServer) GetReport(ctx context.Context, req *gargiv1.GetReportRequest) (*gargiv1.GetReportResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	kind := req.GetKind()
	if kind != ReportSummary && !contains(reportKinds, kind) {
		return nil, status.Errorf(codes.InvalidArgument, "unknown report kind %q (want %s or one of %s)", kind, ReportSummary, strings.Join(reportKinds, ", "))
	}
	rec, err := s.store.GetExperiment(ctx, req.GetId())
	if err != nil {
		return nil, simulationError("getting report", err)
	}
	if rec.Status != sim.ExperimentSucceeded || len(rec.Report) == 0 {
		return nil, status.Errorf(codes.FailedPrecondition, "experiment %q is %s: its report is not ready", rec.ID, rec.Status)
	}
	var full map[string]any
	if err := json.Unmarshal(rec.Report, &full); err != nil {
		return nil, status.Errorf(codes.Internal, "decoding report: %v", err)
	}
	var doc any = full
	if kind != ReportSummary {
		reports, _ := full["reports"].(map[string]any)
		part, ok := reports[kind]
		if !ok {
			return nil, status.Errorf(codes.NotFound, "experiment %q has no %s report", rec.ID, kind)
		}
		doc = part
	}
	val, err := structpb.NewValue(doc)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encoding report: %v", err)
	}
	return &gargiv1.GetReportResponse{Id: rec.ID, Kind: kind, Report: val}, nil
}

// GetRun replays one replication of the experiment (see jobs.Replay) and
// returns its engine events in the StreamExecutionUpdates shape plus the
// run record.
func (s *SimulationServer) GetRun(ctx context.Context, req *gargiv1.GetRunRequest) (*gargiv1.GetRunResponse, error) {
	id := req.GetId()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	rec, err := s.store.GetExperiment(ctx, id)
	if err != nil {
		return nil, simulationError("getting run", err)
	}
	if req.GetPolicy() == "" && rec.Status != sim.ExperimentSucceeded {
		return nil, status.Errorf(codes.FailedPrecondition, "experiment %q is %s: pass a policy, or wait for the report to pick the best one", id, rec.Status)
	}
	bundle, err := s.store.GetModel(ctx, rec.Model.ID, rec.Model.Version)
	if err != nil {
		return nil, simulationError("getting run", err)
	}
	exp, err := jobs.CompileExperiment(bundle, rec.Definition, rec.Format, s.registry)
	if err != nil {
		return nil, simulationError("compiling experiment", err)
	}
	var report *experiment.Report
	if len(rec.Report) > 0 {
		report = &experiment.Report{}
		if err := json.Unmarshal(rec.Report, report); err != nil {
			return nil, status.Errorf(codes.Internal, "decoding report: %v", err)
		}
	}
	world := req.GetWorld()
	if world == "" && len(exp.Worlds) > 0 {
		world = string(exp.Worlds[0].ID)
	}
	replay, err := jobs.Replay(ctx, exp, jobs.ReplayRequest{
		World:       world,
		Policy:      req.GetPolicy(),
		Index:       int(req.GetIndex()),
		Report:      report,
		ExecutionID: engine.ExecutionID(fmt.Sprintf("sim-%s-%s-%d", id, world, req.GetIndex())),
	})
	if err != nil {
		return nil, simulationError("replaying run", err)
	}
	resp := &gargiv1.GetRunResponse{
		Id:       id,
		World:    string(replay.World),
		PolicyId: replay.PolicyID,
		Index:    req.GetIndex(),
		Seed:     replay.Seed,
		Events:   make([]*gargiv1.StreamExecutionUpdatesResponse, 0, len(replay.Events)),
		Record:   toPBRunRecord(replay.Record),
	}
	last := make(map[engine.NodeID]engine.Status)
	for _, ev := range replay.Events {
		resp.Events = append(resp.Events, eventToUpdate(ev, last))
	}
	return resp, nil
}

// StreamExperimentProgress sends a "progress" event with the current
// snapshot, one per runner progress update, and a final "done" event with
// the terminal status, then ends. For an experiment this server is not
// running it polls the store until the experiment is terminal.
func (s *SimulationServer) StreamExperimentProgress(req *gargiv1.StreamExperimentProgressRequest, stream gargiv1.SimulationService_StreamExperimentProgressServer) error {
	id := req.GetId()
	if id == "" {
		return status.Error(codes.InvalidArgument, "id is required")
	}
	ctx := stream.Context()
	updates, unsubscribe := s.runner.Subscribe(id)
	defer unsubscribe()
	rec, err := s.store.GetExperiment(ctx, id)
	if err != nil {
		return simulationError("streaming experiment", err)
	}

	var seq uint64
	send := func(eventType string, rec sim.ExperimentRecord) error {
		seq++
		return stream.Send(&gargiv1.StreamExperimentProgressResponse{
			Id:        rec.ID,
			Sequence:  seq,
			Timestamp: timestamppb.Now(),
			EventType: eventType,
			Status:    toPBExperimentStatus(rec.Status),
			Progress:  toPBProgress(rec.Progress),
			Error:     rec.Error,
		})
	}
	if rec.Status.Terminal() {
		return send("done", rec)
	}
	if err := send("progress", rec); err != nil {
		return err
	}
	for updates != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case p, ok := <-updates:
			if !ok {
				updates = nil
				break
			}
			rec.Status = sim.ExperimentRunning
			rec.Progress = p
			if err := send("progress", rec); err != nil {
				return err
			}
		}
	}

	// The runner is done with the experiment (or never ran it): follow the
	// store until it is terminal.
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	for {
		cur, err := s.store.GetExperiment(ctx, id)
		if err != nil {
			return simulationError("streaming experiment", err)
		}
		if cur.Status.Terminal() {
			return send("done", cur)
		}
		if cur.Status != rec.Status || cur.Progress != rec.Progress {
			rec = cur
			if err := send("progress", rec); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func toPBExperimentStatus(s sim.ExperimentStatus) gargiv1.ExperimentStatus {
	switch s {
	case sim.ExperimentQueued:
		return gargiv1.ExperimentStatus_EXPERIMENT_STATUS_QUEUED
	case sim.ExperimentRunning:
		return gargiv1.ExperimentStatus_EXPERIMENT_STATUS_RUNNING
	case sim.ExperimentSucceeded:
		return gargiv1.ExperimentStatus_EXPERIMENT_STATUS_SUCCEEDED
	case sim.ExperimentFailed:
		return gargiv1.ExperimentStatus_EXPERIMENT_STATUS_FAILED
	case sim.ExperimentCancelled:
		return gargiv1.ExperimentStatus_EXPERIMENT_STATUS_CANCELLED
	default:
		return gargiv1.ExperimentStatus_EXPERIMENT_STATUS_UNSPECIFIED
	}
}

func toPBProgress(p sim.Progress) *gargiv1.ExperimentProgress {
	return &gargiv1.ExperimentProgress{
		Done:     int64(p.Done),
		Total:    int64(p.Total),
		Best:     p.Best,
		BestMean: p.BestMean,
		CiWidth:  p.CIWidth,
	}
}

// toPBExperiment converts rec, summarizing its report when it has one.
func (s *SimulationServer) toPBExperiment(rec sim.ExperimentRecord) *gargiv1.Experiment {
	out := &gargiv1.Experiment{
		Id:         rec.ID,
		Model:      &gargiv1.ModelRef{Id: rec.Model.ID, Version: rec.Model.Version},
		Status:     toPBExperimentStatus(rec.Status),
		Error:      rec.Error,
		Progress:   toPBProgress(rec.Progress),
		CreatedAt:  timestampOrNil(rec.CreatedAt),
		StartedAt:  timestampOrNil(rec.StartedAt),
		FinishedAt: timestampOrNil(rec.FinishedAt),
		Definition: string(rec.Definition),
		Format:     rec.Format,
	}
	if len(rec.Report) == 0 {
		return out
	}
	var report experiment.Report
	if err := json.Unmarshal(rec.Report, &report); err != nil {
		s.logger.Warn("decoding experiment report", "experiment_id", rec.ID, "error", err)
		return out
	}
	out.ReportKinds = []string{ReportSummary}
	var reports map[string]json.RawMessage
	if report.Reports != nil {
		if data, err := json.Marshal(report.Reports); err == nil {
			_ = json.Unmarshal(data, &reports)
		}
	}
	for _, kind := range reportKinds {
		if _, ok := reports[kind]; ok {
			out.ReportKinds = append(out.ReportKinds, kind)
		}
	}
	for _, w := range report.Worlds {
		if len(w.Policies) == 0 {
			continue
		}
		best := w.Policies[0]
		out.Best = append(out.Best, &gargiv1.WorldBest{
			World:    string(w.World),
			PolicyId: best.PolicyID,
			Mean:     best.Objective.Mean,
			Ci95Low:  best.Objective.CI95Low,
			Ci95High: best.Objective.CI95High,
		})
	}
	return out
}

func toPBRunRecord(r sim.RunRecord) *gargiv1.RunRecord {
	out := &gargiv1.RunRecord{
		Objective:   r.Objective,
		Cost:        r.Cost,
		Duration:    durationpb.New(r.Duration),
		Abandoned:   r.Abandoned,
		Path:        make([]string, len(r.Path)),
		FinalStatus: string(r.FinalStatus),
		Metrics:     r.Metrics,
		Constraints: r.Constraints,
	}
	for i, n := range r.Path {
		out.Path[i] = string(n)
	}
	if len(r.Decisions) > 0 {
		out.Decisions = make(map[string]string, len(r.Decisions))
		for k, v := range r.Decisions {
			out.Decisions[string(k)] = v
		}
	}
	return out
}
