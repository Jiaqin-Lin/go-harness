package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reuben/go-harness/internal/session"
	"github.com/reuben/go-harness/internal/types"
)

// stubLLM 是假模型：不打网络、不花钱，按脚本一次一句地吐文本，
// 顺便把每次收到的历史记下来 —— 分叉的测试全靠它
type stubLLM struct {
	mu      sync.Mutex
	replies []string
	seen    [][]*types.Message
	calls   int
	gate    chan struct{} // 非 nil 就先卡住，用来占着锁测 409
	started chan struct{} // 每次调用都会 close 一次，测试用它等「真的跑起来了」
	once    sync.Once
}

func (s *stubLLM) generate(ctx context.Context, conversation types.Conversation) <-chan types.AssistantEvent {
	s.mu.Lock()
	s.calls++
	reply := s.replies[min(s.calls-1, len(s.replies)-1)]
	// 只记带工具的调用：后台生成标题那条也是走这个桩，但它没有工具，
	// 记进来会把对话历史的断言搅乱
	if len(conversation.Tools) > 0 {
		s.seen = append(s.seen, conversation.Messages)
	}
	gate := s.gate
	s.mu.Unlock()

	if s.started != nil {
		s.once.Do(func() { close(s.started) })
	}

	events := make(chan types.AssistantEvent)
	go func() {
		defer close(events)
		if gate != nil {
			select {
			case <-gate:
			case <-ctx.Done():
				events <- types.AssistantEvent{Type: types.AssistantErrorEvent,
					Message: &types.Message{StopReason: types.AbortedStop}}
				return
			}
		}
		events <- types.AssistantEvent{Type: types.AssistantStartEvent}
		events <- types.AssistantEvent{Type: types.AssistantTextDeltaEvent, Delta: reply, ContentBlockIndex: 0}
		events <- types.AssistantEvent{Type: types.AssistantDoneEvent, Message: &types.Message{
			StopReason: types.NormalStop,
			Usage:      &types.Usage{Input: 10, Cached: 4, Output: 5, Total: 15},
		}}
	}()
	return events
}

func (s *stubLLM) histories() [][]*types.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.seen)
}

// newTestServer 起一个真的 HTTP 服务。SSE 必须走真连接才测得出来：
// httptest.ResponseRecorder 的 flush 行为跟真响应对不上，容易测了个假的。
// 走的是真正的 Listen（端口 0 让内核分），Host 白名单才会跟着实际端口走
func newTestServer(t *testing.T, llm *stubLLM) (base, cwd string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // 会话别写进真的 ~/.go-harness
	cwd = t.TempDir()

	srv := New(Config{Port: 0, Cwd: cwd, Generate: llm.generate})
	listener, err := srv.Listen()
	if err != nil {
		t.Fatalf("监听失败：%v", err)
	}
	go srv.Serve(listener)
	t.Cleanup(func() { listener.Close() })

	return "http://" + listener.Addr().String(), cwd
}

// runChat 发一条消息，把整条 SSE 流读完
func runChat(t *testing.T, base string, body map[string]any) []wireEvent {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("编码请求失败：%v", err)
	}
	resp, err := http.Post(base+"/api/chat", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("发消息失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chat 返回 %d：%s", resp.StatusCode, readAll(t, resp))
	}

	var events []wireEvent
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 1<<16), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event wireEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatalf("事件不是合法 JSON：%v（%s）", err, line)
		}
		events = append(events, event)
	}
	return events
}

func eventTypes(events []wireEvent) []string {
	kinds := make([]string, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Type)
	}
	return kinds
}

func entriesOf(events []wireEvent) []entryDTO {
	var entries []entryDTO
	for _, event := range events {
		if event.Type == "entry" && event.Entry != nil {
			entries = append(entries, *event.Entry)
		}
	}
	return entries
}

// waitForSession 轮询到会话落够 want 条 entry 为止。不能只看「文件在不在」：
// 落盘是一条条 append 的，读到一半就是一份合法的前缀（少几条），
// 那时候断言会随机失败 —— 客户端断开之后服务端本来也还在收尾
func waitForSession(t *testing.T, base, cwd, id string, want int) sessionView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(base + "/api/session?cwd=" + url.QueryEscape(cwd) + "&id=" + id)
		if err == nil {
			if resp.StatusCode == http.StatusOK {
				var view sessionView
				err := json.NewDecoder(resp.Body).Decode(&view)
				resp.Body.Close()
				if err == nil && len(view.Entries) >= want {
					return view
				}
			} else {
				resp.Body.Close()
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("等不到落够 %d 条 entry", want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func fetchSession(t *testing.T, base, cwd, id string) sessionView {
	t.Helper()
	resp, err := http.Get(base + "/api/session?cwd=" + url.QueryEscape(cwd) + "&id=" + id)
	if err != nil {
		t.Fatalf("读会话失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("读会话返回 %d：%s", resp.StatusCode, readAll(t, resp))
	}
	var view sessionView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatalf("会话不是合法 JSON：%v", err)
	}
	return view
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			return sb.String()
		}
	}
}

// 一轮跑完：事件顺序对、entry 挂得对、文件真的落下来了
func TestChatStreamsAndPersists(t *testing.T) {
	llm := &stubLLM{replies: []string{"第一答"}}
	base, cwd := newTestServer(t, llm)
	const id = "aaaa1111bbbb2222"

	events := runChat(t, base, map[string]any{"cwd": cwd, "sessionId": id, "text": "第一问"})

	want := []string{"turn_start", "assistant_start", "text_delta", "message_end", "entry", "entry", "done"}
	if got := eventTypes(events); !slices.Equal(got, want) {
		t.Errorf("事件序列不对：\n得到 %v\n想要 %v", got, want)
	}

	entries := entriesOf(events)
	if len(entries) != 2 {
		t.Fatalf("该落两条 entry（用户 + 助手），实际 %d 条", len(entries))
	}
	if entries[0].Message.Role != "user" || entries[0].ParentID != "" {
		t.Errorf("第一条该是根上的用户消息，实际 %+v", entries[0])
	}
	if entries[1].Message.Role != "assistant" || entries[1].ParentID != entries[0].ID {
		t.Errorf("助手消息该挂在用户消息下面，实际 %+v", entries[1])
	}
	if usage := entries[1].Message.Usage; usage == nil || usage.Output != 5 {
		t.Errorf("落盘的助手消息该带着用量，实际 %+v", usage)
	}

	infos, err := session.List(cwd)
	if err != nil || len(infos) != 1 || infos[0].ID != id {
		t.Fatalf("文件该已经落下来了：%+v err=%v", infos, err)
	}

	view := fetchSession(t, base, cwd, id)
	if len(view.Entries) != 2 || view.LeafID != entries[1].ID {
		t.Errorf("GET /api/session 该跟 entry 事件对上：%+v", view)
	}
}

// 分叉：从旧节点再挂一条，模型看到的历史只有「根 → 分支点」，
// 旁边那条支路在树上、但不在这次请求里
func TestBranchSendsOnlyThatPathToTheModel(t *testing.T) {
	llm := &stubLLM{replies: []string{"第一答", "第二答"}}
	base, cwd := newTestServer(t, llm)
	const id = "cccc3333dddd4444"

	first := entriesOf(runChat(t, base, map[string]any{"cwd": cwd, "sessionId": id, "text": "第一问"}))
	branchPoint := first[0].ID

	second := entriesOf(runChat(t, base, map[string]any{
		"cwd": cwd, "sessionId": id, "parentId": branchPoint, "text": "换个问法",
	}))

	if second[0].ParentID != branchPoint {
		t.Errorf("新消息该挂在分支点 %s 下面，实际挂在 %q", branchPoint, second[0].ParentID)
	}
	if second[0].ID == first[1].ID || second[0].ParentID == first[1].ID {
		t.Error("新消息跟原来的答复合成了兄弟，不该是父子")
	}

	histories := llm.histories()
	if len(histories) != 2 {
		t.Fatalf("该调了两次模型，实际 %d 次", len(histories))
	}
	// 模型看到的是「分支点那条路径 + 这次的新消息」，旁边那条支路不在里面
	var seen []string
	for _, msg := range histories[1] {
		seen = append(seen, msg.Text())
	}
	if !slices.Equal(seen, []string{"第一问", "换个问法"}) {
		t.Errorf("分叉之后模型该只看到根到分支点再加新消息，实际看到 %v", seen)
	}

	// 树上四个节点：第一问 -> [第一答, 换个问法 -> 第二答]
	if view := fetchSession(t, base, cwd, id); len(view.Entries) != 4 {
		t.Errorf("树上该有四个节点，实际 %d 个：%+v", len(view.Entries), view.Entries)
	}
}

// 同一个会话同时只跑一轮：第二条消息直接 409，不排队
func TestChatConflictsWhileRunning(t *testing.T) {
	gate := make(chan struct{})
	llm := &stubLLM{replies: []string{"慢答"}, gate: gate, started: make(chan struct{})}
	base, cwd := newTestServer(t, llm)
	const id = "eeee5555ffff6666"

	done := make(chan struct{})
	go func() {
		defer close(done)
		runChat(t, base, map[string]any{"cwd": cwd, "sessionId": id, "text": "第一问"})
	}()

	select {
	case <-llm.started:
	case <-time.After(5 * time.Second):
		t.Fatal("第一轮没跑起来")
	}

	payload, _ := json.Marshal(map[string]any{"cwd": cwd, "sessionId": id, "text": "插队"})
	resp, err := http.Post(base+"/api/chat", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("第二条消息发失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("跑着的时候该 409，实际 %d", resp.StatusCode)
	}

	close(gate)
	<-done
}

// 写操作只认 application/json：跨站的表单发不出这个类型
func TestPostRequiresJSON(t *testing.T) {
	llm := &stubLLM{replies: []string{"答"}}
	base, _ := newTestServer(t, llm)

	resp, err := http.Post(base+"/api/chat", "text/plain", strings.NewReader(`{"text":"hi"}`))
	if err != nil {
		t.Fatalf("请求发失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("该 415，实际 %d", resp.StatusCode)
	}
}

// Host 不是本机的请求一律拒掉（DNS rebinding 防的就是这个）
func TestForeignHostRejected(t *testing.T) {
	llm := &stubLLM{replies: []string{"答"}}
	base, _ := newTestServer(t, llm)

	req, err := http.NewRequest(http.MethodGet, base+"/api/projects", nil)
	if err != nil {
		t.Fatalf("建请求失败：%v", err)
	}
	req.Host = "evil.example.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求发失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("外来的 Host 该 403，实际 %d", resp.StatusCode)
	}
}

// 入参不对时要在开流之前就报错（开流之后只剩事件能说话）
func TestChatValidation(t *testing.T) {
	llm := &stubLLM{replies: []string{"答"}}
	base, cwd := newTestServer(t, llm)

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"空消息", map[string]any{"cwd": cwd, "sessionId": "aaaa1111bbbb2222", "text": "   "}, http.StatusBadRequest},
		{"id 形状不对", map[string]any{"cwd": cwd, "sessionId": "nope", "text": "hi"}, http.StatusBadRequest},
		{"cwd 不是目录", map[string]any{"cwd": cwd + "/不存在", "sessionId": "aaaa1111bbbb2222", "text": "hi"}, http.StatusBadRequest},
		{"parentId 不在会话里", map[string]any{"cwd": cwd, "sessionId": "aaaa1111bbbb2222", "parentId": "deadbeef", "text": "hi"}, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			payload, _ := json.Marshal(c.body)
			resp, err := http.Post(base+"/api/chat", "application/json", bytes.NewReader(payload))
			if err != nil {
				t.Fatalf("请求发失败：%v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != c.want {
				t.Errorf("该 %d，实际 %d", c.want, resp.StatusCode)
			}
		})
	}
}

// 标题是另一条 goroutine 生成、写 sidecar 的：这一轮的响应不等它，
// 但它最后得真的落下来
func TestTitleGeneratedInBackground(t *testing.T) {
	llm := &stubLLM{replies: []string{"第一答", "登录报错排查"}}
	base, cwd := newTestServer(t, llm)
	const id = "abcd1234abcd1234"

	runChat(t, base, map[string]any{"cwd": cwd, "sessionId": id, "text": "帮我看看登录为什么报错"})

	deadline := time.Now().Add(5 * time.Second)
	for {
		infos, err := session.List(cwd)
		if err != nil {
			t.Fatalf("列会话失败：%v", err)
		}
		if len(infos) == 1 && infos[0].Title != "" {
			if infos[0].Title != "登录报错排查" {
				t.Fatalf("标题不对：%q", infos[0].Title)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("等不到标题落盘")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// 「停止」走端点：服务端自己取消那一轮，照样落盘、照样把流收尾。
// 客户端掐连接的话它不知道服务端什么时候收完尾，重读会读到旧状态
func TestAbortEndpointStopsRunningTurn(t *testing.T) {
	gate := make(chan struct{})
	llm := &stubLLM{replies: []string{"慢答"}, gate: gate, started: make(chan struct{})}
	base, cwd := newTestServer(t, llm)
	const id = "7777aaaa7777bbbb"

	done := make(chan []wireEvent, 1)
	go func() {
		done <- runChat(t, base, map[string]any{"cwd": cwd, "sessionId": id, "text": "跑一半就停"})
	}()

	select {
	case <-llm.started:
	case <-time.After(5 * time.Second):
		t.Fatal("第一轮没跑起来")
	}

	payload, _ := json.Marshal(map[string]any{"cwd": cwd, "sessionId": id})
	resp, err := http.Post(base+"/api/chat/abort", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("调 abort 失败：%v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("abort 该 200，实际 %d", resp.StatusCode)
	}

	events := <-done
	if kinds := eventTypes(events); kinds[len(kinds)-1] != "done" {
		t.Errorf("取消之后流也该正常收尾（done 在最后），实际 %v", kinds)
	}

	view := fetchSession(t, base, cwd, id)
	if len(view.Entries) != 2 || view.Entries[1].Message.StopReason != "aborted" {
		t.Fatalf("取消之后该落下一整轮（用户 + 已中止的助手），实际 %+v", view.Entries)
	}

	// 没在跑的时候再点，是 409 而不是静默成功
	resp2, err := http.Post(base+"/api/chat/abort", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("第二次调 abort 失败：%v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Errorf("没在跑该 409，实际 %d", resp2.StatusCode)
	}
}

// 找不到的会话是 400，不是把服务打崩
func TestSessionNotFound(t *testing.T) {
	llm := &stubLLM{replies: []string{"答"}}
	base, cwd := newTestServer(t, llm)

	resp, err := http.Get(base + "/api/session?cwd=" + url.QueryEscape(cwd) + "&id=ffffffffffffffff")
	if err != nil {
		t.Fatalf("请求发失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("该 400，实际 %d", resp.StatusCode)
	}
}

// 点「停止」= 关掉这个请求，服务端照样把这一轮落盘，文件不留半轮
func TestAbortStillPersistsTurn(t *testing.T) {
	gate := make(chan struct{})
	llm := &stubLLM{replies: []string{"慢答"}, gate: gate, started: make(chan struct{})}
	base, cwd := newTestServer(t, llm)
	const id = "9999aaaa8888bbbb"

	ctx, cancel := context.WithCancel(context.Background())
	payload, _ := json.Marshal(map[string]any{"cwd": cwd, "sessionId": id, "text": "跑一半就停"})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/chat", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	go func() {
		<-llm.started
		cancel()
	}()
	if _, err := http.DefaultClient.Do(req); err == nil {
		// 客户端这条连接断了是预期的，服务端那边的落盘才是被测的东西
		t.Log("请求正常结束了")
	}

	// 客户端断开是立刻返回的，服务端那边还在收尾 —— 等它把这一轮写完
	view := waitForSession(t, base, cwd, id, 2)
	if len(view.Entries) != 2 {
		t.Fatalf("取消之后也该留下一整轮（用户 + 被中止的助手），实际 %d 条", len(view.Entries))
	}
	if got := view.Entries[1].Message.StopReason; got != "aborted" {
		t.Errorf("第二条该记成中止，实际 %q", got)
	}
}
