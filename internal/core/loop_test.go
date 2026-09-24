package core

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/reuben/go-harness/internal/provider"
	"github.com/reuben/go-harness/internal/types"
)

// interruptedLLM 模拟「工具参数流到一半就断了」：先流出一次工具调用的开头和一段参数，
// 再给一个带结束原因的错误事件
func interruptedLLM(stop types.StopReason, errText string) provider.GenerateFunction {
	return func(context.Context, types.Conversation) <-chan types.AssistantEvent {
		events := make(chan types.AssistantEvent, 3)
		events <- types.AssistantEvent{
			Type:    types.AssistantToolCallStartEvent,
			Message: &types.Message{ToolCallID: "c1", ToolName: "bash"},
		}
		events <- types.AssistantEvent{Type: types.AssistantToolCallDeltaEvent, Delta: `{"command":"ls`}
		events <- types.AssistantEvent{
			Type: types.AssistantErrorEvent,
			Message: &types.Message{
				Role:         types.AssistantRole,
				StopReason:   stop,
				ErrorMessage: errText,
			},
		}
		close(events)
		return events
	}
}

// assertToolCallsPaired 检查每条 assistant 消息的 tool_call 都有配对的结果消息。
// 漏一条，历史就不合法，下一次请求会被服务端 400 掉
func assertToolCallsPaired(t *testing.T, msgs []*types.Message) {
	t.Helper()

	results := map[string]bool{}
	for _, msg := range msgs {
		if msg.Role == types.ToolResultRole {
			results[msg.ToolCallID] = true
		}
	}
	for _, msg := range msgs {
		if msg.Role != types.AssistantRole {
			continue
		}
		for _, call := range msg.ToolCalls() {
			if !results[call.ToolCallID] {
				t.Errorf("tool_call %s 没有配对的结果消息", call.ToolCallID)
			}
		}
	}
}

func runInterrupted(t *testing.T, stop types.StopReason, errText string) []*types.Message {
	t.Helper()

	prompt := &types.Message{Role: types.UserRole, ContentBlocks: []types.ContentBlock{types.NewTextBlock("跑个命令")}}
	return Run(context.Background(), []*types.Message{prompt}, &types.Conversation{}, interruptedLLM(stop, errText), func(types.Event) {})
}

// 模型出错时已经流出来的 tool_call 要有配对的结果，且原因要落到文案里
func TestRunPairsToolCallsAfterErrorStop(t *testing.T) {
	msgs := runInterrupted(t, types.ErrorStop, "读流失败：unexpected EOF")

	assertToolCallsPaired(t, msgs)

	if len(msgs) != 3 {
		t.Fatalf("期望 user + assistant + tool_result 三条，实际 %d 条", len(msgs))
	}
	result := msgs[2]
	if result.Role != types.ToolResultRole || result.ToolCallID != "c1" {
		t.Fatalf("最后一条要跟 tool_call 配对，实际 role=%q id=%q", result.Role, result.ToolCallID)
	}
	if text := result.ContentBlocks[0].Text; !strings.Contains(text, "读流失败：unexpected EOF") {
		t.Errorf("结果消息要说出中断原因，实际 %q", text)
	}
}

// 真实那条路：SSE 里工具参数流了一半，流就断了。
// 这条喂的是假服务器（不打真模型），走的是 provider 的真代码，
// 盯住「provider 交出什么样的消息」和「loop 怎么收尾」两边没跑偏
func TestRunPairsToolCallsWhenStreamBreaks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// 只有一帧、参数没给完，没有 [DONE] 也没有 finish_reason：流就这么断了
		fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"bash","arguments":"{\"command\":"}}]}}]}`+"\n\n")
	}))
	defer server.Close()

	prompt := &types.Message{Role: types.UserRole, ContentBlocks: []types.ContentBlock{types.NewTextBlock("跑个命令")}}
	llm := provider.NewDeepSeek("k", server.URL, "m")
	msgs := Run(context.Background(), []*types.Message{prompt}, &types.Conversation{}, llm.Generate, func(types.Event) {})

	assertToolCallsPaired(t, msgs)

	if len(msgs) != 3 {
		t.Fatalf("期望 user + assistant + tool_result 三条，实际 %d 条：%+v", len(msgs), msgs)
	}
	result := msgs[2]
	if result.Role != types.ToolResultRole || result.ToolCallID != "c1" || !result.IsError {
		t.Fatalf("最后一条要是报错的 tool_result：%+v", result)
	}
	if text := result.ContentBlocks[0].Text; !strings.Contains(text, "流在给出结束原因之前就断了") {
		t.Errorf("结果消息要说出中断原因，实际 %q", text)
	}
}

// 用户取消跟模型出错走同一条路：不补结果的话，取消一次就没法继续对话了
func TestRunPairsToolCallsAfterAbort(t *testing.T) {
	msgs := runInterrupted(t, types.AbortedStop, "")

	assertToolCallsPaired(t, msgs)

	if len(msgs) != 3 {
		t.Fatalf("期望 user + assistant + tool_result 三条，实际 %d 条", len(msgs))
	}
	if result := msgs[2]; result.Role != types.ToolResultRole || result.ToolCallID != "c1" {
		t.Fatalf("最后一条要跟 tool_call 配对，实际 role=%q id=%q", result.Role, result.ToolCallID)
	}
}
