package core

import (
	"context"
	"slices"

	"github.com/reuben/go-harness/internal/provider"
	"github.com/reuben/go-harness/internal/tools"
	"github.com/reuben/go-harness/internal/tui"
	"github.com/reuben/go-harness/internal/types"
)

func Run(ctx context.Context,
	prompts []*types.Message,
	conversation *types.Conversation,
	generateFunc provider.GenerateFunction,
	emit tui.EventSend,
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
		msg := provider.CallLLM(ctx, runConversation, generateFunc, emit)
		newMessages = append(newMessages, msg)

		if msg.StopReason == types.ErrorStop || msg.StopReason == types.AbortedStop {
			emit(types.Event{Type: types.TurnEndEvent})
			break
		}

		toolCalls := msg.ToolCalls()
		var toolResultMessages []*types.Message
		shouldStop := false
		if len(toolCalls) > 0 {
			if msg.StopReason == types.LengthStop {
				toolResultMessages, shouldStop = tools.FailTruncatedToolCalls(toolCalls, emit)
			} else {
				toolResultMessages, shouldStop = tools.ExecToolCalls(ctx, conversation, toolCalls, emit)
			}
			runConversation.Messages = append(runConversation.Messages, toolResultMessages...)
			newMessages = append(newMessages, toolResultMessages...)
		}

		emit(types.Event{
			Type:               types.TurnEndEvent,
			Message:            msg,
			ToolResultMessages: toolResultMessages,
		})

		if len(toolCalls) == 0 || shouldStop {
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
