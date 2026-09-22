package tools

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"context"

	"github.com/reuben/go-harness/internal/types"
)

func BashTool(cwd string) *types.Tool {
	return &types.Tool{
		Name:    "bash",
		Snippet: "跑 shell 命令",
		Desc: fmt.Sprintf(
			"用 bash -c 跑一条命令，回 stdout 和 stderr，命令失败时带退出码。"+
				"输出太长时只留尾部：从最后一行往前收，收满 %d 行或 %d KB 就停，更早的内容不在结果里。"+
				"完整的输出写进临时文件，路径附在结果末尾，要全量就用 read 去读它。"+
				"每次调用都是新起的 shell，cd 只影响这一条命令。"+
				"timeout 按秒算，不填就不设超时。",
			maxLines, maxBytes/1024,
		),
		Args: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "要跑的命令，整条交给 bash -c，不用自己再套一层 bash -c",
				},
				"timeout": map[string]any{
					"type": "integer",
					"description": fmt.Sprintf(
						"最多跑多少秒，不填就不设超时；上限 %d 秒，负数或超过上限都报错。到点这条命令会被杀掉",
						maxBashTimeout),
				},
			},
			"required": []string{
				"command",
			},
		},
		Guidelines: []string{
			"不要跑 vim、top、less 这类要交互的命令，它们会一直挂着等输入。",
		},
		Execute: func(ctx context.Context, args map[string]any, _ types.SendPartialResult) (types.ToolResult, error) {
			return executeBash(ctx, cwd, args)
		},
	}
}

func executeBash(ctx context.Context, cwd string, args map[string]any) (types.ToolResult, error) {
	command := stringArg(args, "command")
	if command == "" {
		return types.ToolResult{}, fmt.Errorf("缺少 command 参数")
	}
	timeout, err := intArg(args, "timeout", 0)
	if err != nil {
		return types.ToolResult{}, err
	}
	if timeout < 0 {
		return types.ToolResult{}, fmt.Errorf("timeout 不能是负数，收到 %d", timeout)
	}
	if timeout > maxBashTimeout {
		return types.ToolResult{}, fmt.Errorf("timeout 最多 %d 秒，收到 %d", maxBashTimeout, timeout)
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()
	}

	bashOutput := &bashOutput{}
	defer bashOutput.close()

	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = cwd
	cmd.Stdout, cmd.Stderr = bashOutput, bashOutput
	cmd.WaitDelay = waitDelay
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	runErr := cmd.Run()
	text := bashOutput.text()

	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return types.ToolResult{}, errors.New(withStatus(text,
				fmt.Sprintf("[超过 %d 秒还没跑完，已经杀掉了]", timeout)))
		}
		return types.ToolResult{}, errors.New(withStatus(text, "[已取消]"))
	}

	if exit, ok := errors.AsType[*exec.ExitError](runErr); ok {
		return types.ToolResult{}, errors.New(withStatus(text, fmt.Sprintf("[退出码 %d]", exit.ExitCode())))
	}

	if runErr != nil && !errors.Is(runErr, exec.ErrWaitDelay) {
		return types.ToolResult{}, runErr
	}

	if text == "" {
		text = "（没有输出）"
	}

	return textResult(text), nil
}

type bashOutput struct {
	mu sync.Mutex

	buf            bytes.Buffer
	file           *os.File
	path           string
	tail           []byte
	tailCutMidLine bool
	totalByte      int
	newLines       int
}

func (b *bashOutput) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.file != nil {
		_ = b.file.Close()
		b.file = nil
	}
}

func (b *bashOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.totalByte += len(p)
	b.newLines += bytes.Count(p, []byte{'\n'})
	b.cutTail(p)

	if b.file == nil {
		if b.totalByte <= maxBytes && b.newLines <= maxLines {
			b.buf.Write(p)
			return len(p), nil
		}
		f, err := os.CreateTemp("", "go-harness-bash-*.log")
		if err != nil {
			return 0, err
		}
		b.file, b.path = f, f.Name()
		// 把先前的 buf 内容也写入文件
		if _, err := f.Write(b.buf.Bytes()); err != nil {
			return 0, err
		}
	}
	return b.file.Write(p)
}

func (b *bashOutput) cutTail(p []byte) {
	b.tail = append(b.tail, p...)
	// 字节上限：留最后 maxBytes 个字节
	cut := max(0, len(b.tail)-maxBytes)

	// 行数上限：从尾巴往前数 maxLines 个换行，再往前的一律丢掉
	lines := 0
	for i := len(b.tail) - 1; i >= cut; i-- {
		if b.tail[i] == '\n' {
			lines++
			if lines > maxLines {
				cut = i + 1
				break
			}
		}
	}

	// 必须留够 cut-1 的下标：cut 为 0 时下面会越界
	if cut == 0 {
		return
	}
	for cut < len(b.tail) && !utf8.RuneStart(b.tail[cut]) {
		cut++
	}

	b.tailCutMidLine = b.tail[cut-1] != '\n'
	b.tail = b.tail[cut:]
}

func (b *bashOutput) text() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.totalByte == 0 {
		return ""
	}

	body := string(b.tail)
	// 没截过的时候 tail 就是全部输出，截过则只有结尾一段
	if b.tailCutMidLine {
		if i := strings.IndexByte(body, '\n'); i >= 0 {
			body = body[i+1:] // 第一行是从中间切的，只剩半截，整行丢掉
		} else {
			body = "" // 尾巴全在同一行里，一行完整的都没有
		}
	}

	text := body
	if b.path == "" {
		return text
	}
	return withStatus(text, fmt.Sprintf("[输出太长，这里只给了结尾；完整输出 %d KB 在 %s]", b.totalByte/1024, b.path))
}

func withStatus(text, status string) string {
	if text == "" {
		return status
	}
	return text + "\n\n" + status
}
