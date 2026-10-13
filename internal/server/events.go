package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/reuben/go-harness/internal/types"
)

// wireEvent 是 SSE 里的事件形状。刻意不复用 types.Event：那个结构没有 json tag，
// 而且 message_partial 每帧都带着整条攒到现在的消息 —— 一次长回答等于把全文
// 传几百遍。这里只送每帧真正变了的东西
type wireEvent struct {
	Type string `json:"type"`

	Delta      string `json:"delta,omitempty"`
	BlockIndex int    `json:"blockIndex"` // 不带 omitempty：0 是最常见的块下标，不能省

	ToolCallID string         `json:"toolCallId,omitempty"`
	ToolName   string         `json:"toolName,omitempty"`
	Args       map[string]any `json:"args,omitempty"`
	Blocks     []blockDTO     `json:"blocks,omitempty"`
	IsError    bool           `json:"isError,omitempty"`

	Message *messageDTO `json:"message,omitempty"`
	Entry   *entryDTO   `json:"entry,omitempty"`

	// QueueID 是 queued_delivered 专用：排队的那条消息投递出去了，
	// 前端拿它对上自己手里那条 chip（见 server/queue.go）
	QueueID string `json:"queueId,omitempty"`

	Error string `json:"error,omitempty"`
}

// toWire 把 harness 的事件翻成给前端的事件。返回 false = 这个事件前端不需要
func toWire(event types.Event) (wireEvent, bool) {
	switch event.Type {
	case types.TurnStartEvent:
		return wireEvent{Type: "turn_start"}, true

	case types.MessagePartialEvent:
		return partialWire(event.AssistantEvent)

	case types.MessageEndEvent:
		return wireEvent{Type: "message_end", Message: toMessageDTO(event.Message)}, true

	case types.ToolStartEvent:
		// 不带 Args：参数前端已经从 tool_call_delta 攒出来了，再送一份是白发的
		return wireEvent{Type: "tool_start", ToolCallID: event.ToolCallID, ToolName: event.ToolName}, true

	case types.ToolEndEvent:
		return wireEvent{Type: "tool_end", ToolCallID: event.ToolCallID, ToolName: event.ToolName, Blocks: toBlocksDTO(event.ToolFinalResult), IsError: event.IsError}, true
	}

	// agent_start / agent_end / turn_end / message_start 不发：一轮的结束以
	// handler 自己发的 done 为准，那个更晚 —— 消息都落盘、entry 都推完了
	return wireEvent{}, false
}

func partialWire(event *types.AssistantEvent) (wireEvent, bool) {
	if event == nil {
		return wireEvent{}, false
	}
	switch event.Type {
	case types.AssistantStartEvent:
		return wireEvent{Type: "assistant_start"}, true

	case types.AssistantTextDeltaEvent:
		return wireEvent{Type: "text_delta", Delta: event.Delta, BlockIndex: event.ContentBlockIndex}, true

	case types.AssistantThinkingDeltaEvent:
		return wireEvent{Type: "thinking_delta", Delta: event.Delta, BlockIndex: event.ContentBlockIndex}, true

	case types.AssistantToolCallStartEvent:
		return wireEvent{Type: "tool_call_start", BlockIndex: event.ContentBlockIndex,
			ToolCallID: event.Message.ToolCallID, ToolName: event.Message.ToolName}, true

	case types.AssistantToolCallDeltaEvent:
		return wireEvent{Type: "tool_call_delta", Delta: event.Delta, BlockIndex: event.ContentBlockIndex}, true
	}
	// tool_call_end / done / error 都不用单独发：参数攒完由 message_end 带全，
	// 一整轮的结束由 handler 自己发的 done 兜底
	return wireEvent{}, false
}

// eventStream 往一个 SSE 连接上写事件。写失败（关页面、点停止）只把它标死，
// 不打断这一轮的运行和落盘 —— 用户按了停止，已经跑出来的东西还是得记住
type eventStream struct {
	w    http.ResponseWriter
	ctl  *http.ResponseController
	dead bool
}

func newEventStream(w http.ResponseWriter) *eventStream {
	return &eventStream{w: w, ctl: http.NewResponseController(w)}
}

func (s *eventStream) send(event wireEvent) {
	if s.dead {
		return
	}
	buf, err := json.Marshal(event)
	if err != nil {
		s.dead = true
		return
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", buf); err != nil {
		s.dead = true
		return
	}
	if err := s.ctl.Flush(); err != nil {
		s.dead = true
	}
}
