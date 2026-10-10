package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// Project 是用户加进侧栏的项目目录。会话本身还是按 cwd 存在 sessions/ 下，
// 这个清单只记「用户加过哪些目录」—— cwd 编码不可逆（/a-b 和 /a/b 编出来
// 是同一个目录名），从 sessions 目录反推项目路径是反推不出来的
type Project struct {
	Path    string `json:"path"`
	AddedAt string `json:"addedAt"`
}

func projectsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("找不到用户主目录：%w", err)
	}
	return filepath.Join(home, ".go-harness", "projects.json"), nil
}

// projectsMu 护着清单的读-改-写。两个标签页同时加项目时，「读进来-改-再写回去」
// 会把先写的那次整个覆盖掉
var projectsMu sync.Mutex

// ListProjects 按加入顺序返回项目；清单还没建过就是空的
func ListProjects() ([]Project, error) {
	projectsMu.Lock()
	defer projectsMu.Unlock()
	return readProjects()
}

func readProjects() ([]Project, error) {
	path, err := projectsPath()
	if err != nil {
		return nil, err
	}
	buf, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var projects []Project
	if err := json.Unmarshal(buf, &projects); err != nil {
		return nil, fmt.Errorf("%s 坏了：%w", path, err)
	}
	return projects, nil
}

// AddProject 把目录加进清单，已经在里面就什么都不做。
// 目录存不存在这儿不管：加之前服务端会先看，而清单里的目录以后被删掉
// 也得能正常列出来（侧栏显示「目录不在了」）
func AddProject(path string) error {
	path = filepath.Clean(path)
	projectsMu.Lock()
	defer projectsMu.Unlock()

	projects, err := readProjects()
	if err != nil {
		return err
	}
	if slices.ContainsFunc(projects, func(p Project) bool { return p.Path == path }) {
		return nil
	}
	return writeProjects(append(projects, Project{Path: path, AddedAt: timestampNow()}))
}

// RemoveProject 把目录移出清单。只动清单，sessions/ 下的会话一条不删
func RemoveProject(path string) error {
	path = filepath.Clean(path)
	projectsMu.Lock()
	defer projectsMu.Unlock()

	projects, err := readProjects()
	if err != nil {
		return err
	}
	kept := slices.DeleteFunc(projects, func(p Project) bool { return p.Path == path })
	if len(kept) == len(projects) {
		return nil
	}
	return writeProjects(kept)
}

// writeProjects 原子落盘（tmp + rename）：侧栏随时会来读，不能读到半截
func writeProjects(projects []Project) error {
	path, err := projectsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("建目录失败：%w", err)
	}
	if projects == nil {
		projects = []Project{} // 全删光时写 []，不是 null
	}
	buf, err := json.MarshalIndent(projects, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(buf, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
