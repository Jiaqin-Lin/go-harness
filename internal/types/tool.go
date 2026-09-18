package types

import "context"

// SendPartialResult 让工具把中间结果推回上层，比如流式输出时边跑边给
type SendPartialResult func(partialResult ToolResult)

type Tool struct {
	Name    string
	Snippet string
	Desc    string
	Args    map[string]any

	Guidelines []string

	Execute func(ctx context.Context, args map[string]any, sendPartialResult SendPartialResult) (ToolResult, error)
}

type ToolResult struct {
	ContentBlock []ContentBlock
	ShouldStop   bool // 所有工具返回的该字段都为 true 时，loop 才停下
}
