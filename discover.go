package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	scanHeaderLines       = 30
	scanDescriptionPrefix = "Description:"
	scanParamPrefix       = "Param:"
	scanTimeoutPrefix     = "Timeout:"
)

type paramSpec struct {
	Name        string // validated against argumentKeyPattern
	Type        string // "string" | "number" | "boolean"
	Required    bool
	Description string
}

type discoveredTool struct {
	Name        string
	Path        string
	Description string
	Params      []paramSpec
	Timeout     *time.Duration // valid per-tool Timeout: value; nil when undeclared (global applies), &0 for NONE/0
}

// discoverTools scans the given directory for executable files and symlinks
// resolving to executables. It skips subdirectories and non-executable files.
// For each valid executable, it extracts the description and parameters, then
// discoverTools scans the given directory for executable files and symlinks
// resolving to executables. It skips subdirectories and non-executable files.
// For each valid executable, it extracts the description and parameters, then
// constructs a discoveredTool record for later registration with the MCP server.
func discoverTools(scriptsDir string, stderr io.Writer) ([]discoveredTool, error) {
	entries, err := os.ReadDir(scriptsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read scripts directory: %w", err)
	}

	var tools []discoveredTool
	registeredNames := make(map[string]string) // tool name -> file that registered it

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filePath := filepath.Join(scriptsDir, entry.Name())
		resolvedPath, err := filepath.EvalSymlinks(filePath)
		if err != nil {
			continue
		}

		fileInfo, err := os.Stat(resolvedPath)
		if err != nil {
			continue
		}

		// Check executable flag
		if fileInfo.Mode()&0111 == 0 {
			continue
		}

		description, params, timeout := extractFrontmatter(resolvedPath, stderr)
		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))

		// Name collisions (a.sh and a.py both register as "a") are first-
		// wins in ReadDir (i.e. filename) order, with a loud warning — the
		// duplicate would be silently shadowed without it.
		if firstFile, dup := registeredNames[name]; dup {
			fmt.Fprintf(stderr, "Warning: ignoring %s: tool name %q already registered by %s\n", entry.Name(), name, firstFile)
			continue
		}
		registeredNames[name] = entry.Name()

		tools = append(tools, discoveredTool{
			Name:        name,
			Path:        resolvedPath,
			Description: description,
			Params:      params,
			Timeout:     timeout,
		})
	}

	return tools, nil
}

// extractFrontmatter reads the first scanHeaderLines lines of a file in a
// single pass and collects the tool's frontmatter: the first Description:
// line (first occurrence wins; populates the MCP tool description), all
// Param: annotations (invalid ones log a stderr warning and are skipped),
// and the first Timeout: value (first occurrence wins; nil when undeclared
// so the global applies, &0 for NONE/0; an invalid value logs a stderr
// warning and yields nil so the global applies). An unreadable file yields
// extractFrontmatter reads the first scanHeaderLines lines of a file in a
// single pass and collects the tool's frontmatter: the first Description:
// line (first occurrence wins; populates the MCP tool description), all
// Param: annotations (invalid ones log a stderr warning and are skipped),
// and the first Timeout: value (first occurrence wins; nil when undeclared
// so the global applies, &0 for NONE/0; an invalid value logs a stderr
// warning and yields nil so the global applies). An unreadable file yields
// zero values.
func extractFrontmatter(filePath string, stderr io.Writer) (description string, params []paramSpec, timeout *time.Duration) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", []paramSpec{}, nil
	}
	defer file.Close()

	params = []paramSpec{}
	descriptionSeen := false
	timeoutSeen := false
	scanner := bufio.NewScanner(file)
	lineCount := 0

	for scanner.Scan() && lineCount < scanHeaderLines {
		lineCount++
		line := scanner.Text()

		if _, after, found := strings.Cut(line, scanDescriptionPrefix); found {
			if !descriptionSeen {
				description = strings.TrimSpace(after)
			}
			descriptionSeen = true
			continue
		}

		if _, after, found := strings.Cut(line, scanTimeoutPrefix); found {
			if !timeoutSeen {
				if duration, err := parseTimeoutDuration(after); err != nil {
					// Warn and keep timeout nil (global applies) without
					// interrupting the scan, so later Param: lines are still collected.
					fmt.Fprintf(stderr, "Warning: ignoring invalid Timeout in %s: %v\n", filePath, err)
				} else {
					timeout = &duration
				}
			}
			timeoutSeen = true
			continue
		}

		if _, after, found := strings.Cut(line, scanParamPrefix); found {
			// Invalid annotations log their own stderr warning.
			if param, err := parseParamAnnotation(strings.TrimSpace(after), filePath, line, stderr); err == nil {
				params = append(params, param)
			}
		}
	}

	return description, params, timeout
}

// paramTypes is the accepted set of frontmatter parameter types. Anything
// else on a Param: annotation is skipped with a warning.
var paramTypes = map[string]bool{"string": true, "number": true, "boolean": true}

// warnParam logs the single stderr warning shape shared by every rejected
// Param: annotation variant.
func warnParam(filePath, reason, fullLine string, stderr io.Writer) {
	fmt.Fprintf(stderr, "Warning: skipping invalid Param annotation in %s because %s: %q\n", filePath, reason, fullLine)
}

// parseParamAnnotation parses a single parameter annotation string.
// It expects format: <name> <type> <required|optional> "<description>"
// parseParamAnnotation parses a single parameter annotation string.
// It expects format: <name> <type> <required|optional> "<description>"
// Returns error if validation fails (warning already logged to stderr).
func parseParamAnnotation(annotation, filePath, fullLine string, stderr io.Writer) (paramSpec, error) {
	// The description is the last field, wrapped in quotes; the first
	// quoted span is the one we take.
	lastQuoteIdx := strings.LastIndex(annotation, "\"")
	firstQuoteIdx := strings.Index(annotation, "\"")

	if firstQuoteIdx < 0 || lastQuoteIdx < 0 || firstQuoteIdx == lastQuoteIdx {
		warnParam(filePath, "description must be quoted", fullLine, stderr)
		return paramSpec{}, fmt.Errorf("malformed param annotation")
	}

	// Extract description (strip quotes)
	description := annotation[firstQuoteIdx+1 : lastQuoteIdx]

	// Extract the prefix (before the first quote)
	prefix := strings.TrimSpace(annotation[:firstQuoteIdx])

	// Split prefix into name, type, and required/optional token
	tokens := strings.Fields(prefix)
	if len(tokens) != 3 {
		warnParam(filePath, "expected 3 fields (name, type, required|optional)", fullLine, stderr)
		return paramSpec{}, fmt.Errorf("wrong field count")
	}

	name := tokens[0]
	paramType := tokens[1]
	requiredToken := tokens[2]

	// Validate name against argumentKeyPattern
	if !argumentKeyPattern.MatchString(name) {
		warnParam(filePath, fmt.Sprintf("parameter name %q must match %s", name, argumentKeyPattern.String()), fullLine, stderr)
		return paramSpec{}, fmt.Errorf("invalid parameter name")
	}

	// Validate type
	if !paramTypes[paramType] {
		warnParam(filePath, fmt.Sprintf("type must be string, number, or boolean, got %q", paramType), fullLine, stderr)
		return paramSpec{}, fmt.Errorf("invalid type")
	}

	// Validate required/optional token
	var required bool
	switch requiredToken {
	case "required":
		required = true
	case "optional":
		required = false
	default:
		warnParam(filePath, fmt.Sprintf("status must be 'required' or 'optional', got %q", requiredToken), fullLine, stderr)
		return paramSpec{}, fmt.Errorf("invalid required token")
	}

	return paramSpec{
		Name:        name,
		Type:        paramType,
		Required:    required,
		Description: description,
	}, nil
}
