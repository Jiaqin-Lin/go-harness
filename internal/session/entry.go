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
	headerType  = "session"
	messageType = "message"
	version     = 1
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
// 文件里的 message 就是 harness 自己的结构
type Entry struct {
	Type      string         `json:"type"`
	ID        string         `json:"id"`
	ParentID  *string        `json:"parentId"`
	Timestamp string         `json:"timestamp"`
	Message   *types.Message `json:"message,omitempty"`
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

func randomHex(n int) string {
	buf := make([]byte, n)
	// 拿不到随机数就没法保证 id 不重，这是环境坏了，不是能接着跑的错误
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("读随机数失败：%v", err))
	}
	return hex.EncodeToString(buf)
}
