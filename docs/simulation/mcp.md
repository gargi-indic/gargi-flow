# Model Context Protocol (MCP) Server

`gargi` includes a Model Context Protocol (MCP) server (built with official Go SDK `github.com/modelcontextprotocol/go-sdk` v1.8.0) that lets AI agents (such as Claude Code) iteratively create, repair, simulate, and optimize decision models over stdio or HTTP.

## Starting the Server

```bash
# Stdio mode (for desktop/CLI agent integration)
gargi mcp serve --stdio

# HTTP mode (for remote/container agent access)
gargi mcp serve --http :8080
```

## Claude Code Configuration

To add `gargi` to Claude Code via CLI:

```bash
claude mcp add gargi -- gargi mcp serve --stdio
```

Or add it to your project-scoped `.mcp.json`:

```json
{
  "mcpServers": {
    "gargi": {
      "command": "gargi",
      "args": ["mcp", "serve", "--stdio"]
    }
  }
}
```

## Tools

The server exposes the following tools:

### Schema & Templates
- **`get_schema`** `{kind}`: Returns the JSON Schema text for `sim`, `graph`, `policy`, or `experiment`.
- **`list_templates`**: Returns a list of starter templates (`empty`, `loan-underwriting`).

### Model Workspace & The Repair Loop
Each MCP server instance maintains an isolated in-memory model workspace. Agents interact with models through a structured repair loop:

1. **`create_model`** `{id, template}`: Initializes a model from a template (`empty` or `loan-underwriting`).
2. **`patch_model`** `{id, ops}`: Applies an atomic batch of operations to the model. The `op` field selects:
   - `set_world_param`, `remove_world`
   - `set_latent`, `remove_latent`
   - `set_behavior`, `remove_behavior`
   - `set_objective`
   - `add_node`, `remove_node`
   - `add_edge`, `remove_edge`
   - `set_decision`
   - `set_policy`, `remove_policy`
   Returns any validation errors along with targeted `hint` strings.
3. **`validate_model`** `{id}`: Validates the model and returns all errors with actionable repair hints.

Agents repeat `patch_model` using the returned hints until no validation errors remain.

### Simulation & Analysis
- **`run_experiment`** `{model_id, experiment}`: Validates the workspace model and runs the experiment/v1 YAML definition against it. Returns experiment ID and per-world best policy summaries.
- **`get_experiment`** `{id}`: Returns the summary of an experiment by ID.
- **`get_report`** `{id, kind}`: Returns report data (`summary`, `regret`, `robust`, `pareto`, `voi`, `flipPoints`, `pathHeatmap`, `policyTree`).
- **`explain_policy`** `{model_id, policy}`: Generates a plain-text human-readable explanation of a policy document.
- **`compare_policies`** `{id, world?, top?}`: Compares the top policies for a given world, including expected value, confidence intervals, and regret.
- **`sweep_parameter`** `{model_id, world, param, values: [...], experiment}`: Evaluates sensitivity by sweeping a world parameter over a list of values on a copy of the model without mutating the workspace.
- **`deploy_policy`** & **`ingest_outcome`**: Stubs reserved for P3 (Epic #159); `deploy_policy` is annotated with `destructiveHint: true`.

## Prompts

- **`model_this_decision`**: Takes a natural language decision `description` and guides the agent step-by-step through template selection, iterative model patching and validation, experiment execution, and policy explanation.
