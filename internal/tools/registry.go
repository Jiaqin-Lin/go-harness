package tools

import "github.com/reuben/go-harness/internal/types"

func textResult(text string) types.ToolResult {
	return types.ToolResult{ContentBlock: []types.ContentBlock{types.NewTextBlock(text)}}
}

func GetAllTools(cwd string) []*types.Tool {
	return []*types.Tool{
		ReadTool(cwd),
		BashTool(cwd),
		WriteTool(cwd),
	}
}
