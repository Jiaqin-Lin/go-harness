package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/reuben/go-harness/internal/skills"
)

// maxInstallBody 安装请求的 body 上限。zip 是 base64 塞在 JSON 里的，
// 比原包还大三分之一，12MB 差不多能装 6MB 的包
const maxInstallBody = 12 << 20

type skillDTO struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Scope       string `json:"scope"`
	Enabled     bool   `json:"enabled"`
	Shadowed    bool   `json:"shadowed,omitempty"`
	Path        string `json:"path"`
	Dir         string `json:"dir"`
	Error       string `json:"error,omitempty"`
}

func toSkillDTO(s skills.Skill) skillDTO {
	return skillDTO{
		Name:        s.Name,
		Description: s.Description,
		Scope:       string(s.Scope),
		Enabled:     s.Enabled,
		Shadowed:    s.Shadowed,
		Path:        s.Path,
		Dir:         s.Dir,
		Error:       s.Err,
	}
}

// handleSkills 列全部技能：停用的、被同名盖住的、读不出来的都在里面——
// 管理界面要看全部状态。给模型看的那份是 Effective 的事，这里不管
func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	cwd, err := skillCwd(r.URL.Query().Get("cwd"))
	if err != nil {
		fail(w, http.StatusBadRequest, "%v", err)
		return
	}

	list := skills.Scan(cwd)
	out := make([]skillDTO, 0, len(list))
	for _, sk := range list {
		out = append(out, toSkillDTO(sk))
	}

	// 两个根目录给前端做空状态提示用：告诉用户该往哪儿放目录，或者传个 zip
	userDir, err := skills.Root(skills.ScopeUser, cwd)
	if err != nil {
		fail(w, http.StatusInternalServerError, "%v", err)
		return
	}
	projectDir, _ := skills.Root(skills.ScopeProject, cwd)
	writeJSON(w, map[string]any{"skills": out, "userDir": userDir, "projectDir": projectDir})
}

// skillCwd 每个入口都得先过一遍：技能在哪个 scope、哪个目录，全看这个 cwd
func skillCwd(raw string) (string, error) {
	if !filepath.IsAbs(raw) {
		return "", errors.New("cwd 要是绝对路径")
	}
	cwd := filepath.Clean(raw)
	if !isDir(cwd) {
		return "", errors.New("cwd 不是一个目录")
	}
	return cwd, nil
}

// handleSkillEnable 开 / 关一个技能。传的是明确的状态不是 toggle：
// 幂等，双击也不会翻到反的那一边
func (s *Server) handleSkillEnable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cwd     string `json:"cwd"`
		Scope   string `json:"scope"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求体不是合法 JSON：%v", err)
		return
	}

	scope := skills.Scope(req.Scope)
	if !scope.Valid() {
		fail(w, http.StatusBadRequest, "scope 只有 user 和 project 两个值")
		return
	}
	cwd, err := skillCwd(req.Cwd)
	if err != nil {
		fail(w, http.StatusBadRequest, "%v", err)
		return
	}

	// 名字从来不拼进路径：先 Find 定位到磁盘上的那一份，定位不到就 400。
	// 路径穿越到这一步自然就落空了
	if _, ok := skills.Find(scope, cwd, req.Name); !ok {
		fail(w, http.StatusBadRequest, "找不到这个技能")
		return
	}
	if err := skills.SetEnabled(scope, cwd, req.Name, req.Enabled); err != nil {
		fail(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, map[string]any{"scope": string(scope), "name": req.Name, "enabled": req.Enabled})
}

// handleSkillDelete 删掉技能目录。停用清单不动：重装回来还是原来的开关状态，
// 比「装回来发现是关着的」好
func (s *Server) handleSkillDelete(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	scope := skills.Scope(query.Get("scope"))
	if !scope.Valid() {
		fail(w, http.StatusBadRequest, "scope 只有 user 和 project 两个值")
		return
	}
	cwd, err := skillCwd(query.Get("cwd"))
	if err != nil {
		fail(w, http.StatusBadRequest, "%v", err)
		return
	}
	sk, ok := skills.Find(scope, cwd, query.Get("name"))
	if !ok {
		fail(w, http.StatusBadRequest, "找不到这个技能")
		return
	}

	// 技能目录是软链的话，RemoveAll 摘的是链、源目录不动——这是有意的
	if err := os.RemoveAll(sk.Dir); err != nil {
		fail(w, http.StatusInternalServerError, "删不掉 %s：%v", sk.Dir, err)
		return
	}
	writeJSON(w, map[string]string{"scope": string(scope), "name": sk.Name})
}

// handleSkillInstall 收一个 base64 的 zip 装成技能。走 JSON 而不是 multipart：
// 这一层的 POST 一律要求 application/json（见 server.go 的 requireJSONForPost），
// 为上传开个口子等于在 CSRF 防线上开个口子
func (s *Server) handleSkillInstall(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxInstallBody)

	var req struct {
		Cwd       string `json:"cwd"`
		Scope     string `json:"scope"`
		Filename  string `json:"filename"`
		Zip       string `json:"zip"`
		Overwrite bool   `json:"overwrite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			fail(w, http.StatusRequestEntityTooLarge, "包太大了（上限 %d MB）", maxInstallBody>>20)
			return
		}
		fail(w, http.StatusBadRequest, "请求体不是合法 JSON：%v", err)
		return
	}

	scope := skills.Scope(req.Scope)
	if !scope.Valid() {
		fail(w, http.StatusBadRequest, "scope 只有 user 和 project 两个值")
		return
	}
	cwd, err := skillCwd(req.Cwd)
	if err != nil {
		fail(w, http.StatusBadRequest, "%v", err)
		return
	}

	data, err := base64.StdEncoding.DecodeString(req.Zip)
	if err != nil {
		fail(w, http.StatusBadRequest, "zip 不是合法的 base64：%v", err)
		return
	}

	sk, files, err := skills.InstallZip(scope, cwd, req.Filename, data, req.Overwrite)
	switch {
	case errors.Is(err, skills.ErrSkillExists):
		// 409 是让前端再问一句「覆盖？」——要不要盖掉已有的技能得用户说了算
		fail(w, http.StatusConflict, "%v", err)
		return
	case errors.Is(err, skills.ErrInvalidZip):
		fail(w, http.StatusBadRequest, "%v", err)
		return
	case err != nil:
		fail(w, http.StatusInternalServerError, "%v", err)
		return
	}

	// 返回的是装完之后重新读出来的那份：目录名和 frontmatter 里的 name 可能不一样，
	// 界面该显示模型真正会看到的那个名字
	writeJSON(w, map[string]any{"skill": toSkillDTO(sk), "files": files, "overwritten": req.Overwrite})
}
