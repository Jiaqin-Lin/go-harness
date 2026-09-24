package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/joho/godotenv"

	"github.com/reuben/go-harness/internal/core"
	"github.com/reuben/go-harness/internal/prompts"
	"github.com/reuben/go-harness/internal/provider"
	"github.com/reuben/go-harness/internal/session"
	"github.com/reuben/go-harness/internal/tools"
	"github.com/reuben/go-harness/internal/tui"
	"github.com/reuben/go-harness/internal/types"
)

func main() {
	ctx := context.Background()

	// 没有 .env 也能跑，那样就全都从环境变量里来
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "读 .env 失败：%v\n", err)
		os.Exit(1)
	}

	apiKey, baseURL, model := os.Getenv("DEEPSEEK_API_KEY"), os.Getenv("DEEPSEEK_BASE_URL"), os.Getenv("DEEPSEEK_MODEL")
	if apiKey == "" || baseURL == "" || model == "" {
		fmt.Fprintln(os.Stderr, "DEEPSEEK_API_KEY、DEEPSEEK_BASE_URL、DEEPSEEK_MODEL 要在 .env 或环境变量里给全")
		os.Exit(1)
	}
	llm := provider.NewDeepSeek(apiKey, baseURL, model)

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "拿不到启动时目录：%v\n", err)
		os.Exit(1)
	}

	sess, err := session.ContinueRecent(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开会话失败：%v\n", err)
		os.Exit(1)
	}

	allTools := tools.GetAllTools(sess.Cwd())

	conversation := &types.Conversation{
		SystemPrompt: prompts.BuildSystemPrompt(sess.Cwd(), allTools),
		Tools:        allTools,
		Messages:     sess.Messages(),
	}

	send := func(text string) {
		prompt := &types.Message{
			Role:          types.UserRole,
			ContentBlocks: []types.ContentBlock{types.NewTextBlock(text)},
		}
		// 以轮为单位落盘：core.Run 返回时，每个 tool_call 都有配对的 tool_result，
		// 所以文件里不会出现半轮
		for _, msg := range core.Run(ctx, []*types.Message{prompt}, conversation, llm.Generate, tui.Emit) {
			conversation.Messages = append(conversation.Messages, msg)
			if _, err := sess.Append(msg); err != nil {
				fmt.Fprintf(os.Stderr, "会话没存上：%v\n", err)
			}
		}
	}

	if _, err := tea.NewProgram(tui.NewModel(send)).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "输入界面挂了：%v\n", err)
		os.Exit(1)
	}
}
