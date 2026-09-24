package tools

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reuben/go-harness/internal/types"
)

func editOnce(t *testing.T, dir, name string, edits ...map[string]any) (string, error) {
	t.Helper()
	list := make([]any, 0, len(edits))
	for _, e := range edits {
		list = append(list, e)
	}
	res, err := executeEdit(context.Background(), dir, map[string]any{"path": name, "edits": list})
	if err != nil {
		return "", err
	}
	if len(res.ContentBlock) != 1 || res.ContentBlock[0].Kind != types.TextBlock {
		t.Fatalf("期望一个文本块，实际 %+v", res.ContentBlock)
	}
	return res.ContentBlock[0].Text, nil
}

func editText(t *testing.T, dir, name string, edits ...map[string]any) string {
	t.Helper()
	msg, err := editOnce(t, dir, name, edits...)
	if err != nil {
		t.Fatalf("executeEdit 报错：%v", err)
	}
	return msg
}

func editFail(t *testing.T, dir, name string, edits ...map[string]any) error {
	t.Helper()
	if _, err := editOnce(t, dir, name, edits...); err == nil {
		t.Fatal("期望报错，实际成功了")
	} else {
		return err
	}
	return nil
}

func replace(oldText, newText string) map[string]any {
	return map[string]any{"oldText": oldText, "newText": newText}
}

// 1 单处替换：改到、回执对、其它行原样不动
func TestEditSingleReplace(t *testing.T) {
	dir, name := tempFile(t, "a\nb\nc\n")

	msg := editText(t, dir, name, replace("b", "B"))
	if !strings.Contains(msg, "替换了 1 处") {
		t.Errorf("回执该报替换 1 处，实际 %q", msg)
	}
	if got, want := fileContent(t, dir, name), "a\nB\nc\n"; got != want {
		t.Errorf("文件内容 = %q，想要 %q", got, want)
	}
}

// 2 一处换成多行、多行换成一处
func TestEditMultiLine(t *testing.T) {
	dir, name := tempFile(t, "keep\nold1\nold2\ntail\n")
	editText(t, dir, name, replace("old1\nold2", "new1\nnew2\nnew3"))
	if got, want := fileContent(t, dir, name), "keep\nnew1\nnew2\nnew3\ntail\n"; got != want {
		t.Errorf("多行换多行 = %q，想要 %q", got, want)
	}

	if _, err := writeText(t, dir, name, "head\nmid\ntail\n"); err != nil {
		t.Fatalf("重置文件失败：%v", err)
	}
	editText(t, dir, name, replace("head\nmid", "only"))
	if got, want := fileContent(t, dir, name), "only\ntail\n"; got != want {
		t.Errorf("多行换一行 = %q，想要 %q", got, want)
	}
}

// 3 newText 给空串就是删掉这段
func TestEditDeleteText(t *testing.T) {
	dir, name := tempFile(t, "a\nDROP\nb\n")
	editText(t, dir, name, replace("DROP\n", ""))
	if got, want := fileContent(t, dir, name), "a\nb\n"; got != want {
		t.Errorf("删掉一行后 = %q，想要 %q", got, want)
	}
}

// 4 多处在一次调用里改：下标按原文件算，先改后面的不能污染前面的
func TestEditMultipleEdits(t *testing.T) {
	// 第一个替换变长、第二个变短，顺着改就会把第二个的下标带偏
	dir, name := tempFile(t, "aaa\nmiddle\nccc\n")
	msg := editText(t, dir, name, replace("aaa", "aaaaaaaa"), replace("ccc", "c"))
	if !strings.Contains(msg, "替换了 2 处") {
		t.Errorf("回执该报替换 2 处，实际 %q", msg)
	}
	if got, want := fileContent(t, dir, name), "aaaaaaaa\nmiddle\nc\n"; got != want {
		t.Errorf("文件内容 = %q，想要 %q", got, want)
	}
}

// 5 传的时候后面的那处排在前面，结果也要一样
func TestEditOutOfOrderEdits(t *testing.T) {
	dir, name := tempFile(t, "aaa\nbbb\nccc\n")
	editText(t, dir, name, replace("ccc", "cc"), replace("aaa", "aa"))
	if got, want := fileContent(t, dir, name), "aa\nbbb\ncc\n"; got != want {
		t.Errorf("文件内容 = %q，想要 %q", got, want)
	}
}

// 6 找不到就报错，且一个字节都不许落盘
func TestEditOldTextNotFound(t *testing.T) {
	dir, name := tempFile(t, "hello\n")
	err := editFail(t, dir, name, replace("nope", "x"))
	if !strings.Contains(err.Error(), "第 1 处") || !strings.Contains(err.Error(), "找不到") {
		t.Errorf("报错该说清第几处找不到，实际 %v", err)
	}
	if got := fileContent(t, dir, name); got != "hello\n" {
		t.Errorf("报错后文件被改了：%q", got)
	}
}

// 7 oldText 在文件里出现多次：宁可报错也不能挑一处瞎改
func TestEditDuplicateOldText(t *testing.T) {
	dir, name := tempFile(t, "x\ndup\ny\ndup\n")
	err := editFail(t, dir, name, replace("dup", "D"))
	if !strings.Contains(err.Error(), "2 次") {
		t.Errorf("重复出现该报出现次数，实际 %v", err)
	}
	if got, want := fileContent(t, dir, name), "x\ndup\ny\ndup\n"; got != want {
		t.Errorf("报错后文件被改了：%q", got)
	}
}

// 8 两处编辑区域重叠：改哪一处都会把另一处的坐标搞乱，直接报错
func TestEditOverlappingEdits(t *testing.T) {
	dir, name := tempFile(t, "abcdef\n")
	err := editFail(t, dir, name, replace("abcd", "X"), replace("cdef", "Y"))
	if !strings.Contains(err.Error(), "重叠") {
		t.Errorf("重叠该报重叠，实际 %v", err)
	}
	if got, want := fileContent(t, dir, name), "abcdef\n"; got != want {
		t.Errorf("报错后文件被改了：%q", got)
	}
}

// 9 重叠报的「第几处」要是用户给的编号：定位时按文件位置排过序，不能拿排完的下标去报
func TestEditOverlapReportsOriginalNumbers(t *testing.T) {
	// 靠后那处先给、靠前那处后给，排完序正好是反的
	dir, name := tempFile(t, "aaa\nbbb\n")
	err := editFail(t, dir, name, replace("bbb", "Y"), replace("aaa\nbbb", "X"))
	if !strings.Contains(err.Error(), "第 2 处和第 1 处") {
		t.Errorf("重叠该按用户给的顺序报 第 2 处和第 1 处，实际 %v", err)
	}
}

// 10 CRLF 文件：改完还是 CRLF，没被碰的行也得留着 CRLF
func TestEditPreservesCRLF(t *testing.T) {
	dir, name := tempFile(t, "a\r\nb\r\nc\r\n")
	editText(t, dir, name, replace("b", "B"))
	if got, want := fileContent(t, dir, name), "a\r\nB\r\nc\r\n"; got != want {
		t.Errorf("CRLF 文件 = %q，想要 %q", got, want)
	}
}

// 11 CRLF 文件里 oldText 带的是 \n：模型抄过来的多半是 LF，得认
func TestEditCRLFFileWithLFOldText(t *testing.T) {
	dir, name := tempFile(t, "a\r\nb\r\nc\r\n")
	editText(t, dir, name, replace("a\nb", "a\nB"))
	if got, want := fileContent(t, dir, name), "a\r\nB\r\nc\r\n"; got != want {
		t.Errorf("文件 = %q，想要 %q", got, want)
	}
}

// 12 只有 \r 的老式换行也得原样留着
func TestEditPreservesCR(t *testing.T) {
	dir, name := tempFile(t, "a\rb\rc")
	editText(t, dir, name, replace("b", "B"))
	if got, want := fileContent(t, dir, name), "a\rB\rc"; got != want {
		t.Errorf("CR 文件 = %q，想要 %q", got, want)
	}
}

// 13 纯 LF 文件改完不能冒出 CR，也不能凭空多出一个结尾换行
func TestEditPreservesLF(t *testing.T) {
	dir, name := tempFile(t, "a\nb")
	editText(t, dir, name, replace("a", "A"))
	if got, want := fileContent(t, dir, name), "A\nb"; got != want {
		t.Errorf("LF 文件 = %q，想要 %q", got, want)
	}
}

// 14 文件开头的 BOM 是文件的一部分，不能顺手吃掉
func TestEditPreservesBOM(t *testing.T) {
	dir, name := tempFile(t, "\uFEFFhello\r\nworld\r\n")
	editText(t, dir, name, replace("hello", "hi"))
	if got, want := fileContent(t, dir, name), "\uFEFFhi\r\nworld\r\n"; got != want {
		t.Errorf("带 BOM 的文件 = %q，想要 %q", got, want)
	}
}

// 15 newText 里带 CRLF：模型抄了上下文过来时会这样，归一化到 LF 后统一换回去，不能插出 \r\r\n
func TestEditNormalizesNewTextEndings(t *testing.T) {
	dir, name := tempFile(t, "a\nb\nc\n")
	editText(t, dir, name, replace("b", "B1\r\nB2"))
	if got, want := fileContent(t, dir, name), "a\nB1\nB2\nc\n"; got != want {
		t.Errorf("LF 文件 = %q，想要 %q", got, want)
	}

	if _, err := writeText(t, dir, name, "a\r\nb\r\nc\r\n"); err != nil {
		t.Fatalf("重置文件失败：%v", err)
	}
	editText(t, dir, name, replace("b", "B1\r\nB2"))
	if got, want := fileContent(t, dir, name), "a\r\nB1\r\nB2\r\nc\r\n"; got != want {
		t.Errorf("CRLF 文件 = %q，想要 %q", got, want)
	}
}

// 16 单行文件里插多行：没有换行符可参考，就用 LF
func TestEditInsertIntoFileWithoutLineBreak(t *testing.T) {
	dir, name := tempFile(t, "one line")
	editText(t, dir, name, replace("one", "first\nsecond"))
	if got, want := fileContent(t, dir, name), "first\nsecond line"; got != want {
		t.Errorf("文件 = %q，想要 %q", got, want)
	}
}

// 17 path 首尾空白当没写，跟 write 一致
func TestEditTrimsPath(t *testing.T) {
	dir, name := tempFile(t, "x\n")
	editText(t, dir, " "+name+" ", replace("x", "y"))
	if got, want := fileContent(t, dir, name), "y\n"; got != want {
		t.Errorf("文件 = %q，想要 %q", got, want)
	}
}

// 18 参数不合法时的报错要能看出错在哪
func TestEditBadArgs(t *testing.T) {
	dir, name := tempFile(t, "x\n")
	ctx := context.Background()

	_, err := executeEdit(ctx, dir, map[string]any{"edits": []any{replace("x", "y")}})
	if err == nil || !strings.Contains(err.Error(), "path") {
		t.Errorf("缺 path 应报 path 的错，实际 %v", err)
	}

	_, err = executeEdit(ctx, dir, map[string]any{"path": name, "edits": "x"})
	if err == nil || !strings.Contains(err.Error(), "array") {
		t.Errorf("edits 不是数组该报数组的错，实际 %v", err)
	}

	_, err = executeEdit(ctx, dir, map[string]any{"path": name, "edits": []any{}})
	if err == nil || !strings.Contains(err.Error(), "至少") {
		t.Errorf("空 edits 该报至少一处，实际 %v", err)
	}

	_, err = executeEdit(ctx, dir, map[string]any{"path": name, "edits": []any{"not a map"}})
	if err == nil || !strings.Contains(err.Error(), "第 1 处") {
		t.Errorf("edits 元素类型不对该报第几处，实际 %v", err)
	}

	_, err = executeEdit(ctx, dir, map[string]any{"path": name, "edits": []any{replace("", "y")}})
	if err == nil || !strings.Contains(err.Error(), "oldText") {
		t.Errorf("oldText 空该报 oldText 的错，实际 %v", err)
	}

	// 改到不存在的文件：报错来自 os，别吞掉
	_, err = executeEdit(ctx, dir, map[string]any{"path": "nope.txt", "edits": []any{replace("x", "y")}})
	if err == nil {
		t.Error("改不存在的文件该报错")
	}
}

// 19 目标是个目录：不能真去 WriteFile
func TestEditPathIsDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	_, err := executeEdit(context.Background(), dir, map[string]any{
		"path":  "sub",
		"edits": []any{replace("x", "y")},
	})
	if err == nil {
		t.Error("path 指向目录该报错")
	}
}

// 20 required 要挂在 schema 根上：挂进 properties 就成了一个叫 required 的字段，
// 模型看到的是「path、edits 都不是必填」
func TestEditArgsSchemaRequired(t *testing.T) {
	args := EditTool(t.TempDir()).Args

	props, ok := args["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Args 里应该有 properties，实际 %+v", args)
	}
	if _, misplaced := props["required"]; misplaced {
		t.Error("required 挂到 properties 里去了")
	}

	required, ok := args["required"].([]string)
	if !ok {
		t.Fatalf("Args 根上应该有 required，实际 %+v", args)
	}
	for _, key := range []string{"path", "edits"} {
		if !slices.Contains(required, key) {
			t.Errorf("required = %v，少了 %s", required, key)
		}
	}
}
