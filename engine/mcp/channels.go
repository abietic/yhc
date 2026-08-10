package mcp

import (
	"strings"
	"sync"
)

// ChannelAllowlist manages which MCP channels are permitted.
//
// Reference: src/services/mcp/channelAllowlist.ts
type ChannelAllowlist struct {
	mu       sync.RWMutex
	allowed  map[string]bool
	patterns []string
}

// NewChannelAllowlist creates an allowlist from patterns.
func NewChannelAllowlist(patterns []string) *ChannelAllowlist {
	a := &ChannelAllowlist{
		allowed:  make(map[string]bool),
		patterns: patterns,
	}
	for _, p := range patterns {
		if !strings.Contains(p, "*") {
			a.allowed[p] = true
		}
	}
	return a
}

// IsAllowed checks if a channel name is permitted.
func (a *ChannelAllowlist) IsAllowed(channel string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.allowed[channel] {
		return true
	}
	for _, p := range a.patterns {
		if matchGlob(p, channel) {
			return true
		}
	}
	return false
}

func matchGlob(pattern, s string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(s, pattern[:len(pattern)-1])
	}
	if strings.HasPrefix(pattern, "*") {
		return strings.HasSuffix(s, pattern[1:])
	}
	return pattern == s
}

// ChannelPermissions manages per-channel tool access control.
//
// Reference: src/services/mcp/channelPermissions.ts
type ChannelPermissions struct {
	mu    sync.RWMutex
	rules map[string]ChannelRule
}

// ChannelRule defines permissions for a specific channel.
type ChannelRule struct {
	AllowedTools []string `json:"allowedTools,omitempty"`
	DeniedTools  []string `json:"deniedTools,omitempty"`
	ReadOnly     bool     `json:"readOnly,omitempty"`
}

// NewChannelPermissions creates empty channel permissions.
func NewChannelPermissions() *ChannelPermissions {
	return &ChannelPermissions{rules: make(map[string]ChannelRule)}
}

// SetRule sets the permission rule for a channel.
func (p *ChannelPermissions) SetRule(channel string, rule ChannelRule) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rules[channel] = rule
}

// GetRule returns the rule for a channel, if any.
func (p *ChannelPermissions) GetRule(channel string) (ChannelRule, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	r, ok := p.rules[channel]
	return r, ok
}

// IsToolAllowed checks if a tool is allowed for a channel.
func (p *ChannelPermissions) IsToolAllowed(channel, toolName string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	rule, ok := p.rules[channel]
	if !ok {
		return true
	}

	for _, denied := range rule.DeniedTools {
		if denied == toolName || denied == "*" {
			return false
		}
	}
	if len(rule.AllowedTools) > 0 {
		for _, allowed := range rule.AllowedTools {
			if allowed == toolName || allowed == "*" {
				return true
			}
		}
		return false
	}
	return true
}

// ChannelNotificationHandler handles MCP channel notification events.
//
// Reference: src/services/mcp/channelNotification.ts
type ChannelNotificationHandler struct {
	mu        sync.RWMutex
	listeners []func(ChannelNotification)
}

// ChannelNotification represents an MCP channel event.
type ChannelNotification struct {
	Channel string `json:"channel"`
	Type    string `json:"type"` // "connected", "disconnected", "error", "tools_changed"
	Message string `json:"message,omitempty"`
}

// NewChannelNotificationHandler creates a notification handler.
func NewChannelNotificationHandler() *ChannelNotificationHandler {
	return &ChannelNotificationHandler{}
}

// OnNotification registers a listener.
func (h *ChannelNotificationHandler) OnNotification(cb func(ChannelNotification)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.listeners = append(h.listeners, cb)
}

// Emit broadcasts a notification.
func (h *ChannelNotificationHandler) Emit(n ChannelNotification) {
	h.mu.RLock()
	listeners := make([]func(ChannelNotification), len(h.listeners))
	copy(listeners, h.listeners)
	h.mu.RUnlock()
	for _, cb := range listeners {
		cb(n)
	}
}

// OfficialServerEntry represents an entry in the official MCP server registry.
//
// Reference: src/services/mcp/officialRegistry.ts
type OfficialServerEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Command     string `json:"command,omitempty"`
	URL         string `json:"url,omitempty"`
	Category    string `json:"category,omitempty"`
	Verified    bool   `json:"verified,omitempty"`
}

// OfficialRegistry provides a list of known MCP servers.
type OfficialRegistry struct {
	servers []OfficialServerEntry
}

// NewOfficialRegistry creates a registry with known servers.
func NewOfficialRegistry() *OfficialRegistry {
	return &OfficialRegistry{}
}

// Search finds servers matching a query.
func (r *OfficialRegistry) Search(query string) []OfficialServerEntry {
	if query == "" {
		return r.servers
	}
	q := strings.ToLower(query)
	var results []OfficialServerEntry
	for _, s := range r.servers {
		if strings.Contains(strings.ToLower(s.Name), q) ||
			strings.Contains(strings.ToLower(s.Description), q) {
			results = append(results, s)
		}
	}
	return results
}
