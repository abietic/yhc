package prefetch

import (
	"strings"
	"sync"

	"github.com/abietic/yhc/engine/skills"
	"github.com/cloudwego/eino/schema"
)

// SkillPrefetch prefetches skill content based on the latest user message.
// It inspects the last user message for skill-trigger keywords and preloads
// matching skill content as attachment messages for the query context.
type SkillPrefetch struct {
	registry *skills.SkillRegistry
	once     sync.Once
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

	go p.once.Do(func() {
		p.result = p.findMatchingSkills(lastUserMsg)
	})
}

// Collect returns prefetched skill content as attachment messages.
func (p *SkillPrefetch) Collect() []*schema.Message {
	p.once.Do(func() {}) // ensure Start completed
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
		if skill == nil || skill.Name == "" {
			continue
		}
		if !p.skillMatchesMessage(skill, lower) {
			continue
		}
		content, err := p.registry.Invoke(skill.Name, nil)
		if err != nil || strings.TrimSpace(content) == "" {
			continue
		}
		if len(content) > 4000 {
			content = content[:4000] + "\n...(truncated)"
		}
		matches = append(matches, &schema.Message{
			Role:    schema.User,
			Content: content,
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
