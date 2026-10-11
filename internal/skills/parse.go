package skills

import (
	"strings"
	"unicode/utf8"
)

const (
	// frontLines 闭合的 --- 只在前这么多行里找
	frontLines = 64

	// maxDescriptionRunes description 每一轮都要进 system prompt，是技能里唯一
	// 常驻上下文的部分，留太长等于白烧 token
	maxDescriptionRunes = 200
)

// meta 是 SKILL.md 里对加载有用的那部分：frontmatter 的 name / description，
// 以及剥掉 frontmatter 之后的正文
type meta struct {
	Name        string
	Description string
	Body        string
}

// parseMeta 不返回 error：frontmatter 写坏了就退化到兜底值（名字用目录名、
// 描述用正文第一句），一个手滑的冒号不该让整条技能不可用
func parseMeta(data []byte) meta {
	text := normalizeLF(strings.TrimPrefix(string(data), "\ufeff"))

	front, body, ok := splitFront(text)
	m := meta{Body: body}
	if ok {
		m.Name, m.Description = readKeys(front)
	}
	if m.Description == "" {
		m.Description = firstLine(m.Body)
	}
	// 描述是要拼进 system prompt 一行的，换行和连续空格都压掉
	m.Description = clipRunes(collapseSpace(m.Description), maxDescriptionRunes)
	return m
}

// splitFront 切出 frontmatter。首行得是 ---，闭合的 --- 要在前 frontLines 行里；
// 开了没关就当没有 —— 正文里的分隔线、表格首行不能被当成 frontmatter 的开头
func splitFront(text string) (front, body string, ok bool) {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", text, false
	}
	limit := min(len(lines), frontLines)
	for i := 1; i < limit; i++ {
		if strings.TrimSpace(lines[i]) != "---" {
			continue
		}
		// 闭合 --- 后面那行空行是排版，不算正文的开头
		return strings.Join(lines[1:i], "\n"), strings.TrimLeft(strings.Join(lines[i+1:], "\n"), "\n"), true
	}
	return "", text, false
}

// readKeys 读 frontmatter 里的 name / description，其它键（license、allowed-tools…）
// 一律忽略——不认识不等于写错了
func readKeys(front string) (name, description string) {
	lines := strings.Split(front, "\n")
	for i := 0; i < len(lines); i++ {
		key, raw, found := strings.Cut(lines[i], ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		if key != "name" && key != "description" {
			continue
		}

		value := strings.TrimSpace(raw)
		if indicator, ok := blockIndicator(value); ok {
			block, next := readBlock(lines, i+1)
			value = unfoldBlock(block, indicator)
			i = next - 1
		} else {
			value = unquote(value)
		}

		if key == "name" {
			name = value
		} else {
			description = value
		}
	}
	return name, description
}

// blockIndicator 认 YAML 的块标量标记：| 和 >（含 |- |+ >- 这类裁剪写法）
func blockIndicator(value string) (byte, bool) {
	if value == "" || (value[0] != '|' && value[0] != '>') {
		return 0, false
	}
	if strings.Trim(value[1:], "+-0123456789") != "" {
		return 0, false
	}
	return value[0], true
}

// readBlock 收块标量的内容行：缩进行和空行都算，遇到第一行顶格的就停。
// 返回内容行和下一行的下标
func readBlock(lines []string, start int) ([]string, int) {
	var block []string
	i := start
	for ; i < len(lines); i++ {
		line := lines[i]
		if line == "" {
			block = append(block, "")
			continue
		}
		if line[0] != ' ' && line[0] != '\t' {
			break
		}
		block = append(block, line)
	}
	// 尾部的空行是下一段正文前的间隔，不算块内容
	for len(block) > 0 && block[len(block)-1] == "" {
		block = block[:len(block)-1]
	}
	return block, i
}

// unfoldBlock 剥掉块内公共缩进再拼起来：| 保留换行，> 折成空格
func unfoldBlock(block []string, indicator byte) string {
	indent := -1
	for _, line := range block {
		if strings.TrimSpace(line) == "" {
			continue
		}
		n := len(line) - len(strings.TrimLeft(line, " \t"))
		if indent < 0 || n < indent {
			indent = n
		}
	}
	if indent < 0 {
		indent = 0
	}

	out := make([]string, 0, len(block))
	for _, line := range block {
		if len(line) > indent {
			line = line[indent:]
		}
		out = append(out, line)
	}

	sep := "\n"
	if indicator == '>' {
		sep = " "
	}
	return strings.TrimSpace(strings.Join(out, sep))
}

// unquote 剥掉值两端的引号。双引号里认 \" 和 \\ 两种转义
func unquote(value string) string {
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		return unescapeDouble(value[1 : len(value)-1])
	}
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'")
	}
	return value
}

func unescapeDouble(inner string) string {
	var sb strings.Builder
	sb.Grow(len(inner))
	for i := 0; i < len(inner); i++ {
		if inner[i] == '\\' && i+1 < len(inner) && (inner[i+1] == '"' || inner[i+1] == '\\') {
			sb.WriteByte(inner[i+1])
			i++
			continue
		}
		sb.WriteByte(inner[i])
	}
	return sb.String()
}

// firstLine 取正文第一句有信息量的行。标题先跳过——它是名字不是描述；
// 整篇只有标题才退而求其次用它
func firstLine(body string) string {
	var heading string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if heading == "" {
				heading = strings.TrimSpace(strings.TrimLeft(line, "# \t"))
			}
			continue
		}
		if text := strings.TrimSpace(strings.TrimLeft(line, "-*> \t")); text != "" {
			return text
		}
	}
	return heading
}

// collapseSpace 把连续空白（含换行）压成一个空格
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// clipRunes 按字符数截断，超了加省略号
func clipRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}

// cutBytes 截到不超过 max 字节，且不把一个 utf-8 字符切成两半
func cutBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// 换行统一为 LF \n。frontmatter 的 --- 判定靠它，CRLF 的文件不能漏判
func normalizeLF(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}
