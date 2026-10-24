package mcp

import (
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

// repairHints maps a validation error code to a one-line hint that tells an
// agent how to repair the model with patch_model.
var repairHints = map[engine.ErrorCode]string{
	engine.CodeUnresolvedReference: "a referenced id does not exist: check the spelling, or add it first (add_node, set_world_param, set_latent, set_policy)",
	sim.CodeObservesNonAncestor:    "a decision can only observe nodes upstream of it: add an edge or change observes with set_decision",
	sim.CodeInvalidObservePath:     "observes entries have the form <node>.output.<field>, for example ai_score.output.band: fix them with set_decision",
	sim.CodeBehaviorMissing:        "effectful nodes need a behavior: use set_behavior",
	sim.CodeBehaviorUnknownNode:    "a behavior names a node the graph does not have: add the node with add_node or drop the behavior with remove_behavior",
	sim.CodeInvalidExpression:      "CEL: world params are world.<name>, latents latent.<name>, node outputs nodes.<id>.output.<field>",
	engine.CodeMissingRequired:     "a required field is missing: read gargi://schemas/sim or gargi://schemas/graph and set it",
	engine.CodeInvalidValue:        "the value is not allowed here: check it against gargi://schemas/sim or gargi://schemas/graph",
	engine.CodeInvalidType:         "the value has the wrong type: check it against gargi://schemas/sim or gargi://schemas/graph",
	engine.CodeUnknownField:        "the field is not part of the schema: remove it or fix its spelling",
	engine.CodeDuplicateID:         "ids must be unique: remove_node the duplicate or pick another id",
	engine.CodeDanglingEdge:        "an edge names a missing node: add the node with add_node or remove the edge with remove_edge",
	engine.CodeCycle:               "the graph must be acyclic: remove an edge of the cycle with remove_edge",
	engine.CodeSelfLoop:            "a node cannot have an edge to itself: remove it with remove_edge",
	engine.CodeUnknownNodeType:     "unknown node type: use one of the registered types (decision, http_call, wait_for_signal, passthrough, ...)",
	engine.CodeInvalidNodeConfig:   "the node's config is invalid for its type: fix it with remove_node and add_node (or set_decision for decisions)",
	engine.CodeInvalidCondition:    "edge conditions are CEL over upstream, for example upstream.output.choice == 'approve'",
	sim.CodePolicyUnknownDecision:  "the policy names a decision the graph does not have: fix it with set_policy",
	sim.CodePolicyMissingDecision:  "the policy must choose for every decision: add the missing one with set_policy",
	sim.CodePolicyUnknownOption:    "the policy chooses an option the decision does not offer: fix it with set_policy or set_decision",
}

// hintFor returns the repair hint of code, or "" for a code without one.
func hintFor(code engine.ErrorCode) string { return repairHints[code] }
