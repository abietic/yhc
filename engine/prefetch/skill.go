package prefetch

import (
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/abietic/yhc/engine/skills"
	"github.com/cloudwego/eino/schema"
)

// SkillPrefetch prefetches skill content based on the latest user message.
// It inspects the last user message for skill-trigger keywords and preloads
// matching skill content as attachment messages for the query context.
type SkillPrefetch struct {
	registry *skills.SkillRegistry
	mu       sync.Mutex
	done     chan struct{}
	result   []*schema.Message
}

// NewSkillPrefetch creates a SkillPrefetch backed by a skill registry.
// If registry is nil, the prefetch is a no-op.
func NewSkillPrefetch(registry *skills.SkillRegistry) *SkillPrefetch {
	return &SkillPrefetch{registry: registry}
}

// Start starts a non-blocking skill discovery prefetch.
func (p *SkillPrefetch) Start(messages []*schema.Message) {
	if p.registry == nil || len(messages) == 0 {
		return
	}
	lastUserMsg := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i] != nil && messages[i].Role == schema.User {
			lastUserMsg = messages[i].Content
			break
		}
	}
	if lastUserMsg == "" {
		return
	}

	// Claim the start before returning. Collect must not be able to consume
	// the worker's right to run while that goroutine is still unscheduled.
	p.mu.Lock()
	if p.done != nil {
		p.mu.Unlock()
		return
	}
	done := make(chan struct{})
	p.done = done
	p.mu.Unlock()

	go func() {
		defer close(done)
		p.result = p.findMatchingSkills(lastUserMsg)
	}()
}

// Collect waits for an accepted Start and returns its attachment messages.
// Without a Start it returns nil, leaving a later valid Start available.
func (p *SkillPrefetch) Collect() []*schema.Message {
	p.mu.Lock()
	done := p.done
	p.mu.Unlock()
	if done == nil {
		return nil
	}
	// Closing done publishes result to every collector. There is only one
	// worker per instance and the result is not changed after publication.
	<-done
	return p.result
}

func (p *SkillPrefetch) findMatchingSkills(userMsg string) []*schema.Message {
	available := p.registry.List()
	if len(available) == 0 {
		return nil
	}

	lower := strings.ToLower(userMsg)
	var matches []*schema.Message

	for _, skill := range available {
		if skill == nil || skill.Name == "" || skill.DisableModelInvocation {
			continue
		}
		if !p.skillMatchesMessage(skill, lower) {
			continue
		}
		// Render the same detached generation that supplied the source path and
		// invocation flags, rather than looking up its name a second time.
		content, err := skill.Render(nil)
		if err != nil || strings.TrimSpace(content) == "" {
			continue
		}
		prefix := ""
		if skill.FilePath != "" {
			prefix = skill.InvocationPrompt("")
		}
		// Never truncate the locator itself. The full skill remains available
		// through explicit invocation when its header cannot fit prefetch.
		const maxContentBytes = 4000
		if len(prefix) >= maxContentBytes {
			continue
		}
		if limit := maxContentBytes - len(prefix); len(content) > limit {
			end := limit
			for end > 0 && !utf8.RuneStart(content[end]) {
				end--
			}
			content = content[:end] + "\n...(truncated)"
		}
		matches = append(matches, &schema.Message{
			Role:    schema.User,
			Content: prefix + content,
			Extra: map[string]any{
				"is_meta":         true,
				"attachment_kind": "skill_prefetch",
				"skill_name":      skill.Name,
			},
		})
		if len(matches) >= 2 {
			break
		}
	}
	return matches
}

// skillMatchesMessage checks if a skill matches the user message by name, tags,
// or description keywords.
func (p *SkillPrefetch) skillMatchesMessage(skill *skills.Skill, lowerMsg string) bool {
	// Match on skill name.
	if strings.Contains(lowerMsg, strings.ToLower(skill.Name)) {
		return true
	}
	// Match on tags.
	for _, tag := range skill.Tags {
		if tag != "" && strings.Contains(lowerMsg, strings.ToLower(tag)) {
			return true
		}
	}
	// Match on description keywords (words >= 4 chars for relevance).
	if skill.Description != "" {
		for _, word := range strings.Fields(skill.Description) {
			word = strings.ToLower(strings.Trim(word, ".,;:!?()"))
			if len(word) >= 4 && strings.Contains(lowerMsg, word) {
				return true
			}
		}
	}
	return false
}
