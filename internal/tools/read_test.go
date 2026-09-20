package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reuben/go-harness/internal/types"
)

// 建一个临时目录和一个文件，目录当 cwd 用
func tempFile(t *testing.T, content string) (dir, name string) {
	t.Helper()
	dir = t.TempDir()
	name = "f.txt"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("写临时文件失败：%v", err)
	}
	return dir, name
}

func readText(t *testing.T, dir, name string, args map[string]any) string {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	args["path"] = name

	res, err := executeRead(context.Background(), dir, args)
	if err != nil {
		t.Fatalf("executeRead 报错：%v", err)
	}
	if len(res.ContentBlock) != 1 || res.ContentBlock[0].Kind != types.TextBlock {
		t.Fatalf("期望一个文本块，实际 %+v", res.ContentBlock)
	}
	return res.ContentBlock[0].Text
}

func readFail(t *testing.T, dir, name string, args map[string]any) error {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	args["path"] = name

	if _, err := executeRead(context.Background(), dir, args); err == nil {
		t.Fatal("期望报错，实际成功了")
	} else {
		return err
	}
	return nil
}

// 1.1 全空行文件：以前返回空字符串，模型看不出是空行还是工具坏了
func TestReadAllBlankFile(t *testing.T) {
	cases := []struct {
		content string
		want    string
	}{
		{"\n", "1\t\n\n[已读到文件末尾，共 1 行]"},
		{"\n\n", "1\t\n2\t\n\n[已读到文件末尾，共 2 行]"},
		{"\n\n\n", "1\t\n2\t\n3\t\n\n[已读到文件末尾，共 3 行]"},
	}
	for _, c := range cases {
		dir, name := tempFile(t, c.content)
		if got := readText(t, dir, name, nil); got != c.want {
			t.Errorf("内容 %q：\n得到 %q\n想要 %q", c.content, got, c.want)
		}
	}
}

// 1.2 行号：空行和行号本身都要看得见
func TestReadLineNumbers(t *testing.T) {
	dir, name := tempFile(t, "a\n\nb\n")
	want := "1\ta\n2\t\n3\tb\n\n[已读到文件末尾，共 3 行]"
	if got := readText(t, dir, name, nil); got != want {
		t.Errorf("\n得到 %q\n想要 %q", got, want)
	}
}

// 1.3 读到末尾报总行数
func TestReadTotalLines(t *testing.T) {
	dir, name := tempFile(t, "a\nb\nc\n")
	got := readText(t, dir, name, map[string]any{"offset": 2})
	want := "2\tb\n3\tc\n\n[已读到文件末尾，共 3 行]"
	if got != want {
		t.Errorf("\n得到 %q\n想要 %q", got, want)
	}
}

// 没读完时给续读的 offset，不给总行数
func TestReadHasMore(t *testing.T) {
	dir, name := tempFile(t, "a\nb\nc\n")
	got := readText(t, dir, name, map[string]any{"limit": 2})
	want := "1\ta\n2\tb\n\n[已显示第 1-2 行，还有更多。用 offset=3 接着读。]"
	if got != want {
		t.Errorf("\n得到 %q\n想要 %q", got, want)
	}
}

// 接着上一次的 offset 读，能把剩下的读完
func TestReadContinueFromOffset(t *testing.T) {
	dir, name := tempFile(t, "a\nb\nc\n")
	got := readText(t, dir, name, map[string]any{"limit": 2, "offset": 3})
	want := "3\tc\n\n[已读到文件末尾，共 3 行]"
	if got != want {
		t.Errorf("\n得到 %q\n想要 %q", got, want)
	}
}

// 字节预算：行号占的字节要算进去，2000 行 40 字符要触发截断
func TestReadByteBudget(t *testing.T) {
	dir, name := tempFile(t, strings.Repeat("x", 40)+"\n"+strings.Repeat(strings.Repeat("y", 40)+"\n", 1999))

	got := readText(t, dir, name, nil)
	if !strings.Contains(got, "还有更多") {
		t.Fatalf("超过 %d KB 应该截断，实际没截：%q", maxBytes/1024, got[:min(len(got), 100)])
	}
	if strings.Contains(got, "[已读到文件末尾，共 ") {
		t.Error("截断了就不该报总行数，总行数没数完")
	}
	if len(got) > maxBytes+len("\n\n[已显示第 1-2000 行，还有更多。用 offset=2001 接着读。]") {
		t.Errorf("截断后仍然超出 %d 字节预算，实际 %d 字节", maxBytes, len(got))
	}
}

// 超长的一行只能拿到开头，剩下的取不到：offset 按行数算，定位不到行内
func TestReadOverlongLine(t *testing.T) {
	long := strings.Repeat("x", maxBytes+1000)
	dir, name := tempFile(t, long+"\ntail\n")

	got := readText(t, dir, name, nil)
	if !strings.Contains(got, "剩下的取不到") {
		t.Errorf("该说明剩下的取不到，实际尾部 %q", got[len(got)-80:])
	}
	if strings.Contains(got, "tail") {
		t.Error("第一行就超长时，tail 该留给下一次读")
	}
	if len(got) >= len(long) {
		t.Errorf("返回 %d 字节，比原始行 %d 字节还长，说明没有截断", len(got), len(long))
	}

	// 接着读能到下一行，超长行的后半截再也拿不到
	if next := readText(t, dir, name, map[string]any{"offset": 2}); next != "2\ttail\n\n[已读到文件末尾，共 2 行]" {
		t.Errorf("offset=2 该读到 tail，实际 %q", next)
	}
}

// 超长行在中间：先把前面的给出去，下一次单独给那一行的开头
func TestReadOverlongLineInMiddle(t *testing.T) {
	dir, name := tempFile(t, "head\n"+strings.Repeat("x", maxBytes+1000)+"\ntail\n")

	first := readText(t, dir, name, nil)
	if want := "1\thead\n\n[已显示第 1-1 行，还有更多。用 offset=2 接着读。]"; first != want {
		t.Errorf("第一次该只给 head：\n得到 %q\n想要 %q", first, want)
	}

	second := readText(t, dir, name, map[string]any{"offset": 2})
	if !strings.Contains(second, "剩下的取不到") {
		t.Errorf("第二次该给超长行的开头并说明剩下的取不到，实际尾部 %q", second[len(second)-80:])
	}
}

// 2.2 / 2.3 参数越界要报错，不能悄悄换成默认值
func TestReadBadArgs(t *testing.T) {
	dir, name := tempFile(t, "a\nb\nc\n")

	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"offset": 0}, "offset 从 1 数起"},
		{map[string]any{"offset": -1}, "offset 从 1 数起"},
		{map[string]any{"limit": 0}, "limit 至少是 1"},
		{map[string]any{"limit": -3}, "limit 至少是 1"},
		{map[string]any{"offset": 1.5}, "offset 必须是整数"},
		{map[string]any{"limit": "abc"}, "limit 必须是整数"},
		{map[string]any{"offset": true}, "offset 必须是整数"},
		{map[string]any{"limit": []any{500}}, "limit 必须是整数"},
		{map[string]any{"offset": 5}, "offset 5 超过了文件的总行数"},
	}

	for _, c := range cases {
		err := readFail(t, dir, name, c.args)
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("参数 %v：错误信息里没有 %q，实际是 %q", c.args, c.want, err)
		}
	}
}

// 2.4 limit 超过上限继续夹取，不报错
func TestReadLimitClamped(t *testing.T) {
	dir, name := tempFile(t, "a\nb\nc\n")
	got := readText(t, dir, name, map[string]any{"limit": 99999})
	if !strings.Contains(got, "[已读到文件末尾，共 3 行]") {
		t.Errorf("limit 超上限应该按上限处理并读完，实际 %q", got)
	}
}

// 2.1 没传和传了 null 都走默认值，不报错
func TestReadMissingArgsUseDefault(t *testing.T) {
	dir, name := tempFile(t, "a\nb\nc\n")
	for _, args := range []map[string]any{nil, {"offset": nil}, {"limit": nil}} {
		if got := readText(t, dir, name, args); !strings.HasPrefix(got, "1\ta") {
			t.Errorf("参数 %v：应该从第 1 行开始读，实际 %q", args, got)
		}
	}
}

// 2.9 二进制文件不倒进上下文
func TestReadBinary(t *testing.T) {
	dir, name := tempFile(t, "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	got := readText(t, dir, name, nil)
	if !strings.Contains(got, "二进制") {
		t.Errorf("二进制文件应该只回一句提示，实际 %q", got)
	}
}

func TestReadEmptyFile(t *testing.T) {
	dir, name := tempFile(t, "")
	got := readText(t, dir, name, nil)
	if !strings.Contains(got, "空文件") {
		t.Errorf("空文件应该给明确提示，实际 %q", got)
	}
}

func TestIntArg(t *testing.T) {
	dir, name := tempFile(t, "a\nb\nc\n")
	_ = dir
	_ = name

	ok := []struct {
		args map[string]any
		want int
	}{
		{map[string]any{}, 7},
		{map[string]any{"n": nil}, 7},
		{map[string]any{"n": 5}, 5},
		{map[string]any{"n": float64(5)}, 5},
		{map[string]any{"n": "5"}, 5},
		{map[string]any{"n": " 5 "}, 5},
		{map[string]any{"n": float64(-3)}, -3},
	}
	for _, c := range ok {
		got, err := intArg(c.args, "n", 7)
		if err != nil {
			t.Errorf("参数 %v：不该报错，实际 %v", c.args, err)
			continue
		}
		if got != c.want {
			t.Errorf("参数 %v：得到 %d，想要 %d", c.args, got, c.want)
		}
	}

	bad := []map[string]any{
		{"n": 1.5},
		{"n": "abc"},
		{"n": true},
		{"n": []any{500}},
		{"n": map[string]any{"a": 1}},
		{"n": 1e300},
	}
	for _, args := range bad {
		if _, err := intArg(args, "n", 7); err == nil {
			t.Errorf("参数 %v 该报错，实际没报", args)
		}
	}
}
