package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/reuben/go-harness/internal/types"
)

func userMessage(text string) *types.Message {
	return &types.Message{
		Role:          types.UserRole,
		ContentBlocks: []types.ContentBlock{types.NewTextBlock(text)},
	}
}

// setHome 把主目录指到临时目录，测试数据不往真实的 ~/.go-harness 里写
func setHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// graft 直接往树上挂节点，绕过 Append：Append 只会接在当前叶子后面，构造分支只有这一条路
func (s *Session) graft(id, parentID string, msg *types.Message) {
	entry := &Entry{Type: messageType, ID: id, Timestamp: timestampNow(), Message: msg}
	if parentID != "" {
		entry.ParentID = &parentID
	}
	s.entries = append(s.entries, entry)
	s.byId[id] = entry
}

// 文件里存的必须是 harness 自己的消息结构，且字段名是小写驼峰的固定形状 ——
// 这份格式是磁盘上的契约，改了等于改了会话文件格式
func TestEntryJSONRoundTrip(t *testing.T) {
	parent := "root"
	entry := &Entry{
		Type:      messageType,
		ID:        "e1",
		ParentID:  &parent,
		Timestamp: "2026-09-24T10:00:01.000Z",
		Message: &types.Message{
			Role: types.AssistantRole,
			ContentBlocks: []types.ContentBlock{
				{Kind: types.TextBlock, Text: "看看"},
				{Kind: types.ThinkingBlock, Thinking: "想一下"},
				{Kind: types.ToolBlock, ToolCallID: "c1", ToolName: "bash", Args: `{"command":"ls"}`},
			},
			Usage:        &types.Usage{Input: 1, Cached: 2, Output: 3, Total: 6},
			StopReason:   types.ToolUserStop,
			ToolCallID:   "c1",
			ToolName:     "bash",
			ErrorMessage: "错了",
			IsError:      true,
		},
	}

	line, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("编码失败：%v", err)
	}
	for _, want := range []string{`"type":"message"`, `"parentId":"root"`, `"role":"assistant"`, `"toolCallId":"c1"`} {
		if !strings.Contains(string(line), want) {
			t.Errorf("编码结果里没有 %s：%s", want, line)
		}
	}
	if strings.Contains(string(line), "imageData") {
		t.Errorf("没填的字段不该出现在文件里：%s", line)
	}

	var got Entry
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	if !reflect.DeepEqual(&got, entry) {
		t.Errorf("往返之后字段变了：\n原 %+v\n后 %+v", entry, &got)
	}
}

// 从叶子回溯出来的才是模型该看到的历史：分支上的另一条不进来
func TestMessagesFollowsLeafPath(t *testing.T) {
	s := &Session{byId: map[string]*Entry{}}
	s.graft("root", "", userMessage("一"))
	s.graft("a", "root", userMessage("二"))
	s.graft("b", "a", userMessage("三"))
	s.graft("c", "root", userMessage("四"))

	texts := func(leafID string) []string {
		s.leafID = leafID
		var got []string
		for _, msg := range s.Messages() {
			got = append(got, msg.ContentBlocks[0].Text)
		}
		return got
	}

	if got := texts("c"); !reflect.DeepEqual(got, []string{"一", "四"}) {
		t.Errorf("leaf=c 该只出 root 和 c，实际 %v", got)
	}
	if got := texts("b"); !reflect.DeepEqual(got, []string{"一", "二", "三"}) {
		t.Errorf("leaf=b 该出整条链，实际 %v", got)
	}
}

// 打开就退出的会话不该在磁盘上留垃圾文件：文件是第一次落盘时才建的
func TestNoFileUntilFirstAppend(t *testing.T) {
	setHome(t)
	cwd := t.TempDir()

	s, err := Create(cwd)
	if err != nil {
		t.Fatalf("建会话失败：%v", err)
	}

	dir, _ := dirFor(cwd)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("还没落盘，目录就不该存在：%v", err)
	}
	if s.Path() != "" {
		t.Errorf("还没落盘，Path 该是空串，实际 %q", s.Path())
	}

	if _, err := s.Append(userMessage("第一句")); err != nil {
		t.Fatalf("落盘失败：%v", err)
	}
	if s.Path() == "" {
		t.Fatal("落盘之后 Path 不该还是空")
	}
	if infos, err := List(cwd); err != nil || len(infos) != 1 {
		t.Fatalf("落盘之后该能列出来，实际 %+v err=%v", infos, err)
	}

	data, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatalf("读会话文件失败：%v", err)
	}
	if lines := strings.Count(string(data), "\n"); lines != 2 {
		t.Errorf("该是 header + 一条 entry 两行，实际 %d 行：%s", lines, data)
	}
}

// 落盘单位是一轮：core.Run 返回后一次性 Append，读回来必须一模一样
func TestContinueRecentRestoresHistory(t *testing.T) {
	setHome(t)
	cwd := t.TempDir()

	s, err := Create(cwd)
	if err != nil {
		t.Fatalf("建会话失败：%v", err)
	}
	for _, msg := range []*types.Message{
		userMessage("第一句"),
		{Role: types.AssistantRole, ContentBlocks: []types.ContentBlock{types.NewTextBlock("第一答")}},
		{Role: types.ToolResultRole, ToolCallID: "c1", ContentBlocks: []types.ContentBlock{types.NewTextBlock("命令结果")}},
	} {
		if _, err := s.Append(msg); err != nil {
			t.Fatalf("落盘失败：%v", err)
		}
	}

	again, err := ContinueRecent(cwd)
	if err != nil {
		t.Fatalf("续跑失败：%v", err)
	}
	if again.ID() != s.ID() || again.Path() != s.Path() || again.Cwd() != cwd {
		t.Errorf("读回来的会话不是同一个：id=%q path=%q cwd=%q", again.ID(), again.Path(), again.Cwd())
	}
	if got, want := again.Messages(), s.Messages(); !reflect.DeepEqual(got, want) {
		t.Errorf("历史没读回来：\n原 %+v\n后 %+v", want, got)
	}
}

// ContinueRecent 靠 mtime 定谁最近，文件名里的时间戳不参与
func TestContinueRecentPicksLatestByMtime(t *testing.T) {
	setHome(t)
	cwd := t.TempDir()

	older, err := Create(cwd)
	if err != nil {
		t.Fatalf("建会话失败：%v", err)
	}
	if _, err := older.Append(userMessage("早的")); err != nil {
		t.Fatalf("落盘失败：%v", err)
	}

	newer, err := Create(cwd)
	if err != nil {
		t.Fatalf("建会话失败：%v", err)
	}
	if _, err := newer.Append(userMessage("晚的")); err != nil {
		t.Fatalf("落盘失败：%v", err)
	}

	// 两个文件是同一毫秒建的，只有把早的那个 mtime 推回去才分得出先后
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older.Path(), past, past); err != nil {
		t.Fatalf("改 mtime 失败：%v", err)
	}

	got, err := ContinueRecent(cwd)
	if err != nil {
		t.Fatalf("续跑失败：%v", err)
	}
	if got.ID() != newer.ID() {
		t.Errorf("该续最新的那个，实际续了 %q", got.ID())
	}
}

// 一行坏了不该让整个会话打不开：跳过它，别的 entry 照读
func TestOpenSkipsMalformedLines(t *testing.T) {
	setHome(t)
	cwd := t.TempDir()

	path := filepath.Join(t.TempDir(), "broken.jsonl")
	lines := []string{
		`{"type":"session","version":1,"id":"s1","timestamp":"2026-09-24T10:00:00.000Z","cwd":"` + cwd + `"}`,
		entryLine(t, "a1", "", "一"),
		`not json`,
		entryLine(t, "a2", "a1", "二"),
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("写文件失败：%v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("坏行不该让 Open 报错：%v", err)
	}
	if got := len(s.Messages()); got != 2 {
		t.Errorf("该读回两条消息，实际 %d 条", got)
	}
}

// 第一行不是 header，说明这文件不是我们写的，要当成错误而不是空会话
func TestOpenRejectsNonSessionFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-session.jsonl")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("写文件失败：%v", err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("期望报错，实际打开了")
	}
}

// 按 cwd 分目录，所以 List 只看得见这个 cwd 的会话
func TestListOnlyReturnsSessionsOfThatCwd(t *testing.T) {
	home := setHome(t)
	cwd, other := t.TempDir(), t.TempDir()

	if dir, err := dirFor("/Users/reuben/x"); err != nil {
		t.Fatalf("算目录失败：%v", err)
	} else if want := filepath.Join(home, ".go-harness", "sessions", "--Users-reuben-x--"); dir != want {
		t.Fatalf("目录编码不对：\n期望 %s\n实际 %s", want, dir)
	}

	dir, err := dirFor(cwd)
	if err != nil {
		t.Fatalf("算目录失败：%v", err)
	}
	want := filepath.Join(home, ".go-harness", "sessions", "--"+strings.ReplaceAll(strings.TrimPrefix(cwd, "/"), "/", "-")+"--")
	if dir != want {
		t.Fatalf("目录编码不对：\n期望 %s\n实际 %s", want, dir)
	}

	mine, err := Create(cwd)
	if err != nil {
		t.Fatalf("建会话失败：%v", err)
	}
	if _, err := mine.Append(userMessage("我的")); err != nil {
		t.Fatalf("落盘失败：%v", err)
	}
	theirs, err := Create(other)
	if err != nil {
		t.Fatalf("建会话失败：%v", err)
	}
	if _, err := theirs.Append(userMessage("别人的")); err != nil {
		t.Fatalf("落盘失败：%v", err)
	}

	infos, err := List(cwd)
	if err != nil {
		t.Fatalf("列会话失败：%v", err)
	}
	if len(infos) != 1 || infos[0].ID != mine.ID() {
		t.Fatalf("该只列出自己这个 cwd 的会话，实际 %+v", infos)
	}
	if infos[0].Created == "" || infos[0].Modified.IsZero() {
		t.Errorf("列表项要带得上时间，实际 %+v", infos[0])
	}
}

// 没有会话时 ContinueRecent 新建一个，而不是报错
func TestContinueRecentCreatesWhenNothingThere(t *testing.T) {
	setHome(t)

	s, err := ContinueRecent(t.TempDir())
	if err != nil {
		t.Fatalf("续跑失败：%v", err)
	}
	if s.ID() == "" || s.Path() != "" {
		t.Errorf("该拿到一个还没落盘的新会话，实际 id=%q path=%q", s.ID(), s.Path())
	}
}

func entryLine(t *testing.T, id, parentID, text string) string {
	t.Helper()

	entry := Entry{
		Type:      messageType,
		ID:        id,
		Timestamp: "2026-09-24T10:00:01.000Z",
		Message:   userMessage(text),
	}
	if parentID != "" {
		entry.ParentID = &parentID
	}
	line, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("编码失败：%v", err)
	}
	return string(line)
}
