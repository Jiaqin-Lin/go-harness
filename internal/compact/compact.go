// Package compact 把长会话折叠成摘要。
//
// 机制照 pi：token 数优先信 provider 报的用量（上一次请求的 prompt 有多大是它
// 自己说的），超过「窗口 - reserve」就压；切点从最新往前累加到 keepRecent，
// 只切在 user / assistant 消息上 —— tool_result 不能落在保留段的开头，
// 它得跟着自己的 tool_call。
//
// 压缩本身不动树：被摘要掉的消息还在会话文件里，只是不再进上下文。
package compact

import (
	"slices"

	"github.com/reuben/go-harness/internal/types"
)

// 默认值照 pi：reserve 是留给下一轮输出和工具结果的余量，
// keepRecent 是压缩之后原样保留的近期上下文
const (
	DefaultReserveTokens    = 16384
	DefaultKeepRecentTokens = 20000

	// DefaultCompactAt 是绝对触发点：上下文涨到这个数就先压。
	// 1M 窗口只按「窗口 - reserve」算的话要等到 983K 才压，那一刀得摘要掉
	// 近百万 token 的对话，又慢又贵；250K 就压一次便宜得多，余量也留够了
	DefaultCompactAt = 250_000
)

type Settings struct {
	ReserveTokens    int
	KeepRecentTokens int
	// CompactAt 是绝对触发点，0 表示只按窗口算
	CompactAt int
}

func DefaultSettings() Settings {
	return Settings{
		ReserveTokens:    DefaultReserveTokens,
		KeepRecentTokens: DefaultKeepRecentTokens,
		CompactAt:        DefaultCompactAt,
	}
}

// Limit 是实际会触发的那个数：窗口减掉 reserve（留给下一轮输出和工具结果的余量），
// 再被绝对触发点压低，哪个小算哪个。<= 0 表示不压。
//
// 界面上显示的也是它 —— 显示的数和判断用的数是同一个，就不会各说各话
func Limit(contextWindow int, settings Settings) int {
	if contextWindow <= 0 {
		return 0 // 窗口未知：没有分母，任何阈值都是瞎猜
	}
	limit := contextWindow - settings.ReserveTokens
	if settings.CompactAt > 0 && settings.CompactAt < limit {
		limit = settings.CompactAt
	}
	return limit
}

// ShouldCompact 说上下文是不是已经顶到该压的位置了
func ShouldCompact(contextTokens, contextWindow int, settings Settings) bool {
	limit := Limit(contextWindow, settings)
	return limit > 0 && contextTokens > limit
}

// ContextTokens 是当前上下文有多大：最后一条报得出用量的 assistant 消息给的
// input + output（input 是那次请求的 prompt，含 cache 命中；output 是紧接着要
// 追加进历史的那段），再加上它之后消息的估算值。
//
// 拿不到任何用量时整段估算
func ContextTokens(messages []*types.Message) int {
	index := -1
	tokens := 0
	for i, msg := range slices.Backward(messages) {
		if usage := usageOf(msg); usage != nil {
			index = i
			tokens = usage.Input + usage.Output
			break
		}
	}
	if index < 0 {
		return EstimateMessages(messages)
	}
	for _, msg := range messages[index+1:] {
		tokens += EstimateTokens(msg)
	}
	return tokens
}

// usageOf 只认正常收尾的 assistant 消息的用量：报错、中止的那次请求要么没发
// 出去，要么 prompt 大小早就不代表现在的上下文了
func usageOf(msg *types.Message) *types.Usage {
	if msg.Role != types.AssistantRole || msg.Usage == nil || msg.Usage.Input <= 0 {
		return nil
	}
	if msg.StopReason == types.ErrorStop || msg.StopReason == types.AbortedStop {
		return nil
	}
	return msg.Usage
}

// imageChars 是图片折成的字符数。不能按 base64 的长度算 —— 一张截图几百 KB，
// 除 4 就是几十万 token，估出来的数会直接把整个会话判成超限
const imageChars = 4800

// EstimateTokens 按 chars/4 估一条消息的 token 数，宁可估大：估小了模型那边
// 直接报超限，估大了只是早一点压缩
func EstimateTokens(msg *types.Message) int {
	chars := 0
	for _, block := range msg.ContentBlocks {
		switch block.Kind {
		case types.TextBlock:
			chars += len(block.Text)
		case types.ThinkingBlock:
			chars += len(block.Thinking)
		case types.ToolBlock:
			chars += len(block.ToolName) + len(block.Args)
		case types.ImageBlock:
			chars += imageChars
		}
	}
	return (chars + 3) / 4
}

func EstimateMessages(messages []*types.Message) int {
	total := 0
	for _, msg := range messages {
		total += EstimateTokens(msg)
	}
	return total
}

// FindCut 定下这一刀切在哪儿：从最新往前累加，累到 keepRecent 的那一条就是
// 预算耗光的位置，切点取它或其之后最近的合法位置。返回保留段第一条的下标，
// 这段不值得压就返回 -1。
//
// start 之后的 messages[start:] 才是可压的范围（前面那截上一轮已经压过了）
func FindCut(messages []*types.Message, start, keepRecent int) int {
	hit := -1
	total := 0
	for i := len(messages) - 1; i >= start; i-- {
		total += EstimateTokens(messages[i])
		if total >= keepRecent {
			hit = i
			break
		}
	}
	if hit < 0 {
		return -1
	}

	cut := -1
	for j := hit; j < len(messages); j++ {
		if canCutAt(messages[j]) {
			cut = j
			break
		}
	}
	// 耗尽点之后没有能切的位置（上下文结尾是工具结果）：退到它前面最近的那个，
	// 那几条工具结果得跟着自己的 tool_call 一起留下
	for j := hit - 1; cut < 0 && j > start; j-- {
		if canCutAt(messages[j]) {
			cut = j
		}
	}

	// cut <= start 说明左边没剩下可摘要的东西，这轮不用压
	if cut <= start {
		return -1
	}
	return cut
}

func canCutAt(msg *types.Message) bool {
	return msg.Role == types.UserRole || msg.Role == types.AssistantRole
}
