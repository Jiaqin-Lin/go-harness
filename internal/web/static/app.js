// 界面状态与接线。所有状态都在这一个对象里，渲染是「状态变了就重画」，
// 只有流式那一轮走增量（见 dom.live / state.streaming）

import { getJSON, request, streamChat } from './api.js';
import { renderMessage, renderCompaction, updateMessage, setSkillPaths, setImageCtx } from './chat.js';
import { Graph } from './graph.js';
import { DirPicker } from './dirpicker.js';
import { SkillMenu, SkillsModal } from './skills.js';
import { SettingsModal } from './settings.js';
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
  images: el('composer-images'),
  queue: el('composer-queue'),
  imagePick: el('image-pick'),
  imageFile: el('image-file'),
  steer: el('queue-steer'),
  follow: el('queue-follow'),
  skillPick: el('skill-pick'),
  skillChip: el('skill-chip'),
  skillMenu: el('skill-menu'),
  skillsModal: el('skills'),
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
  skillChip: null,   // 这一轮强制要用的技能名；只对下一条消息生效，发出去就清
  images: [],        // 还没发出去的图：{mime, bytes, name, dataUrl}
  queued: [],        // 运行中发出去、还没投递的：{id, mode, text, images}
  notice: '',        // 这一会儿要顶在 hint 上的那句话（超限、刚跑完之类）
  live: [],          // 这一轮已经画出来、还没落盘的消息
  streaming: null,   // 正在长的那个元素
  connecting: null,  // 「正在连接模型…」那行占位
  freshId: null,
  sawDone: false,    // 这一轮的流有没有正常收尾（没有就可能是断了）
  streamError: '',   // 流里报的错，收尾时再显示
  stopping: false,   // 用户点过停止（跟「连接断了」不是一回事）
  contextWindow: 0,  // 模型的上下文窗口，启动时问 /api/meta（拿不到就 0 = 不显示分母）
  compactAt: 0,      // 设置里那个压缩触发点（0 = 没设过、用默认）
  compactLimit: 0,   // 实际会触发的数，服务端算好给的（不压是 0）
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

const skillsMenu = new SkillMenu({
  root: dom.skillMenu,
  onPick: (name) => {
    state.skillChip = name;
    renderComposer();
  },
  onManage: () => skillsModal.open(state.cwd),
});

// refreshSkillPaths 把「SKILL.md 路径 → 技能名」喂给 chat.js，模型 read 技能时
// 界面才认得出来。读不到就当没装技能：退回普通工具行，不影响别的。
//
// 技能是磁盘上的目录，用户完全可以绕过界面直接往 skills/ 里放一份——所以这张表
// 不能只在装东西的时候刷：开页面、开一轮、管理界面里动过之后都得重来一遍
async function refreshSkillPaths(cwd = state.cwd) {
  if (!cwd) {
    setSkillPaths(new Map());
    return;
  }
  try {
    const data = await getJSON(`/api/skills?cwd=${encodeURIComponent(cwd)}`);
    setSkillPaths(new Map((data.skills || []).map((s) => [s.path, s.name])));
  } catch {
    setSkillPaths(new Map());
  }
  // 表是后到的，历史是先画的：不重画一遍，已经显示出来的 read 行永远认不出来
  renderChat();
}

const skillsModal = new SkillsModal({
  root: dom.skillsModal,
  // 停用/删掉的可能正是 chip 上选的那一个：选中的技能不生效了，chip 就得撤掉。
  // 顺带重拉一次路径表：刚装/刚删的技能，历史记录里的标记也得跟着变
  onChanged: async () => {
    refreshSkillPaths();
    if (!state.skillChip || !state.cwd) return;
    let usable = [];
    try {
      const data = await getJSON(`/api/skills?cwd=${encodeURIComponent(state.cwd)}`);
      usable = (data.skills || []).filter((s) => s.enabled && !s.shadowed && !s.error);
    } catch {
      /* 读不到就当它还可用，服务端那边还会再校一次 */
    }
    if (!usable.some((s) => s.name === state.skillChip)) {
      state.skillChip = null;
      renderComposer();
    }
  },
});

const settingsModal = new SettingsModal({
  root: el('settings'),
  onSaved: (meta) => {
    applyMeta(meta);
    renderChat(); // 右栏那行「上下文 x / 压缩线 y」跟着变
  },
});

el('open-settings').addEventListener('click', () => settingsModal.open({ compactAt: state.compactAt }));

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
  if (state.cwd !== cwd) state.skillChip = null; // 技能是按项目扫的，换项目别把上一个项目的选择带过来
  state.cwd = cwd;
  state.images = []; // 图是给「这一句」准备的，换了会话就作废
  state.notice = '';
  refreshSkillPaths(cwd);
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

  if (state.cwd !== cwd) {
    state.skillChip = null; // 同上：技能是跟着项目走的
    // 等技能表到齐再画：不然打开一个用过技能的历史会话，那几行会先闪一下普通
    // 工具行的样子（表是异步来的，历史是当场画的）
    await refreshSkillPaths(cwd);
  }
  state.cwd = cwd;
  state.sessionId = id;
  state.draft = false;
  state.images = [];
  state.notice = '';
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

// pathTo 是「根 → entryId」这一条路径：右栏画选中节点那条，图上标折叠用的是
// 叶子那条（「模型现在看不看得到」看的是叶子）。树上别的支路不进来 ——
// 那正是分叉互不影响的含义
function pathTo(entryId) {
  if (!entryId) return [];
  const byId = new Map(state.entries.map((entry) => [entry.id, entry]));
  const path = [];
  for (let id = entryId, guard = 0; id && guard < 10000; guard++) {
    const entry = byId.get(id);
    if (!entry) break;
    path.push(entry);
    id = entry.parentId || null;
  }
  return path.reverse();
}

function pathEntries() {
  return pathTo(state.selected);
}

// foldedSet 是路径上不再进上下文的 entry：最后一个压缩点之前的历史全算
// （firstKeptEntryId 以前的，连同更早的压缩点），规则照抄 Session.contextPath
function foldedSet(path) {
  const folded = new Set();
  let last = -1;
  path.forEach((entry, i) => { if (entry.compaction) last = i; });
  if (last < 0) return folded; // 路径上没有压缩点 = 一条都没折
  const firstKept = path[last].compaction.firstKeptEntryId;
  let kept = false;
  path.slice(0, last).forEach((entry) => {
    if (entry.id === firstKept) kept = true;
    if (!kept || entry.compaction) folded.add(entry.id);
  });
  return folded;
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

  // 哪几轮不再进上下文：只用来把那些轮画暗一点，不挂角标（角标只留给树上的
  // 事实，见 graph.js）。基准跟右栏的历史同一套 —— 你选中的那条线，
  // 没选就是当前边上的叶子。灰的含义是「在你正看的这条线上被折掉了」，
  // 右栏的折线讲的是同一件事，两个视图对不上就没法看了
  const viewed = state.selected || state.leafId;
  const path = pathTo(viewed);
  const folded = foldedSet(path);
  // 看的这条线上的 entry，用来看某一轮在不在线上（见下面 folded 的判法）
  const onViewPath = new Set(path.map((entry) => entry.id));

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
      // 按用户消息判断：切点可以落在 assistant 上，那一轮问题被折、回答还在，
      // 图上显示的就是问题。
      //
      // 灰得同时满足两个条件：这一轮有一步在看的这条线上，且那一步被折了。
      // 「不在这一条线上」和「被压缩折掉」是两回事 —— 往下的路在别处接着长的
      // 轮次不属于现在这条线，把它画灰会让人以为它曾经在上下文里；
      // 而压缩点常常正好挂在上一轮的末尾，只看轮次结尾（end）会把
      // 「root 连同它那段历史一起被压掉」漏判成亮的
      folded: steps.some((s) => onViewPath.has(s.id) && folded.has(s.id)), // 只决定画不画暗，不挂角标
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
  const folded = foldedSet(path);
  // 图片是按会话现取的（接口不带图片内容），画历史之前先把定位交给 chat.js
  setImageCtx({ cwd: state.cwd, sessionId: state.sessionId });
  // 折叠段是路径的前缀（firstKeptEntryId 自己永远进上下文），所以第一条还进
  // 上下文的 entry 的下标就是被折掉的条数，也就是分隔线的落点
  const cut = path.findIndex((entry) => !folded.has(entry.id));
  const nodes = path.map((entry) => (entry.compaction
    ? renderCompaction(entry, { folded: folded.has(entry.id) })
    : renderMessage(entry.message || {}, { folded: folded.has(entry.id), skill: entry.skill, entryId: entry.id })));
  if (cut > 0) nodes.splice(cut, 0, foldLine(cut));
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

  // 压缩点不是消息：它没有正文，进上下文的是一条现造的摘要
  const parts = [`${path.filter((entry) => entry.message).length} 条消息`];
  if (last) {
    const used = numTokens(last.input + last.output);
    // 分母给压缩线而不是模型窗口：真正决定「什么时候会压」的是它（见 compact.Limit）
    parts.push(`上下文 ${used}${state.compactLimit ? ` / 压缩线 ${numTokens(state.compactLimit)}` : ''}`);
    if (last.input > 0) parts.push(`缓存 ${Math.round((last.cached / last.input) * 100)}%`);
  }
  return parts.join(' · ');
}

function renderComposer() {
  // 跑着的时候输入框照样能打：这一句发出去不是新一轮，而是排到正在跑的那一轮上
  // （插话 / 排队，见 queueMessage）。选中历史节点时（sendParent 为 null）不能发，
  // 除非正在跑 —— 排队的消息对着的是正在跑的那条线，跟选了哪个节点无关
  const canType = Boolean(state.sessionId) && (state.busy || sendParent() !== null);
  const canSend = canType && Boolean(dom.input.value.trim() || state.images.length);

  dom.input.disabled = !canType;
  dom.stop.hidden = !state.busy;
  dom.remove.hidden = state.draft || !state.sessionId;
  dom.hint.textContent = state.notice || (state.busy ? busyHint() : branchHint());

  // 跑着的时候发送区换成两个明确的动作：插话 / 排队，按哪个发生哪个，没有额外的开关
  dom.send.hidden = state.busy;
  dom.send.disabled = !canSend;
  dom.steer.hidden = !state.busy;
  dom.follow.hidden = !state.busy;
  dom.steer.disabled = !canSend;
  dom.follow.disabled = !canSend;

  // 技能按钮只看 cwd：还没选会话时也得能进管理界面（管理弹窗按 cwd 走，跟会话无关）。
  // 跑着的时候不给选：技能是按整轮注进 system prompt 的，插话吃到的是上一轮的
  dom.skillPick.disabled = !state.cwd || state.busy;
  dom.skillChip.hidden = !state.skillChip;
  if (state.skillChip) dom.skillChip.querySelector('.skill-chip-label').textContent = state.skillChip;

  dom.imagePick.disabled = !canType;
  renderImages();
  renderQueue();
}

// busyHint 把两个按钮的区别摆在字面上 —— 这个界面最要紧的就是别让人猜。
// 窄面板里这行会被省略号截掉，所以短着写（完整的解释在按钮的悬停提示里）
function busyHint() {
  return '正在跑… · 插话：做完当前这步就看到 · 排队：这轮跑完再处理';
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
  // 跑着的时候这一句不是新一轮：回车走插话，排队要点那个按钮（见 queueMessage）
  if (state.busy) {
    await queueMessage('steer');
    return;
  }

  const text = dom.input.value.trim();
  const images = state.images;
  const parent = sendParent();
  if ((!text && !images.length) || !state.sessionId || parent === null) return;

  refreshSkillPaths(); // 手工丢进 skills/ 的技能，这一轮就该认得出来（不等它，回来会重画）
  const entriesBefore = state.entries.length;
  const skill = state.skillChip; // 只对这一句生效：发出去就把 chip 清掉
  state.sawDone = false;
  state.streamError = '';
  state.stopping = false;
  state.notice = '';
  state.skillChip = null;

  // 先把用户这句画上去；服务端落盘之后发 entry 事件来认领它（见 adoptEntry）。
  // 技能名跟着一起画：不用等落盘回来，选中过就该在气泡旁边看得见
  const userMsg = { role: 'user', blocks: userBlocks(text, images) };
  const userEl = renderMessage(userMsg, { skill });
  withPin(() => dom.live.append(userEl));
  state.live.push({ el: userEl, msg: userMsg });

  dom.input.value = '';
  state.images = [];
  autoGrow();
  state.busy = true;
  const controller = new AbortController();
  state.abort = controller;
  renderComposer();

  let failure = '';
  try {
    await streamChat(
      {
        cwd: state.cwd,
        sessionId: state.sessionId,
        parentId: parent,
        text,
        skill: skill || undefined,
        images: encodeImages(images),
      },
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
  // 别让人重打一遍。技能选择也一样还回去 —— 被一次连不上吃掉太亏
  if (!stopped && state.entries.length === entriesBefore && (text || images.length) && !dom.input.value.trim()) {
    dom.input.value = text;
    if (images.length) state.images = [...images, ...state.images].slice(0, MAX_IMAGES);
    autoGrow();
    if (skill && !state.skillChip) state.skillChip = skill;
  }

  // 还挂着没投递的队列条目：中止时服务端把队列丢了，连接断了则是再没人管它 ——
  // 两种都还回输入框，排过队的东西不该凭空消失
  if (state.queued.length) {
    for (const item of state.queued) restoreToInput(item);
    state.queued = [];
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

// ── 图片 ─────────────────────────────────────────────────
// 上限跟服务端同一套（server/chat.go 的 maxImages / maxImageRaw）。前端拦一道
// 是为了说清是哪张、为什么 —— 撞 413/400 才知道超限就太晚了

const MAX_IMAGES = 8;
const MAX_IMAGE_BYTES = 4 << 20;

// userBlocks 是一条用户消息的形状：正文 + 图。发出去和画在界面上都是它
function userBlocks(text, images) {
  const blocks = [];
  if (text) blocks.push({ kind: 'text', text });
  for (const image of images) {
    blocks.push({ kind: 'image', imageMime: image.mime, imageBytes: image.bytes, dataUrl: image.dataUrl });
  }
  return blocks;
}

// encodeImages 走线时只给 base64（dataURL 逗号后面那段）：格式由服务端嗅探，
// 前端说的 mime 不算数
function encodeImages(images) {
  return images.map((image) => image.dataUrl.slice(image.dataUrl.indexOf(',') + 1));
}

// addImages 收下这批文件。读成 dataURL 存着：发出去要它，画缩略图也要它
async function addImages(files) {
  const room = MAX_IMAGES - state.images.length;
  const accepted = [];
  let problem = '';
  for (const file of files) {
    if (!file.type.startsWith('image/')) continue;
    if (accepted.length >= room) {
      problem ||= `一次最多 ${MAX_IMAGES} 张图，多的没加进来`;
      break;
    }
    if (file.size > MAX_IMAGE_BYTES) {
      problem ||= `「${file.name || '一张图'}」${sizeText(file.size)}，超过单张 ${MAX_IMAGE_BYTES >> 20}MB 的上限`;
      continue;
    }
    accepted.push(file);
  }

  for (const file of accepted) {
    try {
      state.images.push({ mime: file.type, bytes: file.size, name: file.name, dataUrl: await readDataURL(file) });
    } catch {
      problem ||= `「${file.name || '一张图'}」读不出来`;
    }
  }

  state.notice = problem;
  renderComposer();
}

function readDataURL(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result);
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(file);
  });
}

function sizeText(bytes) {
  return bytes >= 1 << 20 ? `${(bytes / (1 << 20)).toFixed(1)}MB` : `${Math.round(bytes / 1024)}KB`;
}

// renderImages 画还没发出去的那几张：缩略图 + 张数
function renderImages() {
  dom.images.hidden = !state.images.length;
  const nodes = state.images.map((image, index) => {
    const chip = document.createElement('div');
    chip.className = 'image-chip';
    chip.title = `${image.name || '图片'} · ${sizeText(image.bytes)}`;

    const img = document.createElement('img');
    img.src = image.dataUrl;
    img.alt = image.name || '图片';

    const drop = document.createElement('button');
    drop.type = 'button';
    drop.className = 'image-chip-x';
    drop.textContent = '×';
    drop.setAttribute('aria-label', '拿掉这张图');
    drop.addEventListener('click', () => {
      state.images.splice(index, 1);
      renderComposer();
    });

    chip.append(img, drop);
    return chip;
  });
  nodes.push(span('image-count', `${state.images.length}/${MAX_IMAGES}`));
  dom.images.replaceChildren(...nodes);
}

// ── 运行中发的话（插话 / 排队）─────────────────────────────

// queueMessage 运行中发一句：不开新流，只把它排到正在跑的那一轮上。mode 是
// 「什么时候让模型看到它」—— 插话（steer）在工具批次边界投，排队（followUp）
// 等这轮本来要停时投。服务端投递时会在原来那条流上报一声（queued_delivered），
// 那一刻它才变成对话里的气泡；在那之前它只挂在输入框上面的等待区里
async function queueMessage(mode) {
  const text = dom.input.value.trim();
  const images = state.images;
  if ((!text && !images.length) || !state.sessionId) return;
  state.notice = '';

  try {
    const res = await request('POST', '/api/chat/queue', {
      cwd: state.cwd,
      sessionId: state.sessionId,
      text,
      images: encodeImages(images),
      mode,
    });
    state.queued.push({ id: res.id, mode, text, images });
    dom.input.value = '';
    state.images = [];
    autoGrow();
  } catch (err) {
    // 409 = 那一轮刚好跑完了。这句话还在输入框里，等流收完尾再发就是新一轮；
    // 别的错（服务端挂了之类）把原话摆出来
    state.notice = err.status === 409 ? '这一轮刚跑完 —— 再发一次就是新一轮了' : err.message;
  }
  renderComposer();
}

// dequeue 撤回一条还没投递的：服务端还拿着（removed）就连文本带图还回输入框；
// 已经被投出去的（!removed）只摘 chip —— 它已经变成对话里的气泡了
async function dequeue(item) {
  let removed = false;
  try {
    ({ removed } = await request('POST', '/api/chat/dequeue', {
      cwd: state.cwd,
      sessionId: state.sessionId,
      id: item.id,
    }));
  } catch {
    // 问不到服务端就当它已经投出去了：至少别在界面上留个撤不掉的幽灵
  }
  state.queued = state.queued.filter((queued) => queued !== item);
  if (removed) restoreToInput(item);
  renderComposer();
}

// restoreToInput 把一条还没投递的消息还回输入框：撤回了、或者这一轮中止/断了
// 再没人管它了。文字接在现有内容后面，图并回缩略图行（上限照旧）
function restoreToInput(item) {
  if (item.text) {
    dom.input.value = dom.input.value.trim() ? `${dom.input.value}\n${item.text}` : item.text;
  }
  if (item.images.length) {
    state.images = [...item.images, ...state.images].slice(0, MAX_IMAGES);
  }
  autoGrow();
}

// deliverQueued 排队的那条投出去了：chip 摘掉，按它落地的位置画成对话里的气泡。
// 本地那份带着图的 dataURL（服务端发来的消息里只有形状），所以按本地的画；
// 之后落盘的 entry 事件会照旧认领它（adoptEntry 按角色顺序对得上）
function deliverQueued(event) {
  const index = state.queued.findIndex((item) => item.id === event.queueId);
  const item = index >= 0 ? state.queued.splice(index, 1)[0] : null;

  const msg = item
    ? { role: 'user', blocks: userBlocks(item.text, item.images) }
    : { role: 'user', blocks: event.message?.blocks || [] };
  const element = renderMessage(msg, {});
  withPin(() => dom.live.append(element));
  state.live.push({ el: element, msg });
  renderComposer();
}

// renderQueue 画排着队的那几条
function renderQueue() {
  dom.queue.hidden = !state.queued.length;
  dom.queue.replaceChildren(...state.queued.map((item) => {
    const chip = document.createElement('div');
    chip.className = 'queue-chip' + (item.mode === 'steer' ? ' queue-chip--steer' : '');

    const drop = document.createElement('button');
    drop.type = 'button';
    drop.className = 'queue-chip-x';
    drop.textContent = '×';
    drop.title = '撤回这一句（还没投递就还回输入框）';
    drop.addEventListener('click', () => dequeue(item));

    chip.append(
      span('queue-chip-mode', item.mode === 'steer' ? '⚡ 插话' : '⏳ 排队'),
      span('queue-chip-text', queueLabel(item)),
      drop,
    );
    return chip;
  }));
}

// queueLabel 是 chip 上那行字：只发了图的话没有正文可写
function queueLabel(item) {
  const text = clip(item.text, 60);
  if (text) return item.images.length ? `${text}（图 ${item.images.length}）` : text;
  return `图 ${item.images.length} 张`;
}

function handleEvent(event) {
  switch (event.type) {
    case 'compacting':
      showConnecting('正在压缩上下文…');
      break;
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
    case 'queued_delivered':
      deliverQueued(event);
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

// 发出去到模型开始吐字之间有一段空档（推理模型首 token 要等好几秒，
// 压缩上下文还要先花一次调用），先放一行字，别让右栏看着像没反应
function showConnecting(text = '正在连接模型…') {
  if (state.streaming) return;
  if (state.connecting) {
    state.connecting.textContent = text;
    return;
  }
  const element = document.createElement('div');
  element.className = 'chat-empty';
  element.textContent = text;
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
    updateMessage(live.el, entry.message || {}, { skill: entry.skill, entryId: entry.id });
    dom.history.append(live.el);
  } else if (entry.compaction) {
    // 压缩点没有正文，画的是一段摘要
    dom.history.append(renderCompaction(entry));
  } else {
    dom.history.append(renderMessage(entry.message || {}, { skill: entry.skill, entryId: entry.id }));
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
  state.notice = ''; // 又动手打字了，上一句提示该让位了
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

// ── 图片的三条入口：粘贴、拖进来、选文件 ─────────────────

dom.input.addEventListener('paste', (event) => {
  const files = [...(event.clipboardData?.files || [])].filter((file) => file.type.startsWith('image/'));
  if (!files.length) return; // 没图就照旧粘贴文本，别拦
  event.preventDefault();
  // 图文一起粘的（比如从网页上复制）：文字别丢
  const text = event.clipboardData.getData('text/plain');
  if (text) dom.input.setRangeText(text, dom.input.selectionStart, dom.input.selectionEnd, 'end');
  addImages(files);
});

dom.imagePick.addEventListener('click', () => dom.imageFile.click());
dom.imageFile.addEventListener('change', () => {
  addImages(dom.imageFile.files);
  dom.imageFile.value = ''; // 清掉才能再选同一个文件
});

// 拖拽落在整页上都要挡一道：不挡的话浏览器会直接打开那张图，界面就没了
for (const type of ['dragover', 'drop']) {
  document.addEventListener(type, (event) => event.preventDefault());
}
dom.composer.addEventListener('dragover', () => dom.composer.classList.add('is-dropping'));
dom.composer.addEventListener('dragleave', (event) => {
  // 掠过子元素也会报 dragleave，落在 composer 里面就不算离开
  if (event.relatedTarget && dom.composer.contains(event.relatedTarget)) return;
  dom.composer.classList.remove('is-dropping');
});
dom.composer.addEventListener('drop', (event) => {
  dom.composer.classList.remove('is-dropping');
  addImages(event.dataTransfer?.files || []);
});

// ── 运行中的两个动作：插话 / 排队 ─────────────────────────

dom.steer.addEventListener('click', () => queueMessage('steer'));
dom.follow.addEventListener('click', () => queueMessage('followUp'));

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
// 技能菜单挂在这一行：选中的技能只对下一条消息生效，所以没选会话（还没定 cwd）时
// 按钮是灰的，但管理弹窗在选定了项目、还没有会话的时候就该能进
dom.skillPick.addEventListener('click', () => {
  if (state.cwd) skillsMenu.toggle(state.cwd);
});
dom.skillChip.addEventListener('click', (event) => {
  if (!event.target.closest('.skill-chip-x')) return; // 点 chip 正文不开菜单，只有 × 有用
  state.skillChip = null;
  renderComposer();
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

// foldLine 是折叠段和还在上下文里的那段之间的分隔线：上面正好 count 条是折的
function foldLine(count) {
  const line = document.createElement('div');
  line.className = 'fold-line';
  line.textContent = `以上 ${count} 条已折叠`;
  return line;
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

// 模型信息：窗口多大、压缩线在哪。拉不到就算了，右栏那行字少个分母而已
async function loadMeta() {
  try {
    applyMeta(await getJSON('/api/meta'));
  } catch {
    applyMeta(null);
  }
}

function applyMeta(meta) {
  state.contextWindow = meta?.contextWindow || 0;
  state.compactAt = meta?.compactAt || 0;
  state.compactLimit = meta?.compactLimit || 0;
}

loadLayout();
applyLayout();
await refreshProjects();
await loadMeta();
renderAll();
await applyHash();
window.addEventListener('hashchange', applyHash);
