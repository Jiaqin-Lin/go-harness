package types

type Conversation struct {
	SystemPrompt string
	Messages     []*Message
	Tools        map[string]*Tool
}
