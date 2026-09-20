package main

import (
	"context"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reuben/go-harness/internal/core"
	"github.com/reuben/go-harness/internal/prompts"
	"github.com/reuben/go-harness/internal/tools"
	"github.com/reuben/go-harness/internal/tui"
	"github.com/reuben/go-harness/internal/types"
)

func main() {
	ctx := context.Background()

	// TODO 模型接入

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "拿不到启动时目录：%v\n", err)
		os.Exit(1)
	}

	allTools := tools.GetAllTools(cwd)

	conversation := &types.Conversation{
		SystemPrompt: prompts.BuildSystemPrompt(cwd, allTools),
		Tools:        allTools,
	}

	send := func(text string) {
		prompt := &types.Message{
			Role:          types.UserRole,
			ContentBlocks: []types.ContentBlock{types.NewTextBlock(text)},
		}
		conversation.Message = append(conversation.Message,
			core.Run(ctx, []*types.Message{prompt}, conversation)...)
	}

	if _, err := tea.NewProgram(tui.NewModel(send)).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "输入界面挂了：%v\n", err)
		os.Exit(1)
	}
}
