package tools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/reuben/go-harness/internal/types"
)

func ReadTool(cwd string) *types.Tool {
	return &types.Tool{
		Name:    "read",
		Snippet: "读文件内容",
		Desc: fmt.Sprintf(
			"读取一个文件的内容。每行输出成「行号 + 制表符 + 内容」，行号从 1 数起。"+
				"一次最多给 %d 行或 %d KB，撞到哪个上限就在哪停，剩下的用结尾给的 offset 接着读。"+
				"二进制文件、空文件、目录不给内容，只回一句说明。",
			maxLines, maxBytes/1024,
		),
		Args: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "要读的文件路径。相对路径按工作目录解析，~ 展开成家目录。",
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "从第几行开始读，从 1 数起，默认 1。小于 1、或超过文件总行数都报错。",
				},
				"limit": map[string]any{
					"type": "integer",
					"description": fmt.Sprintf(
						"最多读多少行，默认 %d。大于 %d 按 %d 算（不报错），小于 1 报错。",
						maxLines, maxLines, maxLines),
				},
			},
			"required": []string{
				"path",
			},
		},
		Guidelines: []string{
			"看文件用 read，不要用 cat、head、sed 绕一圈。",
			"read 结尾说「还有更多」时，用 offset 接着读，不要从头再读一遍。",
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

	if sniffBinary(br) {
		return textResult(fmt.Sprintf("[%s 看起来是二进制文件，没有当文本读。]", path)), nil
	}

	offset, err := intArg(args, "offset", 1)
	if err != nil {
		return types.ToolResult{}, err
	}
	limit, err := intArg(args, "limit", maxLines)
	if err != nil {
		return types.ToolResult{}, err
	}

	text, err := readLines(ctx, br, offset, limit)
	if err != nil {
		return types.ToolResult{}, fmt.Errorf("读取 %s 失败：%w", path, err)
	}

	return textResult(text), nil
}

func readLines(ctx context.Context, br *bufio.Reader, offset, limit int) (string, error) {
	if offset < 1 {
		return "", fmt.Errorf("offset 从 1 数起，收到 %d", offset)
	}
	if limit < 1 {
		return "", fmt.Errorf("limit 至少是 1，收到 %d", limit)
	}
	// limit 超过上限不报错，直接按上限处理
	if limit > maxLines {
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

	var lines []string
	hasMore := false
	used := 0
	cutLine := 0  // 被截断的行号，0 表示没有
	cutBytes := 0 // 被截断的那一行实际给了多少字节

	for {
		line, cut, err := nextLine(ctx, br, maxBytes)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}

		// 已经读满 limit 行，这一行已经读出来了，下次用 offset 接着读它
		if len(lines) == limit {
			hasMore = true
			break
		}

		lineNo := offset + len(lines)
		prefix := strconv.Itoa(lineNo) + "\t"
		lineBytes := len(prefix) + len(line)
		// 换行符始终比行数少 1
		if len(lines) > 0 {
			lineBytes++ // 换行符占 1
		}

		if cut {
			if len(lines) > 0 {
				hasMore = true
				break
			}
			lines = append(lines, prefix+line)
			cutLine = lineNo
			cutBytes = len(line)
			break
		}

		// 超 50kb
		if len(lines) > 0 && used+lineBytes > maxBytes {
			hasMore = true
			break
		}

		used += lineBytes
		lines = append(lines, prefix+line)
	}

	// 跳完 offset - 1 行之后没有内容，视作 offset 超过了文件的总行数
	if len(lines) == 0 && offset > 1 {
		return "", fmt.Errorf("offset %d 超过了文件的总行数", offset)
	}

	var sb strings.Builder
	sb.WriteString(strings.Join(lines, "\n"))
	switch {
	case cutLine > 0:
		fmt.Fprintf(&sb, "\n\n[第 %d 行超过 %d KB 上限，只给了它的开头 %d 字节，剩下的取不到，别再读它；跳过这一行继续读用 offset=%d。]",
			cutLine, maxBytes/1024, cutBytes, cutLine+1)
	case hasMore:
		fmt.Fprintf(&sb, "\n\n[已显示第 %d-%d 行，还有更多。用 offset=%d 接着读。]",
			offset, offset+len(lines)-1, offset+len(lines))
	default:
		// 读到文件末尾，总数 = 起始行号 + 已读行数 - 1，不用再扫一遍文件
		fmt.Fprintf(&sb, "\n\n[已读到文件末尾，共 %d 行]", offset+len(lines)-1)
	}
	return sb.String(), nil
}

func sniffBinary(br *bufio.Reader) bool {
	head, _ := br.Peek(sniffBytes)
	return bytes.IndexByte(head, 0) >= 0
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
