//go:build !darwin

package server

import "context"

// 别的系统上没有现成的原生选择器：报给前端，让它退回页面上那个目录浏览弹窗
func pickDirectoryNative(context.Context) (string, error) { return "", errNoPicker }
