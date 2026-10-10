// 三个模块都要用的那点小工具。之前 graph.js / chat.js / app.js 各写了一份，
// 已经开始各漂各的了

const ROLE_LABEL = { user: '你', assistant: '助手', tool_result: '工具', error: '出错了' };

// roleLabel 是节点角上和消息头上那个小标签。工具结果直接显示工具自己的名字 ——
// 「bash」「read」比「工具」有信息量
export function roleLabel(msg) {
  if (msg.role === 'tool_result' && msg.toolName) return msg.toolName;
  return ROLE_LABEL[msg.role] || msg.role || '';
}

// summarise 取「这条消息在说啥」：第一块有内容的东西
export function summarise(msg) {
  if (!msg || !msg.blocks) return '';
  for (const block of msg.blocks) {
    if (block.kind === 'text' && block.text) return block.text;
    if (block.kind === 'tool') return [block.toolName, argsPreview(block.toolName, block.args)].filter(Boolean).join(' ');
    if (block.kind === 'image') return '图片';
  }
  return msg.errorMessage || '';
}

// argsPreview 把工具参数压成一行：有命令就给命令，其次给路径
export function argsPreview(toolName, raw) {
  if (!raw) return '';
  let args;
  try {
    args = JSON.parse(raw);
  } catch {
    return clip(raw, 120); // 参数还没流完，先给原文
  }
  if (typeof args.command === 'string') return clip(args.command, 120);
  if (typeof args.path === 'string') return clip(args.path, 120);
  return clip(JSON.stringify(args), 120);
}

// clip 压成一行短摘要：换行折成空格、超长截断
export function clip(text, limit) {
  const line = String(text ?? '').replace(/\s+/g, ' ').trim();
  return line.length > limit ? line.slice(0, limit) + '…' : line;
}
