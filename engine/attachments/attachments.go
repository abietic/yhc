package attachments

import "github.com/cloudwego/eino/schema"

// Processor generates attachment messages between turns.
type Processor struct{}

// NewProcessor creates a new attachment processor.
func NewProcessor() *Processor { return &Processor{} }

// GetAttachments generates attachment messages. v1: pass-through.
// Mirrors query.ts:1580-1657.
func (p *Processor) GetAttachments(
	messages []*schema.Message,
	toolResults []*schema.Message,
) []*schema.Message {
	return nil
}
