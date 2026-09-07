// Tasks tab module: task table rendering.
function renderTasks() {
  const body = document.getElementById('tasksBody');
  body.innerHTML = '';
  const list = Object.values(state.tasks).sort((a, b) => (b.updatedAt || '').localeCompare(a.updatedAt || ''));

  if (list.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="8" class="small">No task data yet</td>';
    body.appendChild(tr);
    return;
  }

  list.forEach((j) => {
    const st = j.status === 'error' ? 'error' : (j.status === 'done' ? 'idle' : 'running');
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td class="code">' + esc(j.id) + '</td>' +
      '<td>' + esc(j.route || '-') + '</td>' +
      '<td><span class="badge ' + stateClass(st) + '">' + esc(j.status) + '</span></td>' +
      '<td>' + esc(agName(j.from || '-') + ' -> ' + agName(j.to || '-')) + '</td>' +
      '<td>' + esc(fdt(j.startedAt)) + '</td>' +
      '<td>' + esc(fdt(j.updatedAt)) + '</td>' +
      '<td>' + esc(String(j.events || 0)) + '</td>' +
      '<td>' + esc(short(j.preview || '-', 90)) + '</td>';
    const graphCell = tr.lastElementChild;
    if (j.traceID && graphCell) {
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'ctl-btn';
      button.textContent = 'Graph';
      button.addEventListener('click', () => loadIdentityGraph(j.traceID));
      graphCell.appendChild(button);
    }
    body.appendChild(tr);
  });
}


let identityGraphRequestSequence = 0;
async function loadIdentityGraph(traceID) {
  const sequence = ++identityGraphRequestSequence;
  const status = document.getElementById('identityGraphStatus');
  const body = document.getElementById('identityGraphBody');
  if (!status || !body) return;
  body.innerHTML = '';
  status.textContent = 'Graphを読み込んでいます…';
  try {
    const response = await fetch('/viewer/identity-graph?trace_id=' + encodeURIComponent(traceID));
    if (!response.ok) throw new Error('HTTP ' + response.status);
    const graph = await response.json();
    if (graph.schema !== 'rencrow.identity-graph.v1' || graph.trace_id !== traceID) {
      throw new Error('GraphのTraceが一致しません');
    }
    const markup = identityGraphMarkup(graph);
    if (sequence !== identityGraphRequestSequence) return;
    body.innerHTML = markup;
    status.textContent = 'Trace: ' + traceID + ' / ' + graph.events.length + ' Events';
  } catch (error) {
    if (sequence !== identityGraphRequestSequence) return;
    status.textContent = 'Graphを取得できませんでした: ' + error.message;
  }
}

function identityGraphMarkup(graph) {
  const keys = ['events', 'event_edges', 'tasks', 'task_edges', 'messages', 'message_edges'];
  if (!keys.every((key) => Array.isArray(graph[key]))) throw new Error('Graph形式が不正です');
  const table = (title, columns, rows) => '<h4>' + esc(title) + '</h4><div class="table-wrap"><table><thead><tr>' +
    columns.map((column) => '<th>' + esc(column) + '</th>').join('') + '</tr></thead><tbody>' +
    (rows.length ? rows.map((row) => '<tr>' + row.map((value) => '<td class="code">' + esc(String(value ?? '')) + '</td>').join('') + '</tr>').join('') :
      '<tr><td colspan="' + columns.length + '" class="small">記録なし</td></tr>') + '</tbody></table></div>';
  return table('Event Graph', ['Seq', 'Event ID', 'Type', 'Task ID', 'Actor'], graph.events.map((event) =>
    [event.event_seq, event.event_id, event.event_type, event.task_id, event.actor_id ? event.actor_kind + ':' + event.actor_id : '未記録'])) +
    table('Event relationships', ['Source Event', 'Relation', 'Target Event'], graph.event_edges.map((edge) =>
      [edge.source_event_id, edge.kind, edge.target_event_id])) +
    table('Task Graph', ['Task ID', 'Status'], graph.tasks.map((task) => [task.task_id, task.status])) +
    table('Task relationships', ['Task', 'Relation', 'Referenced Task'], graph.task_edges.map((edge) =>
      [edge.source_task_id, edge.kind, edge.target_task_id])) +
    table('Communication Graph', ['Message ID'], graph.messages.map((message) => [message.message_id])) +
    table('Message evidence', ['Message ID', 'Event ID'], graph.message_edges.map((edge) => [edge.message_id, edge.event_id]));
}
