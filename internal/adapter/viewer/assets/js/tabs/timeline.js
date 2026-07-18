// Chat Timeline tab module: normal chat message rendering.
const DEFAULT_CHAT_ROUTE_ALIASES = {
  worker: {label: 'Worker', baseURL: 'http://127.0.0.1:8082', model: 'Worker', routePrefix: '/ops'},
  heavy: {label: 'Heavy', baseURL: 'http://127.0.0.1:8083', model: 'Heavy', routePrefix: '/analyze'},
  wild: {label: 'Wild', baseURL: 'http://127.0.0.1:8084', model: 'Wild', routePrefix: '/wild'},
};
let CHAT_ROUTE_ALIASES = {...DEFAULT_CHAT_ROUTE_ALIASES};
const CHAT_ROUTE_ALIAS_STORAGE_KEY = 'chatRouteAlias.selected';

function syncChatRouteAliasesFromRuntimeConfig(localLLM) {
  if (!localLLM || !localLLM.enabled) {
    CHAT_ROUTE_ALIASES = {...DEFAULT_CHAT_ROUTE_ALIASES};
    return;
  }
  CHAT_ROUTE_ALIASES = {
    worker: {
      ...DEFAULT_CHAT_ROUTE_ALIASES.worker,
      baseURL: localLLM.worker_base_url || DEFAULT_CHAT_ROUTE_ALIASES.worker.baseURL,
      model: localLLM.worker_model || DEFAULT_CHAT_ROUTE_ALIASES.worker.model,
    },
    heavy: {
      ...DEFAULT_CHAT_ROUTE_ALIASES.heavy,
      baseURL: localLLM.heavy_base_url || DEFAULT_CHAT_ROUTE_ALIASES.heavy.baseURL,
      model: localLLM.heavy_model || DEFAULT_CHAT_ROUTE_ALIASES.heavy.model,
    },
    wild: {
      ...DEFAULT_CHAT_ROUTE_ALIASES.wild,
      baseURL: localLLM.wild_base_url || DEFAULT_CHAT_ROUTE_ALIASES.wild.baseURL,
      model: localLLM.wild_model || DEFAULT_CHAT_ROUTE_ALIASES.wild.model,
    },
  };
}

function selectedChatRouteAlias() {
  localStorage.removeItem(CHAT_ROUTE_ALIAS_STORAGE_KEY);
  return '';
}

function isExplicitRouteMessage(message) {
  return /^\/(ops|wild|heavy|code|code1|code2|code3|code4|plan|analyze|research|chat)(\s|$)/.test(String(message || '').trim());
}

function selectChatRouteAlias(alias) {
  localStorage.removeItem(CHAT_ROUTE_ALIAS_STORAGE_KEY);
  syncChatRouteAliasButtons();
}

function syncChatRouteAliasButtons() {
  const selected = selectedChatRouteAlias();
  document.querySelectorAll('[data-chat-route]').forEach((btn) => {
    const active = btn.dataset.chatRoute === selected;
    btn.classList.toggle('active', active);
    btn.setAttribute('aria-pressed', active ? 'true' : 'false');
  });
}

function bindChatRouteAliasButtons() {
  document.querySelectorAll('[data-chat-route]').forEach((btn) => {
    btn.addEventListener('click', () => selectChatRouteAlias(btn.dataset.chatRoute || ''));
  });
  syncChatRouteAliasButtons();
}

function applyChatRouteAliasToMessage(message) {
  const trimmed = String(message || '').trim();
  if (!trimmed || isExplicitRouteMessage(trimmed)) return trimmed;
  const selected = selectedChatRouteAlias();
  const alias = selected ? CHAT_ROUTE_ALIASES[selected] : null;
  return alias ? alias.routePrefix + ' ' + trimmed : trimmed;
}

function buildViewerSendRequest(message) {
  const trimmed = String(message || '').trim();
  selectedChatRouteAlias();
  if (!trimmed) return {message: ''};
  if (isExplicitRouteMessage(trimmed)) return {message: trimmed};

  const recipient = typeof selectedViewerChatRecipient === 'function' ? selectedViewerChatRecipient() : 'mio';
  if (recipient) return {message: trimmed, to: recipient};
  return {message: applyRoleTargetToMessage(trimmed)};
}

function viewerLLMStartSelectionForRequest(req) {
  const alias = String(req && req.model_alias ? req.model_alias : '').trim();
  return alias === 'Worker' || alias === 'Heavy' || alias === 'Wild' ? alias : '';
}

function viewerLLMRoleInfo(status, role) {
  if (!status || !role) return null;
  if (status.roles && status.roles[role]) return status.roles[role];
  return status[role] || null;
}

function viewerLLMRoleHealthy(status, role) {
  const info = viewerLLMRoleInfo(status, role);
  if (!info) return false;
  if (info.halted === true) return false;
  return info.health_ok === true || info.status === 'ok' || info.health === 'ok';
}

function viewerLLMSelectionReady(status, selection) {
  return viewerLLMRoleHealthy(status, 'Chat') && viewerLLMRoleHealthy(status, selection);
}

function viewerLLMStopRolesBeforeStart(selection) {
  if (selection === 'Wild') return ['Worker', 'Heavy'];
  if (selection === 'Heavy') return ['Worker', 'Wild'];
  if (selection === 'Worker') return ['Heavy', 'Wild'];
  return [];
}

async function stopViewerLLMRolesBeforeStart(selection) {
  const roles = viewerLLMStopRolesBeforeStart(selection);
  if (!roles.length) return;
  const stopRes = await fetch('/viewer/llm-ops/stop', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({roles}),
  });
  const body = await stopRes.text();
  if (!stopRes.ok) {
    throw new Error(formatViewerLLMOpsHTTPError('llm ops stop failed', stopRes.status, body));
  }
}

function formatViewerLLMOpsHTTPError(prefix, status, body) {
  const text = String(body || '').trim();
  return prefix + ': HTTP ' + String(status) + (text ? ': ' + text : '');
}

async function ensureViewerLLMReadyForRequest(req) {
  const selection = viewerLLMStartSelectionForRequest(req);
  if (!selection) return;

  const healthRes = await fetch('/viewer/llm-ops/health', {cache: 'no-store'});
  if (!healthRes.ok) {
    const body = await healthRes.text();
    throw new Error(formatViewerLLMOpsHTTPError('llm ops health failed', healthRes.status, body));
  }

  const statusRes = await fetch('/viewer/llm-ops/status', {cache: 'no-store'});
  if (!statusRes.ok) {
    const body = await statusRes.text();
    throw new Error(formatViewerLLMOpsHTTPError('llm ops status failed', statusRes.status, body));
  }
  const status = await statusRes.json();
  if (viewerLLMSelectionReady(status, selection)) return;

  await stopViewerLLMRolesBeforeStart(selection);

  const startRes = await fetch('/viewer/llm-ops/start', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({selection}),
  });
  const body = await startRes.text();
  if (!startRes.ok) {
    throw new Error(formatViewerLLMOpsHTTPError('llm ops start failed', startRes.status, body));
  }
  if (typeof refreshLlmOpsStatus === 'function') refreshLlmOpsStatus();
}

const voiceDirectTimelineJobIDs = new Set();

function rememberVoiceDirectTimelineJob(ev) {
  const jobID = String(ev && ev.job_id || '').trim();
  if (!jobID) return;
  const content = String(ev && ev.content || '');
  if (!content.includes('voice_direct')) return;
  voiceDirectTimelineJobIDs.add(jobID);
  if (voiceDirectTimelineJobIDs.size > 80) {
    const first = voiceDirectTimelineJobIDs.values().next().value;
    if (first) voiceDirectTimelineJobIDs.delete(first);
  }
}

function isVoiceDirectTimelineResponse(ev) {
  const jobID = String(ev && ev.job_id || '').trim();
  return !!(jobID && voiceDirectTimelineJobIDs.has(jobID));
}

function addMsgToTimeline(ev) {
  if (ev.type === 'job.notification') { addJobNotificationToTimeline(ev); return; }
  if (ev.type === 'agent.response') removeThinking(ev.job_id);
  if (ev.type === 'agent.thinking') { addThinking(ev); return; }
  if (ev.type === 'agent.start') { addThinkingStart(ev); return; }
  if (isCoordinationTraceEvent(ev)) { addCoordinationTraceToTimeline(ev); return; }
  if (ev.type === 'routing.decision') rememberVoiceDirectTimelineJob(ev);

  if (!matchesFilters(ev)) return;
  if (ev.type === 'idlechat.summary') return;
  if (ev.type === 'idlechat.message') return;
  if (ev.type !== 'message.received' && ev.type !== 'idlechat.message' && (ev.from || '').toLowerCase() !== 'mio') return;

  const em = document.getElementById('empty');
  if (em) em.remove();

  if (ev.type === 'routing.decision') return;
  if (ev.type === 'agent.response' && (ev.to || '').toLowerCase() !== 'user') return;
  if (ev.type === 'agent.response' && isTTSSyncedSpeaker(ev.from) && !isViewerLocalFailureMessage(ev) && !isVoiceDirectTimelineResponse(ev)) return;
  if (ev.type === 'idlechat.message' && isTTSSyncedSpeaker(ev.from)) return;
  if (ev.type === 'agent.note' && (ev.to || '').toLowerCase() !== 'user') return;
  if (ev.type === 'message.received' && (ev.from || '').toLowerCase() !== 'user') return;
  if (ev.type === 'message.received' && String(ev.content || '').trim().startsWith('[voice_direct]')) return;

  const f = ag(ev.from);
  const t = ev.to ? ag(ev.to) : null;
  const dir = t && ev.to ? '<span class="dir">→ ' + t.e + ' ' + t.l + '</span>' : '';
  const displayContent = normalizeViewerDisplayText(ev.content);
  const from = String(ev.from || '').toLowerCase();
  const roleClass = from === 'user' ? ' user' : ' assistant';
  const el = document.createElement('div');
  el.className = 'msg' + roleClass;
  el.innerHTML =
    '<div class="av" style="background:' + f.c + '18;color:' + f.c + '">' + f.e + '</div>' +
    '<div class="mb"><div class="mh">' +
      '<span class="an" style="color:' + f.c + '">' + f.l + '</span>' + dir +
      '<span class="tm">' + ftime(ev.timestamp) + '</span>' +
    '</div><button class="cp" onclick="copyMsg(this)">Copy</button>' +
    '<div class="mc">' + fmt(displayContent) + '</div></div>';
  el.querySelector('.mc').dataset.raw = ev.content || '';
  chat.appendChild(el);
  trimTimelineNodes();
  bump();
}

function isCoordinationTraceEvent(ev) {
  const type = String(ev && ev.type ? ev.type : '');
  return type === 'agent.delegate' || type === 'agent.acknowledge' || type === 'agent.report' || type === 'worker.request' || type === 'worker.result';
}

function addCoordinationTraceToTimeline(ev) {
  if (!matchesCoordinationTraceFilters(ev)) return;
  const em = document.getElementById('empty');
  if (em) em.remove();
  const f = ag(ev.from);
  const t = ev.to ? ag(ev.to) : null;
  const dir = t && ev.to ? '<span class="dir">→ ' + t.e + ' ' + t.l + '</span>' : '';
  const meta = [ev.type || '', ev.route || '', ev.job_id || ''].filter(Boolean).join(' / ');
  const el = document.createElement('div');
  el.className = 'msg assistant coordination-trace';
  el.innerHTML =
    '<div class="av" style="background:' + f.c + '18;color:' + f.c + '">' + f.e + '</div>' +
    '<div class="mb"><div class="mh">' +
      '<span class="an" style="color:' + f.c + '">' + f.l + '</span>' + dir +
      '<span class="tm">' + ftime(ev.timestamp) + '</span>' +
    '</div><button class="cp" onclick="copyMsg(this)">Copy</button>' +
    '<div class="coord-meta">' + esc(meta || 'internal trace') + '</div>' +
    '<div class="mc">' + fmt(normalizeViewerDisplayText(ev.content || '')) + '</div></div>';
  el.querySelector('.mc').dataset.raw = ev.content || '';
  chat.appendChild(el);
  trimTimelineNodes();
  bump();
}

function matchesCoordinationTraceFilters(ev) {
  if (fltType.value && ev.type !== fltType.value) return false;
  if (fltAgent.value && ev.from !== fltAgent.value && ev.to !== fltAgent.value) return false;
  if (fltRoute.value && (ev.route || '') !== fltRoute.value) return false;
  if (fltJob.value && !(ev.job_id || '').toLowerCase().includes(fltJob.value.toLowerCase())) return false;
  if (fltText.value && !(ev.content || '').toLowerCase().includes(fltText.value.toLowerCase())) return false;
  return true;
}

function addJobNotificationToTimeline(ev) {
  const em = document.getElementById('empty');
  if (em) em.remove();
  const fromName = String(ev.from || '').trim() || 'shiro';
  const f = ag(fromName);
  const route = String(ev.route || '').trim();
  const status = String(ev.status || ev.category || '').trim();
  const jobID = String(ev.job_id || '').trim();
  const meta = [route, status, jobID].filter(Boolean).join(' / ');
  const el = document.createElement('div');
  el.className = 'msg assistant job-interrupt';
  el.innerHTML =
    '<div class="av" style="background:' + f.c + '18;color:' + f.c + '">' + f.e + '</div>' +
    '<div class="mb"><div class="mh">' +
      '<span class="an" style="color:' + f.c + '">' + f.l + '</span>' +
      '<span class="dir">割り込み報告</span>' +
      '<span class="tm">' + ftime(ev.timestamp) + '</span>' +
    '</div><button class="cp" onclick="copyMsg(this)">Copy</button>' +
    '<div class="coord-meta">' + esc(meta || 'job.notification') + '</div>' +
    '<div class="mc">' + fmt(normalizeViewerDisplayText(ev.content || '')) + '</div></div>';
  el.querySelector('.mc').dataset.raw = ev.content || '';
  chat.appendChild(el);
  trimTimelineNodes();
  bump();
}

function isViewerLocalFailureMessage(ev) {
  return String(ev && ev.content ? ev.content : '').startsWith('Viewer send unavailable:');
}

bindChatRouteAliasButtons();
