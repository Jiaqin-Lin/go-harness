// 目录选择器。只列目录 —— 选的是项目根，不是文件

import { getJSON } from './api.js';

export class DirPicker {
  // onPick(path)：选定之后交出去，关不关由上层决定
  constructor({ root, pathEl, listEl, onPick }) {
    this.root = root;
    this.pathEl = pathEl;
    this.listEl = listEl;
    this.onPick = onPick;
    this.current = '';
    this.parent = '';

    root.querySelector('#dirpicker-close').addEventListener('click', () => this.close());
    root.querySelector('#dirpicker-up').addEventListener('click', () => this.load(this.parent));
    root.querySelector('#dirpicker-pick').addEventListener('click', () => {
      if (this.current) this.onPick(this.current);
    });
    root.addEventListener('click', (e) => {
      if (e.target === root) this.close(); // 点遮罩关掉
    });
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && !this.root.hidden) this.close();
    });
  }

  async open(startPath = '') {
    this.root.hidden = false;
    await this.load(startPath);
  }

  close() {
    this.root.hidden = true;
  }

  async load(path) {
    let data;
    try {
      data = await getJSON(`/api/fs?path=${encodeURIComponent(path || '')}`);
    } catch (err) {
      this.listEl.replaceChildren(note(`读不了这个目录：${err.message}`));
      return;
    }

    this.current = data.path;
    this.parent = data.parent || '';
    this.pathEl.textContent = data.path;
    this.root.querySelector('#dirpicker-up').disabled = !this.parent;

    const rows = data.dirs.map((dir) => {
      const row = document.createElement('button');
      row.type = 'button';
      row.className = 'dir-row';
      row.textContent = dir.name;
      row.addEventListener('click', () => this.load(dir.path));
      return row;
    });
    this.listEl.replaceChildren(...(rows.length ? rows : [note('这个目录下面没有子目录')]));
  }
}

function note(text) {
  const el = document.createElement('p');
  el.className = 'dir-empty';
  el.textContent = text;
  return el;
}
