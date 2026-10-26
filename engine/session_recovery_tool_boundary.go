package engine

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// SessionToolBoundary observes an exact committed tool suffix. It does not
// attest to external tool state, financial closure, or permission to continue.
// The ordinary incomplete-turn blocker remains in the enclosing native proof.
type SessionToolBoundary struct {
	Version        int      `json:"version"`
	Kind           string   `json:"kind"`
	CallCount      int      `json:"call_count"`
	ToolNames      []string `json:"tool_names"`
	SHA256         string   `json:"sha256"`
	WorkspaceLocal bool     `json:"workspace_local"`
}

func recoveryProofToolBoundary(messages []*schema.Message, cwd string) *SessionToolBoundary {
	seen := make(map[string]bool)
	pending := make(map[string]bool)
	var round []*schema.Message
	var names []string
	count := 0
	workspaceLocal := filepath.IsAbs(cwd)
	for _, message := range messages {
		if message == nil {
			continue
		}
		meta, _ := message.Extra["is_meta"].(bool)
		apiError, _ := message.Extra["api_error"].(bool)
		if message.Role == schema.System || message.Role == schema.RoleType("developer") || meta || apiError {
			if len(message.ToolCalls) != 0 || message.ToolCallID != "" || apiError && message.Role != schema.Assistant {
				return nil
			}
			continue
		}
		switch message.Role {
		case schema.User, schema.Assistant:
			if len(pending) != 0 || message.ToolCallID != "" {
				return nil
			}
			round, names, count = nil, nil, 0
			if len(message.ToolCalls) == 0 {
				continue
			}
			if message.Role != schema.Assistant {
				return nil
			}
			for _, call := range message.ToolCalls {
				if call.ID == "" || seen[call.ID] || call.Type != "function" || call.Function.Name == "" || !json.Valid([]byte(call.Function.Arguments)) {
					return nil
				}
				seen[call.ID], pending[call.ID] = true, true
				workspaceLocal = workspaceLocal && recoveryProofLocalFileCall(call, cwd)
				names = append(names, call.Function.Name)
			}
			count = len(message.ToolCalls)
			round = append(round, message)
		case schema.Tool:
			if len(message.ToolCalls) != 0 || message.ToolCallID == "" || !pending[message.ToolCallID] {
				return nil
			}
			delete(pending, message.ToolCallID)
			round = append(round, message)
		default:
			return nil
		}
	}
	if len(pending) != 0 || count == 0 || len(round) != count+1 {
		return nil
	}
	digest, err := recoveryProofMessagesSHA256(round)
	if err != nil {
		return nil
	}
	slices.Sort(names)
	return &SessionToolBoundary{Version: 1, Kind: "committed_tool_results", CallCount: count, ToolNames: slices.Compact(names), SHA256: digest, WorkspaceLocal: workspaceLocal}
}

// File transport separately rejects links and external mounts. This proves
// that every historical tool's declared file target belongs to that tree.
func recoveryProofLocalFileCall(call schema.ToolCall, cwd string) bool {
	switch call.Function.Name {
	case "Read", "Write", "Edit":
	default:
		return false
	}
	var arguments struct {
		FilePath string `json:"file_path"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &arguments) != nil || arguments.FilePath == "" || strings.ContainsRune(arguments.FilePath, '\x00') {
		return false
	}
	path := arguments.FilePath
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	relative, err := filepath.Rel(cwd, path)
	return err == nil && !filepath.IsAbs(relative) && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
