package model

import (
	"testing"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestAgentConfig_SchemaAndParse(t *testing.T) {
	simSchema := readSchemaJSON(t, "../../../schemas/sim/v1/schema.json")
	policySchema := readSchemaJSON(t, "../../../schemas/policy/v1/schema.json")
	simChecker := &conformanceChecker{t: t, root: simSchema}
	policyChecker := &conformanceChecker{t: t, root: policySchema}

	t.Run("valid sim agent config", func(t *testing.T) {
		docYAML := `
apiVersion: sim/v1
id: test-sim
graph: ./graph.yaml
worlds:
  w: {}
behaviors:
  nodeA:
    agent:
      endpoint: "https://agent.example.com/api"
      model: "gpt-4o"
      system_prompt: "You are an assistant"
      headers:
        Authorization: "Bearer secret"
      timeout: "5s"
      max_requests: 10
      max_cost: 1.5
objective:
  name: obj
  maximize: mean
  value: "1.0"
`
		var doc any
		require.NoError(t, yaml.Unmarshal([]byte(docYAML), &doc))
		errs := simChecker.validate(normalizeNumbers(doc), simSchema, "$")
		require.Empty(t, errs)

		sim, err := ParseSimModel([]byte(docYAML), engine.FormatYAML)
		require.NoError(t, err)
		require.NotNil(t, sim.Behaviors["nodeA"].Agent)
		require.Equal(t, "https://agent.example.com/api", sim.Behaviors["nodeA"].Agent.Endpoint)
		require.Equal(t, "gpt-4o", sim.Behaviors["nodeA"].Agent.Model)
		require.Equal(t, 10, sim.Behaviors["nodeA"].Agent.MaxRequests)
		require.InDelta(t, 1.5, sim.Behaviors["nodeA"].Agent.MaxCost, 1e-9)
	})

	t.Run("valid policy agent config", func(t *testing.T) {
		docYAML := `
apiVersion: policy/v1
id: test-policy
version: v1
decisions:
  decide:
    agent:
      endpoint: "https://agent.example.com/decide"
      model: "decision-v1"
`
		var doc any
		require.NoError(t, yaml.Unmarshal([]byte(docYAML), &doc))
		errs := policyChecker.validate(normalizeNumbers(doc), policySchema, "$")
		require.Empty(t, errs)

		pol, err := ParsePolicy([]byte(docYAML), engine.FormatYAML)
		require.NoError(t, err)
		require.NotNil(t, pol.Decisions["decide"].Agent)
		require.Equal(t, "https://agent.example.com/decide", pol.Decisions["decide"].Agent.Endpoint)
	})

	t.Run("invalid endpoint uri", func(t *testing.T) {
		docYAML := `
apiVersion: sim/v1
id: test-sim
graph: ./graph.yaml
worlds:
  w: {}
behaviors:
  nodeA:
    agent:
      endpoint: "invalid-uri"
objective:
  name: obj
  maximize: mean
  value: "1.0"
`
		_, err := ParseSimModel([]byte(docYAML), engine.FormatYAML)
		require.Error(t, err)
		require.Contains(t, err.Error(), "endpoint must be a valid URI")
	})

	t.Run("invalid timeout duration", func(t *testing.T) {
		docYAML := `
apiVersion: sim/v1
id: test-sim
graph: ./graph.yaml
worlds:
  w: {}
behaviors:
  nodeA:
    agent:
      endpoint: "https://agent.example.com/api"
      timeout: "invalid-duration"
objective:
  name: obj
  maximize: mean
  value: "1.0"
`
		_, err := ParseSimModel([]byte(docYAML), engine.FormatYAML)
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid duration format")
	})

	t.Run("invalid max_requests < 1", func(t *testing.T) {
		docYAML := `
apiVersion: sim/v1
id: test-sim
graph: ./graph.yaml
worlds:
  w: {}
behaviors:
  nodeA:
    agent:
      endpoint: "https://agent.example.com/api"
      max_requests: 0
objective:
  name: obj
  maximize: mean
  value: "1.0"
`
		_, err := ParseSimModel([]byte(docYAML), engine.FormatYAML)
		require.Error(t, err)
		require.Contains(t, err.Error(), "max_requests must be >= 1")
	})

	t.Run("invalid max_cost < 0", func(t *testing.T) {
		docYAML := `
apiVersion: sim/v1
id: test-sim
graph: ./graph.yaml
worlds:
  w: {}
behaviors:
  nodeA:
    agent:
      endpoint: "https://agent.example.com/api"
      max_cost: -0.5
objective:
  name: obj
  maximize: mean
  value: "1.0"
`
		_, err := ParseSimModel([]byte(docYAML), engine.FormatYAML)
		require.Error(t, err)
		require.Contains(t, err.Error(), "max_cost must be >= 0")
	})

	t.Run("policy decision mutually exclusive agent and rules", func(t *testing.T) {
		docYAML := `
apiVersion: policy/v1
id: test-policy
version: v1
decisions:
  decide:
    agent:
      endpoint: "https://agent.example.com/decide"
    rules:
      - choose: "opt1"
`
		_, err := ParsePolicy([]byte(docYAML), engine.FormatYAML)
		require.Error(t, err)
		require.Contains(t, err.Error(), "decision must have exactly one of rules, table, or agent")
	})

	t.Run("policy decision agent with default is rejected", func(t *testing.T) {
		docYAML := `
apiVersion: policy/v1
id: test-policy
version: v1
decisions:
  decide:
    agent:
      endpoint: "https://agent.example.com/decide"
    default: "opt1"
`
		_, err := ParsePolicy([]byte(docYAML), engine.FormatYAML)
		require.Error(t, err)
		require.Contains(t, err.Error(), "default is only allowed with table")
	})
}
