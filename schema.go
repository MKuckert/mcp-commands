package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
)

// parseToolArguments unmarshals the JSON arguments provided by the MCP client
// into a Go map. It handles empty or null payloads by returning an empty map,
// parseToolArguments unmarshals the JSON arguments provided by the MCP client
// into a Go map. It handles empty or null payloads by returning an empty map,
// preventing unmarshal errors when tools are called without arguments.
func parseToolArguments(raw json.RawMessage) (map[string]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return map[string]any{}, nil
	}

	var args map[string]any
	if err := json.Unmarshal(trimmed, &args); err != nil {
		return nil, fmt.Errorf("arguments must be a JSON object")
	}

	return args, nil
}

var argumentKeyPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

// buildInputSchema constructs a JSON Schema for a tool's input parameters.
// It takes a slice of paramSpec and builds a schema with a "properties" object
// and a "required" array (omitted if empty). Duplicate param names are deduplicated:
// the last declaration wins for both properties and required status.
//
// Returns a json.RawMessage containing:
//
//	{"type":"object","properties":{...},"required":[...]}
//
// buildInputSchema constructs a JSON Schema for a tool's input parameters.
// It takes a slice of paramSpec and builds a schema with a "properties" object
// and a "required" array (omitted if empty). Duplicate param names are deduplicated:
// the last declaration wins for both properties and required status.
//
// Returns a json.RawMessage containing:
//
//	{"type":"object","properties":{...},"required":[...]}
//
// The "required" key is omitted entirely if no params are required.
func buildInputSchema(params []paramSpec) json.RawMessage {
	properties := make(map[string]any)
	lastRequired := make(map[string]bool)

	// First pass: build properties map and track last-seen required status
	for _, param := range params {
		properties[param.Name] = map[string]any{
			"type":        param.Type,
			"description": param.Description,
		}
		lastRequired[param.Name] = param.Required
	}

	// Build the schema object
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}

	// Second pass: collect required names (using last-seen required status)
	var requiredNames []string
	seen := make(map[string]bool)
	for _, param := range params {
		if !seen[param.Name] && lastRequired[param.Name] {
			requiredNames = append(requiredNames, param.Name)
			seen[param.Name] = true
		}
	}

	// Only add "required" key if there are required params
	if len(requiredNames) > 0 {
		schema["required"] = requiredNames
	}

	return mustJSONMarshal(schema)
}

func mustJSONMarshal(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
