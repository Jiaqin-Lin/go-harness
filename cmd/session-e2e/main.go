// 端到端驱动：直接调 core.Run，不经过 HTTP 层（web 版那条路见 internal/server）。
//
// 这个包会花真钱（打真模型），所以它只能是 main 包、只能手动跑：
// go test ./... 对 main 包只编译不执行，永远不会调到这里。
// 别把它包成 _test.go，那样每跑一次 go test 就烧一次钱。
//
// 用法：cd <会话目录> && GO_HARNESS_ENV=<.env 路径> session-e2e [-no-tools] "问题"
// 不给问题就只开会话、读历史、退出 —— 用来验「打开就退出不留文件」。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/joho/godotenv"

	"github.com/reuben/go-harness/internal/core"
	"github.com/reuben/go-harness/internal/prompts"
	"github.com/reuben/go-harness/internal/provider"
	"github.com/reuben/go-harness/internal/session"
	"github.com/reuben/go-harness/internal/skills"
	"github.com/reuben/go-harness/internal/tools"
	"github.com/reuben/go-harness/internal/types"
)

func main() {
	noTools := flag.Bool("no-tools", false, "一个工具都不给：模型答什么只能靠恢复的历史，没法自己去看")
	flag.Parse()

	ctx := context.Background()
	if path := os.Getenv("GO_HARNESS_ENV"); path != "" {
		if err := godotenv.Load(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "读 %s 失败：%v\n", path, err)
			os.Exit(1)
		}
	}

	apiKey, baseURL, model := os.Getenv("DEEPSEEK_API_KEY"), os.Getenv("DEEPSEEK_BASE_URL"), os.Getenv("DEEPSEEK_MODEL")
	if apiKey == "" || baseURL == "" || model == "" {
		fmt.Fprintln(os.Stderr, "DEEPSEEK_API_KEY、DEEPSEEK_BASE_URL、DEEPSEEK_MODEL 要给全")
		os.Exit(1)
	}
	llm := provider.NewDeepSeek(apiKey, baseURL, model)

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "拿不到当前目录：%v\n", err)
		os.Exit(1)
	}
	sess, err := session.ContinueRecent(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开会话失败：%v\n", err)
		os.Exit(1)
	}

	allTools := tools.GetAllTools(sess.Cwd())
	if *noTools {
		allTools = nil
	}
	conversation := &types.Conversation{
		SystemPrompt: prompts.BuildSystemPrompt(prompts.Input{
			Cwd:      sess.Cwd(),
			Tools:    allTools,
			Skills:   skills.Effective(sess.Cwd()),
			AgentsMD: prompts.ReadAgentsMD(sess.Cwd()),
		}),
		Tools:    allTools,
		Messages: sess.Messages(),
	}
	fmt.Printf("== 会话 %s\n== 恢复 %d 条历史，%d 个工具\n", sess.ID(), len(conversation.Messages), len(allTools))

	if flag.NArg() == 0 {
		fmt.Println("== 没给问题，就此退出")
		return
	}

	prompt := &types.Message{
		Role:          types.UserRole,
		ContentBlocks: []types.ContentBlock{types.NewTextBlock(flag.Arg(0))},
	}

	emit := func(e types.Event) {
		switch e.Type {
		case types.TurnEndEvent:
			fmt.Printf("-- assistant (%s)：", e.Message.StopReason)
			for _, block := range e.Message.ContentBlocks {
				switch block.Kind {
				case types.TextBlock:
					fmt.Print(block.Text)
				case types.ToolBlock:
					fmt.Printf("[调用 %s %s]", block.ToolName, block.Args)
				}
			}
			fmt.Println()
			for _, msg := range e.ToolResultMessages {
				fmt.Printf("-- tool_result：%.200s\n", msg.ContentBlocks[0].Text)
			}
		}
	}

	for _, msg := range core.Run(ctx, []*types.Message{prompt}, conversation, llm.Generate, emit, core.Queues{}) {
		conversation.Messages = append(conversation.Messages, msg)
		if _, err := sess.Append(msg); err != nil {
			fmt.Fprintf(os.Stderr, "会话没存上：%v\n", err)
		}
	}
	fmt.Printf("== 落盘 %s，共 %d 条消息\n", sess.Path(), len(sess.Messages()))
}
