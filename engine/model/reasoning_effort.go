package model

import "strings"

var adapterReasoningEfforts = map[string]map[string]struct{}{
	"agenticclaude": {
		"low": {}, "medium": {}, "high": {}, "xhigh": {}, "max": {},
	},
	"agenticopenai": {
		"none": {}, "minimal": {}, "low": {}, "medium": {}, "high": {}, "xhigh": {},
	},
	"agenticark": {
		"minimal": {}, "low": {}, "medium": {}, "high": {},
	},
	"agenticgemini": {
		"low": {}, "high": {},
	},
	"agenticdeepseek": {},
	"agenticqwen":     {},
}

// SupportsAdapterReasoningEffort reports whether one provider adapter can
// lower an explicit effort value without translation. Empty effort means
// provider default and is valid for every known adapter.
func SupportsAdapterReasoningEffort(provider, effort string) bool {
	supported, known := adapterReasoningEfforts[strings.ToLower(
		strings.TrimSpace(provider),
	)]
	if !known {
		return false
	}
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" {
		return true
	}
	_, ok := supported[effort]
	return ok
}
