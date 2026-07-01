// Backlog tab module: server-backed idea / unimplemented item board.
let backlogItems = [];
let backlogFetchError = '';

const BACKLOG_STATUSES = ['open', 'implementing', 'testing', 'fixing', 'blocked', 'ok', 'rejected'];

function backlogEl(id) {
  return document.getElementById(id);
}

function normalizeBacklogItem(item) {
  return {
    item_id: String(item.item_id || ''),
    kind: String(item.kind || 'idea'),
    title: String(item.title || 'untitled'),
    body: String(item.body || ''),
    source: String(item.source || 'ren'),
    owner: String(item.owner || ''),
    status: BACKLOG_STATUSES.includes(item.status) ? item.status : 'open',
    priority: String(item.priority || 'normal'),
    tags: Array.isArray(item.tags) ? item.tags : [],
    implementer: String(item.implementer || ''),
    implementation: String(item.implementation || ''),
    test_result: String(item.test_result || ''),
    check_ok: Boolean(item.check_ok),
    checked_by: String(item.checked_by || ''),
    created_at: String(item.created_at || ''),
    updated_at: String(item.updated_at || ''),
  };
}

async function refreshBacklog() {
  try {
    const res = await fetch('/viewer/backlog?limit=300', {cache: 'no-store'});
    if (!res.ok) throw new Error('HTTP ' + String(res.status));
    const payload = await res.json();
    backlogItems = Array.isArray(payload.items) ? payload.items.map(normalizeBacklogItem) : [];
    backlogFetchError = '';
  } catch (err) {
    backlogFetchError = String(err && err.message ? err.message : err);
  }
  renderBacklogDesk();
}

function renderBacklogDesk() {
  const body = backlogEl('backlogBodyTable');
  if (!body) return;
  if (backlogFetchError) {
    body.innerHTML = '<tr><td colspan="8" class="small">Backlog unavailable: ' + esc(backlogFetchError) + '</td></tr>';
    return;
  }
  if (!backlogItems.length) {
    body.innerHTML = '<tr><td colspan="8" class="small">まだ項目がありません</td></tr>';
    return;
  }
  body.innerHTML = backlogItems.map(renderBacklogRow).join('');
  body.querySelectorAll('[data-backlog-action]').forEach((btn) => {
    btn.addEventListener('click', () => handleBacklogAction(btn));
  });
}

function renderBacklogRow(item) {
  const statusClass = item.check_ok || item.status === 'ok' ? 'state-idle' : (item.status === 'blocked' ? 'state-error' : 'state-running');
  return '<tr data-backlog-id="' + escAttr(item.item_id) + '">' +
    '<td><span class="desk-pill">' + esc(item.kind) + '</span><div class="small">' + esc(item.priority) + '</div></td>' +
    '<td><span class="badge ' + statusClass + '">' + esc(item.status) + '</span></td>' +
    '<td><strong>' + esc(item.title) + '</strong><div class="small">' + esc(short(item.body || '-', 120)) + '</div><div class="desk-code">' + esc(item.item_id) + '</div></td>' +
    '<td>' + esc(item.source || '-') + '</td>' +
    '<td>' + esc(item.owner || item.implementer || '-') + '</td>' +
    '<td>' + esc(short(item.test_result || '-', 90)) + '</td>' +
    '<td>' + (item.check_ok ? esc(item.checked_by || 'OK') : '-') + '</td>' +
    '<td><div class="desk-action-row backlog-actions">' +
      renderBacklogAction(item, 'implementing', '実装') +
      renderBacklogAction(item, 'testing', 'テスト') +
      renderBacklogAction(item, 'fixing', '修正') +
      renderBacklogAction(item, 'ok', 'OK') +
      renderBacklogAction(item, 'blocked', '保留') +
    '</div></td>' +
  '</tr>';
}

function renderBacklogAction(item, status, label) {
  return '<button class="ctl-btn" type="button" data-backlog-action="' + escAttr(status) + '" data-backlog-id="' + escAttr(item.item_id) + '">' + esc(label) + '</button>';
}

async function handleBacklogAction(btn) {
  const id = btn.dataset.backlogId || '';
  const status = btn.dataset.backlogAction || '';
  const item = backlogItems.find((it) => it.item_id === id);
  if (!item) return;
  const next = Object.assign({}, item, {
    status,
    check_ok: status === 'ok',
    checked_by: status === 'ok' ? 'ren' : item.checked_by,
  });
  if (status === 'testing') {
    const result = window.prompt('テスト結果', item.test_result || '');
    if (result !== null) next.test_result = result;
  }
  if (status === 'implementing' && !next.owner) next.owner = 'shiro';
  await saveBacklogItem(next);
}

async function addBacklogItem() {
  const titleEl = backlogEl('backlogTitle');
  const bodyEl = backlogEl('backlogBody');
  const kindEl = backlogEl('backlogKind');
  const sourceEl = backlogEl('backlogSource');
  const priorityEl = backlogEl('backlogPriority');
  const title = titleEl ? String(titleEl.value || '').trim() : '';
  if (!title) {
    showToast('タイトルを入れてください', 'error');
    return;
  }
  await saveBacklogItem({
    kind: kindEl ? kindEl.value : 'idea',
    title,
    body: bodyEl ? bodyEl.value : '',
    source: sourceEl ? sourceEl.value : 'ren',
    priority: priorityEl ? priorityEl.value : 'normal',
    status: 'open',
  });
  if (titleEl) titleEl.value = '';
  if (bodyEl) bodyEl.value = '';
}

async function saveBacklogItem(item) {
  const res = await fetch('/viewer/backlog', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(item),
  });
  if (!res.ok) {
    const text = await res.text();
    showToast('Backlog save failed: ' + (text || String(res.status)), 'error');
    return;
  }
  const payload = await res.json();
  const saved = normalizeBacklogItem(payload.item || {});
  backlogItems = backlogItems.filter((it) => it.item_id !== saved.item_id);
  backlogItems.unshift(saved);
  renderBacklogDesk();
  showToast('Backlog updated', 'success');
}

function bindBacklogDeskControls() {
  const add = backlogEl('backlogAddBtn');
  const refresh = backlogEl('backlogRefreshBtn');
  const title = backlogEl('backlogTitle');
  if (add && add.dataset.bound !== '1') {
    add.dataset.bound = '1';
    add.addEventListener('click', addBacklogItem);
  }
  if (refresh && refresh.dataset.bound !== '1') {
    refresh.dataset.bound = '1';
    refresh.addEventListener('click', refreshBacklog);
  }
  if (title && title.dataset.bound !== '1') {
    title.dataset.bound = '1';
    title.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') addBacklogItem();
    });
  }
}

bindBacklogDeskControls();
