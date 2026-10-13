package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reuben/go-harness/internal/skills"
	"github.com/reuben/go-harness/internal/types"
)

func testTool() *types.Tool {
	return &types.Tool{
		Name:       "read",
		Snippet:    "读文件",
		Guidelines: []string{"路径要写清楚。"},
	}
}

// 写一份技能文件，返回能直接塞进 Input 的 Skill
func skillFixture(t *testing.T, name, front, body string) skills.Skill {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SKILL.md")
	content := front + "\n\n" + body
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return skills.Skill{Name: name, Description: "技能说明", Path: path, Dir: dir, Scope: skills.ScopeUser, Enabled: true}
}

// 一般的技能只给名字、描述和位置——正文留给模型自己去 read
func TestPromptListsSkillsWithoutBody(t *testing.T) {
	s := skillFixture(t, "pdf-tools", "---\nname: pdf-tools\ndescription: 处理 PDF\n---\n", "正文里才有的句子")
	prompt := BuildSystemPrompt(Input{Cwd: "/proj", Tools: []*types.Tool{testTool()}, Skills: []skills.Skill{s}})

	if !strings.Contains(prompt, "- pdf-tools: 技能说明（"+s.Path+"）") {
		t.Errorf("技能该按「名字: 描述（路径）」列出来：\n%s", prompt)
	}
	if strings.Contains(prompt, "正文里才有的句子") {
		t.Errorf("没强制的时候不该把正文贴进 prompt：\n%s", prompt)
	}
}

// 没有技能、没有 AGENTS.md 时不多拼任何段落
func TestPromptHasNoSkillSectionWhenEmpty(t *testing.T) {
	prompt := BuildSystemPrompt(Input{Cwd: "/proj", Tools: []*types.Tool{testTool()}})
	if strings.Contains(prompt, "技能") || strings.Contains(prompt, "AGENTS.md") {
		t.Errorf("没技能没约定时不该有这两段：\n%s", prompt)
	}
	if !strings.Contains(prompt, "当前工作目录：/proj") {
		t.Errorf("原有的内容不能少：\n%s", prompt)
	}
}

func TestPromptInjectsAgentsMD(t *testing.T) {
	prompt := BuildSystemPrompt(Input{Cwd: "/proj", Tools: []*types.Tool{testTool()}, AgentsMD: "提交前跑 go test"})
	if !strings.Contains(prompt, "本项目的约定") || !strings.Contains(prompt, "提交前跑 go test") {
		t.Errorf("AGENTS.md 该整份进 prompt：\n%s", prompt)
	}
}

func TestReadAgentsMD(t *testing.T) {
	dir := t.TempDir()
	if got := ReadAgentsMD(dir); got != "" {
		t.Errorf("没有 AGENTS.md 该给空串，实际 %q", got)
	}

	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte("\n  提交前跑 go test\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadAgentsMD(dir); got != "提交前跑 go test" {
		t.Errorf("首尾空白该剪掉，实际 %q", got)
	}

	// 只有空白的文件也算没有：省得拼出一个空段落
	if err := os.WriteFile(path, []byte("   \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadAgentsMD(dir); got != "" {
		t.Errorf("空白文件该当没有，实际 %q", got)
	}
}
