package types

type Conversation struct {
	SystemPrompt string
	Messages     []*Message
	Tools        []*Tool

	// MaxTokens 限制这一次的输出长度，0 = 不限制。摘要是唯一会设置它的调用：
	// 输出上限能兜住跑飞的摘要，而正常对话不该被掐
	MaxTokens int
}
