package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Info 是 List 的一项。只看文件第一行的 header，够列表显示时间 / cwd / id
type Info struct {
	Path     string
	ID       string
	Cwd      string
	Created  string
	Modified time.Time
}

// Create 建一个空会话。不碰磁盘 —— 文件要等第一次落盘时才出现
func Create(cwd string) (*Session, error) {
	dir, err := dirFor(cwd)
	if err != nil {
		return nil, err
	}
	return &Session{
		id:      randomHex(16),
		cwd:     cwd,
		dir:     dir,
		created: timestampNow(),
		byId:    map[string]*Entry{},
	}, nil
}

// Open 打开一个已有的会话文件。坏行跳过，不因为一行坏了就打不开整个会话
func Open(path string) (*Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	header, entries, err := readFile(f)
	if err != nil {
		return nil, err
	}
	if header == nil {
		return nil, fmt.Errorf("%s 的第一行不是会话 header", path)
	}

	s := &Session{
		id:      header.ID,
		cwd:     header.Cwd,
		dir:     filepath.Dir(path),
		created: header.Timestamp,
		path:    path,
		entries: entries,
		byId:    make(map[string]*Entry, len(entries)),
		flushed: true,
	}
	for _, entry := range entries {
		s.byId[entry.ID] = entry
	}
	if len(entries) > 0 {
		s.leafID = entries[len(entries)-1].ID
	}
	return s, nil
}

// List 列出这个 cwd 下的会话，最近改动的在前。只读每个文件的第一行，不扫全文 ——
// 首条消息、消息条数那些是选择器 UI 要的，等真有 UI 再说
func List(cwd string) ([]Info, error) {
	dir, err := dirFor(cwd)
	if err != nil {
		return nil, err
	}

	dirents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // 目录还没建过 = 一个会话都没有
	}
	if err != nil {
		return nil, err
	}

	var infos []Info
	for _, dirent := range dirents {
		name := dirent.Name()
		if dirent.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		if info, ok := readInfo(filepath.Join(dir, name)); ok {
			infos = append(infos, info)
		}
	}
	slices.SortFunc(infos, func(a, b Info) int { return b.Modified.Compare(a.Modified) })
	return infos, nil
}

// ContinueRecent 打开这个 cwd 下最近改动的会话；一个都没有就新建。
// 靠文件的 mtime 找，不看文件名里的时间戳
func ContinueRecent(cwd string) (*Session, error) {
	infos, err := List(cwd)
	if err != nil {
		return nil, err
	}
	if len(infos) == 0 {
		return Create(cwd)
	}
	return Open(infos[0].Path)
}

// readInfo 只读第一行。得用 ReadBytes 而不是 Scanner —— Scanner 默认单行上限 64KB，
// 而消息里可能带 base64 图片，第一行轻松超
func readInfo(path string) (Info, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Info{}, false
	}
	defer f.Close()

	line, err := bufio.NewReader(f).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return Info{}, false
	}

	// 读不出来的文件当它不存在：一个坏文件不该挡住别的会话被列出来
	var header Header
	if json.Unmarshal(bytes.TrimSpace(line), &header) != nil || header.Type != headerType {
		return Info{}, false
	}

	stat, err := f.Stat()
	if err != nil {
		return Info{}, false
	}
	return Info{
		Path:     path,
		ID:       header.ID,
		Cwd:      header.Cwd,
		Created:  header.Timestamp,
		Modified: stat.ModTime(),
	}, true
}

// readFile 流式逐行读，坏行直接跳过
func readFile(r io.Reader) (*Header, []*Entry, error) {
	reader := bufio.NewReader(r)
	var header *Header
	var entries []*Entry

	for {
		line, err := reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			if header == nil {
				var parsed Header
				if json.Unmarshal(trimmed, &parsed) != nil || parsed.Type != headerType {
					return nil, nil, errors.New("第一行不是会话 header，这文件不是我们写的")
				}
				header = &parsed
			} else {
				var entry Entry
				if json.Unmarshal(trimmed, &entry) == nil {
					entries = append(entries, &entry)
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return header, entries, nil
			}
			return nil, nil, err
		}
	}
}

// dirFor 是 cwd 对应的会话目录：~/.go-harness/sessions/--<cwd 编码>--。
// 编码规则照 pi：去掉开头的 /，再把 / \ : 换成 -
func dirFor(cwd string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("找不到用户主目录：%w", err)
	}
	encoded := strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(strings.TrimPrefix(cwd, "/"))
	return filepath.Join(home, ".go-harness", "sessions", "--"+encoded+"--"), nil
}

// fileName 是会话文件名：<时间戳，: 和 . 换成 ->_<session id>.jsonl。
// 时间戳只是给人看的，找「最近一次」靠 mtime
func fileName(created, id string) string {
	return strings.NewReplacer(":", "-", ".", "-").Replace(created) + "_" + id + ".jsonl"
}

// writeLine 写一条记录（自带换行）
func writeLine(w io.Writer, v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("编码会话记录失败：%w", err)
	}
	if _, err := w.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("写会话文件失败：%w", err)
	}
	return nil
}

func appendLine(path string, v any) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	return writeLine(f, v)
}
