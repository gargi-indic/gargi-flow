package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/gargi-indic/gargi-flow/internal/simbackend"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
)

// modelIDPattern is the pattern of a workspace model id.
var modelIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// graphRef is the value of every workspace sim document's graph field.
const graphRef = "./graph.yaml"

// modelDocs are the documents of one workspace model, decoded from YAML:
// the sim/v1 and graph/v1 documents and the policy/v1 documents keyed by
// name (the file base name without ".yaml").
type modelDocs struct {
	sim      map[string]any
	graph    map[string]any
	policies map[string]map[string]any
}

// clone returns a deep copy of d.
func (d *modelDocs) clone() *modelDocs {
	c := &modelDocs{
		sim:      deepCopy(d.sim).(map[string]any),
		graph:    deepCopy(d.graph).(map[string]any),
		policies: make(map[string]map[string]any, len(d.policies)),
	}
	for k, v := range d.policies {
		c.policies[k] = deepCopy(v).(map[string]any)
	}
	return c
}

// deepCopy copies the maps and slices of a decoded document.
func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, e := range t {
			m[k] = deepCopy(e)
		}
		return m
	case []any:
		s := make([]any, len(t))
		for i, e := range t {
			s[i] = deepCopy(e)
		}
		return s
	default:
		return v
	}
}

// renderYAML renders a decoded document as YAML with sorted map keys.
func renderYAML(doc any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// renderedDocs are the rendered documents of a model.
type renderedDocs struct {
	sim, graph []byte
	// policies is keyed by file base name ("<name>.yaml").
	policies map[string][]byte
}

// render renders every document of d.
func (d *modelDocs) render() (*renderedDocs, error) {
	simDoc, err := renderYAML(d.sim)
	if err != nil {
		return nil, fmt.Errorf("rendering sim.yaml: %w", err)
	}
	graphDoc, err := renderYAML(d.graph)
	if err != nil {
		return nil, fmt.Errorf("rendering graph.yaml: %w", err)
	}
	r := &renderedDocs{sim: simDoc, graph: graphDoc, policies: make(map[string][]byte, len(d.policies))}
	for name, p := range d.policies {
		doc, err := renderYAML(p)
		if err != nil {
			return nil, fmt.Errorf("rendering policy %s: %w", name, err)
		}
		r.policies[name+".yaml"] = doc
	}
	return r, nil
}

// files returns the workspace file names of d, sorted.
func (d *modelDocs) files() []string {
	files := []string{"graph.yaml", "sim.yaml"}
	for name := range d.policies {
		files = append(files, "policies/"+name+".yaml")
	}
	sort.Strings(files)
	return files
}

// errModelNotFound is returned for an id the workspace does not hold.
var errModelNotFound = errors.New("model not found")

// workspace is a concurrency-safe in-memory store of models being authored
// through the MCP tools, keyed by model id. Each Server owns one.
type workspace struct {
	mu     sync.Mutex
	models map[string]*modelDocs
}

func newWorkspace() *workspace {
	return &workspace{models: map[string]*modelDocs{}}
}

// create copies template into the workspace as model id and returns the
// model's files.
func (w *workspace) create(id, template string) ([]string, error) {
	if !modelIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid model id %q: want %s", id, modelIDPattern)
	}
	docs, err := loadTemplate(template)
	if err != nil {
		return nil, err
	}
	docs.sim["id"] = id
	docs.sim["graph"] = graphRef
	docs.graph["id"] = id

	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.models[id]; ok {
		return nil, fmt.Errorf("model %q already exists", id)
	}
	w.models[id] = docs
	return docs.files(), nil
}

// get returns a copy of the documents of model id.
func (w *workspace) get(id string) (*modelDocs, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	d, ok := w.models[id]
	if !ok {
		return nil, fmt.Errorf("model %q: %w", id, errModelNotFound)
	}
	return d.clone(), nil
}

// update applies fn to a copy of model id and stores the copy only when fn
// succeeds, so a failed update leaves the model unchanged. It returns the
// stored documents (a copy).
func (w *workspace) update(id string, fn func(*modelDocs) error) (*modelDocs, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	d, ok := w.models[id]
	if !ok {
		return nil, fmt.Errorf("model %q: %w", id, errModelNotFound)
	}
	next := d.clone()
	if err := fn(next); err != nil {
		return nil, err
	}
	w.models[id] = next
	return next.clone(), nil
}

// toBundle returns the model bundle of model id's documents d.
func toBundle(id string, d *modelDocs) (sim.ModelBundle, error) {
	r, err := d.render()
	if err != nil {
		return sim.ModelBundle{}, err
	}
	return sim.ModelBundle{ID: id, Sim: r.sim, Graph: r.graph, Policies: r.policies, Format: "yaml"}, nil
}

// modelURIPrefix is the prefix of the gargi://models/{id} resource.
const modelURIPrefix = "gargi://models/"

// workspaceServer serves the model workspace tools of one Server.
type workspaceServer struct {
	backend  simbackend.Backend
	ws       *workspace
	expStore *experimentsStore
}

// modelIssue is a validation error returned to the agent, with its hint.
type modelIssue struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

func withHints(issues []engine.ValidationIssue) []modelIssue {
	out := make([]modelIssue, len(issues))
	for i, is := range issues {
		out[i] = modelIssue{Path: is.Path, Code: string(is.Code), Message: is.Message, Hint: hintFor(is.Code)}
	}
	return out
}

// jsonResult returns v as the JSON text of a successful tool result.
func jsonResult(v any) *mcp.CallToolResult {
	b, err := json.Marshal(v)
	if err != nil {
		return toolErrorf(engine.CodeInvalidValue, "encoding result: %v", err)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}

// validate validates the bundle of model id's documents d.
func (s *workspaceServer) validate(ctx context.Context, id string, d *modelDocs) ([]modelIssue, error) {
	b, err := toBundle(id, d)
	if err != nil {
		return nil, err
	}
	issues, err := s.backend.ValidateBundle(ctx, b)
	if err != nil {
		return nil, err
	}
	return withHints(issues), nil
}

func (s *workspaceServer) createModel(_ context.Context, _ *mcp.CallToolRequest, in CreateModelInput) (*mcp.CallToolResult, any, error) {
	files, err := s.ws.create(in.ID, in.Template)
	if err != nil {
		return toolErrorf(engine.CodeInvalidValue, "create_model: %v", err), nil, nil
	}
	return jsonResult(map[string]any{"id": in.ID, "files": files}), nil, nil
}

func (s *workspaceServer) patchModel(ctx context.Context, _ *mcp.CallToolRequest, in PatchModelInput) (*mcp.CallToolResult, any, error) {
	ops := make([]json.RawMessage, len(in.Ops))
	for i, op := range in.Ops {
		raw, err := json.Marshal(op)
		if err != nil {
			return toolErrorf(engine.CodeInvalidValue, "ops[%d]: %v", i, err), nil, nil
		}
		ops[i] = raw
	}
	var (
		failed int
		opErr  *opError
	)
	d, err := s.ws.update(in.ID, func(d *modelDocs) error {
		if failed, opErr = applyOps(d, ops); opErr != nil {
			return opErr
		}
		return nil
	})
	switch {
	case opErr != nil:
		hint := opErr.hint
		if hint == "" {
			hint = hintFor(opErr.code)
		}
		return toolError([]engine.ValidationIssue{{
			Path:    engine.PathRoot.Field("ops").Index(failed).String(),
			Code:    opErr.code,
			Message: opErr.message,
		}}, func(engine.ValidationIssue) string { return hint }), nil, nil
	case errors.Is(err, errModelNotFound):
		return toolErrorf(engine.CodeUnresolvedReference, "patch_model: %v: create it with create_model", err), nil, nil
	case err != nil:
		return toolErrorf(engine.CodeInvalidValue, "patch_model: %v", err), nil, nil
	}
	issues, err := s.validate(ctx, in.ID, d)
	if err != nil {
		return toolErrorf(engine.CodeInvalidValue, "patch_model: applied %d operations, but validation failed: %v", len(ops), err), nil, nil
	}
	return jsonResult(map[string]any{"applied": len(ops), "errors": issues}), nil, nil
}

func (s *workspaceServer) validateModel(ctx context.Context, _ *mcp.CallToolRequest, in ValidateModelInput) (*mcp.CallToolResult, any, error) {
	d, err := s.ws.get(in.ID)
	if err != nil {
		return toolErrorf(engine.CodeUnresolvedReference, "validate_model: %v", err), nil, nil
	}
	issues, err := s.validate(ctx, in.ID, d)
	if err != nil {
		return toolErrorf(engine.CodeInvalidValue, "validate_model: %v", err), nil, nil
	}
	return jsonResult(map[string]any{"errors": issues}), nil, nil
}

func (s *workspaceServer) readModel(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	uri := req.Params.URI
	d, err := s.ws.get(strings.TrimPrefix(uri, modelURIPrefix))
	if err != nil {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	r, err := d.render()
	if err != nil {
		return nil, err
	}
	policies := make(map[string]string, len(r.policies))
	for k, v := range r.policies {
		policies[k] = string(v)
	}
	b, err := json.Marshal(map[string]any{"sim.yaml": string(r.sim), "graph.yaml": string(r.graph), "policies": policies})
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: "application/json", Text: string(b)}}}, nil
}

// loadTemplate decodes the documents of the embedded template name.
func loadTemplate(name string) (*modelDocs, error) {
	switch name {
	case "canary":
		name = "canary-rollout"
	case "loan":
		name = "loan-underwriting"
	case "claims":
		name = "claims-triage"
	}
	if name == "" || strings.ContainsAny(name, "/\\.") {
		return nil, fmt.Errorf("unknown template %q", name)
	}
	dir := path.Join("templates", name)
	if _, err := fs.Stat(templatesFS, dir); err != nil {
		return nil, fmt.Errorf("unknown template %q", name)
	}
	docs := &modelDocs{policies: map[string]map[string]any{}}
	var err error
	if docs.sim, err = decodeTemplateFile(path.Join(dir, "sim.yaml")); err != nil {
		return nil, err
	}
	if docs.graph, err = decodeTemplateFile(path.Join(dir, "graph.yaml")); err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(templatesFS, path.Join(dir, "policies"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".yaml" {
			continue
		}
		p, err := decodeTemplateFile(path.Join(dir, "policies", e.Name()))
		if err != nil {
			return nil, err
		}
		docs.policies[strings.TrimSuffix(e.Name(), ".yaml")] = p
	}
	return docs, nil
}

// decodeTemplateFile decodes the embedded YAML document at name. An empty
// document decodes to an empty map.
func decodeTemplateFile(name string) (map[string]any, error) {
	data, err := fs.ReadFile(templatesFS, name)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("template %s: %w", name, err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, nil
}
