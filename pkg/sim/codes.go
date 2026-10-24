package sim

import "github.com/gargi-indic/gargi-flow/pkg/engine"

// Simulation Engine codes represent public evaluation identifiers for engine checks.
const (
	CodeInvalidObservePath        engine.ErrorCode = "invalid_observe_path"
	CodeObservesNonAncestor       engine.ErrorCode = "observes_non_ancestor"
	CodeBehaviorMissing           engine.ErrorCode = "behavior_missing"
	CodeBehaviorUnknownNode       engine.ErrorCode = "behavior_unknown_node"
	CodeInvalidExpression         engine.ErrorCode = "invalid_expression"
	CodePolicyUnobservedReference engine.ErrorCode = "policy_unobserved_reference"
	CodePolicyUnknownOption       engine.ErrorCode = "policy_unknown_option"
	CodePolicyMissingDecision     engine.ErrorCode = "policy_missing_decision"
	CodePolicyUnknownDecision     engine.ErrorCode = "policy_unknown_decision"
	CodeModelNotDiscrete          engine.ErrorCode = "model_not_discrete"
	CodeTooManyPolicies           engine.ErrorCode = "too_many_policies"
	// CodeNotPerfectRecall reports a decision whose information sets do not
	// remember the earlier decisions of the episode, which the exact
	// (backward-induction) policy search requires.
	CodeNotPerfectRecall engine.ErrorCode = "not_perfect_recall"
	// CodePolicyUnknownParam reports a reference to, or a binding of, a
	// policy param that the policy does not declare.
	CodePolicyUnknownParam engine.ErrorCode = "policy_unknown_param"
	// CodeParamOutOfRange reports a param value outside the param's
	// [min, max] range or not one of its values.
	CodeParamOutOfRange engine.ErrorCode = "param_out_of_range"
	// CodeUnresolvedReference reports a reference to an unknown entity.
	CodeUnresolvedReference engine.ErrorCode = "unresolved_reference"
)
