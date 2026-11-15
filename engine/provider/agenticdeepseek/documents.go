package agenticdeepseek

import "github.com/cloudwego/eino/schema"

const documentFileIDExtraKey = "_agenticdeepseek_document_file_id"

// NewFileIDDocumentBlock references an explicitly uploaded document, including
// text_recognition and rich_layout_recognition resources. It never uploads a
// file implicitly. Only user input on Flash accepts these references.
func NewFileIDDocumentBlock(fileID string) *schema.ContentBlock {
	return &schema.ContentBlock{
		Type:          schema.ContentBlockTypeUserInputFile,
		UserInputFile: &schema.UserInputFile{},
		Extra:         map[string]any{documentFileIDExtraKey: fileID},
	}
}
