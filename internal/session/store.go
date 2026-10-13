package session

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/reuben/go-harness/internal/types"
)

// Info 是 List 的一项。只看文件第一行的 header 和标题 sidecar，
// 够列表显示时间 / cwd / id / 标题
type Info struct {
	Path     string
	ID       string
	Cwd      string
	Created  string
	Title    string // 还没生成过就是空串
	Modified time.Time
}

// Create 建一个空会话。不碰磁盘 —— 文件要等第一次落盘时才出现
func Create(cwd string) (*Session, error) {
	return CreateWithID(cwd, randomHex(16))
}

// CreateWithID 建一个指定 id 的空会话。web 端的 id 是前端生成的：这样「新会话」
// 草稿在第一条消息之前就有稳定 id，刷新页面不丢，连点两下也不会建出两个
func CreateWithID(cwd, id string) (*Session, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("会话 id %q 形状不对", id)
	}
	dir, err := dirFor(cwd)
	if err != nil {
		return nil, err
	}
	return &Session{
		id:      id,
		cwd:     cwd,
		dir:     dir,
		created: timestampNow(),
		byId:    map[string]*Entry{},
	}, nil
}

// Find 按 id 找会话文件。文件名是 <时间戳>_<id>.jsonl，按后缀找就够了 ——
// 不用 filepath.Glob：cwd 里带 [ ] * ? 这类字符（macOS 上完全合法的目录名）时，
// 目录那段会被当成 glob 模式解析，明明存在的文件匹配不上，上层就会把它
// 当成「还没落盘的草稿」重建，同一个 id 在磁盘上落出第二个文件
//
// 目录名是 cwd 压出来的、不可逆（`/a-b` 和 `/a/b` 编出来是同一个），所以还得
// 拿文件里的 header 再认一次：这个目录可能住着另一个项目的会话
func Find(cwd, id string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("会话 id %q 形状不对", id)
	}
	dir, err := dirFor(cwd)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fs.ErrNotExist
	}
	if err != nil {
		return "", err
	}
	suffix := "_" + id + ".jsonl"
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), suffix) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		header, ok := readHeader(path)
		if !ok || !sameCwd(header.Cwd, cwd) {
			continue
		}
		return path, nil
	}
	return "", fs.ErrNotExist
}

// sameCwd 比 cwd 认的是同一个目录。两边都 Clean 一道：
// header 里存的可能是建会话那会儿的原样写法
func sameCwd(a, b string) bool { return filepath.Clean(a) == filepath.Clean(b) }

// Delete 删会话文件 + 标题 sidecar（连写标题halfway留下的 tmp 一起）。
// 文件已经不在不算错。目录留着不动 —— 一条条删会话是「清内容」，
// 这个 cwd 以后多半还会再用；整目录的收拾留给 DeleteAll（删项目那条路）
func Delete(path string) error {
	for _, target := range []string{path, titlePath(path), titlePath(path) + ".tmp"} {
		if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// DeleteAll 删掉这个 cwd 名下的所有会话，返回删了几条，最后把目录也收掉 ——
// 删项目就是「这个目录从此不归我管」，留个空壳只会让人以为里面还有东西。
// 同级目录里住着别的项目的会话时不碰它们（List 按文件头里的 cwd 认过），
// 那种情况下目录也收不掉 —— os.Remove 只删得掉空目录
func DeleteAll(cwd string) (int, error) {
	infos, err := List(cwd)
	if err != nil {
		return 0, err
	}
	for i, info := range infos {
		if err := Delete(info.Path); err != nil {
			return i, err
		}
	}
	dir, err := dirFor(cwd)
	if err != nil {
		return len(infos), err
	}
	os.Remove(dir)
	return len(infos), nil
}

// titlePath 是会话文件的标题 sidecar。标题不写进 JSONL 是有原因的：List 只读
// 每个文件的第一行（见 readInfo），标题要是在 entry 里，列表就得全量扫全文
func titlePath(path string) string { return path + ".title" }

// Title 读会话标题，没生成过就是空串
func Title(path string) string {
	buf, err := os.ReadFile(titlePath(path))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(buf))
}

// WriteTitle 原子落盘：标题是后台 goroutine 写的，侧栏随时可能来读，
// 不能让它读到半截文件
func WriteTitle(path, title string) error {
	// 会话文件已经没了就别写：删掉会话之后晚到的标题 goroutine
	// 不该在磁盘上留一个孤儿 sidecar
	if _, err := os.Stat(path); err != nil {
		return err
	}
	tmp := titlePath(path) + ".tmp"
	if err := os.WriteFile(tmp, []byte(title), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, titlePath(path))
}

// MessagePreview 是一条用户消息给侧栏看的样子。只发了图、没写字的消息正文是空的，
// 显示成空串看起来像条坏消息 —— 给个「[图片]」
func MessagePreview(msg *types.Message) string {
	if text := msg.Text(); text != "" {
		return text
	}
	for _, block := range msg.ContentBlocks {
		if block.Kind == types.ImageBlock {
			return "[图片]"
		}
	}
	return ""
}

// FirstUserText 返回第一条用户消息的正文，给侧栏当兜底标题（标题还没生成时
// 显示它）。读到就停：绝大多数会话第二行就是它。
// 一律 ReadBytes 不用 Scanner —— 单行上限 64KB，而消息里可能带 base64 图片
func FirstUserText(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			var entry Entry
			if json.Unmarshal(trimmed, &entry) == nil && entry.Message != nil {
				if entry.Message.Role == types.UserRole {
					return MessagePreview(entry.Message)
				}
			}
		}
		if err != nil {
			return ""
		}
	}
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
		info, ok := readInfo(filepath.Join(dir, name))
		// 目录名是 cwd 压出来的、不可逆，同一个目录里可能住着别的项目的会话
		// （`/a-b` 和 `/a/b` 编出来一样），header 才是权威
		if !ok || !sameCwd(info.Cwd, cwd) {
			continue
		}
		infos = append(infos, info)
	}
	slices.SortFunc(infos, func(a, b Info) int { return b.Modified.Compare(a.Modified) })
	return infos, nil
}

// readHeader 只读第一行。得用 ReadBytes 而不是 Scanner —— Scanner 默认单行上限
// 64KB，而消息里可能带 base64 图片，第一行轻松超
func readHeader(path string) (Header, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Header{}, false
	}
	defer f.Close()

	line, err := bufio.NewReader(f).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return Header{}, false
	}

	// 读不出来的文件当它不存在：一个坏文件不该挡住别的会话被列出来
	var header Header
	if json.Unmarshal(bytes.TrimSpace(line), &header) != nil || header.Type != headerType {
		return Header{}, false
	}
	return header, true
}

func readInfo(path string) (Info, bool) {
	header, ok := readHeader(path)
	if !ok {
		return Info{}, false
	}
	stat, err := os.Stat(path)
	if err != nil {
		return Info{}, false
	}
	return Info{
		Path:     path,
		ID:       header.ID,
		Cwd:      header.Cwd,
		Created:  header.Timestamp,
		Title:    Title(path),
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

// sessionsRoot 是所有会话目录的父目录
func sessionsRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("找不到用户主目录：%w", err)
	}
	return filepath.Join(home, ".go-harness", "sessions"), nil
}

// dirFor 是 cwd 对应的会话目录：~/.go-harness/sessions/--<cwd 编码>-<指纹>--。
//
// 编码规则照 pi（去掉开头的 /，再把 / \ : 换成 -）—— 它是多对一的：/a-b 和 /a/b
// 编出来都是 a-b，光靠它两个项目会住进同一个目录。所以尾巴上再钉一段 cwd 的指纹，
// 把「目录名 → cwd」拉回一对一。名字只是标签，文件头里的 cwd 才是权威
func dirFor(cwd string) (string, error) {
	root, err := sessionsRoot()
	if err != nil {
		return "", err
	}
	// 先 Clean：同一个目录带不带尾斜杠必须落到同一个会话目录，
	// 不然 /a/b 和 /a/b/ 会各建一套会话，侧栏只看得到其中一套
	cwd = filepath.Clean(cwd)
	encoded := strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(strings.TrimPrefix(cwd, "/"))
	return filepath.Join(root, "--"+encoded+"-"+cwdHash(cwd)+"--"), nil
}

// cwdHash 是 cwd 的一小段指纹（4 字节 = 8 位十六进制）。几十个会话目录的量级，
// 撞上要 2 的 16 次方个目录才有一次机会，够用了
func cwdHash(cwd string) string {
	sum := sha256.Sum256([]byte(cwd))
	return hex.EncodeToString(sum[:4])
}

// Migrate 把老命名（--<编码>--，没有指纹）下的会话挪到新名字下。
//
// 认的是每个文件**自己** header 里的 cwd，不是它现在住的目录名 —— 所以就算老目录
// 里混着两个项目的会话，也各自去各自该去的地方，不会搬错。已经在新名字下的不动，
// 读不出 cwd 的文件也不动（宁可留在原地，也不能瞎搬）。重复跑是安全的
func Migrate() (int, error) {
	root, err := sessionsRoot()
	if err != nil {
		return 0, err
	}
	dirs, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil // 还没建过 sessions/，没什么可迁的
	}
	if err != nil {
		return 0, err
	}

	moved := 0
	for _, dirent := range dirs {
		if !dirent.IsDir() {
			continue
		}
		oldDir := filepath.Join(root, dirent.Name())
		files, err := os.ReadDir(oldDir)
		if err != nil {
			return moved, err
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".jsonl") {
				continue // 标题 sidecar 跟着正文走，不单独算一条
			}
			src := filepath.Join(oldDir, file.Name())
			header, ok := readHeader(src)
			if !ok || header.Cwd == "" {
				continue
			}
			dst, err := dirFor(header.Cwd)
			if err != nil {
				return moved, err
			}
			if dst == oldDir {
				continue // 已经在它该在的地方
			}
			target := filepath.Join(dst, file.Name())
			// 目标处已经有同名文件：多半是同一份会话被复制过。跳过不覆盖 ——
			// 留在原地的顶多看不见，盖掉的是一份真数据
			if _, err := os.Stat(target); err == nil {
				continue
			}
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return moved, err
			}
			if err := os.Rename(src, target); err != nil {
				return moved, err
			}
			for _, side := range []string{".title", ".title.tmp"} {
				if _, err := os.Stat(src + side); err == nil {
					os.Rename(src+side, target+side) // 标题搬不动不影响正文，顶多退回没标题
				}
			}
			moved++
		}
		os.Remove(oldDir) // 空了才收得掉；还留着别的东西（比如读不出的文件）就留着
	}
	return moved, nil
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
