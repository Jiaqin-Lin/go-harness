// Package server 是 web 版的传输层：HTTP 端点 + SSE 事件流。
// 它只管「请求进来 -> 读会话文件 -> 把事件写出去」，对话怎么跑是 core 的事
package server

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/reuben/go-harness/internal/provider"
	"github.com/reuben/go-harness/internal/web"
)

type Config struct {
	Port int
	// Cwd 是服务进程的启动目录，目录选择器默认从这儿起
	Cwd string
	// Model 是配的模型名。前端的上下文占用要显示分母，得知道窗口多大
	Model    string
	Generate provider.GenerateFunction
}

// run 是「正在跑的那一轮」在服务进程里的样子。除了点「停止」用的取消函数，
// 它还挂着这一轮的队列：跑着的时候用户塞进来的消息排在这儿，等 loop 在工具批次
// 边界（或这一轮本来要停时）来取（见 internal/core 的 Queues）
type run struct {
	cancel context.CancelFunc
	queue  messageQueue
}

type Server struct {
	cfg Config

	// running 记着哪些会话正在跑一轮。同一个会话同时只允许一轮，
	// 再来一条直接 409 —— 两条支路同时往一个文件里追加会写乱
	mu        sync.Mutex
	boundPort int // Listen 真正绑上的端口，Host 白名单用
	running   map[sessionKey]*run
	titles    map[string]bool // 正在生成标题的会话文件，别为同一个会话付两次钱

	// picking 护着系统目录选择器：它一次只能开一个，叠出来的两个谁也说不清哪个算数
	picking sync.Mutex

	// seq 给排队消息发认领凭据。只在一次 run 里要唯一，够用
	seq atomic.Uint64
}

func New(cfg Config) *Server {
	return &Server{
		cfg:     cfg,
		running: map[sessionKey]*run{},
		titles:  map[string]bool{},
	}
}

// Listen 先把端口占住再返回 —— 占到了才轮到 main 说「服务在 ...」，
// 端口被占的时候不会先说成功再报错
//
// 绑 127.0.0.1 而不是 :port：这个进程手里有 bash 工具，等于一台本机 shell，
// 不能让它出现在局域网里
func (s *Server) Listen() (net.Listener, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.cfg.Port))
	if err != nil {
		return nil, err
	}
	// 以真正绑上的端口为准：-port 0 时端口是内核分的，Host 白名单得按它来，
	// 不然访问哪个地址都是 403，还看不出原因
	s.boundPort = listener.Addr().(*net.TCPAddr).Port
	return listener, nil
}

func (s *Server) Serve(listener net.Listener) error {
	return (&http.Server{Handler: s.Handler()}).Serve(listener)
}

func (s *Server) Handler() http.Handler {
	// Go 1.22 起 ServeMux 认方法，写操作没登记的动词自动 405
	mux := http.NewServeMux()
	s.routes(mux)

	static, err := fs.Sub(web.Static, "static")
	if err != nil {
		panic(fmt.Sprintf("静态资源目录不对：%v", err))
	}
	// 兜底放在最后，API 路径先匹配
	mux.Handle("/", noStore(http.FileServerFS(static)))

	return s.checkHost(requireJSONForPost(mux))
}

// checkHost 挡 DNS rebinding：恶意页面把自己的域名解析到 127.0.0.1 时，
// 浏览器发来的 Host 是那个域名而不是本机地址，这里直接拒。只在本地用，
// 允许的 Host 就两个
func (s *Server) checkHost(next http.Handler) http.Handler {
	port := s.boundPort
	if port == 0 {
		port = s.cfg.Port // 还没 Listen（只有测试会这么用）
	}
	allowed := map[string]bool{
		fmt.Sprintf("127.0.0.1:%d", port): true,
		fmt.Sprintf("localhost:%d", port): true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[r.Host] {
			http.Error(w, "Host 不对", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireJSONForPost 要求 POST 带 application/json。
// 跨站的 HTML form 只能发 urlencoded / multipart / text-plain，发不了 JSON；
// 跨站 fetch 想发 JSON 会先撞 preflight，而我们不回任何 CORS 头。两条路一起堵死。
// DELETE 不用这条：form 发不出 DELETE，fetch 发它又会触发 preflight
func requireJSONForPost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, "POST 只收 application/json", http.StatusUnsupportedMediaType)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// noStore 是给静态资源用的：本地工具没有 CDN，改完 js 刷新就该看到新的，
// 不该还要去清缓存
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
