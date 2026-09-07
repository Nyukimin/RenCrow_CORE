// Instructions tab module: localStorage-backed MVP instruction queue.
const DESK_INSTRUCTION_STATUSES = ['open', 'running', 'blocked', 'done', 'cancelled'];

function deskNewInstructionID() {
  const stamp = new Date().toISOString().replace(/[-:.TZ]/g, '').slice(0, 14);
  const suffix = String(Math.floor(Math.random() * 10000)).padStart(4, '0');
  return 'inst_' + stamp + '_' + suffix;
}

function deskNormalizeInstruction(item) {
  const now = new Date().toISOString();
  return {
    instruction_id: item.instruction_id || deskNewInstructionID(),
    source: item.source || 'manual',
    text: String(item.text || '').trim(),
    status: DESK_INSTRUCTION_STATUSES.includes(item.status) ? item.status : 'open',
    priority: item.priority || 'normal',
    target_agent: item.target_agent || 'Chat',
    created_at: item.created_at || now,
    updated_at: item.updated_at || now,
    due_hint: item.due_hint || null,
    timing_hint: item.timing_hint || 'today',
    task_ids: Array.isArray(item.task_ids) ? item.task_ids : [],
    route: item.route || '',
    last_summary: item.last_summary || '',
    blocked_reason: item.blocked_reason || null,
    cancel_reason: item.cancel_reason || null,
  };
}

// Recovery-only snapshot: never merged into the current instruction queue.
let deskArchivedInstructions = [];

function deskInstructionArchiveView() {
  deskArchivedInstructions = [];
  let items;
  try {
    const raw = localStorage.getItem('rencrow.viewer.instructions.v1');
    if (!raw) return '';
    items = JSON.parse(raw);
    if (!Array.isArray(items)) throw new Error('invalid archive');
  } catch (_) {
    return '<p role="status">旧形式の下書きを読み取れません。保存済みデータは変更していません。</p>';
  }
  if (!items.length) return '';
  deskArchivedInstructions = items;
  return '<section style="grid-column:1/-1"><details><summary>旧形式の下書き（' + esc(String(items.length)) + '件・原本保持）</summary>' +
    '<p>本文を確認し、必要なら空の編集欄へ戻せます。旧ID・実行状態は引き継ぎません。内容を見直して通常の追加操作を行ってください。</p>' +
    items.map((item, index) => {
      const valid = item && typeof item === 'object' && typeof item.text === 'string';
      return '<article class="instruction-card"><pre style="white-space:pre-wrap;overflow-wrap:anywhere">' +
        esc(valid ? item.text : '本文形式を読み取れません。') + '</pre>' +
        '<details><summary>保存時の記録</summary><pre style="white-space:pre-wrap;overflow-wrap:anywhere">' + esc(JSON.stringify(item, null, 2)) + '</pre></details>' +
        (valid ? '<button class="ctl-btn" onclick="deskRecoverInstructionText(' + index + ')">本文を編集欄へ戻す</button>' : '') + '</article>';
    }).join('') + '</details><p id="instructionRecoveryResult" role="status"></p></section>';
}

function deskRecoverInstructionText(index) {
  const result = document.getElementById('instructionRecoveryResult');
  const input = document.getElementById('instructionText');
  const item = Number.isInteger(index) && index >= 0 ? deskArchivedInstructions[index] : null;
  if (!result || !input || !item || typeof item.text !== 'string') return;
  if (input.value) {
    result.textContent = '編集中の本文があります。保存または整理してから復元してください。';
    return;
  }
  input.value = item.text;
  input.focus();
  result.textContent = '本文を編集欄へ戻しました。まだ保存・実行していません。旧IDと実行状態は引き継いでいません。';
}

function renderInstructionsDesk() {
  const root = document.getElementById('instructionColumns');
  if (!root) return;
  const items = deskInstructions().map(deskNormalizeInstruction);
  const notice = deskInstructionArchiveView();
  root.innerHTML = notice + DESK_INSTRUCTION_STATUSES.map((status) => {
    const list = items.filter((it) => it.status === status);
    return '<section class="instruction-column">' +
      '<h3>' + esc(status) + ' <span class="daily-desk-muted">' + esc(String(list.length)) + '</span></h3>' +
      (list.length ? list.map(renderInstructionCard).join('') : '<div class="daily-desk-muted">なし</div>') +
    '</section>';
  }).join('');
}

function renderInstructionCard(item) {
  const tasks = item.task_ids && item.task_ids.length ? item.task_ids.join(', ') : '-';
  return '<article class="instruction-card ' + esc(item.status) + '" data-instruction-id="' + escAttr(item.instruction_id) + '">' +
    '<div class="daily-desk-body">' + esc(short(item.text || '-', 120)) + '</div>' +
    '<div class="desk-row"><span>priority</span><span class="desk-pill">' + esc(item.priority || '-') + '</span></div>' +
    '<div class="desk-row"><span>agent</span><span>' + esc(item.target_agent || '-') + '</span></div>' +
    '<div class="desk-code">' + esc(tasks) + '</div>' +
    '<div class="daily-desk-muted">' + esc(fdt(item.updated_at)) + '</div>' +
    '<div class="desk-action-row">' +
      '<button class="ctl-btn" onclick="deskInstructionStatus(\'' + escAttr(item.instruction_id) + '\', \'running\', event)">running</button>' +
      '<button class="ctl-btn" onclick="deskInstructionStatus(\'' + escAttr(item.instruction_id) + '\', \'done\', event)">done</button>' +
      '<button class="ctl-btn" onclick="deskInstructionStatus(\'' + escAttr(item.instruction_id) + '\', \'cancelled\', event)">cancel</button>' +
      '<button class="ctl-btn" onclick="deskInstructionToChat(\'' + escAttr(item.instruction_id) + '\', event)">Chat</button>' +
    '</div>' +
  '</article>';
}

function deskInstructionStatus(id, status, event) {
  if (event && event.stopPropagation) event.stopPropagation();
  const now = new Date().toISOString();
  const items = deskInstructions().map(deskNormalizeInstruction).map((it) => {
    if (it.instruction_id !== id) return it;
    it.status = status;
    it.updated_at = now;
    if (status === 'cancelled') it.cancel_reason = 'viewer action';
    return it;
  });
  deskSaveInstructions(items);
  renderInstructionsDesk();
  renderHomeDesk();
}

function deskInstructionToChat(id, event) {
  if (event && event.stopPropagation) event.stopPropagation();
  const item = deskInstructions().map(deskNormalizeInstruction).find((it) => it.instruction_id === id);
  if (!item) return;
  const input = document.getElementById('inp');
  if (input) {
    input.value = item.text;
    input.dispatchEvent(new Event('input'));
  }
  switchTab('timeline');
}

function deskAddInstruction() {
  const textEl = document.getElementById('instructionText');
  const targetEl = document.getElementById('instructionTarget');
  const priorityEl = document.getElementById('instructionPriority');
  const text = textEl ? String(textEl.value || '').trim() : '';
  if (!text) return;
  const now = new Date().toISOString();
  const items = deskInstructions().map(deskNormalizeInstruction);
  items.unshift(deskNormalizeInstruction({
    instruction_id: deskNewInstructionID(),
    source: 'manual',
    text,
    status: 'open',
    priority: priorityEl ? priorityEl.value : 'normal',
    target_agent: targetEl ? targetEl.value : 'Chat',
    created_at: now,
    updated_at: now,
    timing_hint: 'today',
  }));
  deskSaveInstructions(items);
  if (textEl) textEl.value = '';
  renderInstructionsDesk();
  renderHomeDesk();
}

function deskClearDoneInstructions() {
  const items = deskInstructions().map(deskNormalizeInstruction).filter((it) => !['done', 'cancelled'].includes(it.status));
  deskSaveInstructions(items);
  renderInstructionsDesk();
  renderHomeDesk();
}

function bindInstructionsDeskControls() {
  const add = document.getElementById('instructionAddBtn');
  const clear = document.getElementById('instructionClearDoneBtn');
  const text = document.getElementById('instructionText');
  if (add && add.dataset.bound !== '1') {
    add.dataset.bound = '1';
    add.addEventListener('click', deskAddInstruction);
  }
  if (clear && clear.dataset.bound !== '1') {
    clear.dataset.bound = '1';
    clear.addEventListener('click', deskClearDoneInstructions);
  }
  if (text && text.dataset.bound !== '1') {
    text.dataset.bound = '1';
    text.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) deskAddInstruction();
    });
  }
}
