package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setHome 把主目录指到临时目录，测试不往真实的 ~/.go-harness 里写
func setHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func fileOf(home string) string { return filepath.Join(home, ".go-harness", "settings.json") }

func mustRead(t *testing.T, path string) string {
	t.Helper()
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", path, err)
	}
	return string(buf)
}

// 文件不在（一次都没改过东西，是常态）= 空配置，而且光读不该把文件建出来
func TestReadMissingFile(t *testing.T) {
	home := setHome(t)
	s, err := Read()
	if err != nil {
		t.Fatalf("文件不在不该报错：%v", err)
	}
	if s.Compact.At != 0 || len(s.Skills.DisabledUser) != 0 {
		t.Fatalf("该给空配置，实际 %+v", s)
	}
	if _, err := os.Stat(fileOf(home)); !os.IsNotExist(err) {
		t.Error("光是读不该把文件建出来")
	}
}

// 改一次读一次；再改别的键时，上次改的不能被挤掉
func TestUpdateKeepsOtherKeys(t *testing.T) {
	setHome(t)
	if err := Update(func(s *Settings) { s.Compact.At = 120000 }); err != nil {
		t.Fatal(err)
	}
	if err := Update(func(s *Settings) { s.Skills.DisabledUser = append(s.Skills.DisabledUser, "pdf") }); err != nil {
		t.Fatal(err)
	}

	s, err := Read()
	if err != nil {
		t.Fatal(err)
	}
	if s.Compact.At != 120000 {
		t.Errorf("压缩阈值被挤掉了：%+v", s)
	}
	if len(s.Skills.DisabledUser) != 1 || s.Skills.DisabledUser[0] != "pdf" {
		t.Errorf("技能停用清单不对：%+v", s)
	}
}

// 归零的键要剪干净：文件里只剩真正改过的，「没改过」和「文件不存在」是一回事
func TestUpdatePrunesZeroValues(t *testing.T) {
	home := setHome(t)
	if err := Update(func(s *Settings) { s.Compact.At = 120000 }); err != nil {
		t.Fatal(err)
	}

	buf := mustRead(t, fileOf(home))
	if !strings.Contains(buf, `"at": 120000`) {
		t.Fatalf("该写进文件：%s", buf)
	}
	// 空的 skills 段不该跟着冒出来（omitzero 而不是 omitempty）
	if strings.Contains(buf, "skills") {
		t.Errorf("没动过的段不该写出来：%s", buf)
	}

	if err := Update(func(s *Settings) { s.Compact.At = 0 }); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(mustRead(t, fileOf(home))); got != "{}" {
		t.Errorf("全归零之后该只剩一个空对象，实际 %s", got)
	}
}

// 配置坏了：读要报错（调用方自己决定宽容到什么程度），写要拒绝 —— 不能把用户的文件盖掉
func TestCorruptFileIsNotClobbered(t *testing.T) {
	home := setHome(t)
	path := fileOf(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const broken = "{ 这不是 json"
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Read(); err == nil {
		t.Error("读坏了该报错")
	}
	err := Update(func(s *Settings) { s.Compact.At = 1000 })
	if err == nil {
		t.Fatal("写该拒绝")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("报错里该带上文件路径：%v", err)
	}
	if got := mustRead(t, path); got != broken {
		t.Errorf("坏文件不该被盖掉，实际：%s", got)
	}
}

// 落盘是原子的：不留 tmp，也不能因为写一半让读的人拿到半截
func TestWriteIsAtomic(t *testing.T) {
	home := setHome(t)
	if err := Update(func(s *Settings) { s.Compact.At = 50000 }); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(fileOf(home)))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("写完不该留临时文件：%s", e.Name())
		}
	}
}
