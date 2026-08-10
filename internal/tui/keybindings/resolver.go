package keybindings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
)

// ResolutionKind describes how a key event affected resolver state.
type ResolutionKind int

const (
	ResolutionNone ResolutionKind = iota
	ResolutionMatch
	ResolutionChordStarted
	ResolutionChordCancelled
)

// Resolution is the context-aware result for one key event.
type Resolution struct {
	Kind    ResolutionKind
	Action  Action
	Pending string
}

// Resolver resolves key events to actions and owns multi-keystroke chord state.
type Resolver struct {
	mu       sync.RWMutex
	blocks   []Block
	compiled map[Context][]compiledBinding
	pending  []KeyPattern
	contexts string
	issues   []ValidationIssue
}

type compiledBinding struct {
	chord      []KeyPattern
	action     Action
	raw        string
	normalized string
}

// NewResolver creates a resolver with only product-reachable default actions.
func NewResolver() *Resolver {
	r := &Resolver{}
	r.SetBindings(DefaultBindings())
	return r
}

// SetBindings replaces all bindings. It is primarily useful for tests and
// callers that already validated their configuration.
func (r *Resolver) SetBindings(blocks []Block) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setBindingsLocked(blocks)
}

// ResetPending cancels any incomplete chord before input ownership moves to
// another editor or modal region.
func (r *Resolver) ResetPending() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.pending = nil
	r.contexts = ""
	r.mu.Unlock()
}

func (r *Resolver) setBindingsLocked(blocks []Block) {
	r.blocks = cloneBlocks(blocks)
	r.compiled = make(map[Context][]compiledBinding)
	r.pending = nil
	r.contexts = ""

	for _, block := range r.blocks {
		keys := make([]string, 0, len(block.Bindings))
		for raw := range block.Bindings {
			keys = append(keys, raw)
		}
		sort.Strings(keys)
		for _, raw := range keys {
			chord, err := ParseChord(raw)
			if err != nil {
				continue
			}
			normalized, _ := NormalizeKeyPattern(raw)
			r.compiled[block.Context] = append(r.compiled[block.Context], compiledBinding{
				chord: chord, action: block.Bindings[raw], raw: raw, normalized: normalized,
			})
		}
	}
}

// ResolveEvent resolves one key and reports chord-prefix consumption.
func (r *Resolver) ResolveEvent(msg tea.KeyPressMsg, activeContexts ...Context) Resolution {
	r.mu.Lock()
	defer r.mu.Unlock()

	contexts := orderedContexts(activeContexts)
	contextKey := contextSignature(contexts)
	if len(r.pending) > 0 && r.contexts != contextKey {
		r.pending = nil
		r.contexts = ""
	}

	current := keyPatternFromMsg(msg)
	if current.Key == "" {
		if len(r.pending) > 0 {
			r.pending = nil
			r.contexts = ""
			return Resolution{Kind: ResolutionChordCancelled}
		}
		return Resolution{Kind: ResolutionNone}
	}
	if len(r.pending) > 0 && current.Key == "escape" && !current.Ctrl && !current.Alt {
		r.pending = nil
		r.contexts = ""
		return Resolution{Kind: ResolutionChordCancelled}
	}

	test := append(append([]KeyPattern(nil), r.pending...), current)
	for _, context := range contexts {
		bindings := r.compiled[context]
		if hasLongerChord(bindings, test) {
			r.pending = test
			r.contexts = contextKey
			return Resolution{Kind: ResolutionChordStarted, Pending: chordString(test)}
		}
		if action, ok := exactChordAction(bindings, test); ok {
			r.pending = nil
			r.contexts = ""
			return Resolution{Kind: ResolutionMatch, Action: action}
		}
	}

	if len(r.pending) > 0 {
		r.pending = nil
		r.contexts = ""
		return Resolution{Kind: ResolutionChordCancelled}
	}
	return Resolution{Kind: ResolutionNone}
}

// Resolve is the compatibility helper for callers that only need exact
// matches. Chord prefixes are consumed but return no action until completed.
func (r *Resolver) Resolve(msg tea.KeyPressMsg, activeContexts ...Context) (Action, bool) {
	resolution := r.ResolveEvent(msg, activeContexts...)
	return resolution.Action, resolution.Kind == ResolutionMatch
}

// GetBindingsForContext returns a defensive key-to-action snapshot.
func (r *Resolver) GetBindingsForContext(context Context) map[string]Action {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string]Action)
	for _, binding := range r.compiled[context] {
		result[binding.raw] = binding.action
	}
	return result
}

// GetKeysForAction returns deterministic display keys for an action.
func (r *Resolver) GetKeysForAction(context Context, action Action) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var keys []string
	for _, binding := range r.compiled[context] {
		if binding.action == action {
			keys = append(keys, binding.raw)
		}
	}
	sort.Strings(keys)
	return keys
}

// GetKeyForAction returns the first deterministic display key.
func (r *Resolver) GetKeyForAction(context Context, action Action) string {
	keys := r.GetKeysForAction(context, action)
	if len(keys) > 0 {
		return keys[0]
	}
	if context != ContextGlobal {
		keys = r.GetKeysForAction(ContextGlobal, action)
		if len(keys) > 0 {
			return keys[0]
		}
	}
	return ""
}

// Issues returns validation findings from the latest user-config load.
func (r *Resolver) Issues() []ValidationIssue {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]ValidationIssue(nil), r.issues...)
}

// LoadUserBindings loads keybindings.json from one already-resolved config root.
// Invalid configurations leave the last valid bindings active.
func (r *Resolver) LoadUserBindings(configDir string) ([]ValidationIssue, error) {
	path := filepath.Join(configDir, "keybindings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	userBlocks, issues, err := parseAndValidateUserBindings(data)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.issues = append([]ValidationIssue(nil), issues...)
	if HasValidationErrors(issues) {
		return append([]ValidationIssue(nil), issues...), nil
	}

	merged := cloneBlocks(r.blocks)
	for _, userBlock := range userBlocks {
		context := Context(userBlock.Context)
		index := blockIndex(merged, context)
		if index < 0 {
			merged = append(merged, Block{Context: context, Bindings: make(map[string]Action)})
			index = len(merged) - 1
		}
		for raw, action := range userBlock.Bindings {
			normalized, _ := NormalizeKeyPattern(raw)
			removeNormalizedBinding(merged[index].Bindings, normalized)
			if action != nil {
				merged[index].Bindings[raw] = Action(*action)
			}
		}
	}
	r.setBindingsLocked(merged)
	r.issues = append([]ValidationIssue(nil), issues...)
	return append([]ValidationIssue(nil), issues...), nil
}

func parseAndValidateUserBindings(
	data []byte,
) ([]userBindingBlock, []ValidationIssue, error) {
	var envelope struct {
		Bindings json.RawMessage `json:"bindings"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, nil, err
	}
	if len(envelope.Bindings) == 0 {
		return nil, nil, fmt.Errorf("keybindings.json must contain a bindings array")
	}
	if bytes.Equal(bytes.TrimSpace(envelope.Bindings), []byte("null")) {
		return nil, nil, fmt.Errorf("keybindings.json bindings must be an array, not null")
	}
	var userBlocks []userBindingBlock
	if err := json.Unmarshal(envelope.Bindings, &userBlocks); err != nil {
		return nil, nil, fmt.Errorf("keybindings.json bindings must be an array: %w", err)
	}
	return userBlocks, validateUserBindingBlocks(userBlocks), nil
}

func orderedContexts(active []Context) []Context {
	seen := make(map[Context]bool, len(active)+1)
	result := make([]Context, 0, len(active)+1)
	for _, context := range active {
		if context == ContextGlobal || seen[context] {
			continue
		}
		seen[context] = true
		result = append(result, context)
	}
	result = append(result, ContextGlobal)
	return result
}

func contextSignature(contexts []Context) string {
	parts := make([]string, len(contexts))
	for i, context := range contexts {
		parts[i] = string(context)
	}
	return strings.Join(parts, "\x00")
}

func hasLongerChord(bindings []compiledBinding, prefix []KeyPattern) bool {
	winners := make(map[string]Action)
	for _, binding := range bindings {
		if len(binding.chord) <= len(prefix) || !chordPrefixMatches(binding.chord, prefix) {
			continue
		}
		winners[binding.normalized] = binding.action
	}
	return len(winners) > 0
}

func exactChordAction(bindings []compiledBinding, chord []KeyPattern) (Action, bool) {
	for i := len(bindings) - 1; i >= 0; i-- {
		if chordEqual(bindings[i].chord, chord) {
			return bindings[i].action, true
		}
	}
	return "", false
}

func chordPrefixMatches(chord, prefix []KeyPattern) bool {
	if len(prefix) > len(chord) {
		return false
	}
	for i := range prefix {
		if chord[i] != prefix[i] {
			return false
		}
	}
	return true
}

func chordEqual(left, right []KeyPattern) bool {
	return len(left) == len(right) && chordPrefixMatches(left, right)
}

func chordString(chord []KeyPattern) string {
	parts := make([]string, len(chord))
	for i, key := range chord {
		parts[i] = key.String()
	}
	return strings.Join(parts, " ")
}

func keyPatternFromMsg(msg tea.KeyPressMsg) KeyPattern {
	canonical := canonicalKeyMsg(msg)
	parsed, _ := ParseKeyPatternStrict(canonical)
	return parsed
}

func cloneBlocks(blocks []Block) []Block {
	cloned := make([]Block, len(blocks))
	for i, block := range blocks {
		bindings := make(map[string]Action, len(block.Bindings))
		for key, action := range block.Bindings {
			bindings[key] = action
		}
		cloned[i] = Block{Context: block.Context, Bindings: bindings}
	}
	return cloned
}

func blockIndex(blocks []Block, context Context) int {
	for i := range blocks {
		if blocks[i].Context == context {
			return i
		}
	}
	return -1
}

func removeNormalizedBinding(bindings map[string]Action, normalized string) {
	for raw := range bindings {
		candidate, err := NormalizeKeyPattern(raw)
		if err == nil && candidate == normalized {
			delete(bindings, raw)
		}
	}
}
