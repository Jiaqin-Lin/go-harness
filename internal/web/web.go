// Package web 只负责把前端文件带进二进制。
// 前端是原生 ES module + CSS，没有构建步骤，改完刷新页面就是新的
package web

import "embed"

//go:embed static
var Static embed.FS
