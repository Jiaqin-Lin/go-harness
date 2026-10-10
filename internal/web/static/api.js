// 跟服务端说话的两种方式：普通 JSON 请求、和一条读到底的 SSE 流

async function failure(res) {
  // 服务端的错误也是 JSON：{"error": "..."}。解析不出来就退回状态码
  try {
    const body = await res.json();
    if (body && body.error) return new Error(body.error);
  } catch {
    /* 不是 JSON，走下面的兜底 */
  }
  return new Error(`${res.status} ${res.statusText}`);
}

async function unwrap(res) {
  if (!res.ok) throw await failure(res);
  return res.json();
}

export function getJSON(url) {
  return fetch(url).then(unwrap);
}

export function request(method, url, body) {
  const init = { method };
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' };
    init.body = JSON.stringify(body);
  }
  return fetch(url, init).then(unwrap);
}

// streamChat 边收边发事件。EventSource 只能 GET，而发消息是 POST，
// 所以手动读 fetch 的流：每帧是 "data: {...}\n\n"，按空行切
export async function streamChat(body, onEvent, signal) {
  const res = await fetch('/api/chat', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal,
  });
  if (!res.ok) throw await failure(res);

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';

  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });

      let cut;
      while ((cut = buffer.indexOf('\n\n')) !== -1) {
        const frame = buffer.slice(0, cut);
        buffer = buffer.slice(cut + 2);
        const line = frame.split('\n').find((l) => l.startsWith('data: '));
        if (!line) continue;
        // 一帧没处理成功不该把整条流带走：剩下的帧还得处理，
        // 不然这一轮的结果就看不到了（服务端那边照样在跑）
        try {
          onEvent(JSON.parse(line.slice(6)));
        } catch (err) {
          console.error('这一帧没处理成功', err, line);
        }
      }
    }
  } finally {
    // 提前退出时把连接关掉。不关的话服务端那一轮还在跑，
    // 重发会撞 409，而「停止」按钮这时已经没了
    reader.cancel().catch(() => {});
  }
}
