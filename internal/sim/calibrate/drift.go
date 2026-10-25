package calibrate

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

// DriftReport compares observed execution data against a baseline model for a specific policy version.
type DriftReport struct {
	PolicyVersion string                   `json:"policyVersion"`
	DriftDetected bool                     `json:"driftDetected"`
	Nodes         map[string]*DriftDetails `json:"nodes"`
	Latents       map[string]*DriftMetric  `json:"latents,omitempty"`
}

// DriftDetails holds the metrics comparison for a single node.
type DriftDetails struct {
	ObservedSamples  int                     `json:"observedSamples"`
	DriftDetected    bool                    `json:"driftDetected"`
	FailureRateDrift *DriftMetric            `json:"failureRateDrift,omitempty"`
	DurationDrift    *DriftMetric            `json:"durationDrift,omitempty"`
	OutputDrift      map[string]*DriftMetric `json:"outputDrift,omitempty"`
}

// DriftMetric holds the predicted vs observed values for a single metric.
type DriftMetric struct {
	Predicted float64 `json:"predicted"`
	Observed  float64 `json:"observed"`
	Delta     float64 `json:"delta"`
	IsDrift   bool    `json:"isDrift"`
}

// DriftOptions configures drift detection thresholds.
type DriftOptions struct {
	ProbabilityThreshold float64
	DurationThreshold    float64
	MinSamples           int
}

func getFloatVal(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	case float32:
		return float64(n)
	default:
		return 0
	}
}

// CheckDrift compares a simulated model's behaviors against observed executions
// and outcomes for a specific policy version and returns a DriftReport.
func CheckDrift(ctx context.Context, mod *model.CompiledModel, reader ExecutionReader, outcomes []sim.OutcomeRecord, policyVersion string, opts ...DriftOptions) (*DriftReport, error) {
	if mod == nil || reader == nil {
		return nil, errors.New("model and reader are required")
	}

	opt := DriftOptions{
		ProbabilityThreshold: 0.1,
		DurationThreshold:    0.2,
		MinSamples:           30,
	}
	if len(opts) > 0 {
		if opts[0].ProbabilityThreshold > 0 {
			opt.ProbabilityThreshold = opts[0].ProbabilityThreshold
		}
		if opts[0].DurationThreshold > 0 {
			opt.DurationThreshold = opts[0].DurationThreshold
		}
		if opts[0].MinSamples > 0 {
			opt.MinSamples = opts[0].MinSamples
		}
	}

	if policyVersion == "" {
		policyVersion = "1"
	}

	report := &DriftReport{
		PolicyVersion: policyVersion,
		Nodes:         make(map[string]*DriftDetails),
	}

	execs, err := reader.ListExecutions(ctx, engine.ExecutionFilter{GraphID: mod.Def.ID})
	if err != nil {
		return nil, err
	}

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
		state, err := reader.GetState(ctx, exec.ExecutionID)
		if err != nil || state == nil {
			continue
		}

		var execPolicyVersion string
		for _, ns := range state.NodeStates() {
			if ns == nil {
				continue
			}
			out := ns.Output()
			if v, ok := out["policy_version"].(string); ok && v != "" {
				execPolicyVersion = v
				break
			} else if v, ok := out["policyVersion"].(string); ok && v != "" {
				execPolicyVersion = v
				break
			}
		}

		if execPolicyVersion == "" {
			execPolicyVersion = "1"
		}

		if policyVersion != "*" && execPolicyVersion != policyVersion {
			continue
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

	for nodeID, st := range stats {
		if st.total < opt.MinSamples && len(st.durations) < opt.MinSamples {
			continue
		}

		b, hasBehavior := mod.Def.Behaviors[nodeID]
		if !hasBehavior {
			continue
		}

		details := &DriftDetails{
			ObservedSamples: st.total,
		}

		predictedFailRate := 0.0
		if b.Fail != nil && b.Fail.P != nil {
			predictedFailRate = getFloatVal(b.Fail.P)
		}

		observedFailCount := 0
		for _, count := range st.failures {
			observedFailCount += count
		}

		var observedFailRate float64
		if st.total > 0 {
			observedFailRate = float64(observedFailCount) / float64(st.total)
		}

		failDelta := math.Abs(observedFailRate - predictedFailRate)
		isFailDrift := failDelta >= opt.ProbabilityThreshold

		if b.Fail != nil || observedFailRate > 0 {
			details.FailureRateDrift = &DriftMetric{
				Predicted: predictedFailRate,
				Observed:  observedFailRate,
				Delta:     failDelta,
				IsDrift:   isFailDrift,
			}
			if isFailDrift {
				details.DriftDetected = true
				report.DriftDetected = true
			}
		}

		if len(st.durations) >= opt.MinSamples {
			var predictedDur float64

			switch d := b.Duration.(type) {
			case model.ValueSpec:
				if d.Lognormal != nil {
					mu := getFloatVal(d.Lognormal.Mu)
					sigma := getFloatVal(d.Lognormal.Sigma)
					predictedDur = math.Exp(mu + (sigma * sigma / 2.0))
				} else if len(d.Empirical) > 0 {
					sum := 0.0
					for _, v := range d.Empirical {
						sum += v
					}
					predictedDur = sum / float64(len(d.Empirical))
				}
			case *model.ValueSpec:
				if d != nil {
					if d.Lognormal != nil {
						mu := getFloatVal(d.Lognormal.Mu)
						sigma := getFloatVal(d.Lognormal.Sigma)
						predictedDur = math.Exp(mu + (sigma * sigma / 2.0))
					} else if len(d.Empirical) > 0 {
						sum := 0.0
						for _, v := range d.Empirical {
							sum += v
						}
						predictedDur = sum / float64(len(d.Empirical))
					}
				}
			case string:
				if td, err := time.ParseDuration(d); err == nil {
					predictedDur = td.Seconds()
				}
			case float64:
				predictedDur = d
			case int:
				predictedDur = float64(d)
			case time.Duration:
				predictedDur = d.Seconds()
			}

			if predictedDur > 0 {
				obsSum := 0.0
				for _, v := range st.durations {
					obsSum += v
				}
				observedDur := obsSum / float64(len(st.durations))

				durDeltaRatio := math.Abs(observedDur-predictedDur) / predictedDur
				isDurDrift := durDeltaRatio >= opt.DurationThreshold

				details.DurationDrift = &DriftMetric{
					Predicted: predictedDur,
					Observed:  observedDur,
					Delta:     math.Abs(observedDur - predictedDur),
					IsDrift:   isDurDrift,
				}
				if isDurDrift {
					details.DriftDetected = true
					report.DriftDetected = true
				}
			}
		}

		for outKey, valSpec := range b.Output {
			if len(valSpec.Choice) > 0 {
				tot := st.outputTotals[outKey]
				if tot >= opt.MinSamples {
					for optVal, predSpec := range valSpec.Choice {
						predP := getFloatVal(predSpec)
						obsCount := st.outputs[outKey][optVal]
						obsP := float64(obsCount) / float64(tot)
						delta := math.Abs(obsP - predP)
						isDrift := delta >= opt.ProbabilityThreshold

						if details.OutputDrift == nil {
							details.OutputDrift = make(map[string]*DriftMetric)
						}
						metricKey := outKey + "." + optVal
						details.OutputDrift[metricKey] = &DriftMetric{
							Predicted: predP,
							Observed:  obsP,
							Delta:     delta,
							IsDrift:   isDrift,
						}
						if isDrift {
							details.DriftDetected = true
							report.DriftDetected = true
						}
					}
				}
			}
		}

		if details.ObservedSamples >= opt.MinSamples || len(st.durations) >= opt.MinSamples {
			report.Nodes[nodeID] = details
		}
	}

	if len(outcomes) > 0 && len(mod.Def.Latents) > 0 {
		latentCounts := make(map[string]int)
		latentTrue := make(map[string]int)
		latentNumeric := make(map[string][]float64)
		latentChoices := make(map[string]map[string]int)

		for _, outcome := range outcomes {
			outPolicy := outcome.PolicyVersion
			if outPolicy == "" {
				outPolicy = "1"
			}
			if policyVersion != "*" && outPolicy != policyVersion {
				continue
			}
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

		for id, latSpec := range mod.Def.Latents {
			tot := latentCounts[id]
			if tot < opt.MinSamples {
				continue
			}
			if report.Latents == nil {
				report.Latents = make(map[string]*DriftMetric)
			}

			if latSpec.Bernoulli != nil {
				predP := getFloatVal(latSpec.Bernoulli.P)
				obsP := float64(latentTrue[id]) / float64(tot)
				delta := math.Abs(obsP - predP)
				isDrift := delta >= opt.ProbabilityThreshold
				report.Latents[id] = &DriftMetric{
					Predicted: predP,
					Observed:  obsP,
					Delta:     delta,
					IsDrift:   isDrift,
				}
				if isDrift {
					report.DriftDetected = true
				}
			} else if latSpec.Lognormal != nil {
				mu := getFloatVal(latSpec.Lognormal.Mu)
				sigma := getFloatVal(latSpec.Lognormal.Sigma)
				predMean := math.Exp(mu + (sigma * sigma / 2.0))
				if len(latentNumeric[id]) > 0 && predMean > 0 {
					sum := 0.0
					for _, val := range latentNumeric[id] {
						sum += val
					}
					obsMean := sum / float64(len(latentNumeric[id]))
					ratio := math.Abs(obsMean-predMean) / predMean
					isDrift := ratio >= opt.DurationThreshold
					report.Latents[id] = &DriftMetric{
						Predicted: predMean,
						Observed:  obsMean,
						Delta:     math.Abs(obsMean - predMean),
						IsDrift:   isDrift,
					}
					if isDrift {
						report.DriftDetected = true
					}
				}
			} else if len(latSpec.Choice) > 0 {
				for optVal, predSpec := range latSpec.Choice {
					predP := getFloatVal(predSpec)
					obsCount := latentChoices[id][optVal]
					obsP := float64(obsCount) / float64(tot)
					delta := math.Abs(obsP - predP)
					isDrift := delta >= opt.ProbabilityThreshold
					metricKey := id + "." + optVal
					report.Latents[metricKey] = &DriftMetric{
						Predicted: predP,
						Observed:  obsP,
						Delta:     delta,
						IsDrift:   isDrift,
					}
					if isDrift {
						report.DriftDetected = true
					}
				}
			}
		}
	}

	return report, nil
}
