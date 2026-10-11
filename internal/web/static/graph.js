// 树图：把会话的 entry 树画成一张左→右的平面图。
//
// 布局是最朴素的那种 tidy tree：深度定列，叶子各占一行，父节点垂直居中在
// 它的子节点之间。会话树绝大多数时候就是一条主线加几处分叉，这个朴素做法
// 已经够用，不用上真正的 tidy 算法。
//
// 节点的宽高可以拖右下角改（拖完按会话存 localStorage），所以列宽按该列
// 最宽的节点算、行高按该行最高的算，拿光标尺子量，不是常量

import { roleLabel, summarise } from './shared.js';

// 缩到这个比例以下，节点上的小字就没法读了
const MIN_READABLE = 0.65;

// 默认尺寸；拖过的节点按 sizeOf 里的那份来
const NODE_W = 208;
const NODE_H = 56;
const COL_GAP = 64; // 列距（= 原来的 COL 272 − NODE_W 208）
const ROW_GAP = 18;
const MIN_W = 140;
const MIN_H = 36;

const SIZE_STORE = 'graph-sizes:';

export class Graph {
  // onSelect(entryId)：点节点 / onBranch(entryId)：点「＋」从这里接着聊
  constructor({ viewport, canvas, edges, nodes, onSelect, onBranch }) {
    this.viewport = viewport;
    this.canvas = canvas;
    this.edges = edges;
    this.nodes = nodes;
    this.onSelect = onSelect;
    this.onBranch = onBranch;

    this.pos = new Map();
    this.camera = { x: 40, y: 40, scale: 1 };
    this.freshId = null;
    this.pointer = null;
    this.sizes = new Map(); // entryId -> {w, h}，拖过尺寸的节点才有
    this.sessionId = null;
    this.resizing = null;
    this.queued = false;

    viewport.addEventListener('wheel', (e) => this.onWheel(e), { passive: false });
    viewport.addEventListener('pointerdown', (e) => this.onPointerDown(e));
    viewport.addEventListener('pointermove', (e) => this.onPointerMove(e));
    viewport.addEventListener('pointerup', (e) => this.onPointerUp(e));
    viewport.addEventListener('pointercancel', (e) => this.onPointerUp(e));
  }

  // render 整棵重画。节点数是几十的量级，重画比做增量合并简单、也不会错
  render(entries, { selectedId, leafId, freshId = null, sessionId = null }) {
    // 换会话才换尺寸表。同一个会话里的重画（流式、拖动）不能重新读 ——
    // 正拖着的那次改动会被 localStorage 里的旧值顶掉
    if (sessionId !== this.sessionId) {
      this.sessionId = sessionId;
      this.sizes = loadSizes(sessionId);
    }
    // 拖动中每一帧都照着这份参数重排（见 queueRerender）
    this.lastEntries = entries;
    this.lastOpts = { selectedId, leafId, sessionId };
    this.freshId = freshId;
    this.index = new Map(entries.map((e) => [e.id, e]));
    this.layout(entries);

    // 选中路径只算一次：从选中节点一路往根走，走过的都是。
    // 用 visited 当终止条件：文件坏成环时不至于转不出来
    const pathIds = new Set();
    for (let id = selectedId; id && !pathIds.has(id); id = this.index.get(id)?.parentId || null) pathIds.add(id);

    const nodes = [];
    const paths = [];
    let maxX = 0;
    let maxY = 0;

    for (const entry of entries) {
      const box = this.pos.get(entry.id);
      maxX = Math.max(maxX, box.x + box.size.w);
      maxY = Math.max(maxY, box.y + box.size.h);

      nodes.push(this.nodeElement(entry, box, { selected: entry.id === selectedId, leaf: entry.id === leafId }));

      const parent = entry.parentId && this.pos.get(entry.parentId);
      if (parent) {
        paths.push(this.edgeElement(parent, box, pathIds.has(entry.id) && pathIds.has(entry.parentId)));
      }
    }

    this.edges.setAttribute('width', maxX);
    this.edges.setAttribute('height', maxY);
    this.edges.replaceChildren(...paths);
    this.nodes.replaceChildren(...nodes);
    this.applyCamera();
  }

  // sizeOf 是节点当前该多大：拖过按拖出来的，没拖过按默认
  sizeOf(entry) {
    return this.sizes.get(entry.id) || { w: NODE_W, h: NODE_H };
  }

  // layout 算每个节点的 x/y。两趟：先按子树占的行号摆，再把行号折成像素。
  // 行号得先攒齐所有行的行高才能折算，所以不能一趟到底
  layout(entries) {
    const byId = this.index;
    const children = new Map();
    const roots = [];

    for (const entry of entries) {
      // 父节点不在（文件被截断）就当根，图不能因此崩掉
      if (entry.parentId && byId.has(entry.parentId)) {
        if (!children.has(entry.parentId)) children.set(entry.parentId, []);
        children.get(entry.parentId).push(entry);
      } else {
        roots.push(entry);
      }
    }

    this.pos = new Map();
    const rowHeights = []; // 每个叶子行的高度 = 行里最高的节点
    let row = 0;

    const place = (entry, depth) => {
      const size = this.sizeOf(entry);
      const kids = children.get(entry.id) || [];
      let spot;
      if (!kids.length) {
        spot = row;
        rowHeights[spot] = Math.max(rowHeights[spot] || 0, size.h);
        row += 1;
      } else {
        const spans = kids.map((kid) => place(kid, depth + 1));
        spot = (Math.min(...spans) + Math.max(...spans)) / 2;
      }
      this.pos.set(entry.id, { spot, depth, size, x: 0, y: 0 });
      return spot;
    };

    for (const root of roots) place(root, 0);

    // 正常文件里走不到这一步；父链成环时会有节点没位置，给它们各自一行
    // 摆出来 —— 没位置就等于从图上消失
    const orphans = [];
    for (const entry of entries) {
      if (this.pos.has(entry.id)) continue;
      const size = this.sizeOf(entry);
      this.pos.set(entry.id, { spot: row, depth: 0, size, x: 0, y: 0 });
      rowHeights[row] = Math.max(rowHeights[row] || 0, size.h);
      orphans.push(entry);
      row += 1;
    }

    // 行号 -> 像素：逐行累加「行高 + 行距」
    const rowTop = [];
    let acc = 0;
    for (let i = 0; i < rowHeights.length; i++) {
      rowTop[i] = acc;
      acc += (rowHeights[i] || NODE_H) + ROW_GAP;
    }

    // 再走一趟：把行号折成像素中心，父节点居中在子节点中心之间。
    // centers 先占位再覆写：父链成环时递归会绕回来，读到占位就停
    const centers = new Map();
    const settle = (entry) => {
      if (centers.has(entry.id)) return centers.get(entry.id);
      centers.set(entry.id, 0);
      const kids = children.get(entry.id) || [];
      let center;
      if (!kids.length) {
        const box = this.pos.get(entry.id);
        center = rowTop[box.spot] + box.size.h / 2;
      } else {
        const cs = kids.map(settle);
        center = (Math.min(...cs) + Math.max(...cs)) / 2;
      }
      centers.set(entry.id, center);
      return center;
    };
    for (const root of roots) settle(root);
    for (const entry of orphans) settle(entry);

    // 列位置：每列取该列最宽的节点，逐列累加
    const colWidth = [];
    for (const box of this.pos.values()) {
      colWidth[box.depth] = Math.max(colWidth[box.depth] || 0, box.size.w);
    }
    const colX = [];
    let x = 0;
    for (let depth = 0; depth < colWidth.length; depth++) {
      colX[depth] = x;
      x += (colWidth[depth] || NODE_W) + COL_GAP;
    }

    for (const [id, box] of this.pos) {
      box.x = colX[box.depth];
      box.y = centers.get(id) - box.size.h / 2;
      delete box.spot;
    }
  }

  nodeElement(entry, box, { selected, leaf }) {
    const el = document.createElement('article');
    el.className = 'node';
    if (entry.id === this.freshId) el.classList.add('is-fresh');
    if (selected) el.classList.add('is-selected');
    if (leaf) el.classList.add('is-leaf');
    if (this.sizes.has(entry.id)) el.classList.add('is-sized');
    if (entry.folded) el.classList.add('is-folded');

    el.style.width = box.size.w + 'px';
    el.style.height = box.size.h + 'px';
    el.style.setProperty('--x', box.x + 'px');
    el.style.setProperty('--y', box.y + 'px');
    el.style.transform = `translate(${box.x}px, ${box.y}px)`;
    el.dataset.id = entry.id;

    // message 为空的是幽灵节点（+ 按出来的待分出新分支），没有内容可点可拖
    const msg = entry.message;
    if (!msg) return this.ghostElement(el);

    el.tabIndex = 0;

    const head = document.createElement('header');
    head.className = 'node-head';
    const role = document.createElement('span');
    role.className = 'node-role';
    role.textContent = roleLabel(msg);
    head.append(role);

    // 角标来自这一轮结尾那条（用户消息自己没有 stopReason）：中止/截断标在这儿
    const tag = document.createElement('span');
    tag.className = 'node-tag';
    tag.textContent = STOP_TAG[entry.stopReason] || '';
    head.append(tag);

    // 「压缩点」跟 STOP_TAG 并排。只标这一刀切在哪儿 —— 树上的事实，跟你看哪条支路无关。
    // 「这一轮还在不在上下文里」不在这里标：那个得看当前这条线，右栏的分隔线在说
    if (entry.compaction) head.append(nodeBadge('压缩点'));

    if (leaf) {
      const badge = document.createElement('span');
      badge.className = 'node-leaf-badge';
      badge.textContent = '最新';
      head.append(badge);
    } else {
      const add = document.createElement('button');
      add.className = 'node-add';
      add.type = 'button';
      add.title = '从这里接着聊';
      add.setAttribute('aria-label', '从这里接着聊');
      add.textContent = '+';
      add.addEventListener('click', (e) => {
        e.stopPropagation();
        this.onBranch(entry.id);
      });
      head.append(add);
    }
    el.append(head);

    const text = document.createElement('p');
    text.className = 'node-text';
    text.textContent = summarise(msg);
    el.append(text);

    const grip = document.createElement('span');
    grip.className = 'node-resize';
    grip.title = '拖拽改变大小';
    el.append(grip);

    el.addEventListener('click', () => this.onSelect(entry.id));
    el.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        this.onSelect(entry.id);
      }
    });
    return el;
  }

  // 幽灵节点：+ 按出来、还没有任何内容的待分分支
  ghostElement(el) {
    el.classList.add('node--ghost');
    const head = document.createElement('header');
    head.className = 'node-head';
    const role = document.createElement('span');
    role.className = 'node-role';
    role.textContent = '新分支';
    head.append(role);
    el.append(head);

    const text = document.createElement('p');
    text.className = 'node-text';
    text.textContent = '（还没说）';
    el.append(text);
    return el;
  }

  edgeElement(parent, child, onPath) {
    const x1 = parent.x + parent.size.w;
    const y1 = parent.y + parent.size.h / 2;
    const x2 = child.x;
    const y2 = child.y + child.size.h / 2;
    const bend = Math.max((x2 - x1) * 0.5, 24);

    const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
    path.setAttribute('d', `M ${x1} ${y1} C ${x1 + bend} ${y1}, ${x2 - bend} ${y2}, ${x2} ${y2}`);
    if (onPath) path.classList.add('is-path');
    return path;
  }

  // ── 相机 ───────────────────────────────────────────────

  applyCamera() {
    const { x, y, scale } = this.camera;
    this.canvas.style.transform = `translate(${x}px, ${y}px) scale(${scale})`;
  }

  zoomBy(factor, cx, cy) {
    const next = Math.min(Math.max(this.camera.scale * factor, 0.25), 2.5);
    const ratio = next / this.camera.scale;
    // 缩放锚在光标上：光标下的那一点不动
    this.camera.x = cx - (cx - this.camera.x) * ratio;
    this.camera.y = cy - (cy - this.camera.y) * ratio;
    this.camera.scale = next;
    this.applyCamera();
  }

  onWheel(event) {
    event.preventDefault();
    const rect = this.viewport.getBoundingClientRect();
    const factor = Math.exp(-event.deltaY * 0.0015);
    this.zoomBy(factor, event.clientX - rect.left, event.clientY - rect.top);
  }

  onPointerDown(event) {
    const grip = event.target.closest('.node-resize');
    if (grip) {
      // 捕获挂在 viewport 上而不是手柄上：重画会把节点整个换掉，
      // 挂在手柄上的捕捉拖两下就跟丢了
      const id = grip.closest('.node').dataset.id;
      const { size } = this.pos.get(id);
      this.resizing = { id, x: event.clientX, y: event.clientY, w: size.w, h: size.h };
      this.viewport.classList.add('is-resizing');
      this.viewport.setPointerCapture(event.pointerId);
      event.preventDefault(); // 拖手柄时别连带把节点文字选上
      return;
    }
    if (event.target.closest('.node')) return;
    this.pointer = { x: event.clientX, y: event.clientY, camX: this.camera.x, camY: this.camera.y };
    this.viewport.classList.add('is-panning');
    this.viewport.setPointerCapture(event.pointerId);
  }

  onPointerMove(event) {
    if (this.resizing) {
      const { id, x, y, w, h } = this.resizing;
      const scale = this.camera.scale || 1;
      // 位移折算回画布坐标：缩到 0.5 时，屏幕上拖 1px 是画布上 2px
      this.sizes.set(id, {
        w: Math.max(MIN_W, Math.round(w + (event.clientX - x) / scale)),
        h: Math.max(MIN_H, Math.round(h + (event.clientY - y) / scale)),
      });
      this.queueRerender();
      return;
    }
    if (!this.pointer) return;
    this.camera.x = this.pointer.camX + (event.clientX - this.pointer.x);
    this.camera.y = this.pointer.camY + (event.clientY - this.pointer.y);
    this.applyCamera();
  }

  onPointerUp(event) {
    if (this.resizing) {
      this.resizing = null;
      this.viewport.classList.remove('is-resizing');
      saveSizes(this.sessionId, this.sizes);
      if (this.viewport.hasPointerCapture(event.pointerId)) this.viewport.releasePointerCapture(event.pointerId);
      return;
    }
    if (!this.pointer) return;
    this.pointer = null;
    this.viewport.classList.remove('is-panning');
    if (this.viewport.hasPointerCapture(event.pointerId)) this.viewport.releasePointerCapture(event.pointerId);
  }

  // queueRerender 是拖动缩放中的每一帧。rAF 合帧：一次 pointermove
  // 可能在一帧里来好几个，全画是白画
  queueRerender() {
    if (this.queued) return;
    this.queued = true;
    requestAnimationFrame(() => {
      this.queued = false;
      if (!this.resizing || !this.lastEntries) return;
      // freshId 不发：重画会把「刚落地」的入场动画再放一遍
      this.render(this.lastEntries, { ...this.lastOpts, freshId: null });
    });
  }

  // reset 把镜头归零。换会话时用：新树不该沿用上一棵树拖过去的取景，
  // 不然节点会画在屏幕外，看着像图没画出来
  reset() {
    this.camera = { x: 40, y: 40, scale: 1 };
    this.applyCamera();
  }

  // overview 是打开会话时的取景：先试着装下整棵树，但别缩到字看不清 ——
  // 装不下的部分交给拖拽和缩放。跟按钮上那个「适应画面」不是一回事，
  // 那个的承诺就是把所有节点都装进去
  overview(entryId) {
    this.fit();
    if (this.camera.scale < MIN_READABLE) {
      this.camera.scale = MIN_READABLE;
      this.applyCamera();
    }
    this.reveal(entryId);
  }

  fit() {
    if (!this.pos.size) return;
    const boxes = [...this.pos.values()];
    const minX = Math.min(...boxes.map((b) => b.x));
    const maxX = Math.max(...boxes.map((b) => b.x + b.size.w));
    const minY = Math.min(...boxes.map((b) => b.y));
    const maxY = Math.max(...boxes.map((b) => b.y + b.size.h));

    const pad = 32;
    const vw = this.viewport.clientWidth;
    const vh = this.viewport.clientHeight;
    const scale = Math.min((vw - pad * 2) / (maxX - minX || 1), (vh - pad * 2) / (maxY - minY || 1), 1.1);

    this.camera.scale = Math.min(Math.max(scale, 0.25), 1.1);
    this.camera.x = pad - minX * this.camera.scale + Math.max((vw - pad * 2 - (maxX - minX) * this.camera.scale) / 2, 0);
    this.camera.y = pad - minY * this.camera.scale + Math.max((vh - pad * 2 - (maxY - minY) * this.camera.scale) / 2, 0);
    this.applyCamera();
  }

  // reveal 把某个节点挪进视野。只挪「最少要挪的那一点」，不把它居中 ——
  // 居中会把画面另一头（多半是根）一把推出屏幕，看着像图坏了
  reveal(entryId) {
    const box = this.pos.get(entryId);
    if (!box) return;

    const pad = 24;
    const scale = this.camera.scale;
    const left = box.x * scale + this.camera.x;
    const top = box.y * scale + this.camera.y;
    const right = left + box.size.w * scale;
    const bottom = top + box.size.h * scale;
    const viewW = this.viewport.clientWidth;
    const viewH = this.viewport.clientHeight;

    let dx = 0;
    let dy = 0;
    if (right > viewW - pad) dx = viewW - pad - right;
    if (left + dx < pad) dx = pad - left;
    if (bottom > viewH - pad) dy = viewH - pad - bottom;
    if (top + dy < pad) dy = pad - top;

    if (dx || dy) {
      this.camera.x += dx;
      this.camera.y += dy;
      this.applyCamera();
    }
  }
}

// 尺寸按会话存 localStorage 一份：拖开一次，刷新、切走再回来都还在
function loadSizes(sessionId) {
  const sizes = new Map();
  if (!sessionId) return sizes;
  try {
    const raw = JSON.parse(localStorage.getItem(SIZE_STORE + sessionId) || '{}');
    for (const [id, size] of Object.entries(raw)) {
      if (size && size.w > 0 && size.h > 0) sizes.set(id, size);
    }
  } catch {
    // 存坏了就当没存过：图照样得画出来
  }
  return sizes;
}

function saveSizes(sessionId, sizes) {
  if (!sessionId) return;
  try {
    localStorage.setItem(SIZE_STORE + sessionId, JSON.stringify(Object.fromEntries(sizes)));
  } catch {
    // 存不下顶多影响下次刷新，不影响眼前
  }
}

// 节点角上那个小词：只在「不是正常答完」的时候才有话说
const STOP_TAG = { tool_use: '调了工具', aborted: '已中止', error: '出错', length: '被截断' };

// nodeBadge 是节点角上的小角标（现在只剩压缩点）。不吃 margin-left: auto ——
// 右对齐是「最新」/「+」的位置
function nodeBadge(text) {
  const badge = document.createElement('span');
  badge.className = 'node-badge';
  badge.textContent = text;
  return badge;
}
