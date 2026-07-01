// Develop tab module: human-readable view of active development work.
function developJobs() {
  return Object.values(state.jobs || {}).sort((a, b) => (String(b.updatedAt || '')).localeCompare(String(a.updatedAt || '')));
}

function developCurrentJob() {
  const jobs = developJobs();
  return jobs.find((j) => !['done', 'idle'].includes(String(j.status || '').toLowerCase())) || jobs[0] || null;
}

function developInferPhase(job) {
  if (!job) return 'Waiting User';
  const text = String((job.preview || '') + ' ' + (job.route || '') + ' ' + (job.status || '')).toLowerCase();
  if (/failed|error|失敗|エラー/.test(text)) return 'Failed';
  if (/done|passed|complete|完了/.test(text)) return 'Done';
  if (/test|go test|e2e|verify|検証/.test(text)) return 'Testing';
  if (/patch|diff|file|apply|実装|変更|更新/.test(text)) return 'Implementing';
  if (/plan|proposal|route|設計|仕様/.test(text)) return 'Planning';
  if (/report|summary|報告/.test(text)) return 'Reporting';
  return String(job.status || '').toLowerCase() === 'running' ? 'Implementing' : 'Waiting User';
}

function developRelatedEvidence(jobID) {
  return (state.evidence || []).find((r) => String(r.job_id || '') === String(jobID || '')) || null;
}

function developRelatedVerification(jobID) {
  return (state.verificationReports || []).find((r) => String(r.job_id || '') === String(jobID || '')) || null;
}

function developRecentLogs(jobID) {
  const jid = String(jobID || '');
  return (state.logs || []).filter((ev) => !jid || String(ev.job_id || '') === jid).slice(-8);
}

function renderDevelopDesk() {
  const taskEl = document.getElementById('developCurrentTask');
  const phaseEl = document.getElementById('developAgentPhase');
  const logsEl = document.getElementById('developLogSummary');
  const artifactsEl = document.getElementById('developArtifacts');
  const actionsEl = document.getElementById('developNextActions');
  if (!taskEl || !phaseEl || !logsEl || !artifactsEl || !actionsEl) return;

  const job = developCurrentJob();
  const evidence = developRelatedEvidence(job && job.id);
  const verification = developRelatedVerification(job && job.id);
  const phase = developInferPhase(job);
  const logs = developRecentLogs(job && job.id);
  const agent = job ? (job.to || job.from || '-') : '-';

  taskEl.innerHTML = '<h3>Current Task</h3>' + (job ? (
    '<div class="desk-code">' + esc(job.id || '-') + '</div>' +
    '<div class="daily-desk-body">' + esc(short(job.preview || evidence && evidence.goal || '-', 180)) + '</div>' +
    '<div class="desk-row"><span>Route</span><span>' + esc(job.route || evidence && evidence.route || '-') + '</span></div>' +
    '<div class="desk-row"><span>Started</span><span>' + esc(fdt(job.startedAt || evidence && evidence.created_at)) + '</span></div>' +
    '<div class="desk-row"><span>Updated</span><span>' + esc(fdt(job.updatedAt || evidence && evidence.finished_at)) + '</span></div>'
  ) : '<div class="daily-desk-muted">現在の開発タスクはありません。</div>');

  phaseEl.innerHTML =
    '<h3>Agent / Phase</h3>' +
    '<div class="desk-row"><span>Owner</span><span>' + esc(agName(agent)) + '</span></div>' +
    '<div class="desk-row"><span>Phase</span><span class="desk-pill">' + esc(phase) + '</span></div>' +
    '<div class="desk-row"><span>Status</span><span>' + esc(job ? job.status || '-' : '-') + '</span></div>' +
    '<div class="desk-row"><span>Retry / Error</span><span>' + esc(evidence ? String(evidence.repair_count || 0) + ' / ' + (evidence.error_kind || '-') : '-') + '</span></div>';

  logsEl.innerHTML = '<h3>作業ログ要約</h3>' + (logs.length ? logs.map((ev) => (
    '<div class="desk-item"><span class="daily-desk-muted">' + esc(fdt(ev.timestamp)) + ' · ' + esc(ev.type || '-') + '</span><div>' + esc(short(ev.content || '-', 160)) + '</div></div>'
  )).join('') : '<div class="daily-desk-muted">関連ログはまだありません。</div>');

  const steps = evidence && Array.isArray(evidence.steps) ? evidence.steps : [];
  const verifications = evidence && Array.isArray(evidence.verification) ? evidence.verification : [];
  artifactsEl.innerHTML =
    '<h3>差分・成果物</h3>' +
    (steps.length ? steps.slice(-6).map((s) => '<div class="desk-item">' + esc(short(s, 150)) + '</div>').join('') : '<div class="daily-desk-muted">差分・成果物は Evidence にまだありません。</div>') +
    (verification ? '<div class="desk-item"><span class="desk-pill">CoVe</span> ' + esc(verification.status || '-') + ' claims=' + esc(String(verification.claim_count || 0)) + '</div>' : '') +
    (verifications.length ? '<div class="desk-item">' + esc(short(verifications[verifications.length - 1], 160)) + '</div>' : '');

  actionsEl.innerHTML =
    '<h3>確認待ち / 次のボタン</h3>' +
    '<div class="desk-action-row">' +
      '<button class="ctl-btn" onclick="deskCreateInstructionFromDevelop()">続ける</button>' +
      '<button class="ctl-btn" onclick="deskCreateInstructionFromDevelop(\'止める\')">止める</button>' +
      '<button class="ctl-btn" onclick="deskCreateInstructionFromDevelop(\'再試行\')">再試行</button>' +
      '<button class="ctl-btn" onclick="deskCreateInstructionFromDevelop(\'前提を見直す\')">前提を見直す</button>' +
      '<button class="ctl-btn" onclick="switchTab(\'reports\')">Reportsで読む</button>' +
      '<button class="ctl-btn" onclick="switchTab(\'timeline\')">Chatで相談する</button>' +
    '</div>' +
    '<div class="daily-desk-muted" style="margin-top:10px">破壊的操作はここから直接実行しません。必要な判断は Instructions または Chat に渡します。</div>';
}

function deskCreateInstructionFromDevelop(prefix) {
  const job = developCurrentJob();
  const items = deskInstructions();
  const now = new Date().toISOString();
  items.unshift({
    instruction_id: 'inst_' + now.replace(/[-:.TZ]/g, '').slice(0, 14),
    source: 'develop',
    text: (prefix || '続ける') + (job ? ': ' + (job.preview || job.route || job.id) : ''),
    status: 'open',
    priority: 'normal',
    target_agent: 'Worker',
    created_at: now,
    updated_at: now,
    timing_hint: 'next',
    job_ids: job && job.id ? [job.id] : [],
    route: job && job.route ? job.route : '',
    last_summary: job && job.preview ? job.preview : '',
    blocked_reason: null,
    cancel_reason: null,
  });
  deskSaveInstructions(items);
  renderInstructionsDesk();
  renderHomeDesk();
  switchTab('instructions');
}

function bindDevelopDeskControls() {
  const btn = document.getElementById('developRefreshBtn');
  if (!btn || btn.dataset.bound === '1') return;
  btn.dataset.bound = '1';
  btn.addEventListener('click', renderDevelopDesk);
}
