package simverify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/nodes"
	"github.com/gargi-indic/gargi-flow/internal/orchestrator"
	"github.com/gargi-indic/gargi-flow/internal/sim/behavior"
	"github.com/gargi-indic/gargi-flow/internal/sim/experiment"
	"github.com/gargi-indic/gargi-flow/internal/sim/jobs"
	"github.com/gargi-indic/gargi-flow/internal/sim/kernel"
	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/internal/sim/sampler"
	"github.com/gargi-indic/gargi-flow/internal/store/memory"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

// stubChooser satisfies decision node registration during model compilation.
type stubChooser struct{}

func (stubChooser) Choose(_ context.Context, _ engine.NodeID, _ map[string]any, options []string) (string, error) {
	if len(options) == 0 {
		return "", errors.New("no options")
	}
	return options[0], nil
}

func compilerRegistry() (*engine.Registry, error) {
	reg := engine.NewRegistry()
	if err := nodes.RegisterDefaults(reg, nodes.Dependencies{}); err != nil {
		return nil, err
	}
	if err := nodes.RegisterDecision(reg, stubChooser{}); err != nil {
		return nil, err
	}
	return reg, nil
}

type eventSink struct {
	mu     sync.Mutex
	events []*engine.Event
}

func (s *eventSink) Emit(_ context.Context, ev *engine.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
	return nil
}

func (s *eventSink) Events() []*engine.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]*engine.Event, len(s.events))
	copy(cp, s.events)
	return cp
}

// Verify executes a temporal fidelity check using the provided VerifyInput.
func Verify(ctx context.Context, input VerifyInput) (*VerificationReport, error) {
	exp := input.CompiledExp
	var err error

	if exp == nil {
		reg, rErr := compilerRegistry()
		if rErr != nil {
			return nil, fmt.Errorf("simverify: creating compiler registry: %w", rErr)
		}

		if input.ExperimentPath != "" {
			exp, err = experiment.LoadFile(input.ExperimentPath, reg)
			if err != nil {
				return nil, fmt.Errorf("simverify: loading experiment from %s: %w", input.ExperimentPath, err)
			}
		} else if input.Bundle != nil {
			exp, err = jobs.CompileExperiment(*input.Bundle, input.BundleExpDef, input.BundleFormat, reg)
			if err != nil {
				return nil, fmt.Errorf("simverify: compiling experiment from bundle: %w", err)
			}
		} else {
			return nil, errors.New("simverify: no experiment provided")
		}
	}

	scenarios := input.Scenarios
	if scenarios <= 0 {
		scenarios = 10
	}

	return verifyExperiment(ctx, exp, input.Bundle, input.PolicyID, scenarios)
}

// VerifyFile runs the verification check for an experiment file on disk.
func VerifyFile(ctx context.Context, path string, opts VerifyOptions) (*VerificationReport, error) {
	return Verify(ctx, VerifyInput{
		ExperimentPath: path,
		Scenarios:      opts.Scenarios,
		PolicyID:       opts.PolicyID,
	})
}

// VerifyCompiled runs the verification check for a compiled experiment.
func VerifyCompiled(ctx context.Context, exp *experiment.CompiledExperiment, opts VerifyOptions) (*VerificationReport, error) {
	return Verify(ctx, VerifyInput{
		CompiledExp: exp,
		Scenarios:   opts.Scenarios,
		PolicyID:    opts.PolicyID,
	})
}

// VerifyBundle runs the verification check for a model bundle and experiment definition.
func VerifyBundle(ctx context.Context, b sim.ModelBundle, def []byte, format string, opts VerifyOptions) (*VerificationReport, error) {
	return Verify(ctx, VerifyInput{
		Bundle:       &b,
		BundleExpDef: def,
		BundleFormat: format,
		Scenarios:    opts.Scenarios,
		PolicyID:     opts.PolicyID,
	})
}

func verifyExperiment(ctx context.Context, exp *experiment.CompiledExperiment, bundle *sim.ModelBundle, requestedPolicyID string, numScenarios int) (*VerificationReport, error) {
	if exp == nil || exp.Model == nil || exp.Model.Graph == nil {
		return nil, errors.New("simverify: compiled experiment is invalid or nil")
	}
	if len(exp.Worlds) == 0 {
		return nil, errors.New("simverify: experiment has no worlds defined")
	}

	policyDocBytes, policyID, simPolicy, err := resolvePolicy(ctx, exp, bundle, requestedPolicyID)
	if err != nil {
		return nil, fmt.Errorf("simverify: resolving policy: %w", err)
	}

	report := &VerificationReport{
		Scenarios: numScenarios,
	}

	var allMismatches []Mismatch
	matchedCount := 0

	for i := 0; i < numScenarios; i++ {
		world := exp.Worlds[i%len(exp.Worlds)]
		rep := i / len(exp.Worlds)
		seed := sim.Seed(exp.Def.Seed, world.ID, rep)

		// 1. Run simulation episode
		simSampler := sampler.NewMonteCarlo(seed)
		simRecord, rErr := kernel.RunEpisode(ctx, exp.Model, world, simPolicy, simSampler, kernel.Options{
			Replication: uint64(rep),
			Trace:       true,
		})
		if rErr != nil {
			return nil, fmt.Errorf("simverify: scenario %d simulation failed: %w", i, rErr)
		}

		// 2. Run Temporal execution
		temporalSampler := sampler.NewMonteCarlo(seed)
		temporalVars := map[string]any{"world": world.Params}
		latents, lErr := behavior.SampleLatents(exp.Model, temporalSampler, temporalVars)
		if lErr != nil {
			return nil, fmt.Errorf("simverify: scenario %d sampling latents: %w", i, lErr)
		}

		ps := memory.NewPolicyStore()
		savedRec, pErr := ps.SavePolicy(ctx, &engine.PolicyRecord{
			ID:       policyID,
			GraphID:  exp.Model.Graph.ID,
			Document: policyDocBytes,
			Format:   "yaml",
		})
		if pErr != nil {
			return nil, fmt.Errorf("simverify: saving policy to PolicyStore: %w", pErr)
		}

		reg := engine.NewRegistry()
		if dErr := nodes.RegisterDefaults(reg, nodes.Dependencies{
			Signals:     orchestrator.NewTemporalSignalSource(),
			PolicyStore: ps,
		}); dErr != nil {
			return nil, fmt.Errorf("simverify: registering node defaults: %w", dErr)
		}
		prodActivities := orchestrator.NewNodeActivities(reg)

		testSuite := &testsuite.WorkflowTestSuite{}
		env := testSuite.NewTestWorkflowEnvironment()

		activityAttempts := make(map[engine.NodeID]int)
		var activityMu sync.Mutex
		var samplerMu sync.Mutex

		activityStub := func(actCtx context.Context, actInput orchestrator.NodeActivityInput) (*orchestrator.NodeActivityResult, error) {
			activityMu.Lock()
			activityAttempts[actInput.NodeID]++
			activityMu.Unlock()

			strNodeID := string(actInput.NodeID)
			cb, simulated := exp.Model.CompiledBehaviors[strNodeID]
			if simulated {
				failClass := ""
				if b, ok := exp.Model.Def.Behaviors[strNodeID]; ok && b.Fail != nil {
					failClass = b.Fail.Class
				}

				samplerMu.Lock()
				out, sErr := behavior.New(cb, failClass).Sample(actCtx, temporalSampler, sim.BehaviorInput{
					NodeID:      actInput.NodeID,
					Inputs:      actInput.Inputs,
					Latents:     latents,
					World:       world,
					Replication: uint64(rep),
				})
				samplerMu.Unlock()

				if sErr != nil {
					return nil, sErr
				}
				if out.Abandoned {
					return nil, orchestrator.WrapTemporalError(engine.NewNonRetryableError(errors.New("abandoned")))
				}
				if out.Err != nil {
					return nil, orchestrator.WrapTemporalError(out.Err)
				}
				return &orchestrator.NodeActivityResult{
					NodeID: actInput.NodeID,
					Mutation: &engine.StateMutation{
						NodeID: actInput.NodeID,
						Set:    out.Output,
					},
				}, nil
			}

			return prodActivities.ExecuteNodeActivity(actCtx, actInput)
		}

		env.RegisterActivityWithOptions(activityStub, activity.RegisterOptions{
			Name: orchestrator.ExecuteNodeActivityName,
		})

		// Schedule signals for wait_for_signal nodes with simulated behaviors
		for _, nodeDef := range exp.Model.Graph.Nodes {
			if nodeDef.Type == "wait_for_signal" {
				strNodeID := string(nodeDef.ID)
				cb, simulated := exp.Model.CompiledBehaviors[strNodeID]
				if simulated {
					failClass := ""
					if b, ok := exp.Model.Def.Behaviors[strNodeID]; ok && b.Fail != nil {
						failClass = b.Fail.Class
					}

					samplerMu.Lock()
					out, sErr := behavior.New(cb, failClass).Sample(ctx, temporalSampler, sim.BehaviorInput{
						NodeID:      nodeDef.ID,
						Inputs:      nil,
						Latents:     latents,
						World:       world,
						Replication: uint64(rep),
					})
					samplerMu.Unlock()

					if sErr == nil && !out.Abandoned && out.Err == nil {
						dur := out.Duration
						if dur <= 0 {
							dur = time.Millisecond
						}
						targetID := nodeDef.ID
						sigName, _ := nodeDef.Config["signal"].(string)
						sigPayload := out.Output
						env.RegisterDelayedCallback(func() {
							if env.IsWorkflowCompleted() {
								return
							}
							env.SignalWorkflow(orchestrator.SignalChannelName, orchestrator.ExternalSignalPayload{
								TargetNodeID: targetID,
								SignalName:   sigName,
								Payload:      sigPayload,
							})
						}, time.Millisecond)
					}
				}
			}
		}

		sink := &eventSink{}
		runner := orchestrator.NewWorkflowRunner(reg).WithEventSink(sink)
		env.RegisterWorkflow(runner.GraphExecutionWorkflow)

		wfInput := orchestrator.GraphWorkflowInput{
			ExecutionID: engine.ExecutionID(fmt.Sprintf("simverify-%s-%d", world.ID, i)),
			Definition:  exp.Model.Graph,
			PinnedPolicies: map[string]string{
				policyID: savedRec.Version,
			},
		}

		env.ExecuteWorkflow(runner.GraphExecutionWorkflow, wfInput)

		var wfResult orchestrator.GraphWorkflowResult
		if getErr := env.GetWorkflowResult(&wfResult); getErr != nil && wfResult.Status == "" {
			wfResult.Status = engine.StatusFailed
		}

		var temporalPath []engine.NodeID
		temporalDecisions := make(map[engine.NodeID]string)

		for _, ev := range sink.Events() {
			switch ev.Type() {
			case engine.EventTypeNodeCompleted:
				temporalPath = append(temporalPath, ev.NodeID())
				if p, ok := ev.Payload().(engine.NodeCompletedPayload); ok && p.Output != nil {
					if c, cOk := p.Output["choice"].(string); cOk && c != "" {
						temporalDecisions[ev.NodeID()] = c
					}
				}
			case engine.EventTypeNodeFailed:
				temporalPath = append(temporalPath, ev.NodeID())
			}
		}

		if wfResult.FinalState != nil {
			for _, ns := range wfResult.FinalState.NodeStates() {
				if ns.Output() != nil {
					if c, cOk := ns.Output()["choice"].(string); cOk && c != "" {
						if _, exists := temporalDecisions[ns.NodeID()]; !exists {
							temporalDecisions[ns.NodeID()] = c
						}
					}
				}
			}
		}

		// 3. Compare simulation vs Temporal
		var scenarioMismatches []Mismatch

		// Status comparison
		if simRecord.FinalStatus != wfResult.Status {
			scenarioMismatches = append(scenarioMismatches, Mismatch{
				Scenario: i,
				World:    world.ID,
				Type:     MismatchStatus,
				Details:  fmt.Sprintf("status mismatch: sim %s vs temporal %s", simRecord.FinalStatus, wfResult.Status),
			})
		}

		// Path comparison
		pathMatch := len(simRecord.Path) == len(temporalPath)
		if pathMatch {
			for idx := range simRecord.Path {
				if simRecord.Path[idx] != temporalPath[idx] {
					pathMatch = false
					break
				}
			}
		}
		if !pathMatch {
			scenarioMismatches = append(scenarioMismatches, Mismatch{
				Scenario: i,
				World:    world.ID,
				Type:     MismatchPath,
				Details:  fmt.Sprintf("path mismatch: sim %v vs temporal %v", simRecord.Path, temporalPath),
			})
		}

		// Decision comparison
		for decNodeID, simChoice := range simRecord.Decisions {
			tChoice, ok := temporalDecisions[decNodeID]
			if !ok || tChoice != simChoice {
				scenarioMismatches = append(scenarioMismatches, Mismatch{
					Scenario: i,
					World:    world.ID,
					Type:     MismatchDecision,
					Details:  fmt.Sprintf("decision mismatch at %q: sim chose %q, temporal chose %q", decNodeID, simChoice, tChoice),
				})
			}
		}

		// Retry policy comparison
		checkedNodes := make(map[engine.NodeID]bool)
		allExecuted := append(simRecord.Path, temporalPath...)
		for _, nID := range allExecuted {
			if checkedNodes[nID] {
				continue
			}
			checkedNodes[nID] = true

			var nDef *engine.NodeDefinition
			for j := range exp.Model.Graph.Nodes {
				if exp.Model.Graph.Nodes[j].ID == nID {
					nDef = &exp.Model.Graph.Nodes[j]
					break
				}
			}
			if nDef == nil {
				continue
			}

			rp := orchestrator.ResolveNodeRetryPolicy(nDef, exp.Model.Graph.RetryPolicies)
			maxAttempts := int(rp.MaximumAttempts)

			if b, ok := exp.Model.Def.Behaviors[string(nID)]; ok && b.Fail != nil {
				if b.Fail.Class == "retryable" && maxAttempts <= 1 {
					scenarioMismatches = append(scenarioMismatches, Mismatch{
						Scenario: i,
						World:    world.ID,
						Type:     MismatchRetryPolicy,
						Details:  fmt.Sprintf("node %q: simulation behavior assumes retries on error (retryable), but graph retry policy forbids retries (max_attempts: %d)", nID, maxAttempts),
					})
				} else if b.Fail.Class == "non_retryable" && maxAttempts > 1 {
					scenarioMismatches = append(scenarioMismatches, Mismatch{
						Scenario: i,
						World:    world.ID,
						Type:     MismatchRetryPolicy,
						Details:  fmt.Sprintf("node %q: simulation behavior declares non_retryable error class, but graph retry policy specifies max_attempts: %d", nID, maxAttempts),
					})
				}
			}

			activityMu.Lock()
			attempts := activityAttempts[nID]
			activityMu.Unlock()
			if attempts > 1 {
				scenarioMismatches = append(scenarioMismatches, Mismatch{
					Scenario: i,
					World:    world.ID,
					Type:     MismatchRetryPolicy,
					Details:  fmt.Sprintf("node %q: activity retried %d times in Temporal, but simulation does not retry", nID, attempts),
				})
			}
		}

		if len(scenarioMismatches) == 0 {
			matchedCount++
		} else {
			allMismatches = append(allMismatches, scenarioMismatches...)
		}
	}

	report.Matched = matchedCount
	report.Mismatches = allMismatches

	return report, nil
}

func resolvePolicy(ctx context.Context, exp *experiment.CompiledExperiment, bundle *sim.ModelBundle, requestedID string) ([]byte, string, sim.Policy, error) {
	// 1. Check policies.files in experiment definition
	if len(exp.Def.Policies.Files) > 0 {
		file := exp.Def.Policies.Files[0]
		path := filepath.Join(exp.BaseDir, file)
		doc, err := os.ReadFile(path)
		if err == nil {
			return parseAndCompilePolicy(doc, file, exp.Model)
		}
	}

	// 2. Check bundle policies
	if bundle != nil && len(bundle.Policies) > 0 {
		keys := make([]string, 0, len(bundle.Policies))
		for k := range bundle.Policies {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		targetKey := keys[0]
		if requestedID != "" {
			for _, k := range keys {
				if strings.Contains(k, requestedID) {
					targetKey = k
					break
				}
			}
		}
		doc := bundle.Policies[targetKey]
		return parseAndCompilePolicy(doc, targetKey, exp.Model)
	}

	// 3. Check for policy files in baseDir/policies/
	if exp.BaseDir != "" {
		polDir := filepath.Join(exp.BaseDir, "policies")
		entries, err := os.ReadDir(polDir)
		if err == nil && len(entries) > 0 {
			var files []string
			for _, e := range entries {
				if !e.IsDir() {
					ext := strings.ToLower(filepath.Ext(e.Name()))
					if ext == ".yaml" || ext == ".yml" || ext == ".json" {
						files = append(files, e.Name())
					}
				}
			}
			sort.Strings(files)
			if len(files) > 0 {
				targetFile := files[0]
				if requestedID != "" {
					for _, f := range files {
						if strings.Contains(f, requestedID) {
							targetFile = f
							break
						}
					}
				}
				doc, rErr := os.ReadFile(filepath.Join(polDir, targetFile))
				if rErr == nil {
					return parseAndCompilePolicy(doc, targetFile, exp.Model)
				}
			}
		}
	}

	// 4. Fallback to solver / exhaustive search
	var policies []sim.Policy
	var err error
	if solver, ok := exp.PolicySource().(experiment.Solver); ok {
		sol, sErr := solver.Solve(ctx, exp.Model, exp.Worlds)
		if sErr != nil {
			return nil, "", nil, sErr
		}
		policies, err = sol.Policies(exp.Model)
	} else {
		policies, err = exp.PolicySource().Policies(exp.Model)
	}
	if err != nil {
		return nil, "", nil, err
	}
	if len(policies) == 0 {
		return nil, "", nil, errors.New("simverify: no policies available in experiment")
	}

	selected := policies[0]
	if requestedID != "" {
		for _, p := range policies {
			if p.ID() == requestedID {
				selected = p
				break
			}
		}
	}

	var buf bytes.Buffer
	if emitErr := experiment.EmitPolicy(selected, &buf); emitErr != nil {
		return nil, "", nil, fmt.Errorf("simverify: emitting policy YAML: %w", emitErr)
	}

	return parseAndCompilePolicy(buf.Bytes(), "emitted.yaml", exp.Model)
}

func parseAndCompilePolicy(doc []byte, filename string, m *model.CompiledModel) ([]byte, string, sim.Policy, error) {
	format := engine.FormatYAML
	if strings.HasSuffix(filename, ".json") {
		format = engine.FormatJSON
	}

	pdef, err := model.ParsePolicy(doc, format)
	if err != nil {
		return nil, "", nil, fmt.Errorf("parsing policy document: %w", err)
	}

	simPol, perrs := model.CompilePolicy(pdef, m)
	if perrs != nil && perrs.Len() > 0 {
		return nil, "", nil, fmt.Errorf("compiling policy: %w", perrs)
	}

	return doc, pdef.ID, simPol, nil
}
