# go-harness session 会话管理实现计划

> 写于 2026-09-24。参照 `/Users/reuben/Documents/pi`。

## 一句话

做**新建 / 继续 / 列出**三件事，格式对齐 pi 的主线实现（`packages/coding-agent/src/core/session-manager.ts`），数据结构用 **parentId 树 + JSONL**。预计新增 `internal/session/` 约 350 行 Go，加一处 `core/loop.go` 的前置修复。

**不抄** pi 的 `packages/session-backends/`（2100 行 conformance）和 `packages/agent/src/harness/`（7500 行恢复状态机）。那两块是 pi 的多后端 + 托管服务逼出来的，跟会话管理本身无关。

---

## 一、对齐 pi 的五个机制

### 1. 树，不是数组

pi 里 `parentId` 记在 **entry** 上，不记在 message 上（`session-manager.ts:144`）：

```ts
interface SessionEntryBase { type: string; id: string; parentId: string | null; timestamp: string; }
interface SessionMessageEntry extends SessionEntryBase { type: "message"; message: AgentMessage; }
```

`SessionManager` 持有 `fileEntries[]` + `byId Map` + `leafId`。**从树到 LLM context 两步**：从 `leafId` 沿 `parentId` 回溯到 root，reverse（`buildSessionPath`，`:334`）。分支就是"从旧节点再挂一个子节点"。

### 2. 目录和文件名（`session-manager.ts:476,949`）

```
~/.pi/agent/sessions/--<cwd 去掉开头 /，再把 / \ : 换成 - >--/<ISO时间戳，: 和 . 换成 - >_<session-id>.jsonl
```

`continueRecent` 靠 **mtime 排序**，零索引文件（`findMostRecentSession`，`:636`）。所以文件名带时间戳只是给人看的，不参与查找。

### 3. 第一条 assistant 之前不落盘（`:1026`）

```ts
const hasAssistant = this.fileEntries.some((e) => e.type === "message" && e.message.role === "assistant");
if (!hasAssistant) return;                    // 攒着，什么都不写
if (!this.flushed) { openSync(f, "wx"); /* 全量刷 */ }
else { appendFileSync(f, ...); }
```

效果：**打开就退出的 session 不留垃圾文件**。注意 header 也在 `fileEntries` 里，所以是跟着第一条 assistant 一起写出去的。

### 4. 坏行静默跳过（`:506`）

`parseSessionEntryLine` 解析失败返回 null，不抛。读的时候是流式逐行切，不是一次性读全文。

### 5. compaction 是 entry，不是原地改写

历史不可变。压缩 = 在路径上追加一条 `compaction` entry，带 `firstKeptEntryId` 指向保留起点；读的时候把更早的段落替换成 summary（`buildContextEntries`，`:418`，**替换规则本身只有 20 行**）。

这条现在不实现（DeepSeek flash 有 1M 上下文），但**格式要留好口子** —— 见第三节。

---

## 二、go-harness 的设计

### 数据结构

`internal/session/entry.go`：

```go
type Header struct {
    Type      string `json:"type"`       // 固定 "session"
    Version   int    `json:"version"`    // 1
    ID        string `json:"id"`
    Timestamp string `json:"timestamp"`  // ISO8601
    Cwd       string `json:"cwd"`
}

type Entry struct {
    Type      string          `json:"type"`           // 目前只有 "message"
    ID        string          `json:"id"`
    ParentID  *string         `json:"parentId"`       // nil = 根
    Timestamp string          `json:"timestamp"`
    Message   *types.Message  `json:"message,omitempty"`
}
```

**`types.Message` 一个字段都不加。** 时间戳在 entry 上，`Message` 里的角色/内容/wire 格式原样复用 —— 这样 JSONL 里的 `message` 就是 `wire.go` 已经在处理的那个结构，不引第二套模型。

### 文件格式

```jsonl
{"type":"session","version":1,"id":"3f2a...","timestamp":"2026-09-24T10:00:00.000Z","cwd":"/Users/reuben/x"}
{"type":"message","id":"a1b2c3d4","parentId":null,"timestamp":"2026-09-24T10:00:01.000Z","message":{"Role":"user",...}}
{"type":"message","id":"b2c3d4e5","parentId":"a1b2c3d4","timestamp":"...","message":{"Role":"assistant",...}}
```

（实际序列化用 struct tag `"role"` 之类的小写名，跟 pi 一致，不是 Go 默认的 `Role`。）

**id 生成对齐 pi 的 `generateId`（`:206`）**：4 字节 `crypto/rand` → 8 位 hex，撞了就重摇（最多 100 次）。零依赖，不引 uuid 库。session id 用 16 字节 → 32 位 hex。

### 包结构

```
internal/session/
  entry.go      Header / Entry 类型、JSON 编解码、id 生成
  session.go    Session 类型：Append、leaf 回溯、Messages()
  store.go      目录编码、文件名、Create / Open / List / ContinueRecent
```

### API

```go
func Create(cwd string) (*Session, error)
func Open(path string) (*Session, error)
func ContinueRecent(cwd string) (*Session, error)   // 没有就 Create
func List(cwd string) ([]Info, error)               // 按 mtime 倒序

func (s *Session) Append(msg *types.Message) string // 返回 entry id
func (s *Session) Messages() []*types.Message       // leaf 路径 → LLM context
func (s *Session) Cwd() string
func (s *Session) ID() string
func (s *Session) Path() string                     // 还没落盘时返回 ""
```

### 落盘时机：**以轮为单位**（跟 pi 不一样，这是刻意的）

pi 是每产生一条消息就 `appendMessage` 落盘。go-harness 落到 **`core.Run` 返回之后**，整轮一次写：

```go
newMessages := core.Run(ctx, []*types.Message{prompt}, conversation, llm.Generate, emit)
for _, m := range newMessages {
    sess.Append(m)
}
conversation.Messages = append(conversation.Messages, newMessages...)
```

**为什么**：`core.Run` 只有两种出口 —— 正常结束（每个 tool_call 都有配对的 tool_result）和错误中止（见第四节，修完也有配对）。所以**落盘单位是轮，就保证了文件永远落在一致状态**。pi 要花 `runtime/` 7500 行去解决的"进程死了 tool_call 悬空"，我们靠这个选择免费拿到。

代价：崩在轮中丢这一轮。可接受，跟 pi "第一条 assistant 之前不落盘"的容忍度是一个量级。

顺带，pi 那个 `hasAssistant` 判断也不需要了 —— **文件在第一次落盘时才创建**，`Session` 上一个 `flushed bool` 就够（第一次 `O_CREATE|O_EXCL` 写出 header + 全部 entry，之后纯 append，对齐 pi 的 `"wx"`）。

### List 的实现细节（一个真实的坑）

只需要**读每个文件的第一行**拿 header（有 cwd / id / timestamp 就够列表用），按 mtime 倒序。pi 的 `buildSessionInfo`（`:688`）会扫全文统计 `messageCount` / `firstMessage` / `allMessagesText`，那是给选择器搜索用的 —— **先不做**。

⚠️ 读第一行要用 `bufio.Reader`（`ReadBytes('\n')`），**不要用 `bufio.Scanner`** —— Scanner 默认单行上限 64KB，而 go-harness 的 message 里可能带 base64 图片，第一行轻松超。

### 按 cwd 分目录，cwd 存 header

现在 `main.go:51` 用启动时的 `os.Getwd()` 建 `SystemPrompt` 和 `Tools`。改成从 session 读：

```go
sess, _ := session.ContinueRecent(cwd)
allTools := tools.GetAllTools(sess.Cwd())
conversation := &types.Conversation{
    SystemPrompt: prompts.BuildSystemPrompt(sess.Cwd(), allTools),
    Tools:        allTools,
    Messages:     sess.Messages(),     // 恢复的历史
}
```

---

## 三、明确不做的（以及为什么可以不做）

| 不做 | 理由 |
|---|---|
| `Storage` / `SessionRepo` 抽象层 | 单一本地文件存储下是纯负债。想换存储时再抽不迟 |
| `runtime/` 恢复状态机、`OperationState`、replay 策略 | 解决的是"进程随时被杀、重启后 tool call 该重放还是跳过"，托管服务才需要 |
| 版本迁移链 | 我们自己就是 v1，没有历史数据要读 |
| `label` / `branch_summary` / `custom_message` / `model_change` 等 entry 类型 | 扩展生态的产物。留 `message` 一种够用 |
| `/export`、gist 分享、选择器搜索/重命名 UI | 交互层的活，跟存储无关 |
| compaction | 1M 上下文暂时用不上 |
| SQLite 后端、fork 并发协调 | 单进程本地文件不需要 |

**compaction 将来怎么加**（说明格式口子留对了）：加一个 `"compaction"` entry 类型 + `Messages()` 里加一段"找路径上最后一个 compaction，把 `firstKeptEntryId` 之前的丢掉换成 summary"。**不改格式，不需要迁移** —— 这正是把 compaction 做成 entry 而不是原地改写的收益。

---

## 四、前置修复：loop 的孤儿 tool_call

**这是现存 bug，session 之前就存在，session 会把它固化到磁盘上。**

`internal/core/loop.go:34`：

```go
if msg.StopReason == types.ErrorStop || msg.StopReason == types.AbortedStop {
    emit(types.Event{Type: types.TurnEndEvent, Message: msg})
    break        // ← 带着 tool_call 就 break 了，这些 call 永远不会被执行
}
```

而错误中止**确实可能发生在工具调用参数已经流了一部分之后** —— `deepseek.go:49`（读流途中出错）、`:229`（流在给出结束原因前就断了）两条路径下，`msg.ContentBlocks` 里已经攒了 `ToolBlock`。

**两个后果**：
1. 用户继续对话时，`conversation.Messages` 里有一条 assistant 带 tool_calls 但没有配对 tool_result → OpenAI 协议要求每个 tool_call 必须有对应的 tool 消息 → **DeepSeek 400**
2. 加了 session 之后，这条坏消息会被写进 JSONL，**每次恢复都带着它**

**修法**：在 break 之前，如果 `len(msg.ToolCalls()) > 0`，补一条 tool_result 消息。跟 `loop.go:44` 的 `FailTruncatedToolCalls`（LengthStop 分支）是对称的两条路，实现放 `tools/exec.go` 里挨着它：

```go
func FailUnstartedToolCalls(callContentBlocks []types.ContentBlock, reason string, emit types.EventSend) []*types.Message
```

文案大意：`工具 X 没有执行：上一轮被中断（<reason>）。需要的话请重新发起这次调用。`

⚠️ **错误只能靠这段文本传达** —— OpenAI 协议的 tool 消息没有 `is_error` 字段，`Message.IsError` 在 wire 上会被丢掉（HANDOFF 第五节 ③ 已经确认过）。所以文案必须自己说清楚。

`AbortedStop`（用户主动取消）也要补，理由一样：不补的话用户取消一次就没法继续对话了。

---

## 五、实现步骤

| 步 | 做什么 | 预估 |
|---|---|---|
| 0 | `core/loop.go` 孤儿 tool_call 修复 + 测试（喂一条 `ErrorStop` 且带 ToolBlock 的消息，断言补出了 tool_result） | ~60 行 |
| 1 | `session/entry.go`：Header / Entry 类型、JSON 编解码、id 生成、路径回溯 | ~130 行 |
| 2 | `session/store.go`：cwd 编码、文件名、`Create` / `Open` / `List` / `ContinueRecent` | ~140 行 |
| 3 | `session/session.go`：`Session`、`Append`、`flushed` 逻辑、`Messages()` | ~90 行 |
| 4 | `main.go` 接线：`ContinueRecent` → 用 `sess.Cwd()` 建 prompt/tools → `core.Run` 后 `Append` | 改 ~10 行 |

每步都能独立编译、独立测。步骤 1-3 不碰任何现存文件。

---

## 六、怎么验收

**单测**（对齐 pi 的 test 路子，但只测我们的最小面）：

- entry 的 JSON 往返：`Entry` 编解码后字段不丢
- 路径回溯：手搓一棵有分支的树（root → a → b，root → c），`leafId=c` 时 `Messages()` 只出 root + c
- 落盘时机：`Create` 之后不调 `Append`，磁盘上**没有文件**；`Append` 第一条之后才有
- 续跑：写一个 session，`ContinueRecent` 读回来，`Messages()` 跟写进去的一致
- 坏行：手动往文件里插一行 `not json`，`Open` 不报错、跳过它
- cwd 编码：`/Users/reuben/x` → `--Users-reuben-x--`；确认 `List` 只返回同 cwd 的会话

**端到端**（照 HANDOFF 的老办法，临时命令行驱动直接调 `core.Run`）：

1. 起一次，让模型在 `/tmp/session-test` 写个文件，退出
2. 再起一次，问"我刚才让你写了什么" —— 答对说明历史恢复正确
3. `ls ~/.go-harness/sessions/--tmp-session-test--/` 确认文件在，`cat` 出来确认格式和 pi 对得上
4. 只发一句就退出的那次，确认没留文件

⚠️ 端到端测的时候记着 HANDOFF 的教训：**测某件事的时候把无关工具摘掉**，不然模型会绕道自己想办法，证明不了被测的东西。

---

## 七、待定 / 已知代价

1. **图片 base64 直接进 JSONL。** pi 就是这样（`session-manager.ts` 全文 0 处提 image），我们照抄。一张 2000x2000 的图 185KB，读几张 session 文件就上兆。本地文件，可接受 —— 但要知道自己在接受什么。将来真要治，方向是把图片落成 sidecar 文件、JSONL 里存路径。
2. **session 目录写死 `~/.go-harness/sessions/`**，不加配置项。改 web 版要能配的时候再加，一行的事。
3. **`List` 不返回 `messageCount` / `firstMessage`**，列表只能显示时间 + cwd + id。要显示首条消息就得读全文 —— 等真有选择器 UI 再说。
4. **没有并发保护。** 两个进程开同一个 session 会互相覆盖。pi 在这里也是靠约定（README 明说"host 自己保证单写者"），单进程 CLI 不需要考虑。
