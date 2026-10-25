package calibrate

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

// EmptyReader is an ExecutionReader that returns no executions.
type EmptyReader struct{}

// NewEmptyReader returns an ExecutionReader with no executions.
func NewEmptyReader() *EmptyReader {
	return &EmptyReader{}
}

// ListExecutions returns an empty list.
func (e *EmptyReader) ListExecutions(_ context.Context, _ engine.ExecutionFilter) ([]*engine.ExecutionSummary, error) {
	return []*engine.ExecutionSummary{}, nil
}

// GetState returns an error as no executions exist.
func (e *EmptyReader) GetState(_ context.Context, execID engine.ExecutionID) (*engine.GraphState, error) {
	return nil, fmt.Errorf("execution %q not found", execID)
}

// GetLogs returns an empty list of logs.
func (e *EmptyReader) GetLogs(_ context.Context, _ engine.ExecutionID, _ uint64, _ int) ([]*engine.ExecutionLogEntry, error) {
	return []*engine.ExecutionLogEntry{}, nil
}

// MemoryReader is an in-memory ExecutionReader for testing and local calibration.
type MemoryReader struct {
	Executions []*engine.ExecutionSummary
	States     map[engine.ExecutionID]*engine.GraphState
	Logs       map[engine.ExecutionID][]*engine.ExecutionLogEntry
}

// NewMemoryReader creates a MemoryReader with pre-populated records.
func NewMemoryReader() *MemoryReader {
	return &MemoryReader{
		Executions: []*engine.ExecutionSummary{},
		States:     make(map[engine.ExecutionID]*engine.GraphState),
		Logs:       make(map[engine.ExecutionID][]*engine.ExecutionLogEntry),
	}
}

// AddExecution registers an execution, its state, and audit logs.
func (m *MemoryReader) AddExecution(summary *engine.ExecutionSummary, state *engine.GraphState, logs []*engine.ExecutionLogEntry) {
	m.Executions = append(m.Executions, summary)
	if state != nil {
		m.States[summary.ExecutionID] = state
	}
	if logs != nil {
		m.Logs[summary.ExecutionID] = logs
	}
}

// ListExecutions filters the in-memory execution summaries.
func (m *MemoryReader) ListExecutions(_ context.Context, filter engine.ExecutionFilter) ([]*engine.ExecutionSummary, error) {
	var result []*engine.ExecutionSummary
	for _, exec := range m.Executions {
		if filter.GraphID != "" && exec.GraphID != "" && exec.GraphID != filter.GraphID {
			continue
		}
		if filter.Status != "" && exec.Status != filter.Status {
			continue
		}
		result = append(result, exec)
	}
	return result, nil
}

// GetState returns the execution state from memory.
func (m *MemoryReader) GetState(_ context.Context, execID engine.ExecutionID) (*engine.GraphState, error) {
	st, ok := m.States[execID]
	if !ok {
		return nil, fmt.Errorf("execution %q not found", execID)
	}
	return st, nil
}

// GetLogs returns the audit log entries from memory.
func (m *MemoryReader) GetLogs(_ context.Context, execID engine.ExecutionID, fromSequence uint64, limit int) ([]*engine.ExecutionLogEntry, error) {
	allLogs := m.Logs[execID]
	var res []*engine.ExecutionLogEntry
	for _, entry := range allLogs {
		if entry.Sequence >= fromSequence {
			res = append(res, entry)
			if limit > 0 && len(res) >= limit {
				break
			}
		}
	}
	return res, nil
}

// ExecutionRecordJSON represents the JSON serialized form of an execution for file-based calibration.
type ExecutionRecordJSON struct {
	Summary *engine.ExecutionSummary    `json:"summary"`
	State   *engine.GraphState          `json:"state"`
	Logs    []*engine.ExecutionLogEntry `json:"logs"`
}

// LoadExecutionsFile loads executions from a JSON or JSONL file.
func LoadExecutionsFile(path string) (*MemoryReader, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	reader := NewMemoryReader()

	// Try reading as JSON array
	var records []*ExecutionRecordJSON
	if err := json.Unmarshal(data, &records); err == nil {
		for _, rec := range records {
			if rec.Summary != nil {
				reader.AddExecution(rec.Summary, rec.State, rec.Logs)
			}
		}
		return reader, nil
	}

	// Try reading as JSONL
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec ExecutionRecordJSON
		if err := json.Unmarshal([]byte(line), &rec); err == nil && rec.Summary != nil {
			reader.AddExecution(rec.Summary, rec.State, rec.Logs)
		}
	}
	return reader, nil
}

// LoadOutcomesFile loads outcome records from a JSON or JSONL file.
func LoadOutcomesFile(path string) ([]sim.OutcomeRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var outcomes []sim.OutcomeRecord
	if err := json.Unmarshal(data, &outcomes); err == nil {
		return outcomes, nil
	}

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec sim.OutcomeRecord
		if err := json.Unmarshal([]byte(line), &rec); err == nil {
			outcomes = append(outcomes, rec)
		}
	}
	return outcomes, nil
}

// RemoteReader queries a gargi server REST and SSE API for executions and logs.
type RemoteReader struct {
	base   string
	client *http.Client
}

// NewRemoteReader returns a RemoteReader communicating with baseURL.
func NewRemoteReader(baseURL string, client *http.Client) (*RemoteReader, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("calibrate: invalid server URL %q: %w", baseURL, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("calibrate: invalid server URL %q: want http(s)://host[:port]", baseURL)
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &RemoteReader{
		base:   strings.TrimRight(baseURL, "/"),
		client: client,
	}, nil
}

// ListExecutions queries /v1/executions.
func (r *RemoteReader) ListExecutions(ctx context.Context, filter engine.ExecutionFilter) ([]*engine.ExecutionSummary, error) {
	vals := url.Values{}
	if filter.GraphID != "" {
		vals.Set("graph_id", filter.GraphID)
	}
	if filter.Status != "" {
		vals.Set("status", string(filter.Status))
	}
	path := r.base + "/v1/executions"
	if q := vals.Encode(); q != "" {
		path += "?" + q
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calibrate: ListExecutions: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("calibrate: ListExecutions: %d: %s", resp.StatusCode, string(body))
	}

	var data struct {
		Executions []struct {
			ExecutionID string `json:"executionId"`
			GraphID     string `json:"graphId"`
			Status      string `json:"status"`
			Version     uint64 `json:"version"`
			CreatedAt   string `json:"createdAt"`
			UpdatedAt   string `json:"updatedAt"`
		} `json:"executions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	var summaries []*engine.ExecutionSummary
	for _, ex := range data.Executions {
		created, _ := time.Parse(time.RFC3339Nano, ex.CreatedAt)
		updated, _ := time.Parse(time.RFC3339Nano, ex.UpdatedAt)
		status, _ := engine.ParseStatus(ex.Status)
		summaries = append(summaries, &engine.ExecutionSummary{
			ExecutionID: engine.ExecutionID(ex.ExecutionID),
			GraphID:     ex.GraphID,
			Status:      status,
			Version:     ex.Version,
			CreatedAt:   created,
			UpdatedAt:   updated,
		})
	}
	return summaries, nil
}

// GetState queries /v1/executions/{execution_id}.
func (r *RemoteReader) GetState(ctx context.Context, execID engine.ExecutionID) (*engine.GraphState, error) {
	path := r.base + "/v1/executions/" + url.PathEscape(string(execID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calibrate: GetState: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("calibrate: GetState: %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	state := &engine.GraphState{}
	if err := state.UnmarshalJSON(body); err == nil {
		return state, nil
	}

	// If protobuf response structure was returned:
	var pbResp struct {
		ExecutionID string `json:"executionId"`
		Status      string `json:"status"`
		Version     uint64 `json:"version"`
		NodeStates  map[string]struct {
			NodeID       string         `json:"nodeId"`
			Status       string         `json:"status"`
			Output       map[string]any `json:"output"`
			ErrorMessage string         `json:"errorMessage"`
			Version      uint64         `json:"version"`
		} `json:"nodeStates"`
	}
	if err := json.Unmarshal(body, &pbResp); err != nil {
		return nil, fmt.Errorf("calibrate: decoding execution state: %w", err)
	}

	nodes := make([]string, 0, len(pbResp.NodeStates))
	for nID := range pbResp.NodeStates {
		nodes = append(nodes, nID)
	}
	rawMap := map[string]any{
		"executionId": pbResp.ExecutionID,
		"status":      pbResp.Status,
		"version":     pbResp.Version,
		"nodes":       nodes,
		"nodeStates":  pbResp.NodeStates,
	}
	rawBytes, _ := json.Marshal(rawMap)
	if err := state.UnmarshalJSON(rawBytes); err != nil {
		return nil, fmt.Errorf("calibrate: constructing GraphState: %w", err)
	}
	return state, nil
}

// GetLogs streams events from /v1/executions/{execution_id}/events or /stream.
func (r *RemoteReader) GetLogs(ctx context.Context, execID engine.ExecutionID, fromSequence uint64, limit int) ([]*engine.ExecutionLogEntry, error) {
	path := fmt.Sprintf("%s/v1/executions/%s/events?from_sequence=%d", r.base, url.PathEscape(string(execID)), fromSequence)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calibrate: GetLogs: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, errors.New("logs stream not available")
	}

	var logs []*engine.ExecutionLogEntry
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			var item struct {
				Sequence  uint64         `json:"sequence"`
				Timestamp string         `json:"timestamp"`
				EventType string         `json:"eventType"`
				NodeID    string         `json:"nodeId"`
				Details   map[string]any `json:"details"`
				Payload   map[string]any `json:"payload"`
			}
			if err := json.Unmarshal([]byte(data), &item); err == nil {
				ts, _ := time.Parse(time.RFC3339Nano, item.Timestamp)
				dt := item.Details
				if dt == nil {
					dt = item.Payload
				}
				if dt == nil {
					dt = make(map[string]any)
				}
				if item.NodeID != "" && dt["nodeId"] == nil {
					dt["nodeId"] = item.NodeID
				}
				logs = append(logs, &engine.ExecutionLogEntry{
					ExecutionID: execID,
					Sequence:    item.Sequence,
					Timestamp:   ts,
					Message:     item.EventType,
					Details:     dt,
				})
				if limit > 0 && len(logs) >= limit {
					break
				}
			}
		}
	}
	return logs, nil
}
