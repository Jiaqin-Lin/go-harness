package server

import (
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
)

type fileDTO struct {
	Path  string `json:"path"` // 相对 cwd：模型拿到的 cwd 就是它，用户手写的路径也长这样
	IsDir bool   `json:"isDir,omitempty"`
}

const (
	// maxWalkEntries 一次补全最多看多少个目录项。补全是每敲一个字跑一次的事，
	// 不能让一次穷举把大仓库整个翻一遍
	maxWalkEntries = 20000
	// maxFileMatches 一次最多回几条。多了菜单也放不下，挑最像的那几条就够
	maxFileMatches = 50
)

// skipDir 是不往里走的目录：隐藏目录（.git 为首）里没有用户会引用的东西，
// node_modules 一进去就是几万个文件，找出来的也基本没用
func skipDir(name string) bool {
	return name == "node_modules" || strings.HasPrefix(name, ".")
}

// handleListFiles 给输入框的 @ 补全用：在项目里按前缀找文件和目录。
// 两档匹配 —— 整条相对路径按前缀命中的排前面（@src/ma 找 src/main.go），
// 只有文件（目录）名命中的排后面（@main 也找得到 cmd/main.go）
func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	cwd := filepath.Clean(strings.TrimSpace(r.URL.Query().Get("cwd")))
	if !filepath.IsAbs(cwd) || !isDir(cwd) {
		fail(w, http.StatusBadRequest, "cwd 要是存在的绝对路径")
		return
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

	var byPath, byName []fileDTO
	visited := 0
	// 走不动的地方（权限、半路消失的目录）跳过就行：补全不值得为一个条目报错
	_ = filepath.WalkDir(cwd, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || path == cwd {
			return nil
		}
		if entry.IsDir() && skipDir(entry.Name()) {
			return fs.SkipDir
		}
		visited++
		if visited > maxWalkEntries {
			return fs.SkipAll
		}
		rel, err := filepath.Rel(cwd, path)
		if err != nil {
			return nil
		}
		switch {
		case strings.HasPrefix(strings.ToLower(rel), query):
			byPath = append(byPath, fileDTO{Path: rel, IsDir: entry.IsDir()})
		case strings.HasPrefix(strings.ToLower(entry.Name()), query):
			byName = append(byName, fileDTO{Path: rel, IsDir: entry.IsDir()})
		}
		return nil
	})

	// 空结果要回 []，不能是 null（前端拿到的 files 直接 .length）——
	// append(nil, nil...) 还是 nil，所以从 make 起头
	files := make([]fileDTO, 0, len(byPath)+len(byName))
	files = append(append(files, byPath...), byName...)
	if len(files) > maxFileMatches {
		files = files[:maxFileMatches]
	}
	writeJSON(w, map[string]any{"files": files})
}
