package server

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newSkillsServer 起测试服务，并把 HOME 定在测试自己的临时目录上：
// 技能就放在 <home>/.go-harness/skills 里，直接往那儿写文件就是装技能
func newSkillsServer(t *testing.T, llm *stubLLM) (base, cwd, home string) {
	t.Helper()
	base, cwd = newTestServer(t, llm)
	// newTestServer 自己设过一个 HOME，这里换掉：换成测试能直接往里放技能的那个
	home = t.TempDir()
	t.Setenv("HOME", home)
	return base, cwd, home
}

func writeSkillFile(t *testing.T, root, dir, content string) string {
	t.Helper()
	skillDir := filepath.Join(root, dir)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("建技能目录失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("写 SKILL.md 失败：%v", err)
	}
	return skillDir
}

func postJSON(t *testing.T, base, path string, body map[string]any) (int, string) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("编码请求失败：%v", err)
	}
	resp, err := http.Post(base+path, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("请求发失败：%v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, readAll(t, resp)
}

func deleteSkill(t *testing.T, base, cwd, scope, name string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, base+"/api/skills?cwd="+url.QueryEscape(cwd)+
		"&scope="+scope+"&name="+url.QueryEscape(name), nil)
	if err != nil {
		t.Fatalf("建请求失败：%v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求发失败：%v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, readAll(t, resp)
}

func fetchSkills(t *testing.T, base, cwd string) (skillsResp, int) {
	t.Helper()
	resp, err := http.Get(base + "/api/skills?cwd=" + url.QueryEscape(cwd))
	if err != nil {
		t.Fatalf("读技能失败：%v", err)
	}
	defer resp.Body.Close()
	var out skillsResp
	if resp.StatusCode != http.StatusOK {
		return out, resp.StatusCode
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("技能列表不是合法 JSON：%v", err)
	}
	return out, resp.StatusCode
}

type skillsResp struct {
	Skills []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Scope       string `json:"scope"`
		Enabled     bool   `json:"enabled"`
		Shadowed    bool   `json:"shadowed"`
		Path        string `json:"path"`
		Dir         string `json:"dir"`
		Error       string `json:"error"`
	} `json:"skills"`
	UserDir    string `json:"userDir"`
	ProjectDir string `json:"projectDir"`
}

// 列表要有两个 scope 的全部技能，包含停用的和被盖住的——管理界面看的就是这份
func TestSkillsAPIList(t *testing.T) {
	llm := &stubLLM{replies: []string{"答"}}
	base, cwd, home := newSkillsServer(t, llm)

	userRoot := filepath.Join(home, ".go-harness", "skills")
	projRoot := filepath.Join(cwd, ".go-harness", "skills")
	writeSkillFile(t, userRoot, "pdf", "---\nname: pdf-tools\ndescription: 处理 PDF\n---\n正文\n")
	writeSkillFile(t, userRoot, "notes", "# notes\n\n随手记点东西\n")
	writeSkillFile(t, projRoot, "report", "---\nname: report\ndescription: 写周报\n---\n正文\n")
	writeSkillFile(t, projRoot, "shadow", "---\nname: pdf-tools\ndescription: 项目里的那份\n---\n正文\n")

	got, code := fetchSkills(t, base, cwd)
	if code != http.StatusOK {
		t.Fatalf("该 200，实际 %d", code)
	}
	if len(got.Skills) != 4 {
		t.Fatalf("该列出 4 份（含被盖住的），实际 %d 份：%+v", len(got.Skills), got.Skills)
	}
	if got.UserDir != userRoot || got.ProjectDir != projRoot {
		t.Errorf("两个根目录该给出来给空状态用：%q %q", got.UserDir, got.ProjectDir)
	}

	byScope := map[string]int{}
	for _, s := range got.Skills {
		byScope[s.Scope]++
		if !s.Enabled {
			t.Errorf("%s 没关过，该是启用的", s.Dir)
		}
		if s.Error != "" {
			t.Errorf("%s 不该有错：%s", s.Dir, s.Error)
		}
	}
	if byScope["user"] != 2 || byScope["project"] != 2 {
		t.Errorf("两个 scope 各该有两份，实际 %v", byScope)
	}

	// 同名的两份：项目级赢，用户级那份标「被覆盖」
	for _, s := range got.Skills {
		if s.Name != "pdf-tools" {
			continue
		}
		wantShadowed := filepath.Join(userRoot, "pdf") == s.Dir
		if s.Shadowed != wantShadowed {
			t.Errorf("%s 的 shadowed 该是 %v：%+v", s.Dir, wantShadowed, s)
		}
	}
	// 描述分别来自 frontmatter 和正文第一句
	for _, s := range got.Skills {
		switch filepath.Base(s.Dir) {
		case "pdf":
			if s.Description != "处理 PDF" {
				t.Errorf("描述该来自 frontmatter，实际 %q", s.Description)
			}
		case "notes":
			if s.Description != "随手记点东西" {
				t.Errorf("描述该取正文第一句，实际 %q", s.Description)
			}
		}
	}
}

// 停用是热插拔：下一轮的 system prompt 里就没了，文件还在；再启用又回来
func TestDisableTakesEffectNextTurn(t *testing.T) {
	llm := &stubLLM{replies: []string{"答", "答", "答"}}
	base, cwd, home := newSkillsServer(t, llm)
	skillDir := writeSkillFile(t, filepath.Join(home, ".go-harness", "skills"), "pdf",
		"---\nname: pdf-tools\ndescription: 处理 PDF 的提取\n---\n正文\n")
	const id = "aaaa1111bbbb2222"

	runChat(t, base, map[string]any{"cwd": cwd, "sessionId": id, "text": "第一问"})
	prompt := llm.lastSystemPrompt(t)
	if !strings.Contains(prompt, "pdf-tools: 处理 PDF 的提取") {
		t.Fatalf("启用的技能该列进 prompt：\n%s", prompt)
	}
	if !strings.Contains(prompt, filepath.Join(skillDir, "SKILL.md")) {
		t.Errorf("技能清单里该带上 SKILL.md 的位置：\n%s", prompt)
	}

	if code, body := postJSON(t, base, "/api/skills/enable", map[string]any{
		"cwd": cwd, "scope": "user", "name": "pdf-tools", "enabled": false,
	}); code != http.StatusOK {
		t.Fatalf("停用该 200，实际 %d：%s", code, body)
	}

	runChat(t, base, map[string]any{"cwd": cwd, "sessionId": id, "text": "第二问"})
	if prompt := llm.lastSystemPrompt(t); strings.Contains(prompt, "pdf-tools") {
		t.Errorf("停用之后下一轮就不该再列出来：\n%s", prompt)
	}
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Errorf("停用不是删除，文件该还在：%v", err)
	}
	got, _ := fetchSkills(t, base, cwd)
	if len(got.Skills) != 1 || got.Skills[0].Enabled {
		t.Errorf("列表里该还能看到这份并标成停用：%+v", got.Skills)
	}

	if code, body := postJSON(t, base, "/api/skills/enable", map[string]any{
		"cwd": cwd, "scope": "user", "name": "pdf-tools", "enabled": true,
	}); code != http.StatusOK {
		t.Fatalf("启用该 200，实际 %d：%s", code, body)
	}
	runChat(t, base, map[string]any{"cwd": cwd, "sessionId": id, "text": "第三问"})
	if prompt := llm.lastSystemPrompt(t); !strings.Contains(prompt, "pdf-tools") {
		t.Errorf("启用之后该回来：\n%s", prompt)
	}
}

// 强制技能：正文整份进这一轮的 prompt，而且不落进会话文件——只对这一条消息生效
func TestForcedSkillBodyInjected(t *testing.T) {
	llm := &stubLLM{replies: []string{"答"}}
	base, cwd, home := newSkillsServer(t, llm)
	writeSkillFile(t, filepath.Join(home, ".go-harness", "skills"), "pdf",
		"---\nname: pdf-tools\ndescription: 处理 PDF\n---\n\n第一步先跑 scripts/extract.sh\n")
	const id = "aaaa1111bbbb2222"

	runChat(t, base, map[string]any{"cwd": cwd, "sessionId": id, "text": "把这份 PDF 处理一下", "skill": "pdf-tools"})

	prompt := llm.lastSystemPrompt(t)
	if !strings.Contains(prompt, "这一轮用户指定了技能「pdf-tools」") {
		t.Fatalf("强制的技能该在 prompt 里说清楚：\n%s", prompt)
	}
	if !strings.Contains(prompt, "第一步先跑 scripts/extract.sh") {
		t.Errorf("强制技能的正文该整份贴进 prompt：\n%s", prompt)
	}
	if strings.Index(prompt, "第一步先跑 scripts/extract.sh") < strings.Index(prompt, "当前工作目录") {
		t.Errorf("正文该压在 prompt 最后：\n%s", prompt)
	}

	// 会话文件里不该有技能的正文：下一轮重放历史时它不该再出现
	view := fetchSession(t, base, cwd, id)
	for _, entry := range view.Entries {
		if entry.Message == nil {
			continue
		}
		for _, block := range entry.Message.Blocks {
			if strings.Contains(block.Text, "第一步先跑 scripts/extract.sh") {
				t.Errorf("强制技能不该落进会话文件：%+v", block)
			}
		}
	}

	// 名字要落在这一轮的用户 entry 上：刷新之后界面靠它把角标补回来。
	// 回答和工具结果不带 —— 模型读了什么跟用户选了什么不是一回事
	for i, entry := range view.Entries {
		want := ""
		if i == 0 {
			want = "pdf-tools"
		}
		if entry.Skill != want {
			t.Errorf("第 %d 条 entry 的技能名该是 %q，实际 %q", i, want, entry.Skill)
		}
	}
}

// 没选技能的轮次，entry 里不该冒出技能名来 —— 角标只在真强制过的那条上
func TestForcedSkillAbsentWhenUnforced(t *testing.T) {
	llm := &stubLLM{replies: []string{"答"}}
	base, cwd, home := newSkillsServer(t, llm)
	writeSkillFile(t, filepath.Join(home, ".go-harness", "skills"), "pdf", "---\nname: pdf-tools\n---\n正文\n")
	const id = "cccc3333dddd4444"

	runChat(t, base, map[string]any{"cwd": cwd, "sessionId": id, "text": "随便问问"})

	for _, entry := range fetchSession(t, base, cwd, id).Entries {
		if entry.Skill != "" {
			t.Errorf("没强制技能的轮次不该有技能名：%+v", entry)
		}
	}
}

// 强制一个模型看不到的技能：开流之前就 400，一个 token 都不花
func TestForcedSkillValidation(t *testing.T) {
	llm := &stubLLM{replies: []string{"答"}}
	base, cwd, home := newSkillsServer(t, llm)
	root := filepath.Join(home, ".go-harness", "skills")
	writeSkillFile(t, root, "pdf", "---\nname: pdf-tools\n---\n正文\n")
	writeSkillFile(t, root, "off", "---\nname: off-tool\n---\n正文\n")
	if code, body := postJSON(t, base, "/api/skills/enable", map[string]any{
		"cwd": cwd, "scope": "user", "name": "off-tool", "enabled": false,
	}); code != http.StatusOK {
		t.Fatalf("停用该 200，实际 %d：%s", code, body)
	}

	cases := map[string]string{
		"没有这个技能": "没这个",
		"技能被停用了": "off-tool",
	}
	for name, skill := range cases {
		t.Run(name, func(t *testing.T) {
			before := llm.callCount()
			code, body := postJSON(t, base, "/api/chat", map[string]any{
				"cwd": cwd, "sessionId": "aaaa1111bbbb2222", "text": "问", "skill": skill,
			})
			if code != http.StatusBadRequest {
				t.Fatalf("该 400，实际 %d：%s", code, body)
			}
			if !strings.Contains(body, "技能") {
				t.Errorf("报错该说清是技能的问题：%s", body)
			}
			if llm.callCount() != before {
				t.Error("校验失败不该花掉任何一次模型调用")
			}
		})
	}
}

// AGENTS.md 在就进 prompt，不在就一个字都不提
func TestAgentsMDInjected(t *testing.T) {
	llm := &stubLLM{replies: []string{"答", "答"}}
	base, cwd, _ := newSkillsServer(t, llm)
	const id = "aaaa1111bbbb2222"

	runChat(t, base, map[string]any{"cwd": cwd, "sessionId": id, "text": "第一问"})
	if prompt := llm.lastSystemPrompt(t); strings.Contains(prompt, "AGENTS.md") {
		t.Errorf("没有 AGENTS.md 时不该有这一段：\n%s", prompt)
	}

	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("提交前跑 go test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runChat(t, base, map[string]any{"cwd": cwd, "sessionId": id, "text": "第二问"})
	prompt := llm.lastSystemPrompt(t)
	if !strings.Contains(prompt, "本项目的约定") || !strings.Contains(prompt, "提交前跑 go test") {
		t.Errorf("AGENTS.md 该进 prompt：\n%s", prompt)
	}
}

// 入参不对一律 400，且不许碰到磁盘上任何东西
func TestSkillsAPIValidation(t *testing.T) {
	llm := &stubLLM{replies: []string{"答"}}
	base, cwd, home := newSkillsServer(t, llm)
	victim := filepath.Join(home, "victim")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, code := fetchSkills(t, base, cwd+"/不存在"); code != http.StatusBadRequest {
		t.Errorf("cwd 不是目录该 400，实际 %d", code)
	}
	resp, err := http.Get(base + "/api/skills?cwd=relative/path")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("相对 cwd 该 400，实际 %d", resp.StatusCode)
	}

	cases := []struct {
		name string
		path string
		body map[string]any
	}{
		{"scope 不认识", "/api/skills/enable", map[string]any{"cwd": cwd, "scope": "global", "name": "x", "enabled": false}},
		{"技能不存在", "/api/skills/enable", map[string]any{"cwd": cwd, "scope": "user", "name": "没这个", "enabled": false}},
		{"名字是路径穿越", "/api/skills/enable", map[string]any{"cwd": cwd, "scope": "user", "name": "../../victim", "enabled": false}},
		{"安装的 scope 不认识", "/api/skills/install", map[string]any{"cwd": cwd, "scope": "global", "filename": "a.zip", "zip": ""}},
		{"安装的 zip 不是 base64", "/api/skills/install", map[string]any{"cwd": cwd, "scope": "user", "filename": "a.zip", "zip": "%%%"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if code, body := postJSON(t, base, c.path, c.body); code != http.StatusBadRequest {
				t.Errorf("该 400，实际 %d：%s", code, body)
			}
		})
	}

	if code, body := deleteSkill(t, base, cwd, "user", "../../victim"); code != http.StatusBadRequest {
		t.Errorf("删除的穿越名字该 400，实际 %d：%s", code, body)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("skills 目录外面的东西一个都没动才对：%v", err)
	}
}

// zipOf 造一个最小可用的技能包
func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatalf("造 zip 失败：%v", err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatalf("写 zip 失败：%v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("收尾 zip 失败：%v", err)
	}
	return buf.Bytes()
}

func installBody(cwd, scope string, data []byte, overwrite bool) map[string]any {
	return map[string]any{
		"cwd": cwd, "scope": scope, "filename": "pdf-tools.zip",
		"zip": base64.StdEncoding.EncodeToString(data), "overwrite": overwrite,
	}
}

// 装一个包：200 落到磁盘、重名 409、带上 overwrite 再装一次覆盖掉
func TestSkillInstallEndpoint(t *testing.T) {
	llm := &stubLLM{replies: []string{"答"}}
	base, cwd, home := newSkillsServer(t, llm)

	first := zipOf(t, map[string]string{
		"pdf-tools/SKILL.md":            "---\nname: pdf\ndescription: 处理 PDF\n---\n正文\n",
		"pdf-tools/scripts/run.sh":      "#!/bin/sh\n",
		"pdf-tools/references/howto.md": "怎么用\n",
	})

	code, body := postJSON(t, base, "/api/skills/install", installBody(cwd, "user", first, false))
	if code != http.StatusOK {
		t.Fatalf("安装该 200，实际 %d：%s", code, body)
	}
	if !strings.Contains(body, `"files":3`) {
		t.Errorf("该报告装了几个文件：%s", body)
	}
	installed := filepath.Join(home, ".go-harness", "skills", "pdf-tools")
	if _, err := os.Stat(filepath.Join(installed, "references", "howto.md")); err != nil {
		t.Errorf("附带的文件该解出来：%v", err)
	}
	// 装完立刻能用：列表里出现，且是启用的
	got, _ := fetchSkills(t, base, cwd)
	if len(got.Skills) != 1 || got.Skills[0].Name != "pdf" || !got.Skills[0].Enabled {
		t.Errorf("装完该在列表里且是启用的：%+v", got.Skills)
	}

	if code, body := postJSON(t, base, "/api/skills/install", installBody(cwd, "user", first, false)); code != http.StatusConflict {
		t.Errorf("重名该 409（前端据此问要不要覆盖），实际 %d：%s", code, body)
	}

	second := zipOf(t, map[string]string{"pdf-tools/SKILL.md": "---\nname: pdf\ndescription: 新版\n---\n正文\n"})
	if code, body := postJSON(t, base, "/api/skills/install", installBody(cwd, "user", second, true)); code != http.StatusOK {
		t.Fatalf("带 overwrite 该 200，实际 %d：%s", code, body)
	}
	if got := readFile(t, filepath.Join(installed, "SKILL.md")); !strings.Contains(got, "新版") {
		t.Errorf("覆盖之后该是新内容：%q", got)
	}
	if _, err := os.Stat(filepath.Join(installed, "references")); err == nil {
		t.Error("覆盖是整份换掉，上一版的附带文件不该留着")
	}

	// 装到项目级：落在项目的 .go-harness 下，不受用户级影响
	projZip := zipOf(t, map[string]string{"proj-skill/SKILL.md": "---\nname: proj-skill\n---\n正文\n"})
	if code, body := postJSON(t, base, "/api/skills/install", installBody(cwd, "project", projZip, false)); code != http.StatusOK {
		t.Fatalf("装到项目级该 200，实际 %d：%s", code, body)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".go-harness", "skills", "proj-skill", "SKILL.md")); err != nil {
		t.Errorf("项目级的包该落在项目目录里：%v", err)
	}
}

// 请求体超过上限：413，别把整个包读进内存再说
func TestSkillInstallTooBig(t *testing.T) {
	llm := &stubLLM{replies: []string{"答"}}
	base, cwd, _ := newSkillsServer(t, llm)

	body := map[string]any{
		"cwd": cwd, "scope": "user", "filename": "big.zip",
		"zip": strings.Repeat("a", maxInstallBody+1),
	}
	if code, resp := postJSON(t, base, "/api/skills/install", body); code != http.StatusRequestEntityTooLarge {
		t.Errorf("该 413，实际 %d：%s", code, resp)
	}
}

// 删除：目录真没了，停用清单不动
func TestSkillDeleteEndpoint(t *testing.T) {
	llm := &stubLLM{replies: []string{"答"}}
	base, cwd, home := newSkillsServer(t, llm)
	dir := writeSkillFile(t, filepath.Join(home, ".go-harness", "skills"), "pdf", "---\nname: pdf-tools\n---\n正文\n")

	if code, body := deleteSkill(t, base, cwd, "user", "pdf-tools"); code != http.StatusOK {
		t.Fatalf("删除该 200，实际 %d：%s", code, body)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("删完目录该没了")
	}
	if got, _ := fetchSkills(t, base, cwd); len(got.Skills) != 0 {
		t.Errorf("列表该空了：%+v", got.Skills)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", path, err)
	}
	return string(buf)
}
