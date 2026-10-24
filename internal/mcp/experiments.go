package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/sim/experiment"
	"github.com/gargi-indic/gargi-flow/internal/sim/explain"
	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/internal/simbackend"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
)

type runRecord struct {
	modelID   string
	report    *experiment.Report
	createdAt time.Time
}

type experimentsStore struct {
	mu     sync.Mutex
	runs   map[string]*runRecord
	nextID int
}

func newExperimentsStore() *experimentsStore {
	return &experimentsStore{
		runs: make(map[string]*runRecord),
	}
}

func (s *experimentsStore) put(id string, rec *runRecord) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		s.nextID++
		id = fmt.Sprintf("exp-%d", s.nextID)
	}
	s.runs[id] = rec
	return id
}

func (s *experimentsStore) get(id string) (*runRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.runs[id]
	return rec, ok
}

// RunExperimentInput specifies the arguments for run_experiment.
type RunExperimentInput struct {
	ModelID    string `json:"model_id" jsonschema:"model id"`
	Experiment string `json:"experiment" jsonschema:"the experiment/v1 YAML text. Its model: field is optional and ignored."`
}

// GetExperimentInput specifies the arguments for get_experiment.
type GetExperimentInput struct {
	ID string `json:"id" jsonschema:"the experiment ID"`
}

// GetReportInput specifies the arguments for get_report.
type GetReportInput struct {
	ID   string `json:"id" jsonschema:"the experiment ID"`
	Kind string `json:"kind" jsonschema:"kind of report (summary, regret, robust, pareto, voi, flipPoints, pathHeatmap, policyTree)"`
}

// ExplainPolicyInput specifies the arguments for explain_policy.
type ExplainPolicyInput struct {
	ModelID string `json:"model_id" jsonschema:"model id"`
	Policy  string `json:"policy" jsonschema:"the policy string (workspace policy document ID)"`
}

// ComparePoliciesInput specifies the arguments for compare_policies.
type ComparePoliciesInput struct {
	ID    string `json:"id" jsonschema:"the experiment ID"`
	World string `json:"world,omitempty" jsonschema:"world to compare policies in. default is the first world."`
	Top   int    `json:"top,omitempty" jsonschema:"number of top policies to return. default is 5."`
}

// SweepParameterInput specifies the arguments for sweep_parameter.
type SweepParameterInput struct {
	ModelID    string    `json:"model_id" jsonschema:"model id"`
	World      string    `json:"world" jsonschema:"world to sweep parameter in"`
	Param      string    `json:"param" jsonschema:"parameter to sweep"`
	Values     []float64 `json:"values" jsonschema:"list of values to sweep"`
	Experiment string    `json:"experiment" jsonschema:"the experiment/v1 YAML text. Its model: field is optional and ignored."`
}

// DeployPolicyInput specifies the arguments for deploy_policy.
type DeployPolicyInput struct {
	ModelID string `json:"model_id" jsonschema:"model id"`
	Policy  string `json:"policy" jsonschema:"policy id to deploy"`
}

// IngestOutcomeInput specifies the arguments for ingest_outcome.
type IngestOutcomeInput struct {
	ModelID string `json:"model_id" jsonschema:"model id"`
	Outcome string `json:"outcome" jsonschema:"outcome to ingest"`
}

func (s *workspaceServer) runExperiment(ctx context.Context, _ *mcp.CallToolRequest, in RunExperimentInput) (*mcp.CallToolResult, any, error) {
	if !modelIDPattern.MatchString(in.ModelID) {
		return toolErrorf(engine.CodeInvalidValue, "invalid model id"), nil, nil
	}

	s.ws.mu.Lock()
	docs, ok := s.ws.models[in.ModelID]
	s.ws.mu.Unlock()

	if !ok {
		return toolErrorf(engine.CodeInvalidValue, "model %q not found", in.ModelID), nil, nil
	}

	simBytes, _ := yaml.Marshal(docs.sim)
	graphBytes, _ := yaml.Marshal(docs.graph)

	b := sim.ModelBundle{
		Sim:      simBytes,
		Graph:    graphBytes,
		Format:   "yaml",
		Policies: make(map[string][]byte),
	}
	for k, v := range docs.policies {
		vBytes, _ := yaml.Marshal(v)
		if !strings.HasSuffix(k, ".yaml") {
			k = k + ".yaml"
		}
		b.Policies[k] = vBytes
	}

	issues, err := s.backend.ValidateBundle(ctx, b)
	if err != nil {
		return nil, nil, fmt.Errorf("validate bundle: %w", err)
	}
	if len(issues) > 0 {
		return toolError(issues, func(issue engine.ValidationIssue) string { return hintFor(issue.Code) }), nil, nil
	}

	runOpts := simbackend.RunOptions{}
	res, err := s.backend.RunBundle(ctx, b, []byte(in.Experiment), "yaml", runOpts)
	if err != nil {
		return toolError(simbackend.IssuesOf(err, engine.CodeInvalidValue), func(issue engine.ValidationIssue) string { return hintFor(issue.Code) }), nil, nil
	}

	id := s.expStore.put(res.ExperimentID, &runRecord{
		modelID:   in.ModelID,
		report:    res.Report,
		createdAt: time.Now(),
	})

	return formatExperimentSummary(id, res.Report)
}

func formatExperimentSummary(id string, rep *experiment.Report) (*mcp.CallToolResult, any, error) {
	type worldSummary struct {
		World      string  `json:"world"`
		BestPolicy string  `json:"best_policy"`
		BestMean   float64 `json:"best_mean"`
		CIWidth    float64 `json:"ci_width"`
	}

	summary := struct {
		ID     string         `json:"id"`
		Worlds []worldSummary `json:"worlds"`
	}{
		ID: id,
	}

	for _, w := range rep.Worlds {
		bestPolicy := ""
		var bestMean, ciWidth float64
		if len(w.Policies) > 0 {
			p := w.Policies[0]
			bestPolicy = p.PolicyID
			bestMean = p.Objective.Mean
			if p.Objective.N > 0 {
				ciWidth = p.Objective.CI95High - p.Objective.CI95Low
			}
		}
		summary.Worlds = append(summary.Worlds, worldSummary{
			World:      string(w.World),
			BestPolicy: bestPolicy,
			BestMean:   bestMean,
			CIWidth:    ciWidth,
		})
	}

	data, err := json.Marshal(summary)
	if err != nil {
		return nil, nil, err
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{
				Text: string(data),
			},
		},
	}, nil, nil
}

func (s *workspaceServer) getExperiment(_ context.Context, _ *mcp.CallToolRequest, in GetExperimentInput) (*mcp.CallToolResult, any, error) {
	rec, ok := s.expStore.get(in.ID)
	if !ok {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(`{"code": "unresolved_reference", "message": "experiment %q not found"}`, in.ID)}},
		}, nil, nil
	}
	return formatExperimentSummary(in.ID, rec.report)
}

func (s *workspaceServer) getReport(_ context.Context, _ *mcp.CallToolRequest, in GetReportInput) (*mcp.CallToolResult, any, error) {
	rec, ok := s.expStore.get(in.ID)
	if !ok {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(`{"code": "unresolved_reference", "message": "experiment %q not found"}`, in.ID)}},
		}, nil, nil
	}

	var data []byte
	var err error
	if in.Kind == "summary" {
		data, err = json.Marshal(rec.report)
	} else {
		// Needs to handle report kinds.
		if rec.report.Reports == nil {
			return toolError([]engine.ValidationIssue{{Path: engine.PathRoot.String(), Code: engine.CodeInvalidValue, Message: fmt.Sprintf("report kind %q was not requested in the experiment", in.Kind)}}, func(_ engine.ValidationIssue) string {
				return fmt.Sprintf("add reports: [%s] to the experiment", in.Kind)
			}), nil, nil
		}

		switch in.Kind {
		case "regret":
			data, err = json.Marshal(rec.report.Reports.Regret)
		case "robust":
			data, err = json.Marshal(rec.report.Reports.Robust)
		case "pareto":
			data, err = json.Marshal(rec.report.Reports.Pareto)
		case "voi":
			data, err = json.Marshal(rec.report.Reports.VOI)
		case "flipPoints":
			data, err = json.Marshal(rec.report.Reports.FlipPoints)
		case "pathHeatmap":
			data, err = json.Marshal(rec.report.Reports.PathHeatmap)
		case "policyTree":
			data, err = json.Marshal(rec.report.Reports.PolicyTree)
		default:
			return toolErrorf(engine.CodeInvalidValue, "unknown report kind %q", in.Kind), nil, nil
		}
		if err == nil && string(data) == "null" {
			return toolError([]engine.ValidationIssue{{Path: engine.PathRoot.String(), Code: engine.CodeInvalidValue, Message: fmt.Sprintf("report kind %q was not requested in the experiment", in.Kind)}}, func(_ engine.ValidationIssue) string {
				return fmt.Sprintf("add reports: [%s] to the experiment", in.Kind)
			}), nil, nil
		}
	}

	if err != nil {
		return nil, nil, err
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{
				Text: string(data),
			},
		},
	}, nil, nil
}

func (s *workspaceServer) explainPolicy(_ context.Context, _ *mcp.CallToolRequest, in ExplainPolicyInput) (*mcp.CallToolResult, any, error) {
	if !modelIDPattern.MatchString(in.ModelID) {
		return toolErrorf(engine.CodeInvalidValue, "invalid model id"), nil, nil
	}

	s.ws.mu.Lock()
	docs, ok := s.ws.models[in.ModelID]
	s.ws.mu.Unlock()

	if !ok {
		return toolErrorf(engine.CodeInvalidValue, "model %q not found", in.ModelID), nil, nil
	}

	policyDoc, ok := docs.policies[in.Policy]
	if !ok {
		return toolErrorf(engine.CodeInvalidValue, "policy %q not found", in.Policy), nil, nil
	}

	format := engine.FormatYAML
	if strings.HasSuffix(in.Policy, ".json") {
		format = engine.FormatJSON
	}

	policyBytes, _ := yaml.Marshal(policyDoc)
	p, err := model.ParsePolicy(policyBytes, format)
	if err != nil {
		return toolError(simbackend.IssuesOf(err, engine.CodeInvalidValue), nil), nil, nil
	}

	explanation := explain.Explain(p)

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{
				Text: explanation,
			},
		},
	}, nil, nil
}

func (s *workspaceServer) comparePolicies(_ context.Context, _ *mcp.CallToolRequest, in ComparePoliciesInput) (*mcp.CallToolResult, any, error) {
	rec, ok := s.expStore.get(in.ID)
	if !ok {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(`{"code": "unresolved_reference", "message": "experiment %q not found"}`, in.ID)}},
		}, nil, nil
	}

	rep := rec.report
	if len(rep.Worlds) == 0 {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "[]"}},
		}, nil, nil
	}

	targetWorld := string(rep.Worlds[0].World)
	if in.World != "" {
		targetWorld = in.World
	}

	var worldData *experiment.WorldReport
	for i := range rep.Worlds {
		if string(rep.Worlds[i].World) == targetWorld {
			worldData = &rep.Worlds[i]
			break
		}
	}

	if worldData == nil {
		return toolErrorf(engine.CodeInvalidValue, "world %q not found in experiment", targetWorld), nil, nil
	}

	top := in.Top
	if top <= 0 {
		top = 5
	}
	if top > len(worldData.Policies) {
		top = len(worldData.Policies)
	}

	type policyInfo struct {
		Policy  string   `json:"policy"`
		Mean    float64  `json:"mean"`
		CIWidth float64  `json:"ci_width"`
		Rank    int      `json:"rank"`
		Regret  *float64 `json:"regret,omitempty"`
	}

	var regretMap map[string]float64
	if rep.Reports != nil && rep.Reports.Regret != nil {
		regretMap = make(map[string]float64)
		for _, row := range rep.Reports.Regret.Rows {
			for _, cell := range row.Cells {
				if string(cell.World) == targetWorld && cell.Regret != nil {
					regretMap[row.PolicyID] = *cell.Regret
				}
			}
		}
	}

	var result []policyInfo
	for i := 0; i < top; i++ {
		p := worldData.Policies[i]
		ciWidth := 0.0
		if p.Objective.N > 0 {
			ciWidth = p.Objective.CI95High - p.Objective.CI95Low
		}
		info := policyInfo{
			Policy:  p.PolicyID,
			Mean:    p.Objective.Mean,
			CIWidth: ciWidth,
			Rank:    i + 1,
		}
		if regretMap != nil {
			if r, ok := regretMap[p.PolicyID]; ok {
				info.Regret = &r
			}
		}
		result = append(result, info)
	}

	data, err := json.Marshal(result)
	if err != nil {
		return nil, nil, err
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{
				Text: string(data),
			},
		},
	}, nil, nil
}

func (s *workspaceServer) sweepParameter(ctx context.Context, _ *mcp.CallToolRequest, in SweepParameterInput) (*mcp.CallToolResult, any, error) {
	if !modelIDPattern.MatchString(in.ModelID) {
		return toolErrorf(engine.CodeInvalidValue, "invalid model id"), nil, nil
	}

	s.ws.mu.Lock()
	docs, ok := s.ws.models[in.ModelID]
	s.ws.mu.Unlock()

	if !ok {
		return toolErrorf(engine.CodeInvalidValue, "model %q not found", in.ModelID), nil, nil
	}

	if len(in.Values) > 25 {
		return toolErrorf(engine.CodeInvalidValue, "too many values: max 25"), nil, nil
	}

	type sweepResult struct {
		Value      float64 `json:"value"`
		BestPolicy string  `json:"best_policy"`
		BestMean   float64 `json:"best_mean"`
	}

	var results []sweepResult

	for _, val := range in.Values {
		// Deep copy the model bundle simulation document and modify it
		simDoc := deepCopy(docs.sim).(map[string]any)

		// Apply parameter change to world
		worldsRaw, ok := simDoc["worlds"]
		if !ok {
			return toolErrorf(engine.CodeInvalidValue, "model has no worlds"), nil, nil
		}

		worlds, ok := worldsRaw.(map[string]any)
		if !ok {
			return toolErrorf(engine.CodeInvalidValue, "invalid worlds format"), nil, nil
		}

		worldRaw, ok := worlds[in.World]
		if !ok {
			return toolErrorf(engine.CodeInvalidValue, "world %q not found", in.World), nil, nil
		}

		world, ok := worldRaw.(map[string]any)
		if !ok {
			// might be nil if empty, handle gracefully
			if worldRaw == nil {
				world = make(map[string]any)
			} else {
				return toolErrorf(engine.CodeInvalidValue, "invalid world format"), nil, nil
			}
		}

		world[in.Param] = val
		worlds[in.World] = world
		simDoc["worlds"] = worlds

		var buf bytes.Buffer
		encoder := yaml.NewEncoder(&buf)
		encoder.SetIndent(2)
		if err := encoder.Encode(simDoc); err != nil {
			return toolErrorf(engine.CodeInvalidValue, "failed to re-encode sim document: %v", err), nil, nil
		}

		newSim := buf.Bytes()

		graphBytes, _ := yaml.Marshal(docs.graph)

		b := sim.ModelBundle{
			Sim:      newSim,
			Graph:    graphBytes,
			Format:   "yaml",
			Policies: make(map[string][]byte),
		}
		for k, v := range docs.policies {
			vBytes, _ := yaml.Marshal(v)
			b.Policies[k] = vBytes
		}

		runOpts := simbackend.RunOptions{}
		res, err := s.backend.RunBundle(ctx, b, []byte(in.Experiment), "yaml", runOpts)
		if err != nil {
			return toolError(simbackend.IssuesOf(err, engine.CodeInvalidValue), func(issue engine.ValidationIssue) string { return hintFor(issue.Code) }), nil, nil
		}

		bestPolicy := ""
		bestMean := 0.0

		for _, w := range res.Report.Worlds {
			if string(w.World) == in.World && len(w.Policies) > 0 {
				bestPolicy = w.Policies[0].PolicyID
				bestMean = w.Policies[0].Objective.Mean
				break
			}
		}

		results = append(results, sweepResult{
			Value:      val,
			BestPolicy: bestPolicy,
			BestMean:   bestMean,
		})
	}

	data, err := json.Marshal(results)
	if err != nil {
		return nil, nil, err
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{
				Text: string(data),
			},
		},
	}, nil, nil
}

func (s *workspaceServer) deployPolicy(_ context.Context, _ *mcp.CallToolRequest, _ DeployPolicyInput) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: `{"code": "unsupported", "message": "not available until P3 (Epic #159)"}`}},
	}, nil, nil
}

func (s *workspaceServer) ingestOutcome(_ context.Context, _ *mcp.CallToolRequest, _ IngestOutcomeInput) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: `{"code": "unsupported", "message": "not available until P3 (Epic #159)"}`}},
	}, nil, nil
}
