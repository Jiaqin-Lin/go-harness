# go-harness 交接文档

> 写于 2026-09-24。用于开新 session 继续。

## 一句话状态

DeepSeek 已接通，工具 loop 端到端跑通并实测成功；**图片支持已做完并实测**（read 能读图、wire 能把图送出去）；**会话管理已做完并实测**（新建 / 继续 / 列出，`internal/session`，格式对齐 pi）。**产品方向改成 web 版，TUI 不做了**（2026-09-24 定），所以 `main.go` 那套 bubbletea 是过渡产物，`tui.Emit` 仍是空函数。

---

## 一、现在什么能用，什么不能用

| 部分 | 状态 |
|---|---|
| `provider`（DeepSeek 流式接入） | ✅ 能用，实测跑通 |
| `tools`（read / bash / write / edit） | ✅ 能用，有测试；read 现在也能读图 |
| `core`（loop） | ✅ 能用 |
| **图片** | ✅ 已实现并实测：read 读图（png/jpeg/gif/webp/bmp，超限自动缩放）、wire 带图（tool 消息和 user 消息都带得动）。**实测跑的是 png，webp/bmp 的 API 接受度没测** |
| **`session`（会话管理）** | ✅ 能用，有单测 + 端到端实测：新建 / 继续 / 列出，落在 `~/.go-harness/sessions/` |
| **`tui`（渲染）** | ❌ 不做了，改 web 版。`tui.Emit` 仍是空函数（`internal/tui/event.go:5`） |

**验证方式**：`main.go` 走的是 bubbletea TUI，需要真 TTY，没法脚本化驱动，所以有个手动命令行驱动 `cmd/session-e2e` 直接调 `core.Run`（⚠️ 它会打真模型、花真钱，只手动跑，见第六节）。当初的 Java CRUD 是用同类驱动在 `/tmp/java-crud` 跑的：**5 轮、7 次工具调用、0 错误**，产物重新 `javac` + `java` 复验过。

图片验证同一个路子：临时驱动直接调 `core.Run`，让模型 read 一张四色块图（颜色顺序故意反着排），报对了。**测的时候必须把 bash 工具摘掉**，不然模型会自己解码 PNG 对像素——那样测出来的是它会写脚本，不是它看得见（第一轮就是这么被骗过去的）。

---

## 二、关键事实（都是实测撞出来的，别重踩）

### 1. `reasoning_content` 必须回传（最要命的一条）

`deepseek-flash` 是 thinking 模式。回传 assistant 轮时如果**不带上 `reasoning_content`**，会直接 400：

```
The `reasoning_content` in the thinking mode must be passed back to the API.
```

**为什么容易漏**：用**真实的 tool_call id** 回传时不带它**能侥幸通过**（服务端按 id 在缓存里查回推理内容），换成伪造 id 立刻炸。所以本地小测试很容易测不出来，线上缓存一过期就完蛋。

**已实现**：推理内容存进 `ContentBlock{Kind: ThinkingBlock}`，`internal/provider/wire.go` 的 `assistantMessage()` 负责回传。

### 2. 只有 OpenAI 兼容接口

- ✅ `POST https://api.deepseek.com/chat/completions`
- ❌ `POST https://api.deepseek.com/v1/messages`（Anthropic 风格）→ **404**

### 3. 未知参数静默忽略

传 `bogus_param_xyz` 也返回 200。**参数名写错不会有任何提示**，排查时要留意。

### 4. 模型信息

```
deepseek-flash        → DeepSeek-V4.1-Flash，1M 上下文，输出上限 393216，input_modalities: [text, image]
deepseek-v4-pro       → 同规格但不支持图片输入
```

`usage` 字段：`prompt_tokens` / `completion_tokens` / `total_tokens` / `prompt_cache_hit_tokens`。
Prompt caching 实测有效（最后一轮 3712/4167 命中）。

### 5. 图片（已全部实测）

| 测什么 | 结果 |
|---|---|
| user 消息带图（content parts） | ✅ |
| **tool 消息里直接带图** | ✅ **能看，不用绕道** |
| 多轮之后图片还在不在 | ✅ |
| 2000x2000 / 185KB base64 | ✅ 没被拒 |

决定性测试：文件名 `/tmp/x7q.png`（无提示），4 个色块顺序故意用非彩虹非 RGB 的 **蓝/黄/红/绿**，模型答 `4个：蓝色,黄色,红色,绿色` —— 顺序全对，只能靠看。

**结论：不需要 pi 那套「把图片从 tool 消息提到后续 user 消息」的绕道**（pi 的 `requiresAssistantAfterToolResult` 兼容开关是为了照顾一堆 OpenAI 兼容厂商）。只接 DeepSeek 一家，直接放 tool 消息里，更简单也不往对话里塞合成消息。

### 6. macOS 剪贴板读图：不需要新依赖

`pngpaste` 没装，`osascript` 三行就够，`cmp` 验证逐字节一致：

```bash
osascript -e 'set f to (open for access POSIX file "/tmp/clip1.png" with write permission)' \
          -e 'write (the clipboard as «class PNGf») to f' \
          -e 'close access f'
```

### 7. 图片已实装（2026-09-24）

- 依赖 `golang.org/x/image v0.46.0`：缩放用 `draw.CatmullRom`，另外借了 bmp / webp 的解码器（顺带升了 x/sys、x/text）
- 上限照抄 pi：2000x2000、base64 ≤ 4.5MB、PNG 优先再 JPEG `80/70/55/40` 逐级降质、还超就按 0.75 缩尺寸
- 代码在 `internal/tools/image.go`；`read` 命中图片就返回 `[文本说明, 图片块]`，不走行数/字节预算
- **只有 BMP 的魔数弱**（`BM` 两个字母，以「BMW」开头的文本文件会被骗），所以保留了 pi 的 `isBmp` 头部校验。png/jpeg/gif/webp 的魔数够长够怪，没做额外校验
- 没用 pi 那套「把图片从 tool 消息提到后续 user 消息」的绕道，tool 消息直接带 content parts，实测能过
- **还没实测的**：webp/bmp 原样透传时 DeepSeek 收不收（png/jpeg/gif 实测没问题）；换成 `deepseek-v4-pro`（不支持图片输入）时会怎么报错
- wire 里 assistant 的 `content` 永远保持字符串，只有 tool / user 消息带图时才换成 content parts 数组

### 8. 会话文件（2026-09-24 实装）

JSONL，第一行 header，之后一行一个 entry。**`parentId` 记在 entry 上（树）**，从 leaf 沿 parentId 回溯再反转就是给模型的历史，分支 = 从旧节点再挂一个子节点。目录：`~/.go-harness/sessions/--<cwd 去掉开头 / 再把 / \ : 换成 - >--/`，文件名 `<ISO 时间戳，: 和 . 换成 ->_<32 位 hex>.jsonl`，**续跑靠 mtime 排序，文件名里的时间戳只是给人看的**。

- **文件在第一次落盘时才建**：打开就退出的会话在磁盘上不留东西（实测：连目录都不建）
- **落盘单位是一轮**（跟 pi 每条消息就落盘不同，是刻意的）：`core.Run` 返回后一次性 Append。core.Run 的两个出口都保证每个 tool_call 有配对，所以文件里不会出现半轮 —— 这就是不用抄 pi `runtime/` 那 7500 行恢复状态机的底气
- **文件里的 `message` 就是 `types.Message` 本身**（给它和 `ContentBlock` 加了小写 JSON tag），没引第二套持久化模型。实测回读之后 thinking 块原样还在 —— 下一轮把它当 `reasoning_content` 发出去，缓存过期也不会 400
- 读文件一律 `bufio.Reader.ReadBytes`，**别用 `bufio.Scanner`**：默认单行上限 64KB，而消息里可能带 base64 图片，第一行轻松超
- **还没做的**：不返回 `messageCount` / 首条消息（列表只有时间 + cwd + id）；没有并发保护（两个进程开同一个会话会互相覆盖）

### 9. 中断路径的孤儿 tool_call（2026-09-24 修）

模型出错 / 用户取消时 `core.Run` 原来带着已经流出来的 tool_call 直接 break，那些调用永远不会执行，历史里就留下没有配对的 tool_call → 下一轮请求 400，而且会被 session 固化到磁盘上。现在 break 之前用 `tools.FailUnstartedToolCalls` 给每个调用补一条报错的 tool_result。

同一条路上还修了一个：`provider/generate.go` 的 `apply` 在 done 事件上只接了 `StopReason`，把 `ErrorMessage` 丢了 —— 于是「流没给出结束原因就断了」（`deepseek.go:229`）这个原因到不了上层，文案里会错写成「用户取消了这一轮」。是 `TestRunPairsToolCallsWhenStreamBreaks` 逮到的。

---

## 三、文件地图

```
main.go                        读 .env → 建 client → 喂给 core.Run
internal/provider/
  deepseek.go    (233行)       DeepSeek 客户端 + SSE 流式解析
  wire.go        (173行)       harness 消息模型 <-> OpenAI 协议转换
  generate.go    ( 75行)       CallLLM：把增量事件折回一条 types.Message
internal/core/loop.go           agent loop
internal/tools/
  read.go / bash.go / write.go / edit.go / exec.go / registry.go
  image.go                    图片嗅探 + 解码 + 缩放重编码（read 读图走这儿）
internal/session/
  entry.go                     Header / Entry、id 生成、时间戳
  session.go                   Session：Append、leaf 回溯、落盘
  store.go                     cwd 编码、文件名、Create / Open / List / ContinueRecent
internal/types/                 全部类型定义（见第五节）
internal/tui/                   ⚠️ event.go 的 Emit 是空的
cmd/session-e2e/main.go         手动端到端驱动（⚠️ 见第六节「会花真钱」那条）
```

配置：`.env`（**已 gitignore**，含真 key）+ `.env.example`（脱敏，可提交）。
三个变量：`DEEPSEEK_API_KEY` / `DEEPSEEK_BASE_URL` / `DEEPSEEK_MODEL`。

---

## 四、已知问题 / 待办

### 待办 A：图片支持 ✅ 已完成（2026-09-24）

1. `read` 工具读图 ✅ 见 `internal/tools/image.go`
2. `wire.go` 补图片 ✅ tool / user 消息带图时 `content` 换成 content parts 数组（1、2 都有测试：`read_test.go` 的 `TestReadImage*`、`wire_test.go`）
3. TUI 粘贴 ❌ 随 TUI 一起没了 —— 改成 web 版后，粘贴在浏览器里是原生的，不用 `osascript` 那套
4. provider 发 `AssistantStartEvent` ✅ 发在 `deepseek.go` 流建立之后（HTTP 200 拿到、开始读 SSE 之前），有单测

**缩放方案拍板结果**：引 `golang.org/x/image`（对齐 pi）。另一条「不缩放只报错」的路没走 —— 4K 截图会直接读不了。

### 待办 B：`SendPartialResult` 给 bash 用（用户拍板：先跳过）

**等 web 版做实时输出时一起做**。现在没有任何消费者，事件发出来也看不见。

用户已确认方向：bash 是唯一合理的消费者（`read`/`write`/`edit` 都没中间结果）。

**两个坑**：
1. **会死锁** —— `bashOutput.Write`（`bash.go:140`）和 `bashOutput.text()`（`bash.go:195`）用同一把 `b.mu`，在 `Write` 里调 `text()` 是**自己锁自己**（Go 的 `sync.Mutex` 不可重入）。得抽一个不加锁的内部方法。
2. **必须节流** —— 每次 write 都发事件的话 `go test` 能吐几千次。按时间（~100ms）或字节数攒批。pi 用 `output-accumulator.ts` 干这个。

`exec.go:134` 的 `settled` 守卫是对的，不用动。

### 待办 C：渲染（原 TUI，现改 web）

TUI 不做了，`main.go` 那套 bubbletea 是过渡产物。事件流的消费端将来是 web：`types.Event` 里该填的字段都已填好，这边不用再动，照着渲染就行。

唯一要留意：**图片块要有降级渲染**（显示成 `[图片 800x600 png 45KB]` 占位），别把 `ImageBlock` 当空块跳过。

---

## 五、类型审计结果

逐个字段查过"谁填 / 谁读"，分三类：

### ① 填了但还没人读（等 TUI，不用动）

| 字段 | 谁填 |
|---|---|
| `Usage` 的 Input/Cached/Output/Total | provider（实测有值） |
| `Message.IsError` / `Event.IsError` | `tools/exec.go` |
| `Event.ToolFinalResult` | `tools/exec.go` |
| `Event.Messages` / `ToolResultMessages` | `core/loop.go` |
| `StopReason` 的 `NormalStop` / `ToolUserStop` | provider（loop 不看它，靠 `len(toolCalls)>0` 判断） |

### ② 定义了但没写入方（有明确未来用途，保留）

| 符号 | 未来用途 |
|---|---|
| `ImageBlock`/`ImageMime`/`ImageData` | ✅ 已用：read 返回图片块，wire 把图片发出去 |
| `SendPartialResult` / `ToolPartialEvent` / `Event.ToolPartialResult` | 待办 B，给 bash（先跳过，等 web 版） |
| `ToolResult.ShouldStop` | **用户明确要求保留**，以后给 hook 用 |
| `AssistantStartEvent` | ✅ 已发，见下面 ④ |

### ③ 可删清单（写于 2026-09-24，确认没写入方、也没读取方）

这几条是"**定义了但当前和可以预见的未来都不会有用途**"，等哪次清理代码时可以删掉。

| 符号 | 位置 | 为什么可删 | 删了会失去什么 |
|---|---|---|---|
| `Usage.CacheHitRate()` | `types/message.go:29` | **方法**，全项目 0 引用。算的是 `Cached/Input`，TUI 真要显示一行除法就够。而且 DeepSeek 返回的 `prompt_cache_miss_tokens` 满足 `Cached + Miss = Input` 恒成立，它只是把已有信息又包了一层 | 无。要显示命中率自己除一次 |
| `Message.ToolName` | `types/message.go:46` | 在 **tool_result 消息**上只写不读（写入点 `tools/exec.go:188`）。它存在是为了"某些厂商要求 tool 消息带 name"（pi 里对应 `requiresToolResultName` 兼容开关），**DeepSeek 不需要**，`wire.go:86` 发 tool 消息时只用了 `ToolCallID` | 无。注意别跟 `ContentBlock.ToolName` 搞混 —— 那个是必需的，用来构造 `tool_calls[].function.name` |
| `Message.IsError` | `types/message.go:47` | OpenAI 协议里 tool 消息**没有** is_error 字段，错误只能靠文本传达，所以 wire 上永不使用。本地只有测试在读（`exec_test.go:39,88`） | 若以后要**渲染持久化的对话历史**（而非实时事件流），或要接 Anthropic（它有 `is_error`），会需要它。只接 DeepSeek + 只做实时渲染的话不需要 |

### ④ 待决策：要么用要么删 —— 已定：留，并且发

`AssistantStartEvent`（`types/event.go:25`）现在在 `deepseek.go` 里，**流建立之后**发一次（HTTP 200 拿到、开始读 SSE 之前）。它跟 `MessageStartEvent` 语义**不重合**：后者在 HTTP 请求发出**之前**就发了，前者表示"流已建立"。DeepSeek 是推理模型，首 token 常要等几秒，UI 区分"正在连模型"vs"模型正在生成"是有意义的。有单测 `provider/deepseek_test.go`。

### ⑤ 看着像死代码、但**不能删**的

这两类容易被误判成"没用的枚举值"，其实不是：

- **`StopReason` 的 `NormalStop` / `ToolUserStop`** —— 没有任何代码分支读它们（loop 靠 `len(toolCalls)>0` 判断，不看 StopReason）。但 provider 必须把 `finish_reason` 映射到**某个**值上，删了就没法表示"正常结束"和"调了工具"这个状态。它们是状态词汇表，UI 将来要显示
- **`Message.Usage`** —— 四个字段填了但没人读。但这就是 token 统计本身，是这轮接入的目的之一

判断标准：**"没人分支"不等于"该删"** —— 如果这个字段是某个状态的唯一载体，那它有用；只有当信息在别处已经完整存在（上面 ③ 那三个）时才是冗余。

---

## 六、踩过的其他坑（避免重踩）

- **端到端测试会花真钱，别把它包成 `_test.go`** —— 打真模型的那条路在 `cmd/session-e2e`（手动跑的 main 包）。`go test ./...` 对 main 包**只编译不执行**，永远不会碰到它。别为了"顺手"改写成 `*_test.go`：那样每跑一次 `go test` 就烧一次钱。现有的 `_test.go` 全是离线的（provider 那几个用 `httptest` 起假服务器）

- **测读图必须把 bash 工具摘掉** —— 不摘的话模型会自己 `bash` 跑脚本解码 PNG 对像素，答案是对的，但证明不了它看得见（详见第一节的验证方式）
- **pi 的 `read` 工具返回 `[文本说明, 图片块]`**，并对不支持视觉的模型有兜底（省略图片 + 加一句说明）。这个兜底**没做**：只接 DeepSeek 一家，配的又是支持图片的 flash，等真换了不支持视觉的模型再说
- **不要用伪造的 tool_call id 测多轮** —— 会误报成 `reasoning_content` 错误，浪费一轮排查（详见第二节第 1 条）
- `usage` 只有在 `stream_options.include_usage=true` 时才在流末尾单独给一帧
- 流式分片里 `reasoning_content` 会先于 `content` 到达，且首帧可能是空串 `""`
- 判断用户取消：`errorEvent` 用 `ctx.Err() != nil` → 落 `AbortedStop` 且不带错误文案。**原来这里错落成 `ErrorStop` + "读流失败：context canceled"**，已修

---

## 七、参考实现

`/Users/reuben/Documents/pi`（TypeScript monorepo）：
- `packages/coding-agent/src/utils/clipboard-image.ts` — 跨平台剪贴板读图
- `packages/coding-agent/src/modes/interactive/interactive-mode.ts:2934` — `handleClipboardPaste`，粘贴→存文件→插路径
- `packages/coding-agent/src/core/tools/read.ts` — read 工具读图
- `packages/coding-agent/src/utils/image-resize-core.ts` — 缩放策略（2000x2000 / 4.5MB / JPEG 80）
- `packages/ai/src/api/openai-completions.ts:1376` — tool result 图片转 wire 格式
- `packages/ai/src/types.ts:547` — 流式事件模型（对比我们的事件定义）
