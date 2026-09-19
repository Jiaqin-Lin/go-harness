package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/reuben/go-harness/internal/core"
	"github.com/reuben/go-harness/internal/prompts"
	"github.com/reuben/go-harness/internal/tools"
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

	tools := tools.GetAllTools(cwd)

	conversation := &types.Conversation{
		SystemPrompt: prompts.BuildSystemPrompt(cwd, tools),
		Tools:        tools,
	}

	fmt.Println("输入问题回车发送，exit 或 ctrl+D 或 ctrl+C 退出。")

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(nil, 1024*1024)

	for {
		fmt.Print("\n>")
		if !in.Scan() {
			break
		}

		text := strings.TrimSpace(in.Text())
		if text == "" {
			continue
		}
		if text == "exit" {
			break
		}

		prompt := &types.Message{
			Role:          types.UserRole,
			ContentBlocks: []types.ContentBlock{types.NewTextBlock(text)},
		}
		conversation.Message = append(conversation.Message,
			core.Run(ctx, []*types.Message{prompt}, conversation)...)
	}

	if err := in.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "\n读取输入失败：%v\n", err)
	}
}
