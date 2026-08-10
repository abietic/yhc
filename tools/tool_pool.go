package tools

import (
	"sort"
	"strings"

	"github.com/cloudwego/eino/schema"
)

var simpleBuiltInTools = map[string]struct{}{
	"Bash": {},
	"Edit": {},
	"Read": {},
}

// ToolPoolOptions controls the model-visible projection of the runtime tool
// registry. AllowedNames scopes every partition; BuiltInNames applies only to
// built-ins so explicit base-tool selection does not implicitly remove MCP.
// Nil means no additional scope; a non-nil empty slice exposes no matching
// tools in that scope.
type ToolPoolOptions struct {
	AllowedNames    []string
	BuiltInNames    []string
	Simple          bool
	BlanketDeniedFn func(string) bool
}

// IsMCPToolName reports whether name follows the first-class MCP naming
// convention used by the runtime registry.
func IsMCPToolName(name string) bool {
	return strings.HasPrefix(name, "mcp__")
}

// AssembleToolPool is the single model-visible tool assembly boundary. It
// keeps the runtime registry complete while filtering hidden, disabled,
// out-of-scope, mode-restricted, and blanket-denied tools before model calls.
// Built-ins and MCP tools are sorted independently for prompt-cache stability,
// then deduplicated with built-ins taking precedence.
func AssembleToolPool(r *Registry, opts ToolPoolOptions) []*schema.ToolInfo {
	if r == nil {
		return nil
	}

	var allowed map[string]struct{}
	if opts.AllowedNames != nil {
		allowed = make(map[string]struct{}, len(opts.AllowedNames))
		for _, name := range opts.AllowedNames {
			name = strings.TrimSpace(name)
			if name != "" {
				allowed[name] = struct{}{}
			}
		}
	}
	var allowedBuiltIns map[string]struct{}
	if opts.BuiltInNames != nil {
		allowedBuiltIns = make(map[string]struct{}, len(opts.BuiltInNames))
		for _, name := range opts.BuiltInNames {
			name = strings.TrimSpace(name)
			if name != "" {
				allowedBuiltIns[name] = struct{}{}
			}
		}
	}

	type candidate struct {
		name  string
		info  *schema.ToolInfo
		isMCP bool
	}
	r.mu.RLock()
	candidates := make([]candidate, 0, len(r.order))
	for _, name := range r.order {
		impl, ok := r.tools[name]
		if !ok || impl.Info == nil || impl.IsHidden || r.disabled[name] {
			continue
		}
		if allowed != nil {
			if _, ok := allowed[name]; !ok {
				continue
			}
		}
		candidates = append(candidates, candidate{name: name, info: impl.Info, isMCP: IsMCPToolName(name)})
	}
	r.mu.RUnlock()

	builtIns := make([]*schema.ToolInfo, 0, len(candidates))
	mcpTools := make([]*schema.ToolInfo, 0)
	for _, candidate := range candidates {
		if !candidate.isMCP && allowedBuiltIns != nil {
			if _, ok := allowedBuiltIns[candidate.name]; !ok {
				continue
			}
		}
		if opts.Simple && !candidate.isMCP {
			if _, ok := simpleBuiltInTools[candidate.name]; !ok {
				continue
			}
		}
		if opts.BlanketDeniedFn != nil && opts.BlanketDeniedFn(candidate.name) {
			continue
		}
		if candidate.isMCP {
			mcpTools = append(mcpTools, candidate.info)
		} else {
			builtIns = append(builtIns, candidate.info)
		}
	}

	byName := func(i, j int) bool { return builtIns[i].Name < builtIns[j].Name }
	sort.SliceStable(builtIns, byName)
	sort.SliceStable(mcpTools, func(i, j int) bool { return mcpTools[i].Name < mcpTools[j].Name })

	result := make([]*schema.ToolInfo, 0, len(builtIns)+len(mcpTools))
	seen := make(map[string]struct{}, len(builtIns)+len(mcpTools))
	for _, info := range append(builtIns, mcpTools...) {
		if _, ok := seen[info.Name]; ok {
			continue
		}
		seen[info.Name] = struct{}{}
		result = append(result, info)
	}
	return result
}
