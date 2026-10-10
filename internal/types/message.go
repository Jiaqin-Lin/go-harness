package types

import "strings"

type Role string

const (
	UserRole       Role = "user"
	AssistantRole  Role = "assistant"
	ToolResultRole Role = "tool_result"
)

type StopReason string

const (
	PendingStop  StopReason = "pending"
	NormalStop   StopReason = "normal"
	LengthStop   StopReason = "length"
	ToolUserStop StopReason = "tool_use"
	ErrorStop    StopReason = "error"
	AbortedStop  StopReason = "aborted"
)

// 下面这些小写 tag 是会话文件用的（internal/session）：JSONL 里存的就是这个结构本身，
// 不另立一套持久化模型。发给模型的 wire 格式由 provider 自己转换，跟这里无关
type Usage struct {
	Input  int `json:"input"`
	Cached int `json:"cached"`
	Output int `json:"output"`
	Total  int `json:"total"`
}

func (u *Usage) CacheHitRate() float64 {
	if u.Input <= 0 {
		return 0
	}
	return float64(u.Cached) / float64(u.Input)
}

type Message struct {
	Role Role `json:"role"`

	ContentBlocks []ContentBlock `json:"contentBlocks,omitempty"`

	Usage        *Usage     `json:"usage,omitempty"`
	StopReason   StopReason `json:"stopReason,omitempty"`
	ErrorMessage string     `json:"errorMessage,omitempty"`

	ToolCallID string `json:"toolCallId,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
	IsError    bool   `json:"isError,omitempty"`
}

// NewToolResult 造一条 tool_result 消息。每个 tool_call 都得有配对的它 ——
// 工具真的跑完、被中断时补的、发请求前合成的，形状都是这一个
func NewToolResult(callID, toolName string, blocks []ContentBlock, isError bool) *Message {
	return &Message{
		Role:          ToolResultRole,
		ContentBlocks: blocks,
		ToolCallID:    callID,
		ToolName:      toolName,
		IsError:       isError,
	}
}

// Text 是消息的正文：只有文本块算数，thinking / 工具调用 / 图片都不算。
// 会话列表的兜底标题、标题生成的输入要的都是它
func (m *Message) Text() string { return TextOf(m.ContentBlocks) }

func TextOf(blocks []ContentBlock) string {
	var parts []string
	for _, b := range blocks {
		if b.Kind == TextBlock {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "")
}

func (m *Message) ToolCalls() []ContentBlock {
	var toolCalls []ContentBlock
	for _, b := range m.ContentBlocks {
		if b.Kind == ToolBlock {
			toolCalls = append(toolCalls, b)
		}
	}
	return toolCalls
}
