// 界面状态与接线。所有状态都在这一个对象里，渲染是「状态变了就重画」，
// 只有流式那一轮走增量（见 dom.live / state.streaming）

import { getJSON, request, streamChat } from './api.js';
import { renderMessage, updateMessage } from './chat.js';
import { Graph } from './graph.js';
import { DirPicker } from './dirpicker.js';
import { summarise, clip, numTokens } from './shared.js';

const el = (id) => document.getElementById(id);

const dom = {
  app: document.querySelector('.app'),
  sidebar: document.querySelector('.sidebar'),
  chat: document.querySelector('.chat'),
  projectList: el('project-list'),
  sidebarEmpty: el('sidebar-empty'),
  graphTitle: el('graph-title'),
  graphEmpty: el('graph-empty'),
  graphViewport: el('graph-viewport'),
  graphCanvas: el('graph-canvas'),
  graphEdges: el('graph-edges'),
  graphNodes: el('graph-nodes'),
  chatTitle: el('chat-title'),
  chatBranch: el('chat-branch'),
  chatLog: el('chat-log'),
  history: el('chat-history'),
  live: el('chat-live'),
  composer: el('composer'),
  input: el('composer-input'),
  send: el('composer-send'),
  stop: el('composer-stop'),
  hint: el('composer-hint'),
  remove: el('session-delete'),
  splitterSidebar: el('splitter-sidebar'),
  splitterChat: el('splitter-chat'),
  toggleSidebar: el('toggle-sidebar'),
  toggleChat: el('toggle-chat'),
};

const state = {
  projects: [],
  open: new Set(),   // 侧栏里展开着的项目
  cwd: null,         // 当前项目
  sessionId: null,   // 当前会话；draft 为 true 时它还没落盘
  draft: false,
  title: '',
  preview: '',
  entries: [],
  leafId: '',
  selected: null,    // 选中的 entry（右栏只画根到它的路径）
  pendingBranch: null, // 「+ 建出来的新分支」挂在哪条 entry 下面；非 null 时输入框才对着这条新支路
  busy: false,
  abort: null,
  live: [],          // 这一轮已经画出来、还没落盘的消息
  streaming: null,   // 正在长的那个元素
  connecting: null,  // 「正在连接模型…」那行占位
  freshId: null,
  sawDone: false,    // 这一轮的流有没有正常收尾（没有就可能是断了）
  streamError: '',   // 流里报的错，收尾时再显示
  stopping: false,   // 用户点过停止（跟「连接断了」不是一回事）
  contextWindow: 0,  // 模型的上下文窗口，启动时问 /api/meta（拿不到就 0 = 不显示分母）
  // 布局偏好：拖过的栏宽（px，null = 还没拖过、用 CSS 默认）和栏的显隐
  sidebarW: null,
  chatW: null,
  sidebarHidden: false,
  chatHidden: false,
};

const graph = new Graph({
  viewport: dom.graphViewport,
  canvas: dom.graphCanvas,
  edges: dom.graphEdges,
  nodes: dom.graphNodes,
  onSelect: (id) => selectEntry(id),
  onBranch: (id) => branchFrom(id),
});

const picker = new DirPicker({
  root: el('dirpicker'),
  pathEl: el('dirpicker-path'),
  listEl: el('dirpicker-list'),
  onPick: async (path) => {
    picker.close();
    try {
      await request('POST', '/api/projects', { path });
      await refreshProjects();
      newSession(path, /* openSidebar */ true);
    } catch (err) {
      dom.hint.textContent = err.message;
    }
  },
});

// ── 侧栏 ─────────────────────────────────────────────────

async function refreshProjects() {
  try {
    const data = await getJSON('/api/projects');
    state.projects = data.projects || [];
  } catch (err) {
    state.projects = [];
    dom.hint.textContent = err.message;
  }
  renderSidebar();
}

function renderSidebar() {
  dom.sidebarEmpty.hidden = state.projects.length > 0;
  dom.projectList.replaceChildren(...state.projects.map(projectElement));
}

function projectElement(project) {
  const wrap = document.createElement('div');
  wrap.className = 'project';

  const head = document.createElement('div');
  head.className = 'project-head' + (state.open.has(project.path) ? ' is-open' : '');
  head.tabIndex = 0;
  head.addEventListener('click', () => toggleProject(project));

  const caret = span('project-caret', '▸');
  const name = span('project-name', project.name);
  const count = span('project-count', project.exists ? String((project.sessions || []).length) : '');
  const remove = document.createElement('button');
  remove.type = 'button';
  remove.className = 'btn btn-ghost btn-icon';
  remove.textContent = '×';
  remove.title = '从列表里移除（不删会话）';
  remove.setAttribute('aria-label', '移除项目');
  remove.addEventListener('click', async (event) => {
    event.stopPropagation();
    await request('DELETE', `/api/projects?path=${encodeURIComponent(project.path)}`);
    state.open.delete(project.path);
    await refreshProjects();
  });
  // 新建会话的入口在这个方块里（跟 × 一样悬停才显形）；目录不在就没得建
  let add = null;
  if (project.exists) {
    add = document.createElement('button');
    add.type = 'button';
    add.className = 'btn btn-ghost btn-icon';
    add.textContent = '+';
    add.title = '在这个项目里新建会话';
    add.setAttribute('aria-label', '新建会话');
    add.addEventListener('click', (event) => {
      event.stopPropagation();
      newSession(project.path, /* openSidebar */ true);
    });
  }
  head.append(caret, name, count);
  if (add) head.append(add);
  head.append(remove);
  wrap.append(head);

  if (!project.exists) {
    wrap.append(span('project-missing', '目录不在了'));
  } else if (state.open.has(project.path)) {
    wrap.append(sessionList(project));
  }
  return wrap;
}

function sessionList(project) {
  const list = document.createElement('div');
  list.className = 'session-list';

  if (state.draft && state.cwd === project.path) {
    list.append(activeRow('新会话（还没开始）', ''));
  }
  for (const session of project.sessions || []) {
    list.append(sessionRow(project, session));
  }
  return list;
}

function sessionRow(project, session) {
  const row = document.createElement('button');
  row.type = 'button';
  row.className = 'session-row';
  if (state.cwd === project.path && state.sessionId === session.id) row.classList.add('is-active');
  row.append(
    span('session-title', session.title || session.preview || '新会话'),
    span('session-meta', when(session.modified)),
  );
  row.addEventListener('click', () => openSession(project.path, session.id));
  return row;
}

function activeRow(title, meta) {
  const row = document.createElement('button');
  row.type = 'button';
  row.className = 'session-row is-active';
  row.append(span('session-title', title), span('session-meta', meta));
  return row;
}

function toggleProject(project) {
  if (state.open.has(project.path)) {
    state.open.delete(project.path);
  } else {
    state.open.add(project.path);
    const first = (project.sessions || [])[0];
    if (state.cwd !== project.path) {
      // 点开项目就把最近一条会话打开，跟命令行版的「续上最近一次」是一回事
      first ? openSession(project.path, first.id) : newSession(project.path);
      return; // openSession 自己会重画侧栏
    }
  }
  renderSidebar();
}

function newSession(cwd, openSidebar = false) {
  if (state.busy) return; // 跑着一轮的时候换会话，事件会灌进新会话里
  if (openSidebar) state.open.add(cwd);
  state.cwd = cwd;
  state.sessionId = newSessionID();
  state.draft = true;
  state.title = '';
  state.preview = '';
  state.entries = [];
  state.leafId = '';
  state.selected = null;
  state.pendingBranch = null;
  state.freshId = null;
  clearLive();
  graph.reset(); // 新树从原点重新取景；不然会沿用上一个会话拖过的镜头，画到屏幕外
  renderAll();
  dom.input.focus();
}

// ── 会话 ─────────────────────────────────────────────────

async function openSession(cwd, id, entryId = null) {
  if (state.busy) return;

  let view;
  try {
    view = await getJSON(`/api/session?cwd=${encodeURIComponent(cwd)}&id=${encodeURIComponent(id)}`);
  } catch (err) {
    dom.hint.textContent = err.message;
    await refreshProjects();
    return;
  }

  state.cwd = cwd;
  state.sessionId = id;
  state.draft = false;
  state.open.add(cwd);
  applySession(view);
  state.selected = (entryId && entryById(entryId) ? entryId : state.leafId) || null;
  state.pendingBranch = null;
  state.freshId = null;

  clearLive();
  renderAll();
  // 先把镜头架到当前这条支路的末端：长会话里「我在哪儿」比「树有多大」重要
  graph.overview(state.selected);
  updateHash();
}

// applySession 把服务端的会话搬进本地状态，选中节点不在这里定
function applySession(view) {
  state.title = view.title || '';
  state.preview = view.preview || '';
  state.entries = view.entries || [];
  state.leafId = view.leafId || '';
}

// reloadSession 重读当前会话。一轮跑完（包括被中止）之后用它对齐 ——
// 本地那份是乐观画出来的，磁盘上才是真的
async function reloadSession() {
  if (!state.cwd || !state.sessionId || state.draft) return;
  const view = await getJSON(`/api/session?cwd=${encodeURIComponent(state.cwd)}&id=${encodeURIComponent(state.sessionId)}`);
  applySession(view);
  if (!entryById(state.selected)) state.selected = state.leafId || null;
  renderAll();
}

// 点节点 = 只看这条支路（右栏画到它为止）；要接着说话得先按 +（见 branchFrom）
function selectEntry(id) {
  if (state.selected === id && state.pendingBranch === null) return;
  state.pendingBranch = null;
  applySelection(id);
}

// + 按钮：在图上把「待分出的新分支」画出来，输入框才解锁 ——
// 用户先看到分支长在哪儿，再决定说什么
function branchFrom(id) {
  if (state.busy) return;
  state.pendingBranch = id;
  applySelection(id);
  dom.input.focus();
}

function applySelection(id) {
  state.selected = id;
  renderGraph();
  renderChat();
  // 点开一轮 = 要看这轮的问答，翻到尾巴（回答在最后）
  dom.chatLog.scrollTop = dom.chatLog.scrollHeight;
  renderComposer();
  graph.reveal(state.pendingBranch !== null ? PENDING_ID : id);
  updateHash();
}

function entryById(id) {
  return state.entries.find((entry) => entry.id === id);
}

// pathEntries 是「根 → 选中节点」这一条路径：右栏只显示它。
// 树上别的支路不进来 —— 那正是分叉互不影响的含义
function pathEntries() {
  if (!state.selected) return [];
  const byId = new Map(state.entries.map((entry) => [entry.id, entry]));
  const path = [];
  for (let id = state.selected, guard = 0; id && guard < 10000; guard++) {
    const entry = byId.get(id);
    if (!entry) break;
    path.push(entry);
    id = entry.parentId || null;
  }
  return path.reverse();
}

// ── 渲染 ─────────────────────────────────────────────────

function renderAll() {
  renderSidebar();
  renderGraph();
  renderChat();
  renderComposer();
}

const PENDING_ID = '__pending__'; // 幽灵节点（+ 画出来的待分出新分支）在图上占的 id

function renderGraph() {
  const { nodes, owner } = graphModel();

  dom.graphEmpty.hidden = state.entries.length > 0;
  dom.graphTitle.textContent = nodes.length ? `${nodes.length} 轮` : '';

  // 真实 entry 映射回它所属轮次的节点：owner 里都有，没收录的（文件截断）
  // 沿父链往上找
  const nodeId = (id) => {
    for (let cur = id, guard = 0; cur && guard < 10000; guard++) {
      if (owner.has(cur)) return owner.get(cur);
      cur = entryById(cur)?.parentId || null;
    }
    return null;
  };
  if (state.pendingBranch !== null) {
    nodes.push({ id: PENDING_ID, parentId: state.pendingBranch, message: null });
  }
  graph.render(nodes, {
    selectedId: state.pendingBranch !== null ? PENDING_ID : nodeId(state.selected),
    leafId: nodeId(state.leafId),
    freshId: nodeId(state.freshId),
    sessionId: state.sessionId,
  });
}

// graphModel 把真实 entry 树折成图上的「轮」：一个节点 = 一轮对话（你问的那句
// + 它的回答，中间调的工具有多少都并进这一轮）。节点的 id 用这一轮的结尾 entry ——
// 点开看到整轮问答、「+」在回答之后分叉，都对着它来。owner 是反向映射：
// 每条 entry -> 它所属轮次的节点 id
function graphModel() {
  const byId = new Map(state.entries.map((e) => [e.id, e]));
  const children = new Map();
  for (const entry of state.entries) {
    if (!entry.parentId || !byId.has(entry.parentId)) continue;
    if (!children.has(entry.parentId)) children.set(entry.parentId, []);
    children.get(entry.parentId).push(entry);
  }

  const owner = new Map();
  const nodes = [];

  for (const entry of state.entries) {
    if (entry.message?.role !== 'user') continue;

    // 一轮的结尾：顺着回答链往下走，到叶子停。遇到用户消息就是下一轮了；
    // 遇到岔口（旧版界面能在半路分叉）也走回答链那条，分出去的那支挂在
    // 这一轮节点下面（它的父节点会回溯到本条用户消息）
    const steps = [entry];
    let end = entry;
    for (;;) {
      const next = (children.get(end.id) || []).find((kid) => kid.message?.role !== 'user');
      if (!next) break;
      end = next;
      steps.push(end);
    }
    for (const step of steps) owner.set(step.id, end.id);

    // 图的父节点 = 最近的上一条用户消息那一轮（中间隔着的是它的回答）
    let parent = entry.parentId || null;
    while (parent && byId.get(parent)?.message?.role !== 'user') {
      parent = byId.get(parent)?.parentId || null;
    }
    nodes.push({
      ...end,
      parentId: parent ? owner.get(parent) || null : null,
      message: entry.message, // 节点上显示的是你问的那句
      stopReason: end.message?.stopReason || '', // 中止/出错标在节点角上
    });
  }
  return { nodes, owner };
}

// turnLabel 给一轮起个短名：从条目沿父链往上找，这一轮是从哪句用户消息起的
function turnLabel(entryId) {
  for (let id = entryId, guard = 0; id && guard < 10000; guard++) {
    const entry = entryById(id);
    if (!entry) break;
    if (entry.message?.role === 'user') return clip(summarise(entry.message), 16);
    id = entry.parentId || null;
  }
  return '这一轮';
}

function renderChat() {
  const path = pathEntries();
  const nodes = path.map((entry) => renderMessage(entry.message || {}));
  withPin(() => {
    dom.history.replaceChildren(...nodes);
    if (!path.length && !state.sessionId) {
      const empty = document.createElement('p');
      empty.className = 'chat-empty';
      empty.textContent = '选一个会话，或者新建一个。';
      dom.history.append(empty);
    }
  });
  dom.chatTitle.textContent = state.title || state.preview || (state.sessionId ? '新会话' : '');
  dom.chatBranch.textContent = pathSummary(path);
  renderComposer();
}

// pathSummary 是右栏头第二行：路径多长、上下文占了多少、缓存命中多少。
// 上下文取路径上最后一条带 usage 的消息 —— 它的 input+output 就是接着往下
// 说时要带的历史大小；窗口大小问服务端要（/api/meta，模型知识不写死在这）
function pathSummary(path) {
  if (!path.length) return '';
  let last = null;
  for (const entry of path) {
    if (entry.message?.usage) last = entry.message.usage;
  }

  const parts = [`${path.length} 条消息`];
  if (last) {
    const used = numTokens(last.input + last.output);
    parts.push(`上下文 ${used}${state.contextWindow ? ` / ${numTokens(state.contextWindow)}` : ''}`);
    if (last.input > 0) parts.push(`缓存 ${Math.round((last.cached / last.input) * 100)}%`);
  }
  return parts.join(' · ');
}

function renderComposer() {
  const ready = Boolean(state.sessionId) && !state.busy && sendParent() !== null;
  dom.input.disabled = !ready;
  dom.send.disabled = !ready || !dom.input.value.trim();
  dom.stop.hidden = !state.busy;
  dom.remove.hidden = state.draft || !state.sessionId;
  dom.hint.textContent = state.busy ? '正在跑…' : branchHint();
}

// sendParent 是「这一句接在哪儿」：只有两个地方能接 —— 当前的 tip（接着往下说）、
// 或 + 画出来的新分支。null = 选中的是历史节点，得先按 +
function sendParent() {
  if (state.pendingBranch !== null) return state.pendingBranch;
  if (!state.entries.length) return ''; // 全新会话：这句就是起点
  if (state.selected && state.selected === state.leafId) return state.selected;
  return null;
}

function branchHint() {
  if (!state.sessionId) return '先选一个会话，或者新建一个';
  if (state.pendingBranch !== null) {
    return `在「${turnLabel(state.pendingBranch)}」这一轮之后分出新的一条，说点什么`;
  }
  if (!state.entries.length) return '这一句就是起点';
  if (state.selected === state.leafId) {
    return `接着「${turnLabel(state.selected)}」往下`;
  }
  return '这是历史里的一轮 —— 按它上面的 + 才能从这儿分出新的一条';
}

// ── 发消息 ───────────────────────────────────────────────

async function send() {
  const text = dom.input.value.trim();
  const parent = sendParent();
  if (!text || state.busy || !state.sessionId || parent === null) return;

  const entriesBefore = state.entries.length;
  state.sawDone = false;
  state.streamError = '';
  state.stopping = false;

  // 先把用户这句画上去；服务端落盘之后发 entry 事件来认领它（见 adoptEntry）
  const userMsg = { role: 'user', blocks: [{ kind: 'text', text }] };
  const userEl = renderMessage(userMsg);
  withPin(() => dom.live.append(userEl));
  state.live.push({ el: userEl, msg: userMsg });

  dom.input.value = '';
  autoGrow();
  state.busy = true;
  const controller = new AbortController();
  state.abort = controller;
  renderComposer();

  let failure = '';
  try {
    await streamChat(
      { cwd: state.cwd, sessionId: state.sessionId, parentId: parent, text },
      handleEvent,
      controller.signal,
    );
  } catch (err) {
    if (err.name !== 'AbortError') failure = err.message;
  }

  // 用户点过停止（服务端会把这一轮收完尾，不是连接断了），或者连接被自己掐了
  const stopped = state.stopping || controller.signal.aborted;
  state.busy = false;
  state.abort = null;
  state.streaming = null;
  state.connecting = null;
  state.draft = false;
  state.freshId = null;

  // 这一轮不管怎么结束，都以服务端为准重画一遍：点「停止」之后服务端照样
  // 把这一轮落盘了（见 server/chat.go），本地这份乐观状态跟磁盘对不上；
  // 中途挂掉时没落盘的临时消息也一并撤掉
  dom.live.replaceChildren();
  state.live = [];
  try {
    await reloadSession();
  } catch {
    // 读不回来就保持现状，至少别把画面清空
  }

  // 一句话都没落盘（409、连不上、还没跑就断了）：把打的字还回输入框，
  // 别让人重打一遍
  if (!stopped && state.entries.length === entriesBefore && text && !dom.input.value.trim()) {
    dom.input.value = text;
    autoGrow();
  }

  if (failure) showError(failure);
  else if (state.streamError) showError(state.streamError);
  else if (!state.sawDone && !stopped) showError('连接断了，这一轮可能没跑完 —— 刷新页面看服务端最终落盘的结果');

  renderComposer();
  renderGraph();
  updateHash(); // 新会话到这一刻才真的有文件，地址栏该记上它了
  // 新会话这时候才出现在列表里，标题还要再晚几秒（后台生成的）
  setTimeout(refreshProjects, 1200);
  setTimeout(refreshProjects, 6000);
}

function handleEvent(event) {
  switch (event.type) {
    case 'turn_start':
      showConnecting();
      break;
    case 'assistant_start':
      startAssistant();
      break;
    case 'text_delta':
      appendDelta(event.blockIndex, 'text', event.delta);
      break;
    case 'thinking_delta':
      appendDelta(event.blockIndex, 'thinking', event.delta);
      break;
    case 'tool_call_start':
      startToolCall(event);
      break;
    case 'tool_call_delta':
      appendToolArgs(event);
      break;
    case 'message_end':
      finishAssistant(event.message);
      break;
    case 'tool_start':
      startToolRun(event);
      break;
    case 'tool_end':
      finishToolRun(event);
      break;
    case 'entry':
      adoptEntry(event.entry);
      break;
    case 'error':
      // 先攒着，等这一轮收尾、按服务端重画之后再显示 ——
      // 立刻塞进 live 区的话，收尾时会被一起清掉
      state.streamError = event.error;
      break;
    case 'done':
      state.sawDone = true;
      break;
    default:
      break;
  }
}

let queued = false;
function queueRender() {
  if (queued) return;
  queued = true;
  requestAnimationFrame(() => {
    queued = false;
    const streaming = state.streaming;
    if (!streaming) return;
    const stay = pinned();
    updateMessage(streaming.el, streaming.msg, { streaming: true });
    if (stay) dom.chatLog.scrollTop = dom.chatLog.scrollHeight;
  });
}

// 发出去到模型开始吐字之间有一段空档（推理模型首 token 要等好几秒），
// 先放一行字，别让右栏看着像没反应
function showConnecting() {
  if (state.streaming || state.connecting) return;
  const element = document.createElement('div');
  element.className = 'chat-empty';
  element.textContent = '正在连接模型…';
  withPin(() => dom.live.append(element));
  state.connecting = element;
}

function startAssistant() {
  state.connecting?.remove();
  state.connecting = null;

  const msg = { role: 'assistant', blocks: [] };
  const element = renderMessage(msg, { streaming: true });
  withPin(() => dom.live.append(element));
  state.streaming = { el: element, msg };
  state.live.push(state.streaming);
}

function appendDelta(index, kind, delta) {
  const streaming = state.streaming;
  if (!streaming) return;
  const block = (streaming.msg.blocks[index] ||= { kind });
  block[kind] = (block[kind] || '') + (delta || '');
  queueRender();
}

function startToolCall(event) {
  const streaming = state.streaming;
  if (!streaming) return;
  streaming.msg.blocks[event.blockIndex] ||= { kind: 'tool', toolName: event.toolName, toolCallId: event.toolCallId, args: '' };
  queueRender();
}

function appendToolArgs(event) {
  const streaming = state.streaming;
  if (!streaming) return;
  const block = streaming.msg.blocks[event.blockIndex];
  if (!block) return;
  block.args = (block.args || '') + (event.delta || '');
  queueRender();
}

function finishAssistant(message) {
  const streaming = state.streaming;
  if (!streaming || !message) return;
  const stay = pinned();
  streaming.msg = message;
  updateMessage(streaming.el, message, {});
  state.streaming = null;
  if (stay) dom.chatLog.scrollTop = dom.chatLog.scrollHeight;
}

function startToolRun(event) {
  const msg = { role: 'tool_result', toolName: event.toolName, toolCallId: event.toolCallId, blocks: [] };
  const element = renderMessage(msg, { streaming: true });
  withPin(() => dom.live.append(element));
  state.live.push({ el: element, msg, toolCallId: event.toolCallId, done: false });
}

function finishToolRun(event) {
  const live = [...state.live].reverse().find((item) => item.toolCallId === event.toolCallId && !item.done);
  if (!live) return;
  live.done = true;
  live.msg.blocks = event.blocks || [];
  live.msg.isError = Boolean(event.isError);
  const stay = pinned();
  updateMessage(live.el, live.msg, {});
  if (stay) dom.chatLog.scrollTop = dom.chatLog.scrollHeight;
}

// adoptEntry 把「刚画出来的消息」换成「已经落盘的 entry」：
// 元素原地从 live 容器挪进 history 容器，内容只按 entry 再对一遍
function adoptEntry(entry) {
  const index = state.live.findIndex((item) => item.msg.role === entry.message?.role);
  const live = index >= 0 ? state.live.splice(index, 1)[0] : null;

  state.entries.push(entry);
  state.leafId = entry.id;
  state.freshId = entry.id;

  // 新分支的第一条落盘了，幽灵节点退役，真节点接管这个位置
  if (state.pendingBranch !== null && entry.parentId === state.pendingBranch) state.pendingBranch = null;

  const stay = pinned();
  if (live) {
    live.el.dataset.entryId = entry.id;
    updateMessage(live.el, entry.message || {}, {});
    dom.history.append(live.el);
  } else {
    dom.history.append(renderMessage(entry.message || {}));
  }
  if (stay) dom.chatLog.scrollTop = dom.chatLog.scrollHeight;

  // 没走开的话眼跟着往下；用户中途点了别的节点就别把他拽回来
  if (state.selected === (entry.parentId || null) || state.selected === null) state.selected = entry.id;

  renderGraph();
  renderComposer();
}

// role 用 'error' 而不是 'tool_result'：adoptEntry 按 role 认领元素，
// 报错框要是顶着工具结果的身份留在 live 区，下一轮会被认领走
function showError(message) {
  const msg = { role: 'error', isError: true, blocks: [{ kind: 'text', text: message }] };
  withPin(() => dom.live.append(renderMessage(msg)));
}

function clearLive() {
  dom.live.replaceChildren();
  dom.history.replaceChildren();
  state.live = [];
  state.streaming = null;
  state.connecting = null;
}

// ── 输入框 ───────────────────────────────────────────────

function autoGrow() {
  dom.input.style.height = 'auto';
  dom.input.style.height = Math.min(dom.input.scrollHeight, window.innerHeight * 0.4) + 'px';
}

dom.input.addEventListener('input', () => {
  autoGrow();
  renderComposer();
});

dom.input.addEventListener('keydown', (event) => {
  if (event.key !== 'Enter') return;
  if (event.isComposing) return; // 输入法选字时的回车不算发送
  if (event.shiftKey) return; // Shift+回车换行，交给 textarea 自己
  event.preventDefault();
  send();
});

dom.composer.addEventListener('submit', (event) => {
  event.preventDefault();
  send();
});

// 「停止」是让服务端把那停一轮（走端点），不是把连接掐了：掐连接的话
// 客户端不知道服务端什么时候收完尾，重读会读到落盘落了一半的状态
dom.stop.addEventListener('click', async () => {
  if (!state.busy) return;
  state.stopping = true;
  try {
    await request('POST', '/api/chat/abort', { cwd: state.cwd, sessionId: state.sessionId });
  } catch {
    state.abort?.abort(); // 连端点都够不着了，只能把这条连接掐掉
  }
});
el('graph-fit').addEventListener('click', () => graph.fit());

el('session-delete').addEventListener('click', async () => {
  if (state.draft || !state.sessionId) return;
  if (!confirm('删掉这个会话？磁盘上的文件也会删。')) return;
  try {
    await request('DELETE', `/api/session?cwd=${encodeURIComponent(state.cwd)}&id=${encodeURIComponent(state.sessionId)}`);
  } catch (err) {
    dom.hint.textContent = err.message;
    return;
  }
  state.sessionId = null;
  state.entries = [];
  state.leafId = '';
  state.selected = null;
  state.title = '';
  clearLive();
  await refreshProjects();
  renderAll();
  location.hash = '';
});

el('add-project').addEventListener('click', () => picker.open(state.cwd || ''));
el('add-project-empty').addEventListener('click', () => picker.open(state.cwd || ''));

// ── 面板宽度与显隐 ───────────────────────────────────────
// 两条分隔条拖宽窄，工具栏两个按钮藏/露；偏好存 localStorage，刷新还在

const LAYOUT_KEY = 'ui:layout';

function loadLayout() {
  try {
    const saved = JSON.parse(localStorage.getItem(LAYOUT_KEY) || '{}');
    state.sidebarW = saved.sidebarW || null;
    state.chatW = saved.chatW || null;
    state.sidebarHidden = Boolean(saved.sidebarHidden);
    state.chatHidden = Boolean(saved.chatHidden);
  } catch {
    // 存坏了就用默认布局
  }
}

function saveLayout() {
  try {
    localStorage.setItem(LAYOUT_KEY, JSON.stringify({
      sidebarW: state.sidebarW,
      chatW: state.chatW,
      sidebarHidden: state.sidebarHidden,
      chatHidden: state.chatHidden,
    }));
  } catch {
    // 存不下不影响眼前
  }
}

function applyLayout() {
  dom.app.classList.toggle('is-sidebar-hidden', state.sidebarHidden);
  dom.app.classList.toggle('is-chat-hidden', state.chatHidden);
  dom.toggleSidebar.classList.toggle('is-off', state.sidebarHidden);
  dom.toggleChat.classList.toggle('is-off', state.chatHidden);
  if (state.sidebarW) document.documentElement.style.setProperty('--sidebar-w', `${state.sidebarW}px`);
  if (state.chatW) document.documentElement.style.setProperty('--chat-w', `${state.chatW}px`);
}

// wireSplitter 让一条分隔条拖动改宽度。invert 给「分隔条在面板左边」的那种用：
// 往左拖是把面板拉宽
function wireSplitter(element, { min, max, invert = false, current, apply }) {
  let drag = null;
  element.addEventListener('pointerdown', (event) => {
    drag = { x: event.clientX, start: current() };
    element.setPointerCapture(event.pointerId);
    element.classList.add('is-dragging');
    event.preventDefault();
  });
  element.addEventListener('pointermove', (event) => {
    if (!drag) return;
    const delta = (event.clientX - drag.x) * (invert ? -1 : 1);
    apply(Math.round(Math.min(Math.max(drag.start + delta, min), max)));
  });
  const end = (event) => {
    if (!drag) return;
    drag = null;
    element.classList.remove('is-dragging');
    if (element.hasPointerCapture(event.pointerId)) element.releasePointerCapture(event.pointerId);
    saveLayout();
  };
  element.addEventListener('pointerup', end);
  element.addEventListener('pointercancel', end);
}

wireSplitter(dom.splitterSidebar, {
  min: 180,
  max: 420,
  current: () => state.sidebarW || dom.sidebar.getBoundingClientRect().width,
  apply: (width) => {
    state.sidebarW = width;
    applyLayout();
  },
});

wireSplitter(dom.splitterChat, {
  min: 320,
  max: 760,
  invert: true, // 对话栏贴着右边，分隔条往左拖才是把它拉宽
  current: () => state.chatW || dom.chat.getBoundingClientRect().width,
  apply: (width) => {
    state.chatW = width;
    applyLayout();
  },
});

dom.toggleSidebar.addEventListener('click', () => {
  state.sidebarHidden = !state.sidebarHidden;
  applyLayout();
  saveLayout();
});

dom.toggleChat.addEventListener('click', () => {
  state.chatHidden = !state.chatHidden;
  applyLayout();
  saveLayout();
  // 藏着的这段时间流式更新会把「贴底」状态弄丢，重新露出来就回到最新
  if (!state.chatHidden) dom.chatLog.scrollTop = dom.chatLog.scrollHeight;
});

// ── 地址栏 ───────────────────────────────────────────────
// 会话和选中节点进 hash：刷新页面回到原处，也能直接把链接发给自己

function updateHash() {
  if (!state.cwd || !state.sessionId) return;
  const entry = state.selected ? `/${state.selected}` : '';
  location.hash = `#/p/${encodeURIComponent(state.cwd)}/${state.sessionId}${entry}`;
}

async function applyHash() {
  const parts = location.hash.replace(/^#\/?/, '').split('/');
  // p/<cwd>/<sessionId>[/<entryId>]。cwd 里的 "/" 都被编码成 %2F，切分才安全
  if (parts[0] !== 'p' || parts.length < 3) return;
  const cwd = decodeURIComponent(parts[1]);
  const sessionId = parts[2];
  const entryId = parts[3] || null;
  if (cwd === state.cwd && sessionId === state.sessionId && (!entryId || entryId === state.selected)) return;
  if (state.busy || !/^[0-9a-f]{8,32}$/.test(sessionId)) return;
  await openSession(cwd, sessionId, entryId);
}

// ── 小工具 ───────────────────────────────────────────────

function span(className, text) {
  const node = document.createElement('span');
  node.className = className;
  node.textContent = text;
  return node;
}


function when(iso) {
  if (!iso) return '';
  const then = new Date(iso);
  const minutes = Math.floor((Date.now() - then.getTime()) / 60000);
  if (minutes < 1) return '刚刚';
  if (minutes < 60) return `${minutes} 分钟前`;
  if (minutes < 60 * 24) return `${Math.floor(minutes / 60)} 小时前`;
  const pad = (n) => String(n).padStart(2, '0');
  return `${pad(then.getMonth() + 1)}-${pad(then.getDate())} ${pad(then.getHours())}:${pad(then.getMinutes())}`;
}

function newSessionID() {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
}

function pinned() {
  return dom.chatLog.scrollHeight - dom.chatLog.scrollTop - dom.chatLog.clientHeight < 48;
}

// withPin 在改动之前先记住「用户是不是贴着底」，贴着的才跟着滚 ——
// 用户翻历史的时候别把他拽回去
function withPin(mutate) {
  const stay = pinned();
  mutate();
  if (stay) dom.chatLog.scrollTop = dom.chatLog.scrollHeight;
}

// ── 起步 ─────────────────────────────────────────────────

// 模型信息（上下文窗口多大）。拉不到就算了，右栏那行字少个分母而已
async function loadMeta() {
  try {
    const meta = await getJSON('/api/meta');
    state.contextWindow = meta.contextWindow || 0;
  } catch {
    state.contextWindow = 0;
  }
}

loadLayout();
applyLayout();
await refreshProjects();
await loadMeta();
renderAll();
await applyHash();
window.addEventListener('hashchange', applyHash);
