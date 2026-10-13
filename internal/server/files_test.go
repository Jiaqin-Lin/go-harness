package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func fetchFiles(t *testing.T, base, cwd, q string) (filesResp, int) {
	t.Helper()
	resp, err := http.Get(base + "/api/files?cwd=" + url.QueryEscape(cwd) + "&q=" + url.QueryEscape(q))
	if err != nil {
		t.Fatalf("读文件列表失败：%v", err)
	}
	defer resp.Body.Close()
	var out filesResp
	if resp.StatusCode != http.StatusOK {
		return out, resp.StatusCode
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("文件列表不是合法 JSON：%v", err)
	}
	return out, resp.StatusCode
}

type filesResp struct {
	Files []fileDTO `json:"files"`
}

func pathsOf(files []fileDTO) []string {
	out := make([]string, 0, len(files))
	for _, file := range files {
		out = append(out, file.Path)
	}
	return out
}

// writeFile 在 cwd 下建一个文件（连目录）
func writeFile(t *testing.T, cwd, rel string) {
	t.Helper()
	path := filepath.Join(cwd, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListFilesMatchesPathAndName(t *testing.T) {
	base, cwd := newTestServer(t, &stubLLM{})
	for _, rel := range []string{
		"main.go",
		"internal/server/api.go",
		".git/config",       // 隐藏目录：不进去
		"node_modules/a.js", // 依赖目录：不进去
	} {
		writeFile(t, cwd, rel)
	}

	// 路径前缀命中：@internal 先给目录自己，往下才是里面的文件
	out, code := fetchFiles(t, base, cwd, "internal")
	if code != http.StatusOK {
		t.Fatalf("状态码 %d", code)
	}
	got := pathsOf(out.Files)
	if !slices.Equal(got, []string{"internal", "internal/server", "internal/server/api.go"}) {
		t.Errorf("按路径前缀该给三层，得到 %v", got)
	}
	if !out.Files[0].IsDir || out.Files[2].IsDir {
		t.Errorf("目录要带 isDir 标记：%+v", out.Files)
	}

	// 文件名命中：@api 时 relative path 不以前缀匹配，靠名字捞出来
	out, _ = fetchFiles(t, base, cwd, "api")
	if got := pathsOf(out.Files); !slices.Equal(got, []string{"internal/server/api.go"}) {
		t.Errorf("按文件名该找到 api.go，得到 %v", got)
	}

	// 空 query 是「列个大概」：只给前几条，但隐藏目录和依赖目录一律不出现
	out, _ = fetchFiles(t, base, cwd, "")
	for _, path := range pathsOf(out.Files) {
		if path == ".git" || path == "node_modules" {
			t.Errorf("不该列出 %s：%v", path, pathsOf(out.Files))
		}
	}
	if len(out.Files) == 0 {
		t.Error("空 query 也该给几条")
	}
}

func TestListFilesCapsMatches(t *testing.T) {
	base, cwd := newTestServer(t, &stubLLM{})
	for i := range maxFileMatches + 10 {
		writeFile(t, cwd, fmt.Sprintf("dir/file%03d.txt", i))
	}

	out, _ := fetchFiles(t, base, cwd, "dir")
	if len(out.Files) != maxFileMatches {
		t.Errorf("一次最多回 %d 条，回了 %d", maxFileMatches, len(out.Files))
	}
}

// 没有命中的时候要回空数组而不是 null：前端拿到的 files 直接 .length
func TestListFilesEmptyIsAnArray(t *testing.T) {
	base, cwd := newTestServer(t, &stubLLM{})
	writeFile(t, cwd, "main.go")

	out, code := fetchFiles(t, base, cwd, "查无此文件")
	if code != http.StatusOK {
		t.Fatalf("状态码 %d", code)
	}
	if out.Files == nil {
		t.Error("没有命中该给 []，给的是 null")
	}
}

func TestListFilesRejectsRelativeCwd(t *testing.T) {
	base, _ := newTestServer(t, &stubLLM{})
	if _, code := fetchFiles(t, base, "relative/path", ""); code != http.StatusBadRequest {
		t.Errorf("相对路径该 400，得到 %d", code)
	}
}
