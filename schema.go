package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// parseToolArguments unmarshals the JSON arguments provided by the MCP client
// into a Go map. It handles empty or null payloads by returning an empty map,
// preventing unmarshal errors when tools are called without arguments.
func parseToolArguments(raw json.RawMessage) (map[string]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return map[string]any{}, nil
	}

	var args map[string]any
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&args); err != nil || args == nil {
		return nil, fmt.Errorf("arguments must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("arguments must be a single JSON object")
	}

	return args, nil
}

var argumentKeyPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

// normalizeParams keeps each parameter's first position and last declaration.
// Both the published schema and the readable list use the same normalized set.
func normalizeParams(params []paramSpec) []paramSpec {
	out := make([]paramSpec, 0, len(params))
	positions := make(map[string]int, len(params))
	for _, param := range params {
		if pos, ok := positions[param.Name]; ok {
			out[pos] = param
		} else {
			positions[param.Name] = len(out)
			out = append(out, param)
		}
	}
	return out
}

// buildInputSchema constructs a JSON Schema for a tool's input parameters.
// It takes a slice of paramSpec and builds a schema with a "properties" object
// and a "required" array (omitted if empty). Duplicate param names are deduplicated:
// the last declaration wins for both properties and required status.
//
// Returns a json.RawMessage containing:
//
//	{"type":"object","properties":{...},"additionalProperties":false,"required":[...]}
//
// The "required" key is omitted entirely if no params are required.
func buildInputSchema(params []paramSpec) json.RawMessage {
	properties := make(map[string]any, len(params))
	var requiredNames []string
	for _, param := range normalizeParams(params) {
		properties[param.Name] = map[string]any{
			"type":        param.Type,
			"description": param.Description,
		}
		if param.Required {
			requiredNames = append(requiredNames, param.Name)
		}
	}

	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}

	// Only add "required" key if there are required params
	if len(requiredNames) > 0 {
		schema["required"] = requiredNames
	}

	return mustJSONMarshal(schema)
}

// resolveInputSchema validates the exact schema advertised to MCP clients.
func resolveInputSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	const resource = "https://mcp-commands.local/tool-input"
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("decode tool input schema: %w", err)
	}
	if err := compiler.AddResource(resource, document); err != nil {
		return nil, fmt.Errorf("add tool input schema: %w", err)
	}
	resolved, err := compiler.Compile(resource)
	if err != nil {
		return nil, fmt.Errorf("compile tool input schema: %w", err)
	}
	return resolved, nil
}

func validateToolArguments(args map[string]any, schema *jsonschema.Schema) error {
	if err := schema.Validate(args); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	return nil
}

func mustJSONMarshal(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
