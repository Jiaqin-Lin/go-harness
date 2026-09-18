package tools

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxLines  = 2000
	maxBytes  = 50 * 1024
	bufioSize = 64 * 1024
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

// intArg 取整数参数。模型可能给 number、int，也可能给字符串形式的数字
// 都接受；拿不到合法数字时返回 fallback
func intArg(args map[string]any, key string, fallback int) int {
	switch v := args[key].(type) {
	case float64:
		return clampToInt(v)
	case int:
		return v
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return fallback
}

func clampToInt(f float64) int {
	switch {
	case f > math.MaxInt32:
		return math.MaxInt32
	case f < math.MinInt32:
		return math.MinInt32
	}
	return int(f)
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
