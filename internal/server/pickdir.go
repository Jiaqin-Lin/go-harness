package server

import (
	"errors"
	"net/http"
)

// errNoPicker 这台机器上没有原生目录选择器（非 macOS）。前端拿它当「退回
// 页面上那个目录浏览弹窗」的信号，不是给用户看的错
var errNoPicker = errors.New("这台机器上没有原生目录选择器")

// errPickerCanceled 用户在对话框里点了取消：什么都没发生，更不是失败
var errPickerCanceled = errors.New("目录选择被取消")

// pickDirectory 弹系统的目录选择器，返回选中的绝对路径。平台相关的那一步做成
// 了变量：测试里换成假实现，就不会真弹一个对话框到用户屏幕上
var pickDirectory = pickDirectoryNative

// handlePickDirectory 请服务端弹一个系统的目录选择器，把选中的目录交给前端去
// 加项目。
//
// 为什么在服务端弹：浏览器拿不到本机绝对路径（file input 里的 path 只有
// Electron 那种环境才有），而项目的 cwd 必须是绝对的。服务端就跑在这台机器上、
// 跟用户在同一个会话里，弹出来的就是真的系统对话框
func (s *Server) handlePickDirectory(w http.ResponseWriter, r *http.Request) {
	// 连着点两下会叠出两个对话框，谁也说不清哪个算数
	if !s.picking.TryLock() {
		fail(w, http.StatusConflict, "目录选择器已经开着了")
		return
	}
	defer s.picking.Unlock()

	path, err := pickDirectory(r.Context())
	switch {
	case errors.Is(err, errPickerCanceled):
		writeJSON(w, map[string]any{"canceled": true})
	case errors.Is(err, errNoPicker):
		fail(w, http.StatusNotImplemented, "%v", err)
	case err != nil:
		fail(w, http.StatusInternalServerError, "目录选择器没打开：%v", err)
	default:
		writeJSON(w, map[string]any{"path": path})
	}
}
