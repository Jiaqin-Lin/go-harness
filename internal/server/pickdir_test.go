package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
)

// stubPick 换掉平台相关的那一步：测试里不能真弹一个对话框到用户屏幕上
func stubPick(t *testing.T, fn func(context.Context) (string, error)) {
	t.Helper()
	orig := pickDirectory
	pickDirectory = fn
	t.Cleanup(func() { pickDirectory = orig })
}

func pickOnce(t *testing.T, base string) (int, map[string]any) {
	t.Helper()
	code, body := postJSON(t, base, "/api/pick-directory", map[string]any{})
	if code != http.StatusOK {
		return code, nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("响应不是合法 JSON：%v（%s）", err, body)
	}
	return code, out
}

func TestPickDirectoryReturnsPath(t *testing.T) {
	base, _ := newTestServer(t, &stubLLM{})
	stubPick(t, func(context.Context) (string, error) { return "/tmp/选中的目录", nil })

	code, out := pickOnce(t, base)
	if code != http.StatusOK {
		t.Fatalf("状态码 %d", code)
	}
	if out["path"] != "/tmp/选中的目录" {
		t.Errorf("该把选中的路径交出来，得到 %v", out)
	}
	if _, ok := out["canceled"]; ok {
		t.Errorf("选出来了就不该带 canceled：%v", out)
	}
}

// 取消不是错：回 200 + canceled，前端什么都不做
func TestPickDirectoryCanceled(t *testing.T) {
	base, _ := newTestServer(t, &stubLLM{})
	stubPick(t, func(context.Context) (string, error) { return "", errPickerCanceled })

	code, out := pickOnce(t, base)
	if code != http.StatusOK || out["canceled"] != true {
		t.Errorf("取消该是 200 + canceled，得到 %d %v", code, out)
	}
}

// 没有原生选择器的机器（非 macOS）：501 是给前端的信号 —— 退回页面上的目录浏览
func TestPickDirectoryNoNativePicker(t *testing.T) {
	base, _ := newTestServer(t, &stubLLM{})
	stubPick(t, func(context.Context) (string, error) { return "", errNoPicker })

	if code, _ := postJSON(t, base, "/api/pick-directory", map[string]any{}); code != http.StatusNotImplemented {
		t.Errorf("该回 501，得到 %d", code)
	}
}

func TestPickDirectoryFailure(t *testing.T) {
	base, _ := newTestServer(t, &stubLLM{})
	stubPick(t, func(context.Context) (string, error) { return "", context.DeadlineExceeded })

	if code, _ := postJSON(t, base, "/api/pick-directory", map[string]any{}); code != http.StatusInternalServerError {
		t.Errorf("选不出来该回 500，得到 %d", code)
	}
}

// 对话框开着的时候再点：409，别叠第二个对话框
func TestPickDirectoryOnlyOneDialog(t *testing.T) {
	base, _ := newTestServer(t, &stubLLM{})
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	stubPick(t, func(context.Context) (string, error) {
		once.Do(func() { close(entered) })
		<-release // 第二次调用时这个 channel 已经关了，立刻返回
		return "/tmp/x", nil
	})

	var wg sync.WaitGroup
	wg.Go(func() { pickOnce(t, base) })
	<-entered

	if code, _ := postJSON(t, base, "/api/pick-directory", map[string]any{}); code != http.StatusConflict {
		t.Errorf("第二个该被挡成 409，得到 %d", code)
	}

	close(release)
	wg.Wait()

	// 放掉之后还能再开：锁不该一直占着
	code, out := pickOnce(t, base)
	if code != http.StatusOK || out["path"] != "/tmp/x" {
		t.Errorf("上一轮结束之后该能再选，得到 %d %v", code, out)
	}
}
