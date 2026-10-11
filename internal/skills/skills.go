package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/reuben/go-harness/internal/settings"
)

type Scope string

const (
	ScopeUser    Scope = "user"    // ~/.go-harness/skills
	ScopeProject Scope = "project" // <cwd>/.go-harness/skills
)

func (s Scope) Valid() bool { return s == ScopeUser || s == ScopeProject }

// Skill 是一个技能目录，磁盘上是 <root>/<目录名>/SKILL.md。
// 目录里的 scripts/ references/ assets/ 加载阶段不关心，模型读 SKILL.md 自己会找到
type Skill struct {
	Name        string // frontmatter 的 name；没写就用目录名
	Description string
	Path        string // SKILL.md 的绝对路径——模型 read 用的就是它
	Dir         string
	Scope       Scope
	Enabled     bool   // settings 里没有记录 = true
	Shadowed    bool   // 同名技能另一个 scope 也有一份，这份没生效
	Err         string // 读不了 / 重名；非空则不进 Effective，管理界面标出来
}

func skillMD(dir string) string { return filepath.Join(dir, "SKILL.md") }

// userRoot 跟 sessions/、projects.json 同一个 .go-harness 根
func userRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("找不到用户主目录：%w", err)
	}
	return filepath.Join(home, ".go-harness", "skills"), nil
}

func projectRoot(cwd string) string { return filepath.Join(cwd, ".go-harness", "skills") }

// Root 是 scope 对应的技能根目录。目录不存在不算错——首次运行就是这样
func Root(scope Scope, cwd string) (string, error) {
	if scope == ScopeProject {
		return projectRoot(cwd), nil
	}
	return userRoot()
}

// Scan 列出两个 scope 里的全部技能，含停用、被覆盖、读不出来的——管理界面要全量。
// 只看一层：<root>/<目录名>/SKILL.md，不递归
func Scan(cwd string) []Skill { return load(cwd) }

// Effective 是这一轮真正给模型看的清单。顺序是「先滤停用和读不了的，再按名字合并，
// 项目级盖用户级」——不能反：反了的话停用项目级那份不会露出用户级那份，
// 开关就成了死的
func Effective(cwd string) []Skill {
	var out []Skill
	for _, s := range load(cwd) {
		if s.Enabled && s.Err == "" && !s.Shadowed {
			out = append(out, s)
		}
	}
	return out
}

// Find 在指定 scope 里按名字找。scope 要显式给：同名技能两个 scope 各有一份时，
// 启停和删除都得打对那一份
func Find(scope Scope, cwd, name string) (Skill, bool) {
	for _, s := range load(cwd) {
		if s.Scope == scope && s.Name == name {
			return s, true
		}
	}
	return Skill{}, false
}

// ByName 在生效的技能里按名字找：被覆盖、停用、读不出来的都找不到。
// 强制技能拿它校验——模型能看到的才允许强制
func ByName(cwd, name string) (Skill, bool) {
	for _, s := range Effective(cwd) {
		if s.Name == name {
			return s, true
		}
	}
	return Skill{}, false
}

// maxBodyBytes 强制注入的正文上限。SKILL.md 正常几 KB，这条只是兜住
// 有人往里面塞整本书的情况
const maxBodyBytes = 256 << 10

// Body 返回剥掉 frontmatter 的正文，强制技能注入用
func (s Skill) Body() (string, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return "", err
	}
	body := parseMeta(data).Body
	if len(body) > maxBodyBytes {
		return cutBytes(body, maxBodyBytes) + "\n\n（正文太长，截断了）", nil
	}
	return body, nil
}

// ValidName 只管安装新建目录：技能名会变成目录名，得是一个干净的路径元素。
// 其它入口（启停、删除、强制）从不拿名字拼路径，一律先 Find 拿 Dir
func ValidName(name string) bool {
	if name == "" || name != strings.TrimSpace(name) || len([]rune(name)) > 64 {
		return false
	}
	if name == "." || name == ".." || strings.HasPrefix(name, ".") {
		return false
	}
	return !strings.ContainsAny(name, "/\\\x00")
}

// load 扫一遍并按当前状态（启停、重名、覆盖）标好每一份
func load(cwd string) []Skill {
	root, err := userRoot()
	if err != nil {
		return nil
	}
	out := scanRoot(root, ScopeUser)

	// cwd 就是家目录时两个 root 是同一个目录，再扫一遍每条技能会出现两行
	if project := projectRoot(cwd); filepath.Clean(project) != filepath.Clean(root) {
		out = append(out, scanRoot(project, ScopeProject)...)
	}

	// 排序在判重之前：谁赢谁输就看这个先后，稳不稳定直接体现在结果里
	sortSkills(out)

	conf, err := settings.Read()
	if err != nil {
		conf = settings.Settings{} // 配置坏了当全启用，见 state.go
	}
	for i := range out {
		out[i].Enabled = enabledIn(conf, out[i].Scope, cwd, out[i].Name)
	}

	markDuplicates(out)
	markShadowed(out)
	return out
}

func scanRoot(root string, scope Scope) []Skill {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil // 目录还没建过是常态，不是错误
	}

	var out []Skill
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue // .DS_Store、编辑器临时文件，以及安装失败留下的残骸
		}
		dir := filepath.Join(root, entry.Name())
		// 用 Stat 而不是 entry.IsDir()：跟着软链走，技能目录可以放在别处再链过来。
		// 删除时 RemoveAll 只摘链、不动源目录，这是有意的
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}

		s, err := loadDir(dir, scope, entry.Name())
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // 没有 SKILL.md 的目录（比如只放 scripts/ 的）不算技能
			}
			// 读不出来也列出来：管理界面要能告诉用户这个目录有问题
			out = append(out, Skill{
				Name: entry.Name(), Dir: dir, Path: skillMD(dir), Scope: scope,
				Err: fmt.Sprintf("SKILL.md 读不了：%v", err),
			})
			continue
		}
		out = append(out, s)
	}
	return out
}

// loadDir 读一个技能目录。安装完之后也走这里，所以它不填 Enabled/Shadowed，
// 那两个得看在不在清单里、有没有重名
func loadDir(dir string, scope Scope, fallbackName string) (Skill, error) {
	path := skillMD(dir)
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	m := parseMeta(data)
	name := m.Name
	if name == "" {
		name = fallbackName
	}
	return Skill{Name: name, Description: m.Description, Path: path, Dir: dir, Scope: scope}, nil
}

// sortSkills 按名字排，同名的按目录。顺序稳定是硬要求：这份清单是 system prompt
// 的一段，而 system prompt 是请求前缀，DeepSeek 拿它做缓存，抖动等于每轮多花钱
func sortSkills(skills []Skill) {
	slices.SortFunc(skills, func(a, b Skill) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.Dir, b.Dir)
	})
}

// markDuplicates 同一个 scope 里两个目录报同一个名字：排序靠前的赢，输家标 Err。
// 停用的不参与判——开关是按名字记的，两份都关着的时候再报个重名出来只会让人困惑
func markDuplicates(skills []Skill) {
	taken := map[Scope]map[string]string{} // scope → 名字 → 赢家的目录
	for i := range skills {
		s := &skills[i]
		if !s.Enabled || s.Err != "" {
			continue
		}
		if taken[s.Scope] == nil {
			taken[s.Scope] = map[string]string{}
		}
		if dir, ok := taken[s.Scope][s.Name]; ok {
			s.Err = fmt.Sprintf("和 %s 重名，这份没生效", dir)
			continue
		}
		taken[s.Scope][s.Name] = s.Dir
	}
}

// markShadowed 跨 scope 重名时项目级赢，给输家打标记。管理界面要把没生效的
// 那份也列出来，不标一下用户会以为它坏了
func markShadowed(skills []Skill) {
	projectWins := map[string]bool{}
	for _, s := range skills {
		if s.Enabled && s.Err == "" && s.Scope == ScopeProject {
			projectWins[s.Name] = true
		}
	}
	for i := range skills {
		s := &skills[i]
		if s.Enabled && s.Err == "" && s.Scope == ScopeUser && projectWins[s.Name] {
			s.Shadowed = true
		}
	}
}
