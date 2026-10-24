package mcp

import (
	"context"
	"io/fs"
	"path/filepath"

	"github.com/gargi-indic/gargi-flow/internal/simbackend"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/schemas"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options configures the mcp server.
type Options struct {
	// Backend validates workspace models; nil means simbackend.NewLocal().
	Backend simbackend.Backend
	// Version is the server version reported to clients; "" means "dev".
	Version string
}

// GetSchemaInput is the input for get_schema tool.
type GetSchemaInput struct {
	Kind string `json:"kind"`
}

// NewServer returns an *mcp.Server (go-sdk) named "gargi" with this part's
// tools and resources registered. Each server has its own model workspace.
func NewServer(opts Options) *mcp.Server {
	if opts.Version == "" {
		opts.Version = "dev"
	}
	if opts.Backend == nil {
		opts.Backend = simbackend.NewLocal()
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "gargi",
		Version: opts.Version,
	}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{
			Resources: &mcp.ResourceCapabilities{},
		},
	})

	// Register tools
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_schema",
		Description: "Returns the JSON Schema text for the specified kind.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input GetSchemaInput) (*mcp.CallToolResult, any, error) {
		schema, err := schemas.Schema(input.Kind)
		if err != nil {
			return toolErrorf(engine.CodeInvalidValue, "unknown schema kind: %q", input.Kind), nil, nil
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{
					Text: string(schema),
				},
			},
		}, nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_templates",
		Description: "Returns a JSON list of available templates.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		listing, err := listTemplates()
		if err != nil {
			return toolErrorf(engine.CodeInvalidValue, "failed to list templates: %v", err), nil, nil
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{
					Text: listing,
				},
			},
		}, nil, nil
	})

	// Register the model workspace tools and resource.
	ws := &workspaceServer{backend: opts.Backend, ws: newWorkspace()}
	ws.register(server)

	// Register concrete schemas
	for _, kind := range []string{"sim", "graph", "policy", "experiment"} {
		schemaKind := kind // capture for closure
		uri := "gargi://schemas/" + schemaKind
		server.AddResource(&mcp.Resource{
			URI:         uri,
			Name:        "JSON Schema: " + schemaKind,
			Description: "JSON schema for " + schemaKind,
			MIMEType:    "application/schema+json",
		}, func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			schema, err := schemas.Schema(schemaKind)
			if err != nil {
				return nil, err
			}
			return &mcp.ReadResourceResult{
				Contents: []*mcp.ResourceContents{
					{
						URI:      uri,
						MIMEType: "application/schema+json",
						Text:     string(schema),
					},
				},
			}, nil
		})
	}

	// Register concrete templates
	entries, err := fs.ReadDir(templatesFS, "templates")
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			_ = fs.WalkDir(templatesFS, filepath.Join("templates", name), func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() {
					rel, _ := filepath.Rel(filepath.Join("templates", name), path)
					uri := "gargi://templates/" + name + "/" + rel
					templateName := name
					templateRel := rel // capture for closure
					server.AddResource(&mcp.Resource{
						URI:         uri,
						Name:        "Template: " + templateName + " " + templateRel,
						Description: "Starter template for " + templateName + " " + templateRel,
						MIMEType:    "application/yaml",
					}, func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
						content, err := fs.ReadFile(templatesFS, filepath.Join("templates", templateName, templateRel))
						if err != nil {
							return nil, err
						}
						return &mcp.ReadResourceResult{
							Contents: []*mcp.ResourceContents{
								{
									URI:      uri,
									MIMEType: "application/yaml",
									Text:     string(content),
								},
							},
						}, nil
					})
				}
				return nil
			})
		}
	}
	return server
}

// CreateModelInput is the input of the create_model tool.
type CreateModelInput struct {
	ID       string `json:"id" jsonschema:"model id, matching ^[a-z][a-z0-9_-]{0,62}$"`
	Template string `json:"template" jsonschema:"template to start from: empty or loan-underwriting"`
}

// PatchModelInput is the input of the patch_model tool.
type PatchModelInput struct {
	ID  string           `json:"id" jsonschema:"model id"`
	Ops []map[string]any `json:"ops" jsonschema:"operations applied in order, all or nothing; the op field selects one of: set_world_param{world,param,value}, remove_world{world}, set_latent{name,spec}, remove_latent{name}, set_behavior{node,spec}, remove_behavior{node}, set_objective{spec}, add_node{id,type,config?,joinMode?}, remove_node{id}, add_edge{from,to,condition?}, remove_edge{from,to}, set_decision{node,options,observes}, set_policy{name,document}, remove_policy{name}"`
}

// ValidateModelInput is the input of the validate_model tool.
type ValidateModelInput struct {
	ID string `json:"id" jsonschema:"model id"`
}

// register adds the model workspace tools (create_model, patch_model,
// validate_model) and the gargi://models/{id} resource to server.
func (s *workspaceServer) register(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_model",
		Description: "Creates a model in the workspace from a template (empty or loan-underwriting). Returns {id, files}.",
	}, s.createModel)
	mcp.AddTool(server, &mcp.Tool{
		Name: "patch_model",
		Description: "Applies typed operations to a workspace model, all or nothing, then validates it. " +
			"Returns {applied, errors: [{path, code, message, hint}]}; validation errors are repaired with further patches.",
	}, s.patchModel)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "validate_model",
		Description: "Validates a workspace model. Returns {errors: [{path, code, message, hint}]}.",
	}, s.validateModel)
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: modelURIPrefix + "{id}",
		Name:        "Workspace model",
		Description: `The rendered documents of a workspace model: {"sim.yaml": ..., "graph.yaml": ..., "policies": {"<name>.yaml": ...}}`,
		MIMEType:    "application/json",
	}, s.readModel)
}
