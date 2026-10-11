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

	// Skill 是发这条时用户选的技能名。界面拿它在气泡旁边标一下 ——
	// 技能正文没落盘，重画历史时只有这个字段能说清「这轮用了什么」
	Skill string `json:"skill,omitempty"`

	// Compaction 非空说明这个节点是压缩点：它之前的历史已经折进 Summary，
	// 图上前端把它画成一个能展开的摘要节点
	Compaction *compactionDTO `json:"compaction,omitempty"`
}

type compactionDTO struct {
	Summary      string `json:"summary"`
	TokensBefore int    `json:"tokensBefore,omitempty"`

	// FirstKeptEntryID 是保留段的第一条：比它早的 entry 已经折进 Summary。
	// 前端靠它算「哪些 entry 不再进上下文」，规则跟 Session.contextPath 一致
	FirstKeptEntryID string `json:"firstKeptEntryId,omitempty"`
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
	dto := entryDTO{ID: entry.ID, Timestamp: entry.Timestamp, Skill: entry.Skill}
	if entry.ParentID != nil {
		dto.ParentID = *entry.ParentID
	}
	dto.Message = toMessageDTO(entry.Message)
	if entry.Summary != "" {
		dto.Compaction = &compactionDTO{
			Summary:          entry.Summary,
			TokensBefore:     entry.TokensBefore,
			FirstKeptEntryID: entry.FirstKeptEntryID,
		}
	}
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
