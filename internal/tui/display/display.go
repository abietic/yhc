// Package display implements display-related utilities for the TUI.
// Mirrors src/utils/displayTags.ts, src/types/connectorText.ts,
// and src/utils/argumentSubstitution.ts from the reference.
package display

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// --- Display Tags (src/utils/displayTags.ts) ---

// xmlTagOpenPattern matches opening tags to find tag names for stripping.
var xmlTagOpenPattern = regexp.MustCompile(`<([a-z][\w-]*)(?:\s[^>]*)?>`)

// ideContextTagNames are the IDE-injected tags to strip.
var ideContextTagNames = map[string]bool{
	"ide_opened_file": true,
	"ide_selection":   true,
}

// StripDisplayTags strips XML-like tag blocks from text for use in UI titles.
// System-injected context arrives wrapped in tags and should never surface as a title.
// If stripping would result in empty text, returns the original unchanged.
func StripDisplayTags(text string) string {
	result := strings.TrimSpace(stripAllTagBlocks(text))
	if result == "" {
		return text
	}
	return result
}

// StripDisplayTagsAllowEmpty is like StripDisplayTags but returns empty string
// when all content is tags. Used to detect command-only prompts.
func StripDisplayTagsAllowEmpty(text string) string {
	return strings.TrimSpace(stripAllTagBlocks(text))
}

// StripIdeContextTags strips only IDE-injected context tags (ide_opened_file, ide_selection).
// Used by text resubmit so UP-arrow resubmit preserves user-typed content.
func StripIdeContextTags(text string) string {
	return strings.TrimSpace(stripTagBlocksByName(text, ideContextTagNames))
}

// stripAllTagBlocks removes all <tag>...</tag> blocks where tag is lowercase.
func stripAllTagBlocks(text string) string {
	result := text
	for {
		matches := xmlTagOpenPattern.FindStringIndex(result)
		if matches == nil {
			break
		}
		// Extract tag name
		submatch := xmlTagOpenPattern.FindStringSubmatch(result[matches[0]:])
		if len(submatch) < 2 {
			break
		}
		tagName := submatch[0]
		_ = tagName
		name := submatch[1]
		closeTag := "</" + name + ">"
		closeIdx := strings.Index(result[matches[1]:], closeTag)
		if closeIdx < 0 {
			break
		}
		end := matches[1] + closeIdx + len(closeTag)
		// Remove trailing newline if present
		if end < len(result) && result[end] == '\n' {
			end++
		}
		result = result[:matches[0]] + result[end:]
	}
	return result
}

// stripTagBlocksByName removes only <tag>...</tag> blocks with specific tag names.
func stripTagBlocksByName(text string, names map[string]bool) string {
	result := text
	for {
		matches := xmlTagOpenPattern.FindStringIndex(result)
		if matches == nil {
			break
		}
		submatch := xmlTagOpenPattern.FindStringSubmatch(result[matches[0]:])
		if len(submatch) < 2 {
			break
		}
		name := submatch[1]
		if !names[name] {
			// Skip this tag, advance past it
			result = result[:matches[0]] + "\x00" + result[matches[0]+1:]
			continue
		}
		closeTag := "</" + name + ">"
		closeIdx := strings.Index(result[matches[1]:], closeTag)
		if closeIdx < 0 {
			break
		}
		end := matches[1] + closeIdx + len(closeTag)
		if end < len(result) && result[end] == '\n' {
			end++
		}
		result = result[:matches[0]] + result[end:]
	}
	// Restore any sentinel chars
	result = strings.ReplaceAll(result, "\x00", "<")
	return result
}

// --- Connector Text (src/types/connectorText.ts) ---

// ConnectorTextBlock represents a connector_text content block.
type ConnectorTextBlock struct {
	Type          string `json:"type"` // always "connector_text"
	ConnectorText string `json:"connector_text"`
}

// IsConnectorTextBlock checks if a value is a ConnectorTextBlock.
func IsConnectorTextBlock(block map[string]interface{}) bool {
	typ, _ := block["type"].(string)
	_, hasText := block["connector_text"].(string)
	return typ == "connector_text" && hasText
}

// ConnectorTextToTextBlock converts a connector_text block to a regular text block.
func ConnectorTextToTextBlock(block ConnectorTextBlock) map[string]interface{} {
	return map[string]interface{}{
		"type": "text",
		"text": block.ConnectorText,
	}
}

// --- Argument Substitution (src/utils/argumentSubstitution.ts) ---

// ParseArguments parses an arguments string into an array of individual arguments.
// Handles quoted strings (single and double quotes).
func ParseArguments(args string) []string {
	if strings.TrimSpace(args) == "" {
		return nil
	}

	var result []string
	var current strings.Builder
	inSingle := false
	inDouble := false

	for i := 0; i < len(args); i++ {
		ch := args[i]
		switch {
		case ch == '\'' && !inDouble:
			inSingle = !inSingle
		case ch == '"' && !inSingle:
			inDouble = !inDouble
		case (ch == ' ' || ch == '\t') && !inSingle && !inDouble:
			if current.Len() > 0 {
				result = append(result, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		result = append(result, current.String())
	}

	return result
}

// ParseArgumentNames parses argument names from frontmatter.
// Accepts either a space-separated string or returns the input slice.
func ParseArgumentNames(input string) []string {
	if strings.TrimSpace(input) == "" {
		return nil
	}
	names := strings.Fields(input)
	var valid []string
	for _, name := range names {
		if name != "" && !isNumericOnly(name) {
			valid = append(valid, name)
		}
	}
	return valid
}

// GenerateProgressiveArgumentHint shows remaining unfilled args.
func GenerateProgressiveArgumentHint(argNames, typedArgs []string) string {
	remaining := argNames[len(typedArgs):]
	if len(remaining) == 0 {
		return ""
	}
	parts := make([]string, len(remaining))
	for i, name := range remaining {
		parts[i] = fmt.Sprintf("[%s]", name)
	}
	return strings.Join(parts, " ")
}

// SubstituteArguments substitutes $ARGUMENTS placeholders in content with actual values.
//
// Supports:
//   - $ARGUMENTS - replaced with the full arguments string
//   - $ARGUMENTS[0], $ARGUMENTS[1], etc. - replaced with individual indexed arguments
//   - $0, $1, etc. - shorthand for indexed arguments
//   - Named arguments (e.g., $foo, $bar) - when argument names are defined
func SubstituteArguments(content, args string, appendIfNoPlaceholder bool, argumentNames []string) string {
	if args == "" {
		return content
	}

	parsedArgs := ParseArguments(args)
	originalContent := content

	// Replace named arguments ($name -> value by position)
	for i, name := range argumentNames {
		if name == "" {
			continue
		}
		pattern := regexp.MustCompile(`\$` + regexp.QuoteMeta(name) + `(?![[\w])`)
		val := ""
		if i < len(parsedArgs) {
			val = parsedArgs[i]
		}
		content = pattern.ReplaceAllString(content, val)
	}

	// Replace indexed arguments ($ARGUMENTS[0], $ARGUMENTS[1], etc.)
	indexedPattern := regexp.MustCompile(`\$ARGUMENTS\[(\d+)\]`)
	content = indexedPattern.ReplaceAllStringFunc(content, func(match string) string {
		submatch := indexedPattern.FindStringSubmatch(match)
		if len(submatch) < 2 {
			return match
		}
		idx, err := strconv.Atoi(submatch[1])
		if err != nil || idx >= len(parsedArgs) {
			return ""
		}
		return parsedArgs[idx]
	})

	// Replace shorthand indexed arguments ($0, $1, etc.)
	shorthandPattern := regexp.MustCompile(`\$(\d+)`)
	content = shorthandPattern.ReplaceAllStringFunc(content, func(match string) string {
		submatch := shorthandPattern.FindStringSubmatch(match)
		if len(submatch) < 2 {
			return match
		}
		idx, err := strconv.Atoi(submatch[1])
		if err != nil || idx >= len(parsedArgs) {
			return ""
		}
		return parsedArgs[idx]
	})

	// Replace $ARGUMENTS with the full arguments string
	content = strings.ReplaceAll(content, "$ARGUMENTS", args)

	// If no placeholders were found, append arguments
	if content == originalContent && appendIfNoPlaceholder && args != "" {
		content = content + "\n\nARGUMENTS: " + args
	}

	return content
}

func isNumericOnly(s string) bool {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return len(s) > 0
}
