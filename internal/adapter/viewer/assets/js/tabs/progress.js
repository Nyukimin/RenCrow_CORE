// Progress tab module: job progress derivation and rendering.
function progressPhaseLabel(phase) {
  const p = String(phase || 'received');
  if (p === 'received') return 'received';
  if (p === 'routing') return 'routing';
  if (p === 'delegating') return 'delegating';
  if (p === 'chatting') return 'chatting';
  if (p === 'delegated_to_worker') return 'to worker';
  if (p === 'delegated_to_coder') return 'to coder';
  if (p === 'queued') return 'queued';
  if (p === 'waiting') return 'waiting';
  if (p === 'processing') return 'processing';
  if (p === 'worker_verifying') return 'verifying';
  if (p === 'retrying') return 'retrying';
  if (p === 'reporting') return 'reporting';
  if (p === 'done') return 'done';
  if (p === 'error') return 'error';
  return p;
}

function classifyJobPhase(ev, current) {
  const from = String(ev.from || '').toLowerCase();
  const to = String(ev.to || '').toLowerCase();
  const content = String(ev.content || '');
  if (ev.type === 'message.received') return {phase: 'received', owner: 'mio'};
  if (ev.type === 'routing.decision') return {phase: 'routing', owner: current.owner || 'mio'};
  if (ev.type === 'agent.delegate') return {phase: 'delegating', owner: to || current.owner};
  if (ev.type === 'agent.dispatch') return {phase: 'delegating', owner: to || current.owner};
  if (ev.type === 'agent.thinking') return {phase: 'chatting', owner: from || current.owner};
  if (ev.type === 'mailbox.sent') return {phase: 'queued', owner: to || current.owner};
  if (ev.type === 'mailbox.waiting') return {phase: 'waiting', owner: to || current.owner};
  if (ev.type === 'mailbox.received') return {phase: 'processing', owner: from || current.owner};
  if (ev.type === 'worker.retry_request') return {phase: 'retrying', owner: to || 'coder1'};
  if (ev.type === 'worker.request') return {phase: 'worker_verifying', owner: 'worker'};
  if (ev.type === 'worker.result') return {phase: 'reporting', owner: 'shiro'};
  if (ev.type === 'worker.classified_failure') return {phase: 'retrying', owner: from || 'shiro'};
  if (ev.type === 'agent.error' || ev.type === 'mailbox.error') return {phase: 'error', owner: from || current.owner};
  if (ev.type === 'agent.start') {
    if (to === 'shiro') {
      if (content.indexOf('Worker実行') >= 0 || content.indexOf('Patch') >= 0 || content.indexOf('整形') >= 0) {
        return {phase: 'worker_verifying', owner: 'shiro'};
      }
      return {phase: 'delegated_to_worker', owner: 'shiro'};
    }
    if (to.indexOf('coder') === 0) return {phase: 'delegated_to_coder', owner: to};
    if (to === 'mio') return {phase: 'reporting', owner: 'mio'};
  }
  if (ev.type === 'agent.response') {
    if (from === 'mio' && to === 'user') {
      const lower = content.toLowerCase();
      return {phase: (lower.indexOf('error') >= 0 || lower.indexOf('失敗') >= 0) ? 'error' : 'done', owner: 'mio'};
    }
    if (from === 'shiro' && to === 'mio') return {phase: 'reporting', owner: 'mio'};
    if (from.indexOf('coder') === 0 && to === 'shiro') return {phase: 'worker_verifying', owner: 'shiro'};
  }
  if (ev.type === 'agent.report' && from === 'shiro' && to === 'mio') return {phase: 'reporting', owner: 'mio'};
  return {phase: current.phase || 'received', owner: current.owner || '-'};
}

function deriveProgressData() {
  const jobs = {};
  state.logs.forEach((ev) => {
    const jid = String(ev.job_id || '');
    if (!jid) return;
    const current = jobs[jid] || {
      jobID: jid,
      route: ev.route || '-',
      phase: 'received',
      owner: 'mio',
      retryCount: 0,
      failureKind: '',
      failureReason: '',
      latestSummary: '',
      latestChatReport: '',
      latestWorkerNote: '',
      latestCoderNote: '',
      status: 'running',
      updatedAt: ev.timestamp || '',
      startedAt: ev.timestamp || '',
      recentEvents: [],
    };

    current.route = ev.route || current.route;
    current.updatedAt = ev.timestamp || current.updatedAt;
    if (!current.startedAt) current.startedAt = ev.timestamp || '';
    if (ev.content) current.latestSummary = short(ev.content, 120);

    const phased = classifyJobPhase(ev, current);
    current.phase = phased.phase;
    current.owner = phased.owner;

    if (ev.type === 'worker.retry_request') {
      const m = String(ev.content || '').match(/retry=(\d+)/);
      const retry = m ? Number(m[1]) : (current.retryCount + 1);
      if (retry > current.retryCount) current.retryCount = retry;
    }
    if (ev.type === 'worker.classified_failure') {
      const raw = String(ev.content || '');
      const idx = raw.indexOf(':');
      current.failureKind = idx > 0 ? raw.slice(0, idx).trim() : raw.trim();
      current.failureReason = idx > 0 ? raw.slice(idx + 1).trim() : raw.trim();
      current.status = 'error';
    }
    if (ev.type === 'agent.error' || ev.type === 'mailbox.error') {
      current.failureKind = ev.type;
      current.failureReason = String(ev.content || '').trim();
      current.status = 'error';
    }
    if (ev.type === 'agent.note') {
      if (String(ev.from || '').toLowerCase() === 'mio' && String(ev.to || '').toLowerCase() === 'user') current.latestChatReport = ev.content || current.latestChatReport;
      if (String(ev.from || '').toLowerCase() === 'shiro') current.latestWorkerNote = ev.content || current.latestWorkerNote;
      if (String(ev.from || '').toLowerCase().indexOf('coder') === 0) current.latestCoderNote = ev.content || current.latestCoderNote;
    }
    if ((ev.type === 'agent.error' || ev.type === 'mailbox.error') && String(ev.from || '').toLowerCase().indexOf('coder') === 0) {
      current.latestCoderNote = ev.content || current.latestCoderNote;
    }
    if (ev.type === 'agent.response' && String(ev.from || '').toLowerCase() === 'mio' && String(ev.to || '').toLowerCase() === 'user') {
      current.latestChatReport = ev.content || current.latestChatReport;
      current.status = current.phase === 'error' ? 'error' : 'done';
    } else if (ev.type === 'agent.response' && current.status !== 'error') {
      current.status = (current.phase === 'done') ? 'done' : 'running';
    }

    if (ev.type === 'agent.start' || ev.type === 'agent.dispatch' || ev.type === 'agent.note' || ev.type === 'agent.response' || ev.type === 'agent.error' || ev.type === 'mailbox.sent' || ev.type === 'mailbox.waiting' || ev.type === 'mailbox.received' || ev.type === 'mailbox.error' || ev.type === 'worker.retry_request' || ev.type === 'worker.classified_failure' || ev.type === 'message.received' || ev.type === 'routing.decision') {
      current.recentEvents.push({
        timestamp: ev.timestamp || '',
        type: ev.type || '',
        from: ev.from || '',
        to: ev.to || '',
        content: short(ev.content || '', 200),
      });
      if (current.recentEvents.length > PROGRESS_RECENT_EVENTS) current.recentEvents.shift();
    }

    jobs[jid] = current;
  });

  const list = Object.values(jobs).sort((a, b) => (b.updatedAt || '').localeCompare(a.updatedAt || ''));
  const running = list.filter((j) => j.status !== 'done');
  const done = list.filter((j) => j.status === 'done').slice(0, PROGRESS_DONE_LIMIT);
  const filtered = running.concat(done);

  const agents = {};
  AGENTS.forEach((id) => {
    const base = state.agents[id] || {};
    const related = filtered.filter((j) => j.owner === id || String(base.jobID || '') === String(j.jobID || '')).sort((a, b) => (b.updatedAt || '').localeCompare(a.updatedAt || ''));
    const top = related[0] || null;
    agents[id] = {
      id: id,
      state: base.state || 'offline',
      jobID: top ? top.jobID : (base.jobID || '-'),
      phase: top ? top.phase : '-',
      retryCount: top ? top.retryCount : 0,
      failureKind: top ? top.failureKind : '',
      latestSummary: top ? top.latestSummary : (base.preview || '-'),
      updatedAt: top ? top.updatedAt : (base.updatedAt || ''),
      openCount: Object.keys(state.openTasks[id] || {}).length,
    };
  });

  return {jobs: filtered, agents: agents};
}

function toggleProgressJob(jobID) {
  state.progressOpenJobs[jobID] = !state.progressOpenJobs[jobID];
  renderProgress();
}
window.toggleProgressJob = toggleProgressJob;

function renderProgressDetail(job) {
  const items = Array.isArray(job.recentEvents) ? job.recentEvents.slice().reverse() : [];
  const logs = items.length === 0
    ? '<div class="progress-empty">No recent events</div>'
    : items.map((item) => (
      '<div class="progress-log-item">' +
        '<div class="progress-log-meta">' + esc(ftime(item.timestamp)) + ' · ' + esc(item.type) + ' · ' + esc(agName(item.from || '-')) + ' → ' + esc(agName(item.to || '-')) + '</div>' +
        '<div>' + esc(item.content || '-') + '</div>' +
      '</div>'
    )).join('');
  return '' +
    '<div class="progress-detail">' +
      '<div class="progress-section"><b>Chat Report</b><div style="margin-top:6px;line-height:1.5">' + esc(job.latestChatReport || '-') + '</div></div>' +
      '<div class="progress-section"><b>Worker Note</b><div style="margin-top:6px;line-height:1.5">' + esc(job.latestWorkerNote || '-') + '</div></div>' +
      '<div class="progress-section"><b>Coder Note</b><div style="margin-top:6px;line-height:1.5">' + esc(job.latestCoderNote || '-') + '</div></div>' +
      '<div class="progress-section"><b>Failure</b><div style="margin-top:6px;line-height:1.5">' + esc(job.failureReason || '-') + '</div></div>' +
      '<div class="progress-section"><b>Recent Events</b><div class="progress-log" style="margin-top:6px">' + logs + '</div></div>' +
    '</div>';
}

function renderProgress() {
  const data = deriveProgressData();
  const agentCards = document.getElementById('progressAgentCards');
  const body = document.getElementById('progressBody');
  const agentSummary = document.getElementById('progressAgentSummary');
  const jobSummary = document.getElementById('progressJobSummary');
  if (!agentCards || !body || !agentSummary || !jobSummary) return;

  agentCards.innerHTML = '';
  body.innerHTML = '';

  const agentValues = Object.values(data.agents);
  const activeAgents = agentValues.filter((a) => a.state !== 'offline').length;
  agentSummary.textContent = 'active: ' + String(activeAgents) + ' / ' + String(agentValues.length);
  jobSummary.textContent = 'showing: ' + String(data.jobs.length) + ' jobs';

  agentValues.forEach((a) => {
    const info = ag(a.id);
    const card = document.createElement('div');
    card.className = 'card';
    card.innerHTML =
      '<h4>' + info.e + ' ' + info.l + '</h4>' +
      '<div class="row"><span>State</span><span class="badge ' + stateClass(a.state) + '">' + esc(a.state) + '</span></div>' +
      '<div class="row"><span>Phase</span><span class="phase-badge">' + esc(progressPhaseLabel(a.phase)) + '</span></div>' +
      '<div class="row"><span>Job</span><span class="code">' + esc(a.jobID || '-') + '</span></div>' +
      '<div class="row"><span>Retry</span><span>' + esc(String(a.retryCount || 0)) + '</span></div>' +
      '<div class="row"><span>Failure</span><span>' + esc(a.failureKind || '-') + '</span></div>' +
      '<div class="row"><span>Open</span><span>' + esc(String(a.openCount || 0)) + '</span></div>' +
      '<div class="row"><span>Updated</span><span>' + esc(ftime(a.updatedAt)) + '</span></div>' +
      '<div class="small" style="margin-top:8px;line-height:1.5">' + esc(short(a.latestSummary || '-', 120)) + '</div>';
    agentCards.appendChild(card);
  });

  if (data.jobs.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="10" class="small">No progress data yet</td>';
    body.appendChild(tr);
    return;
  }

  data.jobs.forEach((j) => {
    const st = j.status === 'error' ? 'error' : (j.status === 'done' ? 'idle' : 'running');
    const open = !!state.progressOpenJobs[j.jobID];
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td class="code">' + esc(j.jobID) + '</td>' +
      '<td>' + esc(j.route || '-') + '</td>' +
      '<td><span class="phase-badge">' + esc(progressPhaseLabel(j.phase)) + '</span></td>' +
      '<td>' + esc(agName(j.owner || '-')) + '</td>' +
      '<td>' + esc(String(j.retryCount || 0)) + '</td>' +
      '<td><span class="badge ' + errorKindClass(j.failureKind || '') + '">' + esc(j.failureKind || '-') + '</span></td>' +
      '<td><span class="badge ' + stateClass(st) + '">' + esc(j.status || '-') + '</span></td>' +
      '<td>' + esc(fdt(j.updatedAt)) + '</td>' +
      '<td>' + esc(short(j.latestSummary || '-', 120)) + '</td>' +
      '<td><button class="ctl-btn" onclick="toggleProgressJob(\'' + esc(j.jobID) + '\')">' + (open ? 'Hide' : 'Open') + '</button></td>';
    body.appendChild(tr);

    if (open) {
      const exp = document.createElement('tr');
      exp.className = 'progress-expand';
      exp.innerHTML = '<td colspan="10">' + renderProgressDetail(j) + '</td>';
      body.appendChild(exp);
    }
  });
}
