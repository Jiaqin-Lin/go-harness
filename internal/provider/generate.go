package provider

import (
	"context"

	"github.com/reuben/go-harness/internal/types"
)

// GenerateFunction 产出一次模型调用的增量事件，正在拼装的消息挂在事件里
type GenerateFunction func(context.Context, types.Conversation) <-chan types.AssistantEvent

func GenerateFunc(ctx context.Context, conversation types.Conversation) <-chan types.AssistantEvent {
	// TODO 接入模型
	return nil
}

// CallLLM 跑完一次模型调用，把增量事件折成一条 assistant 消息。
//
// 契约一：永不返回 nil。调用失败也要返回一条 StopReason=ErrorStop、ErrorMessage 非空的消息，
// 让 loop 靠 StopReason 收尾，而不是靠 nil 判断。
// 契约二：只读 conversation。往对话里追加消息是 loop 的事，两边都追加迟早会重一遍。
func CallLLM(ctx context.Context, conversation types.Conversation, generateFunc GenerateFunction, emit types.EventSend) *types.Message {
	// TODO 接入模型：先返回一条错误消息，让 loop 的错误分支能跑通
	return &types.Message{
		Role:         types.AssistantRole,
		StopReason:   types.ErrorStop,
		ErrorMessage: "还没接模型",
	}
}
