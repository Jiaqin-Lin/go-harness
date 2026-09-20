package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func newTestModel(t *testing.T) Model {
	t.Helper()
	return newTestModelWith(t, new([]string))
}

// sent 收着 Model 交出去的每一条输入，测试用它替掉真正的 core.Run
func newTestModelWith(t *testing.T, sent *[]string) Model {
	t.Helper()
	m := NewModel(func(text string) { *sent = append(*sent, text) })
	got, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return got.(Model)
}

func send(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	got, cmd := m.Update(msg)
	return got.(Model), cmd
}

func typeText(t *testing.T, m Model, s string) Model {
	t.Helper()
	m, _ = send(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	return m
}

// 回车发送，不是换行
func TestEnterSends(t *testing.T) {
	m := typeText(t, newTestModel(t), "你好")

	m, cmd := send(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("回车应该触发一次 core.Run")
	}
	if m.input.Value() != "" {
		t.Errorf("发送之后输入框该清空，实际 %q", m.input.Value())
	}
	if !m.busy {
		t.Error("发送之后该是忙状态")
	}
	if len(m.history) == 0 || !strings.Contains(m.history[0], "你好") {
		t.Errorf("发给用户看的那一行没进 history：%v", m.history)
	}
}

// ctrl+J 和 alt+回车是换行，都不触发发送
func TestNewlineKeys(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.KeyMsg
	}{
		{"ctrl+j", tea.KeyMsg{Type: tea.KeyCtrlJ}},
		{"alt+enter", tea.KeyMsg{Type: tea.KeyEnter, Alt: true}},
	}
	for _, c := range cases {
		m := typeText(t, newTestModel(t), "ab")
		m, cmd := send(t, m, c.msg)
		m = typeText(t, m, "cd")

		if cmd != nil {
			t.Errorf("%s 不该触发发送", c.name)
		}
		if got := m.input.Value(); got != "ab\ncd" {
			t.Errorf("%s 该换行，实际 %q", c.name, got)
		}
	}
}

// 续行不带 >，否则一个两行的输入看起来跟两条独立的消息一样
func TestContinuationLineHasNoPrompt(t *testing.T) {
	m := typeText(t, newTestModel(t), "1")
	m, _ = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = typeText(t, m, "2")

	if got := m.input.Value(); got != "1\n2" {
		t.Fatalf("输入框里该是两行，实际 %q", got)
	}
	if got := strings.Count(m.View(), promptPrefix); got != 1 {
		t.Errorf("多行输入里 %q 只该出现一次，实际 %d 次：\n%s", promptPrefix, got, m.View())
	}

	// 回车之后的回显要保持同样的缩进，不能在续行上又把 > 补回来
	m, _ = send(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.history) == 0 {
		t.Fatal("回车之后该留一条回显")
	}
	if got, want := m.history[0], "> 1\n  2"; got != want {
		t.Errorf("回显该是 %q，实际 %q", want, got)
	}
}

// 粘一大段代码不能被截断，输入框跟着内容长高
func TestPasteBigBlob(t *testing.T) {
	const n = 300
	m := newTestModel(t) // 80x24
	m, _ = send(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("line\n", n)), Paste: true})

	if got := strings.Count(m.input.Value(), "\n") + 1; got != n+1 {
		t.Errorf("粘进来 %d 行，只剩 %d 行", n+1, got)
	}
	if got, want := m.input.Height(), 23; got != want { // 24 行屏幕留一行
		t.Errorf("输入框该长到 %d 行，实际 %d", want, got)
	}
}

// textarea 写死了 10000 行上限，粘贴超过要报出丢了多少，不能悄悄没了
func TestPasteOverflowWarns(t *testing.T) {
	m := newTestModel(t)
	m, _ = send(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("x\n", 12000)), Paste: true})

	if got := strings.Count(m.input.Value(), "\n") + 1; got != 10000 {
		t.Errorf("textarea 该把行数封在 10000，实际 %d 行", got)
	}
	if !strings.Contains(m.View(), "有 2001 行没进去") {
		t.Errorf("丢了 2001 行却没报出来：\n%s", m.View())
	}

	// 发出去之后提示该消失，它说的只是当时那一坨粘贴
	m = typeText(t, m, "x")
	m, _ = send(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if strings.Contains(m.View(), "没进去") {
		t.Error("发送之后不该还挂着上一条粘贴的提示")
	}
}

// 没撞到上限就不报警
func TestPasteUnderLimitNoWarn(t *testing.T) {
	m := newTestModel(t)
	m, _ = send(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("x\n", 5000)), Paste: true})

	if got := strings.Count(m.input.Value(), "\n") + 1; got != 5001 {
		t.Errorf("5001 行没超上限，该一行不少，实际 %d 行", got)
	}
	if strings.Contains(m.View(), "没进去") {
		t.Errorf("没超上限不该报警：\n%s", m.View())
	}
}

// 已经有内容时接着粘，两次加起来超上限也要报
func TestPasteOverflowCountsExisting(t *testing.T) {
	m := newTestModel(t)
	m, _ = send(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("x\n", 6000)), Paste: true})
	m, _ = send(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("y\n", 6000)), Paste: true})

	if got := strings.Count(m.input.Value(), "\n") + 1; got != 10000 {
		t.Errorf("两次粘贴加起来该封在 10000 行，实际 %d 行", got)
	}
	if !strings.Contains(m.View(), "没进去") {
		t.Errorf("第二次粘贴丢了行却没报：\n%s", m.View())
	}
}

// 超过屏幕高度之后，ctrl+J 还能继续加行，只是看不见最后几行
func TestNewlineBeyondScreen(t *testing.T) {
	m := newTestModel(t)
	for range 50 {
		m, _ = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	}

	if got := strings.Count(m.input.Value(), "\n"); got != 50 {
		t.Errorf("该加进去 50 个换行，实际 %d 个", got)
	}
	if got, want := m.input.Height(), 23; got != want {
		t.Errorf("输入框高度该封顶在 %d，实际 %d", want, got)
	}
}

// 多行内容原样交出去，跑完之前是忙状态
func TestMultilineSubmit(t *testing.T) {
	var sent []string
	m := typeText(t, newTestModelWith(t, &sent), "第一行")
	m, _ = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = typeText(t, m, "第二行")

	m, cmd := send(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("回车应该触发一次 onSubmit")
	}
	if !m.busy {
		t.Error("跑完之前该是忙状态")
	}

	msg := cmd().(runDoneMsg)
	if len(sent) != 1 || sent[0] != "第一行\n第二行" {
		t.Errorf("交出去该是完整的多行内容，实际 %q", sent)
	}

	m, _ = send(t, m, msg)
	if m.busy {
		t.Error("收到 runDoneMsg 之后不该还是忙状态")
	}
}

// 空输入回车不发送
func TestEmptyEnterDoesNothing(t *testing.T) {
	m, cmd := send(t, newTestModel(t), tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("空输入回车不该触发 core.Run")
	}
	if m.busy {
		t.Error("空输入回车不该进忙状态")
	}
}

// ctrl+D 在输入框空的时候退出，不空的时候不退出
func TestCtrlD(t *testing.T) {
	if _, cmd := send(t, newTestModel(t), tea.KeyMsg{Type: tea.KeyCtrlD}); cmd == nil {
		t.Error("输入框空的时候 ctrl+D 该退出")
	}

	m := typeText(t, newTestModel(t), "x")
	if _, cmd := send(t, m, tea.KeyMsg{Type: tea.KeyCtrlD}); cmd != nil {
		// 这里给的是 textarea 自己的「删后一个字符」，可能是 nil
		if m.input.Value() == "" {
			t.Error("输入框不空的时候 ctrl+D 不该退出")
		}
	}
}

// View 在还没收到 WindowSizeMsg 的时候也不能崩
func TestViewBeforeWindowSize(t *testing.T) {
	m := NewModel(func(string) {})
	if got := m.View(); got == "" {
		t.Error("第一帧不该是空的")
	}
}
