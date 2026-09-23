package tools

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxLines   = 2000
	maxBytes   = 50 * 1024
	bufioSize  = 64 * 1024
	sniffBytes = 8 * 1024

	maxBashTimeout = 2_147_483_647 / 1000
	waitDelay      = 5 * time.Second
)

// cutRunes 把字符串截到不超过 maxBytes 字节，且不把一个 utf-8 字符切成两半
func cutRunes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if maxBytes >= len(s) {
		return s
	}

	// 如果 maxBytes 这个位置落在某个字符中间，往前退到它的起始字节
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

// stringArg 取字符串参数，不存在或者类型不对时返回空串
func stringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

// stringArgStrict 取字符串参数。没传、传 null、类型不对都报错，空串是合法值
func stringArgStrict(args map[string]any, key string) (string, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return "", fmt.Errorf("缺少 %s 参数", key)
	}
	v, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s 必须是字符串，收到 %v（类型 %T）", key, raw, raw)
	}
	return v, nil
}

// intArg 取整数参数。没传或者传了 null 返回 fallback
func intArg(args map[string]any, key string, fallback int) (int, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return fallback, nil
	}

	switch v := raw.(type) {
	case int:
		return v, nil
	case float64:
		if v != math.Trunc(v) {
			return 0, fmt.Errorf("%s 必须是整数，收到 %v", key, v)
		}
		if v > 1<<53 || v < -(1<<53) {
			return 0, fmt.Errorf("%s 超出可用范围，收到 %v", key, v)
		}
		return int(v), nil
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n, nil
		}
		return 0, fmt.Errorf("%s 必须是整数，收到 %q", key, v)
	default:
		return 0, fmt.Errorf("%s 必须是整数，收到 %v（类型 %T）", key, raw, raw)
	}
}

// 将模型传的 path 解析为绝对路径
func resolvePath(cwd, path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		homePath, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("无法展开 ~：%w", err)
		}

		if path == "~" {
			return homePath, nil
		}

		return filepath.Join(homePath, path[2:]), nil
	}

	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}

	return filepath.Join(cwd, path), nil
}
