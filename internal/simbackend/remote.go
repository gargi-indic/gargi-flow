package simbackend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/gargi-indic/gargi-flow/internal/sim/experiment"
	"github.com/gargi-indic/gargi-flow/internal/sim/explain"
	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

// ErrNotFound is wrapped by the errors Remote returns when the server
// answers 404: an unknown model, model version, experiment or report, or a
// policy name the model does not contain. Test for it with errors.Is.
var ErrNotFound = errors.New("simbackend: not found")

// Remote is a Backend that talks to a gargi server over the SimulationService
// REST and SSE API, using only net/http.
type Remote struct {
	base   string
	client *http.Client
}

var _ Backend = (*Remote)(nil)

// NewRemote returns a Backend for the gargi server at baseURL (an http or
// https URL with a host; a trailing "/" is trimmed). A nil client means a new
// http.Client; http.DefaultClient is never used. The client must not set a
// Timeout shorter than the experiments it follows, since the SSE stream stays
// open until the experiment ends.
func NewRemote(baseURL string, client *http.Client) (*Remote, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("simbackend: invalid server URL %q: %w", baseURL, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("simbackend: invalid server URL %q: want http(s)://host[:port]", baseURL)
	}
	if client == nil {
		client = &http.Client{}
	}
	return &Remote{base: strings.TrimRight(baseURL, "/"), client: client}, nil
}

// restError mirrors the server's JSON error body (APIError in
// internal/api): {code, status, message, details, errors:[{path,code,message}]}.
type restError struct {
	Code    int                      `json:"code"`
	Status  string                   `json:"status"`
	Message string                   `json:"message"`
	Details []string                 `json:"details"`
	Errors  []engine.ValidationIssue `json:"errors"`
}

// screaming converts "InvalidArgument" to "INVALID_ARGUMENT".
func screaming(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

// errorFromResponse turns a non-2xx response body into an error. Validation
// problems become a *engine.ValidationErrors; 404 wraps ErrNotFound.
func errorFromResponse(method, path string, status int, body []byte) error {
	var re restError
	if err := json.Unmarshal(body, &re); err != nil || (re.Message == "" && re.Status == "") {
		text := strings.TrimSpace(string(body))
		if text == "" {
			text = http.StatusText(status)
		}
		re = restError{Code: status, Status: http.StatusText(status), Message: text}
	}
	if len(re.Errors) > 0 {
		ve := &engine.ValidationErrors{}
		for _, is := range re.Errors {
			ve.AddIssue(is)
		}
		return ve
	}
	err := fmt.Errorf("simbackend: %s %s: %d %s: %s", method, path, status, screaming(re.Status), re.Message)
	if status == http.StatusNotFound {
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	return err
}

func (r *Remote) newRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.base+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// do sends a JSON request and returns the body of a 2xx response.
func (r *Remote) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	req, err := r.newRequest(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("simbackend: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("simbackend: %s %s: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, errorFromResponse(method, path, resp.StatusCode, data)
	}
	return data, nil
}

// documents are the sources of a model bundle, read from disk.
type documents struct {
	sim, graph string
	policies   map[string]string
	format     engine.Format
}

// readSim reads the sim document at path and the graph document it names
// (relative to the sim file's directory).
func readSim(path string) (*documents, error) {
	format, err := formatFor(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	def, err := model.ParseSimModel(data, format)
	if err != nil {
		return nil, err
	}
	graph, err := os.ReadFile(filepath.Join(filepath.Dir(path), def.Graph))
	if err != nil {
		return nil, err
	}
	return &documents{sim: string(data), graph: string(graph), format: format}, nil
}

// ValidateModel validates the sim/v1 model at path on the server. The graph
// document it names is read from the sim file's directory.
func (r *Remote) ValidateModel(ctx context.Context, path string) ([]engine.ValidationIssue, error) {
	docs, err := readSim(path)
	if err != nil {
		var ve *engine.ValidationErrors
		if _, ferr := formatFor(path); ferr != nil {
			return IssuesOf(ferr, engine.CodeInvalidFormat), nil
		}
		if errors.As(err, &ve) {
			return IssuesOf(err, engine.CodeInvalidFormat), nil
		}
		return nil, err
	}
	return r.validate(ctx, map[string]any{
		"sim": docs.sim, "graph": docs.graph, "format": string(docs.format),
	})
}

// ValidateBundle validates the in-memory model bundle b on the server
// (POST /v1/models:validate). An empty b.Format means YAML.
func (r *Remote) ValidateBundle(ctx context.Context, b sim.ModelBundle) ([]engine.ValidationIssue, error) {
	policies := make(map[string]string, len(b.Policies))
	for k, v := range b.Policies {
		policies[k] = string(v)
	}
	return r.validate(ctx, map[string]any{
		"sim": string(b.Sim), "graph": string(b.Graph), "policies": policies, "format": b.Format,
	})
}

// validate posts body to /v1/models:validate and returns the sorted issues
// of the response.
func (r *Remote) validate(ctx context.Context, body map[string]any) ([]engine.ValidationIssue, error) {
	const p = "/v1/models:validate"
	data, err := r.do(ctx, http.MethodPost, p, body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Errors []engine.ValidationIssue `json:"errors"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("simbackend: POST %s: decoding response: %w", p, err)
	}
	if len(resp.Errors) == 0 {
		return nil, nil
	}
	ve := &engine.ValidationErrors{}
	for _, is := range resp.Errors {
		ve.AddIssue(is)
	}
	return IssuesOf(ve, engine.CodeInvalidValue), nil
}

// flexInt decodes a JSON number or a quoted number (protojson writes int64
// values as strings).
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*f = flexInt(n)
	return nil
}

// streamEvent is the data of one SSE frame of StreamExperimentProgress, in
// protojson with proto field names.
type streamEvent struct {
	Status   string `json:"status"`
	Error    string `json:"error"`
	Progress struct {
		Done     flexInt `json:"done"`
		Total    flexInt `json:"total"`
		Best     string  `json:"best"`
		BestMean float64 `json:"best_mean"`
		CIWidth  float64 `json:"ci_width"`
	} `json:"progress"`
}

const (
	statusPrefix    = "EXPERIMENT_STATUS_"
	statusSucceeded = statusPrefix + "SUCCEEDED"
)

// RunBundle runs the given experiment document against an in-memory model bundle
// on the server.
func (r *Remote) RunBundle(ctx context.Context, b sim.ModelBundle, definition []byte, format string, opts RunOptions) (*RunResult, error) {
	policies := make(map[string]string, len(b.Policies))
	for k, v := range b.Policies {
		policies[k] = string(v)
	}
	saved, err := r.do(ctx, http.MethodPost, "/v1/models", map[string]any{
		"sim": string(b.Sim), "graph": string(b.Graph), "policies": policies, "format": b.Format,
	})
	if err != nil {
		return nil, err
	}
	var saveResp struct {
		Model struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		} `json:"model"`
	}
	if err := json.Unmarshal(saved, &saveResp); err != nil || saveResp.Model.ID == "" {
		return nil, fmt.Errorf("simbackend: POST /v1/models: unexpected response %q", saved)
	}

	created, err := r.do(ctx, http.MethodPost, "/v1/experiments", map[string]any{
		"model":      map[string]any{"id": saveResp.Model.ID, "version": saveResp.Model.Version},
		"experiment": string(definition),
		"format":     format,
	})
	if err != nil {
		return nil, err
	}
	var createResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created, &createResp); err != nil || createResp.ID == "" {
		return nil, fmt.Errorf("simbackend: POST /v1/experiments: unexpected response %q", created)
	}
	id := createResp.ID

	if err := r.follow(ctx, id, opts.Progress); err != nil {
		return nil, fmt.Errorf("experiment %s: %w", id, err)
	}
	body, err := r.Report(ctx, id, "summary")
	if err != nil {
		return nil, fmt.Errorf("experiment %s: %w", id, err)
	}
	var rep experiment.Report
	if err := json.Unmarshal(body, &rep); err != nil {
		return nil, fmt.Errorf("simbackend: decoding summary report of experiment %s: %w", id, err)
	}
	return &RunResult{ExperimentID: id, Report: &rep}, nil
}

// RunExperiment uploads the model bundle the experiment at path names (the
// sim document, its graph and the policy files the experiment lists), creates
// the experiment on the server, follows its progress over SSE and returns the
// summary report with the stored experiment's ID. Policies in the result is
// nil. opts.Workers and opts.Top are not sent: the server decides them.
// Validation problems come back as *engine.ValidationErrors.
func (r *Remote) RunExperiment(ctx context.Context, path string, opts RunOptions) (*RunResult, error) {
	expFormat, err := formatFor(path)
	if err != nil {
		return nil, err
	}
	expData, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	exp, err := model.ParseExperiment(expData, expFormat)
	if err != nil {
		return nil, err
	}
	if exp.Model == "" {
		return nil, errors.New("experiment has no model")
	}
	dir := filepath.Dir(path)
	docs, err := readSim(filepath.Join(dir, exp.Model))
	if err != nil {
		return nil, err
	}
	b := sim.ModelBundle{
		Sim:      []byte(docs.sim),
		Graph:    []byte(docs.graph),
		Policies: make(map[string][]byte),
		Format:   string(docs.format),
	}
	policyPaths := append([]string(nil), exp.Policies.Files...)
	if exp.Policies.Policy != "" {
		policyPaths = append(policyPaths, exp.Policies.Policy)
	}
	for _, p := range policyPaths {
		data, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			return nil, err
		}
		b.Policies[filepath.Base(p)] = data
	}
	return r.RunBundle(ctx, b, expData, string(expFormat), opts)
}

// follow reads the SSE stream of experiment id until its done event and
// returns an error unless the experiment succeeded. It runs in the caller's
// goroutine and returns promptly when ctx is cancelled.
func (r *Remote) follow(ctx context.Context, id string, progress func(sim.Progress)) error {
	path := "/v1/experiments/" + url.PathEscape(id) + "/events"
	req, err := r.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("simbackend: GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return errorFromResponse(http.MethodGet, path, resp.StatusCode, data)
	}

	var event, data string
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			ev, d := event, data
			event, data = "", ""
			if ev == "" {
				continue
			}
			var se streamEvent
			if err := json.Unmarshal([]byte(d), &se); err != nil {
				return fmt.Errorf("simbackend: GET %s: decoding %s event: %w", path, ev, err)
			}
			switch ev {
			case "progress":
				if progress != nil {
					progress(sim.Progress{
						Done: int(se.Progress.Done), Total: int(se.Progress.Total),
						Best: se.Progress.Best, BestMean: se.Progress.BestMean, CIWidth: se.Progress.CIWidth,
					})
				}
			case "done":
				if se.Status == statusSucceeded {
					return nil
				}
				status := strings.ToLower(strings.TrimPrefix(se.Status, statusPrefix))
				if se.Error != "" {
					return fmt.Errorf("experiment %s: %s", status, se.Error)
				}
				return fmt.Errorf("experiment %s", status)
			}
		case strings.HasPrefix(line, ":"):
			// comment (heartbeat)
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("simbackend: GET %s: %w", path, err)
	}
	return fmt.Errorf("simbackend: GET %s: event stream ended before the experiment finished", path)
}

// Report returns report kind of experiment experimentID as the server sends
// it. An unknown experiment wraps ErrNotFound.
func (r *Remote) Report(ctx context.Context, experimentID, kind string) (json.RawMessage, error) {
	path := "/v1/experiments/" + url.PathEscape(experimentID) + "/reports/" + url.PathEscape(kind)
	data, err := r.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

// Cancel cancels a queued or running experiment on the server.
func (r *Remote) Cancel(ctx context.Context, experimentID string) error {
	path := "/v1/experiments/" + url.PathEscape(experimentID) + ":cancel"
	_, err := r.do(ctx, http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	return nil
}

// ExplainPolicy renders a policy stored in a server model. ref is
// "<model-id>[@<version>]:<policy-name>", for example
// "loan-underwriting:recession-v1.yaml". A .json policy name parses as JSON;
// anything else parses as YAML.
func (r *Remote) ExplainPolicy(ctx context.Context, ref string) (string, error) {
	modelRef, name, ok := strings.Cut(ref, ":")
	if !ok || modelRef == "" || name == "" {
		return "", fmt.Errorf("invalid policy reference %q: want <model-id>[@<version>]:<policy-name>", ref)
	}
	id, version, _ := strings.Cut(modelRef, "@")
	if id == "" {
		return "", fmt.Errorf("invalid policy reference %q: empty model id", ref)
	}
	path := "/v1/models/" + url.PathEscape(id)
	if version != "" {
		path += "?version=" + url.QueryEscape(version)
	}
	data, err := r.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	var resp struct {
		Model struct {
			Policies map[string]string `json:"policies"`
		} `json:"model"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("simbackend: GET %s: decoding response: %w", path, err)
	}
	doc, ok := resp.Model.Policies[name]
	if !ok {
		return "", fmt.Errorf("model %q has no policy %q: %w", modelRef, name, ErrNotFound)
	}
	format := engine.FormatYAML
	if strings.EqualFold(filepath.Ext(name), ".json") {
		format = engine.FormatJSON
	}
	p, err := model.ParsePolicy([]byte(doc), format)
	if err != nil {
		return "", err
	}
	return explain.Explain(p), nil
}

// DeployPolicy sets the deployment mode of a policy version.
func (r *Remote) DeployPolicy(ctx context.Context, ref string, mode string) (*DeployResult, error) {
	if mode != "live" && mode != "shadow" {
		return nil, fmt.Errorf("invalid mode %q: want 'live' or 'shadow'", mode)
	}
	cleanRef := ref
	var version string
	if before, after, ok := strings.Cut(ref, "@"); ok {
		cleanRef = before
		version = after
	}

	req := map[string]string{
		"id":      cleanRef,
		"version": version,
		"mode":    mode,
	}
	path := "/v1/policies/" + url.PathEscape(cleanRef) + ":deploy"
	body, err := r.do(ctx, http.MethodPost, path, req)
	if err != nil {
		return nil, err
	}
	var resp struct {
		ID      string `json:"id"`
		Version string `json:"version"`
		Mode    string `json:"mode"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("simbackend: POST %s: decoding response: %w", path, err)
	}
	return &DeployResult{
		ID:      resp.ID,
		Version: resp.Version,
		Mode:    resp.Mode,
	}, nil
}
