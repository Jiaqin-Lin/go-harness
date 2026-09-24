package session

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/reuben/go-harness/internal/types"
)

// Session 是一次对话的持久化载体。内存里始终留着全量 entry（一棵树），
// 磁盘上只追加：第一次落盘把 header 和攒下的 entry 一起写出，之后每轮往后追加
type Session struct {
	id      string
	cwd     string
	dir     string
	created string
	path    string // 落盘前是空串
	entries []*Entry
	byId    map[string]*Entry
	leafID  string
	flushed bool
}

func (s *Session) ID() string  { return s.id }
func (s *Session) Cwd() string { return s.cwd }

// Path 是会话文件的路径；还没落盘时返回空串
func (s *Session) Path() string { return s.path }

// Append 把一条消息挂到当前叶子下面并落盘，返回 entry id。
// 调用方按轮为单位调用（core.Run 返回之后），所以文件里永远不会出现半轮
func (s *Session) Append(msg *types.Message) (string, error) {
	entry := &Entry{Type: messageType, ID: newID(s.byId), Timestamp: timestampNow(), Message: msg}
	if s.leafID != "" {
		parent := s.leafID
		entry.ParentID = &parent
	}

	s.entries = append(s.entries, entry)
	s.byId[entry.ID] = entry
	s.leafID = entry.ID

	return entry.ID, s.persist(entry)
}

// Messages 是给模型看的历史：从叶子沿 parentId 回溯到根。
// 树只在 leafPath 里摊平一次，其他地方都当线性历史用
func (s *Session) Messages() []*types.Message {
	var messages []*types.Message
	for _, entry := range s.leafPath() {
		if entry.Message != nil {
			messages = append(messages, entry.Message)
		}
	}
	return messages
}

func (s *Session) leafPath() []*Entry {
	var path []*Entry
	for id := s.leafID; id != ""; {
		entry := s.byId[id]
		if entry == nil {
			break // 父节点不在（文件被截断），能回溯到哪算哪
		}
		path = append(path, entry)
		if entry.ParentID == nil {
			break
		}
		id = *entry.ParentID
	}
	slices.Reverse(path)
	return path
}

// persist 落盘。文件在第一次落盘时才建，所以「打开就退出」的会话在磁盘上不留东西
func (s *Session) persist(entry *Entry) error {
	if s.flushed {
		return appendLine(s.path, entry)
	}

	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("建会话目录失败：%w", err)
	}
	path := filepath.Join(s.dir, fileName(s.created, s.id))
	// O_EXCL：同名文件已经在了说明 id 撞上了，宁可直接报错也不要往别人的会话里写
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("建会话文件失败：%w", err)
	}
	defer f.Close()
	s.path = path

	// 文件到这一刻才建，header 跟攒下的 entry 一起写；之后每轮只追加新的
	if err := writeLine(f, Header{Type: headerType, Version: version, ID: s.id, Timestamp: s.created, Cwd: s.cwd}); err != nil {
		return err
	}
	for _, e := range s.entries {
		if err := writeLine(f, e); err != nil {
			return err
		}
	}

	s.flushed = true
	return nil
}
