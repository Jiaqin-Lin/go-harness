// 输入框里的 @ 文件补全：光标停在一个 @ 词里的时候，拿项目里的路径给候选。
//
// 这里只管「列出什么、选中什么、插什么」——菜单什么时候开、什么时候关，
// 由 app.js 在输入事件里驱动（见 dom.input 的 input / keydown）

import { getJSON } from './api.js';

// atToken 判光标是不是正停在一个 @ 词里，是就返回 @ 的位置、词的结尾和词本身。
// 词的边界是空白：@ 后面敲了空格就说明这个路径写完了。@ 前面也得是行首或空白，
// 不然 foo@bar.com 这种也会被当成在补全
export function atToken(input) {
  const before = input.value.slice(0, input.selectionStart);
  const at = before.lastIndexOf('@');
  if (at < 0) return null;
  const query = before.slice(at + 1);
  if (/\s/.test(query)) return null;
  if (at > 0 && !/\s/.test(before[at - 1])) return null;
  return { start: at, end: input.selectionStart, query };
}

export class FileMenu {
  // cwd()：当前项目（路径是相对它算的）
  // onShow()：菜单露出来了 —— 技能菜单在同一个位置，得让个位
  // onChange()：往输入框里插了字，发送按钮、自动长高得跟着重算
  constructor({ root, input, cwd, onShow, onChange }) {
    this.root = root;
    this.input = input;
    this.cwdOf = cwd;
    this.onShow = onShow;
    this.onChange = onChange;
    this.token = null; // 插字要用的：这次补全替换的是输入框的哪一段
    this.items = [];
    this.active = 0;
    this.seq = 0; // 打字快过请求时，只认最后一笔的结果

    // 点菜单别把输入框的焦点拿走，不然点完一下还得再点回输入框
    root.addEventListener('mousedown', (event) => event.preventDefault());
    document.addEventListener('click', (event) => {
      if (!this.root.hidden && !this.root.contains(event.target)) this.close();
    });
    input.addEventListener('blur', () => this.close());
  }

  // update 每次输入都叫一遍：光标还在 @ 词里就按当前的词重列候选，
  // 不在（写了空格、删了 @）就收起来
  async update() {
    const token = atToken(this.input);
    const cwd = this.cwdOf();
    if (!token || !cwd) {
      this.close();
      return;
    }

    this.token = token;
    const seq = ++this.seq;
    let files;
    try {
      ({ files = [] } = await getJSON(
        `/api/files?cwd=${encodeURIComponent(cwd)}&q=${encodeURIComponent(token.query)}`,
      ));
    } catch {
      this.close(); // 目录读不到之类：补全给不出候选而已，输入框照用
      return;
    }
    if (seq !== this.seq) return; // 这份回来晚了，输入已经往前走

    this.items = files;
    this.active = 0;
    if (files.length) this.render();
    else this.close();
  }

  // handleKey 吃掉菜单开着时的方向键、回车、Tab、Esc，返回 true = 这次按键归菜单。
  // 回车走这里就等于「插入路径」而不是「把消息发出去」
  handleKey(event) {
    if (this.root.hidden || !this.items.length) return false;
    switch (event.key) {
      case 'ArrowDown':
        this.move(1);
        return true;
      case 'ArrowUp':
        this.move(-1);
        return true;
      case 'Enter':
      case 'Tab':
        this.pick(this.items[this.active]);
        return true;
      case 'Escape':
        this.close();
        return true;
      default:
        return false;
    }
  }

  move(delta) {
    this.active = (this.active + delta + this.items.length) % this.items.length;
    this.highlight();
  }

  // pick 把 @<词> 整段换成 @<路径>。目录补一个斜杠 —— 接着往下打就是它的子路径，
  // 下一次输入菜单会按新词重新列
  pick(file) {
    if (!file || !this.token) return;
    const text = '@' + file.path + (file.isDir ? '/' : '');
    this.input.setRangeText(text, this.token.start, this.token.end, 'end');
    this.close();
    this.onChange?.();
  }

  close() {
    this.root.hidden = true;
    this.items = [];
    this.token = null;
    this.seq++; // 让还在路上的那个响应作废
  }

  render() {
    this.root.hidden = false;
    const rows = this.items.map((file, index) => {
      const row = document.createElement('button');
      row.type = 'button';
      row.className = 'file-menu-item';
      row.append(...fileLabel(file));
      row.addEventListener('click', () => this.pick(file));
      row.addEventListener('mouseenter', () => {
        this.active = index;
        this.highlight();
      });
      return row;
    });

    // 提示就挂在菜单尾巴上：用到 @ 的时候它正好在眼前
    const hint = document.createElement('div');
    hint.className = 'file-menu-hint';
    hint.textContent = '↑↓ 选 · Enter / Tab 插入路径 · Esc 关掉';

    this.root.replaceChildren(...rows, hint);
    this.highlight();
    this.onShow?.();
  }

  highlight() {
    const rows = this.root.querySelectorAll('.file-menu-item');
    rows.forEach((row, index) => row.classList.toggle('is-active', index === this.active));
    rows[this.active]?.scrollIntoView({ block: 'nearest' });
  }
}

// fileLabel 一行的两段：目录压暗、名字留着。同名的文件基本靠目录区分，
// 目录反而是最该先看到的那半截
function fileLabel(file) {
  const cut = file.path.lastIndexOf('/');
  const nodes = [];
  if (cut >= 0) {
    const dir = document.createElement('span');
    dir.className = 'file-path';
    dir.textContent = file.path.slice(0, cut + 1);
    nodes.push(dir);
  }
  const name = document.createElement('span');
  name.className = 'file-name';
  name.textContent = file.path.slice(cut + 1) + (file.isDir ? '/' : '');
  nodes.push(name);
  return nodes;
}
