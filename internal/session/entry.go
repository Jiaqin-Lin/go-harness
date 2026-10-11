// 会话文件格式跟 pi 对齐：一行一个 JSON，第一行是 header，之后每行是一个 entry。
// entry 自己带 id / parentId，所以历史是一棵树，分支就是「从旧节点再挂一个子节点」。
package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/reuben/go-harness/internal/types"
)

const (
	headerType     = "session"
	messageType    = "message"
	compactionType = "compaction"
	version        = 1
)

// Header 是每个会话文件的第一行
type Header struct {
	Type      string `json:"type"`
	Version   int    `json:"version"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
}

// Entry 是树上的一个节点。时间戳记在 entry 上，消息本身一个字段都不加 ——
// 文件里的 message 就是 harness 自己的结构。
//
// compaction 类型的 entry 没有 message，正文在 summary 里：它说「firstKeptEntryID
// 之前的历史已经折叠成这段摘要」。被折叠的 entry 一条不少地留在文件里，只是不再发给
// 模型 —— 压缩是追加，不是删除，见 Session.MessagesAt
type Entry struct {
	Type      string         `json:"type"`
	ID        string         `json:"id"`
	ParentID  *string        `json:"parentId"`
	Timestamp string         `json:"timestamp"`
	Message   *types.Message `json:"message,omitempty"`

	// Skill 是这一轮用户指定必须用的技能名，只挂在那一轮的用户 entry 上。
	// 它记录的是「发这条时用户选了什么」——重放历史时技能正文不会再贴一遍
	// （那正是「只对下一条消息生效」），界面靠这个字段把角标补回来
	Skill string `json:"skill,omitempty"`

	Summary          string `json:"summary,omitempty"`
	FirstKeptEntryID string `json:"firstKeptEntryId,omitempty"`
	TokensBefore     int    `json:"tokensBefore,omitempty"`
}

// timestampNow 是会话里所有时间戳的格式：ISO8601 带毫秒，UTC
func timestampNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// newID 摇一个 8 位 hex，撞了就重摇。100 次都撞上只能退到 32 位，
// 真发生时说明 id 空间被填满了，不是随机数的问题
func newID(taken map[string]*Entry) string {
	for range 100 {
		if id := randomHex(4); taken[id] == nil {
			return id
		}
	}
	return randomHex(16)
}

// ValidID 是会话 id 的形状：8~32 位小写 hex。外部传进来的 id 会被拼进文件名
// 和 glob 模式，所以形状得先卡住
func ValidID(id string) bool {
	if len(id) < 8 || len(id) > 32 {
		return false
	}
	for i := range len(id) {
		if c := id[i]; !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

func randomHex(n int) string {
	buf := make([]byte, n)
	// 拿不到随机数就没法保证 id 不重，这是环境坏了，不是能接着跑的错误
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("读随机数失败：%v", err))
	}
	return hex.EncodeToString(buf)
}
