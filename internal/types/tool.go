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

	// Execute 返回的 error 是写给模型看的，不是写给用户看的，文案里要带上
	// 模型传的值和合法范围。loop 收到非 nil 的 error 会把文案包成 IsError=true 的
	// tool_result 回给模型，本次 turn 继续，让它自己改了重试。
	// 不能把 error 丢掉，也不会有人替它兜底：每个 tool_use 都要有配对的 tool_result。
	//
	// ctx 取消不用在这里管成 error——loop 自己看 ctx 决定停不停，返回什么都会被收下。
	Execute func(ctx context.Context, args map[string]any, sendPartialResult SendPartialResult) (ToolResult, error)
}

type ToolResult struct {
	ContentBlock []ContentBlock
	ShouldStop   bool // 本轮所有工具的该字段都为 true 就停止调模型；也可以留给工具和以后的 hook 用
}
