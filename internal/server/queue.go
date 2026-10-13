package server

import (
	"net/http"
	"strconv"

	"github.com/reuben/go-harness/internal/types"
)

// 运行中用户还能接着发话，这里放的就是那些还没投递的消息。两条队列，差别只在
// 投递时机：steer 在工具批次边界投，模型做完当前这步就能看到；followUp 在这一轮
// 本来要停时才投，排到这次任务结束之后。一次只投一条（照 pi 的默认）—— 三条插话
// 一口气灌进去，模型往往只答最后一条
const (
	queueSteer    = "steer"
	queueFollowUp = "followUp"
)

type queuedMessage struct {
	// ID 是给前端的认领凭据：投递那一刻按它报一声，前端才知道哪条 chip 该变成气泡
	ID  string
	Msg *types.Message
	// Mode 只在入队排队列时有用，投出去之后就没意义了
	Mode string
}

// messageQueue 的两条队列都按入队顺序走。调用方一律拿 s.mu 护着
type messageQueue struct {
	steer    []*queuedMessage
	followUp []*queuedMessage
}

func (q *messageQueue) push(item *queuedMessage) {
	if item.Mode == queueFollowUp {
		q.followUp = append(q.followUp, item)
		return
	}
	q.steer = append(q.steer, item)
}

// pop 从指定队列取一条。时机由调用方定，这里只管顺序
func (q *messageQueue) pop(mode string) *queuedMessage {
	list := &q.steer
	if mode == queueFollowUp {
		list = &q.followUp
	}
	if len(*list) == 0 {
		return nil
	}
	item := (*list)[0]
	*list = (*list)[1:]
	return item
}

// popAny 取一条，插话优先 —— 它本来就是「越早投越好」的那一类
func (q *messageQueue) popAny() *queuedMessage {
	if item := q.pop(queueSteer); item != nil {
		return item
	}
	return q.pop(queueFollowUp)
}

// remove 把还没投递的某条摘出来。已经投出去的返回 false
func (q *messageQueue) remove(id string) bool {
	for _, list := range []*[]*queuedMessage{&q.steer, &q.followUp} {
		for i, item := range *list {
			if item.ID == id {
				*list = append((*list)[:i], (*list)[i+1:]...)
				return true
			}
		}
	}
	return false
}

// pop 从正在跑的那一轮上取一条消息
func (s *Server) pop(key sessionKey, mode string) *queuedMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.running[key]
	if current == nil {
		return nil
	}
	return current.queue.pop(mode)
}

// drainer 造 core 要的投递回调：取一条、在流上报一声、把消息交给 loop 注入。
// 报的那一声带着 ID —— 前端靠它把「排队中」的 chip 换成对话里的气泡。
// loop 停下来的线上还剩着的东西由收尾循环兜底（见 handleChat）
func (s *Server) drainer(key sessionKey, stream *eventStream, mode string) func() *types.Message {
	return func() *types.Message {
		item := s.pop(key, mode)
		if item == nil {
			return nil
		}
		stream.send(queuedDelivered(item))
		return item.Msg
	}
}

func queuedDelivered(item *queuedMessage) wireEvent {
	return wireEvent{Type: "queued_delivered", QueueID: item.ID, Message: toMessageDTO(item.Msg)}
}

// nextTurn 是收尾循环的关口：这一轮跑完了，队列里还有货就把锁留着、交出下一条，
// 没有就放锁并返回 nil（这条请求到此为止）。
//
// 查队列和放锁必须在同一把锁里完成：中间留条缝的话，进来的排队请求会看到「还在跑」
// 把消息排上队，然后这一轮放锁走人 —— 那条消息就再也没人投了
func (s *Server) nextTurn(key sessionKey, current *run) *queuedMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[key] != current {
		return nil // 锁已经不是这一轮的了（中止收尾过），别接着跑
	}
	if item := current.queue.popAny(); item != nil {
		return item
	}
	delete(s.running, key)
	return nil
}

// dropQueue 中止时把还排着的消息扔掉：用户按了停止，排队的东西该还回他自己手上
// （前端把本地那份还给输入框），不该接着跑
func (s *Server) dropQueue(key sessionKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.running[key]; current != nil {
		current.queue = messageQueue{}
	}
}

type queueRequest struct {
	Cwd       string   `json:"cwd"`
	SessionID string   `json:"sessionId"`
	Text      string   `json:"text"`
	Images    []string `json:"images"`
	Mode      string   `json:"mode"` // steer（默认）/ followUp
}

// handleChatQueue 收一条「运行中发的话」。它不开流：消息只是排上队，什么时候让模型
// 看到由正在跑的那一轮决定，投递时在那条流上发 queued_delivered
func (s *Server) handleChatQueue(w http.ResponseWriter, r *http.Request) {
	var req queueRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Mode == "" {
		req.Mode = queueSteer
	}
	if req.Mode != queueSteer && req.Mode != queueFollowUp {
		fail(w, http.StatusBadRequest, "mode 只能是 steer 或 followUp")
		return
	}

	// 校验跟 /api/chat 完全共用：排队和直发的是同一种消息，规则不能有第二种
	msg, err := buildUserMessage(req.Text, req.Images)
	if err != nil {
		fail(w, http.StatusBadRequest, "%v", err)
		return
	}
	cwd, err := checkTarget(req.Cwd, req.SessionID)
	if err != nil {
		fail(w, http.StatusBadRequest, "%v", err)
		return
	}

	key := keyFor(cwd, req.SessionID)
	item := &queuedMessage{ID: strconv.FormatUint(s.seq.Add(1), 10), Msg: msg, Mode: req.Mode}

	// 「还在不在跑」和入队一次判掉：分成两步的话，中间这一轮可能刚好收尾，
	// 消息排进一条已经走完的队列，再没人投
	s.mu.Lock()
	current := s.running[key]
	if current == nil {
		s.mu.Unlock()
		// 前端收到这个会退回普通发送（那一轮刚好跑完了）
		fail(w, http.StatusConflict, "这个会话没有在跑，直接发就行")
		return
	}
	current.queue.push(item)
	s.mu.Unlock()

	writeJSON(w, map[string]string{"id": item.ID})
}

// handleChatDequeue 把还没投递的某条摘回去。不在跑、或者已经被投出去了，
// removed 就是 false —— 前端照旧把 chip 摘掉（它已经变成对话里的气泡了）
func (s *Server) handleChatDequeue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cwd       string `json:"cwd"`
		SessionID string `json:"sessionId"`
		ID        string `json:"id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}

	key := keyFor(req.Cwd, req.SessionID)
	removed := false
	s.mu.Lock()
	if current := s.running[key]; current != nil {
		removed = current.queue.remove(req.ID)
	}
	s.mu.Unlock()

	writeJSON(w, map[string]bool{"removed": removed})
}
