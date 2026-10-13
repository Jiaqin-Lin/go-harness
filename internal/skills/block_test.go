package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 块里该有名字、位置、技能目录和正文；剥掉之后剩下的正好是用户那句话
func TestBlockAndStripRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := Skill{Name: "pdf-tools", Dir: dir, Path: filepath.Join(dir, "SKILL.md")}
	writeFile(t, s.Path, "---\nname: pdf-tools\n---\n\n第一步先跑 extract.sh\n")

	block := Block(s)
	for _, want := range []string{`<skill name="pdf-tools"`, s.Path, dir, "第一步先跑 extract.sh", "</skill>"} {
		if !strings.Contains(block, want) {
			t.Errorf("块里该有 %q：\n%s", want, block)
		}
	}
	if strings.Contains(block, "name: pdf-tools") {
		t.Errorf("frontmatter 不该进块：\n%s", block)
	}

	// 正文和用户那句话拼起来之后（types.TextOf 那种拼法），剥出来的是原话
	if got := StripBlock(block + "把这份 PDF 处理一下"); got != "把这份 PDF 处理一下" {
		t.Errorf("剥出来该是用户那句话，得到 %q", got)
	}
	// 没块的消息原样过
	if got := StripBlock("普通的一句话"); got != "普通的一句话" {
		t.Errorf("没有块的消息该原样给，得到 %q", got)
	}
}

// 正文读不出来也只是少一段材料：块还在，里面说明一句
func TestBlockUnreadableBody(t *testing.T) {
	s := Skill{Name: "gone", Dir: "/skills/gone", Path: "/skills/gone/SKILL.md"}

	block := Block(s)
	if !strings.Contains(block, "技能正文读不出来") {
		t.Errorf("读不出来该在块里说明：\n%s", block)
	}
	if !strings.Contains(block, `name="gone"`) || !strings.Contains(block, "</skill>") {
		t.Errorf("块本身还得是完整的：\n%s", block)
	}
}

// 块是断的（写了一半的文件）时原样给：宁可显示得难看，也不要把用户的正文吞掉
func TestStripBrokenBlock(t *testing.T) {
	broken := "<skill name=\"x\" location=\"/x\">\n正文没写完"
	if got := StripBlock(broken); got != broken {
		t.Errorf("断块该原样给，得到 %q", got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(strings.TrimSuffix(path, "/SKILL.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
