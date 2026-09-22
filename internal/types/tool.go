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
	// 模型传的值和合法范围。loop 收到非 nil 的 error 之后：
	//   - 可恢复的（参数不合法、文件不存在之类）：包成 IsError=true 的 ToolResult，
	//     当作这条 tool_use 的 tool_result 回给模型，本次 turn 继续，让它自己改了重试
	//   - 不可恢复的（ctx 被取消、panic）：终止整个 loop
	// 不能把 error 丢掉，接口要求每个 tool_use 都要有配对的 tool_result。
	Execute func(ctx context.Context, args map[string]any, sendPartialResult SendPartialResult) (ToolResult, error)
}

type ToolResult struct {
	ContentBlock []ContentBlock
	ShouldStop   bool // 所有工具返回的该字段都为 true 时，停止调用工具；也可后续留给 hook 使用
}
