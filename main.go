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

	// 启动目录是「可能被当成项目」的默认值：目录选择器从这儿起
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "拿不到启动时目录：%v\n", err)
		os.Exit(1)
	}

	srv := server.New(server.Config{
		Port:     *port,
		Cwd:      cwd,
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
