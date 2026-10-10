// 树图：把会话的 entry 树画成一张左→右的平面图。
//
// 布局是最朴素的那种 tidy tree：深度定列，叶子各占一行，父节点垂直居中在
// 它的子节点之间。会话树绝大多数时候就是一条主线加几处分叉，这个朴素做法
// 已经够用，不用上真正的 tidy 算法

import { roleLabel, summarise } from './shared.js';

// 缩到这个比例以下，节点上的小字就没法读了
const MIN_READABLE = 0.65;

const NODE_W = 208;
const TOOL_H = 40;
const NODE_H = 56;
const COL = 272; // 列间距 = 节点宽 + 64
const ROW = 74;

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

    viewport.addEventListener('wheel', (e) => this.onWheel(e), { passive: false });
    viewport.addEventListener('pointerdown', (e) => this.onPointerDown(e));
    viewport.addEventListener('pointermove', (e) => this.onPointerMove(e));
    viewport.addEventListener('pointerup', (e) => this.onPointerUp(e));
    viewport.addEventListener('pointercancel', (e) => this.onPointerUp(e));
  }

  // render 整棵重画。节点数是几十的量级，重画比做增量合并简单、也不会错
  render(entries, { selectedId, leafId, freshId = null }) {
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
      maxX = Math.max(maxX, box.x + NODE_W);
      maxY = Math.max(maxY, box.y + NODE_H);

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

  // layout 算每个节点的 x/y。返回行号是为了让父节点能居中在子节点之间
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
    let row = 0;

    const walk = (entry, depth) => {
      const kids = children.get(entry.id) || [];
      let y;
      if (!kids.length) {
        y = row * ROW;
        row += 1;
      } else {
        const spans = kids.map((kid) => walk(kid, depth + 1));
        y = (Math.min(...spans) + Math.max(...spans)) / 2;
      }
      const height = heightFor(entry);
      this.pos.set(entry.id, { x: depth * COL, y, height });
      return y;
    };

    for (const root of roots) walk(root, 0);

    // 正常文件里走不到这一步；父链成环或断在半路时会有节点没位置，
    // 给它们各自一行摆出来 —— 没位置就等于从图上消失
    for (const entry of entries) {
      if (this.pos.has(entry.id)) continue;
      this.pos.set(entry.id, { x: 0, y: row * ROW, height: heightFor(entry) });
      row += 1;
    }
  }

  nodeElement(entry, box, { selected, leaf }) {
    const msg = entry.message || {};
    const el = document.createElement('article');
    el.className = 'node';
    if (entry.id === this.freshId) el.classList.add('is-fresh');
    if (selected) el.classList.add('is-selected');
    if (leaf) el.classList.add('is-leaf');
    const isTool = msg.role === 'tool_result';
    if (isTool) el.classList.add('node--tool');

    el.style.setProperty('--x', box.x + 'px');
    el.style.setProperty('--y', box.y + 'px');
    el.style.transform = `translate(${box.x}px, ${box.y}px)`;
    el.dataset.id = entry.id;
    el.tabIndex = 0;

    const head = document.createElement('header');
    head.className = 'node-head';
    const role = document.createElement('span');
    role.className = 'node-role';
    role.textContent = roleLabel(msg);
    head.append(role);

    const tag = document.createElement('span');
    tag.className = 'node-tag';
    tag.textContent = tagOf(msg);
    head.append(tag);

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

    el.addEventListener('click', () => this.onSelect(entry.id));
    el.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        this.onSelect(entry.id);
      }
    });
    return el;
  }

  edgeElement(parent, child, onPath) {
    const x1 = parent.x + NODE_W;
    const y1 = parent.y + parent.height / 2;
    const x2 = child.x;
    const y2 = child.y + child.height / 2;
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
    if (event.target.closest('.node')) return;
    this.pointer = { x: event.clientX, y: event.clientY, camX: this.camera.x, camY: this.camera.y };
    this.viewport.classList.add('is-panning');
    this.viewport.setPointerCapture(event.pointerId);
  }

  onPointerMove(event) {
    if (!this.pointer) return;
    this.camera.x = this.pointer.camX + (event.clientX - this.pointer.x);
    this.camera.y = this.pointer.camY + (event.clientY - this.pointer.y);
    this.applyCamera();
  }

  onPointerUp(event) {
    if (!this.pointer) return;
    this.pointer = null;
    this.viewport.classList.remove('is-panning');
    if (this.viewport.hasPointerCapture(event.pointerId)) this.viewport.releasePointerCapture(event.pointerId);
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
    const maxX = Math.max(...boxes.map((b) => b.x + NODE_W));
    const minY = Math.min(...boxes.map((b) => b.y));
    const maxY = Math.max(...boxes.map((b) => b.y + NODE_H));

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
    const right = left + NODE_W * scale;
    const bottom = top + box.height * scale;
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

function heightFor(entry) {
  return entry.message?.role === 'tool_result' ? TOOL_H : NODE_H;
}

// 节点角上那个小词：只在「不是正常答完」的时候才有话说
const STOP_TAG = { tool_use: '调了工具', aborted: '已中止', error: '出错', length: '被截断' };

function tagOf(msg) {
  return STOP_TAG[msg.stopReason] || '';
}

