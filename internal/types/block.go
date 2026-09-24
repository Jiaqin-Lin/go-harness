package types

type BlockKind string

const (
	TextBlock     BlockKind = "text"
	ThinkingBlock BlockKind = "thinking"
	ToolBlock     BlockKind = "tool"
	ImageBlock    BlockKind = "image"
)

type ContentBlock struct {
	Kind BlockKind `json:"kind"`

	Text     string `json:"text,omitempty"`     // TextBlock
	Thinking string `json:"thinking,omitempty"` // ThinkingBlock

	ToolCallID string `json:"toolCallId,omitempty"` // ToolBlock
	ToolName   string `json:"toolName,omitempty"`   // ToolBlock
	Args       string `json:"args,omitempty"`       // ToolBlock

	ImageMime string `json:"imageMime,omitempty"` // ImageBlock
	ImageData string `json:"imageData,omitempty"` // ImageBlock
}

func NewTextBlock(text string) ContentBlock {
	return ContentBlock{Kind: TextBlock, Text: text}
}
