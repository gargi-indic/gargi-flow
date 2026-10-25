package model

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func normalizeNumbers(v any) any {
	switch val := v.(type) {
	case map[string]any:
		res := make(map[string]any)
		for k, v := range val {
			res[k] = normalizeNumbers(v)
		}
		return res
	case map[any]any:
		res := make(map[string]any)
		for k, v := range val {
			res[fmt.Sprintf("%v", k)] = normalizeNumbers(v)
		}
		return res
	case []any:
		res := make([]any, len(val))
		for i, v := range val {
			res[i] = normalizeNumbers(v)
		}
		return res
	case int:
		return float64(val)
	case int64:
		return float64(val)
	default:
		return val
	}
}

func readSchemaJSON(t *testing.T, path string) map[string]any {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	var schema map[string]any
	if err := json.Unmarshal(b, &schema); err != nil {
		t.Fatalf("failed to parse schema %s: %v", path, err)
	}
	return schema
}

func readYAML(t *testing.T, path string) any {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	var doc any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("failed to parse yaml %s: %v", path, err)
	}
	return normalizeNumbers(doc)
}

type conformanceChecker struct {
	t    *testing.T
	root map[string]any
}

func (c *conformanceChecker) validate(doc any, schema map[string]any, path string) []string {
	var errs []string

	if ref, ok := schema["$ref"].(string); ok {
		if !strings.HasPrefix(ref, "#/") {
			return []string{fmt.Sprintf("%s: unsupported ref %s", path, ref)}
		}
		parts := strings.Split(ref[2:], "/")
		resolved := c.root
		for _, part := range parts {
			if next, ok := resolved[part].(map[string]any); ok {
				resolved = next
			} else {
				return []string{fmt.Sprintf("%s: failed to resolve ref %s", path, ref)}
			}
		}
		return c.validate(doc, resolved, path)
	}

	if oneOf, ok := schema["oneOf"].([]any); ok {
		matchCount := 0
		var allErrs [][]string
		for _, s := range oneOf {
			if sm, ok := s.(map[string]any); ok {
				subErrs := c.validate(doc, sm, path)
				if len(subErrs) == 0 {
					matchCount++
				} else {
					allErrs = append(allErrs, subErrs)
				}
			}
		}
		if matchCount != 1 {
			return append(errs, fmt.Sprintf("%s: oneOf must match exactly one, but matched %d. Errors: %v", path, matchCount, allErrs))
		}
	}

	typ, ok := schema["type"].(string)
	if !ok && (schema["properties"] != nil || schema["required"] != nil) {
		typ = "object"
		ok = true
	}
	if ok {
		switch typ {
		case "object":
			if doc == nil {
				return errs
			}
			obj, ok := doc.(map[string]any)
			if !ok {
				return append(errs, fmt.Sprintf("%s: expected object, got %T", path, doc))
			}

			reqs, _ := schema["required"].([]any)
			for _, r := range reqs {
				reqStr := r.(string)
				if _, ok := obj[reqStr]; !ok {
					errs = append(errs, fmt.Sprintf("%s: missing required property %s", path, reqStr))
				}
			}

			addlPropsAllowed := true
			if ap, ok := schema["additionalProperties"].(bool); ok {
				addlPropsAllowed = ap
			}

			props, _ := schema["properties"].(map[string]any)
			patternProps, hasPatternProps := schema["patternProperties"].(map[string]any)

			for k, v := range obj {
				matched := false
				if props != nil {
					if propSchema, ok := props[k].(map[string]any); ok {
						matched = true
						errs = append(errs, c.validate(v, propSchema, path+"."+k)...)
					}
				}
				if !matched && hasPatternProps {
					for pat, patSchemaVal := range patternProps {
						matchedRegex, _ := regexp.MatchString(pat, k)
						if matchedRegex {
							matched = true
							if patSchema, ok := patSchemaVal.(map[string]any); ok {
								errs = append(errs, c.validate(v, patSchema, path+"."+k)...)
							}
						}
					}
				}
				if !matched && !addlPropsAllowed {
					errs = append(errs, fmt.Sprintf("%s: additional property not allowed: %s", path, k))
				}
			}

		case "array":
			if doc == nil {
				return errs
			}
			arr, ok := doc.([]any)
			if !ok {
				return append(errs, fmt.Sprintf("%s: expected array, got %T", path, doc))
			}
			if items, ok := schema["items"].(map[string]any); ok {
				for i, v := range arr {
					errs = append(errs, c.validate(v, items, fmt.Sprintf("%s[%d]", path, i))...)
				}
			}
		case "string":
			if doc == nil {
				return errs
			}
			strVal, ok := doc.(string)
			if !ok {
				return append(errs, fmt.Sprintf("%s: expected string, got %T", path, doc))
			}

			if enum, ok := schema["enum"].([]any); ok {
				matched := false
				for _, e := range enum {
					if e == strVal {
						matched = true
						break
					}
				}
				if !matched {
					errs = append(errs, fmt.Sprintf("%s: value %v not in enum %v", path, strVal, enum))
				}
			}
			if pat, ok := schema["pattern"].(string); ok {
				if matched, _ := regexp.MatchString(pat, strVal); !matched {
					errs = append(errs, fmt.Sprintf("%s: string does not match pattern %s", path, pat))
				}
			}

		case "boolean":
			if doc == nil {
				return errs
			}
			if _, ok := doc.(bool); !ok {
				return append(errs, fmt.Sprintf("%s: expected boolean, got %T", path, doc))
			}

		case "integer":
			if doc == nil {
				return errs
			}
			num, ok := doc.(float64)
			if !ok {
				return append(errs, fmt.Sprintf("%s: expected integer, got %T", path, doc))
			}
			if float64(int(num)) != num {
				errs = append(errs, fmt.Sprintf("%s: expected integer, got float", path))
			}
			if minVal, ok := schema["minimum"].(float64); ok {
				if num < minVal {
					errs = append(errs, fmt.Sprintf("%s: value %v < minimum %v", path, num, minVal))
				}
			}

		case "number":
			if doc == nil {
				return errs
			}
			num, ok := doc.(float64)
			if !ok {
				return append(errs, fmt.Sprintf("%s: expected number, got %T", path, doc))
			}
			if minVal, ok := schema["minimum"].(float64); ok {
				if num < minVal {
					errs = append(errs, fmt.Sprintf("%s: value %v < minimum %v", path, num, minVal))
				}
			}
		}
	} else if typeList, ok := schema["type"].([]any); ok {
		matched := false
		for _, tAny := range typeList {
			if tStr, ok := tAny.(string); ok {
				dummySchema := make(map[string]any)
				for k, v := range schema {
					if k != "type" {
						dummySchema[k] = v
					}
				}
				dummySchema["type"] = tStr
				subErrs := c.validate(doc, dummySchema, path)
				if len(subErrs) == 0 {
					matched = true
					break
				}
			}
		}
		if !matched {
			return append(errs, fmt.Sprintf("%s: does not match any type in %v", path, typeList))
		}
	}

	if constVal, ok := schema["const"]; ok {
		if doc != constVal {
			errs = append(errs, fmt.Sprintf("%s: value %v does not match const %v", path, doc, constVal))
		}
	}

	return errs
}

func TestSchema_Examples(t *testing.T) {
	tests := []struct {
		schemaPath string
		docs       []string
	}{
		{
			schemaPath: "../../../schemas/sim/v1/schema.json",
			docs: []string{
				"../../../schemas/sim/v1/example.yaml",
				"../../../examples/simulation/loan/sim.yaml",
				"../../../examples/simulation/claims/sim.yaml",
			},
		},
		{
			schemaPath: "../../../schemas/policy/v1/schema.json",
			docs: []string{
				"../../../schemas/policy/v1/example.yaml",
				"../../../examples/simulation/loan/policies/recession-v1.yaml",
				"../experiment/testdata/threshold/policies/theta.yaml",
				"../../../examples/simulation/claims/policies/rules.yaml",
				"../../../examples/simulation/claims/policies/matrix.yaml",
				"../../../examples/simulation/claims/policies/agent.yaml",
			},
		},
		{
			schemaPath: "../../../schemas/experiment/v1/schema.json",
			docs: []string{
				"../../../schemas/experiment/v1/example.yaml",
				"../../../examples/simulation/loan/experiment.yaml",
				"../experiment/testdata/threshold/experiment.yaml",
				"../../../examples/simulation/claims/experiment.yaml",
			},
		},
	}

	for _, tt := range tests {
		t.Run(filepath.Base(filepath.Dir(tt.schemaPath)), func(t *testing.T) {
			schema := readSchemaJSON(t, tt.schemaPath)
			checker := &conformanceChecker{t: t, root: schema}

			for _, docPath := range tt.docs {
				t.Run(filepath.Base(docPath), func(t *testing.T) {
					doc := readYAML(t, docPath)
					errs := checker.validate(doc, schema, "$")
					if len(errs) > 0 {
						for _, e := range errs {
							t.Errorf("Validation error: %s", e)
						}
					}
				})
			}
		})
	}
}
