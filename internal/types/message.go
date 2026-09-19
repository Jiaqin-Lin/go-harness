package types

type Role string

const (
	UserRole       Role = "user"
	AssistantRole  Role = "assistant"
	ToolResultRole Role = "tool_result"
)

type StopReason string

const (
	NormalStop   StopReason = "normal"
	LengthStop   StopReason = "length"
	ToolUserStop StopReason = "tool_use"
	ErrorStop    StopReason = "error"
	AbortedStop  StopReason = "aborted"
)

type Usage struct {
	Input  int
	Cached int
	Output int
	Total  int
}

func (u *Usage) CacheHitRate() float64 {
	if u.Input <= 0 {
		return 0
	}
	return float64(u.Cached) / float64(u.Input)
}

type Message struct {
	Role Role

	ContentBlocks []ContentBlock

	Usage        *Usage
	StopReason   StopReason
	ErrorMessage string

	ToolCallID string
	ToolName   string
	IsError    bool
}
