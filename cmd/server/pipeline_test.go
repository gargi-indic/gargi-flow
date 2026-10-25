package main_test

import (
	"context"
	"net"
	"testing"

	"github.com/gargi-indic/gargi-flow/internal/api"
	"github.com/gargi-indic/gargi-flow/internal/config"
	"github.com/gargi-indic/gargi-flow/internal/nodes"
	"github.com/gargi-indic/gargi-flow/internal/orchestrator"
	"github.com/gargi-indic/gargi-flow/internal/store/memory"
	gargiv1 "github.com/gargi-indic/gargi-flow/pkg/api/v1"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestFullExecutionPipeline_SubmitStoreAndQuery(t *testing.T) {
	ctx := context.Background()
	memStore := memory.NewStore()
	tCfg := config.TemporalConfig{
		TaskQueue: "gargi-pipeline-queue",
	}

	// 1. Setup in-memory gRPC server representing the API layer in composition root
	service := api.NewServer(nil, tCfg, memStore, nil)
	lis := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	gargiv1.RegisterGargiServiceServer(grpcServer, service)

	go func() {
		_ = grpcServer.Serve(lis)
	}()
	t.Cleanup(func() {
		grpcServer.GracefulStop()
	})

	//nolint:staticcheck // DialContext used with bufnet for in-memory testing
	conn, err := grpc.DialContext(
		ctx,
		"bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = conn.Close()
	})

	client := gargiv1.NewGargiServiceClient(conn)

	// 2. Submit graph through API
	execID := "exec-pipeline-e2e-1"
	graphDefJSON := `{
		"apiVersion": "graph/v1",
		"id": "e2e-pipeline",
		"nodes": [
			{
				"id": "fetch_data",
				"type": "transform",
				"config": {
					"set": {"items": [1, 2, 3]}
				}
			},
			{
				"id": "process_data",
				"type": "transform",
				"config": {
					"set": {"processed": true}
				}
			}
		],
		"edges": [
			{"from": "fetch_data", "to": "process_data"}
		]
	}`

	submitResp, err := client.SubmitExecution(ctx, &gargiv1.SubmitExecutionRequest{
		ExecutionId:     execID,
		GraphDefinition: graphDefJSON,
	})
	require.NoError(t, err)
	assert.Equal(t, execID, submitResp.GetExecutionId())
	assert.Equal(t, gargiv1.ExecutionStatus_EXECUTION_STATUS_PENDING, submitResp.GetStatus())

	// Acceptance Criterion 1: Verify initial state is written to the configured StateStore
	initialSt, err := memStore.GetState(ctx, engine.ExecutionID(execID))
	require.NoError(t, err)
	require.NotNil(t, initialSt)
	assert.Equal(t, engine.StatusPending, initialSt.Status())
	assert.Equal(t, 2, len(initialSt.NodeStates()))
	assert.Equal(t, engine.StatusPending, initialSt.NodeState("fetch_data").Status())
	assert.Equal(t, engine.StatusPending, initialSt.NodeState("process_data").Status())

	// 3. Drive execution via Temporal worker pipeline using the same registry and activities wired in worker main.go
	testSuite := &testsuite.WorkflowTestSuite{}
	env := testSuite.NewTestWorkflowEnvironment()

	reg := engine.NewRegistry()
	require.NoError(t, nodes.RegisterDefaults(reg, nodes.Dependencies{
		Signals:     orchestrator.NewTemporalSignalSource(),
		PolicyStore: memStore,
	}))

	na := orchestrator.NewNodeActivities(reg)
	sa := orchestrator.NewStateActivities(memStore)

	env.RegisterWorkflow(orchestrator.GraphExecutionWorkflow)
	env.RegisterActivity(sa.PersistStateActivity)
	env.RegisterActivity(sa.WriteStateActivity)
	env.RegisterActivityWithOptions(na.ExecuteNodeActivity, activity.RegisterOptions{
		Name: orchestrator.ExecuteNodeActivityName,
	})

	def := &engine.GraphDefinition{
		APIVersion: "graph/v1",
		ID:         "e2e-pipeline",
		Nodes: []engine.NodeDefinition{
			{
				ID:   "fetch_data",
				Type: "transform",
				Config: map[string]any{
					"set": map[string]any{"items": []any{1, 2, 3}},
				},
			},
			{
				ID:   "process_data",
				Type: "transform",
				Config: map[string]any{
					"set": map[string]any{"processed": true},
				},
			},
		},
		Edges: []engine.EdgeDefinition{
			{From: "fetch_data", To: "process_data"},
		},
	}

	env.ExecuteWorkflow(orchestrator.GraphExecutionWorkflow, orchestrator.GraphWorkflowInput{
		ExecutionID: engine.ExecutionID(execID),
		Definition:  def,
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var wfResult orchestrator.GraphWorkflowResult
	require.NoError(t, env.GetWorkflowResult(&wfResult))
	assert.Equal(t, engine.StatusSucceeded, wfResult.Status)

	// Persist final completed state to StateStore
	_ = memStore.SetState(ctx, engine.ExecutionID(execID), wfResult.FinalState, initialSt.Version())

	// Acceptance Criterion 2: Verify final graph state is returned and queryable on completion
	getResp, err := client.GetExecution(ctx, &gargiv1.GetExecutionRequest{
		ExecutionId: execID,
	})
	require.NoError(t, err)
	assert.Equal(t, execID, getResp.GetExecutionId())
	assert.Equal(t, gargiv1.ExecutionStatus_EXECUTION_STATUS_SUCCEEDED, getResp.GetStatus())
	assert.Equal(t, 2, len(getResp.GetNodeStates()))
	assert.Equal(t, gargiv1.NodeStatus_NODE_STATUS_SUCCEEDED, getResp.GetNodeStates()["fetch_data"].GetStatus())
	assert.Equal(t, gargiv1.NodeStatus_NODE_STATUS_SUCCEEDED, getResp.GetNodeStates()["process_data"].GetStatus())

	// Query individual node state
	nodeResp, err := client.GetNodeState(ctx, &gargiv1.GetNodeStateRequest{
		ExecutionId: execID,
		NodeId:      "process_data",
	})
	require.NoError(t, err)
	assert.Equal(t, execID, nodeResp.GetExecutionId())
	assert.Equal(t, "process_data", nodeResp.GetNodeState().GetNodeId())
	assert.Equal(t, gargiv1.NodeStatus_NODE_STATUS_SUCCEEDED, nodeResp.GetNodeState().GetStatus())
}
