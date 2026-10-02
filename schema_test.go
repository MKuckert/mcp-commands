package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseToolArgumentsRejectsDoubleEncodedJSON(t *testing.T) {
	t.Parallel()
	// This test explicitly documents that the fallback double-decode has been removed.
	// parseToolArguments must reject double-encoded JSON strings and only accept
	// proper JSON objects (or empty/null).

	// Create a valid JSON object that will be JSON-encoded as a string
	validObject := `{"key": "value"}`
	doubleEncodedJSON := []byte(`"` + strings.ReplaceAll(validObject, `"`, `\"`) + `"`)

	_, err := parseToolArguments(doubleEncodedJSON)
	if err == nil {
		t.Fatal("expected parseToolArguments to reject double-encoded JSON, but it succeeded")
	}
	if !strings.Contains(err.Error(), "arguments must be a JSON object") {
		t.Fatalf("expected error matching 'arguments must be a JSON object', got %q", err.Error())
	}

	// Verify that a proper JSON object still works
	_, err = parseToolArguments([]byte(`{"key": "value"}`))
	if err != nil {
		t.Fatalf("expected parseToolArguments to accept proper JSON object, got error: %v", err)
	}

	// Verify that empty input still works
	_, err = parseToolArguments([]byte(`{}`))
	if err != nil {
		t.Fatalf("expected parseToolArguments to accept empty object, got error: %v", err)
	}

	// Verify that null still works
	_, err = parseToolArguments([]byte(`null`))
	if err != nil {
		t.Fatalf("expected parseToolArguments to accept null, got error: %v", err)
	}
}

func TestBuildInputSchema(t *testing.T) {
	t.Parallel()
	t.Run("zero_params", func(t *testing.T) {
		schema := buildInputSchema([]paramSpec{})

		var decoded map[string]any
		if err := json.Unmarshal(schema, &decoded); err != nil {
			t.Fatalf("failed to unmarshal schema: %v", err)
		}

		if decoded["type"] != "object" {
			t.Errorf("expected type 'object', got %q", decoded["type"])
		}

		props := decoded["properties"].(map[string]any)
		if len(props) != 0 {
			t.Errorf("expected empty properties, got %d", len(props))
		}

		if _, hasRequired := decoded["required"]; hasRequired {
			t.Error("expected 'required' key to be absent, but it was present")
		}
	})

	t.Run("one_required_string_param", func(t *testing.T) {
		params := []paramSpec{
			{
				Name:        "path",
				Type:        "string",
				Required:    true,
				Description: "Path to the input file",
			},
		}
		schema := buildInputSchema(params)

		var decoded map[string]any
		if err := json.Unmarshal(schema, &decoded); err != nil {
			t.Fatalf("failed to unmarshal schema: %v", err)
		}

		if decoded["type"] != "object" {
			t.Errorf("expected type 'object', got %q", decoded["type"])
		}

		props := decoded["properties"].(map[string]any)
		if len(props) != 1 {
			t.Errorf("expected 1 property, got %d", len(props))
		}

		pathProp := props["path"].(map[string]any)
		if pathProp["type"] != "string" {
			t.Errorf("expected type 'string', got %q", pathProp["type"])
		}
		if pathProp["description"] != "Path to the input file" {
			t.Errorf("expected description 'Path to the input file', got %q", pathProp["description"])
		}

		required := decoded["required"].([]any)
		if len(required) != 1 || required[0] != "path" {
			t.Errorf("expected required=['path'], got %v", required)
		}
	})

	t.Run("mix_required_and_optional", func(t *testing.T) {
		params := []paramSpec{
			{
				Name:        "path",
				Type:        "string",
				Required:    true,
				Description: "Path to the input file",
			},
			{
				Name:        "verbose",
				Type:        "boolean",
				Required:    false,
				Description: "Print progress",
			},
		}
		schema := buildInputSchema(params)

		var decoded map[string]any
		if err := json.Unmarshal(schema, &decoded); err != nil {
			t.Fatalf("failed to unmarshal schema: %v", err)
		}

		props := decoded["properties"].(map[string]any)
		if len(props) != 2 {
			t.Errorf("expected 2 properties, got %d", len(props))
		}

		required := decoded["required"].([]any)
		if len(required) != 1 || required[0] != "path" {
			t.Errorf("expected required=['path'], got %v", required)
		}
	})

	t.Run("duplicate_param_names_last_wins", func(t *testing.T) {
		params := []paramSpec{
			{
				Name:        "flag",
				Type:        "string",
				Required:    true,
				Description: "First declaration",
			},
			{
				Name:        "flag",
				Type:        "boolean",
				Required:    false,
				Description: "Last declaration",
			},
		}
		schema := buildInputSchema(params)

		var decoded map[string]any
		if err := json.Unmarshal(schema, &decoded); err != nil {
			t.Fatalf("failed to unmarshal schema: %v", err)
		}

		props := decoded["properties"].(map[string]any)
		if len(props) != 1 {
			t.Errorf("expected 1 property (last wins), got %d", len(props))
		}

		flagProp := props["flag"].(map[string]any)
		if flagProp["type"] != "boolean" {
			t.Errorf("expected type 'boolean' (last declaration), got %q", flagProp["type"])
		}
		if flagProp["description"] != "Last declaration" {
			t.Errorf("expected description 'Last declaration', got %q", flagProp["description"])
		}

		// Required should reflect the last declaration (false)
		if _, hasRequired := decoded["required"]; hasRequired {
			t.Error("expected 'required' key to be absent (last declaration is optional)")
		}
	})

	t.Run("all_three_types", func(t *testing.T) {
		params := []paramSpec{
			{
				Name:        "path",
				Type:        "string",
				Required:    true,
				Description: "Input path",
			},
			{
				Name:        "dpi",
				Type:        "number",
				Required:    false,
				Description: "DPI value",
			},
			{
				Name:        "verbose",
				Type:        "boolean",
				Required:    true,
				Description: "Verbose mode",
			},
		}
		schema := buildInputSchema(params)

		var decoded map[string]any
		if err := json.Unmarshal(schema, &decoded); err != nil {
			t.Fatalf("failed to unmarshal schema: %v", err)
		}

		props := decoded["properties"].(map[string]any)
		if len(props) != 3 {
			t.Errorf("expected 3 properties, got %d", len(props))
		}

		// Check types
		if props["path"].(map[string]any)["type"] != "string" {
			t.Error("path type mismatch")
		}
		if props["dpi"].(map[string]any)["type"] != "number" {
			t.Error("dpi type mismatch")
		}
		if props["verbose"].(map[string]any)["type"] != "boolean" {
			t.Error("verbose type mismatch")
		}

		// Check required
		required := decoded["required"].([]any)
		requiredSet := make(map[string]bool)
		for _, r := range required {
			requiredSet[r.(string)] = true
		}
		if !requiredSet["path"] {
			t.Error("path should be required")
		}
		if requiredSet["dpi"] {
			t.Error("dpi should not be required")
		}
		if !requiredSet["verbose"] {
			t.Error("verbose should be required")
		}
	})
}
