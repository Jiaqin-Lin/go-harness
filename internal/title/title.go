// Package title 是那条异步的标题生成节点：拿会话开头的一问一答喂一次模型，
// 换一个短标题回来。
//
// 它跟对话主链路完全没关系：单独一次调用、单独的 ctx、失败了就当没发生 ——
// 标题没有就没有，侧栏退回显示第一条用户消息。谁都不该等它
package title

import (
	"context"
	"strings"

	"github.com/reuben/go-harness/internal/provider"
	"github.com/reuben/go-harness/internal/skills"
	"github.com/reuben/go-harness/internal/types"
)

const systemPrompt = "你是标题生成器。只输出标题本身：不超过 12 个字，不加引号、不加标点、不加解释。"

// Generate 调一次模型换个标题。失败、超时、模型答歪了都返回空串
func Generate(ctx context.Context, generate provider.GenerateFunction, messages []*types.Message) string {
	if generate == nil {
		return ""
	}

	// 只喂开头一问一答：整条路径又长又贵，概括也用不上。
	// 用户点过技能的那条消息带着几十 KB 的技能正文（见 skills.Block），
	// 不剥掉的话标题会从技能正文里长出来，跟这一轮问的什么没关系
	var parts []string
	for _, msg := range messages {
		if msg.Role != types.UserRole && msg.Role != types.AssistantRole {
			continue
		}
		if text := strings.TrimSpace(skills.StripBlock(msg.Text())); text != "" {
			parts = append(parts, string(msg.Role)+"："+head(text, 400))
		}
		if len(parts) == 2 {
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}

	conversation := types.Conversation{
		SystemPrompt: systemPrompt,
		Messages: []*types.Message{{
			Role:          types.UserRole,
			ContentBlocks: []types.ContentBlock{types.NewTextBlock(strings.Join(parts, "\n"))},
		}},
	}

	// CallLLM 契约上永不返回 nil：出错也是一条 ErrorStop 的消息，
	// 所以不能拿到什么就当成标题 —— 错误文案不是标题
	answer := provider.CallLLM(ctx, conversation, generate, func(types.Event) {})
	if answer.StopReason == types.ErrorStop || answer.StopReason == types.AbortedStop {
		return ""
	}
	return clean(answer.Text())
}

// clean 把模型的输出收拾成一行：去掉引号、只留第一行、超长截断。
// 模型经常不听话地带上「标题：」前缀，或者用引号把标题包起来
func clean(raw string) string {
	text := strings.TrimSpace(raw)
	text = strings.Trim(text, quoteMarks)
	for _, prefix := range []string{"标题：", "标题:", "Title:", "title:"} {
		text = strings.TrimPrefix(text, prefix)
	}
	if cut := strings.IndexByte(text, '\n'); cut >= 0 {
		text = text[:cut]
	}
	text = strings.TrimSpace(strings.Trim(text, quoteMarks))
	return head(text, 24)
}

const quoteMarks = "\"'「」『』《》"

// head 按 rune 截断，中文才不会被砍成半个字
func head(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return strings.TrimSpace(string(runes[:limit])) + "…"
}
