package permission

import (
	"path/filepath"
	"strings"
)

// PathValidationResult represents the outcome of a file path security check.
// Mirrors pathValidation.ts validation result structure.
type PathValidationResult struct {
	// Allowed indicates whether the file operation is permitted.
	Allowed bool
	// Reason explains why the operation was allowed or denied.
	Reason string
	// RequiresAsk indicates the operation requires user confirmation even in auto-mode.
	RequiresAsk bool
}

// sensitivePathPattern defines a file path pattern that should be protected
// from certain operations due to security concerns.
type sensitivePathPattern struct {
	// Pattern is a glob-style or base-name pattern to match against file paths.
	Pattern string
	// Description explains why this path is sensitive.
	Description string
	// Operations lists which ops are blocked: "write", "read", "all".
	Operations []string
}

// sensitivePathPatterns contains the built-in set of sensitive file patterns.
// These paths contain secrets, credentials, or code-execution surfaces that
// should be protected from unauthorized agent access.
var sensitivePathPatterns = []sensitivePathPattern{
	{".env", "Environment file with secrets", []string{"read", "write"}},
	{".env.*", "Environment file variant", []string{"read", "write"}},
	{"*.pem", "PEM certificate/key file", []string{"read"}},
	{"*.key", "Private key file", []string{"read"}},
	{"id_rsa", "SSH private key", []string{"read"}},
	{"id_ed25519", "SSH private key", []string{"read"}},
	{"credentials.json", "Credentials file", []string{"read"}},
	{".git/config", "Git config (may contain tokens)", []string{"write"}},
	{".git/hooks/*", "Git hooks (code execution)", []string{"write"}},
	{".claude/settings.json", "Claude settings", []string{"write"}},
	{".claude/settings.local.json", "Claude local settings", []string{"write"}},
}

// systemPaths contains directory prefixes that belong to the operating system
// and should never be modified by the agent.
var systemPaths = []string{
	"/etc/",
	"/usr/",
	"/bin/",
	"/sbin/",
	"/boot/",
	"/sys/",
	"/proc/",
}

// ValidateFilePath checks if a file operation on the given path is safe.
// It considers the operation type (read/write/create/delete) and the
// current working directory to determine safety.
//
// Operation should be one of: "read", "write", "create", "delete".
// Returns a PathValidationResult indicating whether the operation is allowed.
func ValidateFilePath(path, operation, cwd string) PathValidationResult {
	if path == "" {
		return PathValidationResult{
			Allowed: false,
			Reason:  "empty file path",
		}
	}

	// Normalize the path for consistent checking.
	normalized := NormalizePath(path, cwd)

	// Check system paths first (most restrictive).
	if IsSystemPath(normalized) {
		return PathValidationResult{
			Allowed: false,
			Reason:  "path is in a system directory: " + normalized,
		}
	}

	// Check if path is outside the project directory.
	if IsOutsideProject(normalized, cwd) {
		// Reading outside the project requires confirmation; writes are denied.
		if operation == "read" {
			return PathValidationResult{
				Allowed:     true,
				Reason:      "reading outside project directory requires confirmation",
				RequiresAsk: true,
			}
		}
		return PathValidationResult{
			Allowed: false,
			Reason:  "path is outside the project directory: " + normalized,
		}
	}

	// Check sensitive path patterns.
	if sensitive, desc := IsSensitivePath(normalized, operation); sensitive {
		return PathValidationResult{
			Allowed:     false,
			Reason:      "path matches sensitive pattern: " + desc,
			RequiresAsk: true,
		}
	}

	return PathValidationResult{
		Allowed: true,
		Reason:  "path is within project and not sensitive",
	}
}

// IsOutsideProject checks if a path resolves to a location outside
// the project working directory.
func IsOutsideProject(path, cwd string) bool {
	if cwd == "" {
		return false
	}

	normalized := NormalizePath(path, cwd)
	cwdClean := filepath.Clean(cwd)

	// Ensure cwd ends with separator for proper prefix matching.
	cwdPrefix := cwdClean + string(filepath.Separator)

	// The path is inside the project if it equals cwd or is under it.
	if normalized == cwdClean {
		return false
	}
	return !strings.HasPrefix(normalized, cwdPrefix)
}

// IsSensitivePath checks if a path matches any sensitive patterns.
// Returns whether the path is sensitive and a description of the matching pattern.
func IsSensitivePath(path, operation string) (bool, string) {
	// Get the base name and relative components for matching.
	base := filepath.Base(path)

	for _, sp := range sensitivePathPatterns {
		if !operationBlocked(sp.Operations, operation) {
			continue
		}
		if matchSensitivePattern(sp.Pattern, path, base) {
			return true, sp.Description
		}
	}
	return false, ""
}

// IsSystemPath checks if a path is in a system directory that
// should never be modified by the agent.
func IsSystemPath(path string) bool {
	cleaned := filepath.Clean(path)
	for _, prefix := range systemPaths {
		if strings.HasPrefix(cleaned, prefix) {
			return true
		}
	}
	return false
}

// NormalizePath resolves a path relative to cwd and cleans it.
// If path is already absolute, it is simply cleaned.
// If path is relative, it is joined with cwd and then cleaned.
func NormalizePath(path, cwd string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if cwd == "" {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(cwd, path))
}

// operationBlocked checks whether the given operation is blocked by the pattern's
// operation list.
func operationBlocked(blockedOps []string, operation string) bool {
	for _, op := range blockedOps {
		if op == "all" {
			return true
		}
		if op == operation {
			return true
		}
		// "write" blocks both "write", "create", and "delete" operations.
		if op == "write" && (operation == "create" || operation == "delete") {
			return true
		}
	}
	return false
}

// matchSensitivePattern checks if a path matches a sensitive pattern.
// Supports glob-style matching against both the full path and the base name.
func matchSensitivePattern(pattern, fullPath, baseName string) bool {
	comparePattern := strings.ToLower(pattern)
	comparePath := strings.ToLower(fullPath)
	compareBase := strings.ToLower(baseName)

	// If pattern contains a path separator, match against path components.
	if strings.Contains(pattern, "/") {
		// Check if the pattern appears as a suffix in the full path.
		// e.g., ".git/config" matches "/project/.git/config"
		patternClean := filepath.Clean(comparePattern)
		if strings.HasSuffix(comparePath, string(filepath.Separator)+patternClean) {
			return true
		}
		// Also try glob matching against the full path's tail.
		parts := strings.Split(comparePath, string(filepath.Separator))
		patternParts := strings.Split(patternClean, string(filepath.Separator))
		if len(parts) >= len(patternParts) {
			tail := strings.Join(parts[len(parts)-len(patternParts):], string(filepath.Separator))
			if matched, _ := filepath.Match(patternClean, tail); matched {
				return true
			}
		}
		return false
	}

	// Pattern without separator — match against the base name.
	if matched, _ := filepath.Match(comparePattern, compareBase); matched {
		return true
	}

	// Special handling for ".env.*" style patterns: also check if the base
	// name starts with the prefix before the wildcard.
	if strings.HasPrefix(comparePattern, ".env") {
		if matched, _ := filepath.Match(comparePattern, compareBase); matched {
			return true
		}
	}

	return false
}
