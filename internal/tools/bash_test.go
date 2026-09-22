package tools

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reuben/go-harness/internal/types"
)

// seq 1 20000 的字节数：9*2 + 90*3 + 900*4 + 9000*5 + 10001*6
const seq20000Bytes = 108894

func execBash(t *testing.T, cwd, command string) (string, error) {
	t.Helper()
	res, err := executeBash(context.Background(), cwd, map[string]any{"command": command})
	if err != nil {
		return "", err
	}
	if len(res.ContentBlock) != 1 || res.ContentBlock[0].Kind != types.TextBlock {
		t.Fatalf("期望一个文本块，实际 %+v", res.ContentBlock)
	}
	return res.ContentBlock[0].Text, nil
}

func runBash(t *testing.T, command string) (string, error) {
	t.Helper()
	return execBash(t, t.TempDir(), command)
}

func mustRunBash(t *testing.T, command string) string {
	t.Helper()
	got, err := runBash(t, command)
	if err != nil {
		t.Fatalf("跑 %q 不该出错：%v", command, err)
	}
	return got
}

// 去掉 text() 追加的 [..] 提示，只留正文
func bashBody(t *testing.T, got string) string {
	t.Helper()
	if i := strings.LastIndex(got, "\n\n["); i >= 0 {
		return got[:i]
	}
	return got
}

// 从提示里抠出临时文件路径
func bashTempPath(t *testing.T, got string) string {
	t.Helper()
	const marker = "/go-harness-bash-"
	i := strings.Index(got, marker)
	if i < 0 {
		t.Fatalf("提示里没有临时文件路径：%q", got)
	}
	j := strings.IndexByte(got[i:], ']')
	if j < 0 {
		t.Fatalf("提示没闭合：%q", got[i:])
	}
	return got[strings.LastIndexByte(got[:i], ' ')+1 : i+j]
}

// 成功时不能报错、正文要原样回来：以前 Write 打在 nil 的 *os.File 上，
// 每次调用都返回 invalid argument、输出全丢
func TestBashSuccessDoesNotError(t *testing.T) {
	if got := mustRunBash(t, "echo hi"); got != "hi\n" {
		t.Errorf("得到 %q，想要 %q", got, "hi\n")
	}
}

// stdout 和 stderr 都要收，顺序不定所以分开断言
func TestBashCapturesStdoutAndStderr(t *testing.T) {
	got := mustRunBash(t, "echo out; echo err >&2")
	if !strings.Contains(got, "out") || !strings.Contains(got, "err") {
		t.Errorf("stdout 和 stderr 都该带上，实际 %q", got)
	}
}

// 有输出但没有内容时给一句说明，别让模型以为工具坏了
func TestBashNoOutput(t *testing.T) {
	if got := mustRunBash(t, "true"); got != "（没有输出）" {
		t.Errorf("得到 %q，想要 %q", got, "（没有输出）")
	}
}

// 非零退出码报错，文案里要带上 stderr 和退出码
func TestBashNonZeroExit(t *testing.T) {
	_, err := runBash(t, "echo oops >&2; exit 3")
	if err == nil {
		t.Fatal("非零退出码该报错")
	}
	if !strings.Contains(err.Error(), "oops") {
		t.Errorf("文案里该带上 stderr，实际 %q", err)
	}
	if !strings.Contains(err.Error(), "[退出码 3]") {
		t.Errorf("文案里该带上退出码，实际 %q", err)
	}
}

func TestBashMissingCommand(t *testing.T) {
	for _, args := range []map[string]any{{}, {"command": ""}, {"command": 123}} {
		if _, err := executeBash(context.Background(), t.TempDir(), args); err == nil {
			t.Errorf("参数 %v 该报错", args)
		}
	}
}

// 每次调用都是新 shell：cd 只影响这一条命令
func TestBashCwdNotPersisted(t *testing.T) {
	dir := t.TempDir()
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("解析临时目录失败：%v", err)
	}

	if _, err := execBash(t, dir, "cd /"); err != nil {
		t.Fatalf("cd 不该出错：%v", err)
	}

	got, err := execBash(t, dir, "pwd -P")
	if err != nil {
		t.Fatalf("pwd 不该出错：%v", err)
	}
	if strings.TrimSpace(got) != want {
		t.Errorf("cd 不该跨调用保持：期望 %q，实际 %q", want, strings.TrimSpace(got))
	}
}

// 长度恰好等于上限时 cut 会是 0，以前 b.tail[cut-1] 在这里越界 panic
func TestBashCutTailExactBytes(t *testing.T) {
	var b bashOutput

	b.cutTail(bytes.Repeat([]byte("a"), maxBytes))
	if len(b.tail) != maxBytes {
		t.Errorf("恰好 %d 字节不该裁，实际 %d", maxBytes, len(b.tail))
	}

	b.cutTail([]byte("a"))
	if len(b.tail) != maxBytes {
		t.Errorf("超 1 字节该裁回 %d，实际 %d", maxBytes, len(b.tail))
	}
}

// 两个上限都超：正文只留尾部，完整输出进临时文件
func TestBashTrimsToTail(t *testing.T) {
	got := mustRunBash(t, "seq 1 20000")
	body := bashBody(t, got)

	if n := strings.Count(body, "\n"); n > maxLines {
		t.Errorf("行数上限 %d，实际返回 %d 行", maxLines, n)
	}
	if len(body) > maxBytes {
		t.Errorf("字节上限 %d，实际返回 %d 字节", maxBytes, len(body))
	}
	if head, _, _ := strings.Cut(body, "\n"); head != "18001" {
		t.Errorf("该只留最后 %d 行（从 18001 开始），实际从 %q 开始", maxLines, head)
	}

	path := bashTempPath(t, got)
	defer os.Remove(path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读临时文件失败：%v", err)
	}
	if len(data) != seq20000Bytes {
		t.Errorf("临时文件该有全部 %d 字节，实际 %d", seq20000Bytes, len(data))
	}
}

// 只超字节不超行数：行数上限不该跟着乱裁
func TestBashByteCapOnly(t *testing.T) {
	// 300 行 × 200 字节 = 60000 字节
	got := mustRunBash(t, `for i in $(seq 1 300); do printf '%0199d\n' 0; done`)
	body := bashBody(t, got)

	if len(body) > maxBytes {
		t.Errorf("字节上限 %d，实际返回 %d 字节", maxBytes, len(body))
	}
	if n := strings.Count(body, "\n"); n > maxLines {
		t.Errorf("行数上限 %d，实际返回 %d 行", maxLines, n)
	}

	path := bashTempPath(t, got)
	defer os.Remove(path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读临时文件失败：%v", err)
	}
	if len(data) != 60000 {
		t.Errorf("临时文件该有全部 60000 字节，实际 %d", len(data))
	}
}

// 整段没有换行符时没有完整行可留，正文是空的，但临时文件必须是全的
func TestBashSingleLongLine(t *testing.T) {
	got := mustRunBash(t, `printf 'x%.0s' $(seq 1 60000)`)

	path := bashTempPath(t, got)
	defer os.Remove(path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读临时文件失败：%v", err)
	}
	if len(data) != 60000 {
		t.Errorf("临时文件该有全部 60000 字节，实际 %d", len(data))
	}

	// 正文为空时提示前面没有分隔符，这里不能拿 bashBody 判断
	if !strings.HasPrefix(got, "[输出太长") {
		t.Errorf("没有完整行可留时只该给提示，实际 %q", got[:min(len(got), 60)])
	}
}

func TestBashTimeoutArgs(t *testing.T) {
	bad := []struct {
		timeout any
		want    string
	}{
		{-1, "不能是负数"},
		{maxBashTimeout + 1, "最多"},
		{1.5, "必须是整数"},
	}
	for _, c := range bad {
		args := map[string]any{"command": "true", "timeout": c.timeout}
		_, err := executeBash(context.Background(), t.TempDir(), args)
		if err == nil {
			t.Errorf("timeout=%v 该报错", c.timeout)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("timeout=%v：错误里没有 %q，实际 %q", c.timeout, c.want, err)
		}
	}

	// 不传和传 0 都是不限时
	for _, timeout := range []any{nil, 0} {
		args := map[string]any{"command": "true", "timeout": timeout}
		if _, err := executeBash(context.Background(), t.TempDir(), args); err != nil {
			t.Errorf("timeout=%v 不该报错：%v", timeout, err)
		}
	}
}

// 到点要真把命令杀掉，不能只是返回个错误
func TestBashTimeoutKills(t *testing.T) {
	start := time.Now()
	_, err := executeBash(context.Background(), t.TempDir(),
		map[string]any{"command": "sleep 10", "timeout": 1})
	if err == nil {
		t.Fatal("超过 timeout 该报错")
	}
	if !strings.Contains(err.Error(), "超过 1 秒") {
		t.Errorf("该说是超时，实际 %q", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("该在 1 秒左右被杀掉，实际等了 %v", d)
	}
}
