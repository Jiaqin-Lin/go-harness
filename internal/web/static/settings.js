// 设置弹窗。目前就一个旋钮：压缩触发点
//
// 值存在服务端的 ~/.go-harness/settings.json 里，服务端每一轮现读 ——
// 所以保存之后不用重启、不用重开会话，下一条消息就用新值

import { request } from './api.js';

export class SettingsModal {
  // onSaved(meta)：存完把新的 meta 交出去，上层拿它刷新显示
  constructor({ root, onSaved }) {
    this.root = root;
    this.onSaved = onSaved;
    this.inputEl = root.querySelector('#setting-compact-at');
    this.noteEl = root.querySelector('#settings-note');

    root.querySelector('#settings-close').addEventListener('click', () => this.close());
    root.addEventListener('click', (e) => {
      if (e.target === root) this.close(); // 点遮罩关掉
    });
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && !this.root.hidden) this.close();
    });
    root.querySelector('#settings-save').addEventListener('click', () => this.save());
    // 输入框里回车 = 保存（这是个只有一个输入框的表单）
    this.inputEl.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        this.save();
      }
    });
  }

  // open(meta)：拿当前生效的值把输入框填上
  open(meta) {
    this.root.hidden = false;
    this.inputEl.value = meta?.compactAt || '';
    this.note('');
    this.inputEl.focus();
  }

  close() {
    this.root.hidden = true;
  }

  note(text) {
    this.noteEl.textContent = text;
  }

  async save() {
    const raw = this.inputEl.value.trim();
    const at = raw === '' ? 0 : Number(raw);
    if (!Number.isInteger(at) || at < 0) {
      this.note('填一个非负整数（留空 = 用默认）');
      return;
    }

    try {
      const meta = await request('POST', '/api/settings', { compactAt: at });
      this.onSaved(meta);
      this.close();
    } catch (err) {
      this.note(err.message);
    }
  }
}
