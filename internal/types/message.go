package types

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

func (m *Message) ToolCalls() []ContentBlock {
	var toolCalls []ContentBlock
	for _, b := range m.ContentBlocks {
		if b.Kind == ToolBlock {
			toolCalls = append(toolCalls, b)
		}
	}
	return toolCalls
}
