package api_test

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/api"
	"github.com/gargi-indic/gargi-flow/internal/config"
	"github.com/gargi-indic/gargi-flow/internal/sim/jobs"
	"github.com/gargi-indic/gargi-flow/internal/store/memory"
	gargiv1 "github.com/gargi-indic/gargi-flow/pkg/api/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// newSimGRPC serves GargiService and SimulationService on an in-memory
// listener and returns a SimulationService client and the connection.
func newSimGRPC(t *testing.T, opts jobs.Options) (gargiv1.SimulationServiceClient, *grpc.ClientConn) {
	t.Helper()
	reg, err := jobs.NewRegistry()
	require.NoError(t, err)
	st := memory.NewExperimentStore()
	if opts.Now == nil {
		opts.Now = time.Now
	}
	runner, err := jobs.New(st, reg, opts)
	require.NoError(t, err)
	srv := api.NewGRPCServer(nil, config.TemporalConfig{TaskQueue: "test-queue"}, memory.NewStore(), nil)
	srv.RegisterSimulation(api.NewSimulationServer(st, runner, reg, nil))

	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = conn.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_ = runner.Close(ctx)
		srv.GracefulStop()
	})
	return gargiv1.NewSimulationServiceClient(conn), conn
}

func loanModelRequest(t *testing.T) *gargiv1.SaveModelRequest {
	t.Helper()
	body := loanModelBody(t)
	return &gargiv1.SaveModelRequest{
		Sim:      body["sim"].(string),
		Graph:    body["graph"].(string),
		Policies: body["policies"].(map[string]string),
		Format:   "yaml",
	}
}

// requireValidationDetail asserts err is INVALID_ARGUMENT carrying a
// non-empty ValidationIssues detail and returns the issues.
func requireValidationDetail(t *testing.T, err error) []*gargiv1.ValidationIssue {
	t.Helper()
	st, ok := status.FromError(err)
	require.True(t, ok, "not a status error: %v", err)
	require.Equal(t, codes.InvalidArgument, st.Code(), "%v", err)
	for _, d := range st.Details() {
		if issues, ok := d.(*gargiv1.ValidationIssues); ok {
			require.NotEmpty(t, issues.GetErrors())
			return issues.GetErrors()
		}
	}
	t.Fatalf("no ValidationIssues detail in %v", st.Details())
	return nil
}

func TestSimulationGRPC_EveryRPC(t *testing.T) {
	client, conn := newSimGRPC(t, jobs.Options{RunWorkers: 2})
	ctx := context.Background()

	health, err := grpc_health_v1.NewHealthClient(conn).Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: "gargi.v1.SimulationService"})
	require.NoError(t, err)
	require.Equal(t, grpc_health_v1.HealthCheckResponse_SERVING, health.GetStatus())

	// ValidateModel.
	req := loanModelRequest(t)
	vr, err := client.ValidateModel(ctx, &gargiv1.ValidateModelRequest{Sim: req.Sim, Graph: req.Graph, Policies: req.Policies, Format: req.Format})
	require.NoError(t, err)
	require.True(t, vr.GetValid())
	require.Empty(t, vr.GetErrors())
	vr, err = client.ValidateModel(ctx, &gargiv1.ValidateModelRequest{Sim: req.Sim, Graph: "apiVersion: graph/v1\nnodes: ["})
	require.NoError(t, err)
	require.False(t, vr.GetValid())
	require.NotEmpty(t, vr.GetErrors())

	// SaveModel: invalid models carry every issue as an error detail.
	_, err = client.SaveModel(ctx, &gargiv1.SaveModelRequest{Sim: req.Sim, Graph: "apiVersion: graph/v1\nnodes: ["})
	issues := requireValidationDetail(t, err)
	require.Equal(t, vr.GetErrors()[0].GetPath(), issues[0].GetPath(), "SaveModel and ValidateModel report the same issues")
	saved, err := client.SaveModel(ctx, req)
	require.NoError(t, err)
	require.Equal(t, "loan-underwriting", saved.GetModel().GetId())
	require.Equal(t, "1", saved.GetModel().GetVersion())

	// GetModel / ListModels.
	got, err := client.GetModel(ctx, &gargiv1.GetModelRequest{Id: "loan-underwriting"})
	require.NoError(t, err)
	require.Equal(t, req.Sim, got.GetModel().GetSim())
	_, err = client.GetModel(ctx, &gargiv1.GetModelRequest{Id: "missing"})
	require.Equal(t, codes.NotFound, status.Code(err))
	_, err = client.GetModel(ctx, &gargiv1.GetModelRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	list, err := client.ListModels(ctx, &gargiv1.ListModelsRequest{})
	require.NoError(t, err)
	require.Len(t, list.GetModels(), 1)

	// CreateExperiment: invalid experiments carry every issue.
	_, err = client.CreateExperiment(ctx, &gargiv1.CreateExperimentRequest{
		Model:      &gargiv1.ModelRef{Id: "loan-underwriting"},
		Experiment: "apiVersion: experiment/v1\nworlds: [base]\npolicies: {search: exhaustive}\nevaluator: nope\nruns: 0\ntop: 1\n",
	})
	issues = requireValidationDetail(t, err)
	require.Len(t, issues, 2)
	require.Equal(t, "$.evaluator", issues[0].GetPath())
	require.Equal(t, "invalid_value", issues[0].GetCode())
	require.NotEmpty(t, issues[0].GetMessage())

	created, err := client.CreateExperiment(ctx, &gargiv1.CreateExperimentRequest{
		Model:      &gargiv1.ModelRef{Id: "loan-underwriting", Version: "1"},
		Experiment: loanExactExperiment(t),
		Format:     "yaml",
	})
	require.NoError(t, err)
	id := created.GetId()

	// StreamExperimentProgress until done.
	stream, err := client.StreamExperimentProgress(ctx, &gargiv1.StreamExperimentProgressRequest{Id: id})
	require.NoError(t, err)
	var events []*gargiv1.StreamExperimentProgressResponse
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		events = append(events, ev)
	}
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	require.Equal(t, "done", last.GetEventType())
	require.Equal(t, gargiv1.ExperimentStatus_EXPERIMENT_STATUS_SUCCEEDED, last.GetStatus())
	require.InDelta(t, 460.0, last.GetProgress().GetBestMean(), 1e-9)
	for i, ev := range events {
		require.EqualValues(t, i+1, ev.GetSequence())
	}

	stream, err = client.StreamExperimentProgress(ctx, &gargiv1.StreamExperimentProgressRequest{Id: "missing"})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.Equal(t, codes.NotFound, status.Code(err))

	// GetExperiment.
	exp, err := client.GetExperiment(ctx, &gargiv1.GetExperimentRequest{Id: id})
	require.NoError(t, err)
	require.Equal(t, gargiv1.ExperimentStatus_EXPERIMENT_STATUS_SUCCEEDED, exp.GetExperiment().GetStatus())
	require.Len(t, exp.GetExperiment().GetBest(), 2)

	// GetReport: the report is a JSON value.
	rep, err := client.GetReport(ctx, &gargiv1.GetReportRequest{Id: id, Kind: "summary"})
	require.NoError(t, err)
	worlds := rep.GetReport().GetStructValue().GetFields()["worlds"].GetListValue().GetValues()
	require.Len(t, worlds, 2)
	_, err = client.GetReport(ctx, &gargiv1.GetReportRequest{Id: id, Kind: "voi"})
	require.Equal(t, codes.NotFound, status.Code(err))

	// GetRun.
	run, err := client.GetRun(ctx, &gargiv1.GetRunRequest{Id: id, Index: 2, World: "recession"})
	require.NoError(t, err)
	require.NotEmpty(t, run.GetEvents())
	require.Equal(t, "recession", run.GetWorld())
	for _, ev := range run.GetEvents() {
		require.NotEmpty(t, ev.GetExecutionId())
		require.NotEmpty(t, ev.GetEventType())
	}
	require.NotEmpty(t, run.GetRecord().GetPath())

	// CancelExperiment of a finished experiment keeps its status.
	cancelled, err := client.CancelExperiment(ctx, &gargiv1.CancelExperimentRequest{Id: id})
	require.NoError(t, err)
	require.Equal(t, gargiv1.ExperimentStatus_EXPERIMENT_STATUS_SUCCEEDED, cancelled.GetStatus())
	_, err = client.CancelExperiment(ctx, &gargiv1.CancelExperimentRequest{Id: "missing"})
	require.Equal(t, codes.NotFound, status.Code(err))

	// DeployPolicy: shadow and live modes
	depRes, err := client.DeployPolicy(ctx, &gargiv1.DeployPolicyRequest{
		Id:   "loan-underwriting:recession-v1.yaml",
		Mode: "shadow",
	})
	require.NoError(t, err)
	require.Equal(t, "loan-underwriting:recession-v1.yaml", depRes.GetId())
	require.Equal(t, "1", depRes.GetVersion())
	require.Equal(t, "shadow", depRes.GetMode())

	depResLive, err := client.DeployPolicy(ctx, &gargiv1.DeployPolicyRequest{
		Id:   "loan-underwriting:recession-v1.yaml",
		Mode: "live",
	})
	require.NoError(t, err)
	require.Equal(t, "live", depResLive.GetMode())

	// DeployPolicy errors
	_, err = client.DeployPolicy(ctx, &gargiv1.DeployPolicyRequest{Id: "loan-underwriting:recession-v1.yaml", Mode: "invalid"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = client.DeployPolicy(ctx, &gargiv1.DeployPolicyRequest{Id: "", Mode: "shadow"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = client.DeployPolicy(ctx, &gargiv1.DeployPolicyRequest{Id: "loan-underwriting:missing-policy", Mode: "shadow"})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestSimulationGRPC_StreamFollowsRunningExperiment(t *testing.T) {
	client, _ := newSimGRPC(t, jobs.Options{RunWorkers: 2})
	ctx := context.Background()
	_, err := client.SaveModel(ctx, loanModelRequest(t))
	require.NoError(t, err)
	created, err := client.CreateExperiment(ctx, &gargiv1.CreateExperimentRequest{
		Model:      &gargiv1.ModelRef{Id: "loan-underwriting"},
		Experiment: longExperiment,
	})
	require.NoError(t, err)

	stream, err := client.StreamExperimentProgress(ctx, &gargiv1.StreamExperimentProgressRequest{Id: created.GetId()})
	require.NoError(t, err)
	// Wait for a progress event with work done, then cancel.
	for {
		ev, err := stream.Recv()
		require.NoError(t, err)
		require.Equal(t, "progress", ev.GetEventType())
		if ev.GetProgress().GetDone() > 0 {
			require.Equal(t, gargiv1.ExperimentStatus_EXPERIMENT_STATUS_RUNNING, ev.GetStatus())
			break
		}
	}
	cancelled, err := client.CancelExperiment(ctx, &gargiv1.CancelExperimentRequest{Id: created.GetId()})
	require.NoError(t, err)
	require.Equal(t, gargiv1.ExperimentStatus_EXPERIMENT_STATUS_CANCELLED, cancelled.GetStatus(), "Cancel waits for the running experiment to stop")
	var last *gargiv1.StreamExperimentProgressResponse
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		last = ev
	}
	require.NotNil(t, last)
	require.Equal(t, "done", last.GetEventType())
	require.Equal(t, gargiv1.ExperimentStatus_EXPERIMENT_STATUS_CANCELLED, last.GetStatus())
	require.Equal(t, jobs.MsgCancelled, last.GetError())
}
