package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

const promptPrefix = "> "

// 续行缩进，宽度跟 promptPrefix 一样，多行输入的行首才对得齐
var continuationIndent = strings.Repeat(" ", len(promptPrefix))

// runDoneMsg 是跑完一轮之后回到 Update 的消息。结果由 onSubmit 自己存，这里只是报个信
type runDoneMsg struct{}

type Model struct {
	// 用户敲完回车，字从这里交出去。这一轮怎么跑、跑完存哪儿，都是外面的事
	onSubmit func(string)

	history []string

	input  textarea.Model
	busy   bool
	width  int
	height int

	// 输入框那行的上方常年留一行，只在有话说的时候填字
	notice string
}

// textarea 内部把总行数封在 maxLines 行，粘贴超出时多出来的行直接丢掉、不报错。
// 按它自己的算法预判会丢多少：粘贴的第一行并进光标所在行，所以是两边行数相加再减一。
func pasteOverflow(cur, pasted string) int {
	const maxLines = 10000
	total := strings.Count(cur, "\n") + strings.Count(pasted, "\n") + 1
	return max(total-maxLines, 0)
}

func NewModel(onSubmit func(string)) Model {
	in := textarea.New()
	in.Placeholder = "问点什么"
	in.Prompt = promptPrefix
	// 只有第一行带 > ，续行用空格补齐。每行都带的话，两行输入看起来就是两条独立的消息
	in.SetPromptFunc(len(promptPrefix), func(line int) string {
		if line == 0 {
			return promptPrefix
		}
		return ""
	})
	in.ShowLineNumbers = false
	// 默认的 99 会在第 100 行挡住换行键，粘贴大段代码时输入框高度也会被它夹住
	in.MaxHeight = 0
	in.SetHeight(1)
	in.Focus()

	// 回车留给「发送」，换行改绑 ctrl+J 和 alt+回车
	in.KeyMap.InsertNewline.SetKeys("ctrl+j", "alt+enter")

	return Model{onSubmit: onSubmit, input: in}
}

func (m Model) Init() tea.Cmd {
	return textarea.Blink
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(max(msg.Width-2, 1))
		return m, nil

	case runDoneMsg:
		m.busy = false
		return m, nil

	case tea.KeyMsg:
		// 粘贴会被 String() 包成 [...]，命中不了下面任何一个 case，所以先在这里看一眼
		if msg.Paste {
			if lost := pasteOverflow(m.input.Value(), string(msg.Runes)); lost > 0 {
				m.notice = fmt.Sprintf("[输入框最多 10000 行，这次粘贴有 %d 行没进去]", lost)
			}
		}

		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "ctrl+d":
			// 输入框空着才退出，不空会删除方标块所在的字符
			if m.input.Value() == "" {
				return m, tea.Quit
			}
		case "enter":
			if m.busy {
				return m, nil
			}
			return m.submit()
		case "ctrl+j", "alt+enter":
			m.input.InsertString("\n")
			m.syncInputHeight()
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.syncInputHeight()
	return m, cmd
}

// syncInputHeight 让输入框跟着内容长高，最多占满屏幕留一行
func (m *Model) syncInputHeight() {
	rows := strings.Count(m.input.Value(), "\n") + 1
	m.input.SetHeight(min(rows, max(m.height-1, 1)))
}

func (m Model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil
	}

	m.input.Reset()
	m.syncInputHeight()
	m.notice = ""
	m.history = append(m.history, promptPrefix+strings.ReplaceAll(text, "\n", "\n"+continuationIndent))
	m.busy = true

	// 跑一轮要好几秒，在这儿直接调界面就冻住了。tea.Cmd 是个「返回 Msg 的函数」，
	// bubbletea 拿它在别的 goroutine 里跑，跑完把 Msg 丢回 Update。
	// 等做流式输出再给它加事件回调，现在不用改。
	onSubmit := m.onSubmit
	return m, func() tea.Msg {
		onSubmit(text)
		return runDoneMsg{}
	}
}

func (m Model) View() string {
	// 输入框和提示行先占位置，剩下的才轮到历史。多减的那一行给终端最后一格，
	// 写满最后一格有些终端会滚屏
	extra := 1
	if m.notice != "" {
		extra++
	}
	room := max(m.height-m.input.Height()-extra, 0)
	lines := m.history
	if len(lines) > room {
		lines = lines[len(lines)-room:]
	}

	var sb strings.Builder
	for _, line := range lines {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	// 提示行贴着输入框，输入框占满屏幕时历史被裁光，提示也得看得见
	if m.notice != "" {
		sb.WriteString(m.notice)
		sb.WriteByte('\n')
	}
	if m.busy {
		sb.WriteString("...\n")
	}
	sb.WriteString(m.input.View())
	return sb.String()
}
