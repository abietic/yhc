package session

import (
	"net/url"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// ParsedSessionURL contains the parsed result of a session resume identifier.
//
// Reference: src/utils/sessionUrl.ts (64 lines)
type ParsedSessionURL struct {
	SessionID   string
	IngressURL  string
	IsURL       bool
	JSONLFile   string
	IsJSONLFile bool
}

// ParseSessionIdentifier parses a session resume identifier which can be:
//   - A URL containing session ID
//   - A plain session ID (UUID)
//   - A JSONL file path
//
// Returns nil if the identifier is invalid.
func ParseSessionIdentifier(resumeIdentifier string) *ParsedSessionURL {
	trimmed := strings.TrimSpace(resumeIdentifier)
	if trimmed == "" {
		return nil
	}

	// Check for JSONL file path first
	if strings.ToLower(filepath.Ext(trimmed)) == ".jsonl" {
		return &ParsedSessionURL{
			SessionID:   uuid.New().String(),
			JSONLFile:   trimmed,
			IsJSONLFile: true,
		}
	}

	// Check if it's a plain UUID
	if _, err := uuid.Parse(trimmed); err == nil {
		return &ParsedSessionURL{
			SessionID: trimmed,
		}
	}

	// Check if it's a URL
	if u, err := url.Parse(trimmed); err == nil && u.Scheme != "" && u.Host != "" {
		return &ParsedSessionURL{
			SessionID:  uuid.New().String(),
			IngressURL: u.String(),
			IsURL:      true,
		}
	}

	return nil
}
