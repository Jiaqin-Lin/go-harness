// 技能：输入框上方的选择菜单 + 管理弹窗。
//
// 选中只对下一条消息生效（发出去就清），所以这里只管「选中了什么」，
// 清空和重画由 app.js 做

import { getJSON, request } from './api.js';
import { clip } from './shared.js';

async function loadSkills(cwd) {
  const data = await getJSON(`/api/skills?cwd=${encodeURIComponent(cwd)}`);
  return { list: data.skills || [], userDir: data.userDir || '', projectDir: data.projectDir || '' };
}

function note(text) {
  const el = document.createElement('p');
  el.className = 'dir-empty';
  el.textContent = text;
  return el;
}

// 技能名 + 标记 + 描述。文字都走 textContent，不拼 HTML —— 名字和描述都是从
// SKILL.md 里读出来的，是用户自己放的文件
function skillItem(skill, extras = []) {
  const box = document.createElement('div');
  box.className = 'skill-text';

  const name = document.createElement('span');
  name.className = 'skill-name';
  name.textContent = skill.name;
  box.append(name);
  for (const extra of extras) {
    if (extra) box.append(extra);
  }

  if (skill.description) {
    const desc = document.createElement('span');
    desc.className = 'skill-desc';
    desc.textContent = clip(skill.description, 90);
    box.append(desc);
  }
  return box;
}

function badge(text, kind) {
  const el = document.createElement('span');
  el.className = 'skill-badge' + (kind ? ` skill-badge--${kind}` : '');
  el.textContent = text;
  return el;
}

// ── 输入框上方的选择菜单 ─────────────────────────────────

export class SkillMenu {
  // onPick(name)：选中；onManage()：去管理弹窗
  constructor({ root, onPick, onManage }) {
    this.root = root;
    this.onPick = onPick;
    this.onManage = onManage;

    document.addEventListener('click', (e) => {
      if (this.root.hidden) return;
      // 点菜单里、或者点在「技能」按钮上（那下由按钮自己管开关）都不算点外面
      if (this.root.contains(e.target) || e.target.closest('#skill-pick')) return;
      this.close();
    });
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && !this.root.hidden) this.close();
    });
  }

  async toggle(cwd, opts) {
    if (!this.root.hidden) {
      this.close();
      return;
    }
    await this.open(cwd, opts);
  }

  // canPick=false（现在看的是历史节点）：技能项列出来但点不了 —— 换个节点
  // 再回最后一个节点就能选。管理技能不受影响
  async open(cwd, { canPick = true } = {}) {
    this.root.hidden = false;
    this.root.replaceChildren(note('正在读技能…'));

    let data;
    try {
      data = await loadSkills(cwd);
    } catch (err) {
      this.root.replaceChildren(note(`读不了技能：${err.message}`));
      return;
    }

    // 只列生效的：停用的、被同名盖住的、读不出来的都强制不了（服务端也会 400）
    const usable = data.list.filter((s) => s.enabled && !s.shadowed && !s.error);
    const items = usable.map((skill) => {
      const row = document.createElement('button');
      row.type = 'button';
      row.className = 'skill-menu-item';
      row.disabled = !canPick;
      if (!canPick) row.title = '看的是历史节点 —— 回到最后一个节点才能选技能';
      row.append(skillItem(skill, skill.scope === 'project' ? [badge('项目', 'project')] : []));
      row.addEventListener('click', () => {
        this.close();
        this.onPick(skill.name);
      });
      return row;
    });

    const manage = document.createElement('button');
    manage.type = 'button';
    manage.className = 'skill-menu-item skill-menu-manage';
    manage.textContent = '管理技能…';
    manage.addEventListener('click', () => {
      this.close();
      this.onManage();
    });

    const hint = canPick ? [] : [note('看的是历史节点 —— 回到最后一个节点才能选技能')];
    this.root.replaceChildren(...hint, ...items, manage);
  }

  close() {
    this.root.hidden = true;
  }
}

// ── 管理弹窗 ─────────────────────────────────────────────

export class SkillsModal {
  // onChanged()：启停 / 删除 / 装完之后叫一声
  constructor({ root, onChanged }) {
    this.root = root;
    this.onChanged = onChanged;
    this.cwd = '';
    this.list = []; // 最近一次读到的全量（两层都在里面），过滤只在这里面挑
    this.filter = 'all';
    this.noteEl = root.querySelector('#skills-note');
    this.listEl = root.querySelector('#skills-list');
    this.userDirEl = root.querySelector('#skills-userdir');
    this.projDirEl = root.querySelector('#skills-projdir');
    this.filterEl = root.querySelector('#skills-filter');
    this.scopeEl = root.querySelector('#skills-scope');
    this.fileEl = root.querySelector('#skills-file');

    root.querySelector('#skills-close').addEventListener('click', () => this.close());
    root.addEventListener('click', (e) => {
      if (e.target === root) this.close(); // 点遮罩关掉
    });
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && !this.root.hidden) this.close();
    });
    // 「上传 zip」只是把系统文件选择器叫出来，真装是在选完文件之后（change）
    root.querySelector('#skills-upload').addEventListener('click', () => this.fileEl.click());
    this.fileEl.addEventListener('change', () => this.install());
    // 换过滤不用重读磁盘：东西都在 this.list 里
    this.filterEl.addEventListener('click', (event) => {
      const button = event.target.closest('[data-scope]');
      if (!button) return;
      this.filter = button.dataset.scope;
      this.renderList();
    });
  }

  async open(cwd) {
    this.cwd = cwd;
    this.root.hidden = false;
    this.note('');
    await this.refresh();
  }

  close() {
    this.root.hidden = true;
  }

  note(text) {
    this.noteEl.textContent = text;
  }

  async refresh() {
    let data;
    try {
      data = await loadSkills(this.cwd);
    } catch (err) {
      this.list = [];
      this.listEl.replaceChildren(note(`读不了技能：${err.message}`));
      return;
    }

    // 两个根目录是服务端算的（用户级跟家目录走、项目级跟着当前项目走），
    // 这里只负责显示，别在前端拼路径
    this.userDirEl.textContent = data.userDir;
    this.projDirEl.textContent = data.projectDir;
    this.list = data.list;
    this.renderList();
  }

  // renderList 按当前过滤画列表 —— 列表里是两层的技能，过滤只看这儿的
  // this.filter，跟页脚那个「zip 装到哪一层」的下拉没关系
  renderList() {
    const rows = this.list
      .filter((skill) => this.filter === 'all' || skill.scope === this.filter)
      .map((skill) => this.row(skill));

    const empty = this.filter === 'all'
      ? note('还没有技能。把技能目录放到上面任意一个目录下的 <名字>/SKILL.md，或者上传一个 zip')
      : note('这一层下还没有技能。另一层的在上面两个目录里，或者切回「全部」看两边');
    this.listEl.replaceChildren(...(rows.length ? rows : [empty]));

    for (const button of this.filterEl.querySelectorAll('[data-scope]')) {
      button.classList.toggle('is-on', button.dataset.scope === this.filter);
    }
  }

  row(skill) {
    const row = document.createElement('div');
    row.className = 'skill-row' + (skill.error ? ' is-broken' : '');

    const toggle = document.createElement('input');
    toggle.type = 'checkbox';
    toggle.checked = skill.enabled;
    toggle.disabled = Boolean(skill.error);
    toggle.title = skill.enabled ? '停用（下一轮起不再进上下文）' : '启用';
    toggle.addEventListener('change', () => this.toggle(skill, toggle.checked));

    const extras = [];
    extras.push(badge(skill.scope === 'project' ? '项目' : '用户', skill.scope === 'project' ? 'project' : ''));
    if (skill.shadowed) extras.push(badge('被覆盖'));
    if (skill.error) extras.push(badge(skill.error, 'warn'));

    const text = skillItem(skill, extras);
    const path = document.createElement('span');
    path.className = 'skill-path';
    path.textContent = clip(skill.dir, 60);
    path.title = skill.path;
    text.append(path);

    const remove = document.createElement('button');
    remove.type = 'button';
    remove.className = 'btn btn-ghost btn-icon skill-remove';
    remove.textContent = '×';
    remove.title = '删除';
    remove.addEventListener('click', () => this.remove(skill));

    row.append(toggle, text, remove);
    return row;
  }

  async toggle(skill, enabled) {
    try {
      await request('POST', '/api/skills/enable', {
        cwd: this.cwd, scope: skill.scope, name: skill.name, enabled,
      });
      this.note(enabled ? `已启用 ${skill.name}` : `已停用 ${skill.name}（下一轮起不再进上下文）`);
    } catch (err) {
      this.note(err.message);
    }
    await this.refresh();
    this.onChanged();
  }

  async remove(skill) {
    if (!confirm(`删掉技能「${skill.name}」？它的目录和里面的文件会一起删掉。`)) return;
    try {
      await request(
        'DELETE',
        `/api/skills?cwd=${encodeURIComponent(this.cwd)}&scope=${skill.scope}&name=${encodeURIComponent(skill.name)}`,
      );
      this.note(`已删除 ${skill.name}`);
    } catch (err) {
      this.note(err.message);
    }
    await this.refresh();
    this.onChanged();
  }

  async install() {
    const file = this.fileEl.files[0];
    if (!file) return; // 文件选择器被取消了

    let bytes;
    try {
      bytes = await file.arrayBuffer();
    } catch (err) {
      this.note(`读不了这个文件：${err.message}`);
      return;
    }

    this.note(`正在上传 ${file.name}…`);
    const body = {
      cwd: this.cwd,
      scope: this.scopeEl.value,
      filename: file.name,
      zip: toBase64(bytes),
    };

    try {
      const res = await request('POST', '/api/skills/install', body);
      this.note(`已装 ${res.skill.name}（${res.files} 个文件）`);
    } catch (err) {
      // 同名的已经在了：问一句再覆盖，别自己替用户做决定
      if (err.status !== 409 || !confirm(`${err.message}，覆盖吗？`)) {
        this.note(err.message);
        await this.refresh();
        return;
      }
      try {
        const res = await request('POST', '/api/skills/install', { ...body, overwrite: true });
        this.note(`已覆盖 ${res.skill.name}（${res.files} 个文件）`);
      } catch (err2) {
        this.note(err2.message);
      }
    }

    this.fileEl.value = '';
    await this.refresh();
    this.onChanged();
  }
}

// 分块做 base64：String.fromCharCode(...bytes) 一把梭，几 MB 的包会爆调用栈
function toBase64(buf) {
  const bytes = new Uint8Array(buf);
  const chunk = 8192;
  let binary = '';
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk));
  }
  return btoa(binary);
}
