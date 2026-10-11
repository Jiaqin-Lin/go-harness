package skills

import (
	"path/filepath"
	"slices"

	"github.com/reuben/go-harness/internal/settings"
)

// 技能的启停状态存在 ~/.go-harness/settings.json 里（见 internal/settings）。
//
// 只记「停用」的：没有记录 = 启用。新装的技能默认就能用，不用写文件；
// 重新启用就把名字删掉，文件里永远只剩用户真正关掉的那些

// Enabled 是热路径——构建 system prompt 时每个技能都要问一次。配置坏了
// 就当一个都没停用：配置文件的问题不该让会话聊不下去
func Enabled(scope Scope, cwd, name string) bool {
	s, err := settings.Read()
	if err != nil {
		return true
	}
	return enabledIn(s, scope, cwd, name)
}

func enabledIn(s settings.Settings, scope Scope, cwd, name string) bool {
	if scope == ScopeProject {
		return !slices.Contains(s.Skills.DisabledProject[filepath.Clean(cwd)], name)
	}
	return !slices.Contains(s.Skills.DisabledUser, name)
}

// SetEnabled 写是严格的：配置读不出来就直接报错，不拿一份空配置盖掉用户已有的东西
func SetEnabled(scope Scope, cwd, name string, enabled bool) error {
	return settings.Update(func(s *settings.Settings) {
		if scope == ScopeProject {
			cwd = filepath.Clean(cwd)
			if s.Skills.DisabledProject == nil {
				s.Skills.DisabledProject = map[string][]string{}
			}
			s.Skills.DisabledProject[cwd] = setDisabled(s.Skills.DisabledProject[cwd], name, enabled)
			return
		}
		s.Skills.DisabledUser = setDisabled(s.Skills.DisabledUser, name, enabled)
	})
}

// setDisabled 启用 = 把名字从停用清单里删掉，停用 = 加进去（幂等，重复点不会堆两条）
func setDisabled(names []string, name string, enabled bool) []string {
	if enabled {
		return slices.DeleteFunc(names, func(n string) bool { return n == name })
	}
	if slices.Contains(names, name) {
		return names
	}
	return append(names, name)
}
