package skills

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// setHome 把主目录指到临时目录，测试数据不往真实的 ~/.go-harness 里写
func setHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func userSkillsDir(home string) string { return filepath.Join(home, ".go-harness", "skills") }

func projectSkillsDir(project string) string { return filepath.Join(project, ".go-harness", "skills") }

// writeSkill 在 root/<目录名>/SKILL.md 放一份技能，返回技能目录
func writeSkill(t *testing.T, root, dir, content string) string {
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

func names(skills []Skill) []string {
	out := make([]string, 0, len(skills))
	for _, s := range skills {
		out = append(out, s.Name)
	}
	return out
}

func skillNamed(t *testing.T, skills []Skill, name string) Skill {
	t.Helper()
	for _, s := range skills {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("没有叫 %q 的技能，实际有 %v", name, names(skills))
	return Skill{}
}

// 两个 scope 都扫得到，Path 各指到自己的 SKILL.md
func TestScanTwoScopes(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()

	userDir := writeSkill(t, userSkillsDir(home), "pdf", "---\nname: pdf-tools\ndescription: 处理 PDF\n---\n正文\n")
	projDir := writeSkill(t, projectSkillsDir(project), "report", "# report\n\n写周报\n")

	all := Scan(project)
	if len(all) != 2 {
		t.Fatalf("期望 2 个技能，实际 %v", names(all))
	}

	user := skillNamed(t, all, "pdf-tools")
	if user.Scope != ScopeUser || user.Dir != userDir || user.Path != filepath.Join(userDir, "SKILL.md") {
		t.Errorf("用户级技能的路径不对：%+v", user)
	}
	if !user.Enabled {
		t.Error("没关过的技能该是启用的")
	}
	if user.Description != "处理 PDF" {
		t.Errorf("描述该来自 frontmatter，实际 %q", user.Description)
	}

	proj := skillNamed(t, all, "report")
	if proj.Scope != ScopeProject || proj.Dir != projDir {
		t.Errorf("项目级技能的路径不对：%+v", proj)
	}
	if proj.Description != "写周报" {
		t.Errorf("描述该取正文第一句，实际 %q", proj.Description)
	}
}

// 同名时项目级赢：生效清单里只剩项目级那份，用户级那份留在 Scan 的结果里并打上标记
func TestProjectOverridesUser(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()

	userDir := writeSkill(t, userSkillsDir(home), "x-user", "---\nname: dup\ndescription: 用户级\n---\n正文\n")
	projDir := writeSkill(t, projectSkillsDir(project), "x-proj", "---\nname: dup\ndescription: 项目级\n---\n正文\n")

	eff := Effective(project)
	if len(eff) != 1 || eff[0].Dir != projDir {
		t.Fatalf("项目级该盖掉用户级，实际 %v", names(eff))
	}

	for _, s := range Scan(project) {
		if s.Dir != userDir && s.Dir != projDir {
			t.Fatalf("扫出了没见过的目录 %s", s.Dir)
		}
		// 谁被盖住跟排序无关，只看这一份是不是没进生效清单
		if wantShadowed := s.Dir == userDir; s.Shadowed != wantShadowed {
			t.Errorf("%s 的 Shadowed 该是 %v：%+v", s.Dir, wantShadowed, s)
		}
	}
}

// 停用的技能不进生效清单，文件也还在——停用不是删除
func TestDisabledExcludedFromEffective(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()
	dir := writeSkill(t, userSkillsDir(home), "pdf", "---\ndescription: 处理 PDF\n---\n正文\n")

	if err := SetEnabled(ScopeUser, project, "pdf", false); err != nil {
		t.Fatalf("停用失败：%v", err)
	}

	if eff := Effective(project); len(eff) != 0 {
		t.Errorf("停用的技能不该进生效清单，实际 %v", names(eff))
	}
	all := Scan(project)
	if len(all) != 1 || all[0].Enabled {
		t.Errorf("Scan 该列出停用的这份并标 Enabled=false：%+v", all)
	}
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Errorf("停用不该动文件：%v", err)
	}

	// 再启用就回来
	if err := SetEnabled(ScopeUser, project, "pdf", true); err != nil {
		t.Fatalf("启用失败：%v", err)
	}
	if eff := Effective(project); len(eff) != 1 {
		t.Errorf("重新启用后该回到生效清单，实际 %v", names(eff))
	}
}

// 停用项目级那份之后，用户级的同名技能要露出来——先滤停用再合并就是为了这个
func TestDisabledProjectRevealsUser(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()

	userDir := writeSkill(t, userSkillsDir(home), "x-user", "---\nname: dup\ndescription: 用户级\n---\n正文\n")
	writeSkill(t, projectSkillsDir(project), "x-proj", "---\nname: dup\ndescription: 项目级\n---\n正文\n")

	if err := SetEnabled(ScopeProject, project, "dup", false); err != nil {
		t.Fatalf("停用失败：%v", err)
	}

	eff := Effective(project)
	if len(eff) != 1 || eff[0].Dir != userDir || eff[0].Shadowed {
		t.Fatalf("停用项目级之后该轮到用户级，实际 %+v", eff)
	}
}

// 没有 SKILL.md 的目录不算技能（只放 scripts/ 的目录是合法的）
func TestSkillDirWithoutSkillMDIgnored(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()
	dir := filepath.Join(userSkillsDir(home), "scripts-only", "scripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if all := Scan(project); len(all) != 0 {
		t.Errorf("期望扫不到技能，实际 %v", names(all))
	}
}

// 只扫一层：skills/a/b/SKILL.md 不算技能
func TestNestedSkillIgnored(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()
	writeSkill(t, filepath.Join(userSkillsDir(home), "a"), "b", "---\nname: deep\n---\n正文\n")

	if all := Scan(project); len(all) != 0 {
		t.Errorf("嵌套的技能不该被扫到，实际 %v", names(all))
	}
}

// 技能目录是软链也认。删除走 RemoveAll 只摘链、不动源目录，所以扫的时候必须先跟过去
func TestSymlinkedSkillDirFollowed(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()

	source := writeSkill(t, t.TempDir(), "real", "---\nname: linked\ndescription: 软链过来的\n---\n正文\n")
	root := userSkillsDir(home)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, filepath.Join(root, "link")); err != nil {
		t.Skipf("这个环境建不了软链：%v", err)
	}

	all := Scan(project)
	if len(all) != 1 || all[0].Name != "linked" || all[0].Dir != filepath.Join(root, "link") {
		t.Fatalf("软链目录该被当成技能，实际 %+v", all)
	}
}

// 同一个 scope 里两个目录报同一个名字：排序靠前的赢，输家标 Err 且不进生效清单
func TestDuplicateNameInOneScope(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()
	root := userSkillsDir(home)
	winner := writeSkill(t, root, "a-dir", "---\nname: dup\ndescription: 先来的\n---\n正文\n")
	writeSkill(t, root, "b-dir", "---\nname: dup\ndescription: 后来的\n---\n正文\n")

	all := Scan(project)
	if len(all) != 2 {
		t.Fatalf("两份都该列出来，实际 %v", names(all))
	}
	if all[0].Dir != winner || all[0].Err != "" {
		t.Errorf("排序靠前的该是赢家：%+v", all[0])
	}
	if all[1].Err == "" {
		t.Error("输家该标上重名")
	}

	eff := Effective(project)
	if len(eff) != 1 || eff[0].Dir != winner {
		t.Errorf("重名的只该生效一份，实际 %+v", eff)
	}
}

// cwd 就是家目录时两个 root 是同一个目录，每条技能只能出现一次
func TestHomeCwdNoDuplicate(t *testing.T) {
	home := setHome(t)
	writeSkill(t, userSkillsDir(home), "pdf", "---\nname: pdf\ndescription: 处理 PDF\n---\n正文\n")

	all := Scan(home)
	if len(all) != 1 {
		t.Fatalf("期望 1 个技能，实际 %v", names(all))
	}
	if all[0].Scope != ScopeUser {
		t.Errorf("家目录下扫描时项目 scope 不该贡献技能：%+v", all[0])
	}
}

// 顺序必须稳定：这份清单是 system prompt 的一段，抖动等于每轮都让前缀缓存失效
func TestEffectiveSortedStable(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()
	root := userSkillsDir(home)
	writeSkill(t, root, "d1", "---\nname: Beta\n---\n正文\n")
	writeSkill(t, root, "d2", "---\nname: alpha\n---\n正文\n")
	writeSkill(t, root, "d3", "---\nname: Gamma\n---\n正文\n")

	want := []string{"alpha", "Beta", "Gamma"}
	for i := range 3 {
		if got := names(Effective(project)); !slices.Equal(got, want) {
			t.Fatalf("第 %d 次扫描顺序不对：期望 %v，实际 %v", i+1, want, got)
		}
	}
}

// 同名技能两个 scope 各有一份时，Find 要打对那一份
func TestFindIsScopeScoped(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()
	userDir := writeSkill(t, userSkillsDir(home), "x-user", "---\nname: dup\n---\n正文\n")
	projDir := writeSkill(t, projectSkillsDir(project), "x-proj", "---\nname: dup\n---\n正文\n")

	if s, ok := Find(ScopeUser, project, "dup"); !ok || s.Dir != userDir {
		t.Errorf("Find(user) 该拿到用户级那份：%+v", s)
	}
	if s, ok := Find(ScopeProject, project, "dup"); !ok || s.Dir != projDir {
		t.Errorf("Find(project) 该拿到项目级那份：%+v", s)
	}
	if _, ok := Find(ScopeUser, project, "没这个"); ok {
		t.Error("不存在的名字不该找到")
	}
}

// ByName 是强制技能的校验口：被盖住、被停用、读不出来的都不能强制
func TestByNameRejectsShadowedAndDisabled(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()
	writeSkill(t, userSkillsDir(home), "x-user", "---\nname: dup\n---\n正文\n")
	writeSkill(t, projectSkillsDir(project), "x-proj", "---\nname: dup\n---\n正文\n")
	writeSkill(t, userSkillsDir(home), "other", "---\nname: other\n---\n正文\n")

	if s, ok := ByName(project, "dup"); !ok || s.Scope != ScopeProject {
		t.Errorf("同名该拿到项目级那份：%+v", s)
	}
	if err := SetEnabled(ScopeUser, project, "other", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := ByName(project, "other"); ok {
		t.Error("停用的技能不该能被强制")
	}
	if _, ok := ByName(project, "没这个"); ok {
		t.Error("不存在的技能不该找到")
	}
}

// SKILL.md 读不出来（比如权限不对、是个目录、断掉的软链）时，这份技能带 Err 列出来，
// 而不是悄悄消失——用户得知道这个目录有问题。
// 注意「文件不存在」不算这类：那只是目录里没放 SKILL.md，正常跳过
func TestUnreadableSkillListed(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()
	dir := filepath.Join(userSkillsDir(home), "pdf")
	if err := os.MkdirAll(filepath.Join(dir, "SKILL.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	all := Scan(project)
	if len(all) != 1 || all[0].Err == "" || all[0].Name != "pdf" {
		t.Fatalf("读不出的 SKILL.md 该带 Err 列出来：%+v", all)
	}
	if eff := Effective(project); len(eff) != 0 {
		t.Errorf("读不出来的不该生效，实际 %v", names(eff))
	}
}

func TestValidName(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"pdf-tools", true},
		{"周报", true},
		{"a b", true},
		{"", false},
		{".", false},
		{"..", false},
		{".hidden", false},
		{"a/b", false},
		{"a\\b", false},
		{"有\x00空", false},
		{" 边上空格 ", false},
		{"名字长得离谱名字长得离谱名字长得离谱名字长得离谱名字长得离谱名字长得离谱名字长得离谱名字长得离谱名字长得离谱名字长得离谱名字长得离谱名字长得离谱名字长得离谱名字长得离谱名字长得离谱名字长得离谱名字长得离谱名字长得离谱名字长得离谱名字长得离谱名字长得离谱名字", false},
	}
	for _, tc := range cases {
		if got := ValidName(tc.in); got != tc.want {
			t.Errorf("ValidName(%q)：期望 %v，实际 %v", tc.in, tc.want, got)
		}
	}
}

// Body 剥掉 frontmatter，给强制注入用
func TestSkillBody(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()
	writeSkill(t, userSkillsDir(home), "pdf", "---\nname: pdf\ndescription: d\n---\n\n# 步骤\n一、二\n")

	s, ok := ByName(project, "pdf")
	if !ok {
		t.Fatal("该找到 pdf")
	}
	body, err := s.Body()
	if err != nil {
		t.Fatalf("读正文失败：%v", err)
	}
	if want := "# 步骤\n一、二\n"; body != want {
		t.Errorf("正文该是 %q，实际 %q", want, body)
	}

	if err := os.Remove(s.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Body(); err == nil {
		t.Error("文件没了该报错")
	}
}
