package mcp

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolErrorResult represents the structure of tool error JSON output.
type toolErrorResult struct {
	Errors []toolErrorIssue `json:"errors"`
}

type toolErrorIssue struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// toolError creates an *mcp.CallToolResult with IsError true, formatting the
// validation issues into JSON text sorted by path, code, and message.
func toolError(issues []engine.ValidationIssue, hint func(engine.ValidationIssue) string) *mcp.CallToolResult {
	if hint == nil {
		hint = func(_ engine.ValidationIssue) string { return "" }
	}

	errs := make([]toolErrorIssue, len(issues))
	for i, issue := range issues {
		errs[i] = toolErrorIssue{
			Path:    issue.Path,
			Code:    string(issue.Code),
			Message: issue.Message,
			Hint:    hint(issue),
		}
	}

	sort.SliceStable(errs, func(i, j int) bool {
		if errs[i].Path != errs[j].Path {
			return errs[i].Path < errs[j].Path
		}
		if errs[i].Code != errs[j].Code {
			return errs[i].Code < errs[j].Code
		}
		return errs[i].Message < errs[j].Message
	})

	b, _ := json.Marshal(toolErrorResult{Errors: errs})

	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{
			&mcp.TextContent{
				Text: string(b),
			},
		},
	}
}

// toolErrorf returns a tool error with a single formatted message using the
// engine.ErrorCode format.
func toolErrorf(code engine.ErrorCode, format string, args ...any) *mcp.CallToolResult {
	return toolError([]engine.ValidationIssue{
		{
			Path:    engine.PathRoot.String(),
			Code:    code,
			Message: fmt.Sprintf(format, args...),
		},
	}, nil)
}
