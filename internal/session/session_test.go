package session

import (
	"encoding/json"
	"errors"
	"io/fs"
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

func assistantMessage(text string) *types.Message {
	return &types.Message{
		Role:          types.AssistantRole,
		ContentBlocks: []types.ContentBlock{types.NewTextBlock(text)},
	}
}

func textsOf(messages []*types.Message) []string {
	var texts []string
	for _, msg := range messages {
		texts = append(texts, msg.Text())
	}
	return texts
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

// 分叉：往同一个父节点上再挂一条，两条支路各回各的历史。
// 一个真实形状是「第一问 -> 第一答 -> 从第一问换个方向问」
func TestAppendAfterBranches(t *testing.T) {
	setHome(t)
	cwd := t.TempDir()

	s, err := Create(cwd)
	if err != nil {
		t.Fatalf("建会话失败：%v", err)
	}
	root, err := s.Append(userMessage("第一问"))
	if err != nil {
		t.Fatalf("落盘失败：%v", err)
	}
	answer, err := s.Append(assistantMessage("第一答"))
	if err != nil {
		t.Fatalf("落盘失败：%v", err)
	}
	// 决策点在第一问：不改历史，直接从它再分一条
	other, err := s.AppendAfter(root, userMessage("换个方向问"))
	if err != nil {
		t.Fatalf("分叉失败：%v", err)
	}

	if got := s.Entry(other).ParentID; got == nil || *got != root {
		t.Errorf("分叉出来的 entry 该挂在 %s 下面，实际 %v", root, got)
	}
	if s.Entry(answer).ParentID == nil || *s.Entry(answer).ParentID != root {
		t.Errorf("原来的支路不该被改动")
	}
	if got := textsOf(s.MessagesAt(other)); !reflect.DeepEqual(got, []string{"第一问", "换个方向问"}) {
		t.Errorf("新支路的历史该只有根和新消息，实际 %v", got)
	}
	if got := textsOf(s.MessagesAt(answer)); !reflect.DeepEqual(got, []string{"第一问", "第一答"}) {
		t.Errorf("老支路的历史该一点没变，实际 %v", got)
	}
	if got := len(s.Entries()); got != 3 {
		t.Errorf("树上该有 3 个节点，实际 %d 个", got)
	}
}

// 分叉必须活过重新打开：树上挂错位置的话读回来就串了
func TestBranchSurvivesReopen(t *testing.T) {
	setHome(t)
	cwd := t.TempDir()

	s, err := Create(cwd)
	if err != nil {
		t.Fatalf("建会话失败：%v", err)
	}
	root, _ := s.Append(userMessage("根"))
	a, _ := s.AppendAfter(root, userMessage("支路一"))
	b, _ := s.AppendAfter(root, userMessage("支路二"))

	again, err := Open(s.Path())
	if err != nil {
		t.Fatalf("打开失败：%v", err)
	}
	if got := textsOf(again.MessagesAt(a)); !reflect.DeepEqual(got, []string{"根", "支路一"}) {
		t.Errorf("支路一读回来不对：%v", got)
	}
	if got := textsOf(again.MessagesAt(b)); !reflect.DeepEqual(got, []string{"根", "支路二"}) {
		t.Errorf("支路二读回来不对：%v", got)
	}
	if again.LeafID() != b {
		t.Errorf("叶子该是最后写进去的那个，实际 %q", again.LeafID())
	}
}

// 挂到不存在的父节点上要当场报错，不能落盘 —— 落下去会被回溯静默截断，
// 用户看到的是「分叉不见了」
func TestAppendAfterRejectsUnknownParent(t *testing.T) {
	setHome(t)
	cwd := t.TempDir()

	s, _ := Create(cwd)
	s.Append(userMessage("第一问"))

	if _, err := s.AppendAfter("不存在", userMessage("挂不上去")); err == nil {
		t.Fatal("父节点不存在该报错")
	}
	if got := len(s.Entries()); got != 1 {
		t.Errorf("报错之后不该多出节点，实际 %d 个", got)
	}

	// 已经有内容了还要挂成根，会多出第二个根
	if _, err := s.AppendAfter("", userMessage("又一条根")); err == nil {
		t.Fatal("已经有内容时不该允许再挂根")
	}
}

func TestCreateWithIDRejectsBadID(t *testing.T) {
	setHome(t)
	for _, id := range []string{"", "abc", "ABC12345", "zzzzzzzz", strings.Repeat("a", 33)} {
		if _, err := CreateWithID(t.TempDir(), id); err == nil {
			t.Errorf("id %q 该被拒", id)
		}
	}
}

func TestFindDeleteAndTitle(t *testing.T) {
	setHome(t)
	cwd := t.TempDir()

	s, err := CreateWithID(cwd, "aaaabbbbccccdddd")
	if err != nil {
		t.Fatalf("建会话失败：%v", err)
	}
	if _, err := s.Append(userMessage("帮我看看这个 bug")); err != nil {
		t.Fatalf("落盘失败：%v", err)
	}

	path, err := Find(cwd, s.ID())
	if err != nil || path != s.Path() {
		t.Fatalf("按 id 该找回同一个文件：path=%q err=%v", path, err)
	}
	if _, err := Find(cwd, "ffffffffffffffff"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("找不到该报 fs.ErrNotExist，实际 %v", err)
	}
	if _, err := Find(cwd, "../别的会话"); err == nil {
		t.Error("形状不对的 id 该被拒")
	}

	if got := Title(s.Path()); got != "" {
		t.Errorf("还没生成过标题该是空串，实际 %q", got)
	}
	if err := WriteTitle(s.Path(), "排查登录问题"); err != nil {
		t.Fatalf("写标题失败：%v", err)
	}
	if got := Title(s.Path()); got != "排查登录问题" {
		t.Errorf("标题没读回来：%q", got)
	}
	infos, err := List(cwd)
	if err != nil || len(infos) != 1 || infos[0].Title != "排查登录问题" {
		t.Errorf("列表该带上标题、且不把 .title 当成会话：%+v err=%v", infos, err)
	}

	if err := Delete(s.Path()); err != nil {
		t.Fatalf("删会话失败：%v", err)
	}
	for _, gone := range []string{s.Path(), s.Path() + ".title"} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s 该被删掉：%v", gone, err)
		}
	}
}

// cwd 里带 glob 元字符（macOS 上完全合法的目录名）时也要找得到会话文件。
// 之前 Find 用 filepath.Glob 拼路径，目录名里的 [ ] 被当成模式解析，
// 明明存在的文件匹配不上 —— 上层就把它当新草稿重建，同一个 id 落出两个文件
func TestFindWithGlobMetacharsInCwd(t *testing.T) {
	setHome(t)
	cwd := filepath.Join(t.TempDir(), "proj[1]")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}

	s, err := CreateWithID(cwd, "aaaabbbbccccdddd")
	if err != nil {
		t.Fatalf("建会话失败：%v", err)
	}
	if _, err := s.Append(userMessage("第一句")); err != nil {
		t.Fatalf("落盘失败：%v", err)
	}

	path, err := Find(cwd, s.ID())
	if err != nil {
		t.Fatalf("带 [ ] 的 cwd 里找不到会话：%v", err)
	}
	if path != s.Path() {
		t.Errorf("找到的不是同一个文件：%q != %q", path, s.Path())
	}
}

// 同一个目录带不带尾斜杠是同一个会话目录；不然侧栏里只看得到一半会话
func TestDirForCleansTrailingSlash(t *testing.T) {
	setHome(t)
	cwd := t.TempDir()
	plain, err := dirFor(cwd)
	if err != nil {
		t.Fatalf("算目录失败：%v", err)
	}
	slash, err := dirFor(cwd + "/")
	if err != nil {
		t.Fatalf("算目录失败：%v", err)
	}
	if plain != slash {
		t.Errorf("尾斜杠该被忽略：\n%q\n%q", plain, slash)
	}
}

// `/x/a-b` 和 `/x/a/b` 编码出来是同一个会话目录。侧栏和查找都得按 header 里的
// cwd 认人 —— 不然 A 项目会列出、甚至打开 B 项目的会话，工具还会在 B 目录里跑
func TestSessionsDoNotLeakAcrossCollidingCwds(t *testing.T) {
	setHome(t)
	root := t.TempDir()
	dashed := filepath.Join(root, "a-b")
	nested := filepath.Join(root, "a", "b")
	for _, dir := range []string{dashed, nested} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("建目录失败：%v", err)
		}
	}

	// 前提本身要断言：这两个 cwd 就是会撞成同一个目录名，不然后面测了个寂寞
	dirA, _ := dirFor(dashed)
	dirB, _ := dirFor(nested)
	if dirA != dirB {
		t.Fatalf("这两个 cwd 本该编码成同一个目录：\n%s\n%s", dirA, dirB)
	}

	inDashed, err := CreateWithID(dashed, "aaaabbbbccccdddd")
	if err != nil {
		t.Fatalf("建会话失败：%v", err)
	}
	if _, err := inDashed.Append(userMessage("这是 a-b 里的")); err != nil {
		t.Fatalf("落盘失败：%v", err)
	}
	inNested, err := CreateWithID(nested, "eeeeffff00001111")
	if err != nil {
		t.Fatalf("建会话失败：%v", err)
	}
	if _, err := inNested.Append(userMessage("这是 a/b 里的")); err != nil {
		t.Fatalf("落盘失败：%v", err)
	}

	infos, err := List(nested)
	if err != nil {
		t.Fatalf("列会话失败：%v", err)
	}
	if len(infos) != 1 || infos[0].ID != inNested.ID() {
		t.Errorf("nested 的侧栏里不该出现 dashed 的会话：%+v", infos)
	}

	if _, err := Find(nested, inDashed.ID()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("从 nested 按 id 不该找得到 dashed 的会话，实际 err=%v", err)
	}
	if path, err := Find(dashed, inDashed.ID()); err != nil || path != inDashed.Path() {
		t.Errorf("dashed 自己还是该找得到：path=%q err=%v", path, err)
	}
}

func TestFirstUserText(t *testing.T) {
	setHome(t)
	cwd := t.TempDir()

	s, _ := Create(cwd)
	if _, err := s.Append(assistantMessage("抢跑的助手消息")); err != nil {
		t.Fatalf("落盘失败：%v", err)
	}
	if got := FirstUserText(s.Path()); got != "" {
		t.Errorf("还没有用户消息时该是空串，实际 %q", got)
	}
	if _, err := s.Append(userMessage("真正的问题")); err != nil {
		t.Fatalf("落盘失败：%v", err)
	}
	if got := FirstUserText(s.Path()); got != "真正的问题" {
		t.Errorf("该跳过助手消息拿到第一条用户消息，实际 %q", got)
	}
	if got := FirstUserText(""); got != "" {
		t.Errorf("空路径该是空串，实际 %q", got)
	}
}

func TestProjectsRoundTrip(t *testing.T) {
	home := setHome(t)
	dir := filepath.Join(home, "work", "proj") // 不用真存在：清单只记路径

	if err := AddProject(dir); err != nil {
		t.Fatalf("加项目失败：%v", err)
	}
	if err := AddProject(dir); err != nil {
		t.Fatalf("重复加不该报错：%v", err)
	}
	projects, err := ListProjects()
	if err != nil || len(projects) != 1 || projects[0].Path != dir || projects[0].AddedAt == "" {
		t.Fatalf("加两次该只有一条：%+v err=%v", projects, err)
	}

	if err := RemoveProject(dir); err != nil {
		t.Fatalf("删项目失败：%v", err)
	}
	if projects, err := ListProjects(); err != nil || len(projects) != 0 {
		t.Errorf("删完该是空的：%+v err=%v", projects, err)
	}
	// 全删光之后文件里是 [] 而不是 null，不然下次读出来是 nil 也算能跑，
	// 但文件内容就不自证了
	buf, err := os.ReadFile(filepath.Join(home, ".go-harness", "projects.json"))
	if err != nil || strings.TrimSpace(string(buf)) != "[]" {
		t.Errorf("删光之后该写 []，实际 %q err=%v", buf, err)
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
