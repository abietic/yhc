package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// UserQuestion represents a single question to present to the user.
type UserQuestion struct {
	Header      string           `json:"header,omitempty"`
	Question    string           `json:"question"`
	Options     []QuestionOption `json:"options,omitempty"`
	MultiSelect bool             `json:"multiSelect,omitempty"`
}

// QuestionOption represents a selectable option within a question.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

const askUserQuestionPrompt = `Use this tool when you need to ask the user questions during execution. This allows you to:
1. Gather user preferences or requirements
2. Clarify ambiguous instructions
3. Get decisions on implementation choices as you work
4. Offer choices to the user about what direction to take.

Usage notes:
- Users will always be able to select "Other" to provide custom text input
- Use multiSelect: true to allow multiple answers to be selected for a question
- If you recommend a specific option, make that the first option in the list and add "(Recommended)" at the end of the label

Plan mode note: In plan mode, use this tool to clarify requirements or choose between approaches BEFORE finalizing your plan. Do NOT use this tool to ask "Is my plan ready?" or "Should I proceed?" - use ExitPlanMode for plan approval. IMPORTANT: Do not reference "the plan" in your questions (e.g., "Do you have feedback about the plan?", "Does the plan look good?") because the user cannot see the plan in the UI until you call ExitPlanMode. If you need plan approval, use ExitPlanMode instead.
`

// AskUserQuestionTool returns a tool that asks the user questions during execution.
// Mirrors src/tools/AskUserQuestionTool from the reference.
func AskUserQuestionTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "AskUserQuestion",
			Desc: askUserQuestionPrompt,
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"questions": {
					Type: schema.Array,
					Desc: "Array of question objects to present to the user",
					ElemInfo: &schema.ParameterInfo{
						Type: schema.Object,
						SubParams: map[string]*schema.ParameterInfo{
							"header": {
								Type: schema.String,
								Desc: "Optional header/title for the question section",
							},
							"question": {
								Type:     schema.String,
								Desc:     "The question text to display to the user",
								Required: true,
							},
							"options": {
								Type: schema.Array,
								Desc: "Available options for the user to choose from",
								ElemInfo: &schema.ParameterInfo{
									Type: schema.Object,
									SubParams: map[string]*schema.ParameterInfo{
										"label": {
											Type:     schema.String,
											Desc:     "The option label displayed to the user",
											Required: true,
										},
										"description": {
											Type: schema.String,
											Desc: "Optional description providing more detail about this option",
										},
									},
								},
							},
							"multiSelect": {
								Type: schema.Boolean,
								Desc: "If true, the user can select multiple options",
							},
						},
					},
					Required: true,
				},
			}),
		},
		Execute: executeAskUserQuestion,
	}
}

func executeAskUserQuestion(input string) (string, error) {
	var params struct {
		Questions []UserQuestion    `json:"questions"`
		Answers   map[string]string `json:"answers,omitempty"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("ask_user: invalid params: %w", err)
	}
	if len(params.Questions) == 0 {
		return "", fmt.Errorf("ask_user: questions array is required and must not be empty")
	}

	// If answers are provided (from the interactive dialog), format as Q&A pairs.
	if len(params.Answers) > 0 {
		var result strings.Builder
		for i, q := range params.Questions {
			if i > 0 {
				result.WriteString("\n")
			}
			answer := params.Answers[q.Question]
			if answer == "" {
				answer = "(no answer)"
			}
			fmt.Fprintf(&result, "%q = %q", q.Question, answer)
		}
		return result.String(), nil
	}

	var result strings.Builder
	for i, q := range params.Questions {
		if i > 0 {
			result.WriteString("\n---\n\n")
		}

		if q.Header != "" {
			result.WriteString("## ")
			result.WriteString(q.Header)
			result.WriteString("\n\n")
		}

		if q.Question == "" {
			return "", fmt.Errorf("ask_user: question text is required for question #%d", i+1)
		}

		result.WriteString(q.Question)
		result.WriteString("\n")

		if q.MultiSelect {
			result.WriteString("(Multiple selections allowed)\n")
		}

		if len(q.Options) > 0 {
			result.WriteString("\n")
			for j, opt := range q.Options {
				fmt.Fprintf(&result, "  %d. %s", j+1, opt.Label)
				if opt.Description != "" {
					fmt.Fprintf(&result, " \xe2\x80\x94 %s", opt.Description)
				}
				result.WriteString("\n")
			}
			result.WriteString("  [Other: provide custom text input]\n")
		}
	}

	return result.String(), nil
}
