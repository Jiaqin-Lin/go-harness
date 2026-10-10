package provider

import (
	"fmt"

	"github.com/reuben/go-harness/internal/types"
)

// NormalizeToolCalls 给没有配对的 tool_call 补一条合成的 tool_result。
//
// OpenAI 协议要求每个 tool_call 都跟着一条对应的 tool 消息，但历史是棵树，
// 一条路径可以停在任何地方：停在带 tool_call 的 assistant 上、或者并行调了
// 三个工具只执行了一个，历史里就缺配对，请求直接 400。
//
// 补在这一层、而不是在树上做什么「这个节点不能分叉」的校验：
// 这里是发请求前唯一的关口，绕不过去，分支点也就随便点。
//
// 只补配对，不丢消息 —— pi 那边还会顺手跳过 error/aborted 的 assistant，
// 这里不抄：core/loop.go 在中断路径上已经替它们补过配对，再跳过那些消息
// 反而把后面的 tool_result 变成新的孤儿
func NormalizeToolCalls(messages []*types.Message) []*types.Message {
	out := make([]*types.Message, 0, len(messages))

	// pending 是「刚调过、还在等结果」的调用，answered 是已经等到结果的 id
	var pending []types.ContentBlock
	answered := map[string]bool{}

	flush := func() {
		for _, call := range pending {
			if answered[call.ToolCallID] {
				continue
			}
			out = append(out, types.NewToolResult(call.ToolCallID, call.ToolName,
				[]types.ContentBlock{types.NewTextBlock(
					fmt.Sprintf("工具 %s 没有返回结果（那次调用没有跑完）。需要的话重新调用一次。", call.ToolName))},
				true))
		}
		pending, answered = nil, map[string]bool{}
	}

	for _, msg := range messages {
		switch msg.Role {
		case types.AssistantRole:
			// 上一条 assistant 的调用到此为止，没等到结果的先补上 ——
			// 补的位置就在打断它的这条消息前面
			flush()
			out = append(out, msg)
			if calls := msg.ToolCalls(); len(calls) > 0 {
				pending, answered = calls, map[string]bool{}
			}

		case types.ToolResultRole:
			answered[msg.ToolCallID] = true
			out = append(out, msg)

		default:
			flush()
			out = append(out, msg)
		}
	}
	// 历史以没配对的调用收尾也要补
	flush()

	return out
}
