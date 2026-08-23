package services

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

// PromptSuggestionModelFn generates prompt suggestions.
type PromptSuggestionModelFn func(ctx context.Context, conversation []string) (string, error)

const suggestionPrompt = `[SUGGESTION MODE: Suggest what the user might naturally type next into YHC.]

FIRST: Look at the user's recent messages and original request.

Your job is to predict what THEY would type - not what you think they should do.

THE TEST: Would they think "I was just about to type that"?

EXAMPLES:
User asked "fix the bug and run tests", bug is fixed → "run the tests"
After code written → "try it out"
Claude offers options → suggest the one the user would likely pick, based on conversation
Claude asks to continue → "yes" or "go ahead"
Task complete, obvious follow-up → "commit this" or "push it"
After error or misunderstanding → silence (let them assess/correct)

Be specific: "run the tests" beats "continue".

NEVER SUGGEST:
- Evaluative ("looks good", "thanks")
- Questions ("what about...?")
- Claude-voice ("Let me...", "I'll...", "Here's...")
- New ideas they didn't ask about
- Multiple sentences

Stay silent if the next step isn't obvious from what the user said.

Format: 2-12 words, or one short phrase in languages without spaces. Match the user's style. Or nothing.

Reply with ONLY the suggestion, no quotes or explanation.`

// SuggestionSuppressReason returns a reason string if suggestion generation
// should be suppressed, or empty string if allowed.
//
// Reference: src/services/PromptSuggestion/promptSuggestion.ts getSuggestionSuppressReason
func SuggestionSuppressReason(enabled, inPlanMode, hasPendingPermission bool) string {
	if !enabled {
		return "disabled"
	}
	if hasPendingPermission {
		return "pending_permission"
	}
	if inPlanMode {
		return "plan_mode"
	}
	return ""
}

var (
	silencePattern     = regexp.MustCompile(`\bsilence is\b|\bstay(s|ing)? silent\b`)
	bareSilencePattern = regexp.MustCompile(`^\W*silence\W*$`)
	metaWrappedPattern = regexp.MustCompile(`^\(.*\)$|^\[.*\]$`)
	prefixedLabel      = regexp.MustCompile(`^\w+:\s`)
	multiSentence      = regexp.MustCompile(`[.!?]\s+[A-Z]`)
	hasFormatting      = regexp.MustCompile(`[\n*]|\*\*`)
	evaluativePattern  = regexp.MustCompile(`(?i)thanks|thank you|looks good|sounds good|that works|that worked|that's all|nice|great|perfect|makes sense|awesome|excellent`)
	claudeVoicePattern = regexp.MustCompile(`(?i)^(let me|i'll|i've|i'm|i can|i would|i think|i notice|here's|here is|here are|that's|this is|this will|you can|you should|you could|sure,|of course|certainly)`)
)

var allowedSingleWords = map[string]bool{
	"yes": true, "yeah": true, "yep": true, "yea": true, "yup": true,
	"sure": true, "ok": true, "okay": true,
	"push": true, "commit": true, "deploy": true, "stop": true,
	"continue": true, "check": true, "exit": true, "quit": true,
	"no": true,
}

func containsCJKLetter(value string) bool {
	for _, char := range value {
		if unicode.In(
			char,
			unicode.Han,
			unicode.Hiragana,
			unicode.Katakana,
			unicode.Hangul,
		) {
			return true
		}
	}
	return false
}

// FilterSuggestion returns a reason string if the suggestion should be
// filtered out, or empty string if the suggestion is acceptable.
// Ports the 12-filter chain from the reference.
//
// Reference: src/services/PromptSuggestion/promptSuggestion.ts shouldFilterSuggestion
func FilterSuggestion(suggestion string) string {
	if suggestion == "" {
		return "empty"
	}

	lower := strings.ToLower(suggestion)
	words := strings.Fields(strings.TrimSpace(suggestion))
	wordCount := len(words)

	// done
	if lower == "done" {
		return "done"
	}

	// meta_text
	if lower == "nothing found" || lower == "nothing found." ||
		strings.HasPrefix(lower, "nothing to suggest") ||
		strings.HasPrefix(lower, "no suggestion") ||
		silencePattern.MatchString(lower) ||
		bareSilencePattern.MatchString(lower) {
		return "meta_text"
	}

	// meta_wrapped
	if metaWrappedPattern.MatchString(suggestion) {
		return "meta_wrapped"
	}

	// error_message
	if strings.HasPrefix(lower, "api error:") ||
		strings.HasPrefix(lower, "prompt is too long") ||
		strings.HasPrefix(lower, "request timed out") ||
		strings.HasPrefix(lower, "invalid api key") ||
		strings.HasPrefix(lower, "image was too large") {
		return "error_message"
	}

	// prefixed_label
	if prefixedLabel.MatchString(suggestion) {
		return "prefixed_label"
	}

	// too_few_words
	if wordCount < 2 {
		if strings.HasPrefix(suggestion, "/") {
			// slash commands are valid
		} else if !allowedSingleWords[lower] && !containsCJKLetter(suggestion) {
			return "too_few_words"
		}
	}

	// too_many_words
	if wordCount > 12 {
		return "too_many_words"
	}

	// too_long
	if len(suggestion) >= 100 {
		return "too_long"
	}

	// multiple_sentences
	if multiSentence.MatchString(suggestion) {
		return "multiple_sentences"
	}

	// has_formatting
	if hasFormatting.MatchString(suggestion) {
		return "has_formatting"
	}

	// evaluative
	if evaluativePattern.MatchString(suggestion) {
		return "evaluative"
	}

	// claude_voice
	if claudeVoicePattern.MatchString(suggestion) {
		return "claude_voice"
	}

	return ""
}

// GetSuggestionPrompt returns the prompt text used for suggestion generation.
func GetSuggestionPrompt() string {
	return suggestionPrompt
}

// PromptSuggestionService manages prompt suggestion generation with filtering
// and speculative pre-computation.
//
// Reference: src/services/PromptSuggestion/promptSuggestion.ts +
//
//	src/services/PromptSuggestion/speculation.ts
type PromptSuggestionService struct {
	mu                sync.Mutex
	modelFn           PromptSuggestionModelFn
	enabled           bool
	speculativeResult string
	speculativeCancel context.CancelFunc
	minTurns          int
}

// NewPromptSuggestionService creates a prompt suggestion service.
func NewPromptSuggestionService(modelFn PromptSuggestionModelFn) *PromptSuggestionService {
	return &PromptSuggestionService{
		modelFn:  modelFn,
		enabled:  true,
		minTurns: 2,
	}
}

// SetEnabled enables or disables prompt suggestions.
func (p *PromptSuggestionService) SetEnabled(enabled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.enabled = enabled
}

// IsEnabled returns whether suggestions are enabled.
func (p *PromptSuggestionService) IsEnabled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.enabled
}

// GenerateSuggestion produces a filtered suggested next prompt.
// Returns empty string if the suggestion is suppressed or filtered.
func (p *PromptSuggestionService) GenerateSuggestion(ctx context.Context, conversation []string, assistantTurnCount int) string {
	p.mu.Lock()
	enabled := p.enabled
	minTurns := p.minTurns
	p.mu.Unlock()

	if !enabled || p.modelFn == nil || len(conversation) == 0 {
		return ""
	}

	if assistantTurnCount < minTurns {
		return ""
	}

	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	result, err := p.modelFn(callCtx, conversation)
	if err != nil {
		return ""
	}

	suggestion := strings.TrimSpace(result)
	if reason := FilterSuggestion(suggestion); reason != "" {
		return ""
	}

	return suggestion
}

// StartSpeculation begins computing a suggestion in the background.
// Call after the model produces a response. Returns a cancel function.
func (p *PromptSuggestionService) StartSpeculation(ctx context.Context, conversation []string) context.CancelFunc {
	p.mu.Lock()
	if !p.enabled || p.modelFn == nil {
		p.mu.Unlock()
		return func() {}
	}
	if p.speculativeCancel != nil {
		p.speculativeCancel()
	}
	p.speculativeResult = ""
	p.mu.Unlock()

	specCtx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	p.speculativeCancel = cancel
	p.mu.Unlock()

	go func() {
		result, err := p.modelFn(specCtx, conversation)
		if err != nil || specCtx.Err() != nil {
			return
		}
		suggestion := strings.TrimSpace(result)
		if reason := FilterSuggestion(suggestion); reason != "" {
			return
		}
		p.mu.Lock()
		p.speculativeResult = suggestion
		p.mu.Unlock()
	}()
	return cancel
}

// GetSpeculativeResult returns the pre-computed suggestion if available.
// Clears the result after retrieval (one-shot).
func (p *PromptSuggestionService) GetSpeculativeResult() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := p.speculativeResult
	p.speculativeResult = ""
	return result
}

// AbortSpeculation cancels any in-flight speculative computation.
func (p *PromptSuggestionService) AbortSpeculation() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.speculativeCancel != nil {
		p.speculativeCancel()
		p.speculativeCancel = nil
	}
	p.speculativeResult = ""
}
