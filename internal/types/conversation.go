package types

type Conversation struct {
	SystemPrompt string
	Message      []*Message
	Tools        []*Tool
}
