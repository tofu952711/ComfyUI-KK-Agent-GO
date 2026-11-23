package tool

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
)

// Validate implements the JSON Schema subset used by the existing media tools.
// Unknown schema assertions fail explicitly rather than silently skipping validation.
func Validate(schema json.RawMessage, args json.RawMessage) error {
	var s map[string]any
	var v any
	if e := json.Unmarshal(schema, &s); e != nil {
		return fmt.Errorf("invalid tool schema")
	}
	if e := json.Unmarshal(args, &v); e != nil {
		return fmt.Errorf("invalid tool JSON")
	}
	return validate(s, v, "arguments")
}
func validate(s map[string]any, v any, path string) error {
	for key := range s {
		switch key {
		case "$schema", "title", "description", "default", "examples", "type", "properties", "required", "additionalProperties", "items", "minItems", "maxItems", "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "enum", "const", "minLength", "maxLength":
		default:
			return fmt.Errorf("unsupported tool schema keyword %s", key)
		}
	}
	if options, ok := s["enum"].([]any); ok {
		found := false
		for _, o := range options {
			if reflect.DeepEqual(o, v) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%s is outside enum", path)
		}
	}
	if expected, ok := s["const"]; ok && !reflect.DeepEqual(expected, v) {
		return fmt.Errorf("%s violates const", path)
	}
	switch s["type"] {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		properties, _ := s["properties"].(map[string]any)
		for _, key := range asArray(s["required"]) {
			keyString, ok := key.(string)
			if !ok {
				return fmt.Errorf("invalid required schema")
			}
			if _, ok := m[keyString]; !ok {
				return fmt.Errorf("%s.%s is required", path, key)
			}
		}
		for k, value := range m {
			if raw, ok := properties[k]; ok {
				child, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("invalid property schema")
				}
				if e := validate(child, value, path+"."+k); e != nil {
					return e
				}
			} else if allowed, ok := s["additionalProperties"].(bool); ok && !allowed {
				return fmt.Errorf("%s.%s is not allowed", path, k)
			}
		}
	case "array":
		a, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", path)
		}
		if e := bounds(s, float64(len(a)), "minItems", "maxItems", path); e != nil {
			return e
		}
		if item, ok := s["items"].(map[string]any); ok {
			for i, value := range a {
				if e := validate(item, value, fmt.Sprintf("%s[%d]", path, i)); e != nil {
					return e
				}
			}
		}
	case "number", "integer":
		n, ok := v.(float64)
		if !ok || math.IsInf(n, 0) || math.IsNaN(n) {
			return fmt.Errorf("%s must be numeric", path)
		}
		if s["type"] == "integer" && math.Trunc(n) != n {
			return fmt.Errorf("%s must be an integer", path)
		}
		if e := bounds(s, n, "minimum", "maximum", path); e != nil {
			return e
		}
		if min, ok := s["exclusiveMinimum"].(float64); ok && n <= min {
			return fmt.Errorf("%s below exclusive minimum", path)
		}
		if max, ok := s["exclusiveMaximum"].(float64); ok && n >= max {
			return fmt.Errorf("%s above exclusive maximum", path)
		}
	case "string":
		value, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", path)
		}
		return bounds(s, float64(len([]rune(value))), "minLength", "maxLength", path)
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%s must be boolean", path)
		}
	case "null":
		if v != nil {
			return fmt.Errorf("%s must be null", path)
		}
	case nil:
	default:
		return fmt.Errorf("unsupported schema type")
	}
	return nil
}
func asArray(v any) []any { a, _ := v.([]any); return a }
func bounds(s map[string]any, value float64, minKey, maxKey, path string) error {
	if min, ok := s[minKey].(float64); ok && value < min {
		return fmt.Errorf("%s below %s", path, minKey)
	}
	if max, ok := s[maxKey].(float64); ok && value > max {
		return fmt.Errorf("%s above %s", path, maxKey)
	}
	return nil
}
