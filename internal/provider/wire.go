package provider

import (
	"strings"

	"github.com/reuben/go-harness/internal/types"
)

// 这一层只干一件事：harness 的消息模型 <-> OpenAI 兼容协议。
// 两边的形状不一样的地方都在这里摊平，别的地方不用知道协议长什么样。

type request struct {
	Model    string        `json:"model"`
	Messages []wireMessage `json:"messages"`
	Tools    []wireTool    `json:"tools,omitempty"`
	Stream   bool          `json:"stream"`

	// 开了它，用量才在流末尾单独给一帧
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type wireMessage struct {
	Role string `json:"role"`

	// 纯文本时是 string，带图片时是 []wireContentPart。assistant 永远是 string，
	// 见 assistantMessage 里的原因
	Content any `json:"content"`

	// thinking 模式下必须原样回传，少一次就 400，见 wireMessageFrom 的注释
	ReasoningContent string `json:"reasoning_content,omitempty"`

	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *wireImageURL `json:"image_url,omitempty"`
}

type wireImageURL struct {
	URL string `json:"url"`
}

type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters,omitempty"`
	} `json:"function"`
}

func (d *DeepSeek) requestBody(conversation types.Conversation) request {
	messages := make([]wireMessage, 0, len(conversation.Messages)+1)
	if conversation.SystemPrompt != "" {
		messages = append(messages, wireMessage{Role: "system", Content: conversation.SystemPrompt})
	}
	for _, msg := range conversation.Messages {
		messages = append(messages, wireMessageFrom(msg))
	}

	tools := make([]wireTool, 0, len(conversation.Tools))
	for _, tool := range conversation.Tools {
		var wt wireTool
		wt.Type = "function"
		wt.Function.Name = tool.Name
		wt.Function.Description = tool.Desc
		wt.Function.Parameters = tool.Args
		tools = append(tools, wt)
	}

	return request{
		Model:         d.model,
		Messages:      messages,
		Tools:         tools,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	}
}

func wireMessageFrom(msg *types.Message) wireMessage {
	switch msg.Role {
	case types.ToolResultRole:
		return wireMessage{
			Role:       "tool",
			Content:    contentOf(msg.ContentBlocks),
			ToolCallID: msg.ToolCallID,
		}

	case types.AssistantRole:
		return assistantMessage(msg)

	default:
		return wireMessage{Role: string(msg.Role), Content: contentOf(msg.ContentBlocks)}
	}
}

// contentOf 摊平一条消息的内容。没有图片就给字符串——OpenAI 兼容接口对 content
// 数组的支持参差（pi 撞到过模型把数组结构原样抄进输出），纯文本没必要冒这个险。
// 有图片就必须用数组，图片只能以 content part 的形式随消息发出去
func contentOf(blocks []types.ContentBlock) any {
	hasImage := false
	for _, block := range blocks {
		if block.Kind == types.ImageBlock {
			hasImage = true
			break
		}
	}
	if !hasImage {
		return textOf(blocks)
	}

	parts := make([]wireContentPart, 0, len(blocks))
	for _, block := range blocks {
		switch block.Kind {
		case types.TextBlock:
			if block.Text == "" {
				continue
			}
			parts = append(parts, wireContentPart{Type: "text", Text: block.Text})
		case types.ImageBlock:
			parts = append(parts, wireContentPart{
				Type:     "image_url",
				ImageURL: &wireImageURL{URL: "data:" + block.ImageMime + ";base64," + block.ImageData},
			})
		}
	}
	return parts
}

func assistantMessage(msg *types.Message) wireMessage {
	wire := wireMessage{Role: "assistant"}

	var text, thinking []string
	for _, block := range msg.ContentBlocks {
		switch block.Kind {
		case types.TextBlock:
			text = append(text, block.Text)
		case types.ThinkingBlock:
			thinking = append(thinking, block.Thinking)
		case types.ToolBlock:
			var call wireToolCall
			call.ID = block.ToolCallID
			call.Type = "function"
			call.Function.Name = block.ToolName
			call.Function.Arguments = block.Args
			wire.ToolCalls = append(wire.ToolCalls, call)
		}
	}

	// assistant 的 content 永远是字符串。助手消息本来就不带图片，而数组形式有些
	// OpenAI 兼容实现会把结构原样抄进回复里
	wire.Content = strings.Join(text, "")
	// 上一轮模型的推理内容要跟着这一轮回传。服务端只在缓存里留着按 tool_call id 索引的
	// 推理内容，缓存没了就会因为「thinking 模式的 reasoning_content 没回传」直接 400，
	// 所以不能指望它自己找回来
	wire.ReasoningContent = strings.Join(thinking, "")
	return wire
}

func textOf(blocks []types.ContentBlock) string {
	var parts []string
	for _, block := range blocks {
		if block.Kind == types.TextBlock {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "")
}

// 下面几个是流式分片的形状。一帧只带变化的那部分字段，缺的字段是零值

type chunkResponse struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chunkUsage `json:"usage"`
}

type chunkUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptCacheHitToken int `json:"prompt_cache_hit_tokens"`
}

func (u *chunkUsage) usage() *types.Usage {
	return &types.Usage{
		Input:  u.PromptTokens,
		Cached: u.PromptCacheHitToken,
		Output: u.CompletionTokens,
		Total:  u.TotalTokens,
	}
}
