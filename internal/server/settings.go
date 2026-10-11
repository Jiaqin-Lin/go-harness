package server

import (
	"encoding/json"
	"net/http"

	"github.com/reuben/go-harness/internal/compact"
	"github.com/reuben/go-harness/internal/provider"
	"github.com/reuben/go-harness/internal/settings"
)

// maxCompactAt 压缩触发点的上限。窗口就 1M，填个比窗口还大的值等于关掉压缩，
// 但那是允许的（用户可能就想让它别压），只是别离谱到把数存进去
const maxCompactAt = 10_000_000

// compactSettings 是这一轮该用的压缩参数：默认值叠上用户在设置里改过的。
//
// 每轮现读，不是启动时读一次 —— 阈值改完下一条消息就生效，不用重启，
// 也就不会再有「我改了怎么没生效」这种要靠猜的事
func (s *Server) compactSettings() compact.Settings {
	out := compact.DefaultSettings()
	conf, err := settings.Read()
	if err != nil {
		return out // 配置坏了按默认压：不压比压错更贵
	}
	if conf.Compact.At > 0 {
		out.CompactAt = conf.Compact.At
	}
	return out
}

// writeMeta 是前端的「模型知识」：模型名、窗口多大、以及压缩线在哪。
// 都是服务端的事，前端不写死 —— 换个模型、改个阈值都不用动前端
func (s *Server) writeMeta(w http.ResponseWriter) {
	window := provider.ContextWindow(s.cfg.Model)
	conf := s.compactSettings()
	writeJSON(w, map[string]any{
		"model":         s.cfg.Model,
		"contextWindow": window,
		// compactAt 是设置里的那个数（0 = 没设过，用默认），界面上填的就是它
		"compactAt": conf.CompactAt,
		// compactLimit 是真正会触发的数（窗口 - reserve 和 compactAt 取小）——
		// 显示它才不会和判断用的数各说各话
		"compactLimit": compact.Limit(window, conf),
	})
}

// handleSettings 改设置。目前只有压缩触发点一个旋钮
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CompactAt int `json:"compactAt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求体不是合法 JSON：%v", err)
		return
	}
	if req.CompactAt < 0 || req.CompactAt > maxCompactAt {
		fail(w, http.StatusBadRequest, "压缩触发点得在 0 到 %d 之间（0 = 用默认）", maxCompactAt)
		return
	}

	// 0 是「用默认」：prune 会把 at:0 整个剪掉，读回来就是没设过
	if err := settings.Update(func(conf *settings.Settings) { conf.Compact.At = req.CompactAt }); err != nil {
		fail(w, http.StatusInternalServerError, "%v", err)
		return
	}
	s.writeMeta(w)
}
