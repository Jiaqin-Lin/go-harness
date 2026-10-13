package core

import (
	"context"
	"slices"

	"github.com/reuben/go-harness/internal/provider"
	"github.com/reuben/go-harness/internal/tools"
	"github.com/reuben/go-harness/internal/types"
)

// Queues 是「这一轮跑着的时候用户又塞话进来」的两个入口，各自返回下一条待投递的
// 用户消息（没有就是 nil）。差别只在投递时机：Steer 在工具批次边界投，模型做完当前
// 这步就能看到；FollowUp 在本轮本来要停时才投，排到这次任务结束之后。
// 零值就是没有队列 —— 命令行、e2e 都走这条
type Queues struct {
	Steer    func() *types.Message
	FollowUp func() *types.Message
}

// drain 投一条排队消息进对话。也塞进 newMessages —— 它跟别的消息一样要落盘，
// 顺序就靠这里保证
func drain(conversation *types.Conversation, newMessages *[]*types.Message, next func() *types.Message) bool {
	if next == nil {
		return false
	}
	msg := next()
	if msg == nil {
		return false
	}
	conversation.Messages = append(conversation.Messages, msg)
	*newMessages = append(*newMessages, msg)
	return true
}

func Run(ctx context.Context,
	prompts []*types.Message,
	conversation *types.Conversation,
	generateFunc provider.GenerateFunction,
	emit types.EventSend,
	queues Queues,
) []*types.Message {

	runConversation := &types.Conversation{
		SystemPrompt: conversation.SystemPrompt,
		Messages:     slices.Concat(conversation.Messages, prompts),
		Tools:        conversation.Tools,
	}
	newMessages := append([]*types.Message{}, prompts...)

	emit(types.Event{Type: types.AgentStartEvent})
	emit(types.Event{Type: types.TurnStartEvent})

	// 开跑前先看一眼：前面花掉的工夫（压缩上下文、建连接）里用户可能已经排上了
	drain(runConversation, &newMessages, queues.Steer)

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

		// 中止、或者工具被截断要求收手：什么都不再投，直接收
		if ctx.Err() != nil || shouldStop {
			break
		}

		if len(toolCalls) > 0 {
			// 工具批次跑完了：插话在这儿进来，下一次 LLM 调用就能看到
			drain(runConversation, &newMessages, queues.Steer)
		} else if !drain(runConversation, &newMessages, queues.Steer) &&
			!drain(runConversation, &newMessages, queues.FollowUp) {
			// 模型答完了，这一轮本来要收：先把插话投掉 —— 这条线要停了，再等就
			// 没人来投它；再看排队的下一条。两条都空才是真的结束
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
