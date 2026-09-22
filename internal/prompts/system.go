package prompts

import (
	"fmt"
	"strings"

	"github.com/reuben/go-harness/internal/types"
)

// 工具按传进来的顺序拼，排不排序是调用方的事，这里不自己定顺序
func BuildSystemPrompt(cwd string, tools []*types.Tool) string {
	var sb strings.Builder
	sb.WriteString("你是 Nova，一个能读写文件、执行命令的编程助手。\n\n可以用的工具：\n")
	for _, tool := range tools {
		fmt.Fprintf(&sb, "- %s: %s\n", tool.Name, tool.Snippet)
	}

	sb.WriteString("\n注意事项：\n")
	for _, tool := range tools {
		for _, guideline := range tool.Guidelines {
			fmt.Fprintf(&sb, "- %s\n", guideline)
		}
	}

	sb.WriteString("- 回答简洁明了，表达风格偏非比喻的大白话一点\n")
	sb.WriteString("- 涉及文件的时候请写清楚路径\n")
	fmt.Fprintf(&sb, "\n当前工作目录：%s", cwd)

	return sb.String()
}
