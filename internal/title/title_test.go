package title

import (
	"context"
	"strings"
	"testing"

	"github.com/reuben/go-harness/internal/types"
)

// 模型经常加引号、加前缀、或者干脆多答一行 —— 都要收拾掉
func TestClean(t *testing.T) {
	cases := []struct {
		raw, want string
	}{
		{`"登录报错排查"`, "登录报错排查"},
		{"标题：修好登录\n", "修好登录"},
		{"「数据库连接超时」", "数据库连接超时"},
		{"  多余空白  ", "多余空白"},
		{"第一行\n第二行", "第一行"},
	}
	for _, c := range cases {
		if got := clean(c.raw); got != c.want {
			t.Errorf("clean(%q) = %q，想要 %q", c.raw, got, c.want)
		}
	}

	long := strings.Repeat("超", 50)
	if got := clean(long); len([]rune(got)) > 25 {
		t.Errorf("太长的标题该被截断，实际 %d 个字", len([]rune(got)))
	}
}

func userText(text string) *types.Message {
	return &types.Message{Role: types.UserRole, ContentBlocks: []types.ContentBlock{types.NewTextBlock(text)}}
}

// 模型出错时不能把错误文案当成标题
func TestGenerateSkipsFailedCall(t *testing.T) {
	fail := func(context.Context, types.Conversation) <-chan types.AssistantEvent {
		events := make(chan types.AssistantEvent, 1)
		events <- types.AssistantEvent{Type: types.AssistantErrorEvent, Message: &types.Message{
			StopReason: types.ErrorStop, ErrorMessage: "模型返回 400：余额不足",
		}}
		close(events)
		return events
	}

	if got := Generate(context.Background(), fail, []*types.Message{userText("问一句")}); got != "" {
		t.Errorf("调用失败该返回空串，实际 %q", got)
	}
}

// 只喂开头一问一答，后面的不进去
func TestGenerateFeedsOnlyFirstExchange(t *testing.T) {
	var seen types.Conversation
	capture := func(_ context.Context, conversation types.Conversation) <-chan types.AssistantEvent {
		seen = conversation
		events := make(chan types.AssistantEvent, 2)
		events <- types.AssistantEvent{Type: types.AssistantTextDeltaEvent, Delta: "标题在此", ContentBlockIndex: 0}
		events <- types.AssistantEvent{Type: types.AssistantDoneEvent, Message: &types.Message{StopReason: types.NormalStop}}
		close(events)
		return events
	}

	messages := []*types.Message{
		userText("第一问"),
		{Role: types.AssistantRole, ContentBlocks: []types.ContentBlock{types.NewTextBlock("第一答")}},
		userText("第二问"),
	}
	if got := Generate(context.Background(), capture, messages); got != "标题在此" {
		t.Fatalf("标题没读回来：%q", got)
	}

	if len(seen.Messages) != 1 {
		t.Fatalf("该只发一条消息出去，实际 %d 条", len(seen.Messages))
	}
	body := seen.Messages[0].Text()
	if !strings.Contains(body, "第一问") || !strings.Contains(body, "第一答") {
		t.Errorf("开头一问一答都该在：%q", body)
	}
	if strings.Contains(body, "第二问") {
		t.Errorf("后面的对话不该喂进去：%q", body)
	}
}
