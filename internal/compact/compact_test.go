package compact

import (
	"strings"
	"testing"

	"github.com/reuben/go-harness/internal/types"
)

// textMsg 造一条正文消息，4 个字符估成 1 个 token
func textMsg(role types.Role, text string) *types.Message {
	return &types.Message{Role: role, ContentBlocks: []types.ContentBlock{types.NewTextBlock(text)}}
}

// tokensMsg 造一条正文正好值 want 个 token 的消息
func tokensMsg(role types.Role, want int) *types.Message {
	return textMsg(role, strings.Repeat("x", want*4))
}

func TestEstimateTokens(t *testing.T) {
	big := textMsg(types.UserRole, strings.Repeat("x", 400))
	if got := EstimateTokens(big); got != 100 {
		t.Errorf("400 个字符该估成 100 个 token，实际 %d", got)
	}

	// 图片不能按 base64 的长度算：一张截图几百 KB，除 4 就是几十万
	image := &types.Message{Role: types.UserRole, ContentBlocks: []types.ContentBlock{
		{Kind: types.ImageBlock, ImageMime: "image/png", ImageData: strings.Repeat("A", 400_000)},
	}}
	if got := EstimateTokens(image); got != imageChars/4 {
		t.Errorf("图片该估成固定值 %d，实际 %d", imageChars/4, got)
	}

	thinking := &types.Message{Role: types.AssistantRole, ContentBlocks: []types.ContentBlock{
		{Kind: types.ThinkingBlock, Thinking: strings.Repeat("x", 200)},
		{Kind: types.ToolBlock, ToolName: "read", Args: `{"path":"a.go"}`},
	}}
	if got := EstimateTokens(thinking); got != (200+len("read")+len(`{"path":"a.go"}`)+3)/4 {
		t.Errorf("思考 + 工具调用的估算不对：%d", got)
	}
}

func TestContextTokensUsesProviderUsage(t *testing.T) {
	messages := []*types.Message{
		textMsg(types.UserRole, strings.Repeat("x", 400)), // 100
		{Role: types.AssistantRole, StopReason: types.NormalStop, Usage: &types.Usage{Input: 1000, Cached: 900, Output: 200}},
		textMsg(types.ToolResultRole, strings.Repeat("x", 400)), // 100
	}
	// 用量是上一次请求的 prompt（含 cache 命中）+ 那次回答，再加上用量之后的消息
	if got := ContextTokens(messages); got != 1300 {
		t.Errorf("该用 provider 报的用量 1000+200 再加尾部 100，实际 %d", got)
	}
}

func TestContextTokensIgnoresFailedUsage(t *testing.T) {
	// 报错、中止那两次请求的用量不代表现在的上下文，只能整段估算
	for _, stop := range []types.StopReason{types.ErrorStop, types.AbortedStop} {
		messages := []*types.Message{
			textMsg(types.UserRole, strings.Repeat("x", 4_000)),
			{Role: types.AssistantRole, StopReason: stop, Usage: &types.Usage{Input: 999_999, Output: 1}},
		}
		if got := ContextTokens(messages); got != 1000 {
			t.Errorf("%s 的用量不该被采信，该估成 1000，实际 %d", stop, got)
		}
	}
}

func TestContextTokensFallsBackToEstimate(t *testing.T) {
	messages := []*types.Message{
		textMsg(types.UserRole, strings.Repeat("x", 4_000)), // 1000
		textMsg(types.AssistantRole, strings.Repeat("x", 400)),
	}
	if got := ContextTokens(messages); got != 1100 {
		t.Errorf("没有用量时该整段估算，实际 %d", got)
	}
}

func TestShouldCompact(t *testing.T) {
	settings := Settings{ReserveTokens: 1000, KeepRecentTokens: 100}
	cases := []struct {
		tokens int
		want   bool
	}{
		{8999, false},
		{9000, false}, // 正好等于窗口 - reserve 还不压
		{9001, true},
	}
	for _, c := range cases {
		if got := ShouldCompact(c.tokens, 10_000, settings); got != c.want {
			t.Errorf("窗口 10000、reserve 1000 时 %d 个 token 该压吗？得到 %v", c.tokens, got)
		}
	}

	// 窗口未知时分母都没有，任何阈值都是瞎猜
	if ShouldCompact(1<<30, 0, settings) {
		t.Error("窗口是 0 时不该压")
	}
}

func TestFindCutKeepsRecentTokens(t *testing.T) {
	messages := []*types.Message{
		tokensMsg(types.UserRole, 1000),
		tokensMsg(types.AssistantRole, 1000),
		tokensMsg(types.UserRole, 1000),
		tokensMsg(types.AssistantRole, 1000),
		tokensMsg(types.UserRole, 1000),
	}
	// 从后往前：1000、2000、3000 —— 在第三条（下标 2）上超出 2500，
	// 它就是预算耗光的位置，切点取它
	if got := FindCut(messages, 0, 2500); got != 2 {
		t.Errorf("该切在下标 2，实际 %d", got)
	}
}

func TestFindCutNeverKeepsALeadingToolResult(t *testing.T) {
	messages := []*types.Message{
		tokensMsg(types.UserRole, 10),
		tokensMsg(types.AssistantRole, 10),
		tokensMsg(types.ToolResultRole, 5000), // 一条就把预算吃光
		tokensMsg(types.AssistantRole, 10),
		tokensMsg(types.UserRole, 10),
	}
	// 预算耗光在下标 2（工具结果），它不能当切点，往后最近的合法位置是下标 3
	cut := FindCut(messages, 0, 100)
	if cut != 3 {
		t.Fatalf("该切在下标 3（工具结果后面那条助手消息），实际 %d", cut)
	}
	if messages[cut].Role == types.ToolResultRole {
		t.Error("保留段不能从工具结果开头")
	}
}

func TestFindCutFallsBackBehindTrailingToolResults(t *testing.T) {
	// 一轮在工具跑到一半被中止：结尾就是一条大工具结果，它后面没有能切的位置
	messages := []*types.Message{
		tokensMsg(types.UserRole, 10),
		tokensMsg(types.AssistantRole, 10),
		tokensMsg(types.ToolResultRole, 5000),
	}
	// 退到耗光点前面最近的合法位置：那条助手消息，它的工具结果跟着它一起留下
	if got := FindCut(messages, 0, 100); got != 1 {
		t.Errorf("该退到下标 1，实际 %d", got)
	}
}

func TestFindCutSkipsWhatWasAlreadyCompacted(t *testing.T) {
	// 下标 0 是上一版摘要（messages[start:] 才是这次能压的）
	messages := []*types.Message{
		textMsg(types.UserRole, "上一版摘要"),
		tokensMsg(types.UserRole, 10),
		tokensMsg(types.AssistantRole, 10),
		tokensMsg(types.UserRole, 1000),
		tokensMsg(types.AssistantRole, 1000),
		tokensMsg(types.UserRole, 1000),
	}
	// 从后往前累加到下标 3 时超出 2500，切在这儿：摘要自己不在被压的范围里
	if got := FindCut(messages, 1, 2500); got != 3 {
		t.Errorf("该切在下标 3，实际 %d", got)
	}

	// 上一版摘要后面紧跟着就是保留段，没有别的东西可压
	if got := FindCut([]*types.Message{messages[0], messages[3]}, 1, 100); got != -1 {
		t.Errorf("左边没有可摘要的内容时该返回 -1，实际 %d", got)
	}
}

func TestFindCutDeclinesWhenNothingToCompress(t *testing.T) {
	messages := []*types.Message{
		tokensMsg(types.UserRole, 10),
		tokensMsg(types.AssistantRole, 10),
	}
	if got := FindCut(messages, 0, 20_000); got != -1 {
		t.Errorf("整段都没到 keepRecent 时该返回 -1，实际 %d", got)
	}
}

func TestSerializeKeepsRolesAndTruncatesToolOutput(t *testing.T) {
	long := strings.Repeat("y", toolResultLimit*2)
	messages := []*types.Message{
		textMsg(types.UserRole, "帮我看看"),
		{Role: types.AssistantRole, ContentBlocks: []types.ContentBlock{
			{Kind: types.ThinkingBlock, Thinking: "先读文件"},
			{Kind: types.ToolBlock, ToolName: "read", Args: `{"path":"a.go"}`},
		}},
		textMsg(types.ToolResultRole, long),
	}
	got := serialize(messages)

	for _, want := range []string{"[用户] 帮我看看", "[助手思考] 先读文件", `[助手工具调用] read({"path":"a.go"})`, "被截断"} {
		if !strings.Contains(got, want) {
			t.Errorf("序列化结果里该有 %q：\n%s", want, got)
		}
	}
	if strings.Count(got, "y") >= len(long) {
		t.Error("工具结果该被截断")
	}
}

func TestFileOpsTail(t *testing.T) {
	messages := []*types.Message{
		{Role: types.AssistantRole, ContentBlocks: []types.ContentBlock{
			{Kind: types.ToolBlock, ToolName: "read", Args: `{"path":"b.go"}`},
			{Kind: types.ToolBlock, ToolName: "read", Args: `{"path":"a.go"}`},
		}},
		{Role: types.AssistantRole, ContentBlocks: []types.ContentBlock{
			{Kind: types.ToolBlock, ToolName: "edit", Args: `{"path":"b.go"}`},
			{Kind: types.ToolBlock, ToolName: "write", Args: `{"path":"c.go"}`},
			{Kind: types.ToolBlock, ToolName: "bash", Args: `{"command":"ls"}`}, // 不是文件操作
		}},
	}
	got := fileOpsTail(messages)

	// 读过又改过的不算「只读过」
	if !strings.Contains(got, "<read-files>\na.go\n</read-files>") {
		t.Errorf("只读过的文件清单不对：%s", got)
	}
	if !strings.Contains(got, "<modified-files>\nb.go\nc.go\n</modified-files>") {
		t.Errorf("改过的文件清单不对：%s", got)
	}
}

func TestFileOpsTailEmpty(t *testing.T) {
	if got := fileOpsTail([]*types.Message{textMsg(types.UserRole, "你好")}); got != "" {
		t.Errorf("没有文件操作时不该加尾巴，实际 %q", got)
	}
}
