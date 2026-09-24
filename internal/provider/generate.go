package provider

import (
	"context"

	"github.com/reuben/go-harness/internal/types"
)

// GenerateFunction 产出一次模型调用的增量事件，正在拼装的消息挂在事件里
type GenerateFunction func(context.Context, types.Conversation) <-chan types.AssistantEvent

// CallLLM 跑完一次模型调用，把增量事件折成一条 assistant 消息。
//
// 契约一：永不返回 nil。调用失败也要返回一条 StopReason=ErrorStop、ErrorMessage 非空的消息，
// 让 loop 靠 StopReason 收尾，而不是靠 nil 判断。
// 契约二：只读 conversation。往对话里追加消息是 loop 的事，两边都追加迟早会重一遍。
func CallLLM(ctx context.Context, conversation types.Conversation, generateFunc GenerateFunction, emit types.EventSend) *types.Message {
	msg := &types.Message{Role: types.AssistantRole, StopReason: types.PendingStop}
	emit(types.Event{Type: types.MessageStartEvent, Message: msg})

	for event := range generateFunc(ctx, conversation) {
		apply(msg, event)
		emit(types.Event{Type: types.MessagePartialEvent, AssistantEvent: &event})
	}

	// 流跑完必须落在某个终止状态上。停在 PendingStop 的话 loop 会当成「这轮没调工具」正常收尾，
	// 用户那边就是问了一句什么反应都没有
	if msg.StopReason == types.PendingStop {
		msg.StopReason = types.ErrorStop
		msg.ErrorMessage = "模型一个事件都没发就结束了"
	}

	emit(types.Event{Type: types.MessageEndEvent, Message: msg})
	return msg
}

// apply 把一个增量事件摞到正在拼的消息上。
// 事件里的 ContentBlockIndex 是 provider 分配的：块还没长到那儿就往后补，
// 补出来的空块由后面第一个用它的 delta 认领类型
func apply(msg *types.Message, event types.AssistantEvent) {
	switch event.Type {
	case types.AssistantTextDeltaEvent:
		block(msg, event.ContentBlockIndex, types.TextBlock).Text += event.Delta

	case types.AssistantThinkingDeltaEvent:
		block(msg, event.ContentBlockIndex, types.ThinkingBlock).Thinking += event.Delta

	case types.AssistantToolCallStartEvent:
		b := block(msg, event.ContentBlockIndex, types.ToolBlock)
		b.ToolCallID = event.Message.ToolCallID
		b.ToolName = event.Message.ToolName

	case types.AssistantToolCallDeltaEvent:
		block(msg, event.ContentBlockIndex, types.ToolBlock).Args += event.Delta

	case types.AssistantDoneEvent:
		// done 也可能是失败收尾（流在给出结束原因前就断了），ErrorMessage 得跟着过来，
		// 不然错误原因到这一层就没了
		msg.StopReason = event.Message.StopReason
		msg.ErrorMessage = event.Message.ErrorMessage
		msg.Usage = event.Message.Usage

	case types.AssistantErrorEvent:
		// 终止原因由 provider 定：超时、取消是 AbortedStop，只有真错了才是 ErrorStop
		msg.StopReason = event.Message.StopReason
		msg.ErrorMessage = event.Message.ErrorMessage
	}
}

func block(msg *types.Message, index int, kind types.BlockKind) *types.ContentBlock {
	for len(msg.ContentBlocks) <= index {
		msg.ContentBlocks = append(msg.ContentBlocks, types.ContentBlock{})
	}
	if msg.ContentBlocks[index].Kind == "" {
		msg.ContentBlocks[index].Kind = kind
	}
	return &msg.ContentBlocks[index]
}
