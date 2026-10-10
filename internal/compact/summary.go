package compact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/reuben/go-harness/internal/provider"
	"github.com/reuben/go-harness/internal/types"
)

// toolResultLimit 是序列化时单条工具结果保留的字符数。摘要不需要工具结果的全文，
// 而一次压缩可能要吃掉整个会话，不截断的话摘要请求自己就先超限了
const toolResultLimit = 2000

// Summarize 把 messages 折成一份结构化摘要。想省一次调用没用：
// 摘要是模型的判断，不是能算出来的东西。
//
// prevSummary 非空时走增量更新 —— 上一版摘要里的信息要并进来，不能丢
func Summarize(
	ctx context.Context,
	generate provider.GenerateFunction,
	messages []*types.Message,
	prevSummary string,
	settings Settings,
) (string, error) {
	prompt := buildPrompt(messages, prevSummary)

	// emit 是个空函数：摘要是后台调用，不该往用户那条流里掺事件
	msg := provider.CallLLM(ctx, types.Conversation{
		SystemPrompt: summarizationSystem,
		Messages: []*types.Message{{
			Role:          types.UserRole,
			ContentBlocks: []types.ContentBlock{types.NewTextBlock(prompt)},
		}},
		MaxTokens: settings.ReserveTokens * 8 / 10,
	}, generate, func(types.Event) {})

	switch {
	case msg.StopReason == types.LengthStop:
		// 写了一半的摘要会静默丢掉后半段上下文，宁可不压
		return "", errors.New("摘要撞到了输出上限，没写完")
	case msg.StopReason != types.NormalStop:
		return "", fmt.Errorf("生成摘要失败：%s", msg.ErrorMessage)
	}

	summary := strings.TrimSpace(msg.Text())
	if summary == "" {
		return "", errors.New("摘要生成出来是空的")
	}
	return summary + fileOpsTail(messages), nil
}

func buildPrompt(messages []*types.Message, prevSummary string) string {
	var sb strings.Builder
	sb.WriteString("<conversation>\n")
	sb.WriteString(serialize(messages))
	sb.WriteString("\n</conversation>\n\n")
	if prevSummary != "" {
		sb.WriteString("<previous-summary>\n")
		sb.WriteString(prevSummary)
		sb.WriteString("\n</previous-summary>\n\n")
		sb.WriteString(updatePrompt)
	} else {
		sb.WriteString(summarizationPrompt)
	}
	return sb.String()
}

// serialize 把消息摊成文本再送去摘要，而不是把消息原样接在对话后面 ——
// 原样接的话模型会当成「接着聊」，它是来写摘要的，不是来续写的
func serialize(messages []*types.Message) string {
	var parts []string
	for _, msg := range messages {
		text := msg.Text()
		switch msg.Role {
		case types.UserRole:
			if text != "" {
				parts = append(parts, "[用户] "+text)
			}
		case types.AssistantRole:
			var thinking, calls []string
			for _, block := range msg.ContentBlocks {
				switch block.Kind {
				case types.ThinkingBlock:
					thinking = append(thinking, block.Thinking)
				case types.ToolBlock:
					calls = append(calls, block.ToolName+"("+block.Args+")")
				}
			}
			if len(thinking) > 0 {
				parts = append(parts, "[助手思考] "+strings.Join(thinking, "\n"))
			}
			if text != "" {
				parts = append(parts, "[助手] "+text)
			}
			if len(calls) > 0 {
				parts = append(parts, "[助手工具调用] "+strings.Join(calls, "; "))
			}
		case types.ToolResultRole:
			if text != "" {
				parts = append(parts, "[工具结果] "+truncate(text, toolResultLimit))
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return fmt.Sprintf("%s\n\n[…… 还有 %d 个字符被截断]", string(runes[:limit]), len(runes)-limit)
}

// fileOpsTail 把压缩掉的读写文件列成清单附在摘要后面。工具调用记录一压缩就没了，
// 这两个清单留着，模型才知道自己碰过哪些文件
func fileOpsTail(messages []*types.Message) string {
	read := map[string]bool{}
	modified := map[string]bool{}
	for _, msg := range messages {
		for _, block := range msg.ContentBlocks {
			if block.Kind != types.ToolBlock {
				continue
			}
			var args struct {
				Path string `json:"path"`
			}
			if json.Unmarshal([]byte(block.Args), &args) != nil || args.Path == "" {
				continue
			}
			switch block.ToolName {
			case "read":
				read[args.Path] = true
			case "write", "edit":
				modified[args.Path] = true
			}
		}
	}

	var sections []string
	if files := sortedKeys(remove(read, modified)); len(files) > 0 {
		sections = append(sections, "<read-files>\n"+strings.Join(files, "\n")+"\n</read-files>")
	}
	if files := sortedKeys(modified); len(files) > 0 {
		sections = append(sections, "<modified-files>\n"+strings.Join(files, "\n")+"\n</modified-files>")
	}
	if len(sections) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(sections, "\n\n")
}

// remove 去掉改过的文件：读过又改过的不该出现在「只读过」的清单里
func remove(read, modified map[string]bool) map[string]bool {
	kept := map[string]bool{}
	for path := range read {
		if !modified[path] {
			kept[path] = true
		}
	}
	return kept
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

const summarizationSystem = `你是一个上下文摘要助手。读一段用户和编程助手的对话，按指定格式产出结构化摘要。

不要接着聊，不要回答对话里的任何问题，只输出摘要。`

const summarizationPrompt = `上面是一段需要摘要的对话。产出一份结构化的上下文检查点，供另一个模型接着往下干活。

严格按这个格式：

## 目标
[用户要做什么？涵盖多个任务就都写上]

## 约束与偏好
- [用户提过的约束、偏好、要求]
- [没有就写「无」]

## 进展
### 已完成
- [x] [做完的事]

### 进行中
- [ ] [当前在做的事]

### 卡住了
- [阻碍进展的问题，没有就写「无」]

## 关键决定
- **[决定]**：[为什么这么做]

## 下一步
1. [接下来做什么，按顺序]

## 关键上下文
- [继续干活需要的资料、例子、引用]
- [没有就写「无」]

每节保持简短。文件路径、函数名、报错原文必须原样保留。`

const updatePrompt = `上面是新的对话内容，把它们并进 <previous-summary> 里已有的摘要。规则：

- 已有信息一条都不能丢
- 补充新的进展、决定、上下文
- 「进行中」里做完的挪进「已完成」
- 「下一步」按当前进度重写
- 文件路径、函数名、报错原文原样保留
- 确实不再相关的可以删

严格按这个格式：

## 目标
[保留原有目标，任务扩展了就补上]

## 约束与偏好
- [保留原有的，补充新发现的]

## 进展
### 已完成
- [x] [原有的已完成项 + 新完成的]

### 进行中
- [ ] [按当前进度更新]

### 卡住了
- [当前的阻碍，解决了就删掉]

## 关键决定
- **[决定]**：[为什么]（保留已有的，补充新的）

## 下一步
1. [按当前状态更新]

## 关键上下文
- [保留重要的，需要就补新的]

每节保持简短。文件路径、函数名、报错原文必须原样保留。`
