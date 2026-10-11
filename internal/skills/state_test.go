package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustRead(t *testing.T, path string) string {
	t.Helper()
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", path, err)
	}
	return string(buf)
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Errorf("%s 不该存在", path)
	}
}

// 开关的整个往返：没记录 = 启用，关掉写进文件，再打开就把名字删干净
func TestSetEnabledRoundTrip(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()
	path := filepath.Join(home, ".go-harness", "settings.json")

	if !Enabled(ScopeUser, project, "pdf") {
		t.Fatal("没记录过的技能该是启用的")
	}
	mustNotExist(t, path) // 光读不该把文件建出来

	if err := SetEnabled(ScopeUser, project, "pdf", false); err != nil {
		t.Fatalf("停用失败：%v", err)
	}
	if Enabled(ScopeUser, project, "pdf") {
		t.Error("停用之后该是关的")
	}
	if got := mustRead(t, path); !strings.Contains(got, `"disabledUser"`) || !strings.Contains(got, `"pdf"`) {
		t.Errorf("文件里该记下这份停用：%s", got)
	}

	// 重复停用不该堆第二份
	if err := SetEnabled(ScopeUser, project, "pdf", false); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(mustRead(t, path), `"pdf"`); got != 1 {
		t.Errorf("同一个名字该只有一条，实际 %d 条", got)
	}

	if err := SetEnabled(ScopeUser, project, "pdf", true); err != nil {
		t.Fatalf("启用失败：%v", err)
	}
	if !Enabled(ScopeUser, project, "pdf") {
		t.Error("启用之后该回来")
	}
	// 剪干净之后文件里不该剩空壳
	if got := mustRead(t, path); strings.Contains(got, "pdf") || strings.Contains(got, "disabledUser") {
		t.Errorf("重新启用后该什么都不剩：%s", got)
	}
	mustNotExist(t, path+".tmp")
}

// 项目级是按 cwd 分开记的，而且 cwd 要 Clean 过——/x/y/ 和 /x/y 是同一个项目
func TestSetEnabledProjectKeyedByCwd(t *testing.T) {
	home := setHome(t)
	path := filepath.Join(home, ".go-harness", "settings.json")

	if err := SetEnabled(ScopeProject, "/proj/one/", "report", false); err != nil {
		t.Fatal(err)
	}
	if Enabled(ScopeProject, "/proj/one", "report") != false {
		t.Error("Clean 过的同一个 cwd 该看到同一个状态")
	}
	if !Enabled(ScopeProject, "/proj/two", "report") {
		t.Error("另一个项目不受影响")
	}
	if !Enabled(ScopeUser, "/proj/one", "report") {
		t.Error("项目级的停用不该影响用户级")
	}
	if got := mustRead(t, path); !strings.Contains(got, `"/proj/one"`) {
		t.Errorf("键该是 Clean 过的绝对路径：%s", got)
	}
}

// 文件内容就是磁盘上的契约，改了这个格式等于改配置文件格式
func TestSettingsJSONShape(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()
	if err := SetEnabled(ScopeUser, project, "pdf", false); err != nil {
		t.Fatal(err)
	}
	if err := SetEnabled(ScopeProject, project, "report", false); err != nil {
		t.Fatal(err)
	}

	want := `{
  "skills": {
    "disabledUser": [
      "pdf"
    ],
    "disabledProject": {
      ` + quoteJSON(t, project) + `: [
        "report"
      ]
    }
  }
}
`
	path := filepath.Join(home, ".go-harness", "settings.json")
	if got := mustRead(t, path); got != want {
		t.Errorf("配置文件形状不对：\n实际：\n%s\n期望：\n%s", got, want)
	}
}

func quoteJSON(t *testing.T, s string) string {
	t.Helper()
	buf, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(buf)
}

// 配置坏了：读的时候当「一个都没停用」（否则一次手滑就让会话聊不下去），
// 写的时候报错并带上路径（否则会把用户已有的东西盖掉）
func TestCorruptSettings(t *testing.T) {
	home := setHome(t)
	project := t.TempDir()
	path := filepath.Join(home, ".go-harness", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{ 这不是 json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !Enabled(ScopeUser, project, "pdf") {
		t.Error("配置坏了该当全启用")
	}
	if eff := Effective(project); len(eff) != 0 {
		t.Errorf("空目录里就没有技能，实际 %v", names(eff))
	}

	err := SetEnabled(ScopeUser, project, "pdf", false)
	if err == nil {
		t.Fatal("写的时候该报错")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("报错里该带上文件路径，实际：%v", err)
	}
	if got := mustRead(t, path); got != "{ 这不是 json" {
		t.Errorf("坏文件不该被盖掉，实际：%s", got)
	}
}

// 目录建不出来（比如中间是文件）时报错，而不是装作写成功了
func TestSetEnabledMkdirFailure(t *testing.T) {
	home := setHome(t)
	if err := os.WriteFile(filepath.Join(home, ".go-harness"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SetEnabled(ScopeUser, t.TempDir(), "pdf", false); err == nil {
		t.Error("建目录失败该报错")
	}
}
