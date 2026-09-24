package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/reuben/go-harness/internal/types"
)

// maxSSELine 给 SSE 单行留的上限。一帧一个 delta 通常只有几十字节，
// 但 bufio.Scanner 默认 64KB 撞上限会直接报错停下，留宽一点
const maxSSELine = 1 << 20

// DeepSeek 走 OpenAI 兼容的 /chat/completions。
// 只用 deepseek 一家，所以不做 provider 抽象
type DeepSeek struct {
	apiKey  string
	baseURL string
	model   string
	http    *http.Client
}

func NewDeepSeek(apiKey, baseURL, model string) *DeepSeek {
	return &DeepSeek{
		apiKey:  apiKey,
		baseURL: strings.TrimSuffix(baseURL, "/"),
		model:   model,
		// 不设 Timeout：一次调用可能推理很久，掐时间交给 ctx
		http: &http.Client{},
	}
}

// Generate 发起一次流式调用，把 SSE 拆成增量事件发出来。
// 不管成功失败，最后一定有一个 done 或者 error 事件收尾，channel 到那时才关。
func (d *DeepSeek) Generate(ctx context.Context, conversation types.Conversation) <-chan types.AssistantEvent {
	events := make(chan types.AssistantEvent)

	go func() {
		defer close(events)
		emit := func(ev types.AssistantEvent) { events <- ev }

		if err := d.stream(ctx, conversation, emit); err != nil {
			emit(errorEvent(ctx, err))
		}
	}()

	return events
}

// stream 一次调用的全过程。返回非 nil 表示这次调用没能正常跑完，
// 已经发出去的事件不用回收，调用方补一个 error 事件收尾就行。
func (d *DeepSeek) stream(ctx context.Context, conversation types.Conversation, emit func(types.AssistantEvent)) error {
	body, err := json.Marshal(d.requestBody(conversation))
	if err != nil {
		return fmt.Errorf("拼请求体失败：%w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("建请求失败：%w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+d.apiKey)

	resp, err := d.http.Do(req)
	if err != nil {
		return fmt.Errorf("调模型失败：%w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// 报错时返回的是普通 JSON，不是 SSE，读一小段就够看出问题
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("模型返回 %d：%s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}

	return d.readStream(resp.Body, emit)
}

// readStream 读 SSE。每行形如 `data: {...}`，最后一帧是 `data: [DONE]`
func (d *DeepSeek) readStream(body io.Reader, emit func(types.AssistantEvent)) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), maxSSELine)

	// 块下标由这里分配：文本和思考各占一个块，工具调用按出现顺序依次往后排
	next := 0
	textIndex, thinkingIndex := -1, -1
	toolIndexes := map[int]int{} // 帧里给的工具下标 -> 我们分配的块下标
	toolBlocks := []int{}        // 同上，但按出现顺序存，收尾时才能按顺序发 end

	finishReason := ""
	usage := (*types.Usage)(nil)

	for scanner.Scan() {
		line := scanner.Text()
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue // 空行、注释、event: 行都不用管
		}
		payload = strings.TrimSpace(payload)
		if payload == "[DONE]" {
			break
		}

		var chunk chunkResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return fmt.Errorf("解析流式分片失败：%w", err)
		}
		if chunk.Usage != nil {
			usage = chunk.Usage.usage()
		}
		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]
		delta := choice.Delta

		if delta.ReasoningContent != "" {
			if thinkingIndex < 0 {
				thinkingIndex, next = next, next+1
			}
			emit(types.AssistantEvent{
				Type:              types.AssistantThinkingDeltaEvent,
				ContentBlockIndex: thinkingIndex,
				Delta:             delta.ReasoningContent,
			})
		}

		if delta.Content != "" {
			if textIndex < 0 {
				textIndex, next = next, next+1
			}
			emit(types.AssistantEvent{
				Type:              types.AssistantTextDeltaEvent,
				ContentBlockIndex: textIndex,
				Delta:             delta.Content,
			})
		}

		for _, call := range delta.ToolCalls {
			index, seen := toolIndexes[call.Index]
			if !seen {
				// 第一次见到这个工具调用，开头那帧带着 id 和名字
				index, next = next, next+1
				toolIndexes[call.Index] = index
				toolBlocks = append(toolBlocks, index)
				// 工具调用前后的文本是两段，后面的文本要另起一个块
				textIndex, thinkingIndex = -1, -1
				emit(types.AssistantEvent{
					Type:              types.AssistantToolCallStartEvent,
					ContentBlockIndex: index,
					Message: &types.Message{
						ToolCallID: call.ID,
						ToolName:   call.Function.Name,
					},
				})
			}
			if call.Function.Arguments != "" {
				emit(types.AssistantEvent{
					Type:              types.AssistantToolCallDeltaEvent,
					ContentBlockIndex: index,
					Delta:             call.Function.Arguments,
				})
			}
		}

		if choice.FinishReason != "" {
			finishReason = choice.FinishReason
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读流失败：%w", err)
	}

	// 参数流完了，把开着的工具调用逐个收掉，UI 靠它把「正在生成参数」停掉
	for _, index := range toolBlocks {
		emit(types.AssistantEvent{
			Type:              types.AssistantToolCallEndEvent,
			ContentBlockIndex: index,
		})
	}

	emit(types.AssistantEvent{Type: types.AssistantDoneEvent, Message: doneMessage(finishReason, usage)})
	return nil
}

func errorEvent(ctx context.Context, err error) types.AssistantEvent {
	msg := &types.Message{
		Role:         types.AssistantRole,
		StopReason:   types.ErrorStop,
		ErrorMessage: err.Error(),
	}

	// ctx 已经结束说明是上层主动叫停的（用户取消、超时），不是模型出错。
	// 落成 AbortedStop 让 loop 走取消分支，也别摆一条 context canceled 给用户看
	if ctx.Err() != nil {
		msg.StopReason = types.AbortedStop
		msg.ErrorMessage = ""
	}

	return types.AssistantEvent{Type: types.AssistantErrorEvent, Message: msg}
}

// doneMessage 把模型给的结束原因翻译成 harness 的 StopReason。
// 空的原因说明流没跑到头就断了，认不出的原因照原样报出来，不硬猜
func doneMessage(finishReason string, usage *types.Usage) *types.Message {
	msg := &types.Message{Role: types.AssistantRole, Usage: usage}

	switch finishReason {
	case "stop":
		msg.StopReason = types.NormalStop
	case "tool_calls":
		msg.StopReason = types.ToolUserStop
	case "length":
		msg.StopReason = types.LengthStop
	case "":
		msg.StopReason = types.ErrorStop
		msg.ErrorMessage = "流在给出结束原因之前就断了"
	default:
		msg.StopReason = types.ErrorStop
		msg.ErrorMessage = fmt.Sprintf("模型给了不认识的结束原因 %q", finishReason)
	}

	return msg
}
