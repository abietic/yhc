package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateUserQuestions(t *testing.T) {
	valid := []UserQuestion{{Question: "Choose", Options: []QuestionOption{{Label: "one"}, {Label: "two"}}}}
	cases := []struct {
		name      string
		questions []UserQuestion
		wantErr   bool
	}{
		{name: "one free text", questions: []UserQuestion{{Question: "Choose"}}},
		{name: "four questions", questions: []UserQuestion{{Question: "one"}, {Question: "two"}, {Question: "three"}, {Question: "four"}}},
		{name: "zero questions", wantErr: true},
		{name: "five questions", questions: append(append([]UserQuestion{}, valid...), valid[0], valid[0], valid[0], valid[0]), wantErr: true},
		{name: "one option", questions: []UserQuestion{{Question: "Choose", Options: []QuestionOption{{Label: "one"}}}}, wantErr: true},
		{name: "four options", questions: []UserQuestion{{Question: "Choose", Options: []QuestionOption{{Label: "1"}, {Label: "2"}, {Label: "3"}, {Label: "4"}}}}},
		{name: "five options", questions: []UserQuestion{{Question: "Choose", Options: []QuestionOption{{Label: "1"}, {Label: "2"}, {Label: "3"}, {Label: "4"}, {Label: "5"}}}}, wantErr: true},
		{name: "blank required text", questions: []UserQuestion{{Question: " \t "}}, wantErr: true},
		{name: "blank label", questions: []UserQuestion{{Question: "Choose", Options: []QuestionOption{{Label: " "}, {Label: "two"}}}}, wantErr: true},
		{name: "header boundary", questions: []UserQuestion{{Header: strings.Repeat("h", MaxAskUserHeaderRunes), Question: "Choose"}}},
		{name: "header over boundary", questions: []UserQuestion{{Header: strings.Repeat("h", MaxAskUserHeaderRunes+1), Question: "Choose"}}, wantErr: true},
		{name: "question boundary", questions: []UserQuestion{{Question: strings.Repeat("q", MaxAskUserQuestionRunes)}}},
		{name: "question over boundary", questions: []UserQuestion{{Question: strings.Repeat("q", MaxAskUserQuestionRunes+1)}}, wantErr: true},
		{name: "label boundary", questions: []UserQuestion{{Question: "Choose", Options: []QuestionOption{{Label: strings.Repeat("l", MaxAskUserOptionLabelRunes)}, {Label: "two"}}}}},
		{name: "label over boundary", questions: []UserQuestion{{Question: "Choose", Options: []QuestionOption{{Label: strings.Repeat("l", MaxAskUserOptionLabelRunes+1)}, {Label: "two"}}}}, wantErr: true},
		{name: "description boundary", questions: []UserQuestion{{Question: "Choose", Options: []QuestionOption{{Label: "one", Description: strings.Repeat("d", MaxAskUserOptionDescriptionRunes)}, {Label: "two"}}}}},
		{name: "description over boundary", questions: []UserQuestion{{Question: "Choose", Options: []QuestionOption{{Label: "one", Description: strings.Repeat("d", MaxAskUserOptionDescriptionRunes+1)}, {Label: "two"}}}}, wantErr: true},
		{name: "invalid UTF-8", questions: []UserQuestion{{Question: string([]byte{0xff})}}, wantErr: true},
		{name: "duplicate normalized question", questions: []UserQuestion{{Question: " Pick  one "}, {Question: "pick\t one"}}, wantErr: true},
		{name: "duplicate normalized option", questions: []UserQuestion{{Question: "Choose", Options: []QuestionOption{{Label: " Pick  one "}, {Label: "pick\t one"}}}}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateUserQuestions(tc.questions)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateUserQuestions() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateUserQuestionsEncodedPayloadBoundary(t *testing.T) {
	questions := make([]UserQuestion, MaxAskUserQuestions)
	for questionIndex := range questions {
		questions[questionIndex] = UserQuestion{Question: strings.Repeat(string(rune('a'+questionIndex)), MaxAskUserQuestionRunes)}
		for optionIndex := 0; optionIndex < MaxAskUserQuestionOptions; optionIndex++ {
			questions[questionIndex].Options = append(questions[questionIndex].Options, QuestionOption{Label: string(rune('a' + optionIndex))})
		}
	}
	for padding := 0; ; padding++ {
		remaining := padding
		for questionIndex := range questions {
			for optionIndex := range questions[questionIndex].Options {
				length := min(remaining, MaxAskUserOptionDescriptionRunes)
				questions[questionIndex].Options[optionIndex].Description = strings.Repeat("d", length)
				remaining -= length
			}
		}
		encoded, err := json.Marshal(questions)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) == MaxAskUserQuestionsBytes {
			break
		}
		if len(encoded) > MaxAskUserQuestionsBytes {
			t.Fatal("could not construct exact payload boundary")
		}
	}
	if err := ValidateUserQuestions(questions); err != nil {
		t.Fatalf("exact payload rejected: %v", err)
	}
	questions[MaxAskUserQuestions-1].Options[MaxAskUserQuestionOptions-1].Description += "d"
	if err := ValidateUserQuestions(questions); err == nil {
		t.Fatal("over-limit payload accepted")
	}
}
