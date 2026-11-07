package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed config.schema.json
var configFileSchemaDoc []byte

// maxConfigFileBytes bounds the config-file read: a config file is small, so
// anything larger is almost certainly a wrong path (same LimitReader pattern
// as --api-key-file).
const maxConfigFileBytes = 1 << 20 // 1 MiB

// fileConfig is the decoded config file. Pointer fields distinguish "key
// absent" from "key present with a zero value": key presence is the
// explicit-set marker that drives precedence (flag > env > file > default).
type fileConfig struct {
	Dir      *string           `json:"dir"`
	Scripts  *string           `json:"scripts"`
	Watch    *bool             `json:"watch"`
	LogLevel *string           `json:"log-level"`
	Timeout  *string           `json:"timeout"`
	Server   *fileServerConfig `json:"server"` // nil = key absent (or no server section)
}

// fileServerConfig is the "server" object of the config file. Pointer fields
// distinguish "key absent" from "key present with a zero value", exactly like
// the top level: a present-but-false/empty-meaning key (e.g.
// "allow-all-origins": false, "allowed-origins": []) is an explicit choice
// that suppresses the env-var fallback.
type fileServerConfig struct {
	Host                       *string   `json:"host"`
	Port                       *int      `json:"port"`
	APIKey                     *string   `json:"api-key"`
	APIKeyFile                 *string   `json:"api-key-file"`
	TLSCert                    *string   `json:"tls-cert"`
	TLSKey                     *string   `json:"tls-key"`
	InsecureNoAuth             *bool     `json:"insecure-no-auth"`
	MaxConcurrent              *int      `json:"max-concurrent"`
	AllowedOrigins             *[]string `json:"allowed-origins"`
	AllowAllOrigins            *bool     `json:"allow-all-origins"`
	DisableLocalhostProtection *bool     `json:"disable-localhost-protection"`
}

// configSchemaURL is the resource name under which the embedded schema is
// registered with the compiler; the document itself is embedded, so no
// network or filesystem lookup happens at runtime.
const configSchemaURL = "https://mcp-commands.local/config.schema.json"

// configSchema is the embedded config.schema.json compiled once at package
// init. A malformed built-in schema is a build-time defect, not runtime
// input: fail loudly at startup rather than skip validation.
var configSchema = compileConfigSchema()

func compileConfigSchema() *jsonschema.Schema {
	var document any
	if err := json.Unmarshal(configFileSchemaDoc, &document); err != nil {
		panic(fmt.Sprintf("embedded config.schema.json is not valid JSON: %v", err))
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(configSchemaURL, document); err != nil {
		panic(fmt.Sprintf("embedded config.schema.json: %v", err))
	}
	schema, err := compiler.Compile(configSchemaURL)
	if err != nil {
		panic(fmt.Sprintf("embedded config.schema.json does not compile: %v", err))
	}
	return schema
}

// loadConfigFile reads and validates the --config file: extension dispatch
// (.json strict / .jsonc with comments), size cap, strict single-document
// decode, and validation against the embedded schema (unknown keys and wrong
// types are rejected). Value semantics (duration format, log-level set) are
// validated by the same resolvers the flags use, so one error format covers
// every source.
func loadConfigFile(path string) (*fileConfig, error) {
	jsonc := false
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
	case ".jsonc":
		jsonc = true
	default:
		return nil, fmt.Errorf("config file %s: must have a .json or .jsonc extension", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read config file %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxConfigFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("cannot read config file %s: %w", path, err)
	}
	if len(data) > maxConfigFileBytes {
		return nil, fmt.Errorf("config file %s exceeds the %d-byte maximum (wrong file?)", path, maxConfigFileBytes)
	}
	if jsonc {
		data, err = stripJSONCComments(data)
		if err != nil {
			return nil, fmt.Errorf("config file %s: %w", path, err)
		}
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("config file %s: invalid JSON: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("config file %s: unexpected content after the JSON document", path)
	}
	if err := configSchema.Validate(document); err != nil {
		return nil, fmt.Errorf("config file %s: invalid configuration: %w", path, err)
	}
	// Decode the struct from the validated document, not the raw bytes: the
	// map decode above already collapsed duplicate keys (last wins), so the
	// applied config can never retain a value that is absent from the
	// validated document (a struct decode of the raw bytes would merge
	// duplicate objects and keep earlier keys).
	validated, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("config file %s: %w", path, err)
	}
	var cfg fileConfig
	if err := json.Unmarshal(validated, &cfg); err != nil {
		return nil, fmt.Errorf("config file %s: %w", path, err)
	}
	return &cfg, nil
}

// stripJSONCComments removes // line comments and /* block */ comments from
// JSONC content. Comment bytes are replaced with spaces (newlines kept) so
// byte offsets in later parse errors still point at the original file.
// Comment sequences inside string literals are untouched.
func stripJSONCComments(raw []byte) ([]byte, error) {
	out := make([]byte, len(raw))
	inString := false
	escaped := false
	for i := 0; i < len(raw); {
		c := raw[i]
		if inString {
			out[i] = c
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			i++
			continue
		}
		switch {
		case c == '"':
			inString = true
			out[i] = c
			i++
		case c == '/' && i+1 < len(raw) && raw[i+1] == '/':
			// Line comment: space out until the newline; the newline itself
			// falls through to the next iteration and is preserved.
			for i < len(raw) && raw[i] != '\n' {
				out[i] = ' '
				i++
			}
		case c == '/' && i+1 < len(raw) && raw[i+1] == '*':
			out[i], out[i+1] = ' ', ' '
			i += 2
			closed := false
			for i < len(raw) {
				if raw[i] == '*' && i+1 < len(raw) && raw[i+1] == '/' {
					out[i], out[i+1] = ' ', ' '
					i += 2
					closed = true
					break
				}
				if raw[i] == '\n' {
					out[i] = '\n' // keep line structure for error offsets
				} else {
					out[i] = ' '
				}
				i++
			}
			if !closed {
				return nil, errors.New("unterminated /* comment")
			}
		default:
			out[i] = c
			i++
		}
	}
	return out, nil
}
