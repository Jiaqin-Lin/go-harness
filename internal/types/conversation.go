package types

type Conversation struct {
	SystemPrompt string
	Messages     []*Message
	Tools        []*Tool
}
