package core

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
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
	return Run(context.Background(), []*types.Message{prompt}, &types.Conversation{}, interruptedLLM(stop, errText), func(types.Event) {}, Queues{})
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
	msgs := Run(context.Background(), []*types.Message{prompt}, &types.Conversation{}, llm.Generate, func(types.Event) {}, Queues{})

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

// ── 队列：跑着的时候用户还能塞话进来 ─────────────────────

// scriptedReply 是脚本里的一步：要么发起一次工具调用，要么说一句话
type scriptedReply struct {
	text   string
	toolID string // 非空 = 这一步发起一次工具调用
}

// scriptLLM 按调用次序回话，每次都把收到的历史记下来 ——
// 插话是在哪一步进的上下文，只能从这儿看
type scriptLLM struct {
	mu      sync.Mutex
	replies []scriptedReply
	seen    [][]*types.Message
}

func (s *scriptLLM) generate(_ context.Context, conversation types.Conversation) <-chan types.AssistantEvent {
	s.mu.Lock()
	reply := s.replies[min(len(s.seen), len(s.replies)-1)]
	s.seen = append(s.seen, conversation.Messages)
	s.mu.Unlock()

	events := make(chan types.AssistantEvent)
	go func() {
		defer close(events)
		events <- types.AssistantEvent{Type: types.AssistantStartEvent}
		stop := types.NormalStop

		index := 0
		if reply.toolID != "" {
			// 工具的名字故意用一个没注册的：ExecToolCalls 会回一条「没有这个工具」的
			// 结果消息，工具批次这个边界就出来了，不用真去跑什么
			events <- types.AssistantEvent{Type: types.AssistantToolCallStartEvent, ContentBlockIndex: index,
				Message: &types.Message{ToolCallID: reply.toolID, ToolName: "没这个工具"}}
			events <- types.AssistantEvent{Type: types.AssistantToolCallDeltaEvent, ContentBlockIndex: index, Delta: "{}"}
			events <- types.AssistantEvent{Type: types.AssistantToolCallEndEvent, ContentBlockIndex: index}
			index++
			stop = types.ToolUserStop
		}
		if reply.text != "" {
			events <- types.AssistantEvent{Type: types.AssistantTextDeltaEvent, ContentBlockIndex: index, Delta: reply.text}
		}
		events <- types.AssistantEvent{Type: types.AssistantDoneEvent,
			Message: &types.Message{Role: types.AssistantRole, StopReason: stop}}
	}()
	return events
}

func (s *scriptLLM) histories() [][]*types.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]*types.Message, len(s.seen))
	for i, messages := range s.seen {
		out[i] = slices.Clone(messages)
	}
	return out
}

// once 是「只有一条、投出去就没了」的队列。开跑前就排上的（拿它测压缩那段时间
// 里排进来的）第一取就在第一次调用之前
func once(text string) func() *types.Message {
	delivered := false
	return func() *types.Message {
		if delivered {
			return nil
		}
		delivered = true
		return &types.Message{Role: types.UserRole, ContentBlocks: []types.ContentBlock{types.NewTextBlock(text)}}
	}
}

// delayed 是「第 n 次来取才给」的队列。跑起来之后排上的消息要晚几步才投 ——
// loop 在开跑前会先取一次，用它才能把「投在工具批次边界」和「投在收尾边界」分开测
func delayed(n int, text string) func() *types.Message {
	pulls := 0
	return func() *types.Message {
		pulls++
		if pulls != n {
			return nil
		}
		return &types.Message{Role: types.UserRole, ContentBlocks: []types.ContentBlock{types.NewTextBlock(text)}}
	}
}

func rolePath(messages []*types.Message) []types.Role {
	path := make([]types.Role, len(messages))
	for i, msg := range messages {
		path[i] = msg.Role
	}
	return path
}

// 插话在工具批次边界投：第一次调用发起工具，工具跑完、下一次调用之前它才进上下文 ——
// 「模型做完当前这一步就能看到」就是这个意思
func TestSteerInjectedAfterToolBatch(t *testing.T) {
	llm := &scriptLLM{replies: []scriptedReply{{toolID: "c1"}, {text: "好"}}}
	prompt := &types.Message{Role: types.UserRole, ContentBlocks: []types.ContentBlock{types.NewTextBlock("跑一下")}}

	msgs := Run(context.Background(), []*types.Message{prompt}, &types.Conversation{}, llm.generate,
		func(types.Event) {}, Queues{Steer: delayed(2, "别动那个文件")})

	want := []types.Role{types.UserRole, types.AssistantRole, types.ToolResultRole, types.UserRole, types.AssistantRole}
	if got := rolePath(msgs); !slices.Equal(got, want) {
		t.Fatalf("消息顺序（= 落盘顺序）不对：\n得到 %v\n想要 %v", got, want)
	}
	if text := msgs[3].Text(); text != "别动那个文件" {
		t.Errorf("插进来的该是那条插话，实际 %q", text)
	}
	assertToolCallsPaired(t, msgs)

	histories := llm.histories()
	if len(histories) != 2 {
		t.Fatalf("该调两次模型，实际 %d 次", len(histories))
	}
	// 第二次调用：工具结果后面紧跟着插话，中间不能有别的（不然模型会以为
	// 那是上一批工具的产物）
	second := rolePath(histories[1])
	if n := len(second); n < 3 || second[n-1] != types.UserRole || second[n-2] != types.ToolResultRole {
		t.Errorf("第二次调用的末尾该是「工具结果 + 插话」，实际 %v", second)
	}
}

// 排队（followUp）等的是「这一轮本来要停」：模型答完、没有工具可调了，才作为
// 新的一轮进来 —— 跑着的时候不打断
func TestFollowUpRunsAfterTurnWouldStop(t *testing.T) {
	llm := &scriptLLM{replies: []scriptedReply{{text: "第一答"}, {text: "第二答"}}}
	prompt := &types.Message{Role: types.UserRole, ContentBlocks: []types.ContentBlock{types.NewTextBlock("第一问")}}

	msgs := Run(context.Background(), []*types.Message{prompt}, &types.Conversation{}, llm.generate,
		func(types.Event) {}, Queues{FollowUp: once("还有一句")})

	want := []types.Role{types.UserRole, types.AssistantRole, types.UserRole, types.AssistantRole}
	if got := rolePath(msgs); !slices.Equal(got, want) {
		t.Fatalf("消息顺序（= 落盘顺序）不对：\n得到 %v\n想要 %v", got, want)
	}

	histories := llm.histories()
	if len(histories) != 2 {
		t.Fatalf("该调两次模型，实际 %d 次", len(histories))
	}
	if last := histories[1][len(histories[1])-1]; last.Text() != "还有一句" {
		t.Errorf("第二次调用该带着排队的这句，实际结尾是 %q", last.Text())
	}
}

// 插话排到时这条线已经要停了（模型不再调工具）：也得投掉 ——
// 这条线上再没有别的时机会来取它
func TestSteerDeliveredWhenTurnWouldStop(t *testing.T) {
	llm := &scriptLLM{replies: []scriptedReply{{text: "答"}}}
	prompt := &types.Message{Role: types.UserRole, ContentBlocks: []types.ContentBlock{types.NewTextBlock("问")}}

	msgs := Run(context.Background(), []*types.Message{prompt}, &types.Conversation{}, llm.generate,
		func(types.Event) {}, Queues{Steer: delayed(2, "插一句")})

	want := []types.Role{types.UserRole, types.AssistantRole, types.UserRole, types.AssistantRole}
	if got := rolePath(msgs); !slices.Equal(got, want) {
		t.Fatalf("插话不该丢：\n得到 %v\n想要 %v", got, want)
	}
}

// 开跑前就排上的（上一轮收尾、压缩那段时间里排的）在第一次调用之前就进上下文 ——
// 用户等的那段时间不算白等
func TestSteerQueuedBeforeRunStart(t *testing.T) {
	llm := &scriptLLM{replies: []scriptedReply{{text: "答"}}}
	prompt := &types.Message{Role: types.UserRole, ContentBlocks: []types.ContentBlock{types.NewTextBlock("问")}}

	msgs := Run(context.Background(), []*types.Message{prompt}, &types.Conversation{}, llm.generate,
		func(types.Event) {}, Queues{Steer: once("刚排的")})

	want := []types.Role{types.UserRole, types.UserRole, types.AssistantRole}
	if got := rolePath(msgs); !slices.Equal(got, want) {
		t.Fatalf("开跑前排的该在第一次调用前就进上下文：\n得到 %v\n想要 %v", got, want)
	}
}

// 没队列的时候（命令行、e2e）行为跟以前一样：答完就收
func TestNoQueuesStopsAsBefore(t *testing.T) {
	llm := &scriptLLM{replies: []scriptedReply{{text: "答"}}}
	prompt := &types.Message{Role: types.UserRole, ContentBlocks: []types.ContentBlock{types.NewTextBlock("问")}}

	msgs := Run(context.Background(), []*types.Message{prompt}, &types.Conversation{}, llm.generate,
		func(types.Event) {}, Queues{})

	if len(msgs) != 2 {
		t.Fatalf("期望 user + assistant 两条，实际 %d 条", len(msgs))
	}
}
