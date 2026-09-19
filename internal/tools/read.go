package tools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/reuben/go-harness/internal/types"
)

func ReadTool(cwd string) *types.Tool {
	return &types.Tool{
		Name:    "read",
		Snippet: "读文件内容",
		Desc: fmt.Sprintf(
			"读取一个文件的内容，内容过长时只返回前 %d 行或 %d KB，同时会告诉你从第几行开始继续读。",
			maxLines, maxBytes/1024,
		),
		Args: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "要读取的文件路径；相对路径相对于工作目录解析",
				},
				"offset": map[string]any{
					"type":        "number",
					"description": "从第几行开始读（从 1 数起），默认为 1，小于 1 按 1 算",
				},
				"limit": map[string]any{
					"type": "number",
					"description": fmt.Sprintf(
						"最多读多少行，不填默认为 %d，超过 %d 按 %d 处理", maxLines, maxLines, maxLines),
				},
			},
			"required": []string{
				"path",
			},
		},
		Guidelines: []string{
			"看文件用 read，不要用 cat、sed 绕一圈。",
		},
		Execute: func(ctx context.Context, args map[string]any, _ types.SendPartialResult) (types.ToolResult, error) {
			return executeRead(ctx, cwd, args)
		},
	}
}

func executeRead(ctx context.Context, cwd string, args map[string]any) (types.ToolResult, error) {
	rawPath := strings.TrimSpace(stringArg(args, "path"))
	if rawPath == "" {
		return types.ToolResult{}, fmt.Errorf("缺少 path 参数")
	}

	path, err := resolvePath(cwd, rawPath)
	if err != nil {
		return types.ToolResult{}, err
	}

	f, err := os.Open(path)
	if err != nil {
		return types.ToolResult{}, fmt.Errorf("打开 %s 失败：%w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return types.ToolResult{}, fmt.Errorf("取 %s 的状态失败：%w", path, err)
	}
	switch {
	case info.IsDir():
		return types.ToolResult{}, fmt.Errorf("%s 是一个目录，请用 ls 查看里面有什么", path)
	case !info.Mode().IsRegular():
		return types.ToolResult{}, fmt.Errorf("%s 不是普通文件，无法阅读", path)
	case info.Size() == 0:
		return textResult(fmt.Sprintf("[%s 是空文件。]", path)), nil
	}

	br := bufio.NewReaderSize(f, bufioSize)

	// TODO 二进制文件、图片处理

	text, err := readLines(ctx, br, info.Size(), intArg(args, "offset", 1), intArg(args, "limit", maxLines))
	if err != nil {
		return types.ToolResult{}, fmt.Errorf("读取 %s 失败：%w", path, err)
	}

	return textResult(text), nil
}

func readLines(ctx context.Context, br *bufio.Reader, size int64, offset, limit int) (string, error) {
	if offset < 1 {
		offset = 1
	}

	if offset > int(size) {
		return "", fmt.Errorf("offset %d 不在文件范围内（文件只有 %d 字节）", offset, size)
	}
	// limit 越界不报错，直接按上限处理
	if limit < 1 || limit > maxLines {
		limit = maxLines
	}

	// 先丢掉前 offset - 1 行
	for line := 1; line < offset; line++ {
		if err := discardLine(ctx, br); err != nil {
			if errors.Is(err, io.EOF) {
				return "", fmt.Errorf("offset %d 超过了文件的总行数", offset)
			}
			return "", err
		}
	}

	var body []string
	hasMore := false
	used := 0
	cutLine := 0 // 被截断的行号，0 表示没有

	for {
		line, cut, err := nextLine(ctx, br, maxBytes)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}

		// 已经读满 limit 行，这一行已经读出来了，下次用 offset 接着读它
		if len(body) == limit {
			hasMore = true
			break
		}

		lineBytes := len(line)
		// 换行符始终比行数少 1
		if len(body) > 0 {
			lineBytes++ // 换行符占 1
		}

		if cut {
			// 一行本身就超过 maxBytes，行内被截断掉的部分用 offset 也读不出来，
			// 所以第一行直接给开头；其它位置的超长行留到下一次读，不在正文中间塞半行。
			if len(body) > 0 {
				hasMore = true
				break
			}
			body = append(body, line)
			cutLine = offset
			break
		}

		// 字节数抵达 maxBytes，这一行放不下，留给下一次读
		if used+lineBytes > maxBytes {
			hasMore = true
			break
		}

		used += lineBytes
		body = append(body, line)
	}

	// 跳完 offset - 1 行之后没有内容，视作 offset 超过了文件的总行数
	if len(body) == 0 && offset > 1 {
		return "", fmt.Errorf("offset %d 超过了文件的总行数", offset)
	}

	var sb strings.Builder
	sb.WriteString(strings.Join(body, "\n"))
	switch {
	case cutLine > 0:
		fmt.Fprintf(&sb, "\n\n[第 %d 行超过 %d KB 上限，这里只给了它的开头 %d 字节；跳过这一行继续读用 offset=%d。]",
			cutLine, maxBytes/1024, len(body[len(body)-1]), cutLine+1)
	case hasMore:
		fmt.Fprintf(&sb, "\n\n[已显示第 %d-%d 行，还有更多。用 offset=%d 接着读。]",
			offset, offset+len(body)-1, offset+len(body))
	}
	return sb.String(), nil
}

func discardLine(ctx context.Context, br *bufio.Reader) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		raw, err := br.ReadSlice('\n')
		switch {
		case err == nil:
			// 读到换行符，这一行结束
			return nil
		case errors.Is(err, bufio.ErrBufferFull):
			// 行太长，缓冲区满了还没读到换行符，继续往后丢
			continue
		case errors.Is(err, io.EOF):
			if len(raw) == 0 {
				return io.EOF // 文件读完了，没有下一行
			}
			return nil // 最后一行没有换行符，也算一行
		default:
			return err
		}
	}
}

func nextLine(ctx context.Context, br *bufio.Reader, maxBytes int) (string, bool, error) {
	// 不 +4 无法触发 len(kept) > maxBytes，最多 ==
	boundary := maxBytes + 4
	var kept []byte

	for {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}

		raw, err := br.ReadSlice('\n')
		if len(raw) > 0 {
			text := raw
			complete := false
			if raw[len(raw)-1] == '\n' {
				text = text[:len(text)-1]
				complete = true
			}

			if room := boundary - len(kept); room > 0 {
				if len(text) > room {
					text = text[:room]
				}
				kept = append(kept, text...)
			}

			if complete {
				break
			}
		}

		// 走到这里说明还没读到换行符，err 一定不是 nil
		switch {
		// br 缓冲区满了，continue 往后继续读
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		// 读不到任何字节，说明文件读完了
		case errors.Is(err, io.EOF):
			if len(raw) == 0 {
				return "", false, io.EOF
			}
		// 其它错误
		default:
			return "", false, err
		}
		break
	}

	if len(kept) > maxBytes {
		return cutRunes(string(kept), maxBytes), true, nil
	}

	return string(kept), false, nil
}
