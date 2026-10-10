package server

import (
	"encoding/base64"
	"strings"

	"github.com/reuben/go-harness/internal/session"
	"github.com/reuben/go-harness/internal/types"
)

// 这里是 API 的 JSON 形状，跟会话文件的形状是两码事：文件是磁盘契约
// （internal/session 说了算），这边是给前端的。隔一层，磁盘格式才不会被
// 界面需求牵着走 —— 图片就是例子：文件里存 base64，接口只给形状

type entryDTO struct {
	ID        string      `json:"id"`
	ParentID  string      `json:"parentId,omitempty"` // 空 = 根
	Timestamp string      `json:"timestamp"`
	Message   *messageDTO `json:"message,omitempty"`
}

type messageDTO struct {
	Role         string       `json:"role"`
	Blocks       []blockDTO   `json:"blocks,omitempty"`
	StopReason   string       `json:"stopReason,omitempty"`
	ErrorMessage string       `json:"errorMessage,omitempty"`
	Usage        *types.Usage `json:"usage,omitempty"`
	ToolCallID   string       `json:"toolCallId,omitempty"`
	ToolName     string       `json:"toolName,omitempty"`
	IsError      bool         `json:"isError,omitempty"`
}

// blockDTO 里图片只给形状不给内容：一张 2000x2000 的图 base64 有几百 KB，
// 整棵树的接口把它带上，首屏就是几 MB
type blockDTO struct {
	Kind       string `json:"kind"`
	Text       string `json:"text,omitempty"`
	Thinking   string `json:"thinking,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
	Args       string `json:"args,omitempty"`
	ImageMime  string `json:"imageMime,omitempty"`
	ImageBytes int    `json:"imageBytes,omitempty"`
}

func toEntryDTO(entry *session.Entry) entryDTO {
	dto := entryDTO{ID: entry.ID, Timestamp: entry.Timestamp}
	if entry.ParentID != nil {
		dto.ParentID = *entry.ParentID
	}
	dto.Message = toMessageDTO(entry.Message)
	return dto
}

func toMessageDTO(msg *types.Message) *messageDTO {
	if msg == nil {
		return nil
	}
	dto := &messageDTO{
		Role:         string(msg.Role),
		StopReason:   string(msg.StopReason),
		ErrorMessage: msg.ErrorMessage,
		Usage:        msg.Usage,
		ToolCallID:   msg.ToolCallID,
		ToolName:     msg.ToolName,
		IsError:      msg.IsError,
	}
	for _, block := range msg.ContentBlocks {
		dto.Blocks = append(dto.Blocks, toBlockDTO(block))
	}
	return dto
}

func toBlockDTO(block types.ContentBlock) blockDTO {
	return blockDTO{
		Kind:       string(block.Kind),
		Text:       block.Text,
		Thinking:   block.Thinking,
		ToolCallID: block.ToolCallID,
		ToolName:   block.ToolName,
		Args:       block.Args,
		ImageMime:  block.ImageMime,
		ImageBytes: base64.StdEncoding.DecodedLen(len(block.ImageData)),
	}
}

func toBlocksDTO(result *types.ToolResult) []blockDTO {
	if result == nil {
		return nil
	}
	blocks := make([]blockDTO, 0, len(result.ContentBlock))
	for _, block := range result.ContentBlock {
		blocks = append(blocks, toBlockDTO(block))
	}
	return blocks
}

// clip 把一段文本压成一行短摘要：换行折成空格、超长截断。
// 按 rune 截，中文才不会被砍成半个字
func clip(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
