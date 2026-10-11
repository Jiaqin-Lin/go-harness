package prompts

import (
	"os"
	"path/filepath"
	"strings"
)

// agentsFile 项目约定的文件名。只认项目根这一份：不往上找、不看用户级
const agentsFile = "AGENTS.md"

// ReadAgentsMD 读 <cwd>/AGENTS.md。没有、读不了、空白文件都返回空串——
// 没有约定是常态，不是错误，调用方看到空串就不拼这一段
func ReadAgentsMD(cwd string) string {
	buf, err := os.ReadFile(filepath.Join(cwd, agentsFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(buf))
}
