package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reuben/go-harness/internal/types"
)

func WriteTool(cwd string) *types.Tool {
	return &types.Tool{
		Name:    "write",
		Snippet: "新建文件或整份覆盖",
		Desc:    "写文件。不存在就新建，存在就整份覆盖，父目录会自动建好。只想改文件里的一小段就别用这个，用 edit。",
		Args: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "要写的文件路径。相对路径按工作目录解析，~ 展开成家目录。",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "文件的完整内容，允许空串，那就是一个空文件。",
				},
			},
			"required": []string{
				"path",
				"content",
			},
		},
		Guidelines: []string{
			"write 只用来新建文件或者整份重写；改文件里的一小段用 edit。",
			"覆盖一个已有文件之前，先 read 看一眼它现在是什么样。",
		},
		Execute: func(ctx context.Context, args map[string]any, _ types.SendPartialResult) (types.ToolResult, error) {
			return executeWrite(ctx, cwd, args)
		},
	}
}

func executeWrite(ctx context.Context, cwd string, args map[string]any) (types.ToolResult, error) {
	raw := strings.TrimSpace(stringArg(args, "path"))
	if raw == "" {
		return types.ToolResult{}, fmt.Errorf("缺少 path 参数")
	}
	content, err := stringArgStrict(args, "content")
	if err != nil {
		return types.ToolResult{}, err
	}

	path, err := resolvePath(cwd, raw)
	if err != nil {
		return types.ToolResult{}, err
	}

	if err := ctx.Err(); err != nil {
		return types.ToolResult{}, err
	}

	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return types.ToolResult{}, fmt.Errorf("%s 是一个目录，没法写", path)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return types.ToolResult{}, fmt.Errorf("创建 %s 的父目录失败：%w", filepath.Dir(path), err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return types.ToolResult{}, err
	}

	return textResult(fmt.Sprintf("已写入 %s（%d 字节）", path, len(content))), nil
}
