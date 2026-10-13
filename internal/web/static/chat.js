// 消息渲染。落过盘的 entry 和正在流的那条走同一套函数 —— 一轮跑完把流式的
// 换成落盘的，视觉上不该有跳动

import { roleLabel, argsPreview, clip, numTokens, stripSkillBlock } from './shared.js';

// 参数原样转给 updateMessage：多一个字段（比如 entryId）就不用两边都改一遍 ——
// 少改一边的话，落盘的图片气泡会悄悄退回占位文字，谁都不报错
export function renderMessage(msg, opts = {}) {
  const el = document.createElement('div');
  updateMessage(el, msg, opts);
  return el;
}

// renderCompaction 画压缩点：这一刀把之前的历史折成了摘要。默认折起来 ——
// 摘要是给模型看的检查点，摆在这儿是给「从哪儿开始重放」一个交代
export function renderCompaction(entry, { folded = false } = {}) {
  const el = document.createElement('div');
  el.className = 'msg msg--compaction' + (folded ? ' is-folded' : '');

  const head = document.createElement('div');
  head.className = 'msg-head';
  const role = document.createElement('span');
  role.className = 'msg-role';
  role.textContent = '上下文压缩';
  const usage = document.createElement('span');
  usage.className = 'msg-usage';
  usage.textContent = `压缩前 ${numTokens(entry.compaction.tokensBefore)}`;
  head.append(role, usage);
  // 被更新的摘要取代的旧卡片自己也已折进新摘要
  if (folded) head.append(foldedNote());

  const details = document.createElement('details');
  details.className = 'thinking';
  const summary = document.createElement('summary');
  summary.textContent = '之前的对话折成了这段摘要';
  const body = document.createElement('div');
  body.className = 'bubble';
  body.append(markdown(entry.compaction.summary));
  details.append(summary, body);

  el.append(head, details);
  return el;
}

// updateMessage 整条重画：头（角色 / 用量 / 中止标记）和体都重建。
// 流式那几帧会反复调它，而用量、中止标记、错误边框都是落盘之后才有的东西 ——
// 只在建元素时算一次的话，这一轮刚跑完就是错的，得刷新页面才对。
// entryId 是图片要的：落盘的图得靠 entry + 块下标去 /api/image 取
export function updateMessage(el, msg, { streaming = false, folded = false, skill = '', entryId = '' } = {}) {
  el.className = `msg msg--${messageKind(msg)}` + (folded ? ' is-folded' : '');

  const head = document.createElement('div');
  head.className = 'msg-head';
  const role = document.createElement('span');
  role.className = 'msg-role';
  role.textContent = roleLabel(msg);
  head.append(role);
  // 这一轮是用户点了技能发的：技能名落盘在 entry 上，历史重画时也认得出来。
  // 用 title 而不是多一段文字，是让「选了什么」在头里只占一个角标
  if (skill) {
    const used = document.createElement('span');
    used.className = 'skill-badge skill-badge--used';
    used.textContent = '技能 ' + skill;
    used.title = '这一轮指定用这个技能（正文跟着这条消息一起进了对话）';
    head.append(used);
  }
  if (msg.usage) {
    // 缓存命中率 = cached / input（DeepSeek 的 prompt_tokens 含命中部分）
    const rate = msg.usage.input > 0 ? `缓存 ${Math.round((msg.usage.cached / msg.usage.input) * 100)}% · ` : '';
    const usage = document.createElement('span');
    usage.className = 'msg-usage';
    usage.textContent = `${numTokens(msg.usage.input)} in · ${rate}${numTokens(msg.usage.output)} out`;
    head.append(usage);
  }
  // 中止 / 出错的轮次要标一下，不然看起来就像模型答到一半没了
  if (msg.stopReason === 'aborted' || msg.stopReason === 'error') {
    const note = document.createElement('span');
    note.className = 'msg-note';
    note.textContent = msg.stopReason === 'aborted' ? '已中止' : '出错了';
    head.append(note);
  }
  // 被折掉的 entry 留在原位、变暗，但得说清它只还在文件里
  if (folded) head.append(foldedNote());

  const body = document.createElement('div');
  body.className = 'msg-body';
  fillBody(body, msg, streaming, entryId);
  el.replaceChildren(head, body);
}

// foldedNote 是「这条只在文件里、不再进上下文」那个小标记
function foldedNote() {
  const note = document.createElement('span');
  note.className = 'msg-note';
  note.textContent = '已压缩 · 不再进上下文';
  return note;
}

function messageKind(msg) {
  if (msg.role === 'error') return 'error';
  if (msg.role === 'tool_result') return 'tool';
  return msg.role;
}

function fillBody(body, msg, streaming, entryId) {
  if (msg.role === 'tool_result' || msg.role === 'error') {
    // 工具结果就是一段机器输出，原样给，不走正文那套
    body.replaceChildren(toolResultBlock(msg.blocks || [], msg.isError, entryId));
    return;
  }

  const children = [];
  const blocks = msg.blocks || [];

  blocks.forEach((block, index) => {
    const isLast = index === blocks.length - 1;
    if (block.kind === 'thinking' && block.thinking) {
      children.push(thinkingBlock(block, streaming));
    } else if (block.kind === 'text' && block.text) {
      // 用户点过技能的那条消息，正文跟着一起进了历史（服务端拼的）。页面上
      // 只该看见用户自己的话 —— 技能有「技能 X」角标，正文几十 KB 摆出来没法看
      const text = stripSkillBlock(block.text);
      if (text) children.push(textBlock({ text }, { caret: streaming && isLast }));
    } else if (block.kind === 'tool') {
      children.push(toolCallBlock(block, { running: streaming && isLast }));
    } else if (block.kind === 'image') {
      children.push(imageBlock(block, { entryId, index }));
    }
  });

  if (msg.errorMessage) {
    const err = document.createElement('div');
    err.className = 'bubble';
    err.textContent = msg.errorMessage;
    children.push(err);
  }
  if (!children.length) {
    const empty = document.createElement('div');
    empty.className = 'chat-empty';
    empty.textContent = streaming ? '正在连接模型…' : '（空消息）';
    children.push(empty);
  }

  body.replaceChildren(...children);
}

function textBlock(block, { caret }) {
  const wrap = document.createElement('div');
  wrap.className = 'bubble' + (caret ? ' caret' : '');
  wrap.append(markdown(block.text));
  return wrap;
}

function thinkingBlock(block, streaming) {
  const details = document.createElement('details');
  details.className = 'thinking';
  details.open = streaming;
  const summary = document.createElement('summary');
  summary.textContent = '思考过程';
  const body = document.createElement('p');
  body.textContent = block.thinking;
  details.append(summary, body);
  return details;
}

// skillPaths 是 SKILL.md 绝对路径 → 技能名。技能没有专门的工具，模型用技能就是
// 一次普通的 read，这张表（app.js 从 /api/skills 灌进来）是界面唯一能认出它的地方
let skillPaths = new Map();

export function setSkillPaths(map) {
  skillPaths = map;
}

// skillRead 认「这次 read 读的是哪个技能」。名字以接口给的为准（frontmatter 里的
// 名字可能跟目录名不一样）。参数还没流完、路径不全的时候返回 null，退回普通行
function skillRead(block) {
  if (block.toolName !== 'read' || !block.args || !skillPaths.size) return null;
  let path;
  try {
    path = JSON.parse(block.args).path;
  } catch {
    return null;
  }
  const name = typeof path === 'string' ? skillPaths.get(path) : '';
  return name ? { name, path } : null;
}

// expandedTools 记着哪些工具卡被展开过。一条消息每次重画都是整个重建
// （流式收尾、落盘认领各来一次），不记的话用户正看着的那张卡会当场弹回折叠
const expandedTools = new Set();

// toolCallBlock 造一张调用卡：默认折叠，摘要行是工具名 + 一行参数预览，
// 展开才看到完整参数和输出。长命令就靠这个展开看全 —— 摘要行的省略号只是
// 显示不下时省宽度，不是内容被截掉了
function toolCallBlock(block, { running }) {
  const id = block.toolCallId || '';
  const wrap = document.createElement('details');
  wrap.className = 'tool-call' + (running ? ' is-running' : '');
  // 结果靠这个 id 找回来贴进同一张卡（见 attachToolResult / app.js 的 renderChat）
  if (id) wrap.dataset.toolCallId = id;
  wrap.open = expandedTools.has(id);
  wrap.addEventListener('toggle', () => {
    if (!id) return;
    if (wrap.open) expandedTools.add(id);
    else expandedTools.delete(id);
  });

  const line = document.createElement('summary');
  line.className = 'tool-line';
  const name = document.createElement('span');
  name.className = 'tool-name';
  const args = document.createElement('span');
  args.className = 'tool-args';

  const skill = skillRead(block);
  if (skill) {
    // 用技能是件值得看见的事：整行标出来，读的是哪个文件退到悬停里
    wrap.classList.add('is-skill');
    name.textContent = '技能';
    args.textContent = skill.name;
    args.title = skill.path;
  } else {
    name.textContent = block.toolName || '工具';
    args.textContent = argsPreview(block.toolName, block.args);
  }

  line.append(name, args);
  wrap.append(line);
  const full = toolArgsBody(block);
  if (full) wrap.append(full);
  return wrap;
}

// toolArgsBody 是展开后那段参数：bash 给整条命令，别的给整形 JSON。
// 读不出来（参数还在流）就原样给，反正展开的时候它就长这样
function toolArgsBody(block) {
  const raw = (block.args || '').trim();
  if (!raw) return null;

  let text = raw;
  try {
    const args = JSON.parse(raw);
    text = typeof args?.command === 'string' ? args.command : JSON.stringify(args, null, 2);
  } catch {
    /* 参数还没流完，原样显示 */
  }

  const pre = document.createElement('pre');
  pre.className = 'tool-args-full';
  pre.textContent = text;
  return pre;
}

// attachToolResult 把一次调用的结果贴进它那张卡：调用和结果是同一件事，
// 拆成两处会看岔（工具1 工具2 结果1 结果2）。找不到卡片的退路见 toolResultBlock
export function attachToolResult(card, msg, { entryId = '' } = {}) {
  card.classList.add('has-result');
  if (msg.isError) card.classList.add('is-error');
  card.append(...toolResultContent(msg.blocks || [], entryId));
}

// 工具结果的内容：文本原样给（长了自己滚），图片画出来 —— 模型看到的图，用户也该看到
function toolResultContent(blocks, entryId) {
  const nodes = [];
  const text = blocks.filter((b) => b.kind === 'text').map((b) => b.text).join('\n');
  if (text) {
    const out = document.createElement('pre');
    out.className = 'tool-out';
    out.textContent = text.trim();
    nodes.push(out);
  }
  blocks.forEach((block, index) => {
    if (block.kind === 'image') nodes.push(imageBlock(block, { entryId, index }));
  });
  return nodes;
}

// toolResultBlock 是独立的一张结果卡：路径从半截开始时（调用的那条不在路径上）
// 结果没有卡可并，只能自己站着
export function toolResultBlock(blocks, isError, entryId = '') {
  const wrap = document.createElement('div');
  wrap.className = 'tool-result' + (isError ? ' is-error' : '');
  wrap.append(...toolResultContent(blocks, entryId));
  return wrap;
}

// imageCtx 是取图要的会话定位（app.js 灌进来，跟 skillPaths 一个路子）。
// 一张图几百 KB，整棵树的接口故意不带内容（见 dto.go），所以图是现取的
let imageCtx = { cwd: '', sessionId: '' };

export function setImageCtx(ctx) {
  imageCtx = ctx || { cwd: '', sessionId: '' };
}

// imageBlock 画一张图。还没落盘的消息带着本地 dataURL（用户刚发的那张，
// 服务端还没存）；落盘的靠 entry + 块下标现取。两样都没有就退回一行占位文字
function imageBlock(block, { entryId = '', index = 0 } = {}) {
  const src = block.dataUrl || (entryId ? imageURL(entryId, index) : '');
  if (!src) {
    const el = document.createElement('span');
    el.className = 'tool-image';
    el.textContent = `图片 ${block.imageMime || ''} ${byteSize(block.imageBytes)}`;
    return el;
  }

  const link = document.createElement('a');
  link.className = 'msg-image';
  link.href = src;
  link.target = '_blank';
  link.rel = 'noreferrer';
  link.title = `${block.imageMime || ''} ${byteSize(block.imageBytes)}（点开看原图）`;
  const img = document.createElement('img');
  img.src = src;
  img.alt = block.imageMime || '图片';
  link.append(img);
  return link;
}

function imageURL(entryId, index) {
  const query = new URLSearchParams({
    cwd: imageCtx.cwd || '',
    id: imageCtx.sessionId || '',
    entry: entryId,
    block: String(index),
  });
  return `/api/image?${query}`;
}

function byteSize(bytes) {
  if (!bytes) return '';
  if (bytes < 1024) return bytes + ' B';
  return (bytes / 1024).toFixed(0) + ' KB';
}

// ── markdown ─────────────────────────────────────────────
// 只认围栏代码、行内代码、加粗、斜体、标题、列表、链接、表格、分隔线 ——
// agent 的输出基本就这些。全部用 DOM 拼、不碰 innerHTML，模型吐什么进来都不会变成标签

const INLINE = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\*[^*\s][^*\n]*\*)|(\[[^\]]+\]\([^)\s]+\))/g;

// 表格：| 表头 | ... | 下面跟一行 |---|---|。表头两头的竖线可有可无，
// 单元格里的 | 不支持转义 —— 模型排的表基本不长那样
const TABLE_ROW = /^\s*\|.*\|\s*$/;
const TABLE_SEP = /^\s*\|?[\s:|-]*-[\s:|-]*\|?\s*$/;
const RULE = /^\s*(-{3,}|\*{3,}|_{3,})\s*$/;

function tableCells(line) {
  return line.trim().replace(/^\|/, '').replace(/\|$/, '').split('|').map((cell) => cell.trim());
}

export function markdown(text) {
  const frag = document.createDocumentFragment();
  const lines = text.split('\n');
  let list = null;

  const closeList = () => {
    if (list) frag.append(list);
    list = null;
  };

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];

    if (line.trimStart().startsWith('```')) {
      closeList();
      const lang = line.trim().slice(3).trim();
      const code = [];
      i++;
      while (i < lines.length && !lines[i].trimStart().startsWith('```')) code.push(lines[i++]);
      const pre = document.createElement('pre');
      const el = document.createElement('code');
      if (lang) el.dataset.lang = lang;
      el.textContent = code.join('\n');
      pre.append(el);
      frag.append(pre);
      continue;
    }

    const heading = line.match(/^(#{1,6})\s+(.*)$/);
    if (heading) {
      closeList();
      // 层级照原样给（h5/h6 并到 h4）：字号由 CSS 拉开，这里再改级就看不出深浅了
      const h = document.createElement('h' + Math.min(heading[1].length, 4));
      inline(h, heading[2]);
      frag.append(h);
      continue;
    }

    if (TABLE_ROW.test(line) && i + 1 < lines.length && TABLE_SEP.test(lines[i + 1])) {
      closeList();
      const parsed = table(lines, i);
      frag.append(parsed.el);
      i = parsed.end;
      continue;
    }

    if (RULE.test(line)) {
      closeList();
      frag.append(document.createElement('hr'));
      continue;
    }

    const bullet = line.match(/^\s*[-*]\s+(.*)$/);
    const numbered = line.match(/^\s*\d+\.\s+(.*)$/);
    if (bullet || numbered) {
      const want = bullet ? 'UL' : 'OL';
      if (!list || list.tagName !== want) {
        closeList();
        list = document.createElement(want.toLowerCase());
      }
      const li = document.createElement('li');
      inline(li, (bullet || numbered)[1]);
      list.append(li);
      continue;
    }

    if (line.trim() === '') {
      closeList();
      continue;
    }

    closeList();
    const p = document.createElement('p');
    inline(p, line);
    frag.append(p);
  }

  closeList();
  return frag;
}

// table 从 start 那一行（表头）开始吃一张表，返回元素和最后吃掉的行号 ——
// 外层循环还会 +1，所以这里回的是最后一行自己
function table(lines, start) {
  const head = document.createElement('tr');
  for (const cell of tableCells(lines[start])) {
    const th = document.createElement('th');
    inline(th, cell);
    head.append(th);
  }

  const tbody = document.createElement('tbody');
  let i = start + 2; // 隔掉表头和 |---|---| 分隔行
  for (; i < lines.length && TABLE_ROW.test(lines[i]); i++) {
    const row = document.createElement('tr');
    for (const cell of tableCells(lines[i])) {
      const td = document.createElement('td');
      inline(td, cell);
      row.append(td);
    }
    tbody.append(row);
  }

  const thead = document.createElement('thead');
  thead.append(head);
  const el = document.createElement('table');
  el.append(thead, tbody);
  return { el, end: i - 1 };
}

function inline(parent, text) {
  let last = 0;
  for (const match of text.matchAll(INLINE)) {
    if (match.index > last) parent.append(text.slice(last, match.index));
    const [raw, code, bold, italic, link] = match;
    if (code) {
      const el = document.createElement('code');
      el.textContent = raw.slice(1, -1);
      parent.append(el);
    } else if (bold) {
      const el = document.createElement('strong');
      el.textContent = raw.slice(2, -2);
      parent.append(el);
    } else if (italic) {
      const el = document.createElement('em');
      el.textContent = raw.slice(1, -1);
      parent.append(el);
    } else if (link) {
      const split = raw.indexOf('](');
      const href = raw.slice(split + 2, -1);
      const el = document.createElement('a');
      // 链接文字里常嵌着行内代码（[`x`](./x)）：接着按行内规则拆，
      // 整段当纯文本的话反引号会原样露出来
      inline(el, raw.slice(1, split));
      // 只放行 http/https：模型编出来的 javascript: 链接不能点
      if (/^https?:\/\//i.test(href)) {
        el.href = href;
        el.target = '_blank';
        el.rel = 'noreferrer';
      }
      parent.append(el);
    }
    last = match.index + raw.length;
  }
  if (last < text.length) parent.append(text.slice(last));
}
