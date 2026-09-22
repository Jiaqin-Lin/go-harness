package types

type BlockKind string

const (
	TextBlock     BlockKind = "text"
	ThinkingBlock BlockKind = "thinking"
	ToolBlock     BlockKind = "tool"
	ImageBlock    BlockKind = "image"
)

type ContentBlock struct {
	Kind BlockKind

	Text     string // TextBlock
	Thinking string // ThinkingBlock

	ToolCallID string // ToolBlock
	ToolName   string // ToolBlock
	Args       string // ToolBlock

	ImageMime string // ImageBlock
	ImageData string // ImageBlock
}

func NewTextBlock(text string) ContentBlock {
	return ContentBlock{Kind: TextBlock, Text: text}
}
