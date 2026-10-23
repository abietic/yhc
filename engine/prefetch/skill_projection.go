package prefetch

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// ProjectSkillToolResults shortens redundant Skill results only in the active
// model input. Canonical results and history remain complete for compaction and
// resume. Only an exact, preceding plain-text prefetch can supply the reference;
// later attachments cannot rewrite an already sent prefix.
func ProjectSkillToolResults(active []*schema.Message) []*schema.Message {
	visible := make(map[skillPrefetchKey]struct{})
	pending := make(map[string]string)
	seenCalls := make(map[string]bool)
	projected := active
	copied := false
	for index, message := range active {
		if message == nil {
			continue
		}
		if key, ok := prefetchedSkillKey(message); ok && plainSkillMessage(message) &&
			!strings.HasSuffix(message.Content, "\n...(truncated)") {
			visible[key] = struct{}{}
		}
		if message.Role == schema.Assistant {
			for _, call := range message.ToolCalls {
				if call.ID == "" {
					continue
				}
				if seenCalls[call.ID] {
					delete(pending, call.ID)
					continue
				}
				seenCalls[call.ID] = true
				if call.Function.Name != "Skill" {
					continue
				}
				var input struct {
					Skill string `json:"skill"`
				}
				if json.Unmarshal([]byte(call.Function.Arguments), &input) == nil && input.Skill != "" {
					pending[call.ID] = input.Skill
				}
			}
		}
		if message.Role != schema.Tool {
			continue
		}
		name, matched := pending[message.ToolCallID]
		delete(pending, message.ToolCallID)
		if !matched || message.ToolName != "Skill" || !plainSkillMessage(message) {
			continue
		}
		if isError, present := message.Extra["is_error"]; present && isError != false {
			continue
		}
		if _, present := visible[skillPrefetchKey{name: name, content: message.Content}]; !present {
			continue
		}
		reference := skillResultReference(name, message.Content)
		if reference == "" || len(reference) >= len(message.Content) {
			continue
		}
		if !copied {
			projected = append([]*schema.Message(nil), active...)
			copied = true
		}
		clone := *message
		clone.Content = reference
		projected[index] = &clone
	}
	return projected
}

func plainSkillMessage(message *schema.Message) bool {
	return len(message.MultiContent) == 0 && len(message.UserInputMultiContent) == 0 &&
		len(message.AssistantGenMultiContent) == 0 && len(message.ToolCalls) == 0 && message.ReasoningContent == ""
}

func skillResultReference(name, content string) string {
	reference := fmt.Sprintf("Skill %q instructions are already present above; follow that guidance.", name)
	if strings.HasPrefix(content, "Skill: ") {
		// Preserve the complete source locator produced by InvocationPrompt.
		// Unrecognized headers (including paths with newlines) stay unchanged.
		header, _, found := strings.Cut(content, "\n\n")
		lines := strings.Split(header, "\n")
		if !found || len(lines) != 3 || lines[0] != "Skill: "+name ||
			!strings.HasPrefix(lines[1], "Source: ") || len(lines[1]) == len("Source: ") ||
			!strings.HasPrefix(lines[2], "Base directory: ") || len(lines[2]) == len("Base directory: ") {
			return ""
		}
		reference = header + "\n\n" + reference
	}
	return reference
}
