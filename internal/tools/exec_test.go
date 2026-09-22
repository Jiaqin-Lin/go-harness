package tools

import (
	"context"
	"testing"

	"github.com/reuben/go-harness/internal/types"
)

func toolBlock(id, name, args string) types.ContentBlock {
	return types.ContentBlock{Kind: types.ToolBlock, ToolCallID: id, ToolName: name, Args: args}
}

// echoTool 把每次收到的 path 记下来，用来判断工具到底跑没跑
func echoTool(calls *[]string) []*types.Tool {
	return []*types.Tool{{
		Name: "echo",
		Execute: func(ctx context.Context, args map[string]any, _ types.SendPartialResult) (types.ToolResult, error) {
			*calls = append(*calls, stringArg(args, "path"))
			return textResult("ok"), nil
		},
	}}
}

func runExec(ctx context.Context, calls *[]string, blocks []types.ContentBlock) ([]*types.Message, bool, []types.Event) {
	var events []types.Event
	msgs, shouldStop := ExecToolCalls(ctx, echoTool(calls), blocks, func(e types.Event) {
		events = append(events, e)
	})
	return msgs, shouldStop, events
}

// 工具跑成功了，tool_end 就不能报 IsError：界面靠它决定这次调用显示成成功还是失败
func TestExecToolCallsSuccessIsNotError(t *testing.T) {
	var calls []string
	msgs, shouldStop, events := runExec(context.Background(), &calls,
		[]types.ContentBlock{toolBlock("c1", "echo", `{"path":"a.txt"}`)})

	if len(msgs) != 1 || msgs[0].IsError {
		t.Fatalf("期望一条不报错的 tool_result，实际 %+v", msgs)
	}
	if msgs[0].Role != types.ToolResultRole || msgs[0].ToolCallID != "c1" {
		t.Fatalf("结果要跟 tool_use 配对，实际 role=%q id=%q", msgs[0].Role, msgs[0].ToolCallID)
	}

	var end *types.Event
	for i := range events {
		if events[i].Type == types.ToolEndEvent {
			end = &events[i]
		}
	}
	if end == nil {
		t.Fatal("没有发出 tool_end 事件")
	}
	if end.IsError {
		t.Error("工具跑成功了，tool_end 却带着 IsError")
	}
	if end.Args["path"] != "a.txt" {
		t.Errorf("tool_end 要带上解析后的参数，实际 %+v", end.Args)
	}
	if shouldStop {
		t.Error("工具没要求停，这轮不该停")
	}
}

// ctx 取消之后剩下的 tool_use 也得各自有 tool_result：漏掉一条，
// 历史里就留下没有结果的 tool_use，下一轮请求直接不合法
func TestExecToolCallsKeepsPairsAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var calls []string
	msgs, _, _ := runExec(ctx, &calls, []types.ContentBlock{
		toolBlock("c1", "echo", `{}`),
		toolBlock("c2", "echo", `{}`),
	})

	if len(calls) != 0 {
		t.Fatalf("ctx 已经取消，工具不该真的跑：%v", calls)
	}
	if len(msgs) != 2 {
		t.Fatalf("每个 tool_use 都要有 tool_result，期望 2 条，实际 %d 条", len(msgs))
	}
	for i, want := range []string{"c1", "c2"} {
		if msgs[i].ToolCallID != want || msgs[i].Role != types.ToolResultRole {
			t.Errorf("第 %d 条结果没对上：%+v", i, msgs[i])
		}
		if !msgs[i].IsError {
			t.Errorf("第 %d 条是被取消的，应该标成 IsError", i)
		}
	}
}
