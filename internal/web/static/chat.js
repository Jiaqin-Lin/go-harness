// 消息渲染。落过盘的 entry 和正在流的那条走同一套函数 —— 一轮跑完把流式的
// 换成落盘的，视觉上不该有跳动

import { roleLabel, argsPreview, clip, numTokens } from './shared.js';

export function renderMessage(msg, { streaming = false, folded = false } = {}) {
  const el = document.createElement('div');
  updateMessage(el, msg, { streaming, folded });
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
// 只在建元素时算一次的话，这一轮刚跑完就是错的，得刷新页面才对
export function updateMessage(el, msg, { streaming = false, folded = false } = {}) {
  el.className = `msg msg--${messageKind(msg)}` + (folded ? ' is-folded' : '');

  const head = document.createElement('div');
  head.className = 'msg-head';
  const role = document.createElement('span');
  role.className = 'msg-role';
  role.textContent = roleLabel(msg);
  head.append(role);
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
  fillBody(body, msg, streaming);
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

function fillBody(body, msg, streaming) {
  if (msg.role === 'tool_result' || msg.role === 'error') {
    // 工具结果就是一段机器输出，原样给，不走正文那套
    body.replaceChildren(toolResultBlock(msg.blocks || [], msg.isError));
    return;
  }

  const children = [];
  const blocks = msg.blocks || [];

  blocks.forEach((block, index) => {
    const isLast = index === blocks.length - 1;
    if (block.kind === 'thinking' && block.thinking) {
      children.push(thinkingBlock(block, streaming));
    } else if (block.kind === 'text' && block.text) {
      children.push(textBlock(block, { caret: streaming && isLast }));
    } else if (block.kind === 'tool') {
      children.push(toolCallBlock(block, { running: streaming && isLast }));
    } else if (block.kind === 'image') {
      children.push(imageBlock(block));
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

function toolCallBlock(block, { running }) {
  const wrap = document.createElement('div');
  wrap.className = 'tool-call' + (running ? ' is-running' : '');
  const line = document.createElement('div');
  line.className = 'tool-line';
  const name = document.createElement('span');
  name.className = 'tool-name';
  name.textContent = block.toolName || '工具';
  const args = document.createElement('span');
  args.className = 'tool-args';
  args.textContent = argsPreview(block.toolName, block.args);
  line.append(name, args);
  wrap.append(line);
  return wrap;
}

// 工具结果：文本原样给（长了自己滚），图片只给形状 —— 接口本来就不带图内容
export function toolResultBlock(blocks, isError) {
  const wrap = document.createElement('div');
  wrap.className = 'tool-result' + (isError ? ' is-error' : '');
  const text = blocks.filter((b) => b.kind === 'text').map((b) => b.text).join('\n');
  if (text) {
    const out = document.createElement('pre');
    out.className = 'tool-out';
    out.textContent = text.trim();
    wrap.append(out);
  }
  for (const block of blocks.filter((b) => b.kind === 'image')) {
    wrap.append(imageBlock(block));
  }
  return wrap;
}

function imageBlock(block) {
  const el = document.createElement('span');
  el.className = 'tool-image';
  el.textContent = `图片 ${block.imageMime || ''} ${byteSize(block.imageBytes)}`;
  return el;
}

function byteSize(bytes) {
  if (!bytes) return '';
  if (bytes < 1024) return bytes + ' B';
  return (bytes / 1024).toFixed(0) + ' KB';
}

// ── markdown ─────────────────────────────────────────────
// 只认围栏代码、行内代码、加粗、标题、列表、链接 —— agent 的输出基本就这些。
// 全部用 DOM 拼、不碰 innerHTML，模型吐什么进来都不会变成标签

const INLINE = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\[[^\]]+\]\([^)\s]+\))/g;

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
      const h = document.createElement('h' + Math.min(heading[1].length + 2, 4));
      inline(h, heading[2]);
      frag.append(h);
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

function inline(parent, text) {
  let last = 0;
  for (const match of text.matchAll(INLINE)) {
    if (match.index > last) parent.append(text.slice(last, match.index));
    const [raw, code, bold, link] = match;
    if (code) {
      const el = document.createElement('code');
      el.textContent = raw.slice(1, -1);
      parent.append(el);
    } else if (bold) {
      const el = document.createElement('strong');
      el.textContent = raw.slice(2, -2);
      parent.append(el);
    } else if (link) {
      const split = raw.indexOf('](');
      const href = raw.slice(split + 2, -1);
      const el = document.createElement('a');
      el.textContent = raw.slice(1, split);
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
