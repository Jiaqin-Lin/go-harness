package server

import (
	"context"
	"encoding/base64"
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
	"github.com/reuben/go-harness/internal/skills"
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

const (
	// maxImages 是一条消息最多带几张图。pi 那张按模型声明的表（maxPerMessage）
	// 只管声明、不强制，这里的数就是硬上限
	maxImages = 8
	// maxImageRaw 是单张图的原始字节上限。前端按同一个数先拦一道（界面里的提示
	// 比 413 友好），这儿兜底
	maxImageRaw = 4 << 20
	// maxChatBody 是整个请求体的上限。8 张 4MB 的图 base64 之后约 43MB，留点余量
	maxChatBody = 48 << 20
)

type chatRequest struct {
	Cwd       string `json:"cwd"`
	SessionID string `json:"sessionId"`
	ParentID  string `json:"parentId"` // 分支点；不给就是当前叶子
	Text      string `json:"text"`
	Skill     string `json:"skill"` // 这一轮强制用的技能名；空 = 不强制

	Images []string `json:"images"` // base64；格式由服务端嗅探，见 buildUserMessage
}

// decodeBody 读 JSON 请求体，读不成自己把错误响应发掉并返回 false。
// 带图之后请求体一下就大了，两种失败得分得开：超限给 413，坏 JSON 给 400
func decodeBody(w http.ResponseWriter, r *http.Request, into any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxChatBody)
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			fail(w, http.StatusRequestEntityTooLarge, "请求体太大（上限 %dMB）：图片少放几张，或者换小一点的", maxChatBody>>20)
			return false
		}
		fail(w, http.StatusBadRequest, "请求体不是合法 JSON：%v", err)
		return false
	}
	return true
}

// checkTarget 校验会话定位那几项，返回洗干净的 cwd。/api/chat 和 /api/chat/queue
// 共用 —— 排队的和直发的是同一种消息，规则不能有第二种
func checkTarget(cwd, sessionID string) (string, error) {
	// 要绝对路径：相对路径（尤其是空串）会跟着服务进程的 cwd 走，
	// 落到一个用户没指定的地方去
	if !filepath.IsAbs(cwd) {
		return "", errors.New("cwd 要是绝对路径")
	}
	cwd = filepath.Clean(cwd)
	switch {
	case !session.ValidID(sessionID):
		return "", errors.New("会话 id 形状不对")
	case !isDir(cwd):
		return "", errors.New("cwd 不是一个目录")
	}
	return cwd, nil
}

// buildUserMessage 把正文和图片拼成一条用户消息。图片只给 base64，格式由服务端嗅探
// （前端说的 mime 不算数）；超限的交给 processImage 缩放重编码，缩不动才整条退回 ——
// 收下一张读不出来的图，比在这儿说清楚难查得多
func buildUserMessage(text string, images []string) (*types.Message, error) {
	if len(images) > maxImages {
		return nil, fmt.Errorf("一次最多 %d 张图，这儿有 %d 张", maxImages, len(images))
	}

	blocks := make([]types.ContentBlock, 0, len(images)+1)
	if text = strings.TrimSpace(text); text != "" {
		blocks = append(blocks, types.NewTextBlock(text))
	}
	for i, encoded := range images {
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("第 %d 张图不是合法的 base64", i+1)
		}
		if len(raw) > maxImageRaw {
			return nil, fmt.Errorf("第 %d 张图 %.1fMB，超过 %dMB 的上限", i+1, float64(len(raw))/(1<<20), maxImageRaw>>20)
		}
		payload, err := tools.ImageFromBytes(raw)
		if err != nil {
			return nil, fmt.Errorf("第 %d 张图读不出来：%v", i+1, err)
		}
		blocks = append(blocks, payload.Block())
	}
	if len(blocks) == 0 {
		return nil, errors.New("消息是空的")
	}
	return &types.Message{Role: types.UserRole, ContentBlocks: blocks}, nil
}

// handleChat 是唯一的「开一轮」入口：收一条用户消息、跑一轮、边跑边把事件流出去。
// 跑完一轮不算完 —— 这期间用户排进队列的消息接着跑（见收尾循环）
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if !decodeBody(w, r, &req) {
		return
	}

	prompt, err := buildUserMessage(req.Text, req.Images)
	if err != nil {
		fail(w, http.StatusBadRequest, "%v", err)
		return
	}
	cwd, err := checkTarget(req.Cwd, req.SessionID)
	if err != nil {
		fail(w, http.StatusBadRequest, "%v", err)
		return
	}
	req.Cwd = cwd

	// 强制技能得是这一轮真给模型看得到的那些之一：被停用、被同名盖住、读不出来的
	// 都不行——强制一个模型根本看不到的技能等于没强制。这一步还在开流之前，
	// 所以能当普通 HTTP 错误报出去，一个 token 都不花
	if req.Skill != "" {
		if _, ok := skills.ByName(req.Cwd, req.Skill); !ok {
			fail(w, http.StatusBadRequest, "没有叫「%s」的技能，或者它被停用了", req.Skill)
			return
		}
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

	current := s.acquire(key, cancel)
	if current == nil {
		fail(w, http.StatusConflict, "这个会话正在跑一轮，等它结束")
		return
	}
	// 兜底：收尾循环正常走完时锁已经放了（见 nextTurn），这儿只接住 panic 那条路
	defer s.release(key, current)

	// 到这为止的错误还能当普通 HTTP 错误报出去，下面开始就是事件流了
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	stream := newEventStream(w)

	// 收尾循环：一轮跑完不等于这条请求就完了。用户在这一轮跑着的时候排进来的
	// 消息得接着跑掉，不然它们卡在一条已经收了的线上，再没人管
	skillName := req.Skill
	for {
		parentID = s.runTurn(ctx, stream, sess, key, parentID, prompt, skillName)

		if ctx.Err() != nil {
			s.dropQueue(key)
			break
		}
		next := s.nextTurn(key, current)
		if next == nil {
			break
		}
		stream.send(queuedDelivered(next))
		prompt, skillName = next.Msg, "" // 技能只对用户点的那一条生效
	}

	// done 压在最后：前端收到它，才知道这一条请求（连同排队的几轮）真跑完了
	stream.send(wireEvent{Type: "done"})

	// 标题是后台生成、写 sidecar 的，不占这条流：前端过几秒自己再拉一次侧栏
	s.maybeTitle(sess)
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

// runTurn 跑一轮并落盘，返回末尾那条 entry 的 id（收尾循环里下一轮挂在它下面）。
// ctx 用请求的：关页面、点停止就是取消这一轮，core.Run 在取消路径上会给没跑完的
// 工具调用补配对，所以文件里永远是一整轮。落盘这一段故意不看 ctx ——
// 取消了也一样要记住已经跑出来的东西
func (s *Server) runTurn(ctx context.Context, stream *eventStream, sess *session.Session, key sessionKey, parentID string, prompt *types.Message, skillName string) string {
	cwd := sess.Cwd()
	allTools := tools.GetAllTools(cwd)

	// 顶到窗口阈值就先压缩。压出来的 compaction 挂在分支点上，这一轮从它
	// 下面接着长 —— 之后的历史都从这个节点开始重放
	parentID = s.compactIfNeeded(ctx, stream, sess, parentID)

	// 强制技能只进这一轮的 system prompt，重放历史的时候它不该再贴一遍，
	// 这正是「只对下一条消息生效」。handleChat 校验过了，到这儿找不到
	// 只可能是刚被删/停用——流都开了，报错没意义，当没选就是
	var forced *skills.Skill
	if skillName != "" {
		if sk, ok := skills.ByName(cwd, skillName); ok {
			forced = &sk
		}
	}

	conversation := &types.Conversation{
		SystemPrompt: prompts.BuildSystemPrompt(prompts.Input{
			Cwd:      cwd,
			Tools:    allTools,
			Skills:   skills.Effective(cwd),
			Forced:   forced,
			AgentsMD: prompts.ReadAgentsMD(cwd),
		}),
		Tools:    allTools,
		Messages: sess.MessagesAt(parentID),
	}
	emit := func(event types.Event) {
		if wire, ok := toWire(event); ok {
			stream.send(wire)
		}
	}
	// 跑着的时候用户还能塞话进来：core 在两个边界来取（工具批次之后、这一轮本来
	// 要停时），取到就顺手在流上报一声 —— 前端的 chip 就是靠这一声变成气泡的
	queues := core.Queues{
		Steer:    s.drainer(key, stream, queueSteer),
		FollowUp: s.drainer(key, stream, queueFollowUp),
	}
	newMessages := core.Run(ctx, []*types.Message{prompt}, conversation, s.cfg.Generate, emit, queues)

	// 一条条挂上去，每条落盘之后往前推一格 —— 前端的图靠 entry 事件长出来。
	// 第一条挂在分支点上，后面每条挂在前一条下面
	parent := parentID
	for i, msg := range newMessages {
		// 技能名只钉在这轮的用户消息上（就是第一条），回答和工具结果不带 ——
		// 界面只该在「你问的那句」旁边标出这一轮用了什么技能
		entrySkill := ""
		if i == 0 {
			entrySkill = skillName
		}
		id, err := sess.AppendAfterSkill(parent, msg, entrySkill)
		if err != nil {
			stream.send(wireEvent{Type: "error", Error: fmt.Sprintf("会话没存上：%v", err)})
			break
		}
		parent = id
		entry := toEntryDTO(sess.Entry(id))
		stream.send(wireEvent{Type: "entry", Entry: &entry})
	}
	return parent
}

// compactIfNeeded 在上下文顶到阈值时先压一次，返回这一轮该挂在哪个 entry 下面
// （压了就返回 compaction entry，没压原样返回）。
//
// 压缩失败只报一声就接着跑：它是省钱的优化，不是这一轮能不能跑的前提
func (s *Server) compactIfNeeded(ctx context.Context, stream *eventStream, sess *session.Session, parentID string) string {
	messages := sess.MessagesAt(parentID)
	settings := s.compactSettings() // 每轮现读设置：改完阈值下一条消息就生效
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

// acquire 占住这个会话。占不到 = 已经有一轮在跑
func (s *Server) acquire(key sessionKey, cancel context.CancelFunc) *run {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[key] != nil {
		return nil
	}
	current := &run{cancel: cancel}
	s.running[key] = current
	return current
}

// release 放掉这一轮的锁，但只在锁还归它的时候放：nextTurn 可能已经放过了
// （队列空了），之后新请求又拿了同一把锁 —— 不比对就 delete，会把别人的锁删掉，
// 同一个会话就有两轮在跑，一起往一个文件里追加
func (s *Server) release(key sessionKey, current *run) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[key] == current {
		delete(s.running, key)
	}
}

// abort 让这个会话正在跑的那一轮停下。返回 false = 本来就没在跑
func (s *Server) abort(key sessionKey) bool {
	s.mu.Lock()
	current := s.running[key]
	s.mu.Unlock()
	if current == nil {
		return false
	}
	current.cancel()
	return true
}

func (s *Server) isRunning(key sessionKey) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running[key] != nil
}
