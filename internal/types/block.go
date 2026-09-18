package types

type BlockKind string

const (
	TextBlock     BlockKind = "text"
	ThinkingBlock BlockKind = "thinking"
	ToolBlock     BlockKind = "tool"
	ImageBlock    BlockKind = "image"
)

// ContentBlock 是所有内容块的统一表示，Kind 决定用哪组字段。
type ContentBlock struct {
	Kind BlockKind

	Text     string // TextBlock
	Thinking string // ThinkingBlock

	ToolCallID   string         // ToolBlock
	ToolCallName string         // ToolBlock
	Args         map[string]any // ToolBlock

	ImageMime string // ImageBlock
	ImageData string // ImageBlock
}

func NewTextBlock(text string) ContentBlock {
	return ContentBlock{Kind: TextBlock, Text: text}
}
