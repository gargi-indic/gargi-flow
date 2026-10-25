// Package calibrate fits simulation model parameters (latency distributions,
// branch/decision frequencies, failure rates by error class, and latent rates)
// from historical executions and ingested outcomes.
package calibrate

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

// ExecutionReader defines the capability to read past executions for calibration.
type ExecutionReader interface {
	ListExecutions(ctx context.Context, filter engine.ExecutionFilter) ([]*engine.ExecutionSummary, error)
	GetState(ctx context.Context, executionID engine.ExecutionID) (*engine.GraphState, error)
	GetLogs(ctx context.Context, executionID engine.ExecutionID, fromSequence uint64, limit int) ([]*engine.ExecutionLogEntry, error)
}

// Options configures calibration behavior.
type Options struct {
	// MinLognormalSamples is the minimum number of observed durations
	// required to fit a lognormal distribution (default 5).
	MinLognormalSamples int
	// PreferEmpirical forces duration fitting to use empirical distributions.
	PreferEmpirical bool
}

// Provenance records source window and sample size for calibration.
type Provenance struct {
	Samples  int           `json:"samples" yaml:"samples"`
	Outcomes int           `json:"outcomes" yaml:"outcomes"`
	Window   time.Duration `json:"window" yaml:"window"`
	Oldest   time.Time     `json:"oldest,omitempty" yaml:"oldest,omitempty"`
	Newest   time.Time     `json:"newest,omitempty" yaml:"newest,omitempty"`
}

// Result holds the calibrated behaviors, latents, and provenance metadata.
type Result struct {
	ModelID    string                     `json:"modelId" yaml:"id"`
	Graph      string                     `json:"graph,omitempty" yaml:"graph,omitempty"`
	Behaviors  map[string]model.Behavior  `json:"behaviors" yaml:"behaviors"`
	Latents    map[string]model.ValueSpec `json:"latents" yaml:"latents"`
	Provenance Provenance                 `json:"provenance" yaml:"provenance"`
	Samples    int                        `json:"samples" yaml:"samples"`
	Outcomes   int                        `json:"outcomes" yaml:"outcomes"`
	Window     time.Duration              `json:"window" yaml:"window"`
}

// CalibratedSim produces a full model.Sim with calibrated behaviors and latents.
// If orig is provided, other model sections (worlds, objective, graph, etc.) are preserved.
func (r *Result) CalibratedSim(orig *model.Sim) *model.Sim {
	out := &model.Sim{
		APIVersion: "sim/v1",
		ID:         r.ModelID,
		Graph:      r.Graph,
	}
	if orig != nil {
		*out = *orig
		if out.APIVersion == "" {
			out.APIVersion = "sim/v1"
		}
		if r.ModelID != "" {
			out.ID = r.ModelID
		}
		if r.Graph != "" {
			out.Graph = r.Graph
		}
	}
	out.Behaviors = make(map[string]model.Behavior, len(r.Behaviors))
	for k, v := range r.Behaviors {
		out.Behaviors[k] = v
	}
	out.Latents = make(map[string]model.ValueSpec, len(r.Latents))
	for k, v := range r.Latents {
		out.Latents[k] = v
	}
	return out
}

// OverlayYAML serializes the calibrated model as sim/v1 YAML with a provenance header comment.
func (r *Result) OverlayYAML(orig *model.Sim) ([]byte, error) {
	calSim := r.CalibratedSim(orig)
	simMap := simToMap(calSim)
	data, err := yaml.Marshal(simMap)
	if err != nil {
		return nil, err
	}

	var header strings.Builder
	header.WriteString("# Calibrated sim/v1 model overlay\n")
	fmt.Fprintf(&header, "# Samples: %d executions\n", r.Samples)
	fmt.Fprintf(&header, "# Outcomes: %d records\n", r.Outcomes)
	if r.Window > 0 {
		fmt.Fprintf(&header, "# Window: %s\n", r.Window)
	}
	header.WriteString("\n")
	header.Write(data)

	return []byte(header.String()), nil
}

func valueSpecToMap(vs model.ValueSpec) map[string]any {
	m := make(map[string]any)
	if vs.Fixed != nil {
		m["fixed"] = vs.Fixed
	}
	if vs.Choice != nil {
		m["choice"] = vs.Choice
	}
	if vs.Bernoulli != nil {
		m["bernoulli"] = map[string]any{"p": vs.Bernoulli.P}
	}
	if vs.Normal != nil {
		m["normal"] = map[string]any{"mean": vs.Normal.Mean, "sd": vs.Normal.SD}
	}
	if vs.Lognormal != nil {
		m["lognormal"] = map[string]any{"mu": vs.Lognormal.Mu, "sigma": vs.Lognormal.Sigma}
	}
	if vs.Uniform != nil {
		m["uniform"] = map[string]any{"min": vs.Uniform.Min, "max": vs.Uniform.Max}
	}
	if vs.Triangular != nil {
		m["triangular"] = map[string]any{"min": vs.Triangular.Min, "mode": vs.Triangular.Mode, "max": vs.Triangular.Max}
	}
	if vs.Exponential != nil {
		m["exponential"] = map[string]any{"rate": vs.Exponential.Rate}
	}
	if len(vs.Empirical) > 0 {
		m["empirical"] = vs.Empirical
	}
	if vs.Expr != "" {
		m["expr"] = vs.Expr
	}
	return m
}

func behaviorToMap(b model.Behavior) map[string]any {
	m := make(map[string]any)
	if b.Cost != nil {
		m["cost"] = b.Cost
	}
	if b.Duration != nil {
		if vs, ok := b.Duration.(model.ValueSpec); ok {
			m["duration"] = valueSpecToMap(vs)
		} else if s, ok := b.Duration.(string); ok && s != "" {
			m["duration"] = s
		} else {
			m["duration"] = b.Duration
		}
	}
	if b.Resource != "" {
		m["resource"] = b.Resource
	}
	if b.Patience != nil {
		m["patience"] = b.Patience
	}
	if b.Abandon != nil {
		m["abandon"] = map[string]any{"p": b.Abandon.P}
	}
	if b.Fail != nil {
		m["fail"] = map[string]any{"p": b.Fail.P, "class": b.Fail.Class}
	}
	if len(b.Output) > 0 {
		outMap := make(map[string]any, len(b.Output))
		for k, v := range b.Output {
			outMap[k] = valueSpecToMap(v)
		}
		m["output"] = outMap
	}
	if b.Agent != nil {
		m["agent"] = b.Agent
	}
	return m
}

func simToMap(s *model.Sim) map[string]any {
	m := make(map[string]any)
	m["apiVersion"] = "sim/v1"
	if s.ID != "" {
		m["id"] = s.ID
	}
	if s.Graph != "" {
		m["graph"] = s.Graph
	}
	if len(s.Worlds) > 0 {
		m["worlds"] = s.Worlds
	}
	if len(s.Latents) > 0 {
		latMap := make(map[string]any, len(s.Latents))
		for k, v := range s.Latents {
			latMap[k] = valueSpecToMap(v)
		}
		m["latents"] = latMap
	}
	if len(s.Behaviors) > 0 {
		behMap := make(map[string]any, len(s.Behaviors))
		for k, v := range s.Behaviors {
			behMap[k] = behaviorToMap(v)
		}
		m["behaviors"] = behMap
	}
	if s.Objective.Name != "" || s.Objective.Value != "" {
		objMap := map[string]any{
			"name":     s.Objective.Name,
			"value":    s.Objective.Value,
			"maximize": s.Objective.Maximize,
		}
		if len(s.Objective.Constraints) > 0 {
			objMap["constraints"] = s.Objective.Constraints
		}
		if len(s.Objective.Requirements) > 0 {
			objMap["requirements"] = s.Objective.Requirements
		}
		m["objective"] = objMap
	}
	if len(s.Resources) > 0 {
		m["resources"] = s.Resources
	}
	if s.Arrivals != nil {
		m["arrivals"] = s.Arrivals
	}
	if s.Horizon != "" {
		m["horizon"] = s.Horizon
	}
	if len(s.Metrics) > 0 {
		m["metrics"] = s.Metrics
	}
	return m
}

type nodeStats struct {
	durations    []float64
	failures     map[string]int
	total        int
	outputs      map[string]map[string]int
	outputTotals map[string]int
}

func newNodeStats() *nodeStats {
	return &nodeStats{
		failures:     make(map[string]int),
		outputs:      make(map[string]map[string]int),
		outputTotals: make(map[string]int),
	}
}

func (s *nodeStats) recordOutput(key, val string) {
	if s.outputs[key] == nil {
		s.outputs[key] = make(map[string]int)
	}
	s.outputs[key][val]++
	s.outputTotals[key]++
}

// Calibrate fits per-node latency, branch frequencies, failure rates, and latent rates from executions and outcomes.
func Calibrate(ctx context.Context, mod *model.CompiledModel, reader ExecutionReader, outcomes []sim.OutcomeRecord, opts ...Options) (*Result, error) {
	if mod == nil || reader == nil {
		return nil, errors.New("model and reader are required")
	}

	opt := Options{MinLognormalSamples: 5}
	if len(opts) > 0 {
		if opts[0].MinLognormalSamples > 0 {
			opt.MinLognormalSamples = opts[0].MinLognormalSamples
		}
		opt.PreferEmpirical = opts[0].PreferEmpirical
	}

	res := &Result{
		ModelID:   mod.Def.ID,
		Graph:     mod.Def.Graph,
		Behaviors: make(map[string]model.Behavior),
		Latents:   make(map[string]model.ValueSpec),
	}

	for id, b := range mod.Def.Behaviors {
		res.Behaviors[id] = b
	}
	for id, l := range mod.Def.Latents {
		res.Latents[id] = l
	}

	var execs []*engine.ExecutionSummary
	allExecs, err := reader.ListExecutions(ctx, engine.ExecutionFilter{GraphID: mod.Def.ID})
	if err == nil && len(allExecs) > 0 {
		execs = allExecs
	} else {
		// Fallback to querying succeeded and failed separately
		filterSucc := engine.ExecutionFilter{
			GraphID: mod.Def.ID,
			Status:  engine.StatusSucceeded,
		}
		if s, errSucc := reader.ListExecutions(ctx, filterSucc); errSucc == nil {
			execs = append(execs, s...)
		}
		filterFail := engine.ExecutionFilter{
			GraphID: mod.Def.ID,
			Status:  engine.StatusFailed,
		}
		if f, errFail := reader.ListExecutions(ctx, filterFail); errFail == nil {
			execs = append(execs, f...)
		}
	}

	res.Samples = len(execs)
	res.Outcomes = len(outcomes)

	var oldest, newest time.Time

	stats := make(map[string]*nodeStats)
	if mod.Graph != nil {
		for _, n := range mod.Graph.Nodes {
			stats[string(n.ID)] = newNodeStats()
		}
	}
	for id := range mod.Def.Behaviors {
		if _, ok := stats[id]; !ok {
			stats[id] = newNodeStats()
		}
	}

	for _, exec := range execs {
		if oldest.IsZero() || exec.CreatedAt.Before(oldest) {
			oldest = exec.CreatedAt
		}
		if newest.IsZero() || exec.CreatedAt.After(newest) {
			newest = exec.CreatedAt
		}

		state, err := reader.GetState(ctx, exec.ExecutionID)
		if err != nil {
			return nil, err
		}

		logs, _ := reader.GetLogs(ctx, exec.ExecutionID, 0, 0)

		nodeStartTimes := make(map[string]time.Time)
		failedInLogs := make(map[string]bool)

		for _, log := range logs {
			if log == nil || log.Details == nil {
				continue
			}
			nodeID := extractNodeID(log.Details)
			if nodeID == "" {
				continue
			}
			st, ok := stats[nodeID]
			if !ok {
				st = newNodeStats()
				stats[nodeID] = st
			}

			statusStr := extractStatus(log.Details)

			if strings.EqualFold(statusStr, string(engine.StatusRunning)) {
				nodeStartTimes[nodeID] = log.Timestamp
			}

			isSucceeded := strings.EqualFold(statusStr, string(engine.StatusSucceeded))
			isFailed := strings.EqualFold(statusStr, string(engine.StatusFailed))

			if isSucceeded || isFailed {
				if start, ok := nodeStartTimes[nodeID]; ok && !start.IsZero() {
					dur := log.Timestamp.Sub(start).Seconds()
					if dur >= 0 {
						st.durations = append(st.durations, dur)
					}
					delete(nodeStartTimes, nodeID)
				}
			}

			if isFailed {
				errClass := extractErrorClass(log.Details)
				if errClass == "" {
					errClass = "gargi.critical"
				}
				st.failures[errClass]++
				failedInLogs[nodeID] = true
			}

			if choice, ok := log.Details["choice"].(string); ok && choice != "" {
				st.recordOutput("choice", choice)
			}
			if branch, ok := log.Details["branch"].(string); ok && branch != "" {
				st.recordOutput("branch", branch)
			}
			if opt, ok := log.Details["selectedOption"].(string); ok && opt != "" {
				st.recordOutput("choice", opt)
			}
		}

		if state != nil {
			for _, nodeState := range state.NodeStates() {
				if nodeState == nil {
					continue
				}
				nodeID := string(nodeState.NodeID())
				st, ok := stats[nodeID]
				if !ok {
					st = newNodeStats()
					stats[nodeID] = st
				}

				nodeStatus := nodeState.Status()
				if nodeStatus == engine.StatusPending || nodeStatus == engine.StatusSkipped {
					continue
				}

				st.total++

				if nodeStatus == engine.StatusFailed && !failedInLogs[nodeID] {
					errClass := "gargi.critical"
					if msg := nodeState.ErrorMessage(); msg != "" {
						errClass = msg
					}
					st.failures[errClass]++
				}

				for k, v := range nodeState.Output() {
					if v != nil {
						st.recordOutput(k, formatValue(v))
					}
				}
			}
		}
	}

	if !oldest.IsZero() && !newest.IsZero() {
		res.Window = newest.Sub(oldest)
	}

	res.Provenance = Provenance{
		Samples:  res.Samples,
		Outcomes: res.Outcomes,
		Window:   res.Window,
		Oldest:   oldest,
		Newest:   newest,
	}

	for nodeID, st := range stats {
		if st.total == 0 && len(st.durations) == 0 {
			continue
		}

		b, hasBehavior := res.Behaviors[nodeID]
		if !hasBehavior {
			if origB, ok := mod.Def.Behaviors[nodeID]; ok {
				b = origB
			}
		}

		if st.total > 0 {
			if len(st.failures) > 0 {
				var maxClass string
				var maxCount int
				for class, count := range st.failures {
					if count > maxCount {
						maxCount = count
						maxClass = class
					}
				}
				failP := float64(maxCount) / float64(st.total)
				b.Fail = &model.Fail{
					P:     failP,
					Class: maxClass,
				}
			} else {
				b.Fail = nil
			}
		}

		if len(st.durations) > 0 {
			allPositive := true
			for _, d := range st.durations {
				if d <= 0 {
					allPositive = false
					break
				}
			}

			if len(st.durations) >= opt.MinLognormalSamples && allPositive && !opt.PreferEmpirical {
				sumLn := 0.0
				for _, d := range st.durations {
					sumLn += math.Log(d)
				}
				mu := sumLn / float64(len(st.durations))

				sumSq := 0.0
				for _, d := range st.durations {
					diff := math.Log(d) - mu
					sumSq += diff * diff
				}
				var sigma float64
				if len(st.durations) > 1 {
					sigma = math.Sqrt(sumSq / float64(len(st.durations)-1))
				}
				b.Duration = model.ValueSpec{
					Lognormal: &model.Lognormal{
						Mu:    mu,
						Sigma: sigma,
					},
				}
			} else {
				sorted := make([]float64, len(st.durations))
				copy(sorted, st.durations)
				sort.Float64s(sorted)
				b.Duration = model.ValueSpec{
					Empirical: sorted,
				}
			}
		}

		if len(st.outputs) > 0 {
			if b.Output == nil {
				b.Output = make(map[string]model.ValueSpec)
			}
			for outKey, valCounts := range st.outputs {
				tot := st.outputTotals[outKey]
				if tot > 0 {
					choiceMap := make(map[string]any, len(valCounts))
					for val, count := range valCounts {
						choiceMap[val] = float64(count) / float64(tot)
					}
					b.Output[outKey] = model.ValueSpec{
						Choice: choiceMap,
					}
				}
			}
		}

		res.Behaviors[nodeID] = b
	}

	latentCounts := make(map[string]int)
	latentTrue := make(map[string]int)
	latentChoices := make(map[string]map[string]int)
	latentNumeric := make(map[string][]float64)

	for _, outcome := range outcomes {
		for k, v := range outcome.Latents {
			latentCounts[k]++
			switch val := v.(type) {
			case bool:
				if val {
					latentTrue[k]++
				}
			case string:
				if latentChoices[k] == nil {
					latentChoices[k] = make(map[string]int)
				}
				latentChoices[k][val]++
			case float64:
				latentNumeric[k] = append(latentNumeric[k], val)
			case int:
				latentNumeric[k] = append(latentNumeric[k], float64(val))
			}
		}
	}

	for id := range mod.Def.Latents {
		total, ok := latentCounts[id]
		if !ok || total == 0 {
			continue
		}

		if len(latentChoices[id]) > 0 {
			choiceMap := make(map[string]any, len(latentChoices[id]))
			for val, count := range latentChoices[id] {
				choiceMap[val] = float64(count) / float64(total)
			}
			res.Latents[id] = model.ValueSpec{
				Choice: choiceMap,
			}
		} else if len(latentNumeric[id]) == total {
			allPos := true
			for _, n := range latentNumeric[id] {
				if n <= 0 {
					allPos = false
					break
				}
			}
			if total >= opt.MinLognormalSamples && allPos && !opt.PreferEmpirical {
				sumLn := 0.0
				for _, n := range latentNumeric[id] {
					sumLn += math.Log(n)
				}
				mu := sumLn / float64(total)
				sumSq := 0.0
				for _, n := range latentNumeric[id] {
					diff := math.Log(n) - mu
					sumSq += diff * diff
				}
				sigma := math.Sqrt(sumSq / float64(total-1))
				res.Latents[id] = model.ValueSpec{
					Lognormal: &model.Lognormal{
						Mu:    mu,
						Sigma: sigma,
					},
				}
			} else {
				sorted := make([]float64, len(latentNumeric[id]))
				copy(sorted, latentNumeric[id])
				sort.Float64s(sorted)
				res.Latents[id] = model.ValueSpec{
					Empirical: sorted,
				}
			}
		} else {
			res.Latents[id] = model.ValueSpec{
				Bernoulli: &model.Bernoulli{
					P: float64(latentTrue[id]) / float64(total),
				},
			}
		}
	}

	return res, nil
}

func extractNodeID(details map[string]any) string {
	if nid, ok := details["nodeId"].(string); ok && nid != "" {
		return nid
	}
	if nid, ok := details["node_id"].(string); ok && nid != "" {
		return nid
	}
	if nid, ok := details["nodeID"].(string); ok && nid != "" {
		return nid
	}
	return ""
}

func extractStatus(details map[string]any) string {
	if s, ok := details["status"].(string); ok && s != "" {
		return s
	}
	if s, ok := details["newStatus"].(string); ok && s != "" {
		return s
	}
	if s, ok := details["to_status"].(string); ok && s != "" {
		return s
	}
	return ""
}

func extractErrorClass(details map[string]any) string {
	if classStr, ok := details["errorClass"].(string); ok && classStr != "" {
		return classStr
	}
	if classStr, ok := details["error_class"].(string); ok && classStr != "" {
		return classStr
	}
	if classStr, ok := details["class"].(string); ok && classStr != "" {
		return classStr
	}
	if errData, ok := details["error"].(map[string]any); ok {
		if c, ok := errData["class"].(string); ok && c != "" {
			return c
		}
		if c, ok := errData["errorClass"].(string); ok && c != "" {
			return c
		}
	}
	if errDataStr, ok := details["error"].(string); ok && errDataStr != "" {
		return errDataStr
	}
	return ""
}

func formatValue(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case bool:
		if val {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", val)
	}
}
