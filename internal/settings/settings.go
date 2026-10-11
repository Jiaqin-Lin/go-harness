// Package settings 管 ~/.go-harness/settings.json —— 这个项目里唯一一份配置文件。
//
// 读写都走这个包，不各写各的：两个使用方（技能、压缩）各拿一把锁去改同一个文件，
// 「读进来-改-写回去」会把先写的那次整个覆盖掉
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// omitzero 而不是 omitempty：空结构体（{}）用 omitempty 压不掉，
// 改过压缩阈值之后文件里会凭空多出一个空的 "skills": {}
type Settings struct {
	Skills  Skills  `json:"skills,omitzero"`
	Compact Compact `json:"compact,omitzero"`
}

// Skills 只记「停用」的：没有记录 = 启用，新装的技能默认就能用
type Skills struct {
	DisabledUser    []string            `json:"disabledUser,omitempty"`
	DisabledProject map[string][]string `json:"disabledProject,omitempty"` // 键是项目 cwd
}

type Compact struct {
	// At 是压缩的绝对触发点（token）。0 = 不设绝对线，只按「窗口 - reserve」算
	At int `json:"at,omitempty"`
}

func path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("找不到用户主目录：%w", err)
	}
	return filepath.Join(home, ".go-harness", "settings.json"), nil
}

// mu 护着读-改-写。光读不用拿锁：写是原子替换（tmp + rename），
// 读到的要么是旧版要么是新版
var mu sync.Mutex

// Read 文件不在（一次都没改过东西，是常态）就是空配置。坏了要把错带出去，
// 让写路径拒绝覆盖，读路径自己决定宽容到什么程度
func Read() (Settings, error) {
	p, err := path()
	if err != nil {
		return Settings{}, err
	}
	buf, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	var s Settings
	if err := json.Unmarshal(buf, &s); err != nil {
		return Settings{}, fmt.Errorf("%s 坏了：%w", p, err)
	}
	return s, nil
}

// Update 是唯一的写入口：读进来、交给 fn 改、原子写回去，全程一把锁。
// 配置读不出来就直接报错，不拿一份空配置盖掉用户已有的东西
func Update(fn func(*Settings)) error {
	mu.Lock()
	defer mu.Unlock()

	s, err := Read()
	if err != nil {
		return err
	}
	fn(&s)
	s.prune()
	return write(s)
}

// prune 剪掉空壳：文件里只该剩真正改过的东西，读的人和用户一眼能看明白。
// 剪干净之后「文件里什么都没有」和「文件不存在」是同一件事
func (s *Settings) prune() {
	if len(s.Skills.DisabledUser) == 0 {
		s.Skills.DisabledUser = nil
	}
	for cwd, names := range s.Skills.DisabledProject {
		if len(names) == 0 {
			delete(s.Skills.DisabledProject, cwd)
		}
	}
	if len(s.Skills.DisabledProject) == 0 {
		s.Skills.DisabledProject = nil
	}
}

// write 原子落盘（tmp + rename）：扫描随时会来读，不能读到半截
func write(s Settings) error {
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("建目录失败：%w", err)
	}
	buf, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(buf, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
