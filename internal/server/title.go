package server

import (
	"context"
	"time"

	"github.com/reuben/go-harness/internal/session"
	"github.com/reuben/go-harness/internal/title"
)

// titleTimeout 是标题生成自己那口钟。独立于任何请求 —— 请求早就结束了
const titleTimeout = 45 * time.Second

// maybeTitle 在一轮跑完之后判断要不要起一个后台标题生成。
// 只有「这个会话还没有标题」时才起，而且同一时刻只允许一个在跑：
// 前后脚结束的两个 run 不该各花一次钱
func (s *Server) maybeTitle(sess *session.Session) {
	path := sess.Path()
	if path == "" || session.Title(path) != "" {
		return
	}

	s.mu.Lock()
	if s.titles[path] {
		s.mu.Unlock()
		return
	}
	s.titles[path] = true
	s.mu.Unlock()

	// 在这里就把消息快照下来：这个 Session 对象是这个请求私有的，
	// 出了 runTurn 就没人再碰它了，goroutine 只读快照
	messages := sess.Messages()
	generate := s.cfg.Generate

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.titles, path)
			s.mu.Unlock()
		}()

		// 注意不能用请求的 ctx：请求早在 SSE 关掉时就结束了，
		// 用它的话标题还没生成就被取消
		ctx, cancel := context.WithTimeout(context.Background(), titleTimeout)
		defer cancel()

		if title := title.Generate(ctx, generate, messages); title != "" {
			// 写不进去就算了：标题是锦上添花，不值得为它报错
			_ = session.WriteTitle(path, title)
		}
	}()
}
