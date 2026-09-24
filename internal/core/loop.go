package core

import (
	"context"
	"slices"

	"github.com/reuben/go-harness/internal/provider"
	"github.com/reuben/go-harness/internal/tools"
	"github.com/reuben/go-harness/internal/types"
)

func Run(ctx context.Context,
	prompts []*types.Message,
	conversation *types.Conversation,
	generateFunc provider.GenerateFunction,
	emit types.EventSend,
) []*types.Message {

	runConversation := &types.Conversation{
		SystemPrompt: conversation.SystemPrompt,
		Messages:     slices.Concat(conversation.Messages, prompts),
		Tools:        conversation.Tools,
	}
	newMessages := append([]*types.Message{}, prompts...)

	emit(types.Event{Type: types.AgentStartEvent})
	emit(types.Event{Type: types.TurnStartEvent})

	for {
		msg := provider.CallLLM(ctx, *runConversation, generateFunc, emit)
		runConversation.Messages = append(runConversation.Messages, msg)
		newMessages = append(newMessages, msg)

		if msg.StopReason == types.ErrorStop || msg.StopReason == types.AbortedStop {
			// 中断可能发生在工具参数已经流了一部分之后，这些调用已经进了历史。
			// 给它们补上配对的结果，否则每一条 assistant 都带着悬空的 tool_call
			var unstartedMessages []*types.Message
			if toolCalls := msg.ToolCalls(); len(toolCalls) > 0 {
				reason := msg.ErrorMessage
				if reason == "" {
					reason = "用户取消了这一轮"
				}
				unstartedMessages = tools.FailUnstartedToolCalls(toolCalls, reason, emit)
				newMessages = append(newMessages, unstartedMessages...)
			}
			emit(types.Event{
				Type:               types.TurnEndEvent,
				Message:            msg,
				ToolResultMessages: unstartedMessages,
			})
			break
		}

		toolCalls := msg.ToolCalls()
		var toolResultMessages []*types.Message
		shouldStop := false
		if len(toolCalls) > 0 {
			if msg.StopReason == types.LengthStop {
				toolResultMessages, shouldStop = tools.FailTruncatedToolCalls(toolCalls, emit)
			} else {
				toolResultMessages, shouldStop = tools.ExecToolCalls(ctx, runConversation.Tools, toolCalls, emit)
			}
			runConversation.Messages = append(runConversation.Messages, toolResultMessages...)
			newMessages = append(newMessages, toolResultMessages...)
		}

		emit(types.Event{
			Type:               types.TurnEndEvent,
			Message:            msg,
			ToolResultMessages: toolResultMessages,
		})

		if len(toolCalls) == 0 || shouldStop || ctx.Err() != nil {
			break
		}

		emit(types.Event{Type: types.TurnStartEvent})

	}

	emit(types.Event{
		Type:     types.AgentEndEvent,
		Messages: newMessages,
	})
	return newMessages
}
