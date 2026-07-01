// Ops tab module: LLM runtime and memory management UI.
function latestOpsEventBy(fn) {
  const list = Array.isArray(state.ops.persistedLogs) ? state.ops.persistedLogs : [];
  for (let i = 0; i < list.length; i++) {
    const ev = list[i];
    if (fn(ev)) return ev;
  }
  return null;
}

function currentOpsSummary() {
  const logsFetchError = String(state.ops.opsLogsFetchError || '');
  const persisted = logsFetchError ? [] : (Array.isArray(state.ops.persistedLogs) ? state.ops.persistedLogs : []);
  const runningJobs = Object.values(state.jobs).filter((j) => String(j.status || '') !== 'done');
  const lastMio = logsFetchError ? null : (state.ops.lastMioReport || latestOpsEventBy((ev) => String(ev.from || '').toLowerCase() === 'mio' && String(ev.to || '').toLowerCase() === 'user'));
  const latestJobID = logsFetchError ? '' : (state.ops.latestJobID || ((persisted[0] && persisted[0].job_id) || '-'));
  const latestRoute = logsFetchError ? '' : (state.ops.latestRoute || ((persisted[0] && persisted[0].route) || '-'));
  const latestError = logsFetchError ? null : (state.ops.latestError || latestOpsEventBy((ev) => {
    const t = String(ev.type || '').toLowerCase();
    return t === 'agent.error' || t === 'mailbox.error' || t === 'worker.classified_failure';
  }));
  const activeAgents = AGENTS.filter((id) => {
    const s = state.agents[id];
    return s && s.state !== 'offline';
  });
  return {logsFetchError, persisted, runningJobs, lastMio, latestJobID, latestRoute, latestError, activeAgents};
}

function opsTriageCard(title, value, detail, stateName) {
  return '<div class="ops-triage-card state-' + esc(stateName || 'offline') + '">' +
    '<div class="ops-triage-top"><div class="ops-triage-title">' + esc(title) + '</div><span class="ops-triage-dot" aria-hidden="true"></span></div>' +
    '<div class="ops-triage-value">' + esc(value || '-') + '</div>' +
    '<div class="ops-triage-detail">' + esc(detail || '-') + '</div>' +
  '</div>';
}

function compactOpsDetail(text, limit) {
  return short(String(text || '').replace(/\s+/g, ' '), limit || 120);
}

function opsAudioTriage() {
  const audio = state.ops.runtimeDebugSystem && state.ops.runtimeDebugSystem.audio ? state.ops.runtimeDebugSystem.audio : null;
  const runtimeDebugError = String(state.ops.runtimeDebugSystemFetchError || '').trim();
  if (!audio) {
    return {
      value: 'not checked',
      detail: runtimeDebugError ? 'blocked: ' + runtimeDebugError : 'audio readiness not loaded',
      state: runtimeDebugError ? 'error' : 'thinking',
    };
  }
  const sttOK = audio.stt_ok === true;
  const ttsOK = audio.tts_live_ok === true || audio.tts_ready_ok === true;
  const err = audio.last_error ? 'blocked: ' + String(audio.last_error) : '';
  return {
    value: (sttOK ? 'STT ok' : 'STT ?') + ' / ' + (ttsOK ? 'TTS ok' : 'TTS ?'),
    detail: err ? compactOpsDetail(err, 118) : ([audio.stt_base_url || state.ops.runtimeSTTBaseURL || '', audio.tts_base_url || state.ops.runtimeTTSBaseURL || ''].filter(Boolean).join('\n') || 'audio readiness loaded'),
    state: err ? 'error' : (sttOK && ttsOK ? 'running' : 'thinking'),
  };
}

function opsLLMTriage() {
  if (state.ops.llmStatusError) {
    return {value: 'blocked', detail: compactOpsDetail(state.ops.llmStatusError, 118), state: 'error'};
  }
  if (state.ops.llmStatus) {
    return {value: 'live', detail: state.ops.llmOpsBaseURL || 'LLM Ops status loaded', state: 'running'};
  }
  if (state.ops.llmOpsEnabled) {
    return {value: 'checking', detail: state.ops.llmOpsBaseURL || 'LLM Ops proxy enabled', state: 'thinking'};
  }
  return {value: 'disabled', detail: state.ops.llmOpsConfigured ? 'LLM Ops token missing or proxy disabled' : 'LLM Ops not configured', state: 'offline'};
}

function refreshOpsTriageFromState(summary) {
  const target = document.getElementById('opsTriageCards');
  if (!target) return;
  const data = summary || currentOpsSummary();
  const runtimeHealth = state.ops.runtimeHealth || null;
  const runtimeOK = runtimeHealth && runtimeHealth.status === 'ok';
  const runtimeDetail = runtimeHealthDetailText(runtimeHealth, state.ops.runtimeHealthError);
  const llm = opsLLMTriage();
  const audio = opsAudioTriage();
  target.innerHTML = [
    opsTriageCard('Runtime', state.ops.runtimeHealthError ? 'blocked' : (runtimeOK ? 'ok' : 'checking'), runtimeDetail, state.ops.runtimeHealthError ? 'error' : (runtimeOK ? 'running' : 'thinking')),
    opsTriageCard('LLM', llm.value, llm.detail, llm.state),
    opsTriageCard('Audio', audio.value, audio.detail, audio.state),
    opsTriageCard('Jobs', String(data.runningJobs.length), data.runningJobs.slice(0, 2).map((j) => (j.id || '-') + ' · ' + (j.route || '-') + ' · ' + (j.status || '-')).join('\n') || '進行中ジョブなし', data.runningJobs.length ? 'thinking' : 'running'),
    opsTriageCard('Errors', data.logsFetchError ? 'unavailable' : (data.latestError ? short(data.latestError.type || '-', 20) : 'none'), data.logsFetchError ? ('ops logs unavailable: ' + data.logsFetchError) : (data.latestError ? short(data.latestError.content || '-', 90) : '直近の失敗イベントなし'), data.logsFetchError || data.latestError ? 'error' : 'running'),
  ].join('');
}

function renderOpsCardList(target, items) {
  if (!target) return;
  target.innerHTML = '';
  items.forEach((item) => {
    const card = document.createElement('div');
    card.className = 'card';
    card.innerHTML =
      '<div class="ops-card-title">' + esc(item.title) + '</div>' +
      '<div class="ops-big">' + esc(item.big) + '</div>' +
      '<div class="ops-sub">' + esc(item.sub) + '</div>';
    target.appendChild(card);
  });
}

function renderOps() {
  const cards = document.getElementById('opsCards');
  const secondaryCards = document.getElementById('opsSecondaryCards');
  const focusBody = document.getElementById('opsFocusBody');
  const feedBody = document.getElementById('opsFeedBody');
  if (!cards || !focusBody || !feedBody) return;
  cards.innerHTML = '';
  if (secondaryCards) secondaryCards.innerHTML = '';
  focusBody.innerHTML = '';
  feedBody.innerHTML = '';
  bindDCISearchControls();

  const summary = currentOpsSummary();
  const logsFetchError = summary.logsFetchError;
  const persisted = summary.persisted;
  const runningJobs = summary.runningJobs;
  const lastMio = summary.lastMio;
  const latestJobID = summary.latestJobID;
  const latestRoute = summary.latestRoute;
  const latestError = summary.latestError;
  const activeAgents = summary.activeAgents;
  refreshOpsTriageFromState(summary);

  const primaryCards = [
    {
      title: 'Latest Job',
      big: logsFetchError ? 'unavailable' : (latestJobID || '-'),
      sub: logsFetchError ? ('ops logs unavailable: ' + logsFetchError) : ('route: ' + (latestRoute || '-')),
    },
    {
      title: 'Mio Last Report',
      big: logsFetchError ? 'unavailable' : (lastMio ? short(lastMio.content || '-', 48) : '-'),
      sub: logsFetchError ? ('ops logs unavailable: ' + logsFetchError) : (lastMio ? ('time: ' + fdt(lastMio.timestamp) + '\njob: ' + (lastMio.job_id || '-')) : 'Mio からの最終報告はまだありません'),
    },
    {
      title: 'Running Jobs',
      big: String(runningJobs.length),
      sub: runningJobs.slice(0, 3).map((j) => (j.id || '-') + ' · ' + (j.route || '-') + ' · ' + (j.status || '-')).join('\n') || '進行中ジョブなし',
    },
    {
      title: 'Last Error',
      big: logsFetchError ? 'unavailable' : (latestError ? short(latestError.type || '-', 24) : 'none'),
      sub: logsFetchError ? ('ops logs unavailable: ' + logsFetchError) : (latestError ? (short(latestError.content || '-', 120) + '\njob: ' + (latestError.job_id || '-')) : '直近の失敗イベントなし'),
    },
    {
      title: 'Active Agents',
      big: String(activeAgents.length),
      sub: activeAgents.map((id) => agName(id) + ' · ' + (state.agents[id].state || '-')).join('\n') || '全員 offline',
    },
  ];
  const secondaryItems = [
    toolHarnessOpsCard(),
    dciOpsCard(),
    sandboxOpsCard(),
    skillGovernanceOpsCard(),
    workstreamOpsCard(),
    revenueOpsCard(),
    personaObservationOpsCard(),
    browserTraceAPIOpsCard(),
    complexityHotspotOpsCard(),
    aiWorkflowOpsCard(),
    superAgentOpsCard(),
    heavyWorkerRuntimeOpsCard(),
    knowledgeMemoryOpsCard(),
    typeof hobbyGraphOpsCard === 'function' ? hobbyGraphOpsCard() : {title: 'Hobby Graph', big: 'not loaded', sub: 'hobby graph ops card not loaded'},
    runtimeBlockedRoutesOpsCard(),
  ];
  renderOpsCardList(cards, primaryCards);
  renderOpsCardList(secondaryCards, secondaryItems);

  [
    {label: '最新 route', value: logsFetchError ? 'unavailable' : (latestRoute || '-')},
    {label: '最新 persisted event', value: logsFetchError ? ('Ops logs unavailable: ' + logsFetchError) : (persisted[0] ? ((persisted[0].type || '-') + ' @ ' + fdt(persisted[0].timestamp)) : '-')},
    {label: 'Mio job', value: state.agents.mio && state.agents.mio.jobID ? state.agents.mio.jobID : '-'},
    {label: 'Worker job', value: state.agents.shiro && state.agents.shiro.jobID ? state.agents.shiro.jobID : '-'},
  ].forEach((row) => {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td>' + esc(row.label) + '</td><td>' + esc(row.value) + '</td>';
    focusBody.appendChild(tr);
  });
  renderKnowledgeMemoryDetailFocus(focusBody);

  const feed = persisted.filter((ev) => {
    const t = String(ev.type || '');
    return t === 'message.received' || t === 'routing.decision' || t === 'agent.dispatch' || t === 'agent.start' || t === 'agent.note' || t === 'agent.response' || t === 'mailbox.waiting' || t === 'mailbox.received' || t === 'mailbox.error' || t === 'agent.error' || t === 'worker.classified_failure';
  }).slice(0, 20);
  if (logsFetchError) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="6" class="small">Ops logs unavailable: ' + esc(logsFetchError) + '</td>';
    feedBody.appendChild(tr);
    return;
  }
  if (feed.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="6" class="small">No operator events yet</td>';
    feedBody.appendChild(tr);
    return;
  }
  feed.forEach((ev) => {
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td>' + esc(ftime(ev.timestamp)) + '</td>' +
      '<td>' + esc(ev.type || '-') + '</td>' +
      '<td>' + esc(agName(ev.from || '-')) + ' → ' + esc(agName(ev.to || '-')) + '</td>' +
      '<td class="code">' + esc(ev.job_id || '-') + '</td>' +
      '<td>' + esc(ev.route || '-') + '</td>' +
      '<td>' + esc(short(ev.content || '-', 140)) + '</td>';
    feedBody.appendChild(tr);
  });
  renderToolHarnessEvents();
  renderDCITraces();
  renderDCISearchResult();
  renderSandboxStatus();
  renderSkillExternalPRAudits();
  renderSkillEvidenceAudits();
  renderSuperAgentTerminalAudits();
  renderSuperAgentResumeAudits();
  renderAIWorkflowRunEvidence();
  renderComplexityReviewArtifacts();
  renderRuntimeBlockedRouteAudits();
  renderWorkstreamVaultReviews();
  renderRevenueHumanDecisions();
  renderRevenueChannelDrafts();
  renderRevenueExternalSendAudits();
  renderRevenueDrilldown();
  renderPersonaMetaReviews();
}

function toolHarnessField(ev, snake, pascal) {
  if (!ev) return undefined;
  if (Object.prototype.hasOwnProperty.call(ev, snake)) return ev[snake];
  if (Object.prototype.hasOwnProperty.call(ev, pascal)) return ev[pascal];
  return undefined;
}

function toolHarnessRepairSummary(repair) {
  if (!repair) return '-';
  const kind = toolHarnessField(repair, 'type', 'Type') || '-';
  const path = toolHarnessField(repair, 'path', 'Path');
  const p = Array.isArray(path) ? path.join('.') : String(path || '');
  return p ? kind + ':' + p : kind;
}

function toolHarnessOpsCard() {
  const fetchError = String(state.ops.toolHarnessFetchError || '');
  if (fetchError) {
    return {
      title: 'Tool Harness',
      big: 'unavailable',
      sub: 'tool harness status unavailable: ' + fetchError + '\nblocked: provider protocol recovery state unreadable',
    };
  }
  const events = Array.isArray(state.ops.toolHarnessEvents) ? state.ops.toolHarnessEvents : [];
  const repaired = events.filter((ev) => String(toolHarnessField(ev, 'validation_status', 'ValidationStatus') || '') === 'repaired').length;
  const latest = events[0] || null;
  const latestStatus = latest ? String(toolHarnessField(latest, 'validation_status', 'ValidationStatus') || '-') : '-';
  const latestTool = latest ? String(toolHarnessField(latest, 'tool_name', 'ToolName') || '-') : '-';
  return {
    title: 'Tool Harness',
    big: String(repaired) + '/' + String(events.length),
    sub: latest ? ('latest: ' + latestTool + ' · ' + latestStatus + '\nread-only evidence: provider protocol recovery not verified') : 'mediation event なし\nblocked: provider protocol recovery not verified',
  };
}

function renderToolHarnessEvents() {
  const body = document.getElementById('toolHarnessBody');
  if (!body) return;
  body.innerHTML = '';
  const fetchError = String(state.ops.toolHarnessFetchError || '');
  if (fetchError) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="6" class="small">Tool Harness events unavailable: ' + esc(fetchError) + '</td>';
    body.appendChild(tr);
    return;
  }
  const events = Array.isArray(state.ops.toolHarnessEvents) ? state.ops.toolHarnessEvents : [];
  if (events.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="6" class="small">No tool mediation events yet</td>';
    body.appendChild(tr);
    return;
  }
  events.slice(0, 30).forEach((ev) => {
    const repairs = toolHarnessField(ev, 'repairs_applied', 'Repairs') || [];
    const defaults = toolHarnessField(ev, 'relation_defaults_applied', 'RelationDefaults') || [];
    const status = String(toolHarnessField(ev, 'validation_status', 'ValidationStatus') || '-');
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td>' + esc(ftime(toolHarnessField(ev, 'created_at', 'CreatedAt'))) + '</td>' +
      '<td>' + esc(toolHarnessField(ev, 'tool_name', 'ToolName') || '-') + '</td>' +
      '<td><span class="badge ' + stateClass(status) + '">' + esc(status) + '</span></td>' +
      '<td class="code">' + esc(Array.isArray(repairs) && repairs.length ? repairs.map(toolHarnessRepairSummary).join(', ') : '-') + '</td>' +
      '<td class="code">' + esc(Array.isArray(defaults) && defaults.length ? defaults.map((d) => String(toolHarnessField(d, 'field', 'Field') || '-') + '=' + String(toolHarnessField(d, 'value', 'Value'))).join(', ') : '-') + '</td>' +
      '<td class="code">' + esc(short(toolHarnessField(ev, 'raw_input_hash', 'RawInputHash') || '-', 32)) + '</td>';
    body.appendChild(tr);
  });
}

function dciField(trace, snake, pascal) {
  if (!trace) return undefined;
  if (Object.prototype.hasOwnProperty.call(trace, snake)) return trace[snake];
  if (Object.prototype.hasOwnProperty.call(trace, pascal)) return trace[pascal];
  return undefined;
}

function dciOpsCard() {
  const fetchError = String(state.ops.dciFetchError || '');
  if (fetchError) {
    return {
      title: 'DCI Trace',
      big: 'unavailable',
      sub: 'dci trace status unavailable: ' + fetchError + '\nblocked: read-only evidence state unreadable\nblocked: VectorDB/Qdrant E2E not verified',
    };
  }
  const traces = Array.isArray(state.ops.dciTraces) ? state.ops.dciTraces : [];
  const latest = traces[0] || null;
  const evidenceCount = traces.reduce((sum, trace) => sum + Number(dciField(trace, 'final_evidence_count', 'FinalEvidenceCount') || 0), 0);
  return {
    title: 'DCI Trace',
    big: String(evidenceCount) + '/' + String(traces.length),
    sub: latest ? ('latest: ' + short(dciField(latest, 'user_query', 'UserQuery') || '-', 72) + '\nread-only evidence: VectorDB/Qdrant E2E not verified') : 'Search Trace なし\nblocked: VectorDB/Qdrant E2E not verified',
  };
}

function renderDCITraces() {
  const body = document.getElementById('dciTraceBody');
  if (!body) return;
  body.innerHTML = '';
  const fetchError = String(state.ops.dciFetchError || '');
  if (fetchError) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="6" class="small">DCI search traces unavailable: ' + esc(fetchError) + '</td>';
    body.appendChild(tr);
    return;
  }
  const traces = Array.isArray(state.ops.dciTraces) ? state.ops.dciTraces : [];
  if (traces.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="6" class="small">No DCI search traces yet</td>';
    body.appendChild(tr);
    return;
  }
  traces.slice(0, 20).forEach((trace) => {
    const scope = dciField(trace, 'corpus_scope', 'CorpusScope') || [];
    const status = String(dciField(trace, 'status', 'Status') || '-');
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td>' + esc(ftime(dciField(trace, 'ended_at', 'EndedAt') || dciField(trace, 'started_at', 'StartedAt'))) + '</td>' +
      '<td class="code">' + esc(short(dciField(trace, 'event_id', 'EventID') || '-', 32)) + '</td>' +
      '<td>' + esc(short(dciField(trace, 'user_query', 'UserQuery') || '-', 90)) + '</td>' +
      '<td>' + esc(String(dciField(trace, 'final_evidence_count', 'FinalEvidenceCount') || 0)) + '</td>' +
      '<td><span class="badge ' + stateClass(status) + '">' + esc(status) + '</span></td>' +
      '<td class="code">' + esc(Array.isArray(scope) ? scope.join(', ') : String(scope || '-')) + '</td>';
    body.appendChild(tr);
  });
}

function renderDCISearchResult() {
  const el = document.getElementById('dciSearchResult');
  if (!el) return;
  const result = state.ops.dciLastResult || null;
  if (!result) {
    el.textContent = 'DCI manual search result: -';
    return;
  }
  const pack = result.pack || result.Pack || {};
  const trace = result.trace || result.Trace || {};
  const evidence = pack.evidence || pack.Evidence || [];
  const lines = [
    'event: ' + String(pack.event_id || pack.EventID || trace.event_id || trace.EventID || '-'),
    'query: ' + String(pack.query || pack.Query || '-'),
    'status: ' + String(trace.status || trace.Status || '-'),
    'evidence: ' + String(Array.isArray(evidence) ? evidence.length : 0),
  ];
  const errorMessage = String(trace.error_message || trace.ErrorMessage || '');
  if (errorMessage) {
    lines.push('error: ' + errorMessage);
  }
  if (Array.isArray(evidence) && evidence.length) {
    evidence.slice(0, 3).forEach((ev, idx) => {
      const file = ev.file_path || ev.FilePath || '-';
      const line = ev.line_start || ev.LineStart || '-';
      const snippet = ev.snippet || ev.Snippet || '-';
      lines.push(String(idx + 1) + '. ' + file + ':' + line + ' ' + short(snippet, 160));
    });
  }
  el.textContent = lines.join('\n');
}

function sandboxField(item, snake, pascal) {
  if (!item) return undefined;
  if (Object.prototype.hasOwnProperty.call(item, snake)) return item[snake];
  if (Object.prototype.hasOwnProperty.call(item, pascal)) return item[pascal];
  return undefined;
}

function sandboxOpsCard() {
  const fetchError = String(state.ops.sandboxFetchError || '');
  if (fetchError) {
    return {
      title: 'Sandbox Gate',
      big: 'unavailable',
      sub: 'sandbox status unavailable: ' + fetchError + '\nblocked: promotion apply state unreadable',
    };
  }
  const sandboxes = Array.isArray(state.ops.sandboxes) ? state.ops.sandboxes : [];
  const artifacts = Array.isArray(state.ops.sandboxArtifacts) ? state.ops.sandboxArtifacts : [];
  const promotions = Array.isArray(state.ops.sandboxPromotions) ? state.ops.sandboxPromotions : [];
  const decisions = Array.isArray(state.ops.sandboxDecisions) ? state.ops.sandboxDecisions : [];
  const logs = Array.isArray(state.ops.sandboxGateLogs) ? state.ops.sandboxGateLogs : [];
  const blocked = decisions.filter((d) => String(sandboxField(d, 'status', 'Status') || '') !== 'approve').length;
  const latestLog = logs[0] || null;
  return {
    title: 'Sandbox Gate',
    big: String(promotions.length) + '/' + String(sandboxes.length),
    sub: sandboxes.length || promotions.length || artifacts.length ? ('artifacts: ' + String(artifacts.length) + '\nblocked/needs review: ' + String(blocked) + '\nlatest log: ' + String(sandboxField(latestLog, 'gate_status', 'GateStatus') || '-')) : 'sandbox record なし',
  };
}

function renderSandboxStatus() {
  const body = document.getElementById('sandboxBody');
  if (!body) return;
  body.innerHTML = '';
  const sandboxes = Array.isArray(state.ops.sandboxes) ? state.ops.sandboxes : [];
  const promotions = Array.isArray(state.ops.sandboxPromotions) ? state.ops.sandboxPromotions : [];
  const decisions = Array.isArray(state.ops.sandboxDecisions) ? state.ops.sandboxDecisions : [];
  const fetchError = String(state.ops.sandboxFetchError || '');
  if (fetchError) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="7" class="small">Sandbox status unavailable: ' + esc(fetchError) + '</td>';
    body.appendChild(tr);
    renderSandboxPromotionPreviewResult();
    renderSandboxGateLogs();
    return;
  }
  if (sandboxes.length === 0 && promotions.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="7" class="small">No sandbox or promotion records yet</td>';
    body.appendChild(tr);
    renderSandboxPromotionPreviewResult();
    renderSandboxGateLogs();
    return;
  }
  const rows = Math.max(sandboxes.length, promotions.length);
  for (let i = 0; i < rows; i++) {
    const sandbox = sandboxes[i] || {};
    const promotion = promotions[i] || {};
    const decision = decisions[i] || {};
    const sandboxStatus = String(sandboxField(sandbox, 'status', 'Status') || '-');
    const gate = String(sandboxField(decision, 'status', 'Status') || '-');
    const promotionID = String(sandboxField(promotion, 'promotion_id', 'PromotionID') || '');
    const previewPayload = promotionID ? encodeURIComponent(JSON.stringify(promotion)) : '';
    const preview = promotionID
      ? '<button class="ctl-btn sandbox-promotion-preview" type="button" data-promotion="' + escAttr(previewPayload) + '">Preview</button>'
      : '-';
    const manualReview = promotionID
      ? ' <button class="ctl-btn sandbox-promotion-manual-review" type="button" data-promotion="' + escAttr(previewPayload) + '">Manual Review</button>'
      : '';
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td class="code">' + esc(sandboxField(sandbox, 'sandbox_id', 'SandboxID') || sandboxField(promotion, 'sandbox_id', 'SandboxID') || '-') + '</td>' +
      '<td>' + esc(sandboxField(sandbox, 'type', 'Type') || '-') + '</td>' +
      '<td><span class="badge ' + stateClass(sandboxStatus) + '">' + esc(sandboxStatus) + '</span></td>' +
      '<td class="code">' + esc(short(sandboxField(sandbox, 'path', 'Path') || '-', 90)) + '</td>' +
      '<td class="code">' + esc(promotionID || '-') + '</td>' +
      '<td><span class="badge ' + stateClass(gate) + '">' + esc(gate) + '</span></td>' +
      '<td>' + preview + manualReview + '</td>';
    body.appendChild(tr);
  }
  body.querySelectorAll('.sandbox-promotion-preview').forEach((btn) => {
    btn.addEventListener('click', () => {
      previewSandboxPromotion(btn.getAttribute('data-promotion') || '');
    });
  });
  body.querySelectorAll('.sandbox-promotion-manual-review').forEach((btn) => {
    btn.addEventListener('click', () => {
      requestSandboxPromotionManualReview(btn.getAttribute('data-promotion') || '');
    });
  });
  renderSandboxPromotionPreviewResult();
  renderSandboxGateLogs();
}

function renderSandboxPromotionPreviewResult() {
  const el = document.getElementById('sandboxPromotionPreviewResult');
  if (!el) return;
  const fetchError = String(state.ops.sandboxFetchError || '');
  if (fetchError) {
    el.textContent = 'sandbox promotion diff preview unavailable: ' + fetchError + '\nblocked: promotion apply state unreadable';
    return;
  }
  const preview = state.ops.sandboxPromotionPreviewResult || null;
  if (!preview) {
    el.textContent = 'sandbox promotion diff preview: -';
    return;
  }
  const reviewResult = state.ops.sandboxPromotionManualReviewResult || null;
  el.textContent = formatSandboxPromotionDiffPreview(preview) + (reviewResult ? '\n\nmanual review workflow:\n' + JSON.stringify(reviewResult, null, 2) : '');
}

function renderSandboxGateLogs() {
  const body = document.getElementById('sandboxGateLogBody');
  if (!body) return;
  body.innerHTML = '';
  const fetchError = String(state.ops.sandboxFetchError || '');
  if (fetchError) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="7" class="small">Sandbox gate logs unavailable: ' + esc(fetchError) + '</td>';
    body.appendChild(tr);
    renderSandboxGateLogResult();
    return;
  }
  const logs = Array.isArray(state.ops.sandboxGateLogs) ? state.ops.sandboxGateLogs : [];
  if (logs.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="7" class="small">No sandbox promotion gate logs yet</td>';
    body.appendChild(tr);
    renderSandboxGateLogResult();
    return;
  }
  logs.slice(0, 20).forEach((item) => {
    const eventID = String(sandboxField(item, 'event_id', 'EventID') || '');
    const promotionID = String(sandboxField(item, 'promotion_id', 'PromotionID') || '');
    const gate = String(sandboxField(item, 'gate_status', 'GateStatus') || '-');
    const human = String(sandboxField(item, 'human_approval_status', 'HumanApprovalStatus') || '-');
    const postApply = String(sandboxField(item, 'post_apply_verification', 'PostApplyVerification') || '');
    const reason = String(sandboxField(item, 'reason', 'Reason') || '');
    const gateClass = gate === 'promotion_applied' || gate === 'rollback_executed' ? 'running' : (gate === 'reject' ? 'error' : 'offline');
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td>' + esc(ftime(sandboxField(item, 'created_at', 'CreatedAt'))) + '</td>' +
      '<td class="code">' + esc(short(eventID || '-', 42)) + '</td>' +
      '<td class="code">' + esc(short(promotionID || '-', 42)) + '</td>' +
      '<td><span class="badge ' + stateClass(gateClass) + '">' + esc(gate) + '</span></td>' +
      '<td>' + esc(human) + '</td>' +
      '<td>' + esc(postApply || '-') + '</td>' +
      '<td>' + esc(short(reason || '-', 140)) + '</td>';
    body.appendChild(tr);
  });
  renderSandboxGateLogResult();
}

function renderSandboxGateLogResult() {
  const el = document.getElementById('sandboxGateLogResult');
  if (!el) return;
  const fetchError = String(state.ops.sandboxFetchError || '');
  if (fetchError) {
    el.textContent = 'sandbox promotion gate logs unavailable: ' + fetchError + '\nblocked: promotion apply state unreadable';
    return;
  }
  const logs = Array.isArray(state.ops.sandboxGateLogs) ? state.ops.sandboxGateLogs : [];
  const needsReview = logs.filter((item) => {
    const status = String(sandboxField(item, 'gate_status', 'GateStatus') || '');
    return status === 'needs_review' || status === 'needs_more_tests';
  }).length;
  const applied = logs.filter((item) => String(sandboxField(item, 'gate_status', 'GateStatus') || '') === 'promotion_applied').length;
  const rollback = logs.filter((item) => String(sandboxField(item, 'gate_status', 'GateStatus') || '') === 'rollback_executed').length;
  const verified = logs.filter((item) => String(sandboxField(item, 'post_apply_verification', 'PostApplyVerification') || '').trim() !== '').length;
  const blocked = applied === 0 ? ' / blocked: no promotion applied' : '';
  el.textContent = 'sandbox promotion gate logs: ' + String(logs.length) + ' total / ' + String(needsReview) + ' needs-review / ' + String(applied) + ' applied / ' + String(rollback) + ' rollback / ' + String(verified) + ' post-apply evidence / formal apply requires human approval' + blocked;
}

function formatSandboxPromotionDiffPreview(preview) {
  if (!preview || preview.status === 'failed') {
    return 'sandbox promotion diff preview:\n' + JSON.stringify(preview || {}, null, 2);
  }
  const item = preview.preview || preview;
  const files = Array.isArray(sandboxField(item, 'files', 'Files')) ? sandboxField(item, 'files', 'Files') : [];
  const fileCount = sandboxField(item, 'file_count', 'FileCount');
  const added = sandboxField(item, 'added_lines', 'AddedLines');
  const removed = sandboxField(item, 'removed_lines', 'RemovedLines');
  const riskFlags = sandboxDiffRiskFlags(item);
  const manualReview = Boolean(sandboxField(item, 'requires_manual_review', 'RequiresManualReview'));
  const lines = [
    'sandbox promotion diff preview:',
    'status: ' + String(sandboxField(item, 'status', 'Status') || '-'),
    'files: ' + String(fileCount == null ? files.length : fileCount) + ' added: ' + String(added == null ? '-' : added) + ' removed: ' + String(removed == null ? '-' : removed),
    'manual review: ' + String(manualReview ? 'required' : 'not required'),
    'risk flags: ' + (riskFlags.length ? riskFlags.join(', ') : '-'),
  ];
  files.slice(0, 6).forEach((file, idx) => {
    const fileRiskFlags = sandboxDiffRiskFlags(file);
    const fileManualReview = Boolean(sandboxField(file, 'requires_manual_review', 'RequiresManualReview'));
    lines.push('');
    lines.push('file ' + String(idx + 1) + ': ' + String(sandboxField(file, 'path', 'Path') || '-'));
    lines.push('added: ' + String(sandboxField(file, 'added_lines', 'AddedLines') || 0) + ' removed: ' + String(sandboxField(file, 'removed_lines', 'RemovedLines') || 0) + ' hunks: ' + String(sandboxField(file, 'hunk_count', 'HunkCount') || 0));
    if (fileManualReview || fileRiskFlags.length) {
      lines.push('review: required risk flags: ' + (fileRiskFlags.length ? fileRiskFlags.join(', ') : '-'));
    }
    const hunks = Array.isArray(sandboxField(file, 'hunks', 'Hunks')) ? sandboxField(file, 'hunks', 'Hunks') : [];
    hunks.slice(0, 2).forEach((hunk) => {
      lines.push('@@ -' + String(sandboxField(hunk, 'old_start', 'OldStart') || 0) + ',' + String(sandboxField(hunk, 'old_count', 'OldCount') || 0) + ' +' + String(sandboxField(hunk, 'new_start', 'NewStart') || 0) + ',' + String(sandboxField(hunk, 'new_count', 'NewCount') || 0) + ' @@');
      lines.push(twoColumnDiffRows(sandboxField(hunk, 'rows', 'Rows') || [], 58, 18));
    });
  });
  if (files.length > 6) lines.push('\n... +' + String(files.length - 6) + ' more files');
  return lines.join('\n');
}

function sandboxDiffRiskFlags(item) {
  const flags = sandboxField(item, 'risk_flags', 'RiskFlags');
  return Array.isArray(flags) ? flags.map((flag) => String(flag)).filter(Boolean) : [];
}

function twoColumnDiffRows(rows, width, maxRows) {
  const items = Array.isArray(rows) ? rows.slice(0, maxRows || 18) : [];
  const out = [
    padPreviewCell('old', width) + ' | ' + padPreviewCell('new', width),
    repeatChar('-', width) + '-+-' + repeatChar('-', width),
  ];
  items.forEach((row) => {
    const op = String(sandboxField(row, 'op', 'Op') || '');
    const oldNo = sandboxField(row, 'old_line', 'OldLine');
    const newNo = sandboxField(row, 'new_line', 'NewLine');
    const oldText = String(sandboxField(row, 'old_text', 'OldText') || '');
    const newText = String(sandboxField(row, 'new_text', 'NewText') || '');
    const left = (oldNo ? String(oldNo).padStart(4, ' ') + ' ' : '     ') + (op === 'removed' ? '- ' : '  ') + oldText;
    const right = (newNo ? String(newNo).padStart(4, ' ') + ' ' : '     ') + (op === 'added' ? '+ ' : '  ') + newText;
    out.push(padPreviewCell(left, width) + ' | ' + padPreviewCell(right, width));
  });
  if (Array.isArray(rows) && rows.length > items.length) {
    out.push('... +' + String(rows.length - items.length) + ' more rows');
  }
  return out.join('\n');
}

function skillGovernanceOpsCard() {
  const fetchError = String(state.ops.skillGovernanceFetchError || '');
  if (fetchError) {
    return {
      title: 'Skill Governance',
      big: 'unavailable',
      sub: 'skill governance status unavailable: ' + fetchError + '\nblocked: external PR audit state unreadable',
    };
  }
  const manifests = Array.isArray(state.ops.skillManifests) ? state.ops.skillManifests : [];
  const triggers = Array.isArray(state.ops.skillTriggerLogs) ? state.ops.skillTriggerLogs : [];
  const contributions = Array.isArray(state.ops.contributionGateLogs) ? state.ops.contributionGateLogs : [];
  const prSubmits = Array.isArray(state.ops.skillExternalPRSubmitRecords) ? state.ops.skillExternalPRSubmitRecords : [];
  const transcripts = Array.isArray(state.ops.coderTranscripts) ? state.ops.coderTranscripts : [];
  const blocked = contributions.filter((item) => String(sandboxField(item, 'gate_status', 'GateStatus') || '') === 'blocked').length;
  const blockedPRSubmits = prSubmits.filter((item) => String(sandboxField(item, 'submit_status', 'SubmitStatus') || '') === 'blocked').length;
  const missed = triggers.filter((item) => String(sandboxField(item, 'status', 'Status') || '') === 'missed').length;
  const latest = triggers[0] || null;
  const warning = missed > 0 ? '\nWARNING: skill_trigger_missed requires review' : '';
  const prAdapter = state.ops.skillExternalPRAdapter || 'unconfigured';
  const prAdapterConfigured = state.ops.skillExternalPRAdapterConfigured === true;
  const prApproval = state.ops.skillExternalPRHumanApprovalRequired !== false;
  return {
    title: 'Skill Governance',
    big: String(triggers.length) + '/' + String(manifests.length),
    sub: manifests.length || triggers.length || contributions.length || prSubmits.length || transcripts.length ? ('missed triggers: ' + String(missed) + '\nblocked contributions: ' + String(blocked) + '\nexternal PR adapter: ' + String(prAdapter) + ' / configured: ' + (prAdapterConfigured ? 'yes' : 'no') + ' / human approval: ' + (prApproval ? 'required' : 'missing') + '\nexternal PR audits: ' + String(prSubmits.length) + ' / blocked: ' + String(blockedPRSubmits) + '\ncoder transcripts: ' + String(transcripts.length) + '\nlatest skill: ' + String(sandboxField(latest, 'skill_id', 'SkillID') || '-') + warning) : 'skill manifest なし',
  };
}

function renderSkillExternalPRAudits() {
  const body = document.getElementById('skillExternalPRAuditBody');
  if (!body) return;
  body.innerHTML = '';
  const fetchError = String(state.ops.skillGovernanceFetchError || '');
  if (fetchError) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="9" class="small">Skill external PR submit audits unavailable: ' + esc(fetchError) + '</td>';
    body.appendChild(tr);
    renderSkillExternalPRAuditResult();
    return;
  }
  const records = Array.isArray(state.ops.skillExternalPRSubmitRecords) ? state.ops.skillExternalPRSubmitRecords : [];
  if (records.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="9" class="small">No skill external PR submit audits yet</td>';
    body.appendChild(tr);
    renderSkillExternalPRAuditResult();
    return;
  }
  records.slice(0, 20).forEach((item) => {
    const submitID = String(sandboxField(item, 'submit_id', 'SubmitID') || '');
    const eventID = String(sandboxField(item, 'contribution_event_id', 'ContributionEventID') || '');
    const repo = String(sandboxField(item, 'repo', 'Repo') || '-');
    const branch = String(sandboxField(item, 'target_branch', 'TargetBranch') || '-');
    const status = String(sandboxField(item, 'submit_status', 'SubmitStatus') || '-');
    const adapter = String(sandboxField(item, 'pr_adapter', 'PRAdapter') || 'unconfigured');
    const prURL = String(sandboxField(item, 'pr_url', 'PRURL') || '');
    const created = Boolean(sandboxField(item, 'external_pr_created', 'ExternalPRCreated'));
    const verified = Boolean(sandboxField(item, 'post_submit_verified', 'PostSubmitVerified'));
    const evidence = sandboxField(item, 'post_submit_evidence', 'PostSubmitEvidence') || sandboxField(item, 'failure_reason', 'FailureReason') || '';
    const statusClass = created && verified ? 'running' : (status === 'failed' ? 'error' : 'offline');
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td>' + esc(ftime(sandboxField(item, 'created_at', 'CreatedAt'))) + '</td>' +
      '<td class="code">' + esc(submitID || '-') + '</td>' +
      '<td class="code">' + esc(short(eventID || '-', 42)) + '</td>' +
      '<td>' + esc(repo) + '</td>' +
      '<td>' + esc(branch) + '</td>' +
      '<td><span class="badge ' + stateClass(statusClass) + '">' + esc(status) + '</span></td>' +
      '<td>' + esc(adapter) + '</td>' +
      '<td>' + esc(prURL || (created ? 'PR URL missing' : 'not created')) + '</td>' +
      '<td>' + esc(short(evidence || (created ? 'post-submit evidence missing' : 'not submitted'), 120)) + '</td>';
    body.appendChild(tr);
  });
  renderSkillExternalPRAuditResult();
}

function renderSkillExternalPRAuditResult() {
  const el = document.getElementById('skillExternalPRAuditResult');
  if (!el) return;
  const fetchError = String(state.ops.skillGovernanceFetchError || '');
  if (fetchError) {
    el.textContent = 'skill external PR submit audits unavailable: ' + fetchError + '\nblocked: external PR audit state unreadable';
    return;
  }
  const records = Array.isArray(state.ops.skillExternalPRSubmitRecords) ? state.ops.skillExternalPRSubmitRecords : [];
  const blocked = records.filter((item) => String(sandboxField(item, 'submit_status', 'SubmitStatus') || '') === 'blocked').length;
  const created = records.filter((item) => Boolean(sandboxField(item, 'external_pr_created', 'ExternalPRCreated'))).length;
  const verified = records.filter((item) => Boolean(sandboxField(item, 'post_submit_verified', 'PostSubmitVerified')) && String(sandboxField(item, 'post_submit_evidence', 'PostSubmitEvidence') || '').trim() !== '').length;
  const notCreated = records.length - created;
  const adapter = String(state.ops.skillExternalPRAdapter || 'unconfigured');
  const configured = state.ops.skillExternalPRAdapterConfigured === true;
  const approval = state.ops.skillExternalPRHumanApprovalRequired !== false;
  const blockedText = created === 0 ? '\nblocked: no external PR created' : '';
  el.textContent = 'skill external PR submit audits: ' + String(records.length) + ' total / ' + String(blocked) + ' blocked / ' + String(created) + ' created / ' + String(notCreated) + ' not created / ' + String(verified) + ' verified\nexternal PR adapter: ' + adapter + ' / configured: ' + (configured ? 'yes' : 'no') + ' / human approval: ' + (approval ? 'required' : 'missing') + blockedText;
}

function renderSkillEvidenceAudits() {
  const body = document.getElementById('skillEvidenceAuditBody');
  if (!body) return;
  body.innerHTML = '';
  const fetchError = String(state.ops.skillGovernanceFetchError || '');
  if (fetchError) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="6" class="small">Skill evidence audits unavailable: ' + esc(fetchError) + '</td>';
    body.appendChild(tr);
    renderSkillEvidenceAuditResult();
    return;
  }
  const rows = skillEvidenceAuditRows();
  if (rows.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="6" class="small">No skill evidence records yet</td>';
    body.appendChild(tr);
    renderSkillEvidenceAuditResult();
    return;
  }
  rows.slice(0, 20).forEach((item) => {
    const evidenceClass = item.evidenceOK ? 'running' : 'warning';
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td>' + esc(ftime(item.time)) + '</td>' +
      '<td>' + esc(item.kind) + '</td>' +
      '<td class="code">' + esc(short(item.id || '-', 48)) + '</td>' +
      '<td>' + esc(short(item.target || '-', 64)) + '</td>' +
      '<td><span class="badge ' + stateClass(item.status) + '">' + esc(item.status || '-') + '</span></td>' +
      '<td><span class="badge ' + stateClass(evidenceClass) + '">' + esc(short(item.evidence || '-', 140)) + '</span></td>';
    body.appendChild(tr);
  });
  renderSkillEvidenceAuditResult();
}

function skillEvidenceAuditRows() {
  const triggers = Array.isArray(state.ops.skillTriggerLogs) ? state.ops.skillTriggerLogs : [];
  const contributions = Array.isArray(state.ops.contributionGateLogs) ? state.ops.contributionGateLogs : [];
  const transcripts = Array.isArray(state.ops.coderTranscripts) ? state.ops.coderTranscripts : [];
  const rows = [];
  triggers.forEach((item) => {
    rows.push({
      time: sandboxField(item, 'created_at', 'CreatedAt'),
      kind: 'trigger',
      id: sandboxField(item, 'event_id', 'EventID') || '',
      target: sandboxField(item, 'skill_id', 'SkillID') || '',
      status: String(sandboxField(item, 'status', 'Status') || ''),
      evidenceOK: String(sandboxField(item, 'status', 'Status') || '') === 'triggered',
      evidence: sandboxField(item, 'trigger_reason', 'TriggerReason') || sandboxField(item, 'trigger_type', 'TriggerType') || '',
    });
  });
  contributions.forEach((item) => {
    const testResult = String(sandboxField(item, 'test_result', 'TestResult') || '');
    const approved = Boolean(sandboxField(item, 'diff_human_approved', 'DiffHumanApproved'));
    rows.push({
      time: sandboxField(item, 'created_at', 'CreatedAt'),
      kind: 'contribution_gate',
      id: sandboxField(item, 'event_id', 'EventID') || '',
      target: sandboxField(item, 'repo', 'Repo') || '',
      status: String(sandboxField(item, 'gate_status', 'GateStatus') || ''),
      evidenceOK: testResult.trim() !== '' && approved,
      evidence: testResult.trim() !== '' ? testResult : 'missing test result',
    });
  });
  transcripts.forEach((item) => {
    const segment = String(sandboxField(item, 'segment', 'Segment') || '');
    const evidencePath = String(sandboxField(item, 'evidence_path', 'EvidencePath') || '');
    const legacyDiffPath = String(sandboxField(item, 'skill_diff_path', 'SkillDiffPath') || sandboxField(item, 'diff_path', 'DiffPath') || '');
    const legacyTranscriptPath = String(sandboxField(item, 'agent_transcript_path', 'AgentTranscriptPath') || sandboxField(item, 'transcript_path', 'TranscriptPath') || '');
    rows.push({
      time: sandboxField(item, 'created_at', 'CreatedAt'),
      kind: 'coder_transcript',
      id: sandboxField(item, 'event_id', 'EventID') || sandboxField(item, 'entry_id', 'EntryID') || sandboxField(item, 'job_id', 'JobID') || '',
      target: sandboxField(item, 'skill_id', 'SkillID') || sandboxField(item, 'job_id', 'JobID') || '',
      status: String(sandboxField(item, 'status', 'Status') || segment || 'recorded'),
      evidenceOK: evidencePath.trim() !== '' || (legacyDiffPath.trim() !== '' && legacyTranscriptPath.trim() !== ''),
      evidence: evidencePath || [legacyDiffPath || 'missing skill_diff_path', legacyTranscriptPath || 'missing agent_transcript_path'].join(' / '),
    });
  });
  return rows;
}

function countCoderTranscriptEvidencePairs(transcripts) {
  const groups = new Map();
  transcripts.forEach((item) => {
    const key = String(sandboxField(item, 'job_id', 'JobID') || sandboxField(item, 'session_id', 'SessionID') || sandboxField(item, 'entry_id', 'EntryID') || '');
    if (!key) return;
    const segment = String(sandboxField(item, 'segment', 'Segment') || '');
    const evidencePath = String(sandboxField(item, 'evidence_path', 'EvidencePath') || '');
    const legacyDiffPath = String(sandboxField(item, 'skill_diff_path', 'SkillDiffPath') || sandboxField(item, 'diff_path', 'DiffPath') || '');
    const legacyTranscriptPath = String(sandboxField(item, 'agent_transcript_path', 'AgentTranscriptPath') || sandboxField(item, 'transcript_path', 'TranscriptPath') || '');
    if (!groups.has(key)) groups.set(key, {diff: false, transcript: false});
    const group = groups.get(key);
    if ((segment === 'patch_evidence' && evidencePath.trim() !== '') || legacyDiffPath.trim() !== '') group.diff = true;
    if ((segment === 'transcript_evidence' && evidencePath.trim() !== '') || legacyTranscriptPath.trim() !== '') group.transcript = true;
  });
  let count = 0;
  groups.forEach((group) => {
    if (group.diff && group.transcript) count++;
  });
  return count;
}

function renderSkillEvidenceAuditResult() {
  const el = document.getElementById('skillEvidenceAuditResult');
  if (!el) return;
  const fetchError = String(state.ops.skillGovernanceFetchError || '');
  if (fetchError) {
    el.textContent = 'skill evidence audits unavailable: ' + fetchError + '\nblocked: coder evidence state unreadable';
    return;
  }
  const triggers = Array.isArray(state.ops.skillTriggerLogs) ? state.ops.skillTriggerLogs : [];
  const contributions = Array.isArray(state.ops.contributionGateLogs) ? state.ops.contributionGateLogs : [];
  const transcripts = Array.isArray(state.ops.coderTranscripts) ? state.ops.coderTranscripts : [];
  const triggered = triggers.filter((item) => String(sandboxField(item, 'status', 'Status') || '') === 'triggered').length;
  const passed = contributions.filter((item) => String(sandboxField(item, 'gate_status', 'GateStatus') || '') === 'passed').length;
  const transcriptWithEvidence = countCoderTranscriptEvidencePairs(transcripts);
  const blocked = [];
  if (transcripts.length === 0) blocked.push('blocked: coder evidence transcript not observed');
  if (passed > 0) blocked.push('blocked: passed contribution gate is not external PR evidence');
  el.textContent = 'skill evidence audits: ' + String(triggers.length) + ' triggers / ' + String(triggered) + ' triggered / ' + String(contributions.length) + ' contribution gates / ' + String(passed) + ' passed / ' + String(transcripts.length) + ' coder transcripts / ' + String(transcriptWithEvidence) + ' with diff+transcript evidence' + (blocked.length ? '\n' + blocked.join('\n') : '');
}

function renderSuperAgentTerminalAudits() {
  const body = document.getElementById('superAgentTerminalAuditBody');
  if (!body) return;
  body.innerHTML = '';
  const fetchError = String(state.ops.superAgentFetchError || '');
  if (fetchError) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="6" class="small">SuperAgent terminal audits unavailable: ' + esc(fetchError) + '</td>';
    body.appendChild(tr);
    renderSuperAgentTerminalAuditResult();
    return;
  }
  const runs = Array.isArray(state.ops.superAgentRuns) ? state.ops.superAgentRuns : [];
  const queue = Array.isArray(state.ops.superAgentRunQueue) ? state.ops.superAgentRunQueue : [];
  const rows = [];
  runs.forEach((item) => {
    const status = String(sandboxField(item, 'status', 'Status') || '');
    if (status !== 'completed' && status !== 'failed' && status !== 'cancelled' && status !== 'paused') return;
    rows.push({
      time: sandboxField(item, 'completed_at', 'CompletedAt') || sandboxField(item, 'started_at', 'StartedAt'),
      kind: 'agent_run',
      id: sandboxField(item, 'run_id', 'RunID') || '',
      run: sandboxField(item, 'run_id', 'RunID') || '',
      status,
      evidence: sandboxField(item, 'summary', 'Summary') || '',
    });
  });
  queue.forEach((item) => {
    const status = String(sandboxField(item, 'status', 'Status') || '');
    if (status !== 'completed' && status !== 'failed' && status !== 'cancelled') return;
    rows.push({
      time: sandboxField(item, 'completed_at', 'CompletedAt') || sandboxField(item, 'claimed_at', 'ClaimedAt') || sandboxField(item, 'created_at', 'CreatedAt'),
      kind: 'run_queue',
      id: sandboxField(item, 'queue_id', 'QueueID') || '',
      run: sandboxField(item, 'run_id', 'RunID') || '',
      status,
      evidence: sandboxField(item, 'reason', 'Reason') || '',
    });
  });
  if (rows.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="6" class="small">No SuperAgent terminal records yet</td>';
    body.appendChild(tr);
    renderSuperAgentTerminalAuditResult();
    return;
  }
  rows.slice(0, 20).forEach((item) => {
    const evidence = String(item.evidence || '');
    const evidenceClass = evidence.trim() ? 'running' : 'error';
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td>' + esc(ftime(item.time)) + '</td>' +
      '<td>' + esc(item.kind) + '</td>' +
      '<td class="code">' + esc(short(item.id || '-', 48)) + '</td>' +
      '<td class="code">' + esc(short(item.run || '-', 48)) + '</td>' +
      '<td><span class="badge ' + stateClass(item.status) + '">' + esc(item.status) + '</span></td>' +
      '<td><span class="badge ' + stateClass(evidenceClass) + '">' + esc(evidence.trim() ? short(evidence, 140) : 'missing evidence') + '</span></td>';
    body.appendChild(tr);
  });
  renderSuperAgentTerminalAuditResult();
}

function renderSuperAgentTerminalAuditResult() {
  const el = document.getElementById('superAgentTerminalAuditResult');
  if (!el) return;
  const fetchError = String(state.ops.superAgentFetchError || '');
  if (fetchError) {
    el.textContent = 'superagent terminal audits unavailable: ' + fetchError + '\nblocked: scheduler terminal state unreadable';
    return;
  }
  const runs = Array.isArray(state.ops.superAgentRuns) ? state.ops.superAgentRuns : [];
  const queue = Array.isArray(state.ops.superAgentRunQueue) ? state.ops.superAgentRunQueue : [];
  const terminalRuns = runs.filter((item) => {
    const status = String(sandboxField(item, 'status', 'Status') || '');
    return status === 'completed' || status === 'failed' || status === 'cancelled' || status === 'paused';
  });
  const terminalQueue = queue.filter((item) => {
    const status = String(sandboxField(item, 'status', 'Status') || '');
    return status === 'completed' || status === 'failed' || status === 'cancelled';
  });
  const failedRuns = terminalRuns.filter((item) => String(sandboxField(item, 'status', 'Status') || '') === 'failed').length;
  const failedQueues = terminalQueue.filter((item) => String(sandboxField(item, 'status', 'Status') || '') === 'failed').length;
  const missingRunSummary = terminalRuns.filter((item) => String(sandboxField(item, 'status', 'Status') || '') === 'failed' && String(sandboxField(item, 'summary', 'Summary') || '').trim() === '').length;
  const missingQueueReason = terminalQueue.filter((item) => String(sandboxField(item, 'status', 'Status') || '') === 'failed' && String(sandboxField(item, 'reason', 'Reason') || '').trim() === '').length;
  el.textContent = 'superagent terminal audits: ' + String(terminalRuns.length) + ' terminal runs / ' + String(terminalQueue.length) + ' terminal queue / ' + String(failedRuns) + ' failed runs / ' + String(failedQueues) + ' failed queue / missing evidence: ' + String(missingRunSummary + missingQueueReason);
}

function renderSuperAgentResumeAudits() {
  const body = document.getElementById('superAgentResumeAuditBody');
  if (!body) return;
  body.innerHTML = '';
  const fetchError = String(state.ops.superAgentFetchError || '');
  if (fetchError) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="7" class="small">SuperAgent resume audits unavailable: ' + esc(fetchError) + '</td>';
    body.appendChild(tr);
    renderSuperAgentResumeAuditResult();
    return;
  }
  const rows = superAgentResumeAuditRows();
  if (rows.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="7" class="small">No SuperAgent resume records yet</td>';
    body.appendChild(tr);
    renderSuperAgentResumeAuditResult();
    return;
  }
  rows.slice(0, 20).forEach((item) => {
    const tr = document.createElement('tr');
    const traceText = 'paused:' + String(item.paused) + ' resumed:' + String(item.resumed);
    const controlText = 'runtime-control:' + item.runtimeControlActions.join(',');
    const evidenceClass = item.manualLedger ? 'warning' : (item.resumed > 0 && item.paused > 0 && item.runtimeControlApplied ? 'running' : 'error');
    tr.innerHTML =
      '<td>' + esc(ftime(item.time)) + '</td>' +
      '<td class="code">' + esc(short(item.queueID || '-', 48)) + '</td>' +
      '<td class="code">' + esc(short(item.runID || '-', 48)) + '</td>' +
      '<td><span class="badge ' + stateClass(item.status) + '">' + esc(item.status || '-') + '</span></td>' +
      '<td><span class="badge ' + stateClass(item.paused > 0 && item.resumed > 0 ? 'running' : 'error') + '">' + esc(traceText) + '</span></td>' +
      '<td><span class="badge ' + stateClass(item.runtimeControlApplied ? 'running' : 'warning') + '">' + esc(short(controlText, 140)) + '</span></td>' +
      '<td><span class="badge ' + stateClass(evidenceClass) + '">' + esc(short(item.evidence || '-', 140)) + '</span></td>';
    body.appendChild(tr);
  });
  renderSuperAgentResumeAuditResult();
}

function superAgentResumeAuditRows() {
  const queue = Array.isArray(state.ops.superAgentRunQueue) ? state.ops.superAgentRunQueue : [];
  const traces = Array.isArray(state.ops.superAgentTraceEvents) ? state.ops.superAgentTraceEvents : [];
  return queue.filter((item) => String(sandboxField(item, 'action', 'Action') || '') === 'resume').map((item) => {
    const runID = sandboxField(item, 'run_id', 'RunID') || '';
    const related = traces.filter((ev) => String(sandboxField(ev, 'run_id', 'RunID') || '') === String(runID));
    const paused = related.filter((ev) => String(sandboxField(ev, 'event_type', 'EventType') || '') === 'lead_agent_paused').length;
    const resumed = related.filter((ev) => String(sandboxField(ev, 'event_type', 'EventType') || '') === 'lead_agent_resumed').length;
    const reason = String(sandboxField(item, 'reason', 'Reason') || '');
    const manualLedger = /manual ledger|without scheduler execution|scheduler execution not used/i.test(reason);
    const runtimeControlActions = superAgentResumeRuntimeControlActions(related);
    const runtimeControlApplied = runtimeControlActions.some((action) => action !== 'none');
    return {
      time: sandboxField(item, 'completed_at', 'CompletedAt') || sandboxField(item, 'claimed_at', 'ClaimedAt') || sandboxField(item, 'created_at', 'CreatedAt'),
      queueID: sandboxField(item, 'queue_id', 'QueueID') || '',
      runID,
      status: String(sandboxField(item, 'status', 'Status') || ''),
      paused,
      resumed,
      manualLedger,
      runtimeControlActions,
      runtimeControlApplied,
      evidence: manualLedger ? 'manual-ledger only; runtime control not applied; true long-running resume not verified' : (reason || 'missing resume evidence'),
    };
  });
}

function superAgentResumeRuntimeControlActions(events) {
  const actions = [];
  (Array.isArray(events) ? events : []).forEach((ev) => {
    const summary = String(sandboxField(ev, 'payload_summary', 'PayloadSummary') || '');
    const match = summary.match(/runtime_control=([^\s;]+)/);
    if (!match || !match[1]) return;
    if (!actions.includes(match[1])) actions.push(match[1]);
  });
  return actions.length ? actions : ['none'];
}

function renderSuperAgentResumeAuditResult() {
  const el = document.getElementById('superAgentResumeAuditResult');
  if (!el) return;
  const fetchError = String(state.ops.superAgentFetchError || '');
  if (fetchError) {
    el.textContent = 'superagent resume audits unavailable: ' + fetchError + '\nblocked: true long-running resume state unreadable';
    return;
  }
  const rows = superAgentResumeAuditRows();
  const manual = rows.filter((item) => item.manualLedger).length;
  const completed = rows.filter((item) => item.status === 'completed').length;
  const withPauseResume = rows.filter((item) => item.paused > 0 && item.resumed > 0).length;
  const runtimeControlApplied = rows.filter((item) => item.runtimeControlApplied).length;
  el.textContent = 'superagent resume audits: ' + String(rows.length) + ' resume queue / ' + String(completed) + ' completed / ' + String(manual) + ' manual-ledger / ' + String(withPauseResume) + ' pause-resume trace / ' + String(runtimeControlApplied) + ' runtime-control applied\nblocked: true long-running resume not verified';
}

function renderAIWorkflowRunEvidence() {
  const body = document.getElementById('aiWorkflowRunEvidenceBody');
  if (!body) return;
  body.innerHTML = '';
  const fetchError = String(state.ops.aiWorkflowFetchError || '');
  if (fetchError) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="6" class="small">AI Workflow run evidence unavailable: ' + esc(fetchError) + '</td>';
    body.appendChild(tr);
    renderAIWorkflowRunEvidenceResult([]);
    return;
  }
  const rows = aiWorkflowRunEvidenceRows();
  if (rows.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="6" class="small">No AI Workflow run evidence yet</td>';
    body.appendChild(tr);
    renderAIWorkflowRunEvidenceResult(rows);
    return;
  }
  rows.slice(0, 20).forEach((item) => {
    const statusClass = item.hasCommand && item.hasContext && item.hasTrace ? 'running' : 'offline';
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td class="code">' + esc(short(item.runID || '-', 52)) + '</td>' +
      '<td class="code">' + esc(short(item.workstreamID || '-', 42)) + '</td>' +
      '<td>' + esc(item.hasCommand ? 'present' : 'missing') + '</td>' +
      '<td>' + esc(item.hasContext ? 'present' : 'missing') + '</td>' +
      '<td>' + esc(item.hasTrace ? 'present' : 'missing') + '</td>' +
      '<td><span class="badge ' + stateClass(statusClass) + '">' + esc(item.status) + '</span></td>';
    body.appendChild(tr);
  });
  renderAIWorkflowRunEvidenceResult(rows);
}

function aiWorkflowRunEvidenceRows() {
  const events = Array.isArray(state.ops.aiWorkflowEvents) ? state.ops.aiWorkflowEvents : [];
  const contexts = Array.isArray(state.ops.aiWorkflowContextUsages) ? state.ops.aiWorkflowContextUsages : [];
  const traces = Array.isArray(state.ops.superAgentTraceEvents) ? state.ops.superAgentTraceEvents : [];
  const byRun = new Map();
  const ensure = (runID, workstreamID) => {
    const id = String(runID || '');
    if (!id) return null;
    if (!byRun.has(id)) {
      byRun.set(id, {runID: id, workstreamID: String(workstreamID || ''), hasCommand: false, hasContext: false, hasTrace: false, status: 'incomplete'});
    }
    const row = byRun.get(id);
    if (!row.workstreamID && workstreamID) row.workstreamID = String(workstreamID);
    return row;
  };
  events.forEach((item) => {
    const row = ensure(sandboxField(item, 'run_id', 'RunID'), sandboxField(item, 'workstream_id', 'WorkstreamID'));
    if (!row) return;
    if (String(sandboxField(item, 'event_type', 'EventType') || '') === 'command_invoked') row.hasCommand = true;
  });
  contexts.forEach((item) => {
    const row = ensure(sandboxField(item, 'run_id', 'RunID'), sandboxField(item, 'workstream_id', 'WorkstreamID'));
    if (!row) return;
    row.hasContext = true;
  });
  traces.forEach((item) => {
    const row = ensure(sandboxField(item, 'run_id', 'RunID'), '');
    if (!row) return;
    row.hasTrace = true;
  });
  const out = Array.from(byRun.values());
  out.forEach((item) => {
    item.status = item.hasCommand && item.hasContext && item.hasTrace ? 'same-run evidence' : 'partial evidence';
  });
  return out;
}

function renderAIWorkflowRunEvidenceResult(rows) {
  const el = document.getElementById('aiWorkflowRunEvidenceResult');
  if (!el) return;
  const fetchError = String(state.ops.aiWorkflowFetchError || '');
  if (fetchError) {
    el.textContent = 'ai workflow run evidence unavailable: ' + fetchError + '\nblocked: scheduler normal completion state unreadable';
    return;
  }
  const list = Array.isArray(rows) ? rows : aiWorkflowRunEvidenceRows();
  const sameRun = list.filter((item) => item.hasCommand && item.hasContext && item.hasTrace).length;
  const partial = list.length - sameRun;
  el.textContent = 'ai workflow run evidence: ' + String(list.length) + ' runs / ' + String(sameRun) + ' command-context-trace same-run / ' + String(partial) + ' partial\nblocked: scheduler normal completion not verified';
}

function renderComplexityReviewArtifacts() {
  const body = document.getElementById('complexityReviewArtifactBody');
  if (!body) return;
  body.innerHTML = '';
  const fetchError = String(state.ops.complexityFetchError || '');
  if (fetchError) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="6" class="small">Complexity review artifacts unavailable: ' + esc(fetchError) + '</td>';
    body.appendChild(tr);
    renderComplexityReviewArtifactResult([]);
    return;
  }
  const rows = complexityReviewArtifactRows();
  if (rows.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="6" class="small">No complexity review artifacts yet</td>';
    body.appendChild(tr);
    renderComplexityReviewArtifactResult(rows);
    return;
  }
  rows.slice(0, 20).forEach((item) => {
    const patch = item.patchApplied ? 'applied' : 'not applied';
    const approval = item.humanApprovalRequired ? 'required' : 'missing';
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td>' + esc(ftime(item.createdAt)) + '</td>' +
      '<td class="code">' + esc(short(item.artifactID || '-', 48)) + '</td>' +
      '<td>' + esc(short(item.artifactType || '-', 52)) + '</td>' +
      '<td><span class="badge ' + stateClass(item.status) + '">' + esc(item.status || '-') + '</span></td>' +
      '<td><span class="badge ' + stateClass(item.patchApplied ? 'error' : 'running') + '">' + esc(patch) + '</span></td>' +
      '<td>' + esc(approval) + '</td>';
    body.appendChild(tr);
  });
  renderComplexityReviewArtifactResult(rows);
}

function complexityReviewArtifactRows() {
  const reports = Array.isArray(state.ops.complexityReports) ? state.ops.complexityReports : [];
  return reports.map((item) => {
    const content = String(sandboxField(item, 'content', 'Content') || '');
    const normalized = content.toLowerCase();
    return {
      artifactID: String(sandboxField(item, 'artifact_id', 'ArtifactID') || ''),
      artifactType: String(sandboxField(item, 'artifact_type', 'ArtifactType') || ''),
      status: String(sandboxField(item, 'status', 'Status') || ''),
      createdAt: sandboxField(item, 'created_at', 'CreatedAt'),
      patchApplied: normalized.includes('patch applied: `true`') || normalized.includes('patch applied: true'),
      humanApprovalRequired: normalized.includes('human approval required: `true`') || normalized.includes('human approval required: true'),
    };
  });
}

function renderComplexityReviewArtifactResult(rows) {
  const el = document.getElementById('complexityReviewArtifactResult');
  if (!el) return;
  const fetchError = String(state.ops.complexityFetchError || '');
  if (fetchError) {
    el.textContent = 'complexity review artifacts unavailable: ' + fetchError + '\nblocked: patch apply state unreadable';
    return;
  }
  const list = Array.isArray(rows) ? rows : complexityReviewArtifactRows();
  const pendingReview = list.filter((item) => item.status === 'pending_review').length;
  const failed = list.filter((item) => item.status === 'failed' || item.artifactType === 'complexity_coder_diff_failure').length;
  const patchApplied = list.filter((item) => item.patchApplied).length;
  const approvalRequired = list.filter((item) => item.humanApprovalRequired).length;
  el.textContent = 'complexity review artifacts: ' + String(list.length) + ' total / ' + String(pendingReview) + ' pending-review / ' + String(failed) + ' failed / ' + String(patchApplied) + ' patch applied / ' + String(approvalRequired) + ' human approval required\nmode: review-only blocked: no patch applied';
}

function workstreamOpsCard() {
  const fetchError = String(state.ops.workstreamFetchError || '');
  if (fetchError) {
    return {
      title: 'Workstreams',
      big: 'unavailable',
      sub: 'workstream status unavailable: ' + fetchError + '\nblocked: vault apply state unreadable',
    };
  }
  const workstreams = Array.isArray(state.ops.workstreams) ? state.ops.workstreams : [];
  const goals = Array.isArray(state.ops.workstreamGoals) ? state.ops.workstreamGoals : [];
  const artifacts = Array.isArray(state.ops.workstreamArtifacts) ? state.ops.workstreamArtifacts : [];
  const annotations = Array.isArray(state.ops.workstreamAnnotations) ? state.ops.workstreamAnnotations : [];
  const steering = Array.isArray(state.ops.workstreamSteering) ? state.ops.workstreamSteering : [];
  const heartbeats = Array.isArray(state.ops.workstreamHeartbeats) ? state.ops.workstreamHeartbeats : [];
  const vaultUpdates = latestWorkstreamVaultUpdates(Array.isArray(state.ops.workstreamVaultUpdates) ? state.ops.workstreamVaultUpdates : []);
  const activeGoals = goals.filter((item) => String(sandboxField(item, 'status', 'Status') || '') === 'active').length;
  const waitingGoals = goals.filter((item) => String(sandboxField(item, 'status', 'Status') || '') === 'waiting').length;
  const pendingReviewArtifacts = artifacts.filter((item) => String(sandboxField(item, 'status', 'Status') || '') === 'pending_review').length;
  const activeHeartbeats = heartbeats.filter((item) => String(sandboxField(item, 'status', 'Status') || '') === 'active').length;
  const approvalPending = vaultUpdates.filter((item) => String(sandboxField(item, 'review_status', 'ReviewStatus') || '') === 'pending').length;
  const appliedVaultUpdates = vaultUpdates.filter((item) => Boolean(sandboxField(item, 'applied', 'Applied'))).length;
  const vaultApplyBoundary = appliedVaultUpdates > 0 ? ('vault applied: ' + String(appliedVaultUpdates)) : 'mode: review-only blocked: no vault apply';
  const latest = workstreams[0] || null;
  return {
    title: 'Workstreams',
    big: String(goals.length) + '/' + String(workstreams.length),
    sub: workstreams.length || goals.length || artifacts.length || annotations.length || steering.length || heartbeats.length || vaultUpdates.length ? ('active goals: ' + String(activeGoals) + ' waiting goals: ' + String(waitingGoals) + ' active heartbeats: ' + String(activeHeartbeats) + '\napproval pending: ' + String(approvalPending) + ' vault updates: ' + String(vaultUpdates.length) + '\nartifacts: ' + String(artifacts.length) + ' pending-review: ' + String(pendingReviewArtifacts) + ' annotations: ' + String(annotations.length) + ' steering: ' + String(steering.length) + '\n' + vaultApplyBoundary + '\nlatest: ' + String(sandboxField(latest, 'name', 'Name') || '-')) : 'workstream record なし',
  };
}

function latestWorkstreamVaultUpdates(items) {
  const seen = new Set();
  const out = [];
  items.forEach((item) => {
    const id = String(sandboxField(item, 'update_id', 'UpdateID') || '');
    const key = id || JSON.stringify(item);
    if (seen.has(key)) return;
    seen.add(key);
    out.push(item);
  });
  return out;
}

function renderWorkstreamVaultReviews() {
  const body = document.getElementById('workstreamVaultReviewBody');
  if (!body) return;
  body.innerHTML = '';
  const fetchError = String(state.ops.workstreamFetchError || '');
  if (fetchError) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="8" class="small">Workstream vault reviews unavailable: ' + esc(fetchError) + '</td>';
    body.appendChild(tr);
    renderWorkstreamVaultReviewResult();
    return;
  }
  const updates = latestWorkstreamVaultUpdates(Array.isArray(state.ops.workstreamVaultUpdates) ? state.ops.workstreamVaultUpdates : []);
  if (updates.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="8" class="small">No workstream vault updates yet</td>';
    body.appendChild(tr);
    renderWorkstreamVaultReviewResult();
    return;
  }
  updates.slice(0, 20).forEach((item) => {
    const updateID = String(sandboxField(item, 'update_id', 'UpdateID') || '');
    const review = String(sandboxField(item, 'review_status', 'ReviewStatus') || '-');
    const applied = Boolean(sandboxField(item, 'applied', 'Applied'));
    const appliedPath = String(sandboxField(item, 'applied_path', 'AppliedPath') || '');
    const proposed = String(sandboxField(item, 'proposed_content', 'ProposedContent') || '');
    const pending = review === 'pending';
    const payload = encodeURIComponent(JSON.stringify(item));
    const preview = updateID
      ? '<button class="ctl-btn workstream-vault-preview" type="button" data-update="' + escAttr(payload) + '">Preview</button> '
      : '';
    const actions = preview + (pending && updateID
      ? '<button class="ctl-btn workstream-vault-review" type="button" data-update="' + escAttr(payload) + '" data-review-status="approved">Approve</button> <button class="ctl-btn workstream-vault-review" type="button" data-update="' + escAttr(payload) + '" data-review-status="rejected">Reject</button>'
      : '<span class="small">-</span>');
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td>' + esc(ftime(sandboxField(item, 'created_at', 'CreatedAt'))) + '</td>' +
      '<td class="code">' + esc(updateID || '-') + '</td>' +
      '<td class="code">' + esc(short(sandboxField(item, 'file_path', 'FilePath') || '-', 80)) + '</td>' +
      '<td><span class="badge ' + stateClass(review) + '">' + esc(review) + '</span></td>' +
      '<td><span class="badge ' + stateClass(applied ? 'applied' : 'not-applied') + '">' + esc(applied ? 'applied' : 'not applied') + '</span></td>' +
      '<td class="code">' + esc(appliedPath ? short(appliedPath, 80) : '-') + '</td>' +
      '<td class="code">' + esc(proposed ? short(proposed, 120) : '-') + '</td>' +
      '<td>' + actions + '</td>';
    body.appendChild(tr);
  });
  body.querySelectorAll('.workstream-vault-review').forEach((btn) => {
    btn.addEventListener('click', () => {
      reviewWorkstreamVaultUpdate(btn.getAttribute('data-update') || '', btn.getAttribute('data-review-status') || '');
    });
  });
  body.querySelectorAll('.workstream-vault-preview').forEach((btn) => {
    btn.addEventListener('click', () => {
      previewWorkstreamVaultUpdate(btn.getAttribute('data-update') || '');
    });
  });
  renderWorkstreamVaultReviewResult();
}

function renderWorkstreamVaultReviewResult() {
  const el = document.getElementById('workstreamVaultReviewResult');
  if (!el) return;
  const fetchError = String(state.ops.workstreamFetchError || '');
  if (fetchError) {
    el.textContent = 'workstream vault review unavailable: ' + fetchError + '\nblocked: vault apply state unreadable';
    return;
  }
  const result = state.ops.workstreamVaultReviewResult || null;
  const preview = state.ops.workstreamVaultPreviewResult || null;
  const summary = workstreamVaultReviewSummary();
  if (!result && !preview) {
    el.textContent = summary;
    return;
  }
  el.textContent = summary + '\n\nreview:\n' + (result ? JSON.stringify(result, null, 2) : '-') + (preview ? '\n\npreview:\n' + formatWorkstreamVaultPreview(preview) : '');
}

function workstreamVaultReviewSummary() {
  const updates = latestWorkstreamVaultUpdates(Array.isArray(state.ops.workstreamVaultUpdates) ? state.ops.workstreamVaultUpdates : []);
  const pending = updates.filter((item) => String(sandboxField(item, 'review_status', 'ReviewStatus') || '') === 'pending').length;
  const approved = updates.filter((item) => String(sandboxField(item, 'review_status', 'ReviewStatus') || '') === 'approved').length;
  const rejected = updates.filter((item) => String(sandboxField(item, 'review_status', 'ReviewStatus') || '') === 'rejected').length;
  const applied = updates.filter((item) => Boolean(sandboxField(item, 'applied', 'Applied'))).length;
  const approvedNotApplied = updates.filter((item) => String(sandboxField(item, 'review_status', 'ReviewStatus') || '') === 'approved' && !Boolean(sandboxField(item, 'applied', 'Applied'))).length;
  const lines = [
    'workstream vault review: ' + String(updates.length) + ' total / ' + String(pending) + ' pending / ' + String(approved) + ' approved / ' + String(rejected) + ' rejected / ' + String(applied) + ' applied',
  ];
  if (approvedNotApplied > 0) lines.push('approved not applied: ' + String(approvedNotApplied));
  if (applied === 0) lines.push('blocked: no vault apply');
  return lines.join('\n');
}

function formatWorkstreamVaultPreview(preview) {
  if (!preview || preview.status === 'failed') {
    return JSON.stringify(preview || {}, null, 2);
  }
  const item = preview.preview || preview;
  const current = String(sandboxField(item, 'current_content', 'CurrentContent') || '');
  const proposed = String(sandboxField(item, 'proposed_content', 'ProposedContent') || '');
  const filePath = String(sandboxField(item, 'file_path', 'FilePath') || '-');
  const currentMissing = Boolean(sandboxField(item, 'current_missing', 'CurrentMissing'));
  const added = sandboxField(item, 'added_lines', 'AddedLines');
  const removed = sandboxField(item, 'removed_lines', 'RemovedLines');
  const diff = String(sandboxField(item, 'unified_diff', 'UnifiedDiff') || '');
  return [
    'preview side-by-side:',
    'file: ' + filePath + (currentMissing ? ' (new file)' : ''),
    'added: ' + String(added == null ? '-' : added) + ' removed: ' + String(removed == null ? '-' : removed),
    '',
    twoColumnText('current', current, 'proposed', proposed, 58, 16),
    '',
    'compact unified diff:',
    diff || '-',
  ].join('\n');
}

function twoColumnText(leftTitle, leftText, rightTitle, rightText, width, maxLines) {
  const left = splitPreviewLines(leftText, maxLines);
  const right = splitPreviewLines(rightText, maxLines);
  const count = Math.max(left.length, right.length, 1);
  const border = repeatChar('-', width) + '-+-' + repeatChar('-', width);
  const rows = [
    padPreviewCell(leftTitle, width) + ' | ' + padPreviewCell(rightTitle, width),
    border,
  ];
  for (let i = 0; i < count; i++) {
    rows.push(padPreviewCell(left[i] || '', width) + ' | ' + padPreviewCell(right[i] || '', width));
  }
  return rows.join('\n');
}

function splitPreviewLines(text, maxLines) {
  const lines = String(text || '').split(/\r?\n/);
  const limit = maxLines || 16;
  const out = lines.slice(0, limit).map((line) => line.length > 56 ? line.slice(0, 53) + '...' : line);
  if (lines.length > limit) {
    out.push('... +' + String(lines.length - limit) + ' more lines');
  }
  if (out.length === 0) out.push('');
  return out;
}

function padPreviewCell(text, width) {
  const value = String(text || '');
  if (value.length >= width) return value.slice(0, width);
  return value + repeatChar(' ', width - value.length);
}

function repeatChar(ch, count) {
  return new Array(Math.max(0, count) + 1).join(ch);
}

async function previewWorkstreamVaultUpdate(encodedUpdate) {
  if (!encodedUpdate) return;
  let payload = {};
  try {
    payload = JSON.parse(decodeURIComponent(encodedUpdate));
  } catch (_) {
    state.ops.workstreamVaultPreviewResult = {status: 'failed', error: 'invalid workstream vault update payload'};
    renderWorkstreamVaultReviews();
    return;
  }
  try {
    const res = await fetch('/viewer/workstreams/vault-updates/preview', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(payload),
    });
    const text = await res.text();
    let data = null;
    try {
      data = text ? JSON.parse(text) : {};
    } catch (_) {
      data = {raw: text};
    }
    state.ops.workstreamVaultPreviewResult = res.ok ? data : {status: 'failed', http_status: res.status, response: data};
  } catch (err) {
    state.ops.workstreamVaultPreviewResult = {status: 'failed', error: String(err && err.message ? err.message : err)};
  }
  renderWorkstreamVaultReviews();
}

async function previewSandboxPromotion(encodedPromotion) {
  if (!encodedPromotion) return;
  let payload = {};
  try {
    payload = JSON.parse(decodeURIComponent(encodedPromotion));
  } catch (_) {
    state.ops.sandboxPromotionPreviewResult = {status: 'failed', error: 'invalid sandbox promotion payload'};
    renderSandboxPromotionPreviewResult();
    return;
  }
  try {
    const res = await fetch('/viewer/sandbox/promotions/preview', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(payload),
    });
    const text = await res.text();
    let data = null;
    try {
      data = text ? JSON.parse(text) : {};
    } catch (_) {
      data = {raw: text};
    }
    state.ops.sandboxPromotionPreviewResult = res.ok ? data : {status: 'failed', http_status: res.status, response: data};
  } catch (err) {
    state.ops.sandboxPromotionPreviewResult = {status: 'failed', error: String(err && err.message ? err.message : err)};
  }
  renderSandboxPromotionPreviewResult();
}

async function requestSandboxPromotionManualReview(encodedPromotion) {
  if (!encodedPromotion) return;
  let promotion = {};
  try {
    promotion = JSON.parse(decodeURIComponent(encodedPromotion));
  } catch (_) {
    state.ops.sandboxPromotionManualReviewResult = {status: 'failed', error: 'invalid sandbox promotion payload'};
    renderSandboxPromotionPreviewResult();
    return;
  }
  try {
    const res = await fetch('/viewer/sandbox/promotions/manual-review', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({
        promotion,
        workstream_id: sandboxField(promotion, 'workstream_id', 'WorkstreamID') || '',
      }),
    });
    const text = await res.text();
    let data = null;
    try {
      data = text ? JSON.parse(text) : {};
    } catch (_) {
      data = {raw: text};
    }
    state.ops.sandboxPromotionManualReviewResult = res.ok ? data : {status: 'failed', http_status: res.status, response: data};
    if (res.ok && data && data.preview) {
      state.ops.sandboxPromotionPreviewResult = {preview: data.preview};
    }
  } catch (err) {
    state.ops.sandboxPromotionManualReviewResult = {status: 'failed', error: String(err && err.message ? err.message : err)};
  }
  renderSandboxPromotionPreviewResult();
}

async function reviewWorkstreamVaultUpdate(encodedUpdate, reviewStatus) {
  if (!encodedUpdate || (reviewStatus !== 'approved' && reviewStatus !== 'rejected')) return;
  let payload = {};
  try {
    payload = JSON.parse(decodeURIComponent(encodedUpdate));
  } catch (_) {
    state.ops.workstreamVaultReviewResult = {status: 'failed', error: 'invalid workstream vault update payload'};
    renderWorkstreamVaultReviews();
    return;
  }
  payload.review_status = reviewStatus;
  try {
    const res = await fetch('/viewer/workstreams/vault-updates/review', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(payload),
    });
    const text = await res.text();
    let data = null;
    try {
      data = text ? JSON.parse(text) : {};
    } catch (_) {
      data = {raw: text};
    }
    if (!res.ok) {
      state.ops.workstreamVaultReviewResult = {status: 'failed', http_status: res.status, response: data};
    } else {
      state.ops.workstreamVaultReviewResult = data;
      refreshWorkstreamData();
    }
  } catch (err) {
    state.ops.workstreamVaultReviewResult = {status: 'failed', error: String(err && err.message ? err.message : err)};
  }
  renderWorkstreamVaultReviews();
}

function revenueOpsCard() {
  const fetchError = String(state.ops.revenueFetchError || '');
  if (fetchError) {
    return {
      title: 'Revenue',
      big: 'unavailable',
      sub: 'revenue status unavailable: ' + fetchError + '\nblocked: external send audit state unreadable',
    };
  }
  const market = Array.isArray(state.ops.revenueMarketResearch) ? state.ops.revenueMarketResearch : [];
  const posts = Array.isArray(state.ops.revenueSNSPostMetrics) ? state.ops.revenueSNSPostMetrics : [];
  const products = Array.isArray(state.ops.revenueProducts) ? state.ops.revenueProducts : [];
  const voices = Array.isArray(state.ops.revenueCustomerVoices) ? state.ops.revenueCustomerVoices : [];
  const events = Array.isArray(state.ops.revenueEvents) ? state.ops.revenueEvents : [];
  const decisions = latestRevenueHumanDecisions(Array.isArray(state.ops.revenueHumanDecisions) ? state.ops.revenueHumanDecisions : []);
  const dailyReports = Array.isArray(state.ops.revenueDailyRoutineReports) ? state.ops.revenueDailyRoutineReports : [];
  const channelDrafts = Array.isArray(state.ops.revenueChannelDrafts) ? state.ops.revenueChannelDrafts : [];
  const externalSendApplies = Array.isArray(state.ops.revenueExternalSendApplyRecords) ? state.ops.revenueExternalSendApplyRecords : [];
  const summary = state.ops.revenueSummary && typeof state.ops.revenueSummary === 'object' ? state.ops.revenueSummary : null;
  const paid = events.filter((item) => Number(sandboxField(item, 'amount', 'Amount') || 0) > 0).length;
  const usableVoices = voices.filter((item) => Boolean(sandboxField(item, 'usable_for_marketing', 'UsableForMarketing'))).length;
  const pendingDecisions = decisions.filter((item) => String(sandboxField(item, 'approval_status', 'ApprovalStatus') || '') === 'pending' || String(sandboxField(item, 'gate_status', 'GateStatus') || '') === 'needs_review').length;
  const totalRevenue = summary ? Number(sandboxField(summary, 'total_revenue_amount', 'TotalRevenueAmount') || 0) : events.reduce((sum, item) => sum + Math.max(0, Number(sandboxField(item, 'amount', 'Amount') || 0)), 0);
  const paidCustomers = summary ? Number(sandboxField(summary, 'paid_customer_count', 'PaidCustomerCount') || 0) : 0;
  const trend = summary && Array.isArray(sandboxField(summary, 'kpi_trend', 'KPITrend')) ? sandboxField(summary, 'kpi_trend', 'KPITrend') : [];
  const productSales = summary && Array.isArray(sandboxField(summary, 'product_sales', 'ProductSales')) ? sandboxField(summary, 'product_sales', 'ProductSales') : [];
  const voiceTypes = summary && Array.isArray(sandboxField(summary, 'customer_voice_types', 'CustomerVoiceTypes')) ? sandboxField(summary, 'customer_voice_types', 'CustomerVoiceTypes') : [];
  const topProduct = productSales[0] || null;
  const topVoiceType = voiceTypes[0] || null;
  const latestTrend = trend.length ? trend[trend.length - 1] : null;
  const latest = channelDrafts[0] || dailyReports[0] || products[0] || market[0] || null;
  const channelDraftCount = summary ? Number(sandboxField(summary, 'channel_draft_count', 'ChannelDraftCount') || 0) : channelDrafts.length;
  const externalSendApplyCount = summary ? Number(sandboxField(summary, 'external_send_apply_count', 'ExternalSendApplyCount') || 0) : externalSendApplies.length;
  const externalChannelAdapter = String(state.ops.revenueExternalChannelAdapter || 'unconfigured');
  const externalChannelConfigured = Boolean(state.ops.revenueExternalChannelAdapterConfigured);
  const externalSendApproval = state.ops.revenueExternalSendHumanApprovalRequired !== false;
  return {
    title: 'Revenue',
    big: String(events.length) + '/' + String(products.length),
    sub: market.length || posts.length || products.length || voices.length || events.length || decisions.length || dailyReports.length || channelDrafts.length || externalSendApplies.length ? ('paid events: ' + String(summary ? sandboxField(summary, 'paid_event_count', 'PaidEventCount') : paid) + ' market: ' + String(summary ? sandboxField(summary, 'market_research_count', 'MarketResearchCount') : market.length) + '\nrevenue: ' + String(totalRevenue) + ' paid customers: ' + String(paidCustomers) + '\nvoices usable: ' + String(summary ? sandboxField(summary, 'usable_voice_count', 'UsableVoiceCount') : usableVoices) + '/' + String(summary ? sandboxField(summary, 'customer_voice_count', 'CustomerVoiceCount') : voices.length) + ' posts: ' + String(summary ? sandboxField(summary, 'sns_post_count', 'SNSPostCount') : posts.length) + '\ndaily reports: ' + String(summary ? sandboxField(summary, 'daily_report_count', 'DailyReportCount') : dailyReports.length) + ' channel drafts: ' + String(channelDraftCount) + ' external apply audits: ' + String(externalSendApplyCount) + ' human decisions pending: ' + String(summary ? sandboxField(summary, 'pending_decision_count', 'PendingDecisionCount') : pendingDecisions) + '/' + String(decisions.length) + '\nexternal channel adapter: ' + externalChannelAdapter + ' / configured: ' + (externalChannelConfigured ? 'yes' : 'no') + ' / human approval: ' + (externalSendApproval ? 'required' : 'missing') + '\ntrend days: ' + String(trend.length) + ' latest revenue: ' + String(latestTrend ? sandboxField(latestTrend, 'revenue_amount', 'RevenueAmount') : '-') + '\ntop product: ' + String(topProduct ? (sandboxField(topProduct, 'product_name', 'ProductName') || sandboxField(topProduct, 'product_id', 'ProductID')) : '-') + ' voices top: ' + String(topVoiceType ? sandboxField(topVoiceType, 'voice_type', 'VoiceType') : '-') + '\nlatest: ' + String(sandboxField(latest, 'draft_id', 'DraftID') || sandboxField(latest, 'report_id', 'ReportID') || sandboxField(latest, 'product_name', 'ProductName') || sandboxField(latest, 'theme', 'Theme') || '-')) : 'revenue record なし',
  };
}

function latestRevenueHumanDecisions(items) {
  const seen = new Set();
  const out = [];
  items.forEach((item) => {
    const id = String(sandboxField(item, 'decision_id', 'DecisionID') || '');
    const key = id || JSON.stringify(item);
    if (seen.has(key)) return;
    seen.add(key);
    out.push(item);
  });
  return out;
}

function renderRevenueHumanDecisions() {
  const body = document.getElementById('revenueDecisionBody');
  if (!body) return;
  body.innerHTML = '';
  const decisions = latestRevenueHumanDecisions(Array.isArray(state.ops.revenueHumanDecisions) ? state.ops.revenueHumanDecisions : []);
  if (decisions.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="6" class="small">No revenue human decisions yet</td>';
    body.appendChild(tr);
    renderRevenueDecisionResult();
    return;
  }
  decisions.slice(0, 20).forEach((item) => {
    const decisionID = String(sandboxField(item, 'decision_id', 'DecisionID') || '');
    const approval = String(sandboxField(item, 'approval_status', 'ApprovalStatus') || '-');
    const gate = String(sandboxField(item, 'gate_status', 'GateStatus') || '-');
    const needsReview = approval === 'pending' || gate === 'needs_review';
    const actions = needsReview && decisionID
      ? '<button class="ctl-btn revenue-decision-review" type="button" data-decision-id="' + escAttr(decisionID) + '" data-approval-status="approved">Approve</button> <button class="ctl-btn revenue-decision-review" type="button" data-decision-id="' + escAttr(decisionID) + '" data-approval-status="rejected">Reject</button>'
      : '<span class="small">-</span>';
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td>' + esc(ftime(sandboxField(item, 'created_at', 'CreatedAt'))) + '</td>' +
      '<td class="code">' + esc(decisionID || '-') + '</td>' +
      '<td>' + esc(sandboxField(item, 'decision_type', 'DecisionType') || '-') + '</td>' +
      '<td><span class="badge ' + stateClass(gate) + '">' + esc(approval + ' / ' + gate) + '</span></td>' +
      '<td>' + esc(short(sandboxField(item, 'description', 'Description') || '-', 120)) + '</td>' +
      '<td>' + actions + '</td>';
    body.appendChild(tr);
  });
  body.querySelectorAll('.revenue-decision-review').forEach((btn) => {
    btn.addEventListener('click', () => {
      reviewRevenueHumanDecision(btn.getAttribute('data-decision-id') || '', btn.getAttribute('data-approval-status') || '');
    });
  });
  renderRevenueDecisionResult();
}

function renderRevenueDecisionResult() {
  const el = document.getElementById('revenueDecisionResult');
  if (!el) return;
  const result = state.ops.revenueDecisionReviewResult || null;
  if (!result) {
    el.textContent = 'revenue decision review: -';
    return;
  }
  el.textContent = JSON.stringify(result, null, 2);
}

function renderRevenueChannelDrafts() {
  const body = document.getElementById('revenueChannelDraftBody');
  if (!body) return;
  body.innerHTML = '';
  const drafts = Array.isArray(state.ops.revenueChannelDrafts) ? state.ops.revenueChannelDrafts : [];
  if (drafts.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="7" class="small">No revenue channel drafts yet</td>';
    body.appendChild(tr);
    renderRevenueChannelDraftResult();
    return;
  }
  drafts.slice(0, 20).forEach((item) => {
    const draftID = String(sandboxField(item, 'draft_id', 'DraftID') || '');
    const channel = String(sandboxField(item, 'channel', 'Channel') || '-');
    const approval = String(sandboxField(item, 'approval_status', 'ApprovalStatus') || '-');
    const externalSend = Boolean(sandboxField(item, 'external_send_applied', 'ExternalSendApplied'));
    const sendState = externalSend ? 'sent unexpectedly' : 'draft only';
    const source = sandboxField(item, 'source_report_id', 'SourceReportID') || sandboxField(item, 'source_workstream_id', 'SourceWorkstreamID') || '-';
    const subject = sandboxField(item, 'subject', 'Subject') || '';
    const bodyText = sandboxField(item, 'body', 'Body') || '';
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td>' + esc(ftime(sandboxField(item, 'created_at', 'CreatedAt'))) + '</td>' +
      '<td class="code">' + esc(draftID || '-') + '</td>' +
      '<td>' + esc(channel) + '</td>' +
      '<td><span class="badge ' + stateClass(approval) + '">' + esc(approval) + '</span></td>' +
      '<td class="code">' + esc(short(source, 42)) + '</td>' +
      '<td>' + esc(short((subject ? subject + ' / ' : '') + bodyText, 160)) + '</td>' +
      '<td><span class="badge ' + stateClass(externalSend ? 'failed' : 'pending') + '">' + esc(sendState) + '</span></td>';
    body.appendChild(tr);
  });
  renderRevenueChannelDraftResult();
}

function renderRevenueChannelDraftResult() {
  const el = document.getElementById('revenueChannelDraftResult');
  if (!el) return;
  const fetchError = String(state.ops.revenueFetchError || '');
  if (fetchError) {
    el.textContent = 'revenue channel drafts unavailable: ' + fetchError + '\nblocked: external send audit state unreadable';
    return;
  }
  const drafts = Array.isArray(state.ops.revenueChannelDrafts) ? state.ops.revenueChannelDrafts : [];
  const externalSent = drafts.filter((item) => Boolean(sandboxField(item, 'external_send_applied', 'ExternalSendApplied'))).length;
  const pending = drafts.filter((item) => String(sandboxField(item, 'approval_status', 'ApprovalStatus') || '') === 'pending').length;
  const draftOnly = drafts.length - externalSent;
  const approval = state.ops.revenueExternalSendHumanApprovalRequired !== false;
  el.textContent = 'revenue channel drafts: ' + String(drafts.length) + ' total / ' + String(pending) + ' pending / ' + String(draftOnly) + ' draft-only / ' + String(externalSent) + ' external_send_applied\nmode: draft-only / external send requires human approval: ' + (approval ? 'yes' : 'missing');
}

function renderRevenueExternalSendAudits() {
  const body = document.getElementById('revenueExternalSendAuditBody');
  if (!body) return;
  body.innerHTML = '';
  const fetchError = String(state.ops.revenueFetchError || '');
  if (fetchError) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="9" class="small">Revenue external send apply audits unavailable: ' + esc(fetchError) + '</td>';
    body.appendChild(tr);
    renderRevenueExternalSendAuditResult();
    return;
  }
  const records = Array.isArray(state.ops.revenueExternalSendApplyRecords) ? state.ops.revenueExternalSendApplyRecords : [];
  if (records.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="9" class="small">No revenue external send apply audits yet</td>';
    body.appendChild(tr);
    renderRevenueExternalSendAuditResult();
    return;
  }
  records.slice(0, 20).forEach((item) => {
    const applyID = String(sandboxField(item, 'apply_id', 'ApplyID') || '');
    const draftID = String(sandboxField(item, 'draft_id', 'DraftID') || '');
    const decisionID = String(sandboxField(item, 'decision_id', 'DecisionID') || '');
    const channel = String(sandboxField(item, 'channel', 'Channel') || '-');
    const status = String(sandboxField(item, 'apply_status', 'ApplyStatus') || '-');
    const sendResult = String(sandboxField(item, 'send_result', 'SendResult') || '-');
    const adapter = String(sandboxField(item, 'channel_adapter', 'ChannelAdapter') || 'unconfigured');
    const applied = Boolean(sandboxField(item, 'external_send_applied', 'ExternalSendApplied'));
    const verified = Boolean(sandboxField(item, 'post_send_verified', 'PostSendVerified'));
    const evidence = sandboxField(item, 'post_send_evidence', 'PostSendEvidence') || sandboxField(item, 'failure_reason', 'FailureReason') || '';
    const statusClass = applied && verified ? 'running' : (status === 'failed' ? 'error' : 'offline');
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td>' + esc(ftime(sandboxField(item, 'created_at', 'CreatedAt'))) + '</td>' +
      '<td class="code">' + esc(applyID || '-') + '</td>' +
      '<td class="code">' + esc(short(draftID || '-', 42)) + '</td>' +
      '<td class="code">' + esc(short(decisionID || '-', 42)) + '</td>' +
      '<td>' + esc(channel) + '</td>' +
      '<td><span class="badge ' + stateClass(statusClass) + '">' + esc(status) + '</span></td>' +
      '<td>' + esc(sendResult) + '</td>' +
      '<td>' + esc(adapter) + '</td>' +
      '<td>' + esc(short(evidence || (applied ? 'post-send evidence missing' : 'not sent'), 120)) + '</td>';
    body.appendChild(tr);
  });
  renderRevenueExternalSendAuditResult();
}

function renderRevenueExternalSendAuditResult() {
  const el = document.getElementById('revenueExternalSendAuditResult');
  if (!el) return;
  const fetchError = String(state.ops.revenueFetchError || '');
  if (fetchError) {
    el.textContent = 'revenue external send apply audits unavailable: ' + fetchError + '\nblocked: external send audit state unreadable';
    return;
  }
  const records = Array.isArray(state.ops.revenueExternalSendApplyRecords) ? state.ops.revenueExternalSendApplyRecords : [];
  const sent = records.filter((item) => Boolean(sandboxField(item, 'external_send_applied', 'ExternalSendApplied'))).length;
  const verified = records.filter((item) => Boolean(sandboxField(item, 'post_send_verified', 'PostSendVerified')) && String(sandboxField(item, 'post_send_evidence', 'PostSendEvidence') || '').trim() !== '').length;
  const blocked = records.filter((item) => String(sandboxField(item, 'apply_status', 'ApplyStatus') || '') === 'blocked').length;
  const notSent = records.length - sent;
  const adapter = String(state.ops.revenueExternalChannelAdapter || 'unconfigured');
  const configured = Boolean(state.ops.revenueExternalChannelAdapterConfigured);
  const approval = state.ops.revenueExternalSendHumanApprovalRequired !== false;
  const blockedText = sent === 0 ? '\nblocked: no external send applied' : '';
  el.textContent = 'revenue external send apply audits: ' + String(records.length) + ' total / ' + String(blocked) + ' blocked / ' + String(sent) + ' sent / ' + String(notSent) + ' not sent / ' + String(verified) + ' verified\nexternal channel adapter: ' + adapter + ' / configured: ' + (configured ? 'yes' : 'no') + ' / human approval: ' + (approval ? 'required' : 'missing') + blockedText;
}

function revenueBar(value, max) {
  const n = Math.max(0, Number(value || 0));
  const limit = Math.max(1, Number(max || 1));
  const width = Math.max(1, Math.min(20, Math.round((n / limit) * 20)));
  return '#'.repeat(width) + '.'.repeat(Math.max(0, 20 - width));
}

function revenueDrilldownLines() {
  const fetchError = String(state.ops.revenueFetchError || '');
  if (fetchError) {
    return [
      'Revenue Drilldown unavailable',
      'blocked: external send audit state unreadable',
      fetchError,
    ];
  }
  const summary = state.ops.revenueSummary && typeof state.ops.revenueSummary === 'object' ? state.ops.revenueSummary : {};
  const trend = Array.isArray(sandboxField(summary, 'kpi_trend', 'KPITrend')) ? sandboxField(summary, 'kpi_trend', 'KPITrend') : [];
  const productSales = Array.isArray(sandboxField(summary, 'product_sales', 'ProductSales')) ? sandboxField(summary, 'product_sales', 'ProductSales') : [];
  const voiceTypes = Array.isArray(sandboxField(summary, 'customer_voice_types', 'CustomerVoiceTypes')) ? sandboxField(summary, 'customer_voice_types', 'CustomerVoiceTypes') : [];
  const decisions = latestRevenueHumanDecisions(Array.isArray(state.ops.revenueHumanDecisions) ? state.ops.revenueHumanDecisions : []);
  const drafts = Array.isArray(state.ops.revenueChannelDrafts) ? state.ops.revenueChannelDrafts : [];
  const externalSendApplies = Array.isArray(state.ops.revenueExternalSendApplyRecords) ? state.ops.revenueExternalSendApplyRecords : [];
  const maxRevenue = Math.max(1, ...trend.map((item) => Number(sandboxField(item, 'revenue_amount', 'RevenueAmount') || 0)));
  const maxProductRevenue = Math.max(1, ...productSales.map((item) => Number(sandboxField(item, 'revenue_amount', 'RevenueAmount') || 0)));
  const maxVoiceCount = Math.max(1, ...voiceTypes.map((item) => Number(sandboxField(item, 'count', 'Count') || 0)));
  const lines = [
    'Revenue Drilldown',
    'summary: revenue=' + String(sandboxField(summary, 'total_revenue_amount', 'TotalRevenueAmount') || 0) +
      ' paid_customers=' + String(sandboxField(summary, 'paid_customer_count', 'PaidCustomerCount') || 0) +
      ' pending_decisions=' + String(sandboxField(summary, 'pending_decision_count', 'PendingDecisionCount') || 0) +
      ' channel_drafts=' + String(sandboxField(summary, 'channel_draft_count', 'ChannelDraftCount') || drafts.length) +
      ' external_apply_audits=' + String(sandboxField(summary, 'external_send_apply_count', 'ExternalSendApplyCount') || externalSendApplies.length),
    '',
    'KPI trend graph:',
  ];
  if (trend.length === 0) {
    lines.push('-');
  } else {
    trend.slice(-14).forEach((item) => {
      const day = sandboxField(item, 'date', 'Date') || '-';
      const revenue = Number(sandboxField(item, 'revenue_amount', 'RevenueAmount') || 0);
      const posts = Number(sandboxField(item, 'post_count', 'PostCount') || 0);
      const voices = Number(sandboxField(item, 'voice_count', 'VoiceCount') || 0);
      lines.push(day + ' |' + revenueBar(revenue, maxRevenue) + '| revenue=' + revenue + ' posts=' + posts + ' voices=' + voices);
    });
  }
  lines.push('', 'Product sales graph:');
  if (productSales.length === 0) {
    lines.push('-');
  } else {
    productSales.slice(0, 10).forEach((item) => {
      const name = sandboxField(item, 'product_name', 'ProductName') || sandboxField(item, 'product_id', 'ProductID') || '-';
      const revenue = Number(sandboxField(item, 'revenue_amount', 'RevenueAmount') || 0);
      const sales = Number(sandboxField(item, 'sales_count', 'SalesCount') || 0);
      lines.push(short(name, 28) + ' |' + revenueBar(revenue, maxProductRevenue) + '| revenue=' + revenue + ' sales=' + sales);
    });
  }
  lines.push('', 'Customer voice graph:');
  if (voiceTypes.length === 0) {
    lines.push('-');
  } else {
    voiceTypes.slice(0, 10).forEach((item) => {
      const voiceType = sandboxField(item, 'voice_type', 'VoiceType') || '-';
      const count = Number(sandboxField(item, 'count', 'Count') || 0);
      lines.push(short(voiceType, 28) + ' |' + revenueBar(count, maxVoiceCount) + '| count=' + count);
    });
  }
  lines.push('', 'Decision drilldown:');
  if (decisions.length === 0) {
    lines.push('-');
  } else {
    decisions.slice(0, 8).forEach((item) => {
      lines.push(
        String(sandboxField(item, 'decision_id', 'DecisionID') || '-') +
        ' / ' + String(sandboxField(item, 'decision_type', 'DecisionType') || '-') +
        ' / ' + String(sandboxField(item, 'approval_status', 'ApprovalStatus') || '-') +
        ' / ' + String(sandboxField(item, 'gate_status', 'GateStatus') || '-')
      );
    });
  }
  return lines;
}

function renderRevenueDrilldown() {
  const el = document.getElementById('revenueDrilldownResult');
  if (!el) return;
  el.textContent = revenueDrilldownLines().join('\n');
}

async function reviewRevenueHumanDecision(decisionID, approvalStatus) {
  if (!decisionID || (approvalStatus !== 'approved' && approvalStatus !== 'rejected')) return;
  const payload = {decision_id: decisionID, approval_status: approvalStatus};
  try {
    const res = await fetch('/viewer/revenue/human-decision-gate/review', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(payload),
    });
    const text = await res.text();
    let data = null;
    try {
      data = text ? JSON.parse(text) : {};
    } catch (_) {
      data = {raw: text};
    }
    if (!res.ok) {
      state.ops.revenueDecisionReviewResult = {status: 'failed', http_status: res.status, response: data};
    } else {
      state.ops.revenueDecisionReviewResult = data;
      refreshRevenueData();
    }
  } catch (err) {
    state.ops.revenueDecisionReviewResult = {status: 'failed', error: String(err && err.message ? err.message : err)};
  }
  renderRevenueHumanDecisions();
}

function personaObservationOpsCard() {
  const fetchError = String(state.ops.personaObservationFetchError || '');
  if (fetchError) {
    return {
      title: 'Persona Observation',
      big: 'unavailable',
      sub: 'persona observation status unavailable: ' + fetchError + '\nblocked: persona meta review state unreadable\nblocked: long-term personality update state unreadable',
    };
  }
  const discomforts = Array.isArray(state.ops.personaDiscomfortLogs) ? state.ops.personaDiscomfortLogs : [];
  const triggers = Array.isArray(state.ops.personaTriggerLogs) ? state.ops.personaTriggerLogs : [];
  const canonicals = Array.isArray(state.ops.personaCanonicalResponseLogs) ? state.ops.personaCanonicalResponseLogs : [];
  const observations = Array.isArray(state.ops.personaObservationLogs) ? state.ops.personaObservationLogs : [];
  const metaUpdates = latestPersonaMetaProfileUpdates(Array.isArray(state.ops.personaMetaProfileUpdates) ? state.ops.personaMetaProfileUpdates : []);
  const sessions = Array.isArray(state.ops.personaInterfaceSessions) ? state.ops.personaInterfaceSessions : [];
  const pending = observations.filter((item) => String(sandboxField(item, 'review_status', 'ReviewStatus') || '') === 'pending').length;
  const pendingMeta = metaUpdates.filter((item) => String(sandboxField(item, 'review_status', 'ReviewStatus') || '') === 'pending').length;
  const latest = discomforts[0] || observations[0] || metaUpdates[0] || triggers[0] || null;
  return {
    title: 'Persona Observation',
    big: String(observations.length) + '/' + String(discomforts.length),
    sub: discomforts.length || triggers.length || canonicals.length || observations.length || metaUpdates.length || sessions.length ? ('review pending: ' + String(pending) + ' meta pending: ' + String(pendingMeta) + '\ncanonical: ' + String(canonicals.length) + ' sessions: ' + String(sessions.length) + '\nlatest: ' + String(sandboxField(latest, 'character_id', 'CharacterID') || sandboxField(latest, 'target_id', 'TargetID') || '-')) : 'persona observation record なし',
  };
}

function latestPersonaMetaProfileUpdates(items) {
  const seen = new Set();
  const out = [];
  items.forEach((item) => {
    const id = String(sandboxField(item, 'update_id', 'UpdateID') || '');
    const key = id || JSON.stringify(item);
    if (seen.has(key)) return;
    seen.add(key);
    out.push(item);
  });
  return out;
}

function renderPersonaMetaReviews() {
  const body = document.getElementById('personaMetaReviewBody');
  if (!body) return;
  body.innerHTML = '';
  const fetchError = String(state.ops.personaObservationFetchError || '');
  if (fetchError) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="6" class="small">Persona meta reviews unavailable: ' + esc(fetchError) + '</td>';
    body.appendChild(tr);
    renderPersonaMetaReviewResult();
    return;
  }
  const updates = latestPersonaMetaProfileUpdates(Array.isArray(state.ops.personaMetaProfileUpdates) ? state.ops.personaMetaProfileUpdates : []);
  if (updates.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="6" class="small">No persona meta updates yet</td>';
    body.appendChild(tr);
    renderPersonaMetaReviewResult();
    return;
  }
  updates.slice(0, 20).forEach((item) => {
    const updateID = String(sandboxField(item, 'update_id', 'UpdateID') || '');
    const review = String(sandboxField(item, 'review_status', 'ReviewStatus') || '-');
    const target = String(sandboxField(item, 'target_id', 'TargetID') || '-');
    const observer = String(sandboxField(item, 'observer_id', 'ObserverID') || '-');
    const proposed = String(sandboxField(item, 'proposed_content', 'ProposedContent') || '');
    const pending = review === 'pending';
    const payload = encodeURIComponent(JSON.stringify(item));
    const actions = pending && updateID
      ? '<button class="ctl-btn persona-meta-review" type="button" data-update="' + escAttr(payload) + '" data-review-status="approved">Approve</button> <button class="ctl-btn persona-meta-review" type="button" data-update="' + escAttr(payload) + '" data-review-status="rejected">Reject</button>'
      : '<span class="small">-</span>';
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td>' + esc(ftime(sandboxField(item, 'created_at', 'CreatedAt'))) + '</td>' +
      '<td class="code">' + esc(updateID || '-') + '</td>' +
      '<td>' + esc(observer + ' → ' + target) + '</td>' +
      '<td><span class="badge ' + stateClass(review) + '">' + esc(review) + '</span></td>' +
      '<td class="code">' + esc(proposed ? short(proposed, 120) : '-') + '</td>' +
      '<td>' + actions + '</td>';
    body.appendChild(tr);
  });
  body.querySelectorAll('.persona-meta-review').forEach((btn) => {
    btn.addEventListener('click', () => {
      reviewPersonaMetaUpdate(btn.getAttribute('data-update') || '', btn.getAttribute('data-review-status') || '');
    });
  });
  renderPersonaMetaReviewResult();
}

function renderPersonaMetaReviewResult() {
  const el = document.getElementById('personaMetaReviewResult');
  if (!el) return;
  const fetchError = String(state.ops.personaObservationFetchError || '');
  if (fetchError) {
    el.textContent = 'persona meta review unavailable: ' + fetchError + '\nblocked: persona meta review state unreadable';
    return;
  }
  const result = state.ops.personaMetaReviewResult || null;
  if (!result) {
    el.textContent = 'persona meta review: -';
    return;
  }
  el.textContent = JSON.stringify(result, null, 2);
}

async function reviewPersonaMetaUpdate(encodedUpdate, reviewStatus) {
  if (!encodedUpdate || (reviewStatus !== 'approved' && reviewStatus !== 'rejected')) return;
  let payload = {};
  try {
    payload = JSON.parse(decodeURIComponent(encodedUpdate));
  } catch (_) {
    state.ops.personaMetaReviewResult = {status: 'failed', error: 'invalid persona meta update payload'};
    renderPersonaMetaReviews();
    return;
  }
  payload.review_status = reviewStatus;
  try {
    const res = await fetch('/viewer/persona-observation/meta-updates/review', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(payload),
    });
    const text = await res.text();
    let data = null;
    try {
      data = text ? JSON.parse(text) : {};
    } catch (_) {
      data = {raw: text};
    }
    if (!res.ok) {
      state.ops.personaMetaReviewResult = {status: 'failed', http_status: res.status, response: data};
    } else {
      state.ops.personaMetaReviewResult = data;
      refreshPersonaObservationData();
    }
  } catch (err) {
    state.ops.personaMetaReviewResult = {status: 'failed', error: String(err && err.message ? err.message : err)};
  }
  renderPersonaMetaReviews();
}

function browserTraceAPIOpsCard() {
  const fetchError = String(state.ops.browserTraceAPIFetchError || '');
  if (fetchError) {
    return {
      title: 'Browser Trace API',
      big: 'unavailable',
      sub: 'browser trace api status unavailable: ' + fetchError + '\nblocked: official API adoption state unreadable\nblocked: fetcher implementation state unreadable',
    };
  }
  const runs = Array.isArray(state.ops.browserTraceRuns) ? state.ops.browserTraceRuns : [];
  const candidates = Array.isArray(state.ops.browserTraceAPICandidates) ? state.ops.browserTraceAPICandidates : [];
  const schemas = Array.isArray(state.ops.browserTraceAPISchemas) ? state.ops.browserTraceAPISchemas : [];
  const coverage = Array.isArray(state.ops.browserTraceAPICoverageReports) ? state.ops.browserTraceAPICoverageReports : [];
  const artifacts = Array.isArray(state.ops.browserTraceAPIArtifacts) ? state.ops.browserTraceAPIArtifacts : [];
  const auth = candidates.filter((item) => Boolean(sandboxField(item, 'auth_required', 'AuthRequired'))).length;
  const fetcherProposals = artifacts.filter((item) => String(sandboxField(item, 'artifact_type', 'Type') || '') === 'fetcher_proposal').length;
  const latest = candidates[0] || runs[0] || null;
  return {
    title: 'Browser Trace API',
    big: String(candidates.length) + '/' + String(runs.length),
    sub: runs.length || candidates.length || schemas.length || coverage.length || artifacts.length ? ('auth candidates: ' + String(auth) + ' schemas: ' + String(schemas.length) + '\ncoverage reports: ' + String(coverage.length) + ' fetcher proposals: ' + String(fetcherProposals) + '\nlatest: ' + String(sandboxField(latest, 'path_template', 'PathTemplate') || sandboxField(latest, 'trace_run_id', 'TraceRunID') || '-') + '\nreview-only: no official API adoption') : 'browser trace api record なし\nblocked: no trace candidates\nblocked: no official API adoption',
  };
}

async function requestBrowserTraceAPIFetcherProposal(candidateID, workstreamID) {
  if (!candidateID) return;
  try {
    const res = await fetch('/viewer/browser-trace-api/fetcher-proposals', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({
        candidate_id: candidateID,
        workstream_id: workstreamID || '',
        human_approved: true,
      }),
    });
    const text = await res.text();
    let data = null;
    try {
      data = text ? JSON.parse(text) : {};
    } catch (_) {
      data = {raw: text};
    }
    state.ops.browserTraceAPIFetcherProposalResult = res.ok ? data : {status: 'failed', http_status: res.status, response: data};
  } catch (err) {
    state.ops.browserTraceAPIFetcherProposalResult = {status: 'failed', error: String(err && err.message ? err.message : err)};
  }
  renderOps();
}

function complexityHotspotOpsCard() {
  const fetchError = String(state.ops.complexityFetchError || '');
  if (fetchError) {
    return {
      title: 'Complexity Hotspots',
      big: 'unavailable',
      sub: 'complexity hotspot status unavailable: ' + fetchError + '\nblocked: patch apply state unreadable',
    };
  }
  const scans = Array.isArray(state.ops.complexityScans) ? state.ops.complexityScans : [];
  const hotspots = Array.isArray(state.ops.complexityHotspots) ? state.ops.complexityHotspots : [];
  const evidence = Array.isArray(state.ops.complexityEvidence) ? state.ops.complexityEvidence : [];
  const reports = Array.isArray(state.ops.complexityReports) ? state.ops.complexityReports : [];
  const highRisk = hotspots.filter((item) => String(sandboxField(item, 'risk_level', 'RiskLevel') || '') === 'high').length;
  const pendingReview = reports.filter((item) => String(sandboxField(item, 'status', 'Status') || '') === 'pending_review').length;
  const latest = hotspots[0] || scans[0] || null;
  return {
    title: 'Complexity Hotspots',
    big: String(hotspots.length) + '/' + String(scans.length),
    sub: scans.length || hotspots.length || evidence.length || reports.length ? ('high risk: ' + String(highRisk) + ' evidence: ' + String(evidence.length) + '\nreports: ' + String(reports.length) + ' pending-review: ' + String(pendingReview) + '\nlatest: ' + String(sandboxField(latest, 'hotspot_type', 'HotspotType') || sandboxField(latest, 'scan_id', 'ScanID') || '-') + '\nmode: review-only blocked: no patch applied') : 'complexity hotspot record なし',
  };
}

function superAgentOpsCard() {
  const fetchError = String(state.ops.superAgentFetchError || '');
  if (fetchError) {
    return {
      title: 'SuperAgent Harness',
      big: 'unavailable',
      sub: 'superagent status unavailable: ' + fetchError + '\nblocked: scheduler terminal state unreadable\nblocked: true long-running resume state unreadable',
    };
  }
  const runs = Array.isArray(state.ops.superAgentRuns) ? state.ops.superAgentRuns : [];
  const tasks = Array.isArray(state.ops.superAgentSubagentTasks) ? state.ops.superAgentSubagentTasks : [];
  const contexts = Array.isArray(state.ops.superAgentContextPacks) ? state.ops.superAgentContextPacks : [];
  const channels = Array.isArray(state.ops.superAgentMessageChannels) ? state.ops.superAgentMessageChannels : [];
  const events = Array.isArray(state.ops.superAgentTraceEvents) ? state.ops.superAgentTraceEvents : [];
  const queue = Array.isArray(state.ops.superAgentRunQueue) ? state.ops.superAgentRunQueue : [];
  const runtimeConfig = state.ops.superAgentRuntimeConfig || {};
  const running = runs.filter((item) => String(sandboxField(item, 'status', 'Status') || '') === 'running').length;
  const queued = queue.filter((item) => String(sandboxField(item, 'status', 'Status') || '') === 'queued').length;
  const latest = runs[0] || tasks[0] || events[0] || null;
  const schedulerEnabled = Boolean(sandboxField(runtimeConfig, 'run_queue_scheduler_enabled', 'RunQueueSchedulerEnabled'));
  const schedulerInterval = Number(sandboxField(runtimeConfig, 'run_queue_scheduler_interval_sec', 'RunQueueSchedulerIntervalSec') || 0);
  const schedulerClaimLimit = Number(sandboxField(runtimeConfig, 'run_queue_scheduler_claim_limit', 'RunQueueSchedulerClaimLimit') || 0);
  const schedulerState = schedulerEnabled ? 'enabled' : 'disabled';
  const schedulerDetail = schedulerEnabled ?
    ('scheduler:' + schedulerState + ' interval:' + String(schedulerInterval) + 's claim:' + String(schedulerClaimLimit)) :
    'scheduler:disabled blocked: scheduler disabled';
  return {
    title: 'SuperAgent Harness',
    big: String(runs.length) + '/' + String(tasks.length),
    sub: runs.length || tasks.length || contexts.length || channels.length || events.length || queue.length || runtimeConfig ? ('running: ' + String(running) + ' context packs: ' + String(contexts.length) + '\nchannels: ' + String(channels.length) + ' trace events: ' + String(events.length) + '\nrun queue: ' + String(queue.length) + ' queued: ' + String(queued) + '\n' + schedulerDetail + '\nlatest: ' + String(sandboxField(latest, 'run_id', 'RunID') || sandboxField(latest, 'subagent_id', 'SubagentID') || '-')) : 'superagent harness record なし',
  };
}

function aiWorkflowOpsCard() {
  const fetchError = String(state.ops.aiWorkflowFetchError || '');
  if (fetchError) {
    return {
      title: 'AI Workflow',
      big: 'unavailable',
      sub: 'ai workflow status unavailable: ' + fetchError + '\nblocked: scheduler normal completion state unreadable',
    };
  }
  const events = Array.isArray(state.ops.aiWorkflowEvents) ? state.ops.aiWorkflowEvents : [];
  const memories = Array.isArray(state.ops.aiWorkflowProjectMemoryIndexes) ? state.ops.aiWorkflowProjectMemoryIndexes : [];
  const worktrees = Array.isArray(state.ops.aiWorkflowWorktreeRegistries) ? state.ops.aiWorkflowWorktreeRegistries : [];
  const commands = Array.isArray(state.ops.aiWorkflowCommandRegistries) ? state.ops.aiWorkflowCommandRegistries : [];
  const contexts = Array.isArray(state.ops.aiWorkflowContextUsages) ? state.ops.aiWorkflowContextUsages : [];
  const policy = state.ops.aiWorkflowContextBudgetPolicy || {};
  const latest = events[0] || contexts[0] || commands[0] || null;
  const maxTokens = Number(sandboxField(policy, 'max_context_tokens', 'MaxContextTokens') || 0);
  const warnRatio = Number(sandboxField(policy, 'warn_at_ratio', 'WarnAtRatio') || 0);
  const stopRatio = Number(sandboxField(policy, 'stop_at_ratio', 'StopAtRatio') || 0);
  const budgetDetail = maxTokens > 0 ?
    ('context-budget:enabled max:' + String(maxTokens) + ' warn:' + String(warnRatio) + ' stop:' + String(stopRatio)) :
    'context-budget:disabled blocked: context budget disabled';
  return {
    title: 'AI Workflow',
    big: String(events.length) + '/' + String(contexts.length),
    sub: events.length || memories.length || worktrees.length || commands.length || contexts.length || policy ? ('commands: ' + String(commands.length) + ' worktrees: ' + String(worktrees.length) + '\nproject memory: ' + String(memories.length) + ' context usage: ' + String(contexts.length) + '\n' + budgetDetail + '\nlatest: ' + String(sandboxField(latest, 'event_id', 'EventID') || sandboxField(latest, 'command_name', 'CommandName') || '-')) : 'ai workflow record なし',
  };
}

function heavyWorkerRuntimeOpsCard() {
  const fetchError = String(state.ops.heavyWorkerRuntimeDiagnosticsFetchError || '');
  if (fetchError) {
    return {
      title: 'Heavy Runtime',
      big: 'unavailable',
      sub: 'heavy runtime diagnostics unavailable: ' + fetchError + '\nblocked: RouteANALYZE provider state unreadable\nblocked: LLM Ops live state unreadable',
    };
  }
  const diag = state.ops.heavyWorkerRuntimeDiagnostics || null;
  if (!diag) {
    return {
      title: 'Heavy Runtime',
      big: '-',
      sub: 'runtime diagnostics 未取得',
    };
  }
  const ops = diag.llm_ops || {};
  const live = ops.live_available ? 'live' : 'config';
  const stateInfo = ops.role_state || {};
  const memory = ops.memory || {};
  const health = stateInfo.health_ok === true ? 'healthy' : (stateInfo.health_ok === false ? 'unhealthy' : live);
  const model = memory.model || diag.model || '-';
  const pid = memory.pid == null ? 'pid -' : ('pid ' + String(memory.pid));
  const base = memory.port ? replaceURLPort(diag.base_url, memory.port) : (diag.base_url || '-');
  const error = ops.error ? '\n' + String(ops.error) : '';
  return {
    title: 'Heavy Runtime',
    big: health,
    sub: 'route: ' + (diag.route || 'ANALYZE') + ' ' + (diag.route_prefix || '/analyze') +
      '\n' + String(model) + ' · ' + pid +
      '\n' + String(base) + error,
  };
}

function knowledgeMemoryOpsCard() {
  const fetchError = String(state.ops.knowledgeMemoryFetchError || '');
  if (fetchError) {
    return {
      title: 'Knowledge Memory',
      big: 'unavailable',
      sub: 'knowledge memory status unavailable: ' + fetchError + '\nblocked: memory promote state unreadable\nblocked: source registry sync state unreadable',
    };
  }
  const personal = Array.isArray(state.ops.knowledgePersonalArchive) ? state.ops.knowledgePersonalArchive : [];
  const creative = Array.isArray(state.ops.knowledgeCreativeItems) ? state.ops.knowledgeCreativeItems : [];
  const news = Array.isArray(state.ops.knowledgeNewsItems) ? state.ops.knowledgeNewsItems : [];
  const intake = Array.isArray(state.ops.knowledgeDailyIntakeRules) ? state.ops.knowledgeDailyIntakeRules : [];
  const temporal = Array.isArray(state.ops.knowledgeTemporalMarkers) ? state.ops.knowledgeTemporalMarkers : [];
  const dreams = Array.isArray(state.ops.knowledgeDreamRuns) ? state.ops.knowledgeDreamRuns : [];
  const pendingDreams = dreams.filter((item) => String(sandboxField(item, 'review_status', 'ReviewStatus') || '') === 'pending').length;
  const latest = personal[0] || creative[0] || news[0] || intake[0] || null;
  return {
    title: 'Knowledge Memory',
    big: String(personal.length) + '/' + String(creative.length),
    sub: personal.length || creative.length || news.length || intake.length || temporal.length || dreams.length ? ('daily intake: ' + String(intake.length) + ' news: ' + String(news.length) + '\ntemporal: ' + String(temporal.length) + ' dream pending: ' + String(pendingDreams) + '\nlatest: ' + String(sandboxField(latest, 'title', 'Title') || sandboxField(latest, 'topic', 'Topic') || sandboxField(latest, 'entry_id', 'EntryID') || '-') + '\nreview-only: promote not verified') : 'knowledge memory record なし\nblocked: empty ledger\nblocked: no memory promote verified',
  };
}

function hobbyGraphOpsCard() {
  const fetchError = String(state.ops.hobbyGraphOverviewFetchError || '');
  if (fetchError) {
    return {
      title: 'Hobby Graph',
      big: 'unavailable',
      sub: 'hobby graph overview fetch failed: ' + compactOpsDetail(fetchError, 120),
    };
  }
  const overview = state.ops.hobbyGraphOverview || null;
  if (!overview) {
    return {
      title: 'Hobby Graph',
      big: 'not checked',
      sub: 'overview not loaded',
    };
  }
  if (overview.available === false) {
    return {
      title: 'Hobby Graph',
      big: 'no DB',
      sub: String(overview.error || 'hobby graph database not found') + '\npath: ' + String(overview.db_path || '-'),
    };
  }
  const stats = overview.stats && typeof overview.stats === 'object' ? overview.stats : {};
  const items = Number(stats.hobby_items || 0);
  const relations = Number(stats.hobby_relations || 0);
  const topics = Number(stats.hobby_topic_candidates || 0);
  const latestCandidate = Array.isArray(overview.topic_candidates) && overview.topic_candidates.length ? overview.topic_candidates[0] : null;
  const latestRelation = Array.isArray(overview.relations) && overview.relations.length ? overview.relations[0] : null;
  const relationText = latestRelation
    ? String(latestRelation.from_title || latestRelation.from_item_id || '-') + ' -> ' + String(latestRelation.relation_type || '-') + ' -> ' + String(latestRelation.to_title || latestRelation.to_item_id || '-')
    : 'relation なし';
  const candidateText = latestCandidate
    ? String(latestCandidate.title || latestCandidate.reason || latestCandidate.candidate_id || '-')
    : 'topic candidate なし';
  return {
    title: 'Hobby Graph',
    big: String(items) + ' / ' + String(relations) + ' / ' + String(topics),
    sub: 'items / relations / topics\nlatest topic: ' + compactOpsDetail(candidateText, 88) + '\nlatest relation: ' + compactOpsDetail(relationText, 88),
  };
}

function runtimeBlockedRoutesOpsCard() {
  const routes = Array.isArray(state.ops.runtimeBlockedRoutes) ? state.ops.runtimeBlockedRoutes : [];
  const blocked = routes.filter((item) => !Boolean(item.ok)).length;
  const unavailable = routes.filter((item) => Number(item.status || 0) === 503).length;
  return {
    title: 'Runtime Blocked Routes',
    big: String(blocked) + '/' + String(routes.length),
    sub: routes.length ? ('503 unavailable: ' + String(unavailable) + '\n' + routes.map((item) => String(item.label || item.path || '-') + ': HTTP ' + String(item.status || 0)).join('\n') + '\nblocked: dependency unavailable') : 'blocked route checks 未取得',
  };
}

function renderRuntimeBlockedRouteAudits() {
  const body = document.getElementById('runtimeBlockedRouteAuditBody');
  if (!body) return;
  body.innerHTML = '';
  const routes = Array.isArray(state.ops.runtimeBlockedRoutes) ? state.ops.runtimeBlockedRoutes : [];
  if (!routes.length) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="4" class="small">No runtime blocked route checks yet</td>';
    body.appendChild(tr);
    renderRuntimeBlockedRouteAuditResult();
    return;
  }
  routes.forEach((item) => {
    const ok = Boolean(item.ok);
    const status = Number(item.status || 0);
    const result = ok ? 'available' : (status === 503 ? 'blocked' : 'failed');
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td class="code">' + esc(item.path || item.label || '-') + '</td>' +
      '<td><span class="badge ' + stateClass(ok ? 'idle' : 'unavailable') + '">HTTP ' + esc(String(status)) + '</span></td>' +
      '<td>' + esc(result) + '</td>' +
      '<td>' + esc(short(item.body || '-', 160)) + '</td>';
    body.appendChild(tr);
  });
  renderRuntimeBlockedRouteAuditResult();
}

function renderRuntimeBlockedRouteAuditResult() {
  const el = document.getElementById('runtimeBlockedRouteAuditResult');
  if (!el) return;
  const routes = Array.isArray(state.ops.runtimeBlockedRoutes) ? state.ops.runtimeBlockedRoutes : [];
  const blocked = routes.filter((item) => !Boolean(item.ok)).length;
  const unavailable = routes.filter((item) => Number(item.status || 0) === 503).length;
  const available = routes.filter((item) => Boolean(item.ok)).length;
  el.textContent = 'runtime blocked route audits: ' + String(routes.length) + ' checked / ' + String(blocked) + ' blocked / ' + String(unavailable) + ' unavailable / ' + String(available) + ' available\nblocked: Source Registry staging, Memory Layers, Sandbox, and LLM Ops require their runtime dependencies';
}

function renderKnowledgeMemoryDetailFocus(focusBody) {
  const candidates = knowledgeMemoryDetailCandidates();
  const controls = candidates.map((candidate) => {
    return '<button class="ctl-btn" onclick="fetchKnowledgeMemoryDetail(&quot;' + esc(candidate.type) + '&quot;,&quot;' + esc(candidate.id) + '&quot;)">' + esc(candidate.label) + '</button>';
  }).join(' ');
  const detail = state.ops.knowledgeMemoryDetail || null;
  const tr = document.createElement('tr');
  tr.innerHTML = '<td>Knowledge Memory Detail</td><td>' + (controls || '<span class="small">detail候補なし</span>') + '</td>';
  focusBody.appendChild(tr);
  const detailRow = document.createElement('tr');
  detailRow.innerHTML = '<td>Knowledge Detail Result</td><td class="code">' + esc(detail ? JSON.stringify(detail, null, 2) : '-') + '</td>';
  focusBody.appendChild(detailRow);
}

function knowledgeMemoryDetailCandidates() {
  const out = [];
  const personal = Array.isArray(state.ops.knowledgePersonalArchive) ? state.ops.knowledgePersonalArchive : [];
  const creative = Array.isArray(state.ops.knowledgeCreativeItems) ? state.ops.knowledgeCreativeItems : [];
  const news = Array.isArray(state.ops.knowledgeNewsItems) ? state.ops.knowledgeNewsItems : [];
  const intake = Array.isArray(state.ops.knowledgeDailyIntakeRules) ? state.ops.knowledgeDailyIntakeRules : [];
  const temporal = Array.isArray(state.ops.knowledgeTemporalMarkers) ? state.ops.knowledgeTemporalMarkers : [];
  const dreams = Array.isArray(state.ops.knowledgeDreamRuns) ? state.ops.knowledgeDreamRuns : [];
  if (personal[0]) out.push({type: 'personal_archive', id: String(sandboxField(personal[0], 'entry_id', 'EntryID') || ''), label: 'Personal'});
  if (creative[0]) out.push({type: 'creative_knowledge', id: String(sandboxField(creative[0], 'item_id', 'ItemID') || ''), label: 'Creative'});
  if (news[0]) out.push({type: 'news_knowledge', id: String(sandboxField(news[0], 'item_id', 'ItemID') || ''), label: 'News'});
  if (intake[0]) out.push({type: 'daily_intake_rule', id: String(sandboxField(intake[0], 'rule_id', 'RuleID') || ''), label: 'Intake'});
  if (temporal[0]) out.push({type: 'temporal_marker', id: String(sandboxField(temporal[0], 'marker_id', 'MarkerID') || ''), label: 'Temporal'});
  if (dreams[0]) out.push({type: 'dream_run', id: String(sandboxField(dreams[0], 'run_id', 'RunID') || ''), label: 'Dream'});
  return out.filter((candidate) => candidate.id);
}

function fetchKnowledgeMemoryDetail(detailType, id) {
  const type = String(detailType || '').trim();
  const detailID = String(id || '').trim();
  if (!type || !detailID) return;
  fetch('/viewer/knowledge-memory?detail_type=' + encodeURIComponent(type) + '&id=' + encodeURIComponent(detailID) + '&limit=100')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'knowledge memory detail unavailable'));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.ops.knowledgeMemoryDetail = data;
      renderOps();
    })
    .catch((err) => {
      state.ops.knowledgeMemoryDetail = {error: String(err && err.message ? err.message : err), detail_type: type, id: detailID};
      renderOps();
      console.error(err);
    });
}

let dciSearchBound = false;
function bindDCISearchControls() {
  if (dciSearchBound) return;
  dciSearchBound = true;
  const input = document.getElementById('dciSearchInput');
  const button = document.getElementById('dciSearchBtn');
  if (!input || !button) return;
  const run = () => {
    const query = String(input.value || '').trim();
    if (!query) {
      state.ops.dciLastResult = {pack: {query: ''}, trace: {status: 'query_required'}};
      renderDCISearchResult();
      return;
    }
    button.disabled = true;
    fetch('/viewer/dci/search', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({query}),
    })
      .then((r) => {
        if (!r.ok) {
          return r.text().then((text) => {
            throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'dci search unavailable'));
          });
        }
        return r.json();
      })
      .then((data) => {
        state.ops.dciLastResult = data;
        renderDCISearchResult();
        refreshDCIData();
      })
      .catch((err) => {
        state.ops.dciLastResult = {pack: {query}, trace: {status: 'failed', error_message: String(err && err.message ? err.message : err)}};
        renderDCISearchResult();
      })
      .finally(() => {
        button.disabled = false;
      });
  };
  button.addEventListener('click', run);
  input.addEventListener('keydown', (ev) => {
    if (ev.key === 'Enter') run();
  });
}

let llmOpsUIBound = false;
function bindLLMOpsButtons() {
  if (llmOpsUIBound) return;
  llmOpsUIBound = true;
  const refresh = document.getElementById('llmOpsRefreshBtn');
  const stopBtn = document.getElementById('llmOpsStopBtn');
  const restartBtn = document.getElementById('llmOpsRestartBtn');
  if (refresh) refresh.addEventListener('click', refreshLlmOpsStatus);
  if (stopBtn) stopBtn.addEventListener('click', llmOpsStopChatWorker);
  if (restartBtn) restartBtn.addEventListener('click', llmOpsRestartAllRoles);
}

function syncLLMOpsPanel(cfg, fetchError) {
  const panel = document.getElementById('llmOpsPanel');
  if (!panel) return;
  const runtimeConfigError = String(fetchError || '').trim();
  const configured = Boolean(cfg && cfg.llm_ops_configured);
  const enabled = Boolean(cfg && cfg.llm_ops_enabled);
  const baseURL = cfg && cfg.llm_ops_base_url ? String(cfg.llm_ops_base_url) : '';
  state.ops.runtimeConfigFetchError = runtimeConfigError;
  state.ops.llmOpsConfigured = configured;
  state.ops.llmOpsBaseURL = baseURL;
  state.ops.localLLM = cfg && cfg.local_llm ? cfg.local_llm : null;
  state.ops.webGather = cfg && cfg.web_gather ? cfg.web_gather : null;
  state.ops.webwrightFetch = cfg && cfg.webwright_fetch ? cfg.webwright_fetch : null;
  state.ops.browserActor = cfg && cfg.browser_actor ? cfg.browser_actor : null;
  state.ops.runtimeReadiness = cfg && cfg.runtime_readiness ? cfg.runtime_readiness : null;
  state.ops.runtimeSTTBaseURL = cfg && cfg.stt_base_url ? String(cfg.stt_base_url) : '';
  state.ops.runtimeSTTStreamURL = cfg && cfg.stt_stream_url ? String(cfg.stt_stream_url) : '';
  state.ops.runtimeTTSBaseURL = cfg && cfg.tts_base_url ? String(cfg.tts_base_url) : '';
  if (typeof syncChatRouteAliasesFromRuntimeConfig === 'function') {
    syncChatRouteAliasesFromRuntimeConfig(state.ops.localLLM);
  }
  state.ops.llmOpsEnabled = enabled;
  bindLLMOpsButtons();
  const configEl = document.getElementById('llmOpsConfigState');
  const refresh = document.getElementById('llmOpsRefreshBtn');
  const stopBtn = document.getElementById('llmOpsStopBtn');
  const restartBtn = document.getElementById('llmOpsRestartBtn');
  [refresh, stopBtn, restartBtn].forEach((btn) => {
    if (btn) btn.disabled = !enabled;
  });
  if (configEl) {
    if (runtimeConfigError) {
      configEl.innerHTML = '<span class="badge state-error">unavailable</span> ' + esc('runtime config unavailable: ' + runtimeConfigError);
    } else if (enabled) {
      configEl.innerHTML = '<span class="badge state-running">enabled</span> ' + esc(baseURL || 'llm_ops configured');
    } else if (configured) {
      configEl.innerHTML = '<span class="badge state-error">token missing</span> ' + esc(baseURL || 'llm_ops configured') + '<div class="ops-sub">LLM_OPS_TOKEN が未設定のためViewerプロキシは無効です</div>';
    } else {
      configEl.innerHTML = '<span class="badge state-offline">disabled</span><div class="ops-sub">~/.picoclaw/config.yaml に llm_ops.enabled/base_url がありません</div>';
    }
  }
  renderLocalLLMRuntimeConfig();
  renderWebGatherOpsStatus();
  renderRuntimeDependencyReadiness();
  refreshRuntimeHealthStatus();
  if (enabled) refreshLlmOpsStatus();
  else {
    state.ops.llmStatus = null;
    state.ops.llmStatusError = runtimeConfigError ? ('runtime config unavailable: ' + runtimeConfigError) : (configured ? 'LLM_OPS_TOKEN missing' : 'llm_ops disabled');
    renderLlmMemoryStatus();
    setLlmOpsStatusPre(state.ops.llmStatusError);
  }
}

function syncRuntimeDebugSystem(snapshot, fetchError) {
  state.ops.runtimeDebugSystemFetchError = String(fetchError || '').trim();
  state.ops.runtimeDebugSystem = snapshot && typeof snapshot === 'object' ? snapshot : null;
  renderRuntimeDependencyReadiness();
}

function renderWebGatherOpsStatus() {
  const el = document.getElementById('webGatherOpsCards');
  if (!el) return;
  if (state.ops.runtimeConfigFetchError) {
    el.innerHTML = '<div class="debug-empty">web_gather runtime config unavailable: ' + esc(state.ops.runtimeConfigFetchError) + '</div>';
    return;
  }
  const wg = state.ops.webGather || {};
  const ww = state.ops.webwrightFetch || {};
  const rows = [
    {
      title: 'Web Gather Fetch',
      state: 'running',
      badge: 'running',
      big: 'http',
      sub: [
        wg.fetch_cache ? 'fetch cache=on' : 'fetch cache=off',
        wg.failure_cache ? 'failure cache=on' : 'failure cache=off',
        wg.rate_state ? 'rate state=on' : 'rate state=off',
      ].join('\n'),
    },
    {
      title: 'Discovery',
      state: 'configured',
      badge: 'thinking',
      big: ['local_cache', 'rss_atom', 'sitemap'].concat(wg.searxng_configured ? ['searxng'] : [], wg.yacy_configured ? ['yacy'] : []).join(' / '),
      sub: [wg.searxng_base_url ? 'searxng=' + wg.searxng_base_url : '', wg.yacy_base_url ? 'yacy=' + wg.yacy_base_url : ''].filter(Boolean).join('\n') || 'local providers only',
    },
    {
      title: 'Webwright',
      state: ww.enabled ? 'enabled' : 'disabled',
      badge: ww.enabled ? 'running' : 'offline',
      big: ww.enabled ? (ww.model || 'Coder1') : '-',
      sub: [
        ww.responses_endpoint ? 'responses=' + ww.responses_endpoint : '',
        ww.runner_path ? 'runner=' + ww.runner_path : '',
        ww.staging_output_dir ? 'staging=' + ww.staging_output_dir : '',
      ].filter(Boolean).join('\n') || 'webwright_fetch.enabled=false',
    },
  ];
  el.innerHTML = rows.map((row) => (
    '<div class="llm-runtime-card">' +
      '<div class="ops-card-title">' + esc(row.title) + '<span class="badge ' + stateClass(row.badge) + '">' + esc(row.state) + '</span></div>' +
      '<div class="llm-runtime-model">' + esc(row.big || '-') + '</div>' +
      '<div class="ops-sub">' + esc(row.sub || '-') + '</div>' +
    '</div>'
  )).join('');
}

function renderLocalLLMRuntimeConfig() {
  const el = document.getElementById('llmRuntimeConfigCards');
  if (!el) return;
  if (state.ops.runtimeConfigFetchError) {
    el.innerHTML = '<div class="debug-empty">local_llm runtime config unavailable: ' + esc(state.ops.runtimeConfigFetchError) + '</div>';
    return;
  }
  const localLLM = state.ops.localLLM || {};
  const liveModels = localLLM.live_models || {};
  if (!localLLM.enabled) {
    el.innerHTML = '<div class="debug-empty">local_llm disabled</div>';
    return;
  }
  const rows = [
    llmRuntimeRoleRow('Chat', localLLM.chat_model, localLLM.chat_base_url, '', liveModels.chat),
    llmRuntimeRoleRow('Worker', localLLM.worker_model, localLLM.worker_base_url, '', liveModels.worker),
    llmRuntimeRoleRow('Heavy', localLLM.heavy_model, localLLM.heavy_base_url,
      sameLocalLLMEndpoint(localLLM.heavy_base_url, localLLM.worker_base_url, localLLM.heavy_model, localLLM.worker_model) ? 'shared' : '', liveModels.heavy),
    llmRuntimeRoleRow('Wild', localLLM.wild_model, localLLM.wild_base_url,
      sameLocalLLMEndpoint(localLLM.wild_base_url, localLLM.chat_base_url, localLLM.wild_model, localLLM.chat_model) ? 'shared' : '', liveModels.wild),
  ].filter((row) => row.model || row.url);
  const params = [
    localLLM.provider ? 'provider=' + localLLM.provider : '',
    localLLM.timeout_sec ? 'timeout=' + localLLM.timeout_sec + 's' : '',
    localLLM.global_concurrency ? 'global=' + localLLM.global_concurrency : '',
    localLLM.model_concurrency ? 'model=' + localLLM.model_concurrency : '',
  ].filter(Boolean).join(' · ');
  el.innerHTML = rows.map((row) => (
    '<div class="llm-runtime-card">' +
      '<div class="ops-card-title">' + esc(row.role) + '<span class="badge ' + stateClass(row.stateClass || row.state) + '">' + esc(row.state) + '</span></div>' +
      '<div class="llm-runtime-model">' + esc(row.model || '-') + '</div>' +
      '<div class="llm-runtime-url">' + esc(row.url || '-') + '/v1/chat/completions</div>' +
      (row.meta ? '<div class="ops-sub">' + esc(row.meta) + '</div>' : '') +
    '</div>'
  )).join('') + (params ? '<div class="ops-sub">' + esc(params) + '</div>' : '');
}

function renderRuntimeDependencyReadiness() {
  const el = document.getElementById('runtimeReadinessCards');
  if (!el) return;
  const runtimeConfigError = String(state.ops.runtimeConfigFetchError || '').trim();
  const runtimeDebugError = String(state.ops.runtimeDebugSystemFetchError || '').trim();
  const readiness = state.ops.runtimeReadiness || {};
  const browserActor = state.ops.browserActor || {};
  const audio = state.ops.runtimeDebugSystem && state.ops.runtimeDebugSystem.audio ? state.ops.runtimeDebugSystem.audio : null;
  const sttItems = [
    runtimeReadinessItem('env', readiness.stt_gateway_env_present),
    runtimeReadinessItem('config', readiness.stt_gateway_config_present),
  ];
  if (audio && (audio.stt_base_url || readiness.stt_gateway_config_present === true)) {
    sttItems.push(runtimeReadinessItem('health', audio.stt_ok));
  }
  const ttsItems = [
    runtimeReadinessItem('env', readiness.tts_provider_env_present),
    runtimeReadinessItem('config', readiness.tts_provider_config_present),
  ];
  if (audio && (audio.tts_base_url || readiness.tts_provider_config_present === true)) {
    ttsItems.push(runtimeReadinessItem('live', audio.tts_live_ok));
    ttsItems.push(runtimeReadinessItem('ready', audio.tts_ready_ok));
  }
  const audioError = audio && audio.last_error ? 'blocked: ' + String(audio.last_error) : (runtimeDebugError ? 'blocked: ' + runtimeDebugError : '');
  const sttDetail = [state.ops.runtimeSTTBaseURL || state.ops.runtimeSTTStreamURL || '', audioError, 'blocked: real microphone STT E2E not verified'].filter(Boolean).join('\n');
  const ttsDetail = [state.ops.runtimeTTSBaseURL || '', audioError, 'blocked: browser audio playback/lip sync E2E not verified'].filter(Boolean).join('\n');
  const externalChannelDetail = 'blocked: real external API file event E2E not verified';
  const distributedDetail = [
    readiness.distributed_enabled ? 'distributed runtime configured' : 'blocked: distributed disabled',
    'blocked: Wild SSH/multi-machine E2E not verified',
  ].join('\n');
  const browserActorDetail = browserActor.enabled ? [
    'runner: ' + String(browserActor.runner_path || '-'),
    'browser: ' + String(browserActor.browser || '-'),
    'profile: ' + String(browserActor.profile_root || '-'),
    'artifacts: ' + String(browserActor.artifact_root || '-'),
    'origins: ' + String(browserActor.allowed_origin_count || 0),
  ].join('\n') : 'blocked: browser_actor disabled';
  const runtimeHealth = state.ops.runtimeHealth || null;
  const runtimeHealthChecks = runtimeHealthChecksByName(runtimeHealth);
  const runtimeHealthChat = runtimeHealthChecks.local_llm_chat || runtimeHealthChecks.chat || null;
  const runtimeHealthWorker = runtimeHealthChecks.local_llm_worker || runtimeHealthChecks.worker || null;
  const runtimeHealthDetail = runtimeHealthDetailText(runtimeHealth, state.ops.runtimeHealthError);
  const rows = [
    runtimeConfigError ? runtimeReadinessCard('Runtime Config', [
      runtimeReadinessItem('config', false),
    ], 'blocked: ' + runtimeConfigError) : '',
    runtimeReadinessCard('Runtime Health', [
      runtimeReadinessItem('service', runtimeHealth && runtimeHealth.status === 'ok'),
      runtimeReadinessItem('chat', runtimeHealthCheckOK(runtimeHealthChat)),
      runtimeReadinessItem('worker', runtimeHealthCheckOK(runtimeHealthWorker)),
    ], runtimeHealthDetail),
    runtimeReadinessCard('LLM Ops', [
      runtimeReadinessItem('configured', state.ops.llmOpsConfigured),
      runtimeReadinessItem('proxy', state.ops.llmOpsEnabled),
      runtimeReadinessItem('live', state.ops.llmStatus != null),
    ], [state.ops.llmOpsBaseURL || '', state.ops.llmStatusError ? 'blocked: ' + String(state.ops.llmStatusError) : ''].filter(Boolean).join('\n')),
    runtimeReadinessCard('Slack', [
      runtimeReadinessItem('credentials', readiness.slack_credentials_present),
      runtimeReadinessItem('webhook', readiness.slack_webhook_registered),
      runtimeReadinessItem('file', readiness.slack_file_payload_pipeline),
    ], externalChannelDetail),
    runtimeReadinessCard('Discord', [
      runtimeReadinessItem('credentials', readiness.discord_credentials_present),
      runtimeReadinessItem('webhook', readiness.discord_webhook_registered),
      runtimeReadinessItem('file', readiness.discord_file_payload_pipeline),
    ], externalChannelDetail),
    runtimeReadinessCard('Telegram', [
      runtimeReadinessItem('credentials', readiness.telegram_credentials_present),
      runtimeReadinessItem('webhook', readiness.telegram_webhook_registered),
      runtimeReadinessItem('file', readiness.telegram_file_payload_pipeline),
    ], externalChannelDetail),
    runtimeReadinessCard('STT', sttItems, sttDetail),
    runtimeReadinessCard('TTS', ttsItems, ttsDetail),
    runtimeReadinessCard('Distributed', [
      runtimeReadinessItem('enabled', readiness.distributed_enabled),
      runtimeReadinessItem('transport', readiness.distributed_transports_present),
      runtimeReadinessItem('ssh-config', readiness.distributed_ssh_configured),
      runtimeReadinessItem('ssh-connected', readiness.distributed_ssh_connected),
      runtimeReadinessItem('local', readiness.distributed_local_transport),
    ], distributedDetail),
    runtimeReadinessCard('Source Registry', [
      runtimeReadinessItem('conversation', readiness.conversation_enabled),
      runtimeReadinessItem('l1', readiness.l1_sqlite_config_present),
      runtimeReadinessItem('memory-layers', readiness.memory_layers_available),
      runtimeReadinessItem('memory-route', readiness.memory_layers_status_available),
      runtimeReadinessItem('source', readiness.source_registry_available),
      runtimeReadinessItem('source-route', readiness.source_registry_status_available),
    ], readiness.source_registry_available ? '/viewer/source-registry' : 'blocked: conversation L1 disabled'),
    runtimeReadinessCard('Knowledge Memory', [
      runtimeReadinessItem('enabled', readiness.knowledge_memory_enabled),
      runtimeReadinessItem('status', readiness.knowledge_memory_status_available),
    ], readiness.knowledge_memory_enabled ? '/viewer/knowledge-memory' : 'blocked: knowledge memory disabled'),
    runtimeReadinessCard('Browser Trace API', [
      runtimeReadinessItem('enabled', readiness.browser_trace_api_enabled),
      runtimeReadinessItem('status', readiness.browser_trace_api_status_available),
      runtimeReadinessItem('fetcher', readiness.browser_trace_api_fetcher_available),
    ], readiness.browser_trace_api_enabled ? 'review-only: discover and fetcher proposal require evidence' : 'blocked: browser trace API disabled'),
    runtimeReadinessCard('Browser Actor', [
      runtimeReadinessItem('enabled', browserActor.enabled),
      runtimeReadinessItem('headless', browserActor.headless_default),
      runtimeReadinessItem('trace', browserActor.save_trace),
      runtimeReadinessItem('mask', browserActor.mask_secrets),
    ], browserActorDetail),
    runtimeReadinessCard('Sandbox', [
      runtimeReadinessItem('enabled', readiness.sandbox_enabled),
      runtimeReadinessItem('status', readiness.sandbox_status_available),
    ], readiness.sandbox_enabled ? '/viewer/sandbox' : 'blocked: sandbox disabled'),
  ].filter(Boolean);
  el.innerHTML = rows.join('');
  if (typeof refreshOpsTriageFromState === 'function') refreshOpsTriageFromState();
}

function runtimeReadinessItem(label, value) {
  const ok = value === true;
  return '<span class="badge ' + stateClass(ok ? 'running' : 'offline') + '">' + esc(label + ':' + (ok ? 'present' : 'missing')) + '</span>';
}

function runtimeReadinessCard(title, items, detail) {
  return '<div class="llm-runtime-card">' +
    '<div class="ops-card-title">' + esc(title) + '</div>' +
    '<div class="runtime-readiness-badges">' + items.join('') + '</div>' +
    (detail ? '<div class="llm-runtime-url">' + esc(detail) + '</div>' : '') +
  '</div>';
}

function runtimeHealthChecksByName(report) {
  const out = {};
  const checks = report && Array.isArray(report.checks) ? report.checks : [];
  checks.forEach((check) => {
    if (!check || !check.name) return;
    out[String(check.name).toLowerCase()] = check;
  });
  return out;
}

function runtimeHealthCheckOK(check) {
  return Boolean(check && String(check.status || '').toLowerCase() === 'ok');
}

function runtimeHealthDetailText(report, errorText) {
  if (errorText) return 'blocked: ' + String(errorText);
  if (!report) return 'blocked: /health not checked yet';
  const checks = Array.isArray(report.checks) ? report.checks : [];
  const blocked = checks.filter((check) => check && String(check.status || '').toLowerCase() !== 'ok');
  if (!blocked.length) return '/health';
  return 'blocked: ' + blocked.map((check) => {
    const name = check.name || 'check';
    const message = check.message || check.status || 'down';
    return String(name) + ': ' + String(message);
  }).join('; ');
}

function llmRuntimeRoleRow(role, configModel, configURL, configuredState, live) {
  const status = state.ops.llmStatus || {};
  const roleState = status.roles && status.roles[role] ? status.roles[role] : null;
  const memoryRole = status.memory && status.memory.llm_by_role && status.memory.llm_by_role[role]
    ? status.memory.llm_by_role[role]
    : null;
  const livePort = memoryRole && memoryRole.port != null ? Number(memoryRole.port) : null;
  const liveURL = Number.isFinite(livePort) ? replaceURLPort(configURL, livePort) : '';
  const liveModel = memoryRole && memoryRole.model ? String(memoryRole.model) : '';
  const serverModel = liveLLMEffectiveModel(live);
  const alias = live && live.alias ? String(live.alias) : String(configModel || '');
  const pid = memoryRole && memoryRole.pid != null ? 'pid ' + String(memoryRole.pid) : '';
  const liveState = liveLLMState(live);
  let runtimeState = configuredState || 'configured';
  let stateClassName = configuredState === 'shared' ? 'thinking' : 'offline';

  if (roleState) {
    if (roleState.halted) {
      runtimeState = 'halted';
      stateClassName = 'error';
    } else if (roleState.health_ok === false) {
      runtimeState = 'unhealthy';
      stateClassName = 'error';
    } else if (roleState.health_ok === true || pid) {
      runtimeState = 'running';
      stateClassName = 'running';
    }
  } else if (memoryRole && memoryRole.pid != null) {
    runtimeState = 'running';
    stateClassName = 'running';
  } else if (liveState.state) {
    runtimeState = liveState.state;
    stateClassName = liveState.stateClass;
  }

  const meta = [
    alias ? 'alias=' + alias : '',
    serverModel && alias && serverModel !== alias ? 'server=' + serverModel : '',
    live && live.default_model && live.default_model !== serverModel ? 'default=' + live.default_model : '',
    pid,
    live && live.error ? 'probe=' + live.error : '',
  ].filter(Boolean).join('\n');

  return {
    role,
    model: liveModel || serverModel || configModel,
    url: liveURL || (live && live.base_url ? live.base_url : '') || configURL,
    state: runtimeState,
    stateClass: stateClassName,
    meta,
  };
}

function liveLLMEffectiveModel(live) {
  if (!live || typeof live !== 'object') return '';
  return String(live.backend_model || live.loaded_model || '').trim();
}

function liveLLMState(live) {
  if (!live || typeof live !== 'object') return {state: '', stateClass: ''};
  if (live.error && !live.backend_model && !live.loaded_model) return {state: 'probe error', stateClass: 'error'};
  if (live.loaded === true) return {state: 'loaded', stateClass: 'running'};
  if (live.loaded === false) return {state: 'not loaded', stateClass: 'offline'};
  const status = String(live.status || '').toLowerCase();
  if (status === 'healthy' || status === 'ok') return {state: 'healthy', stateClass: 'running'};
  return {state: live.backend_model || live.loaded_model ? 'resolved' : '', stateClass: live.backend_model || live.loaded_model ? 'running' : ''};
}

function replaceURLPort(rawURL, port) {
  const text = String(rawURL || '').trim();
  if (!text) return 'http://127.0.0.1:' + String(port);
  try {
    const parsed = new URL(text);
    parsed.port = String(port);
    return parsed.toString().replace(/\/+$/, '');
  } catch (_) {
    return text.replace(/:\d+(\/.*)?$/, ':' + String(port));
  }
}

function sameLocalLLMEndpoint(urlA, urlB, modelA, modelB) {
  return String(urlA || '').replace(/\/+$/, '') === String(urlB || '').replace(/\/+$/, '') &&
    String(modelA || '') === String(modelB || '');
}

function setLlmOpsStatusPre(text) {
  const el = document.getElementById('llmOpsStatusPre');
  if (el) el.textContent = text == null ? '' : String(text);
}

function llmRoleMemoryState(role, info) {
  if (!info || info.pid == null || info.rss_mib == null) return 'offline';
  const roleState = state.ops.llmStatus && state.ops.llmStatus.roles && state.ops.llmStatus.roles[role];
  if (roleState && roleState.halted) return 'error';
  if (roleState && roleState.health_ok === false) return 'error';
  return 'running';
}

function renderLlmMemoryStatus() {
  const cards = document.getElementById('llmMemoryCards');
  const systemBar = document.getElementById('llmMemorySystemBar');
  const processListsEl = document.getElementById('llmMemoryProcessLists');
  const rolesEl = document.getElementById('llmMemoryRoles');
  if (!cards || !systemBar || !rolesEl) return;

  const status = state.ops.llmStatus || {};
  const localLLM = state.ops.localLLM || {};
  const memory = status.memory || {};
  const system = memory.system || {};
  const byRole = memory.llm_by_role || {};
  const totalGiB = num(system.total_gib) || (num(system.total_bytes) / 1073741824);
  const usedGiB = num(system.used_gib) || (num(system.used_bytes) / 1073741824);
  const freeGiB = num(system.free_gib) || (num(system.free_bytes) / 1073741824);
  const availableGiB = memoryGiB(system, ['available', 'available_for_llm', 'safe_available_for_llm']);
  const swapUsedGiB = memoryGiB(system, ['swap_used', 'swap.used', 'swap_used_for_llm']);
  const compressedGiB = memoryGiB(system, ['compressed', 'compressed_memory']);
  const fileCacheGiB = memoryGiB(system, ['file_cache', 'cache', 'cached']);
  const wiredGiB = memoryGiB(system, ['wired', 'wired_memory']);
  const availableForLLMGiB = memoryGiB(system, ['available_for_llm']);
  const usedForLLMGiB = memoryGiB(system, ['used_for_llm']);
  const safeAvailableForLLMGiB = memoryGiB(system, ['safe_available_for_llm']);
  const safetyMarginGiB = memoryGiB(system, ['llm_safety_margin']);
  const usedPct = pct(usedGiB, totalGiB);
  const freePct = pct(freeGiB, totalGiB);
  const chatRSSMiB = roleRSSMiB(byRole.Chat);
  const workerRSSMiB = roleRSSMiB(byRole.Worker);

  cards.innerHTML = [
    {title: 'Total RAM', big: fmtGiB(totalGiB), sub: system.total_bytes ? fmtBytesAsGiB(system.total_bytes) : 'memory.system.total_gib', indicator: memoryIndicator('none')},
    {title: 'Used RAM', big: fmtGiB(usedGiB), sub: usedPct.toFixed(1) + '% used', indicator: memoryIndicatorForUsedPct(usedPct)},
    {title: 'Free RAM', big: fmtGiB(freeGiB), sub: freePct.toFixed(1) + '% free', indicator: memoryIndicatorForFreePct(freePct)},
    {title: 'Available RAM', big: fmtReportedGiB(availableGiB), sub: memorySourceLabel(system, ['available', 'available_for_llm', 'safe_available_for_llm']), indicator: memoryIndicatorForAvailable(availableGiB)},
    {title: 'Swap Used', big: fmtReportedGiB(swapUsedGiB), sub: memorySourceLabel(system, ['swap_used', 'swap.used', 'swap_used_for_llm']), indicator: memoryIndicatorForSwap(swapUsedGiB)},
    {title: 'Memory Pressure', big: fmtMemoryPressure(system), sub: memorySourceLabel(system, ['memory_pressure', 'pressure', 'memory_pressure_percent']), indicator: memoryIndicatorForPressure(system)},
    {title: 'Compressed', big: fmtReportedGiB(compressedGiB), sub: memorySourceLabel(system, ['compressed', 'compressed_memory']), indicator: memoryIndicatorForCompressed(compressedGiB, totalGiB)},
    {title: 'File Cache', big: fmtReportedGiB(fileCacheGiB), sub: memorySourceLabel(system, ['file_cache', 'cache', 'cached']), indicator: memoryIndicator('none')},
    {title: 'Wired', big: fmtReportedGiB(wiredGiB), sub: memorySourceLabel(system, ['wired', 'wired_memory']), indicator: memoryIndicatorForWired(wiredGiB, totalGiB)},
    {title: 'Available for LLM', big: fmtReportedGiB(availableForLLMGiB), sub: memorySourceLabel(system, ['available_for_llm']), indicator: memoryIndicatorForAvailable(availableForLLMGiB)},
    {title: 'Used for LLM', big: fmtReportedGiB(usedForLLMGiB), sub: memorySourceLabel(system, ['used_for_llm']), indicator: memoryIndicatorForUsedPct(pct(usedForLLMGiB, totalGiB))},
    {title: 'Safe Available', big: fmtReportedGiB(safeAvailableForLLMGiB), sub: memorySourceLabel(system, ['safe_available_for_llm']), indicator: memoryIndicatorForSafeAvailable(safeAvailableForLLMGiB)},
    {title: 'Safety Margin', big: fmtReportedGiB(safetyMarginGiB), sub: memorySourceLabel(system, ['llm_safety_margin']), indicator: memoryIndicator('none')},
    {title: 'Chat RSS', big: fmtGiBFromMiB(chatRSSMiB), sub: rolePIDLabel(byRole.Chat)},
    {title: 'Worker RSS', big: fmtGiBFromMiB(workerRSSMiB), sub: rolePIDLabel(byRole.Worker)},
  ].map((item) => (
    '<div class="llm-memory-card">' +
      '<div class="ops-card-title"><span>' + esc(item.title) + '</span>' + renderMemoryIndicator(item.indicator) + '</div>' +
      '<div class="ops-big">' + esc(item.big) + '</div>' +
      '<div class="ops-sub">' + esc(item.sub) + '</div>' +
    '</div>'
  )).join('');

  const barFill = systemBar.querySelector('span');
  if (barFill) barFill.style.width = usedPct.toFixed(1) + '%';
  systemBar.title = 'Used ' + usedPct.toFixed(1) + '% / Free ' + freePct.toFixed(1) + '%';

  if (processListsEl) {
    processListsEl.innerHTML =
      renderMemoryProcessList('Top Memory Processes', memoryList(memory, system, ['top_memory_processes', 'top_processes', 'processes'])) +
      renderMemoryProcessList('Model Processes', memoryList(memory, system, ['model_processes', 'llm_processes', 'models']));
  }

  const roles = Object.keys(byRole).sort((a, b) => {
    const order = {Chat: 0, Worker: 1};
    return (order[a] ?? 50) - (order[b] ?? 50) || a.localeCompare(b);
  });
  if (roles.length === 0) {
    const fallback = renderLocalLLMFallback(localLLM, state.ops.llmStatusError);
    rolesEl.innerHTML = fallback || (state.ops.llmStatusError
      ? '<div class="debug-empty">' + esc(state.ops.llmStatusError) + '</div>'
      : '<div class="debug-empty">memory.llm_by_role is empty</div>');
    return;
  }
  rolesEl.innerHTML = roles.map((role) => {
    const info = byRole[role] || {};
    const rssMiB = roleRSSMiB(info);
    const rssPct = pct(rssMiB, totalGiB * 1024);
    const st = llmRoleMemoryState(role, info);
    const pid = info.pid == null ? 'stopped' : 'pid ' + String(info.pid);
    return '<div class="llm-role-memory-item">' +
      '<div class="llm-role-memory-head">' +
        '<div><div class="llm-role-memory-title">' + esc(role) + '</div><div class="llm-role-memory-meta">' + esc(pid) + ' · ' + esc(fmtGiBFromMiB(rssMiB)) + ' RSS</div></div>' +
        '<span class="badge ' + stateClass(st) + '">' + esc(st) + '</span>' +
      '</div>' +
      '<div class="llm-role-memory-bar" title="' + escAttr(rssPct.toFixed(2) + '% of system RAM') + '"><span style="width:' + escAttr(rssPct.toFixed(2)) + '%"></span></div>' +
    '</div>';
  }).join('');
}

function memoryField(obj, names) {
  for (const name of names) {
    const parts = String(name).split('.');
    let cur = obj;
    for (const part of parts) {
      if (!cur || typeof cur !== 'object' || !(part in cur)) {
        cur = undefined;
        break;
      }
      cur = cur[part];
    }
    if (cur !== undefined && cur !== null && cur !== '') return {name, value: cur};
  }
  return {name: names[0], value: null};
}

function memoryGiB(system, bases) {
  const gibNames = bases.map((base) => base + '_gib');
  const byteNames = bases.map((base) => base + '_bytes');
  const gib = memoryField(system, gibNames);
  if (gib.value !== null) return num(gib.value);
  const bytes = memoryField(system, byteNames);
  if (bytes.value !== null) return num(bytes.value) / 1073741824;
  return null;
}

function fmtReportedGiB(value) {
  if (value === null || value === undefined || value === '') return '-';
  const n = Number(value);
  if (!Number.isFinite(n) || n < 0) return '-';
  return n.toFixed(n >= 10 ? 1 : 2) + ' GiB';
}

function memorySourceLabel(system, bases) {
  const names = bases.flatMap((base) => [base + '_gib', base + '_bytes', base]);
  const found = memoryField(system, names);
  if (found.value === null) return 'not reported';
  return 'memory.system.' + found.name;
}

function fmtMemoryPressure(system) {
  const found = memoryField(system, ['memory_pressure', 'pressure', 'memory_pressure_percent']);
  if (found.value === null) return '-';
  if (typeof found.value === 'number') {
    return found.name.endsWith('_percent') ? found.value.toFixed(1) + '%' : String(found.value);
  }
  return String(found.value);
}

function memoryIndicator(level, label) {
  const normalized = level || 'unknown';
  const labels = {ok: 'OK', warn: 'WARN', danger: 'DANGER', unknown: 'UNKNOWN', none: ''};
  const classes = {ok: 'running', warn: 'thinking', danger: 'error', unknown: 'offline', none: ''};
  return {level: normalized, label: label || labels[normalized] || 'UNKNOWN', state: classes[normalized] || 'offline'};
}

function renderMemoryIndicator(indicator) {
  if (!indicator || indicator.level === 'none') return '';
  return '<span class="llm-memory-indicator state-' + escAttr(indicator.state) + '">' + esc(indicator.label) + '</span>';
}

function memoryIndicatorForUsedPct(value) {
  if (value == null || !Number.isFinite(Number(value))) return memoryIndicator('unknown');
  if (value >= 95) return memoryIndicator('danger');
  if (value >= 90) return memoryIndicator('warn');
  return memoryIndicator('ok');
}

function memoryIndicatorForFreePct(value) {
  if (value == null || !Number.isFinite(Number(value))) return memoryIndicator('unknown');
  if (value <= 3) return memoryIndicator('danger');
  if (value <= 8) return memoryIndicator('warn');
  return memoryIndicator('ok');
}

function memoryIndicatorForAvailable(value) {
  const n = Number(value);
  if (!Number.isFinite(n)) return memoryIndicator('unknown');
  if (n < 4) return memoryIndicator('danger');
  if (n < 8) return memoryIndicator('warn');
  return memoryIndicator('ok');
}

function memoryIndicatorForSafeAvailable(value) {
  const n = Number(value);
  if (!Number.isFinite(n)) return memoryIndicator('unknown');
  if (n < 2) return memoryIndicator('danger');
  if (n < 4) return memoryIndicator('warn');
  return memoryIndicator('ok');
}

function memoryIndicatorForSwap(value) {
  const n = Number(value);
  if (!Number.isFinite(n)) return memoryIndicator('unknown');
  if (n >= 4) return memoryIndicator('danger');
  if (n >= 1) return memoryIndicator('warn');
  return memoryIndicator('ok');
}

function memoryIndicatorForCompressed(value, totalGiB) {
  const n = Number(value);
  const total = Number(totalGiB);
  if (!Number.isFinite(n) || !Number.isFinite(total) || total <= 0) return memoryIndicator('unknown');
  const ratio = (n / total) * 100;
  if (ratio >= 10) return memoryIndicator('danger');
  if (ratio >= 5) return memoryIndicator('warn');
  return memoryIndicator('ok');
}

function memoryIndicatorForWired(value, totalGiB) {
  const n = Number(value);
  const total = Number(totalGiB);
  if (!Number.isFinite(n) || !Number.isFinite(total) || total <= 0) return memoryIndicator('unknown');
  const ratio = (n / total) * 100;
  if (ratio >= 50) return memoryIndicator('danger');
  if (ratio >= 35) return memoryIndicator('warn');
  return memoryIndicator('ok');
}

function memoryIndicatorForPressure(system) {
  const text = String(memoryField(system, ['memory_pressure', 'pressure']).value || '').toLowerCase();
  if (text.includes('critical')) return memoryIndicator('danger');
  if (text.includes('warn')) return memoryIndicator('warn');
  if (text.includes('normal') || text.includes('ok')) return memoryIndicator('ok');
  const pctValue = memoryField(system, ['memory_pressure_percent']).value;
  const n = Number(pctValue);
  if (!Number.isFinite(n)) return memoryIndicator('unknown');
  if (n >= 98) return memoryIndicator('danger');
  if (n >= 90) return memoryIndicator('warn');
  return memoryIndicator('ok');
}

function memoryList(memory, system, names) {
  const fromMemory = memoryField(memory, names);
  if (Array.isArray(fromMemory.value)) return fromMemory.value;
  const fromSystem = memoryField(system, names);
  if (Array.isArray(fromSystem.value)) return fromSystem.value;
  return [];
}

function renderMemoryProcessList(title, rows) {
  const items = Array.isArray(rows) ? rows : [];
  const body = items.length ? items.slice(0, 8).map(renderMemoryProcessRow).join('') : '<div class="ops-sub">not reported</div>';
  return '<div class="llm-memory-process-list">' +
    '<div class="ops-card-title">' + esc(title) + '</div>' +
    body +
  '</div>';
}

function renderMemoryProcessRow(row) {
  if (row == null || typeof row !== 'object') {
    return '<div class="llm-memory-process-row"><span class="llm-memory-process-name">' + esc(String(row || '-')) + '</span><span class="llm-memory-process-meta">-</span></div>';
  }
  const name = row.name || row.command || row.process || row.model || row.role || ('pid ' + (row.pid == null ? '-' : row.pid));
  const rss = row.rss_gib != null ? fmtGiB(row.rss_gib) : fmtGiBFromMiB(row.rss_mib != null ? row.rss_mib : (num(row.rss_bytes) / 1048576));
  const pid = row.pid == null ? '' : 'pid ' + row.pid;
  const meta = [rss, pid].filter((part) => part && part !== '-').join(' · ') || '-';
  return '<div class="llm-memory-process-row"><span class="llm-memory-process-name">' + esc(name) + '</span><span class="llm-memory-process-meta">' + esc(meta) + '</span></div>';
}

function renderLocalLLMFallback(localLLM, errorText) {
  if (!localLLM || !localLLM.enabled) return '';
  const rows = [
    {role: 'Chat', model: localLLM.chat_model, url: localLLM.chat_base_url},
    {role: 'Worker', model: localLLM.worker_model, url: localLLM.worker_base_url},
    {role: 'Wild', model: localLLM.wild_model, url: localLLM.wild_base_url},
  ].filter((row) => row.model || row.url);
  if (!rows.length) return '';
  const note = errorText
    ? '<div class="debug-empty">' + esc(errorText) + '<div class="ops-sub">Mac管理APIが未到達のため、メモリ値は取得できません。推論API設定のみ表示しています。</div></div>'
    : '';
  const params = [
    localLLM.provider ? 'provider=' + localLLM.provider : '',
    localLLM.timeout_sec ? 'timeout=' + localLLM.timeout_sec + 's' : '',
    localLLM.global_concurrency ? 'global=' + localLLM.global_concurrency : '',
    localLLM.model_concurrency ? 'model=' + localLLM.model_concurrency : '',
  ].filter(Boolean).join(' · ');
  return note + rows.map((row) => (
    '<div class="llm-role-memory-item">' +
      '<div class="llm-role-memory-head">' +
        '<div><div class="llm-role-memory-title">' + esc(row.role) + '</div>' +
        '<div class="llm-role-memory-meta">' + esc(row.model || '-') + '</div>' +
        '<div class="ops-sub">' + esc(row.url || '-') + '</div></div>' +
        '<span class="badge state-offline">ops api down</span>' +
      '</div>' +
    '</div>'
  )).join('') + (params ? '<div class="ops-sub">' + esc(params) + '</div>' : '');
}

async function refreshRuntimeHealthStatus() {
  const controller = typeof AbortController === 'function' ? new AbortController() : null;
  const timer = controller ? setTimeout(() => controller.abort(), 3500) : null;
  try {
    const res = await fetch('/health', {
      cache: 'no-store',
      signal: controller ? controller.signal : undefined,
    });
    const body = await res.text();
    try {
      state.ops.runtimeHealth = JSON.parse(body);
      state.ops.runtimeHealthError = '';
    } catch (parseErr) {
      state.ops.runtimeHealth = null;
      state.ops.runtimeHealthError = 'HTTP ' + res.status + ': ' + String(parseErr);
    }
  } catch (err) {
    state.ops.runtimeHealth = null;
    state.ops.runtimeHealthError = err && err.name === 'AbortError' ? '/health timeout' : String(err);
  } finally {
    if (timer) clearTimeout(timer);
    renderRuntimeDependencyReadiness();
  }
}

function roleRSSMiB(info) {
  if (!info) return 0;
  return num(info.rss_mib) || (num(info.rss_bytes) / 1048576);
}

function rolePIDLabel(info) {
  if (!info || info.pid == null) return 'stopped';
  return 'pid ' + String(info.pid);
}

async function refreshLlmOpsStatus() {
  try {
    const res = await fetch('/viewer/llm-ops/status', { cache: 'no-store' });
    const body = await res.text();
    if (!res.ok) {
      state.ops.llmStatusError = 'HTTP ' + res.status + (body ? ': ' + body.trim() : '');
      setLlmOpsStatusPre('HTTP ' + res.status + '\n' + body);
      renderLlmMemoryStatus();
      renderRuntimeDependencyReadiness();
      return;
    }
    try {
      state.ops.llmStatus = JSON.parse(body);
      state.ops.llmStatusError = '';
      renderLlmMemoryStatus();
      renderRuntimeDependencyReadiness();
      setLlmOpsStatusPre(JSON.stringify(state.ops.llmStatus, null, 2));
    } catch (parseErr) {
      state.ops.llmStatusError = String(parseErr);
      setLlmOpsStatusPre(body);
      renderLlmMemoryStatus();
      renderRuntimeDependencyReadiness();
    }
  } catch (err) {
    state.ops.llmStatusError = String(err);
    setLlmOpsStatusPre(String(err));
    renderLlmMemoryStatus();
    renderRuntimeDependencyReadiness();
  }
}

async function llmOpsStopChatWorker() {
  if (!confirm('MLX 上の Chat と Worker を停止しますか？（自動復旧しません／halted まで停止）')) return;
  try {
    const res = await fetch('/viewer/llm-ops/stop', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ roles: ['Chat', 'Worker'] }),
    });
    const body = await res.text();
    setLlmOpsStatusPre((res.ok ? '' : 'HTTP ' + res.status + '\n') + body);
    await refreshLlmOpsStatus();
  } catch (err) {
    setLlmOpsStatusPre(String(err));
  }
}

async function llmOpsRestartAllRoles() {
  if (!confirm('管理対象ロールをすべて再起動しますか？')) return;
  try {
    const res = await fetch('/viewer/llm-ops/restart', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ roles: 'all' }),
    });
    const body = await res.text();
    setLlmOpsStatusPre((res.ok ? '' : 'HTTP ' + res.status + '\n') + body);
    await refreshLlmOpsStatus();
  } catch (err) {
    setLlmOpsStatusPre(String(err));
  }
}
