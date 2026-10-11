package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/reuben/go-harness/internal/session"
	"github.com/reuben/go-harness/internal/types"
)

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects", s.handleProjects)
	mux.HandleFunc("POST /api/projects", s.handleProjectAdd)
	mux.HandleFunc("DELETE /api/projects", s.handleProjectRemove)
	mux.HandleFunc("GET /api/fs", s.handleListDirs)
	mux.HandleFunc("GET /api/meta", s.handleMeta)
	mux.HandleFunc("POST /api/settings", s.handleSettings)
	mux.HandleFunc("GET /api/session", s.handleSession)
	mux.HandleFunc("DELETE /api/session", s.handleSessionDelete)
	mux.HandleFunc("POST /api/chat", s.handleChat)
	mux.HandleFunc("POST /api/chat/abort", s.handleChatAbort)
	mux.HandleFunc("GET /api/skills", s.handleSkills)
	mux.HandleFunc("POST /api/skills/enable", s.handleSkillEnable)
	mux.HandleFunc("DELETE /api/skills", s.handleSkillDelete)
	mux.HandleFunc("POST /api/skills/install", s.handleSkillInstall)
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	s.writeMeta(w)
}

type projectDTO struct {
	Path     string       `json:"path"`
	Name     string       `json:"name"`
	Exists   bool         `json:"exists"`
	Sessions []sessionDTO `json:"sessions"`
}

type sessionDTO struct {
	ID       string `json:"id"`
	Title    string `json:"title,omitempty"`
	Preview  string `json:"preview,omitempty"` // 标题还没生成时的兜底：第一条用户消息
	Created  string `json:"created"`
	Modified string `json:"modified"`
}

type sessionView struct {
	ID      string     `json:"id"`
	Cwd     string     `json:"cwd"`
	Title   string     `json:"title,omitempty"`
	Preview string     `json:"preview,omitempty"`
	LeafID  string     `json:"leafId,omitempty"`
	Entries []entryDTO `json:"entries"`
}

// handleProjects 是侧栏的全部数据：项目 + 每个项目下的会话。
// 一次给完，前端不用按项目再拉一遍
func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := session.ListProjects()
	if err != nil {
		fail(w, http.StatusInternalServerError, "%v", err)
		return
	}

	out := make([]projectDTO, 0, len(projects))
	for _, project := range projects {
		dto := projectDTO{Path: project.Path, Name: filepath.Base(project.Path), Sessions: []sessionDTO{}}
		if !isDir(project.Path) {
			// 目录没了也照样列出来，前端标「目录不在了」。悄悄从清单里抹掉的话，
			// 用户会以为会话丢了
			out = append(out, dto)
			continue
		}
		dto.Exists = true

		infos, err := session.List(project.Path)
		if err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		for _, info := range infos {
			item := sessionDTO{
				ID:       info.ID,
				Title:    info.Title,
				Created:  info.Created,
				Modified: info.Modified.Format(time.RFC3339),
			}
			if item.Title == "" {
				item.Preview = clip(session.FirstUserText(info.Path), 48)
			}
			dto.Sessions = append(dto.Sessions, item)
		}
		out = append(out, dto)
	}
	writeJSON(w, map[string]any{"projects": out})
}

func (s *Server) handleProjectAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求体不是合法 JSON：%v", err)
		return
	}

	path := filepath.Clean(strings.TrimSpace(req.Path))
	if !filepath.IsAbs(path) {
		fail(w, http.StatusBadRequest, "要给绝对路径")
		return
	}
	if !isDir(path) {
		fail(w, http.StatusBadRequest, "%s 不是一个目录", path)
		return
	}
	if err := session.AddProject(path); err != nil {
		fail(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, map[string]string{"path": path})
}

// handleProjectRemove 只把目录移出清单，sessions/ 下的会话一条不删
func (s *Server) handleProjectRemove(w http.ResponseWriter, r *http.Request) {
	path := filepath.Clean(r.URL.Query().Get("path"))
	if !filepath.IsAbs(path) {
		fail(w, http.StatusBadRequest, "要给绝对路径")
		return
	}
	if err := session.RemoveProject(path); err != nil {
		fail(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, map[string]string{"path": path})
}

type dirDTO struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// handleListDirs 给目录选择器用。只列目录，文件不列 —— 选的是项目根。
// 默认从服务进程的启动目录起，省得每次从头点
func (s *Server) handleListDirs(w http.ResponseWriter, r *http.Request) {
	path := filepath.Clean(strings.TrimSpace(r.URL.Query().Get("path")))
	if path == "" || path == "." {
		path = s.cfg.Cwd
	}
	if !filepath.IsAbs(path) {
		fail(w, http.StatusBadRequest, "要给绝对路径")
		return
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		fail(w, http.StatusBadRequest, "读不了 %s：%v", path, err)
		return
	}

	dirs := []dirDTO{}
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, dirDTO{Name: entry.Name(), Path: filepath.Join(path, entry.Name())})
		}
	}
	slices.SortFunc(dirs, func(a, b dirDTO) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })

	resp := map[string]any{"path": path, "dirs": dirs}
	if parent := filepath.Dir(path); parent != path {
		resp["parent"] = parent
	}
	writeJSON(w, resp)
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	path, sess, err := s.openSession(r.URL.Query().Get("cwd"), r.URL.Query().Get("id"))
	if err != nil {
		fail(w, http.StatusBadRequest, "%v", err)
		return
	}

	entries := make([]entryDTO, 0, len(sess.Entries()))
	for _, entry := range sess.Entries() {
		entries = append(entries, toEntryDTO(entry))
	}
	view := sessionView{
		ID:      sess.ID(),
		Cwd:     sess.Cwd(),
		Title:   session.Title(path),
		LeafID:  sess.LeafID(),
		Entries: entries,
	}
	if view.Title == "" {
		// 从刚解析好的 entry 里拿，别再开一次同一个文件
		view.Preview = clip(firstUserText(sess), 48)
	}
	writeJSON(w, view)
}

func firstUserText(sess *session.Session) string {
	for _, entry := range sess.Entries() {
		if entry.Message != nil && entry.Message.Role == types.UserRole {
			return entry.Message.Text()
		}
	}
	return ""
}

func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	cwd, id := r.URL.Query().Get("cwd"), r.URL.Query().Get("id")
	path, _, err := s.openSession(cwd, id)
	if err != nil {
		fail(w, http.StatusBadRequest, "%v", err)
		return
	}
	// 查锁用跟 chat 一样的 key 构造：cwd 写法不一致的话，正在跑的那一轮会被删掉
	if s.isRunning(keyFor(cwd, id)) {
		fail(w, http.StatusConflict, "这个会话正在跑一轮，等它结束再删")
		return
	}
	if err := session.Delete(path); err != nil {
		fail(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, map[string]string{"id": id})
}

// openSession 按 cwd + id 打开会话文件，顺便把入参校验掉
func (s *Server) openSession(cwd, id string) (string, *session.Session, error) {
	if !session.ValidID(id) {
		return "", nil, errors.New("会话 id 形状不对")
	}
	if !filepath.IsAbs(cwd) {
		return "", nil, errors.New("cwd 要是绝对路径")
	}
	cwd = filepath.Clean(cwd)
	if !isDir(cwd) {
		return "", nil, errors.New("cwd 不是一个目录")
	}
	path, err := session.Find(cwd, id)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil, errors.New("找不到这个会话")
	}
	if err != nil {
		return "", nil, err
	}
	sess, err := session.Open(path)
	if err != nil {
		return "", nil, err
	}
	return path, sess, nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// writeJSON 和 fail 是这一层仅有的两个出口形状：成了给对象，没成给 {"error": ...}，
// 前端只解析这两种
func writeJSON(w http.ResponseWriter, value any) {
	writeJSONWithStatus(w, http.StatusOK, value)
}

func fail(w http.ResponseWriter, code int, format string, args ...any) {
	writeJSONWithStatus(w, code, map[string]string{"error": fmt.Sprintf(format, args...)})
}

// writeJSONWithStatus 先编码再写头：编码失败时头还没出去，还能改成一个正经错误码
func writeJSONWithStatus(w http.ResponseWriter, code int, value any) {
	buf, err := json.Marshal(value)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	w.Write(buf)
}
