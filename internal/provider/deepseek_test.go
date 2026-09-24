package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/reuben/go-harness/internal/types"
)

// 流建立之后要发一次 start：MessageStartEvent 说的是「请求已发出」，它说的是
// 「模型开始回了」。推理模型首 token 要等几秒，UI 靠这两个事件区分等待和生成
func TestGenerateEmitsAssistantStart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"好"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	var got []types.AssistantEventType
	for event := range NewDeepSeek("key", server.URL, "deepseek-flash").Generate(context.Background(), types.Conversation{}) {
		got = append(got, event.Type)
	}

	want := []types.AssistantEventType{types.AssistantStartEvent, types.AssistantTextDeltaEvent, types.AssistantDoneEvent}
	if len(got) != len(want) {
		t.Fatalf("事件序列\n得到 %v\n想要 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("事件序列\n得到 %v\n想要 %v", got, want)
		}
	}
}
