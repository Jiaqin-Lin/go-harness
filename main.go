package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/joho/godotenv"

	"github.com/reuben/go-harness/internal/provider"
	"github.com/reuben/go-harness/internal/server"
	"github.com/reuben/go-harness/internal/session"
)

func main() {
	port := flag.Int("port", 8776, "监听端口。只绑 127.0.0.1")
	flag.Parse()

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

	// 老命名下的会话挪到新名字下（目录名加了 cwd 指纹，见 session.dirFor）。
	// 一次性的事，挪不动也不能拦住服务启动 —— 老目录还在，下次再试
	if moved, err := session.Migrate(); err != nil {
		fmt.Fprintf(os.Stderr, "会话目录没迁移完：%v\n", err)
	} else if moved > 0 {
		fmt.Printf("把 %d 条老会话挪到了新的目录名下\n", moved)
	}

	// 启动目录是「可能被当成项目」的默认值：目录选择器从这儿起
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "拿不到启动时目录：%v\n", err)
		os.Exit(1)
	}

	srv := server.New(server.Config{
		Port:     *port,
		Cwd:      cwd,
		Model:    model,
		Generate: provider.NewDeepSeek(apiKey, baseURL, model).Generate,
	})

	// 先把端口占住，占到了才说「服务在哪儿」—— 端口被占时不该先报成功
	listener, err := srv.Listen()
	if err != nil {
		fmt.Fprintf(os.Stderr, "监听失败：%v\n", err)
		os.Exit(1)
	}
	fmt.Printf("go-harness 在 http://%s（Ctrl+C 退出）\n", listener.Addr())

	if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "服务挂了：%v\n", err)
		os.Exit(1)
	}
}
