//go:build darwin

package server

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// chooseFolder 是 AppleScript 的原生目录选择器：弹出来的就是访达那个对话框，
// 选完给回来的是绝对路径 —— 正是浏览器自己给不了的那个东西。
// 提示语是写死的，AppleScript 里不拼任何外部来的文本
const chooseFolder = `POSIX path of (choose folder with prompt "选择项目目录")`

func pickDirectoryNative(ctx context.Context) (string, error) {
	// ctx 挂在请求上：页面关了就把 osascript 收掉，别让对话框一直挂着
	out, err := exec.CommandContext(ctx, "osascript", "-e", chooseFolder).Output()
	if err != nil {
		// 取消时 osascript 以 1 退出、stderr 是 "User canceled. (-128)"，
		// stdout 空着 —— 那不是失败
		var exit *exec.ExitError
		if errors.As(err, &exit) && strings.Contains(string(exit.Stderr), "-128") {
			return "", errPickerCanceled
		}
		return "", err
	}
	// POSIX path of 对目录会带尾斜杠，交给后面 Clean 就行
	if path := strings.TrimSpace(string(out)); path != "" {
		return path, nil
	}
	return "", errPickerCanceled
}
