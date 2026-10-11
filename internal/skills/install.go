package skills

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var (
	ErrSkillExists = errors.New("已经有同名的技能")
	ErrInvalidZip  = errors.New("这个 zip 装不了")
)

const (
	// maxZipEntries 条目数上限，兜住「几万个空文件」这种包
	maxZipEntries = 500
)

// maxUnzipBytes 解开后总量的上限，兜住解压炸弹。是变量只为测试能把它调小
var maxUnzipBytes int64 = 64 << 20

// InstallZip 把一个技能包解到 <root>/<名字>/。zip 已经由上层从 base64 解出来了，
// filename 只在平铺 zip（没有顶层目录）时用来取名
func InstallZip(scope Scope, cwd, filename string, data []byte, overwrite bool) (Skill, int, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Skill{}, 0, fmt.Errorf("%w：不是合法的 zip", ErrInvalidZip)
	}

	name, strip, err := zipLayout(r, filename)
	if err != nil {
		return Skill{}, 0, err
	}

	root, err := Root(scope, cwd)
	if err != nil {
		return Skill{}, 0, err
	}
	dir := filepath.Join(root, name)

	if _, err := os.Lstat(dir); err == nil {
		if !overwrite {
			return Skill{}, 0, fmt.Errorf("%w：%s", ErrSkillExists, name)
		}
		// 覆盖是整份换掉：先删干净再解，免得新旧文件混在一起
		if err := os.RemoveAll(dir); err != nil {
			return Skill{}, 0, err
		}
	}

	files, err := unzip(r, dir, strip)
	if err != nil {
		os.RemoveAll(dir) // 解了一半的残骸不留着
		return Skill{}, 0, err
	}

	s, err := loadDir(dir, scope, name)
	if err != nil {
		os.RemoveAll(dir)
		return Skill{}, 0, fmt.Errorf("%w：装出来读不出 SKILL.md", ErrInvalidZip)
	}
	s.Enabled = Enabled(scope, cwd, s.Name)
	return s, files, nil
}

// zipLayout 定技能名，并给出解压时要剥掉的前缀。
//
// 根下就有 SKILL.md 的，整个包就是技能本身，不剥前缀、名字取文件名；
// 否则恰好一个顶层目录就剥掉它用目录名（GitHub 打的包都是 repo-main/ 这样），
// 多个顶层目录说明包里装的不止一个东西，不收
func zipLayout(r *zip.Reader, filename string) (name, strip string, err error) {
	tops := map[string]bool{}
	rootSkillMD := false
	for _, f := range r.File {
		p, ok := safeEntry(f.Name)
		if !ok {
			return "", "", fmt.Errorf("%w：包里有一个不安全的路径 %q", ErrInvalidZip, f.Name)
		}
		if skipEntry(p) {
			continue
		}
		// 目录条目（zip -r 会给每个目录存一条）按顶层目录算：它说明目录存在，
		// 不是「根下有个散文件」——不区分的话 zip -r 的包会被当成多顶层条目
		if !f.FileInfo().IsDir() && p == "SKILL.md" {
			rootSkillMD = true
		}
		top := p
		if i := strings.IndexByte(p, '/'); i >= 0 {
			top = p[:i]
		}
		tops[top] = true
	}

	switch {
	case rootSkillMD:
		name = strings.TrimSuffix(filepath.Base(filename), ".zip")
	case len(tops) == 1:
		for top := range tops {
			name = top
			strip = top + "/"
		}
	default:
		return "", "", fmt.Errorf("%w：包里得是一个技能的目录（一个顶层目录，里面放 SKILL.md），现在有多个顶层条目", ErrInvalidZip)
	}

	if !ValidName(name) {
		return "", "", fmt.Errorf("%w：技能名 %q 不合法（只能是普通名字，不能带路径分隔符、不能以点开头）", ErrInvalidZip, name)
	}
	return name, strip, nil
}

// unzip 把条目解到 dir。返回落盘的文件数，至少要有一个 SKILL.md
func unzip(r *zip.Reader, dir, strip string) (int, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}

	files := 0
	var total int64
	hasSkillMD := false

	for i, f := range r.File {
		if i >= maxZipEntries {
			return 0, fmt.Errorf("%w：条目太多（超过 %d 个）", ErrInvalidZip, maxZipEntries)
		}
		p, ok := safeEntry(f.Name)
		if !ok {
			return 0, fmt.Errorf("%w：包里有一个不安全的路径 %q", ErrInvalidZip, f.Name)
		}
		if skipEntry(p) {
			continue
		}

		// 顶层目录本身（或者不在顶层目录里的杂项条目）剥完是空的，跳过
		rel := p
		if strip != "" {
			rel = strings.TrimPrefix(p, strip)
			if rel == p {
				continue
			}
		}

		target := filepath.Join(dir, rel)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return 0, err
			}
			continue
		}
		// 软链之类的非常规条目直接跳过：包里塞一条指向外面的链，
		// 等于绕过上面刚做过的路径检查
		if !f.Mode().IsRegular() {
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return 0, err
		}
		n, err := writeEntry(f, target, maxUnzipBytes-total)
		if err != nil {
			return 0, err
		}
		total += n
		if total > maxUnzipBytes {
			return 0, fmt.Errorf("%w：解开后太大（超过 %d MB）", ErrInvalidZip, maxUnzipBytes>>20)
		}

		if rel == "SKILL.md" {
			hasSkillMD = true
		}
		files++
	}

	if !hasSkillMD {
		return 0, fmt.Errorf("%w：技能根目录下没有 SKILL.md", ErrInvalidZip)
	}
	return files, nil
}

func writeEntry(f *zip.File, target string, room int64) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	// zip 里带的权限位只信可执行那一位：技能包里的脚本要能直接跑
	mode := os.FileMode(0o644)
	if f.Mode()&0o111 != 0 {
		mode = 0o755
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return 0, err
	}
	defer out.Close()

	// 解压出来的实际大小以写进去的字节数为准，不信条目标称的大小
	return io.Copy(out, io.LimitReader(rc, room+1))
}

// safeEntry 归一化条目路径，不安全的直接判死。zip-slip 就在这儿挡：绝对路径、
// 反斜杠、.. 一律不收——整包作废，不留半截安装。
// 反斜杠不当作分隔符认：Windows 打的包用 \ 也一并拒掉，不猜
func safeEntry(name string) (string, bool) {
	name = strings.TrimSuffix(name, "/") // 目录条目
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return "", false
	}
	p := path.Clean(name)
	if p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return "", false
	}
	return p, true
}

// skipEntry 跳过不该进技能目录的东西：Finder 打的包里的 __MACOSX/ 和 .DS_Store
func skipEntry(p string) bool {
	return strings.HasPrefix(p, "__MACOSX/") || p == "__MACOSX" || path.Base(p) == ".DS_Store"
}
