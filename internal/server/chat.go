package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/reuben/go-harness/internal/compact"
	"github.com/reuben/go-harness/internal/core"
	"github.com/reuben/go-harness/internal/prompts"
	"github.com/reuben/go-harness/internal/provider"
	"github.com/reuben/go-harness/internal/session"
	"github.com/reuben/go-harness/internal/tools"
	"github.com/reuben/go-harness/internal/types"
)

// sessionKey 是「一个会话」在服务进程里的身份。用 cwd + id 而不是文件路径：
// 新会话在第一条消息之前还没有文件，而它一样要占住这把锁
type sessionKey struct{ cwd, id string }

// keyFor 是构造 key 的唯一入口：cwd 统一 Clean。各处写法不一致的话
// （/x/y 和 /x/y/）会算出两把锁，409 守卫就形同虚设了
func keyFor(cwd, id string) sessionKey {
	return sessionKey{cwd: filepath.Clean(cwd), id: id}
}

type chatRequest struct {
	Cwd       string `json:"cwd"`
	SessionID string `json:"sessionId"`
	ParentID  string `json:"parentId"` // 分支点；不给就是当前叶子
	Text      string `json:"text"`
}

// handleChat 是唯一的写入口：收一条用户消息、跑一轮、边跑边把事件流出去
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求体不是合法 JSON：%v", err)
		return
	}

	text := strings.TrimSpace(req.Text)
	// 要绝对路径：相对路径（尤其是空串）会跟着服务进程的 cwd 走，
	// 落到一个用户没指定的地方去
	if !filepath.IsAbs(req.Cwd) {
		fail(w, http.StatusBadRequest, "cwd 要是绝对路径")
		return
	}
	req.Cwd = filepath.Clean(req.Cwd)

	switch {
	case text == "":
		fail(w, http.StatusBadRequest, "消息是空的")
		return
	case !session.ValidID(req.SessionID):
		fail(w, http.StatusBadRequest, "会话 id 形状不对")
		return
	case !isDir(req.Cwd):
		fail(w, http.StatusBadRequest, "cwd 不是一个目录")
		return
	}

	sess, err := s.openOrCreate(req.Cwd, req.SessionID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "%v", err)
		return
	}

	// 分支点：不给就是当前叶子（= 最后写进去的那条），全新会话就是根
	parentID := req.ParentID
	if parentID == "" {
		parentID = sess.LeafID()
	}
	if parentID != "" && sess.Entry(parentID) == nil {
		fail(w, http.StatusBadRequest, "parentId %s 不在这个会话里", parentID)
		return
	}

	// 同一个会话同时只允许跑一轮：两条支路一起往一个文件里追加会写乱
	key := keyFor(req.Cwd, req.SessionID)

	// 这一轮的 ctx 挂在请求 ctx 上（关页面照样取消），另外再挂一个可被
	// /api/chat/abort 点掉的取消函数 —— 让「停止」是服务端停，不是客户端把
	// 连接掐了。掐连接的话客户端不知道那一轮什么时候收完尾，重读会读到旧状态
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	if !s.acquire(key, cancel) {
		fail(w, http.StatusConflict, "这个会话正在跑一轮，等它结束")
		return
	}
	defer s.release(key)

	// 到这为止的错误还能当普通 HTTP 错误报出去，下面开始就是事件流了
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	stream := newEventStream(w)

	s.runTurn(ctx, stream, sess, parentID, text)
}

// handleChatAbort 让正在跑的那一轮停下来。这一轮的收尾（落盘 + done）照跑，
// 客户端等流自己结束就行 —— 不用掐连接，也就不会读到落盘落了一半的状态
func (s *Server) handleChatAbort(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cwd       string `json:"cwd"`
		SessionID string `json:"sessionId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求体不是合法 JSON：%v", err)
		return
	}
	if !session.ValidID(req.SessionID) {
		fail(w, http.StatusBadRequest, "会话 id 形状不对")
		return
	}
	if !s.abort(keyFor(req.Cwd, req.SessionID)) {
		fail(w, http.StatusConflict, "这个会话没有在跑")
		return
	}
	writeJSON(w, map[string]string{"id": req.SessionID})
}

// runTurn 跑一轮并落盘。ctx 用请求的：关页面、点停止就是取消这一轮，
// core.Run 在取消路径上会给没跑完的工具调用补配对，所以文件里永远是一整轮。
// 落盘这一段故意不看 ctx —— 取消了也一样要记住已经跑出来的东西
func (s *Server) runTurn(ctx context.Context, stream *eventStream, sess *session.Session, parentID, text string) {
	cwd := sess.Cwd()
	allTools := tools.GetAllTools(cwd)

	// 顶到窗口阈值就先压缩。压出来的 compaction 挂在分支点上，这一轮从它
	// 下面接着长 —— 之后的历史都从这个节点开始重放
	parentID = s.compactIfNeeded(ctx, stream, sess, parentID)

	conversation := &types.Conversation{
		SystemPrompt: prompts.BuildSystemPrompt(cwd, allTools),
		Tools:        allTools,
		Messages:     sess.MessagesAt(parentID),
	}
	prompt := &types.Message{Role: types.UserRole, ContentBlocks: []types.ContentBlock{types.NewTextBlock(text)}}

	emit := func(event types.Event) {
		if wire, ok := toWire(event); ok {
			stream.send(wire)
		}
	}
	newMessages := core.Run(ctx, []*types.Message{prompt}, conversation, s.cfg.Generate, emit)

	// 一条条挂上去，每条落盘之后往前推一格 —— 前端的图靠 entry 事件长出来。
	// 第一条挂在分支点上，后面每条挂在前一条下面
	parent := parentID
	for _, msg := range newMessages {
		id, err := sess.AppendAfter(parent, msg)
		if err != nil {
			stream.send(wireEvent{Type: "error", Error: fmt.Sprintf("会话没存上：%v", err)})
			break
		}
		parent = id
		entry := toEntryDTO(sess.Entry(id))
		stream.send(wireEvent{Type: "entry", Entry: &entry})
	}

	// done 压在最后：前端收到它，才知道图上的节点都齐了
	stream.send(wireEvent{Type: "done"})

	// 标题是后台生成、写 sidecar 的，不占这条流：前端过几秒自己再拉一次侧栏
	s.maybeTitle(sess)
}

// compactIfNeeded 在上下文顶到阈值时先压一次，返回这一轮该挂在哪个 entry 下面
// （压了就返回 compaction entry，没压原样返回）。
//
// 压缩失败只报一声就接着跑：它是省钱的优化，不是这一轮能不能跑的前提
func (s *Server) compactIfNeeded(ctx context.Context, stream *eventStream, sess *session.Session, parentID string) string {
	messages := sess.MessagesAt(parentID)
	settings := compact.DefaultSettings()
	if !compact.ShouldCompact(compact.ContextTokens(messages), provider.ContextWindow(s.cfg.Model), settings) {
		return parentID
	}

	// 有上一个摘要时 messages[0] 就是它：这一刀只摘要它后面的新内容，
	// 新旧两版交给模型合并
	start := 0
	prevSummary := sess.LastSummaryAt(parentID)
	if prevSummary != "" {
		start = 1
	}

	entries := sess.ContextEntries(parentID)
	cut := compact.FindCut(messages, start, settings.KeepRecentTokens)
	if cut < 0 || cut >= len(entries) {
		return parentID
	}

	stream.send(wireEvent{Type: "compacting"})
	summary, err := compact.Summarize(ctx, s.cfg.Generate, messages[start:cut], prevSummary, settings)
	if err != nil {
		// 点了停止：这一轮本来就跑不成了，取消不该报成压缩失败
		if ctx.Err() != nil {
			return parentID
		}
		stream.send(wireEvent{Type: "error", Error: fmt.Sprintf("上下文没压成，这一轮带着原样的历史跑：%v", err)})
		return parentID
	}

	id, err := sess.AppendCompaction(parentID, summary, entries[cut].ID, compact.ContextTokens(messages))
	if err != nil {
		stream.send(wireEvent{Type: "error", Error: fmt.Sprintf("压缩没存上：%v", err)})
		return parentID
	}
	entry := toEntryDTO(sess.Entry(id))
	stream.send(wireEvent{Type: "entry", Entry: &entry})
	return id
}

// openOrCreate 按 id 打开会话；文件不存在就是这个 id 的草稿还没落过盘。
// 「新会话」不建空文件是有意的（见 internal/session），所以这里不是错误路径
func (s *Server) openOrCreate(cwd, id string) (*session.Session, error) {
	path, err := session.Find(cwd, id)
	if errors.Is(err, fs.ErrNotExist) {
		return session.CreateWithID(cwd, id)
	}
	if err != nil {
		return nil, err
	}
	return session.Open(path)
}

func (s *Server) acquire(key sessionKey, cancel context.CancelFunc) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[key] {
		return false
	}
	s.running[key] = true
	s.cancels[key] = cancel
	return true
}

func (s *Server) release(key sessionKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, key)
	delete(s.cancels, key)
}

// abort 让这个会话正在跑的那一轮停下。返回 false = 本来就没在跑
func (s *Server) abort(key sessionKey) bool {
	s.mu.Lock()
	cancel := s.cancels[key]
	s.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func (s *Server) isRunning(key sessionKey) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running[key]
}
