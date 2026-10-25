package sim

import (
	"context"
	"errors"
	"time"
)

// ErrInvalidPageToken is returned by ExperimentStore.ListExperiments for a
// page token it did not issue.
var ErrInvalidPageToken = errors.New("sim: invalid page token")

// ModelBundle is one version of a stored simulation model: the sim/v1
// document together with the graph/v1 document it references and any
// policy/v1 documents experiments may name. Documents are kept as submitted.
//
// Bundles are versioned. Saving a bundle with an ID that already exists
// creates the next version; the store assigns Version ("1", "2", ...) and
// CreatedAt.
type ModelBundle struct {
	// ID identifies the model; it is the key of every version.
	ID string `json:"id"`
	// Version is the store-assigned version: "1" for the first save of an
	// ID, then "2", "3", ... It is ignored by SaveModel.
	Version string `json:"version"`
	// Sim is the sim/v1 document. Its graph field names the graph document,
	// which resolves to Graph by base name.
	Sim []byte `json:"sim"`
	// Graph is the graph/v1 document the sim/v1 document references.
	Graph []byte `json:"graph"`
	// Policies holds policy/v1 documents keyed by file base name (for
	// example "recession-v1.yaml"); an experiment's policies.files and
	// policies.policy paths resolve to these keys by base name. The key's
	// extension selects the document format.
	Policies map[string][]byte `json:"policies,omitempty"`
	// Format is the format of Sim: "yaml" or "json".
	Format string `json:"format"`
	// CreatedAt is when this version was saved; the store assigns it.
	CreatedAt time.Time `json:"createdAt"`
}

// ModelRef names one version of a stored model. An empty Version means the
// latest version.
type ModelRef struct {
	// ID is the model ID.
	ID string `json:"id"`
	// Version is the model version; empty means the latest one.
	Version string `json:"version,omitempty"`
}

// ExperimentStatus is the lifecycle state of an experiment job.
type ExperimentStatus string

// Experiment statuses. An experiment moves from queued to running and ends
// in one of the terminal statuses succeeded, failed or cancelled; a queued
// experiment may also be cancelled before it runs.
const (
	ExperimentQueued    ExperimentStatus = "queued"
	ExperimentRunning   ExperimentStatus = "running"
	ExperimentSucceeded ExperimentStatus = "succeeded"
	ExperimentFailed    ExperimentStatus = "failed"
	ExperimentCancelled ExperimentStatus = "cancelled"
)

// Terminal reports whether s is succeeded, failed or cancelled.
func (s ExperimentStatus) Terminal() bool {
	switch s {
	case ExperimentSucceeded, ExperimentFailed, ExperimentCancelled:
		return true
	default:
		return false
	}
}

// ExperimentCheckpoint captures the intermediate state of an experiment
// to allow resuming it after an interruption.
type ExperimentCheckpoint struct {
	CompletedChunks int    `json:"completedChunks"`
	TotalChunks     int    `json:"totalChunks"`
	EvaluatedRuns   int    `json:"evaluatedRuns"`
	PartialResults  []byte `json:"partialResults,omitempty"`
}

// Progress is a snapshot of a running experiment.
type Progress struct {
	// Done is the number of completed units of work (see
	// experiment.Options.Progress).
	Done int `json:"done"`
	// Total is the total number of units of work; 0 until known.
	Total int `json:"total"`
	// Best is the best policy ID of the first world, once known (at the
	// latest when the experiment succeeds).
	Best string `json:"best,omitempty"`
	// BestMean is the objective mean of Best.
	BestMean float64 `json:"bestMean,omitempty"`
	// CIWidth is the width of the 95% confidence interval of BestMean (0
	// for exact estimates).
	CIWidth float64 `json:"ciWidth,omitempty"`
}

// ExperimentRecord is the persisted state of one experiment job.
type ExperimentRecord struct {
	// ID identifies the experiment.
	ID string `json:"id"`
	// Model is the evaluated model version. Stored records carry a
	// concrete version.
	Model ModelRef `json:"model"`
	// Definition is the experiment/v1 document as submitted.
	Definition []byte `json:"definition"`
	// Format is the format of Definition: "yaml" or "json".
	Format string `json:"format"`
	// Status is the job's lifecycle state.
	Status ExperimentStatus `json:"status"`
	// Error describes why the experiment failed or was cancelled.
	Error string `json:"error,omitempty"`
	// Progress is the latest progress snapshot.
	Progress Progress `json:"progress"`
	// Checkpoint is the latest execution checkpoint, if any.
	Checkpoint *ExperimentCheckpoint `json:"checkpoint,omitempty"`
	// Report is the experiment.Report as JSON, set when the experiment
	// succeeds.
	Report []byte `json:"report,omitempty"`
	// CreatedAt is when the experiment was submitted. CreateExperiment sets
	// it to the current time when it is zero.
	CreatedAt time.Time `json:"createdAt"`
	// StartedAt is when the experiment started running; zero before.
	StartedAt time.Time `json:"startedAt"`
	// FinishedAt is when the experiment reached a terminal status; zero
	// before.
	FinishedAt time.Time `json:"finishedAt"`
}

// ListExperimentsOptions pages through ListExperiments.
type ListExperimentsOptions struct {
	// Limit is the maximum number of records returned; values <= 0 select
	// the store's default page size.
	Limit int
	// PageToken continues a previous listing: pass the token it returned.
	// Empty starts from the newest experiment.
	PageToken string
}

// ExperimentStore persists simulation model bundles and experiment records.
//
// Implementations are safe for concurrent use. Every value passed in or
// returned is a copy: callers may modify returned bundles and records
// without affecting the store. Not-found errors wrap engine.ErrStoreNotFound
// (an *engine.StoreNotFoundError satisfies this); creating an experiment
// whose ID exists wraps engine.ErrStoreConflict. The contract maps directly
// to SQL: bundles are rows keyed by (id, version), experiments rows keyed by
// id, listing is keyset pagination over (created_at, id).
type ExperimentStore interface {
	// SaveModel stores b as the next version of b.ID (b.Version and
	// b.CreatedAt are ignored) and returns the stored bundle with its
	// assigned Version and CreatedAt. b.ID must not be empty.
	SaveModel(ctx context.Context, b ModelBundle) (ModelBundle, error)
	// GetModel returns version version of model id, or its latest version
	// when version is empty.
	GetModel(ctx context.Context, id, version string) (ModelBundle, error)
	// ListModels returns the latest version of every model, sorted by ID.
	ListModels(ctx context.Context) ([]ModelBundle, error)

	// CreateExperiment stores a new experiment record. rec.ID must not be
	// empty and must not exist yet; a zero rec.CreatedAt is set to the
	// current time.
	CreateExperiment(ctx context.Context, rec ExperimentRecord) error
	// GetExperiment returns the experiment record with the given ID.
	GetExperiment(ctx context.Context, id string) (ExperimentRecord, error)
	// ListExperiments returns experiment records newest first (by
	// CreatedAt, then by ID descending) and the token of the next page,
	// which is empty on the last page. An unknown page token wraps
	// ErrInvalidPageToken.
	ListExperiments(ctx context.Context, opts ListExperimentsOptions) ([]ExperimentRecord, string, error)
	// UpdateExperiment atomically reads the record with the given ID,
	// applies fn to a copy and stores the result, which it returns. When fn
	// returns an error nothing is stored and that error is returned. The
	// record's ID cannot be changed.
	UpdateExperiment(ctx context.Context, id string, fn func(*ExperimentRecord) error) (ExperimentRecord, error)
}
