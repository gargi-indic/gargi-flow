package simbackend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"

	"github.com/gargi-indic/gargi-flow/internal/sim/experiment"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

// Backend runs simulation work for the gargi CLI and (later) the MCP server.
// Local runs in-process; the remote implementation talks to a gargi server.
type Backend interface {
	// ValidateModel validates the sim/v1 model at path. Problems come back as
	// sorted issues with a nil error; error is reserved for I/O or internal
	// failures.
	ValidateModel(ctx context.Context, path string) ([]engine.ValidationIssue, error)
	// ValidateBundle validates an in-memory model bundle (sim, graph,
	// policies). Problems come back as sorted issues with a nil error.
	ValidateBundle(ctx context.Context, b sim.ModelBundle) ([]engine.ValidationIssue, error)
	// RunBundle runs an experiment/v1 document (definition, in format) against an in-memory model bundle.
	// The document's model field is ignored in favor of b.
	RunBundle(ctx context.Context, b sim.ModelBundle, definition []byte, format string, opts RunOptions) (*RunResult, error)
	// RunExperiment runs the experiment/v1 document at path. A document or
	// policy problem is returned as an error that IssuesOf turns into
	// validation issues; an *InternalError marks a failure that is not one.
	RunExperiment(ctx context.Context, path string, opts RunOptions) (*RunResult, error)
	// Report returns report kind ("summary", "regret", ...) of a stored
	// experiment as JSON.
	Report(ctx context.Context, experimentID, kind string) (json.RawMessage, error)
	// ExplainPolicy renders a policy in plain language (internal/sim/explain).
	// For Local, ref is a policy/v1 file path.
	ExplainPolicy(ctx context.Context, ref string) (string, error)
	// Cancel cancels a queued or running experiment.
	Cancel(ctx context.Context, experimentID string) error
	// DeployPolicy sets the deployment mode of a policy version.
	DeployPolicy(ctx context.Context, ref string, mode string) (*DeployResult, error)
}

// DeployResult holds the outcome of DeployPolicy.
type DeployResult struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Mode    string `json:"mode"`
}

// RunOptions tunes RunExperiment.
type RunOptions struct {
	// Workers and Top override the experiment's values; 0 keeps the
	// experiment default.
	Workers, Top int
	// Progress, when set, receives progress snapshots while the experiment
	// runs. Calls are serialized.
	Progress func(sim.Progress)
	// ExportJSONL is a path to a file where run records are exported as JSONL.
	ExportJSONL string
	// RecordSink, when set, receives run records as JSONL.
	RecordSink io.Writer
}

// RunResult is the outcome of RunExperiment.
type RunResult struct {
	// ExperimentID identifies the stored experiment; "" for Local.
	ExperimentID string
	// Report is the experiment report.
	Report *experiment.Report
	// Policies are the evaluated policies (Local only; used by --emit-best);
	// nil for remote backends.
	Policies []sim.Policy
}

// ErrUnsupported is returned (wrapped) by a Backend for an operation it
// cannot perform, e.g. Local.Report (Local keeps no experiment store).
var ErrUnsupported = errors.New("simbackend: operation not supported by this backend")

// InternalError wraps a failure that is not a problem with the user's
// documents, such as a node registry that cannot be built.
type InternalError struct{ Err error }

// Error implements error.
func (e *InternalError) Error() string { return e.Err.Error() }

// Unwrap returns the wrapped error.
func (e *InternalError) Unwrap() error { return e.Err }

// IssuesOf returns err as sorted validation issues. An error that is not
// *engine.ValidationErrors becomes a single issue at the root path with
// fallback code.
func IssuesOf(err error, fallback engine.ErrorCode) []engine.ValidationIssue {
	var ve *engine.ValidationErrors
	var issues []engine.ValidationIssue
	if errors.As(err, &ve) && ve != nil && ve.Len() > 0 {
		issues = ve.Issues()
	} else {
		issues = []engine.ValidationIssue{{Path: engine.PathRoot.String(), Code: fallback, Message: err.Error()}}
	}
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Path != issues[j].Path {
			return issues[i].Path < issues[j].Path
		}
		if issues[i].Code != issues[j].Code {
			return issues[i].Code < issues[j].Code
		}
		return issues[i].Message < issues[j].Message
	})
	return issues
}
