package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reuben/go-harness/internal/types"
)

func writeText(t *testing.T, dir, path, content string) (string, error) {
	t.Helper()
	res, err := executeWrite(context.Background(), dir, map[string]any{"path": path, "content": content})
	if err != nil {
		return "", err
	}
	if len(res.ContentBlock) != 1 || res.ContentBlock[0].Kind != types.TextBlock {
		t.Fatalf("期望一个文本块，实际 %+v", res.ContentBlock)
	}
	return res.ContentBlock[0].Text, nil
}

func writeFail(t *testing.T, dir, path, content string) error {
	t.Helper()
	if _, err := writeText(t, dir, path, content); err == nil {
		t.Fatal("期望报错，实际成功了")
	} else {
		return err
	}
	return nil
}

// 直接读磁盘，绕开 read 工具的行号格式
func fileContent(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("读 %s 失败：%v", name, err)
	}
	return string(b)
}

// 新建、覆盖、父目录不存在都得能写
func TestWriteCreateAndOverwrite(t *testing.T) {
	dir := t.TempDir()

	if msg, err := writeText(t, dir, "deep/nested/a.txt", "hello"); err != nil {
		t.Fatalf("新建文件不该出错：%v", err)
	} else if !strings.Contains(msg, "5 字节") {
		t.Errorf("回执该报 5 字节，实际 %q", msg)
	}
	if got := fileContent(t, dir, "deep/nested/a.txt"); got != "hello" {
		t.Errorf("文件内容 = %q，想要 %q", got, "hello")
	}

	if _, err := writeText(t, dir, "deep/nested/a.txt", "bye"); err != nil {
		t.Fatalf("覆盖文件不该出错：%v", err)
	}
	if got := fileContent(t, dir, "deep/nested/a.txt"); got != "bye" {
		t.Errorf("覆盖后内容 = %q，想要 %q", got, "bye")
	}
}

// 空 content 是合法值：建空文件、清空文件都得能办到
func TestWriteEmptyContent(t *testing.T) {
	dir := t.TempDir()

	if msg, err := writeText(t, dir, "empty.txt", ""); err != nil {
		t.Fatalf("写空文件不该出错：%v", err)
	} else if !strings.Contains(msg, "0 字节") {
		t.Errorf("回执该报 0 字节，实际 %q", msg)
	}
	if got := fileContent(t, dir, "empty.txt"); got != "" {
		t.Errorf("空文件的内容 = %q，想要空串", got)
	}

	if _, err := writeText(t, dir, "empty.txt", "x"); err != nil {
		t.Fatalf("先写点东西进去不该出错：%v", err)
	}
	if _, err := writeText(t, dir, "empty.txt", ""); err != nil {
		t.Fatalf("清空文件不该出错：%v", err)
	}
	if got := fileContent(t, dir, "empty.txt"); got != "" {
		t.Errorf("清空后内容 = %q，想要空串", got)
	}
}

// 参数不合法时的报错要能看出错在哪：缺参数、类型不对、指到目录
func TestWriteBadArgs(t *testing.T) {
	dir := t.TempDir()

	_, err := executeWrite(context.Background(), dir, map[string]any{"content": "x"})
	if err == nil || !strings.Contains(err.Error(), "path") {
		t.Errorf("缺 path 应报 path 的错，实际 %v", err)
	}

	_, err = executeWrite(context.Background(), dir, map[string]any{"path": "a.txt"})
	if err == nil || !strings.Contains(err.Error(), "content") {
		t.Errorf("缺 content 应报 content 的错，实际 %v", err)
	}

	_, err = executeWrite(context.Background(), dir, map[string]any{"path": "a.txt", "content": 123})
	if err == nil || !strings.Contains(err.Error(), "字符串") {
		t.Errorf("content 类型不对应说清是类型问题，实际 %v", err)
	}

	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	if err := writeFail(t, dir, "sub", "x"); !strings.Contains(err.Error(), "目录") {
		t.Errorf("path 指向目录应说清是目录，实际 %v", err)
	}
}

// path 首尾的空白要当没写，不能落一个带空格的文件名
func TestWriteTrimsPath(t *testing.T) {
	dir := t.TempDir()

	if _, err := writeText(t, dir, " a.txt ", "x"); err != nil {
		t.Fatalf("写文件不该出错：%v", err)
	}
	if got := fileContent(t, dir, "a.txt"); got != "x" {
		t.Errorf("a.txt 的内容 = %q，想要 %q", got, "x")
	}
	if _, err := os.Stat(filepath.Join(dir, " a.txt ")); err == nil {
		t.Error("落了一个带空格的文件名")
	}
}
