package engine

import "github.com/cloudwego/eino/schema"

func sideQuestionMessages(entries []string, question string) []*schema.Message {
	result := make([]*schema.Message, 0, len(entries)+1)
	for _, entry := range entries {
		result = append(result, &schema.Message{Role: schema.User, Content: "Conversation snapshot: " + entry})
	}
	return append(result, &schema.Message{Role: schema.User, Content: "Independent question: " + question})
}
