package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
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
// resolving to executables. It skips subdirectories, non-regular files
// (FIFOs, devices — including symlinks to them), and non-executable files.
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

		if !isToolFile(fileInfo) {
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

// isToolFile reports whether a resolved path is a registrable tool file:
// it must be a regular file (FIFOs, devices, and symlinks to any of them
// are skipped — os.Open on an executable FIFO would block discovery
// forever) and executable. Executability is OS-aware: Unix permission
// bits, Windows executable extensions (normal file modes never set 0111).
// On Windows the extension is taken from the resolved target (fileInfo
// names it), not the link: alias.exe -> notes.txt must be skipped, alias
// -> tool.exe must register, because the target is what gets executed.
func isToolFile(fileInfo os.FileInfo) bool {
	if !fileInfo.Mode().IsRegular() {
		return false
	}
	if runtime.GOOS == "windows" {
		return isWindowsExecutable(fileInfo.Name())
	}
	return fileInfo.Mode()&0111 != 0
}

// windowsExecutableExtensions are the file types the execution path can
// start directly on Windows (PE binaries via CreateProcess). Batch files
// (.bat/.cmd) are excluded: CreateProcess only runs them through cmd.exe
// with a re-parsed command line, and the exec path does not add such a
// wrapper, so a registered tool would fail or mangle its arguments. Script
// languages (.ps1, .js) do not self-execute there either.
var windowsExecutableExtensions = map[string]bool{
	".exe": true,
	".com": true,
}

func isWindowsExecutable(name string) bool {
	return windowsExecutableExtensions[strings.ToLower(filepath.Ext(name))]
}

// extractFrontmatter reads the first scanHeaderLines lines of a file in a
// single pass and collects the tool's frontmatter: the first Description:
// line (first occurrence wins; populates the MCP tool description), all
// Param: annotations (invalid ones log a stderr warning and are skipped),
// and the first Timeout: value. First-occurrence wins: `timeoutSeen` is set
// on the first Timeout: line even when it is invalid (which logs a stderr
// warning and yields nil, so the global applies), so later valid values are
// ignored. nil when undeclared, &0 for NONE/0. An unreadable file yields
// zero values. A line exceeding the scanner buffer aborts the scan early and
// logs a stderr warning; the metadata read so far is returned.
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

	for lineCount < scanHeaderLines {
		if !scanner.Scan() {
			break
		}
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

	// A frontmatter line at or over the scanner buffer (64 KiB) aborts the
	// scan early. Surface it: the tool registers with the metadata collected
	// so far, and the warning makes the incompleteness visible.
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(stderr, "Warning: frontmatter of %s is incomplete (%v); using the metadata read so far\n", filePath, err)
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
