// 确认弹窗：删东西之前问一句。
// 用页面自己的弹窗而不是原生 confirm —— 原生那个是浏览器长相，跟这块界面不是
// 一路，而且只有两行字，写不下「会一起删掉几个会话」这种话

export class ConfirmModal {
  constructor({ root }) {
    this.root = root;
    this.resolve = null;
    this.titleEl = root.querySelector('#confirm-title');
    this.textEl = root.querySelector('#confirm-text');
    this.okEl = root.querySelector('#confirm-ok');
    this.cancelEl = root.querySelector('#confirm-cancel');

    this.okEl.addEventListener('click', () => this.done(true));
    this.cancelEl.addEventListener('click', () => this.done(false));
    root.querySelector('#confirm-close').addEventListener('click', () => this.done(false));
    root.addEventListener('click', (e) => {
      if (e.target === root) this.done(false); // 点遮罩关掉
    });
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && !this.root.hidden) this.done(false);
    });
  }

  // ask 弹一次、等一个答复：点是 true，取消 / 关掉 / 按 Esc 都是 false
  ask({ title, body = '', ok = '删除' }) {
    this.titleEl.textContent = title;
    this.textEl.textContent = body;
    this.okEl.textContent = ok;
    this.root.hidden = false;
    // 焦点落在取消上：这一下回车按下去不该是删
    this.cancelEl.focus();
    return new Promise((resolve) => {
      this.resolve = resolve;
    });
  }

  done(ok) {
    if (this.root.hidden) return;
    this.root.hidden = true;
    const resolve = this.resolve;
    this.resolve = null;
    resolve?.(ok);
  }
}
