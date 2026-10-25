// Package simverify provides temporal fidelity verification checks for simulation models.
package simverify

import (
	"github.com/gargi-indic/gargi-flow/internal/sim/experiment"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

// MismatchType represents the category of mismatch between simulation and Temporal.
type MismatchType string

const (
	// MismatchStatus indicates a difference in final execution status.
	MismatchStatus MismatchType = "status"
	// MismatchPath indicates a difference in the executed sequence of nodes.
	MismatchPath MismatchType = "path"
	// MismatchDecision indicates a difference in choices made by a decision node.
	MismatchDecision MismatchType = "decision"
	// MismatchRetryPolicy indicates a discrepancy in retry expectations or configuration.
	MismatchRetryPolicy MismatchType = "retry_policy"
)

// Mismatch describes a single discrepancy found during verification of a scenario.
type Mismatch struct {
	Scenario int          `json:"scenario"`
	World    sim.WorldID  `json:"world"`
	Type     MismatchType `json:"type"`
	Details  string       `json:"details"`
}

// VerificationReport contains the aggregated results of a temporal fidelity check.
type VerificationReport struct {
	Scenarios  int        `json:"scenarios"`
	Matched    int        `json:"matched"`
	Mismatches []Mismatch `json:"mismatches,omitempty"`
}

// VerifyOptions configures the verification execution.
type VerifyOptions struct {
	Scenarios int
	PolicyID  string
}

// VerifyInput provides the experiment configuration or path to Verify.
type VerifyInput struct {
	ExperimentPath string
	CompiledExp    *experiment.CompiledExperiment
	Bundle         *sim.ModelBundle
	BundleExpDef   []byte
	BundleFormat   string
	Scenarios      int
	PolicyID       string
}
