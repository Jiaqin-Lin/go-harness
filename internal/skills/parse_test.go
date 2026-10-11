package skills

import (
	"strings"
	"testing"
)

// frontmatter 的各种写法都得能读出来，读不出来也得退到兜底值而不是报错
func TestParseMeta(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantName string
		wantDesc string
		wantBody string // 期望正文里含这段（空 = 不检查）
	}{
		{
			name:     "完整的 frontmatter",
			in:       "---\nname: pdf-tools\ndescription: 处理 PDF 的提取和合并\n---\n\n# 用法\n先跑 extract.sh\n",
			wantName: "pdf-tools",
			wantDesc: "处理 PDF 的提取和合并",
			wantBody: "先跑 extract.sh",
		},
		{
			name:     "没有 frontmatter 时描述取正文第一句，跳过标题",
			in:       "# pdf-tools\n\n处理 PDF 的提取。\n",
			wantDesc: "处理 PDF 的提取。",
		},
		{
			name:     "整篇只有标题就拿标题当描述",
			in:       "# 只有标题\n",
			wantDesc: "只有标题",
		},
		{
			name:     "没闭合的 --- 不算 frontmatter，整份都当正文",
			in:       "---\nname: x\n这就是正文\n",
			wantDesc: "name: x",
			wantBody: "这就是正文",
		},
		{
			name:     "空 frontmatter",
			in:       "---\n---\n正文第一行\n",
			wantDesc: "正文第一行",
		},
		{
			name:     "双引号里的冒号不截断",
			in:       "---\ndescription: \"提取: 合并\"\n---\n正文\n",
			wantDesc: "提取: 合并",
		},
		{
			name:     "单引号",
			in:       "---\nname: 'a b'\n---\n正文\n",
			wantName: "a b",
			wantDesc: "正文",
		},
		{
			name:     "双引号里的转义",
			in:       "---\ndescription: \"路径是 C:\\\\tmp 里的 \\\"x\\\"\"\n---\n正文\n",
			wantDesc: `路径是 C:\tmp 里的 "x"`,
		},
		{
			name:     "块标量 | 每行都留着（拼回一行时压成空格）",
			in:       "---\ndescription: |\n  第一行\n  第二行\n---\n正文\n",
			wantDesc: "第一行 第二行",
		},
		{
			name:     "块标量 > 折成一行",
			in:       "---\ndescription: >-\n  折成\n  一行\n---\n正文\n",
			wantDesc: "折成 一行",
		},
		{
			name:     "不认识的键忽略，不当错",
			in:       "---\nname: x\nlicense: MIT\nallowed-tools: [bash]\ndescription: d\n---\n正文\n",
			wantName: "x",
			wantDesc: "d",
		},
		{
			name:     "CRLF 的文件",
			in:       "---\r\nname: crlf\r\ndescription: 换行是 CRLF\r\n---\r\n正文\r\n",
			wantName: "crlf",
			wantDesc: "换行是 CRLF",
			wantBody: "正文",
		},
		{
			name:     "带 BOM 的文件",
			in:       "\ufeff---\nname: bom\n---\n正文\n",
			wantName: "bom",
			wantDesc: "正文",
			wantBody: "正文",
		},
		{
			name:     "描述里的连续空白压成一个空格",
			in:       "---\ndescription: 一   二 三\n---\n正文\n",
			wantDesc: "一 二 三",
		},
		{
			name:     "冒号后面没东西就用正文兜底",
			in:       "---\ndescription:\n---\n# 标题\n正文第一句\n",
			wantDesc: "正文第一句",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := parseMeta([]byte(tc.in))
			if m.Name != tc.wantName {
				t.Errorf("name：期望 %q，实际 %q", tc.wantName, m.Name)
			}
			if m.Description != tc.wantDesc {
				t.Errorf("description：期望 %q，实际 %q", tc.wantDesc, m.Description)
			}
			if tc.wantBody != "" && !strings.Contains(m.Body, tc.wantBody) {
				t.Errorf("正文里期望含 %q，实际 %q", tc.wantBody, m.Body)
			}
		})
	}
}

// 裸标量不跨行（只认 | 和 >）：缩进的续行留在 frontmatter 里，不会被拼进值
func TestParseMetaPlainScalarStaysOnOneLine(t *testing.T) {
	m := parseMeta([]byte("---\ndescription: 第一行\n  续行\n---\n正文\n"))
	if m.Description != "第一行" {
		t.Errorf("裸标量不该跨行，实际 %q", m.Description)
	}
}

// 描述再长也只留 maxDescriptionRunes 个字，它是每轮都要进 system prompt 的部分
func TestParseMetaClipsDescription(t *testing.T) {
	long := strings.Repeat("长", 500)
	m := parseMeta([]byte("---\ndescription: " + long + "\n---\n正文\n"))
	if got := len([]rune(m.Description)); got != maxDescriptionRunes+1 { // +1 是省略号
		t.Errorf("期望截到 %d 字，实际 %d", maxDescriptionRunes+1, got)
	}
	if !strings.HasSuffix(m.Description, "…") {
		t.Errorf("截断该带省略号，实际 %q", m.Description)
	}
}
