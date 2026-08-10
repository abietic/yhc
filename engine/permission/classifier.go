package permission

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/abietic/yhc/engine/execution"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ClassifierDecision is the outcome of the YOLO/auto-mode permission classifier.
// Mirrors src/services/permissions/yoloClassifier.ts decision type.
type ClassifierDecision string

const (
	// ClassifierAllow means the tool use is safe to proceed without user confirmation.
	ClassifierAllow ClassifierDecision = "allow"
	// ClassifierDeny means the tool use is unsafe and should be blocked.
	ClassifierDeny ClassifierDecision = "deny"
	// ClassifierAsk means the classifier is uncertain; fall back to prompting the user.
	ClassifierAsk ClassifierDecision = "ask"
)

const (
	classifierQuerySource  = "yolo_classifier"
	classifierMaxTokens    = 256
	classifierContextLimit = 5
)

// ClassifierConfig configures the AI permission classifier for auto mode.
// Mirrors yoloClassifier.ts YoloClassifierConfig.
type ClassifierConfig struct {
	// ChatModel is the LLM used for classification decisions.
	ChatModel model.BaseChatModel
	// ProviderUsage attributes this helper call to an exact active Goal.
	ProviderUsage execution.ProviderUsageAdmitter
	// AllowRules are user-supplied glob/regex patterns for auto-allowing tool uses.
	AllowRules []string
	// DenyRules are user-supplied glob/regex patterns for auto-denying tool uses.
	DenyRules []string
	// EnvironmentRules describe the execution environment constraints.
	EnvironmentRules []string
	// ClaudeMdContext is CLAUDE.md content providing intent signals for the classifier.
	ClaudeMdContext string
}

// ClassifyToolUse runs the auto-mode permission classifier pipeline:
//  1. If no ChatModel is configured, return "ask" (fail safe to prompting)
//  2. Build the classification prompt and call the LLM
//  3. Parse the response for <allow/> or <block/> tags
//  4. Return ClassifierDeny on parse failure (fail closed)
//
// QueryEngine's canonical action descriptor is the sole owner of deterministic
// Auto admission; this classifier never maintains a second name allowlist.
func ClassifyToolUse(
	ctx context.Context,
	cfg *ClassifierConfig,
	toolName string,
	toolInput map[string]any,
	conversationContext []*schema.Message,
) (ClassifierDecision, error) {
	// No model configured — fail safe to prompting.
	if cfg == nil || cfg.ChatModel == nil {
		return ClassifierAsk, nil
	}

	// Build prompt and call the LLM.
	systemPrompt := buildClassifierSystemPrompt(cfg)
	userPrompt := buildClassifierUserPrompt(toolName, toolInput, conversationContext)

	maxTokens := classifierMaxTokens
	var logicalRoundID string
	if cfg.ProviderUsage != nil {
		logicalRoundID = cfg.ProviderUsage.NewLogicalRoundID()
	}
	msg, err := execution.SideQueryWithRetry(ctx, cfg.ChatModel, execution.SideQueryOptions{
		SystemPrompt:        systemPrompt,
		Messages:            []*schema.Message{{Role: schema.User, Content: userPrompt}},
		MaxOutputTokens:     &maxTokens,
		QuerySource:         classifierQuerySource,
		ProviderUsage:       cfg.ProviderUsage,
		UsageLogicalRoundID: logicalRoundID,
	}, nil)
	if err != nil {
		// On LLM error, fail safe to prompting rather than blocking
		return ClassifierAsk, fmt.Errorf("classifier side query failed: %w", err)
	}

	// Parse the response.
	return parseClassifierResponse(msg), nil
}

// buildClassifierSystemPrompt constructs the system prompt for the permission
// classifier. Instructs the LLM to respond with <allow/> or <block/> tags.
// Mirrors yoloClassifier.ts buildClassifierPrompt.
func buildClassifierSystemPrompt(cfg *ClassifierConfig) string {
	var b strings.Builder

	b.WriteString(`You are a permission classifier for an AI coding agent running in auto-mode.
Your job is to decide whether a tool use is safe to execute without user confirmation.

Respond with EXACTLY one of:
- <allow/> if the tool use is safe and expected given the conversation context
- <block/> if the tool use is potentially dangerous, destructive, or unexpected

Guidelines:
- File reads, searches, and metadata operations are generally safe
- File writes/edits within the project directory are generally safe if they match the conversation intent
- Shell commands that modify files, install packages, or access the network need careful evaluation
- Destructive operations (rm -rf, force push, database modifications) should be blocked
- Operations outside the project directory should be blocked unless clearly intended
`)

	if len(cfg.AllowRules) > 0 {
		b.WriteString("\nUser-configured ALLOW rules (auto-allow if matched):\n")
		for _, rule := range cfg.AllowRules {
			b.WriteString("- ")
			b.WriteString(rule)
			b.WriteString("\n")
		}
	}

	if len(cfg.DenyRules) > 0 {
		b.WriteString("\nUser-configured DENY rules (always block if matched):\n")
		for _, rule := range cfg.DenyRules {
			b.WriteString("- ")
			b.WriteString(rule)
			b.WriteString("\n")
		}
	}

	if len(cfg.EnvironmentRules) > 0 {
		b.WriteString("\nEnvironment constraints:\n")
		for _, rule := range cfg.EnvironmentRules {
			b.WriteString("- ")
			b.WriteString(rule)
			b.WriteString("\n")
		}
	}

	if claudeMd := strings.TrimSpace(cfg.ClaudeMdContext); claudeMd != "" {
		b.WriteString("\nProject context (from CLAUDE.md):\n")
		// Truncate to avoid prompt overflow
		if len(claudeMd) > 2000 {
			claudeMd = claudeMd[:2000] + "..."
		}
		b.WriteString(claudeMd)
		b.WriteString("\n")
	}

	return b.String()
}

// buildClassifierUserPrompt formats the tool use and recent conversation
// for the classifier to evaluate.
func buildClassifierUserPrompt(toolName string, toolInput map[string]any, messages []*schema.Message) string {
	var b strings.Builder

	b.WriteString("Tool being invoked: ")
	b.WriteString(toolName)
	b.WriteString("\n\nTool input:\n")
	b.WriteString(formatClassifierToolInput(toolInput))

	// Include recent conversation context (last N messages)
	if contextText := extractRecentContext(messages, classifierContextLimit); contextText != "" {
		b.WriteString("\n\nRecent conversation context:\n")
		b.WriteString(contextText)
	}

	b.WriteString("\n\nIs this tool use safe to auto-execute? Respond with <allow/> or <block/>.")
	return b.String()
}

// formatClassifierToolInput marshals the tool input for the classifier prompt.
func formatClassifierToolInput(input map[string]any) string {
	if len(input) == 0 {
		return "{}"
	}
	encoded, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return fmt.Sprint(input)
	}
	return string(encoded)
}

// extractRecentContext extracts the last N messages as summarized text
// for the classifier prompt.
func extractRecentContext(messages []*schema.Message, limit int) string {
	if len(messages) == 0 || limit <= 0 {
		return ""
	}

	start := len(messages) - limit
	if start < 0 {
		start = 0
	}

	var parts []string
	for _, msg := range messages[start:] {
		if msg == nil {
			continue
		}
		text := strings.TrimSpace(msg.Content)
		if text == "" {
			continue
		}
		// Truncate individual messages to keep prompt manageable
		if len(text) > 500 {
			text = text[:500] + "..."
		}
		parts = append(parts, fmt.Sprintf("[%s]: %s", msg.Role, text))
	}
	return strings.Join(parts, "\n")
}

// parseClassifierResponse extracts the decision from the LLM response.
// Looks for <allow/> or <block/> tags. Falls back to ClassifierDeny (fail closed)
// if neither tag is found.
// Mirrors yoloClassifier.ts parseClassifierResponse.
func parseClassifierResponse(msg *schema.Message) ClassifierDecision {
	if msg == nil {
		return ClassifierDeny
	}

	content := strings.ToLower(strings.TrimSpace(stripClassifierThinking(msg.Content)))
	if content == "" {
		return ClassifierDeny
	}

	hasAllow := strings.Contains(content, "<allow/>") || strings.Contains(content, "<allow />")
	hasBlock := strings.Contains(content, "<block/>") || strings.Contains(content, "<block />")

	switch {
	case hasAllow && !hasBlock:
		return ClassifierAllow
	case hasBlock && !hasAllow:
		return ClassifierDeny
	default:
		// Ambiguous or missing tags — fail closed
		return ClassifierDeny
	}
}

var classifierThinkingPattern = regexp.MustCompile(`(?is)<thinking>[\s\S]*?</thinking>|<thinking>[\s\S]*$`)

func stripClassifierThinking(content string) string {
	return classifierThinkingPattern.ReplaceAllString(content, "")
}

// --- Speculative Bash Classifier ---
// Fires LLM classification speculatively (in background) as soon as a Bash tool_use
// block is parsed during streaming, before canUseTool is even called.
// The result can short-circuit the permission dialog if it arrives within a grace period.
// Mirrors toolExecution.ts:734-752 startSpeculativeClassifierCheck.

const (
	// SpeculativeGracePeriod is the maximum time to wait for a speculative result
	// before falling through to the interactive dialog.
	SpeculativeGracePeriod = 2 * time.Second
)

// speculativeCheck tracks a single in-flight speculative classification.
type speculativeCheck struct {
	decision ClassifierDecision
	done     chan struct{} // closed when classification completes
}

// SpeculativeClassifier manages background classifier checks that fire ahead
// of the permission flow. When a Bash command is parsed, Start() fires the LLM
// call. Later, Peek() or Consume() retrieves the result.
// Mirrors toolExecution.ts speculative classifier pattern.
type SpeculativeClassifier struct {
	mu      sync.Mutex
	pending map[string]*speculativeCheck // keyed by command string
	cfg     *ClassifierConfig
}

// NewSpeculativeClassifier creates a speculative classifier.
// If cfg is nil or has no ChatModel, Start() will be a no-op.
func NewSpeculativeClassifier(cfg *ClassifierConfig) *SpeculativeClassifier {
	return &SpeculativeClassifier{
		pending: make(map[string]*speculativeCheck),
		cfg:     cfg,
	}
}

// Start fires a background classifier check for a Bash command.
// Non-blocking — the result is stored for later retrieval via Peek/Consume.
// If the command is already being checked, this is a no-op.
// Mirrors toolExecution.ts:734-752 startSpeculativeClassifierCheck.
func (sc *SpeculativeClassifier) Start(ctx context.Context, toolName, command string, messages []*schema.Message) {
	if sc == nil || sc.cfg == nil || sc.cfg.ChatModel == nil {
		return
	}
	// Only speculate on Bash commands — other tools don't benefit.
	if toolName != "Bash" {
		return
	}
	if command == "" {
		return
	}

	sc.mu.Lock()
	if _, exists := sc.pending[command]; exists {
		sc.mu.Unlock()
		return
	}
	check := &speculativeCheck{
		done: make(chan struct{}),
	}
	sc.pending[command] = check
	sc.mu.Unlock()

	// Fire classification in background.
	go func() {
		defer close(check.done)
		input := map[string]any{"command": command}
		decision, _ := ClassifyToolUse(ctx, sc.cfg, toolName, input, messages)
		check.decision = decision
	}()
}

// Peek waits up to timeout for a speculative result for the given command.
// Returns the decision and true if available within the timeout, or (ClassifierAsk, false)
// if the timeout expires or no speculative check was started.
// Mirrors the 2-second grace period race in useCanUseTool.tsx.
func (sc *SpeculativeClassifier) Peek(command string, timeout time.Duration) (ClassifierDecision, bool) {
	if sc == nil {
		return ClassifierAsk, false
	}

	sc.mu.Lock()
	check, exists := sc.pending[command]
	sc.mu.Unlock()

	if !exists {
		return ClassifierAsk, false
	}

	select {
	case <-check.done:
		return check.decision, true
	case <-time.After(timeout):
		return ClassifierAsk, false
	}
}

// Consume retrieves and removes the speculative check channel for a command.
// Returns a channel that will receive the decision when ready, or nil if
// no speculative check was started for this command.
// Used by the interactive handler to race the classifier against user input.
func (sc *SpeculativeClassifier) Consume(command string) <-chan ClassifierDecision {
	if sc == nil {
		return nil
	}

	sc.mu.Lock()
	check, exists := sc.pending[command]
	if exists {
		delete(sc.pending, command)
	}
	sc.mu.Unlock()

	if !exists {
		return nil
	}

	// Wrap in a buffered channel that delivers the decision once done.
	ch := make(chan ClassifierDecision, 1)
	go func() {
		<-check.done
		ch <- check.decision
	}()
	return ch
}

// Cancel removes a pending speculative check without consuming the result.
// Used when the permission flow takes an early path (rule allow/deny) and
// the speculative result is no longer needed.
func (sc *SpeculativeClassifier) Cancel(command string) {
	if sc == nil {
		return
	}
	sc.mu.Lock()
	delete(sc.pending, command)
	sc.mu.Unlock()
}

// ClassifierCache provides LRU caching for classifier decisions to avoid
// redundant model calls for identical tool invocations.
//
// Reference: yoloClassifier.ts uses request caching to avoid re-classifying
// the same tool+input combination.
type ClassifierCache struct {
	mu      sync.Mutex
	entries map[string]classifierCacheEntry
	maxSize int
	order   []string
}

type classifierCacheEntry struct {
	decision  ClassifierDecision
	createdAt time.Time
}

// NewClassifierCache creates a cache with the given max size.
func NewClassifierCache(maxSize int) *ClassifierCache {
	if maxSize <= 0 {
		maxSize = 100
	}
	return &ClassifierCache{
		entries: make(map[string]classifierCacheEntry),
		maxSize: maxSize,
	}
}

func classifierCacheKey(toolName string, toolInput map[string]any) string {
	inputJSON, _ := json.Marshal(toolInput)
	return toolName + ":" + string(inputJSON)
}

// Get returns a cached decision if available and not expired.
func (c *ClassifierCache) Get(toolName string, toolInput map[string]any) (ClassifierDecision, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := classifierCacheKey(toolName, toolInput)
	entry, ok := c.entries[key]
	if !ok {
		return "", false
	}
	// Expire after 5 minutes
	if time.Since(entry.createdAt) > 5*time.Minute {
		delete(c.entries, key)
		return "", false
	}
	return entry.decision, true
}

// Put stores a classifier decision in the cache.
func (c *ClassifierCache) Put(toolName string, toolInput map[string]any, decision ClassifierDecision) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := classifierCacheKey(toolName, toolInput)
	if _, exists := c.entries[key]; !exists {
		c.order = append(c.order, key)
	}
	c.entries[key] = classifierCacheEntry{
		decision:  decision,
		createdAt: time.Now(),
	}

	// Evict oldest if over capacity
	for len(c.entries) > c.maxSize && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}

// ClassifyToolUseWithCache wraps ClassifyToolUse with LRU caching.
func ClassifyToolUseWithCache(
	ctx context.Context,
	cfg *ClassifierConfig,
	cache *ClassifierCache,
	toolName string,
	toolInput map[string]any,
	conversationContext []*schema.Message,
) (ClassifierDecision, error) {
	if cache != nil {
		if cached, ok := cache.Get(toolName, toolInput); ok {
			return cached, nil
		}
	}

	decision, err := ClassifyToolUse(ctx, cfg, toolName, toolInput, conversationContext)
	if err != nil {
		return decision, err
	}

	if cache != nil {
		cache.Put(toolName, toolInput, decision)
	}
	return decision, nil
}
