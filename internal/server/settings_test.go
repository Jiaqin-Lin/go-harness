package server

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/reuben/go-harness/internal/types"
)

// 压缩阈值改完，下一条消息就该按新值来 —— 不用重启服务、不用重开会话。
// 这条就是当初「我改了怎么没生效」的根因，钉在这儿
func TestCompactThresholdTakesEffectNextTurn(t *testing.T) {
	llm := &stubLLM{
		// 保留段要够大，切点才落得上去（keepRecent 是 20000 个 token）
		replies: []string{strings.Repeat("长答复", 30_000), strings.Repeat("长答复", 30_000)},
		summary: "## 摘要\n之前聊了很多",
		// 报 6 万 token：默认线（25 万）压不动它，调低之后就该压
		usage: &types.Usage{Input: 60_000, Cached: 50_000, Output: 500, Total: 60_500},
	}
	base, cwd := newTestServer(t, llm)
	const id = "aaaa1111bbbb2222"

	first := eventTypes(runChat(t, base, map[string]any{"cwd": cwd, "sessionId": id, "text": "第一问"}))
	if slices.Contains(first, "compacting") {
		t.Fatalf("默认阈值下不该压，实际事件：%v", first)
	}

	if code, body := postJSON(t, base, "/api/settings", map[string]any{"compactAt": 50_000}); code != http.StatusOK {
		t.Fatalf("改设置该 200，实际 %d：%s", code, body)
	}

	second := eventTypes(runChat(t, base, map[string]any{"cwd": cwd, "sessionId": id, "text": "第二问"}))
	if !slices.Contains(second, "compacting") {
		t.Fatalf("阈值调低之后该压，实际事件：%v", second)
	}

	// 压完模型看到的第一条该是摘要
	histories := llm.histories()
	seen := histories[len(histories)-1]
	if len(seen) == 0 || !strings.Contains(seen[0].Text(), "之前聊了很多") {
		t.Errorf("压缩后模型该先看到摘要，实际 %v", textsOf(seen))
	}

	// 再调回默认：meta 上就该是默认值
	if code, body := postJSON(t, base, "/api/settings", map[string]any{"compactAt": 0}); code != http.StatusOK {
		t.Fatalf("改回默认该 200，实际 %d：%s", code, body)
	}
	meta := fetchMeta(t, base)
	if meta.CompactAt != 250_000 || meta.CompactLimit != 250_000 {
		t.Errorf("回到默认该是 250000，实际 %+v", meta)
	}
}

// 阈值是设置里的数，不是编译进去的常量：存了就在 meta 上看得见，前端拿它显示
func TestSettingsReflectedInMeta(t *testing.T) {
	base, _ := newTestServer(t, &stubLLM{})
	if code, body := postJSON(t, base, "/api/settings", map[string]any{"compactAt": 120_000}); code != http.StatusOK {
		t.Fatalf("该 200，实际 %d：%s", code, body)
	}

	meta := fetchMeta(t, base)
	if meta.Model != "deepseek-flash" || meta.CompactAt != 120_000 || meta.CompactLimit != 120_000 {
		t.Errorf("meta 不对：%+v", meta)
	}
}

func TestSettingsValidation(t *testing.T) {
	base, _ := newTestServer(t, &stubLLM{})

	cases := []struct {
		name string
		body map[string]any
	}{
		{"负数", map[string]any{"compactAt": -1}},
		{"大得离谱", map[string]any{"compactAt": maxCompactAt + 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if code, body := postJSON(t, base, "/api/settings", c.body); code != http.StatusBadRequest {
				t.Errorf("该 400，实际 %d：%s", code, body)
			}
		})
	}
}

// fetchMeta 读一次 /api/meta
func fetchMeta(t *testing.T, base string) metaDTO {
	t.Helper()
	resp, err := http.Get(base + "/api/meta")
	if err != nil {
		t.Fatalf("读 meta 失败：%v", err)
	}
	defer resp.Body.Close()
	var meta metaDTO
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		t.Fatalf("meta 不是合法 JSON：%v", err)
	}
	return meta
}

type metaDTO struct {
	Model        string `json:"model"`
	CompactAt    int    `json:"compactAt"`
	CompactLimit int    `json:"compactLimit"`
}

func textsOf(messages []*types.Message) []string {
	var out []string
	for _, msg := range messages {
		out = append(out, msg.Text())
	}
	return out
}
