const phases = { segment: '分段', reverse: '反推', review: '复核' };
const number = (value, digits = 1) => typeof value === 'number' && Number.isFinite(value) ? value.toFixed(digits) : '—';

export function formatTokenSpeed(data, timestamp = Date.now()) {
  const rows = data.requests || [];
  const active = rows.findLast(row => row.kind !== 'end');
  const finished = rows.filter(row => row.kind === 'end');
  const last = finished.at(-1);
  const running = data.status === 'running';
  const extra = running ? Math.max(0, timestamp / 1000 - data.updated_at) : 0;
  const title = running ? '运行中' : data.status === 'completed' ? '已完成' : data.status === 'cancelled' ? '已取消' : '已停止 / 失败';
  const lines = [`Token速度 · ${title} · ${data.model || ''}`];
  lines.push(`请求平均 ${number(data.average_tps, 2)} tok/s · 有效 ${number(data.effective_tps, 2)} tok/s`);
  lines.push(`已知输出 ${data.output_tokens ?? '—'} token · 总用时 ${number(data.task_seconds + extra)}s · 失败请求 ${data.failed_requests || 0}`);
  if (active) {
    const elapsed = active.elapsed_seconds + extra;
    lines.push(`${phases[active.phase] || '请求'} · ${number(elapsed)}s · ${active.received_characters || 0} 字符`);
    lines.push(active.first_token_seconds == null ? '等待首内容，速度待接口返回用量后计算' : `首内容等待 ${number(active.first_token_seconds)}s · 正在接收，tok/s结算中`);
  } else if (running) lines.push('等待模型请求 / 工具处理 / 重试');
  if (last) {
    lines.push(`最近请求：${phases[last.phase] || '模型'} · ${number(last.average_tps, 2)} tok/s · ${last.output_tokens ?? '—'} token / ${number(last.elapsed_seconds)}s`);
    if (last.output_tokens == null) lines.push('该请求未返回token用量，无法计算速度');
  }
  lines.push('请求平均含首token等待；有效速度含预处理、工具与重试。');
  if (!data.usage_complete && finished.length) lines.push('部分请求用量缺失：已知输出与请求平均仅统计有用量的请求。');
  if (finished.length) {
    lines.push('每次请求（含工具续轮）：');
    finished.forEach((row, index) => lines.push(`${index + 1}. ${phases[row.phase] || '模型'} ${number(row.average_tps, 2)} tok/s · ${row.output_tokens ?? '—'} token · ${number(row.elapsed_seconds)}s · 首内容 ${number(row.first_token_seconds)}s${row.failure_code ? ` · ${row.failure_code}` : ''}`));
  }
  return lines.join('\n');
}
