// Home / Daily Desk tab module.
const DESK_INSTRUCTION_KEY = 'rencrow.viewer.instructions.v1';
const DESK_REPORT_READ_KEY = 'rencrow.viewer.reportReads.v1';

function deskJSONLoad(key, fallback) {
  try {
    const raw = localStorage.getItem(key);
    if (!raw) return fallback;
    const parsed = JSON.parse(raw);
    return parsed == null ? fallback : parsed;
  } catch (_) {
    return fallback;
  }
}

function deskJSONSave(key, value) {
  try { localStorage.setItem(key, JSON.stringify(value)); } catch (_) {}
}

function deskInstructions() {
  const items = deskJSONLoad(DESK_INSTRUCTION_KEY, []);
  return Array.isArray(items) ? items : [];
}

function deskSaveInstructions(items) {
  deskJSONSave(DESK_INSTRUCTION_KEY, Array.isArray(items) ? items : []);
}

function deskReportReads() {
  const items = deskJSONLoad(DESK_REPORT_READ_KEY, {});
  return items && typeof items === 'object' && !Array.isArray(items) ? items : {};
}

function deskSaveReportReads(items) {
  deskJSONSave(DESK_REPORT_READ_KEY, items && typeof items === 'object' ? items : {});
}

function deskOpenInstructions() {
  return deskInstructions().filter((it) => !['done', 'cancelled'].includes(String(it.status || 'open')));
}

function deskAllReports() {
  const evidence = Array.isArray(state.evidence) ? state.evidence : [];
  const verification = Array.isArray(state.verificationReports) ? state.verificationReports : [];
  const byJob = {};
  evidence.forEach((item) => {
    const jobID = String(item.job_id || '');
    if (!jobID) return;
    byJob[jobID] = byJob[jobID] || {job_id: jobID};
    byJob[jobID].evidence = item;
  });
  verification.forEach((item) => {
    const jobID = String(item.job_id || '');
    if (!jobID) return;
    byJob[jobID] = byJob[jobID] || {job_id: jobID};
    byJob[jobID].verification = item;
  });
  return Object.values(byJob).map(deskBuildReportView).sort((a, b) => (Date.parse(b.created_at || 0) || 0) - (Date.parse(a.created_at || 0) || 0));
}

function deskBuildReportView(source) {
  const ev = source.evidence || {};
  const vr = source.verification || {};
  const jobID = source.job_id || ev.job_id || vr.job_id || '';
  const status = deskReportStatus(ev, vr);
  const title = ev.goal || vr.route || ('Job ' + jobID);
  const changed = Array.isArray(ev.steps) ? ev.steps.filter((s) => /file|diff|変更|生成|作成|追加|更新/i.test(String(s || ''))).slice(0, 6) : [];
  const verified = [];
  if (Array.isArray(ev.verification)) verified.push(...ev.verification.slice(-5));
  if (vr.status) verified.push('verification_status=' + vr.status + ' claims=' + String(vr.claim_count || 0));
  const failed = [];
  if (ev.status === 'failed') failed.push(ev.error || ev.error_kind || 'job failed');
  if (vr.status === 'unsupported' || vr.status === 'conflict') failed.push('verification ' + vr.status);
  const unconfirmed = [];
  if (!ev.job_id) unconfirmed.push('execution evidence is not linked');
  if (vr.status === 'not_checked') unconfirmed.push('verification not checked');
  return {
    report_id: 'rep_' + jobID,
    job_id: jobID,
    title,
    status,
    summary: deskReportSummary(ev, vr, title, status),
    changed,
    verified,
    failed,
    unconfirmed,
    evidence_refs: deskEvidenceRefs(ev, vr),
    artifacts: [],
    next_decision: deskNextDecisions(status, vr),
    created_at: ev.finished_at || ev.created_at || vr.created_at || '',
  };
}

function deskReportStatus(ev, vr) {
  if (ev.status === 'failed') return 'failed';
  if (vr.status === 'unsupported' || vr.status === 'conflict') return 'partial';
  if (ev.status === 'passed' || vr.status === 'verified' || vr.status === 'weakly_supported') return 'success';
  return 'unknown';
}

function deskReportSummary(ev, vr, title, status) {
  if (ev.goal) return ev.goal;
  if (vr.status) return title + ' / verification=' + vr.status;
  return status === 'unknown' ? '詳細確認が必要です。' : title;
}

function deskEvidenceRefs(ev, vr) {
  const refs = [];
  if (ev.job_id) refs.push({type: 'evidence', id: ev.job_id, path: '/viewer/evidence/detail?job_id=' + ev.job_id});
  if (vr.job_id) refs.push({type: 'verification', id: vr.job_id, path: '/viewer/verification/detail?job_id=' + vr.job_id});
  return refs;
}

function deskNextDecisions(status, vr) {
  if (status === 'failed') return ['失敗原因を確認する', '再試行するか前提を見直す'];
  if (status === 'partial') return ['unsupported / conflict を確認する', '追加検証するか判断する'];
  if (vr.status === 'not_checked') return ['検証が必要か判断する'];
  return ['次の指示を作る'];
}

function deskRunningJobs() {
  return Object.values(state.jobs || {}).filter((j) => !['done', 'idle'].includes(String(j.status || '').toLowerCase()));
}

function deskLatestConversation() {
  const user = [...(state.logs || [])].reverse().find((ev) => String(ev.from || '').toLowerCase() === 'user');
  const reply = [...(state.logs || [])].reverse().find((ev) => String(ev.to || '').toLowerCase() === 'user' && String(ev.content || '').trim());
  return {user, reply};
}

function deskAgentList() {
  return AGENTS.map((id) => ({id, item: state.agents[id] || {}}));
}

function deskStatusLabel() {
  const failed = (state.evidence || []).some((r) => r.status === 'failed') || (state.verificationReports || []).some((r) => r.status === 'unsupported' || r.status === 'conflict');
  if (failed) return {label: '要確認', cls: 'danger'};
  if (deskRunningJobs().length > 0) return {label: '作業中', cls: 'warn'};
  if (deskUnreadReports().length > 0) return {label: '報告あり', cls: 'warn'};
  return {label: '通常', cls: ''};
}

function deskUnreadReports() {
  const reads = deskReportReads();
  return deskAllReports().filter((r) => !reads[r.report_id]).slice(0, 5);
}

function renderHomeDesk() {
  const badge = document.getElementById('homeStateBadge');
  const statusCard = document.getElementById('homeStatusCard');
  const lastCard = document.getElementById('homeLastConversation');
  const runningCard = document.getElementById('homeRunningWork');
  const reportCard = document.getElementById('homeUnreadReports');
  const instructionCard = document.getElementById('homeOpenInstructions');
  const agentsCard = document.getElementById('homeAgents');
  if (!badge || !statusCard || !lastCard || !runningCard || !reportCard || !instructionCard || !agentsCard) return;

  const status = deskStatusLabel();
  badge.className = 'desk-status-pill ' + status.cls;
  badge.textContent = status.label;

  const running = deskRunningJobs();
  const reports = deskUnreadReports();
  const instructions = deskOpenInstructions();
  statusCard.innerHTML =
    '<h3>今日の状態</h3>' +
    '<div class="desk-row"><span>状態</span><span class="desk-pill ' + status.cls + '">' + esc(status.label) + '</span></div>' +
    '<div class="desk-row"><span>最終更新</span><span>' + esc(fdt(new Date().toISOString())) + '</span></div>' +
    '<div class="desk-row"><span>Active Agents</span><span>' + esc(String(deskAgentList().filter((a) => a.item.state && a.item.state !== 'offline').length)) + '</span></div>' +
    '<div class="desk-row"><span>進行中Job</span><span>' + esc(String(running.length)) + '</span></div>' +
    '<div class="desk-row"><span>未読Report</span><span>' + esc(String(reports.length)) + '</span></div>' +
    '<div class="desk-row"><span>未完了Instruction</span><span>' + esc(String(instructions.length)) + '</span></div>' +
    (state.homeSendError ? '<div class="desk-row"><span>送信失敗</span><span class="desk-pill danger">' + esc(state.homeSendError) + '</span></div>' : '');

  const latest = deskLatestConversation();
  lastCard.innerHTML =
    '<h3>最後の会話</h3>' +
    '<div class="daily-desk-body">' + esc(short(latest.user ? latest.user.content : 'まだ会話ログがありません', 120)) + '</div>' +
    '<div class="daily-desk-muted">' + esc(short(latest.reply ? latest.reply.content : '-', 140)) + '</div>' +
    '<div class="desk-action-row"><button class="ctl-btn" onclick="switchTab(\'timeline\')">会話を再開</button><span class="daily-desk-muted">' + esc(latest.reply ? fdt(latest.reply.timestamp) : '-') + '</span></div>';

  runningCard.innerHTML = '<h3>進行中の作業</h3>' + (running.length ? running.slice(0, 3).map((j) => (
    '<div class="desk-item"><div class="desk-code">' + esc(j.id || '-') + '</div><div>' + esc(short(j.preview || j.route || '-', 90)) + '</div><div class="desk-action-row"><span class="desk-pill">' + esc(j.status || '-') + '</span><button class="ctl-btn" onclick="switchTab(\'develop\')">Developで見る</button></div></div>'
  )).join('') : '<div class="daily-desk-muted">進行中の作業はありません。</div>');

  reportCard.innerHTML = '<h3>未読レポート</h3>' + (reports.length ? reports.slice(0, 3).map((r) => (
    '<div class="desk-item"><div>' + esc(short(r.title || '-', 90)) + '</div><div class="desk-code">' + esc(r.job_id || '-') + '</div><div class="desk-action-row"><span class="desk-pill">' + esc(r.status || '-') + '</span><button class="ctl-btn" onclick="switchTab(\'reports\')">Reportsで読む</button></div></div>'
  )).join('') : '<div class="daily-desk-muted">未読相当のレポートはありません。</div>');

  instructionCard.innerHTML = '<h3>未完了指示</h3>' + (instructions.length ? instructions.slice(0, 3).map((it) => (
    '<div class="desk-item"><div>' + esc(short(it.text || '-', 90)) + '</div><div class="desk-action-row"><span class="desk-pill">' + esc(it.status || 'open') + '</span><span class="daily-desk-muted">' + esc(it.target_agent || '-') + '</span><button class="ctl-btn" onclick="switchTab(\'instructions\')">Instructions</button></div></div>'
  )).join('') : '<div class="daily-desk-muted">未完了指示はありません。</div>');

  agentsCard.innerHTML = '<h3>呼び出せるキャラクター</h3>' + deskAgentList().map((a) => (
    '<div class="desk-row"><span>' + esc(agName(a.id)) + '</span><span class="desk-pill">' + esc(a.item.state || 'offline') + '</span></div>'
  )).join('');
}

function homeRoutePrefix(target) {
  const t = String(target || '').toLowerCase();
  if (t === 'worker') return '/ops ';
  if (t === 'coder') return '/code ';
  if (t === 'heavy') return '/analyze ';
  if (t === 'wild') return '/wild ';
  return '';
}

function bindHomeDeskControls() {
  const input = document.getElementById('homeInput');
  const send = document.getElementById('homeSendBtn');
  const target = document.getElementById('homeTargetAgent');
  if (!input || !send || send.dataset.bound === '1') return;
  send.dataset.bound = '1';
  const doSend = () => {
    const text = String(input.value || '').trim();
    if (!text) return;
    send.disabled = true;
    const prefix = homeRoutePrefix(target ? target.value : '');
    state.homeSendError = '';
    sendViewerMessage(prefix + text).then(() => {
      input.value = '';
      switchTab('timeline');
    }).catch((err) => {
      state.homeSendError = 'Home send unavailable: ' + String(err && err.message ? err.message : err);
      console.error(err);
      showToast('Home send failed', 'error');
    }).finally(() => {
      send.disabled = false;
      renderHomeDesk();
    });
  };
  send.addEventListener('click', doSend);
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      doSend();
    }
  });
}
