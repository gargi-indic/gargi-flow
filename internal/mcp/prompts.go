package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ModelThisDecisionInput describes the arguments for model_this_decision.
type ModelThisDecisionInput struct {
	Description string `json:"description" jsonschema:"description of the decision to model"`
}

func registerPrompts(server *mcp.Server) {
	server.AddPrompt(&mcp.Prompt{
		Name:        "model_this_decision",
		Description: "Guides the agent to model a decision using the workspace tools.",
		Arguments: []*mcp.PromptArgument{
			{
				Name:        "description",
				Description: "description of the decision to model",
				Required:    true,
			},
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		description := ""
		if req != nil && req.Params != nil && req.Params.Arguments != nil {
			if arg, ok := req.Params.Arguments["description"]; ok {
				description = arg
			}
		}

		prompt := `You are going to model the following decision:
` + description + `

Here is your workflow:
1. Call list_templates to see available templates.
2. Call create_model to create a model from the "empty" template.
3. Call patch_model to add worlds, latents, behaviors, nodes, edges, decisions, and objective to the model.
   - You can use CEL variables like: world.<p>, latent.<n>, nodes.<id>.output.<f>, run.cost
4. Call validate_model to check for errors, repairing with the hints until there are no errors.
5. Call run_experiment with evaluator: exact and policies: {search: exact} for discrete models.
6. Call get_report and/or compare_policies to view results.
7. Call explain_policy to get a plain text explanation of the resulting policy.
`
		return &mcp.GetPromptResult{
			Description: "Guide to model a decision",
			Messages: []*mcp.PromptMessage{
				{
					Role: mcp.Role("user"),
					Content: &mcp.TextContent{
						Text: prompt,
					},
				},
			},
		}, nil
	})
}
