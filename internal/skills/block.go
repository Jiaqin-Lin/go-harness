package skills

import (
	"fmt"
	"strings"
)

// 技能正文进对话的形状：一个 <skill> 块。它跟着用户那句话同一条消息走，
// 不进 system prompt —— system prompt 是缓存前缀，贴在那儿等于点一次技能
// 就把整段历史从那儿往后全部作废（正文还会一次不落地重传）。
//
// 拼在这儿、拆也在这些消费方：服务端拼（server/chat.go），界面渲染、
// 侧栏预览、标题生成都要先拆掉再拿文本。格式只有这一份说明

const (
	blockOpen  = "<skill name="
	blockClose = "</skill>"
)

// Block 把技能正文拼成块。正文读不出来也照给：块里留着名字、目录和一行说明，
// 模型至少知道这一轮被指定了哪个技能
func Block(s Skill) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s%q location=%q>\n", blockOpen, s.Name, s.Path)
	fmt.Fprintf(&sb, "这一轮用户指定了这个技能，按下面的说明做。相对路径按这个目录解析：%s\n\n", s.Dir)

	body, err := s.Body()
	if err != nil {
		fmt.Fprintf(&sb, "（技能正文读不出来：%v）\n", err)
	} else {
		sb.WriteString(body)
		sb.WriteString("\n")
	}
	sb.WriteString(blockClose)
	// 收尾留空行：没有图片时整条消息的文本块是拼起来的（types.TextOf），
	// 不留的话块尾会跟用户那句话黏在一起
	sb.WriteString("\n\n")
	return sb.String()
}

// StripBlock 去掉开头的技能块，只留用户自己说的话。正文几十 KB，原样摆到
// 侧栏预览、图上标签、标题里都没法看
func StripBlock(text string) string {
	if !strings.HasPrefix(text, blockOpen) {
		return text
	}
	cut := strings.Index(text, blockClose)
	if cut < 0 {
		return text // 块是断的（文件写了一半），原样给，别把内容吞了
	}
	return strings.TrimLeft(text[cut+len(blockClose):], "\n")
}
