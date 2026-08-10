package tools

import "strings"

// ToolPreset identifies a predefined set of tools.
// Reference: src/tools.ts:165-187
type ToolPreset string

const (
	PresetDefault ToolPreset = "default"
)

var validPresets = map[ToolPreset]bool{
	PresetDefault: true,
}

// ToolSelection is the parsed value of the public --tools option. A nil
// *ToolSelection means the option was not provided; an empty Names slice means
// it was explicitly provided as an empty value and disables built-in tools.
type ToolSelection struct {
	Preset ToolPreset
	Names  []string
}

// ParseToolPreset validates and returns a tool preset, or empty string if invalid.
func ParseToolPreset(preset string) ToolPreset {
	p := ToolPreset(strings.ToLower(strings.TrimSpace(preset)))
	if validPresets[p] {
		return p
	}
	return ""
}

// ParseToolSelection parses the reference-compatible --tools value. The only
// named preset is "default"; every other value is treated as an explicit,
// comma-or-whitespace-separated list of built-in tool names.
func ParseToolSelection(values []string) ToolSelection {
	joined := strings.TrimSpace(strings.Join(values, " "))
	if preset := ParseToolPreset(joined); preset != "" {
		return ToolSelection{Preset: preset}
	}

	names := strings.FieldsFunc(joined, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	return ToolSelection{Names: names}
}

// ResolveToolSelection expands a parsed selection into built-in tool names.
func ResolveToolSelection(r *Registry, selection ToolSelection) []string {
	if selection.Preset != "" {
		return GetToolsForPreset(r, selection.Preset)
	}
	return append([]string(nil), selection.Names...)
}

// GetToolsForPreset returns tool names for the given preset.
// For "default", returns all enabled, non-hidden built-in tools from the registry.
func GetToolsForPreset(r *Registry, preset ToolPreset) []string {
	if preset == "" {
		preset = PresetDefault //nolint:ineffassign,wastedassign // currently only one preset
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	var names []string
	for _, name := range r.order {
		if IsMCPToolName(name) {
			continue
		}
		impl, ok := r.tools[name]
		if !ok {
			continue
		}
		if impl.IsHidden {
			continue
		}
		if r.disabled[name] {
			continue
		}
		names = append(names, name)
	}
	return names
}
