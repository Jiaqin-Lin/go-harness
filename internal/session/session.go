package session

import (
	"errors"
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

// LeafID 是当前叶子。web 端不依赖它（那边每个请求都自带 parentId），
// 命令行续跑（ContinueRecent + Messages）才用
func (s *Session) LeafID() string { return s.leafID }

// Entries 按写入顺序返回全部 entry。树是调用方按 ParentID 自己组的 ——
// 图要的是整棵树，历史要的是其中一条路径
func (s *Session) Entries() []*Entry { return s.entries }

func (s *Session) Entry(id string) *Entry { return s.byId[id] }

// Append 把一条消息挂到当前叶子下面并落盘，返回 entry id。
// 调用方按轮为单位调用（core.Run 返回之后），所以文件里永远不会出现半轮
func (s *Session) Append(msg *types.Message) (string, error) {
	return s.AppendAfter(s.leafID, msg)
}

// AppendAfter 把消息挂到指定父节点下面，返回 entry id。分叉靠的就是它：
// 同一个父节点挂多个子节点就是多条支路，历史一条不动、也不用复制文件。
// 空 parentID = 挂成根，只允许用在还没有任何 entry 的新会话上 —— 允许往中间
// 挂空的会多出第二个根，图布局和「根到某节点」两个假设会一起破
func (s *Session) AppendAfter(parentID string, msg *types.Message) (string, error) {
	if parentID == "" {
		if len(s.entries) > 0 {
			return "", errors.New("会话已经有内容了，新消息得挂在某个 entry 下面")
		}
	} else if s.byId[parentID] == nil {
		// 挂到不存在的节点上会被回溯静默截断，用户看到的是「我的分叉不见了」，
		// 所以在落盘之前就拦住
		return "", fmt.Errorf("父节点 %s 不在这个会话里", parentID)
	}

	entry := &Entry{Type: messageType, ID: newID(s.byId), Timestamp: timestampNow(), Message: msg}
	if parentID != "" {
		parent := parentID
		entry.ParentID = &parent
	}

	s.entries = append(s.entries, entry)
	s.byId[entry.ID] = entry
	previousLeaf := s.leafID
	s.leafID = entry.ID

	// 写盘失败就把刚挂上的这条撤下来：内存里的树必须跟文件一致，
	// 不一致的话之后每条都会挂在一条文件里并不存在的父节点上
	if err := s.persist(entry); err != nil {
		s.entries = s.entries[:len(s.entries)-1]
		delete(s.byId, entry.ID)
		s.leafID = previousLeaf
		return "", err
	}
	return entry.ID, nil
}

// Messages 是给模型看的历史：从当前叶子沿 parentId 回溯到根
func (s *Session) Messages() []*types.Message {
	return messagesOf(s.pathTo(s.leafID))
}

// MessagesAt 是从根到 entryID 这条路径上的消息，也就是「点这个 entry 继续
// 对话」时该发给模型的历史 —— 旁边的支路在树上，但不在路径里。
//
// entryID 不在树上时返回空（pathTo 遇到不认识的节点就停）。调用方得先确认
// 这个节点存在，否则模型会拿到一份「没有任何历史」的请求白跑一轮
func (s *Session) MessagesAt(entryID string) []*types.Message {
	return messagesOf(s.pathTo(entryID))
}

func messagesOf(entries []*Entry) []*types.Message {
	var messages []*types.Message
	for _, entry := range entries {
		if entry.Message != nil {
			messages = append(messages, entry.Message)
		}
	}
	return messages
}

// pathTo 沿 parentId 从 entryID 回溯到根再反转。树只在这里摊平成一条路径，
// 其他地方都当线性历史用
func (s *Session) pathTo(entryID string) []*Entry {
	var path []*Entry
	for id := entryID; id != ""; {
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
	s.path = path

	// 文件到这一刻才建，header 跟攒下的 entry 一起写；之后每轮只追加新的
	err = writeLine(f, Header{Type: headerType, Version: version, ID: s.id, Timestamp: s.created, Cwd: s.cwd})
	for _, e := range s.entries {
		if err == nil {
			err = writeLine(f, e)
		}
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		// 半截文件比没有文件更糟：它会被当成一个正常会话读进来，而 flushed
		// 还是 false，下一次 Append 又在同一个路径上撞 O_EXCL，永久失败。
		// 删掉它，让下一次从干净的路径重来
		os.Remove(path)
		s.path = ""
		return err
	}

	s.flushed = true
	return nil
}
