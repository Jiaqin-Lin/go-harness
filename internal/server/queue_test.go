package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/reuben/go-harness/internal/types"
)

// startGatedChat 开一轮并卡住它（模型等着 gate），返回读完流的通道。
// 卡住的那段时间正是「用户在跑的时候又发了一句」的窗口
func startGatedChat(t *testing.T, base, cwd, id, text string) chan []wireEvent {
	t.Helper()
	done := make(chan []wireEvent, 1)
	go func() { done <- runChat(t, base, map[string]any{"cwd": cwd, "sessionId": id, "text": text}) }()
	return done
}

// 跑着的时候发的那一句：先排队，投递时报一声（带认领 id），落盘落在正确的位置上，
// 且模型下一次调用看得见它。插话和排队两种都走这条 —— 差别只在什么时候投
func TestQueuedMessageDeliveredMidRun(t *testing.T) {
	for _, mode := range []string{queueSteer, queueFollowUp} {
		t.Run(mode, func(t *testing.T) {
			gate := make(chan struct{})
			llm := &stubLLM{replies: []string{"答"}, gate: gate, started: make(chan struct{})}
			base, cwd := newTestServer(t, llm)
			const id = "aaaabbbbccccdddd"

			done := startGatedChat(t, base, cwd, id, "第一问")
			<-llm.started

			status, body := postJSON(t, base, "/api/chat/queue", map[string]any{
				"cwd": cwd, "sessionId": id, "text": "插一句", "mode": mode,
			})
			if status != http.StatusOK {
				t.Fatalf("排队该 200，实际 %d：%s", status, body)
			}
			var ack struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal([]byte(body), &ack); err != nil || ack.ID == "" {
				t.Fatalf("排队该给一个认领 id，实际 %s", body)
			}

			close(gate)
			events := <-done

			var delivered []wireEvent
			for _, event := range events {
				if event.Type == "queued_delivered" {
					delivered = append(delivered, event)
				}
			}
			if len(delivered) != 1 {
				t.Fatalf("该投一次，实际 %d 次（%v）", len(delivered), eventTypes(events))
			}
			if delivered[0].QueueID != ack.ID {
				t.Errorf("投递该带着排队时给的 id：%q vs %q", delivered[0].QueueID, ack.ID)
			}
			if message := delivered[0].Message; message == nil || message.Role != "user" || message.Blocks[0].Text != "插一句" {
				t.Errorf("投递事件该带着那条用户消息，实际 %+v", delivered[0].Message)
			}

			// 落盘顺序就是它进上下文的顺序：问、答、插话、答
			view := fetchSession(t, base, cwd, id)
			if len(view.Entries) != 4 {
				t.Fatalf("该落 4 条 entry，实际 %d 条", len(view.Entries))
			}
			queued := view.Entries[2]
			if queued.Message.Role != "user" || queued.Message.Blocks[0].Text != "插一句" {
				t.Errorf("第三条该是排队的这句，实际 %+v", queued.Message)
			}
			if queued.ParentID != view.Entries[1].ID {
				t.Errorf("它该接在上一轮的答后面，实际父节点 %q", queued.ParentID)
			}
			if view.LeafID != view.Entries[3].ID {
				t.Errorf("叶子该是最后那条答，实际 %q", view.LeafID)
			}

			// 模型第二次调用：历史末尾就是这句
			histories := llm.histories()
			if len(histories) != 2 {
				t.Fatalf("该调两次模型，实际 %d 次", len(histories))
			}
			if last := histories[1][len(histories[1])-1]; last.Text() != "插一句" {
				t.Errorf("第二次调用该带着这句，实际结尾是 %q", last.Text())
			}
		})
	}
}

// 没在跑的时候排队：409，前端收到就退回普通发送
func TestQueueConflictsWhenIdle(t *testing.T) {
	llm := &stubLLM{replies: []string{"答"}}
	base, cwd := newTestServer(t, llm)

	status, body := postJSON(t, base, "/api/chat/queue", map[string]any{
		"cwd": cwd, "sessionId": "aaaabbbbccccdddd", "text": "hi",
	})
	if status != http.StatusConflict {
		t.Errorf("没在跑该 409，实际 %d：%s", status, body)
	}
}

// 排队的那句在投出去之前撤得回来；投出去之后只剩「摘掉界面上那条」
func TestDequeueQueueItem(t *testing.T) {
	gate := make(chan struct{})
	llm := &stubLLM{replies: []string{"答"}, gate: gate, started: make(chan struct{})}
	base, cwd := newTestServer(t, llm)
	const id = "eeeeffffeeeef000"

	done := startGatedChat(t, base, cwd, id, "第一问")
	<-llm.started

	_, body := postJSON(t, base, "/api/chat/queue", map[string]any{
		"cwd": cwd, "sessionId": id, "text": "发错了", "mode": queueSteer,
	})
	var ack struct {
		ID string `json:"id"`
	}
	json.Unmarshal([]byte(body), &ack)

	status, body := postJSON(t, base, "/api/chat/dequeue", map[string]any{"cwd": cwd, "sessionId": id, "id": ack.ID})
	if status != http.StatusOK || !strings.Contains(body, "true") {
		t.Fatalf("还没投出去该撤得回来：%d %s", status, body)
	}

	close(gate)
	events := <-done

	for _, event := range events {
		if event.Type == "queued_delivered" {
			t.Error("撤回来的那条不该被投出去")
		}
	}
	view := fetchSession(t, base, cwd, id)
	if len(view.Entries) != 2 {
		t.Errorf("撤回来的那条不该落盘，实际落了 %d 条 entry", len(view.Entries))
	}

	// 已经跑完了再撤：没有了，removed=false（前端照旧摘掉 chip）
	status, body = postJSON(t, base, "/api/chat/dequeue", map[string]any{"cwd": cwd, "sessionId": id, "id": ack.ID})
	if status != http.StatusOK || !strings.Contains(body, "false") {
		t.Errorf("跑完之后该报 false：%d %s", status, body)
	}
}

// 中止：还排着的消息不进对话 —— 用户按了停止，排队的东西该还回他自己手上
func TestAbortDropsQueue(t *testing.T) {
	gate := make(chan struct{})
	llm := &stubLLM{replies: []string{"答"}, gate: gate, started: make(chan struct{})}
	base, cwd := newTestServer(t, llm)
	const id = "9999888877776666"

	done := startGatedChat(t, base, cwd, id, "第一问")
	<-llm.started

	postJSON(t, base, "/api/chat/queue", map[string]any{
		"cwd": cwd, "sessionId": id, "text": "插一句", "mode": queueSteer,
	})
	if status, body := postJSON(t, base, "/api/chat/abort", map[string]any{"cwd": cwd, "sessionId": id}); status != http.StatusOK {
		t.Fatalf("中止该 200，实际 %d：%s", status, body)
	}

	events := <-done // ctx 取消之后这一轮自己收尾，不用等 gate
	for _, event := range events {
		if event.Type == "queued_delivered" {
			t.Error("中止之后不该再投排队的东西")
		}
	}

	view := fetchSession(t, base, cwd, id)
	for _, entry := range view.Entries {
		if entry.Message != nil && entry.Message.Blocks != nil && entry.Message.Blocks[0].Text == "插一句" {
			t.Error("中止之后排队的消息不该落盘")
		}
	}

	// gate 故意不放：后台那条生成标题的调用会一直卡着。放掉它的话，它会在
	// 测试收尾（TempDir 清理）之后才去写文件，清理随机失败
}

// ── 用户发图 ─────────────────────────────────────────────

// testPNG 造一张实心 PNG
func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := range width {
		for y := range height {
			img.Set(x, y, color.RGBA{R: 0x30, G: uint8(x % 256), B: uint8(y % 256), A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("编码 PNG 失败：%v", err)
	}
	return buf.Bytes()
}

// 用户发的图：进得了模型的上下文、落得了盘、取回来跟原图一模一样
func TestChatWithImages(t *testing.T) {
	llm := &stubLLM{replies: []string{"看到了一只猫"}}
	base, cwd := newTestServer(t, llm)
	const id = "1234123412341234"

	raw := testPNG(t, 24, 16)
	events := runChat(t, base, map[string]any{
		"cwd": cwd, "sessionId": id, "text": "看看这张",
		"images": []string{base64.StdEncoding.EncodeToString(raw)},
	})

	entries := entriesOf(events)
	if len(entries) != 2 {
		t.Fatalf("该落 2 条 entry，实际 %d 条", len(entries))
	}
	blocks := entries[0].Message.Blocks
	if len(blocks) != 2 || blocks[0].Kind != "text" || blocks[1].Kind != "image" {
		t.Fatalf("用户消息该是「正文 + 图」，实际 %+v", blocks)
	}
	// ImageBytes 是按 base64 长度估的（DecodedLen 给的是上界，最多多报两个字节），
	// 界面拿它显示 "xx KB" 够用；精确到字节的校验在下面取图那一段
	if blocks[1].ImageMime != "image/png" || blocks[1].ImageBytes < len(raw) || blocks[1].ImageBytes > len(raw)+2 {
		t.Errorf("图片块该报出格式和体积（约 %d 字节），实际 %+v", len(raw), blocks[1])
	}

	// 模型收到的就是图片块本身（wire 那边会转成 content part）
	histories := llm.histories()
	if got := histories[0][0].ContentBlocks[1].Kind; got != types.ImageBlock {
		t.Errorf("模型该收到图片块，实际 %q", got)
	}

	// 取图端点：按 entry + 块下标拿回来
	resp, err := http.Get(base + "/api/image?cwd=" + url.QueryEscape(cwd) + "&id=" + id + "&entry=" + entries[0].ID + "&block=1")
	if err != nil {
		t.Fatalf("取图失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("取图该 200，实际 %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type 该是 image/png，实际 %q", got)
	}
	if body := readAll(t, resp); !bytes.Equal([]byte(body), raw) {
		t.Error("取回来的图跟发出去的不一样")
	}
}

// 只发图不写字也是合法的一句：正文为空，侧栏得给个交代
func TestChatWithImageOnlyMessage(t *testing.T) {
	llm := &stubLLM{replies: []string{"嗯"}}
	base, cwd := newTestServer(t, llm)
	const id = "1010101010101010"

	raw := testPNG(t, 8, 8)
	events := runChat(t, base, map[string]any{
		"cwd": cwd, "sessionId": id, "images": []string{base64.StdEncoding.EncodeToString(raw)},
	})

	entries := entriesOf(events)
	blocks := entries[0].Message.Blocks
	if len(blocks) != 1 || blocks[0].Kind != "image" {
		t.Fatalf("只发图时消息里该只有图片块，实际 %+v", blocks)
	}
	// 模型那边也得收到图，不是一条空消息
	if len(llm.histories()[0][0].ContentBlocks) != 1 {
		t.Error("只发图的那句也该带着图片块进上下文")
	}
}

func TestImageEndpointRejectsNonImageBlocks(t *testing.T) {
	llm := &stubLLM{replies: []string{"嗯"}}
	base, cwd := newTestServer(t, llm)
	const id = "5555444433332222"

	events := runChat(t, base, map[string]any{"cwd": cwd, "sessionId": id, "text": "只有文字"})
	entries := entriesOf(events)

	cases := []struct {
		name              string
		entry, blockIndex string
	}{
		{"不是图片块", entries[0].ID, "0"},
		{"块下标越界", entries[0].ID, "9"},
		{"entry 不在会话里", "deadbeef", "0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, err := http.Get(base + "/api/image?cwd=" + url.QueryEscape(cwd) + "&id=" + id + "&entry=" + c.entry + "&block=" + c.blockIndex)
			if err != nil {
				t.Fatalf("请求失败：%v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("该 404，实际 %d", resp.StatusCode)
			}
		})
	}
}

func TestChatImageValidation(t *testing.T) {
	llm := &stubLLM{replies: []string{"答"}}
	base, cwd := newTestServer(t, llm)

	small := base64.StdEncoding.EncodeToString(testPNG(t, 8, 8))
	tooMany := make([]string, maxImages+1)
	for i := range tooMany {
		tooMany[i] = small
	}
	tooBig := base64.StdEncoding.EncodeToString(make([]byte, maxImageRaw+1))

	cases := []struct {
		name string
		body map[string]any
	}{
		{"超过张数上限", map[string]any{"cwd": cwd, "sessionId": "aaaa1111bbbb2222", "text": "看", "images": tooMany}},
		{"不是 base64", map[string]any{"cwd": cwd, "sessionId": "aaaa1111bbbb2222", "text": "看", "images": []string{"!!!不是!!!"}}},
		{"认不出格式", map[string]any{"cwd": cwd, "sessionId": "aaaa1111bbbb2222", "text": "看",
			"images": []string{base64.StdEncoding.EncodeToString([]byte("这不是图片"))}}},
		{"单张太大", map[string]any{"cwd": cwd, "sessionId": "aaaa1111bbbb2222", "text": "看", "images": []string{tooBig}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := postJSON(t, base, "/api/chat", c.body)
			if status != http.StatusBadRequest {
				t.Errorf("该 400，实际 %d：%s", status, body)
			}
		})
	}
}

// 请求体超限要报 413，不是没头没脑的「不是合法 JSON」：带图的消息最容易撞上它。
// 这里的请求体得是一段「合规矩但太长」的 JSON —— 塞一堆 0 字节的话解码器第一个
// 字节就报语法错，根本走不到上限那一步
func TestDecodeBodyTooLarge(t *testing.T) {
	body := []byte(`{"text":"` + strings.Repeat("a", maxChatBody) + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	var into map[string]any
	if decodeBody(rec, req, &into) {
		t.Fatal("超限的请求体该被拒掉")
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("该 413，实际 %d", rec.Code)
	}
}

func TestDecodeBodyBadJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader("{不是 JSON"))
	rec := httptest.NewRecorder()

	var into map[string]any
	if decodeBody(rec, req, &into) {
		t.Fatal("坏 JSON 该被拒掉")
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("该 400，实际 %d", rec.Code)
	}
}
