# go-harness 交接文档

> 写于 2026-09-24。用于开新 session 继续。

## 一句话状态

DeepSeek 已接通，工具 loop 端到端跑通并实测成功；**TUI 渲染还是空函数**，所以 `go run .` 交互式看不到任何输出；图片支持已调研完并验证了方案，尚未实现。

---

## 一、现在什么能用，什么不能用

| 部分 | 状态 |
|---|---|
| `provider`（DeepSeek 流式接入） | ✅ 能用，实测跑通 |
| `tools`（read / bash / write / edit） | ✅ 能用，有测试 |
| `core`（loop） | ✅ 能用 |
| **`tui`（渲染）** | ❌ **`tui.Emit` 是空函数**（`internal/tui/event.go:5`），事件发出去没人收 |
| **图片** | ❌ 未实现，且 `wire.go` 会**静默丢掉**图片块 |

**验证方式**：`main.go` 走的是 bubbletea TUI，需要真 TTY，没法脚本化驱动。当时是临时写了个命令行驱动（`cmd/demo`，跑完已删）直接调 `core.Run`，在 `/tmp/java-crud` 让模型写了个 Java CRUD：**5 轮、7 次工具调用、0 错误**，产物我自己重新 `javac` + `java` 复验过。

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

---

## 三、文件地图

```
main.go                        读 .env → 建 client → 喂给 core.Run
internal/provider/
  deepseek.go    (233行)       DeepSeek 客户端 + SSE 流式解析
  wire.go        (173行)       harness 消息模型 <-> OpenAI 协议转换
  generate.go    ( 75行)       CallLLM：把增量事件折回一条 types.Message
internal/core/loop.go           agent loop
internal/tools/                 read / bash / write / edit + registry
internal/types/                 全部类型定义（见第五节）
internal/tui/                   ⚠️ event.go 的 Emit 是空的
```

配置：`.env`（**已 gitignore**，含真 key）+ `.env.example`（脱敏，可提交）。
三个变量：`DEEPSEEK_API_KEY` / `DEEPSEEK_BASE_URL` / `DEEPSEEK_MODEL`。

---

## 四、已知问题 / 待办

### 待办 A：图片支持（调研完成，可直接动手）

1. **`read` 工具支持读图** —— 这是整条链的落点
   - 识别图片 → 解码 → 缩放 → 返回 `[文本说明块, 图片块]`
   - 参考 pi：`packages/coding-agent/src/core/tools/read.ts`
2. **`wire.go` 补图片** —— `ImageBlock` → `{"type":"image_url","image_url":{"url":"data:<mime>;base64,..."}}`
   - tool 消息的 `content` 要从 string 改成 content parts 数组
   - **注意：现在这里会静默丢图，不报错也不提示**
3. **TUI 粘贴**（可延后到做 TUI 时）—— Ctrl+V → `osascript` 读剪贴板 → 存临时文件 → 把**路径**插进输入框
4. **provider 发一次 `AssistantStartEvent`**

**缩放方案（需拍板）**：Go 标准库能解码 png/jpeg/gif，但**没有任何缩放函数**。
- 推荐 (a)：引 `golang.org/x/image`（有 `CatmullRom`/`BiLinear` 采样器，已验证可拉到 v0.46.0），对齐 pi 的策略：max 2000x2000、base64 ≤ 4.5MB、JPEG 质量 80
- (b)：不缩放，只设大小上限，超了报错 — 0 依赖但粘贴 4K 截图体验差

### 待办 B：`SendPartialResult` 给 bash 用（做 TUI 时一起）

用户已确认方向：bash 是唯一合理的消费者（`read`/`write`/`edit` 都没中间结果）。

**两个坑**：
1. **会死锁** —— `bashOutput.Write`（`bash.go:140`）和 `bashOutput.text()`（`bash.go:195`）用同一把 `b.mu`，在 `Write` 里调 `text()` 是**自己锁自己**（Go 的 `sync.Mutex` 不可重入）。得抽一个不加锁的内部方法。
2. **必须节流** —— 每次 write 都发事件的话 `go test` 能吐几千次。按时间（~100ms）或字节数攒批。pi 用 `output-accumulator.ts` 干这个。

`exec.go:134` 的 `settled` 守卫是对的，不用动。

### 待办 C：TUI 渲染

`tui.Emit` 要接上，把 `types.Event` 渲染出来。用户明确说**不急**，但要求"确保后续有机会消费"—— 目前所有会被 TUI 消费的字段都已填好，不用提前做任何事。

唯一要留意：**图片块要有降级渲染**（显示成 `[图片 800x600 png 45KB]` 占位），否则 `View()` 会当空块跳过。

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
| `ImageBlock`/`ImageMime`/`ImageData` | 待办 A，马上要用 |
| `SendPartialResult` / `ToolPartialEvent` / `Event.ToolPartialResult` | 待办 B，给 bash |
| `ToolResult.ShouldStop` | **用户明确要求保留**，以后给 hook 用 |
| `AssistantStartEvent` | 语义不重合：区分"请求已发出"(`MessageStartEvent`) 和"流已建立"（本事件）。**要么发要么删，别挂着当死代码** |

### ③ 可删清单（写于 2026-09-24，确认没写入方、也没读取方）

这几条是"**定义了但当前和可以预见的未来都不会有用途**"，等哪次清理代码时可以删掉。

| 符号 | 位置 | 为什么可删 | 删了会失去什么 |
|---|---|---|---|
| `Usage.CacheHitRate()` | `types/message.go:29` | **方法**，全项目 0 引用。算的是 `Cached/Input`，TUI 真要显示一行除法就够。而且 DeepSeek 返回的 `prompt_cache_miss_tokens` 满足 `Cached + Miss = Input` 恒成立，它只是把已有信息又包了一层 | 无。要显示命中率自己除一次 |
| `Message.ToolName` | `types/message.go:46` | 在 **tool_result 消息**上只写不读（写入点 `tools/exec.go:188`）。它存在是为了"某些厂商要求 tool 消息带 name"（pi 里对应 `requiresToolResultName` 兼容开关），**DeepSeek 不需要**，`wire.go:86` 发 tool 消息时只用了 `ToolCallID` | 无。注意别跟 `ContentBlock.ToolName` 搞混 —— 那个是必需的，用来构造 `tool_calls[].function.name` |
| `Message.IsError` | `types/message.go:47` | OpenAI 协议里 tool 消息**没有** is_error 字段，错误只能靠文本传达，所以 wire 上永不使用。本地只有测试在读（`exec_test.go:39,88`） | 若以后要**渲染持久化的对话历史**（而非实时事件流），或要接 Anthropic（它有 `is_error`），会需要它。只接 DeepSeek + 只做实时渲染的话不需要 |

### ④ 待决策：要么用要么删

| 符号 | 情况 |
|---|---|
| `AssistantStartEvent` | `types/event.go:25`。provider **从来没发过**它，所以现在是死代码。但它跟 `MessageStartEvent` 语义**不重合**：后者在 HTTP 请求发出**之前**就发了，前者表示"流已建立"。DeepSeek 是推理模型，首 token 常要等几秒，UI 区分"正在连模型"vs"模型正在生成"是有意义的。**建议 provider 在流建立后发一次（1 行），否则就该删掉**，别继续挂着 |

### ⑤ 看着像死代码、但**不能删**的

这两类容易被误判成"没用的枚举值"，其实不是：

- **`StopReason` 的 `NormalStop` / `ToolUserStop`** —— 没有任何代码分支读它们（loop 靠 `len(toolCalls)>0` 判断，不看 StopReason）。但 provider 必须把 `finish_reason` 映射到**某个**值上，删了就没法表示"正常结束"和"调了工具"这个状态。它们是状态词汇表，UI 将来要显示
- **`Message.Usage`** —— 四个字段填了但没人读。但这就是 token 统计本身，是这轮接入的目的之一

判断标准：**"没人分支"不等于"该删"** —— 如果这个字段是某个状态的唯一载体，那它有用；只有当信息在别处已经完整存在（上面 ③ 那三个）时才是冗余。

---

## 六、踩过的其他坑（避免重踩）

- **pi 的 `read` 工具返回 `[文本说明, 图片块]`**，并对不支持视觉的模型有兜底（省略图片 + 加一句说明）
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
