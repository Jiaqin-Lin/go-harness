package provider

import (
	"encoding/json"
	"testing"

	"github.com/reuben/go-harness/internal/types"
)

func marshal(t *testing.T, msg *types.Message) string {
	t.Helper()
	got, err := json.Marshal(wireMessageFrom(msg))
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	return string(got)
}

// 纯文本消息的 content 必须是字符串，不能是 parts 数组：数组形式有些 OpenAI
// 兼容实现会把结构原样抄进回复里
func TestWireTextMessageStaysString(t *testing.T) {
	got := marshal(t, &types.Message{
		Role:          types.ToolResultRole,
		ContentBlocks: []types.ContentBlock{types.NewTextBlock("两行\n文本")},
		ToolCallID:    "call_1",
	})
	want := `{"role":"tool","content":"两行\n文本","tool_call_id":"call_1"}`
	if got != want {
		t.Errorf("\n得到 %s\n想要 %s", got, want)
	}
}

// 带图片就得走 content parts，图片编成 data URL。tool 消息和 user 消息同理，
// 两种消息都测：图片块能出现在哪儿由工具和上层决定，wire 不该挑
func TestWireMessageWithImage(t *testing.T) {
	blocks := []types.ContentBlock{
		types.NewTextBlock("看图"),
		{Kind: types.ImageBlock, ImageMime: "image/png", ImageData: "AAAB"},
	}

	cases := []struct {
		msg  *types.Message
		want string
	}{
		{
			&types.Message{Role: types.ToolResultRole, ContentBlocks: blocks, ToolCallID: "call_1"},
			`{"role":"tool","content":[{"type":"text","text":"看图"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAB"}}],"tool_call_id":"call_1"}`,
		},
		{
			&types.Message{Role: types.UserRole, ContentBlocks: blocks},
			`{"role":"user","content":[{"type":"text","text":"看图"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAB"}}]}`,
		},
		{
			// 只有图片没有文字时，不给 wire 塞一个空的 text part
			&types.Message{Role: types.ToolResultRole, ContentBlocks: []types.ContentBlock{
				types.NewTextBlock(""),
				{Kind: types.ImageBlock, ImageMime: "image/jpeg", ImageData: "AAAB"},
			}, ToolCallID: "call_1"},
			`{"role":"tool","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,AAAB"}}],"tool_call_id":"call_1"}`,
		},
	}

	for _, c := range cases {
		if got := marshal(t, c.msg); got != c.want {
			t.Errorf("\n得到 %s\n想要 %s", got, c.want)
		}
	}
}

// assistant 的 content 永远是字符串（助手消息不带图片），推理内容要原样带上
func TestWireAssistantKeepsStringContent(t *testing.T) {
	got := marshal(t, &types.Message{
		Role: types.AssistantRole,
		ContentBlocks: []types.ContentBlock{
			{Kind: types.ThinkingBlock, Thinking: "先想"},
			types.NewTextBlock("再答"),
			{Kind: types.ToolBlock, ToolCallID: "call_1", ToolName: "read", Args: `{"path":"a.png"}`},
		},
	})
	want := `{"role":"assistant","content":"再答","reasoning_content":"先想",` +
		`"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"a.png\"}"}}]}`
	if got != want {
		t.Errorf("\n得到 %s\n想要 %s", got, want)
	}
}
