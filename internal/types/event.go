package types

type EventType string

const (
	AgentStartEvent EventType = "agent_start"
	AgentEndEvent   EventType = "agent_end"

	TurnStartEvent EventType = "turn_start"
	TurnEndEvent   EventType = "turn_end"

	MessageStartEvent   EventType = "message_start"
	MessagePartialEvent EventType = "message_partial"
	MessageEndEvent     EventType = "message_end"

	ToolStartEvent   EventType = "tool_start"
	ToolPartialEvent EventType = "tool_partial"
	ToolEndEvent     EventType = "tool_end"
)

// 出现在 message_partial 里
type AssistantEventType string

const (
	AssistantStartEvent         AssistantEventType = "start"
	AssistantTextDeltaEvent     AssistantEventType = "text_delta"
	AssistantThinkingDeltaEvent AssistantEventType = "thinking_delta"
	AssistantToolCallStartEvent AssistantEventType = "tool_call_start"
	AssistantToolCallDeltaEvent AssistantEventType = "tool_call_delta"
	AssistantToolCallEndEvent   AssistantEventType = "tool_call_end"
	AssistantDoneEvent          AssistantEventType = "done"
	AssistantErrorEvent         AssistantEventType = "error"
)

type AssistantEvent struct {
	Type              AssistantEventType
	Message           *Message
	ContentBlockIndex int    // start / done / error 为 0
	Delta             string // 只有 _delta 后缀用
}

// agent_start           没有字段
// turn_start            没有字段
// agent_end             Messages
// turn_end              Message、ToolFinalResult
// message_start         Message
// message_update        AssistantEvent（消息在它里面）
// message_end           Message
// tool_execution_start  ToolCallID、ToolName、Args
// tool_execution_update ToolCallID、ToolName、Args、ToolPartialResult
// tool_execution_end    ToolCallID、ToolName、Args、ToolFinalResult、IsError
type Event struct {
	Type              EventType
	Messages          []*Message
	Message           *Message
	AssistantEvent    *AssistantEvent
	ToolCallID        string
	ToolName          string
	Args              map[string]any
	ToolPartialResult *ToolResult
	ToolFinalResult   *ToolResult
	IsError           bool
}
