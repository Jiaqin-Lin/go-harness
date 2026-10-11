package skills

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type zentry struct {
	name    string
	content string
	mode    os.FileMode // 0 = 普通文件
}

// zipOf 现造一个 zip 包，测试不依赖外部文件。
// 名字以 / 结尾的条目按目录条目写（zip -r 会给每个目录存一条）
func zipOf(t *testing.T, entries ...zentry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		var (
			f   interface{ Write([]byte) (int, error) }
			err error
		)
		switch {
		case strings.HasSuffix(e.name, "/"):
			fh := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
			fh.SetMode(os.ModeDir | 0o755)
			f, err = w.CreateHeader(fh)
		case e.mode == 0:
			f, err = w.Create(e.name)
		default:
			// SetMode 会把 creator 标成 unix——只写 ExternalAttrs 的话读出来会被当成 FAT 包，权限位就丢了
			fh := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
			fh.SetMode(e.mode)
			f, err = w.CreateHeader(fh)
		}
		if err != nil {
			t.Fatalf("造 zip 失败（%s）：%v", e.name, err)
		}
		if _, err := f.Write([]byte(e.content)); err != nil {
			t.Fatalf("写 zip 失败（%s）：%v", e.name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("收尾 zip 失败：%v", err)
	}
	return buf.Bytes()
}

func mustInstall(t *testing.T, scope Scope, cwd, filename string, data []byte, overwrite bool) (Skill, int) {
	t.Helper()
	s, files, err := InstallZip(scope, cwd, filename, data, overwrite)
	if err != nil {
		t.Fatalf("安装失败：%v", err)
	}
	return s, files
}

// 只有一个顶层目录时剥掉它：GitHub 打的包都长这样
func TestInstallStripsTopDir(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()
	data := zipOf(t,
		zentry{name: "pdf-tools/SKILL.md", content: "---\nname: pdf\ndescription: 处理 PDF\n---\n正文\n"},
		zentry{name: "pdf-tools/scripts/extract.sh", content: "#!/bin/sh\necho hi\n", mode: 0o755},
		zentry{name: "pdf-tools/references/formats.md", content: "格式\n"},
	)

	s, files := mustInstall(t, ScopeUser, project, "repo-main.zip", data, false)
	if s.Name != "pdf" {
		t.Errorf("名字该来自 frontmatter，实际 %q", s.Name)
	}
	wantDir := filepath.Join(userSkillsDir(home), "pdf-tools")
	if s.Dir != wantDir {
		t.Errorf("目录名该来自顶层目录，实际 %s", s.Dir)
	}
	if files != 3 {
		t.Errorf("期望装 3 个文件，实际 %d", files)
	}
	if !s.Enabled {
		t.Error("新装的技能该是启用的")
	}

	if got := mustRead(t, filepath.Join(wantDir, "references", "formats.md")); got != "格式\n" {
		t.Errorf("附带的文件该解出来，实际 %q", got)
	}
	info, err := os.Stat(filepath.Join(wantDir, "scripts", "extract.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("zip 里带执行位的脚本要保住执行位")
	}
}

// 平铺的包用文件名当目录名
func TestInstallFlatZipUsesFilename(t *testing.T) {
	setHome(t)
	project := t.TempDir()
	data := zipOf(t, zentry{name: "SKILL.md", content: "---\nname: flat\n---\n正文\n"})

	s, _ := mustInstall(t, ScopeProject, project, "my-skill.zip", data, false)
	if s.Dir != filepath.Join(projectSkillsDir(project), "my-skill") {
		t.Errorf("平铺的包该用文件名当目录名，实际 %s", s.Dir)
	}
	if s.Scope != ScopeProject {
		t.Errorf("scope 不对：%v", s.Scope)
	}
}

// zip 命令行打的包会给每个目录存一条目录条目，不能把它当成「根下的散文件」——
// 真出过这个错：zip -r 出来的包被判成「多个顶层条目」装不上
func TestInstallZipCLIDirectoryEntries(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()
	data := zipOf(t,
		zentry{name: "todo-tool/"},
		zentry{name: "todo-tool/SKILL.md", content: "---\nname: todo\n---\n正文\n"},
		zentry{name: "todo-tool/scripts/"},
		zentry{name: "todo-tool/scripts/x.sh", content: "#!/bin/sh\n", mode: 0o755},
	)

	s, files := mustInstall(t, ScopeUser, project, "todo-tool.zip", data, false)
	if s.Dir != filepath.Join(userSkillsDir(home), "todo-tool") {
		t.Errorf("该剥掉顶层目录装成 todo-tool，实际 %s", s.Dir)
	}
	if files != 2 {
		t.Errorf("目录条目不该算文件数，实际 %d", files)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "scripts", "x.sh")); err != nil {
		t.Errorf("子目录里的文件该解出来：%v", err)
	}
}

// 根下就有 SKILL.md：整个包就是技能本身，一个前缀都不剥，名字取文件名
func TestInstallFlatZipKeepsSubdirs(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()
	data := zipOf(t,
		zentry{name: "SKILL.md", content: "---\nname: flat\n---\n正文\n"},
		zentry{name: "scripts/"},
		zentry{name: "scripts/x.sh", content: "#!/bin/sh\n"},
	)

	s, _ := mustInstall(t, ScopeUser, project, "my-flat.zip", data, false)
	if s.Dir != filepath.Join(userSkillsDir(home), "my-flat") {
		t.Errorf("平铺包该用文件名当目录名，实际 %s", s.Dir)
	}
	// 平铺包里带子目录是正常的：它就是技能的一部分
	if _, err := os.Stat(filepath.Join(s.Dir, "scripts", "x.sh")); err != nil {
		t.Errorf("子目录该原样留着：%v", err)
	}
}

// Finder 打的包会带 __MACOSX/ 和 .DS_Store，跳过就是，不能算成"多个顶层条目"
func TestInstallSkipsFinderJunk(t *testing.T) {
	setHome(t)
	project := t.TempDir()
	data := zipOf(t,
		zentry{name: "__MACOSX/pdf-tools/._SKILL.md", content: "junk"},
		zentry{name: "pdf-tools/.DS_Store", content: "junk"},
		zentry{name: "pdf-tools/SKILL.md", content: "---\nname: pdf\n---\n正文\n"},
	)

	s, files := mustInstall(t, ScopeUser, project, "pdf-tools.zip", data, false)
	if files != 1 {
		t.Errorf("垃圾条目不该算进来，实际 %d 个文件", files)
	}
	mustNotExist(t, filepath.Join(s.Dir, ".DS_Store"))
}

// zip-slip：包里的 .. 和绝对路径都拒掉，整包不收，外面一个字节都不许落
func TestInstallRejectsZipSlip(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()
	outside := filepath.Join(home, ".go-harness", "evil.txt")

	for _, name := range []string{"../evil.txt", "../../evil.txt", "/tmp/evil.txt", "pdf-tools/../../evil.txt", `..\evil.txt`} {
		data := zipOf(t,
			zentry{name: "pdf-tools/SKILL.md", content: "---\nname: pdf\n---\n正文\n"},
			zentry{name: name, content: "x"},
		)
		_, _, err := InstallZip(ScopeUser, project, "pdf.zip", data, false)
		if !errors.Is(err, ErrInvalidZip) {
			t.Errorf("%q 该被拒掉，实际 %v", name, err)
		}
		mustNotExist(t, outside)
		mustNotExist(t, filepath.Join(userSkillsDir(home), "pdf-tools"))
	}
}

// 顶层有多个目录，或者既有目录又有散文件，都说明包里不止一个东西
func TestInstallRejectsMultipleTops(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()

	cases := []struct {
		desc     string
		filename string
		data     []byte
	}{
		{"两个顶层目录", "pdf.zip", zipOf(t, zentry{name: "a/SKILL.md", content: "x"}, zentry{name: "b/SKILL.md", content: "x"})},
		{"顶层既有目录又有散文件", "pdf.zip", zipOf(t, zentry{name: "a/SKILL.md", content: "x"}, zentry{name: "README.md", content: "x"})},
		{"根下没有 SKILL.md", "pdf.zip", zipOf(t, zentry{name: "a/README.md", content: "x"})},
		{"目录名以点开头", "pdf.zip", zipOf(t, zentry{name: ".hidden/SKILL.md", content: "x"})},
		{"平铺包但文件名给不出合法名字", "", zipOf(t, zentry{name: "SKILL.md", content: "x"})},
		{"压根不是 zip", "pdf.zip", []byte("这不是 zip")},
	}
	for _, tc := range cases {
		_, _, err := InstallZip(ScopeUser, project, tc.filename, tc.data, false)
		if !errors.Is(err, ErrInvalidZip) {
			t.Errorf("%s：该判为装不了，实际 %v", tc.desc, err)
		}
	}
	if entries, err := os.ReadDir(userSkillsDir(home)); err == nil && len(entries) > 0 {
		t.Errorf("失败的安装不该留下任何东西，实际 %v", entries)
	}
}

// 已经装过同名技能时不隐式覆盖，要覆盖也得整份换掉
func TestInstallConflictThenOverwrite(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()
	dir := filepath.Join(userSkillsDir(home), "pdf")

	first := zipOf(t,
		zentry{name: "pdf/SKILL.md", content: "---\nname: pdf\ndescription: 旧的\n---\n旧正文\n"},
		zentry{name: "pdf/old.txt", content: "旧的\n"},
	)
	mustInstall(t, ScopeUser, project, "pdf.zip", first, false)

	second := zipOf(t,
		zentry{name: "pdf/SKILL.md", content: "---\nname: pdf\ndescription: 新的\n---\n新正文\n"},
		zentry{name: "pdf/new.txt", content: "新的\n"},
	)
	if _, _, err := InstallZip(ScopeUser, project, "pdf.zip", second, false); !errors.Is(err, ErrSkillExists) {
		t.Fatalf("同名该报 ErrSkillExists，实际 %v", err)
	}

	s, _ := mustInstall(t, ScopeUser, project, "pdf.zip", second, true)
	if s.Description != "新的" {
		t.Errorf("覆盖后该是新的描述，实际 %q", s.Description)
	}
	mustNotExist(t, filepath.Join(dir, "old.txt")) // 覆盖是整份换掉，不留上一版的残留
	if got := mustRead(t, filepath.Join(dir, "new.txt")); got != "新的\n" {
		t.Errorf("新文件没装上：%q", got)
	}
}

// 条目数和解开后的体积都有上限，超了就当包里不对
func TestInstallCaps(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()

	many := []zentry{{name: "pdf/SKILL.md", content: "---\nname: pdf\n---\n正文\n"}}
	for i := range maxZipEntries {
		many = append(many, zentry{name: "pdf/f" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".txt", content: "x"})
	}
	if _, _, err := InstallZip(ScopeUser, project, "pdf.zip", zipOf(t, many...), false); !errors.Is(err, ErrInvalidZip) {
		t.Errorf("条目太多该被拒，实际 %v", err)
	}

	// 体积上限调小来测：真塞 64MB 进测试太慢
	old := maxUnzipBytes
	maxUnzipBytes = 1024
	t.Cleanup(func() { maxUnzipBytes = old })

	big := zipOf(t, zentry{name: "pdf/SKILL.md", content: "---\nname: pdf\n---\n" + strings.Repeat("a", 4096)})
	if _, _, err := InstallZip(ScopeUser, project, "pdf.zip", big, false); !errors.Is(err, ErrInvalidZip) {
		t.Errorf("解开后太大该被拒，实际 %v", err)
	}
	if entries, err := os.ReadDir(userSkillsDir(home)); err == nil && len(entries) > 0 {
		t.Errorf("失败的安装不该留下残骸，实际 %v", entries)
	}
}

// 包本身有问题（路径不安全之类）时，先校验完整包再动磁盘：老的技能原封不动
func TestInstallRejectsBeforeTouchingExisting(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()
	dir := filepath.Join(userSkillsDir(home), "pdf")

	good := zipOf(t, zentry{name: "pdf/SKILL.md", content: "---\nname: pdf\ndescription: 旧的\n---\n正文\n"})
	mustInstall(t, ScopeUser, project, "pdf.zip", good, false)

	bad := zipOf(t,
		zentry{name: "pdf/SKILL.md", content: "---\nname: pdf\n---\n新的\n"},
		zentry{name: "../evil.txt", content: "x"},
	)
	if _, _, err := InstallZip(ScopeUser, project, "pdf.zip", bad, true); !errors.Is(err, ErrInvalidZip) {
		t.Fatalf("该被判为装不了，实际 %v", err)
	}
	if got := mustRead(t, filepath.Join(dir, "SKILL.md")); !strings.Contains(got, "旧的") {
		t.Errorf("装不了的时候原有技能该原样留着，实际 %q", got)
	}
}

// 解到一半才失败（体积超限）：不留半截目录
func TestInstallFailureCleansUp(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()
	dir := filepath.Join(userSkillsDir(home), "pdf")

	old := maxUnzipBytes
	maxUnzipBytes = 512
	t.Cleanup(func() { maxUnzipBytes = old })

	big := zipOf(t,
		zentry{name: "pdf/SKILL.md", content: "---\nname: pdf\n---\n正文\n"},
		zentry{name: "pdf/big.txt", content: strings.Repeat("a", 4096)},
	)
	if _, _, err := InstallZip(ScopeUser, project, "pdf.zip", big, false); !errors.Is(err, ErrInvalidZip) {
		t.Fatalf("超限该被判为装不了，实际 %v", err)
	}
	// 覆盖是先删旧的再解，所以失败之后老内容也没了——重点是不能留半截
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		t.Errorf("失败之后不该留半截内容，实际 %v", entries)
	}
}
