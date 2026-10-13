package prompts

import (
	"fmt"
	"strings"

	"github.com/reuben/go-harness/internal/skills"
	"github.com/reuben/go-harness/internal/types"
)

// Input 是这一轮 system prompt 的全部输入。prompt 每一轮都是重新拼的，
// 调用方把当轮的状态塞进来就行——所以「停用技能下一轮生效」是白来的。
//
// 这里只放「这一轮没变的东西」：它是请求的缓存前缀，塞进每轮会变的内容
// （比如用户强制的技能正文）等于把前缀从那儿起全作废
type Input struct {
	Cwd      string
	Tools    []*types.Tool
	Skills   []skills.Skill // 已经生效的那些：启用的、没被同名盖住的
	AgentsMD string         // <cwd>/AGENTS.md 的内容，空就是没有
}

// 工具按传进来的顺序拼，排不排序是调用方的事，这里不自己定顺序
func BuildSystemPrompt(in Input) string {
	var sb strings.Builder
	sb.WriteString("你是 Nova，一个能读写文件、执行命令的编程助手。\n\n可以用的工具：\n")
	for _, tool := range in.Tools {
		fmt.Fprintf(&sb, "- %s: %s\n", tool.Name, tool.Snippet)
	}

	// 技能只给名字、描述和位置：正文让模型自己 read，用不上的一步都不用走
	if len(in.Skills) > 0 {
		sb.WriteString("\n可以用的技能（要用的时候先 read 它的 SKILL.md，照里面的步骤做）：\n")
		for _, s := range in.Skills {
			fmt.Fprintf(&sb, "- %s: %s（%s）\n", s.Name, s.Description, s.Path)
		}
	}

	if in.AgentsMD != "" {
		sb.WriteString("\n本项目的约定（AGENTS.md，优先按它做）：\n")
		sb.WriteString(in.AgentsMD)
		sb.WriteString("\n")
	}

	sb.WriteString("\n注意事项：\n")
	for _, tool := range in.Tools {
		for _, guideline := range tool.Guidelines {
			fmt.Fprintf(&sb, "- %s\n", guideline)
		}
	}

	sb.WriteString("- 回答简洁明了，表达风格偏非比喻的大白话一点\n")
	sb.WriteString("- 涉及文件的时候请写清楚路径\n")
	fmt.Fprintf(&sb, "\n当前工作目录：%s", in.Cwd)

	return sb.String()
}
