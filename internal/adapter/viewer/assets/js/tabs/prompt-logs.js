// Character Latest Prompt tab: character-scoped latest exchanges and internal raw details.
function promptDebugAgeClass(createdAt) {
  const timestamp = Date.parse(String(createdAt || ''));
  if (!Number.isFinite(timestamp)) return 'age-old';
  const age = Math.max(0, Date.now() - timestamp);
  if (age <= 5 * 60 * 1000) return 'age-recent';
  if (age <= 60 * 60 * 1000) return 'age-hour';
  if (age <= 24 * 60 * 60 * 1000) return 'age-day';
  return 'age-old';
}

function promptDebugLevelClass(item) {
  const level = String(item && item.level || '').toLowerCase();
  if (level === 'error' || level === 'critical' || level === 'fatal') return 'level-error';
  if (level === 'warn' || level === 'warning') return 'level-warn';
  if (level === 'debug' || level === 'trace') return 'level-debug';
  return 'level-info';
}

function promptDebugBlockClass(label) {
  const normalized = String(label || '').replace(/[^0-9]/g, '');
  const value = Number(normalized);
  const palette = ['00', '10', '20', '30', '40', '50', '60', '70', '80', '90'];
  const key = Number.isFinite(value) ? String(value - (value % 10)).padStart(2, '0') : '00';
  return 'prompt-block-' + (palette.indexOf(key) >= 0 ? key : '00');
}

function promptDebugBlockPath(block) {
  const label = String(block && block.label || '-');
  switch (String(block && block.type || '')) {
    case 'character_system_prompt': return 'STATIC PREFIX / Character SystemPrompt / ' + label;
    case 'stable_runtime_context': return 'STATIC PREFIX / Stable RuntimeContext / ' + label;
    case 'recall_pack': return 'DYNAMIC AREA / RecallPack / ' + label;
    case 'variable_runtime_context': return 'DYNAMIC AREA / ' + label;
    case 'user_message': return 'User Message';
    default: return label;
  }
}

function promptDebugMeta(item) {
  const metadata = item && item.metadata && typeof item.metadata === 'object' ? item.metadata : {};
  return [
    item && item.request_id ? 'request: ' + item.request_id : '',
    metadata.agent_id ? 'agent: ' + metadata.agent_id : '',
    metadata.caller ? 'caller: ' + metadata.caller : '',
    metadata.target_id ? 'target: ' + metadata.target_id : '',
  ].filter(Boolean).join(' · ');
}

function promptDebugProjectionSignature() {
  const exchangeSignature = (exchange) => ({
    exchange_id: exchange && exchange.exchange_id || '',
    request_id: exchange && exchange.request_id || '',
    agent_id: exchange && exchange.agent_id || '',
    caller: exchange && exchange.caller || '',
    target_id: exchange && exchange.target_id || '',
    execution_role: exchange && exchange.execution_role || '',
    created_at: exchange && exchange.created_at || '',
    items: Array.isArray(exchange && exchange.items) ? exchange.items.map((item) => ({
      created_at: item && item.created_at || '',
      stage: item && item.stage || '',
      level: item && item.level || '',
      request_id: item && item.request_id || '',
      payload_sha256: item && item.payload_sha256 || '',
    })) : [],
  });
  return JSON.stringify({
    available: state.ops.promptDebugAvailable,
    error: state.ops.promptDebugFetchError || '',
    source: state.ops.promptDebugSource || '',
    characters: (state.ops.promptDebugCharacterLatest || []).map(exchangeSignature),
    internal: (state.ops.promptDebugInternalExchanges || []).map(exchangeSignature),
  });
}

function capturePromptDebugViewState() {
  const root = document.getElementById('panel-prompt-logs');
  if (!root) return null;
  return {
    mainScrollTop: typeof mainEl !== 'undefined' && mainEl ? mainEl.scrollTop : 0,
    details: Array.from(root.querySelectorAll('details')).map((element) => element.open),
    preScrollTops: Array.from(root.querySelectorAll('pre')).map((element) => element.scrollTop),
  };
}

function restorePromptDebugViewState(viewState) {
  if (!viewState) return;
  const root = document.getElementById('panel-prompt-logs');
  if (!root) return;
  const restore = () => {
    if (typeof mainEl !== 'undefined' && mainEl) mainEl.scrollTop = viewState.mainScrollTop;
    root.querySelectorAll('details').forEach((element, index) => { element.open = Boolean(viewState.details[index]); });
    root.querySelectorAll('pre').forEach((element, index) => { element.scrollTop = viewState.preScrollTops[index] || 0; });
  };
  restore();
  requestAnimationFrame(restore);
}

function renderPromptDebug() {
  const characterTarget = document.getElementById('promptDebugCharacterList');
  const internalTarget = document.getElementById('promptDebugInternalList');
  const status = document.getElementById('promptDebugStatus');
  if (!characterTarget || !internalTarget) return;
  bindPromptDebugControls();
  const signature = promptDebugProjectionSignature();
  if (signature === state.ops.promptDebugRenderSignature) return;
  state.ops.promptDebugRenderSignature = signature;
  const viewState = capturePromptDebugViewState();
  characterTarget.innerHTML = '';
  internalTarget.innerHTML = '';
  const fetchError = String(state.ops.promptDebugFetchError || '').trim();
  const characters = Array.isArray(state.ops.promptDebugCharacterLatest) ? state.ops.promptDebugCharacterLatest : [];
  const internal = Array.isArray(state.ops.promptDebugInternalExchanges) ? state.ops.promptDebugInternalExchanges : [];
  if (status) {
    status.textContent = fetchError ? '取得失敗: ' + fetchError : (!state.ops.promptDebugAvailable ? 'ログ未作成' : (characters.length + ' characters / ' + internal.length + ' internal'));
  }
  if (fetchError) {
    characterTarget.innerHTML = '<div class="prompt-debug-empty level-error">Prompt debug unavailable: ' + esc(fetchError) + '</div>';
    restorePromptDebugViewState(viewState);
    return;
  }
  if (!state.ops.promptDebugAvailable) {
    characterTarget.innerHTML = '<div class="prompt-debug-empty age-old">Prompt debug log is not available. Capture is disabled or the file has not been created.</div>';
    restorePromptDebugViewState(viewState);
    return;
  }
  const characterByID = new Map(characters.map((exchange) => [String(exchange && exchange.agent_id || '').toLowerCase(), exchange]));
  [
    {id: 'mio', label: 'Mio'},
    {id: 'shiro', label: 'Shiro'},
    {id: 'kuro', label: 'Kuro'},
    {id: 'midori', label: 'Midori'},
  ].forEach((character) => {
    const exchange = characterByID.get(character.id);
    characterTarget.appendChild(exchange ? promptDebugExchangeCard(exchange, character.label) : promptDebugEmptyCharacterCard(character.label));
  });
  if (!internal.length) {
    internalTarget.innerHTML = '<div class="prompt-debug-empty age-old">No internal / worker prompt exchanges yet.</div>';
    restorePromptDebugViewState(viewState);
    return;
  }
  internal.forEach((exchange) => internalTarget.appendChild(promptDebugExchangeCard(exchange, 'Internal')));
  restorePromptDebugViewState(viewState);
}

function promptDebugEmptyCharacterCard(label) {
  const card = document.createElement('article');
  card.className = 'prompt-debug-record prompt-debug-character-empty age-old';
  card.innerHTML = '<header class="prompt-debug-record-head"><div><strong>' + esc(label) + '</strong><div class="small">最新のchat roleログはまだありません</div></div></header>';
  return card;
}

function promptDebugExchangeCard(exchange, heading) {
  const items = Array.isArray(exchange && exchange.items) ? exchange.items : [];
  const latest = items[items.length - 1] || items[0] || {};
  const requestID = String(exchange && exchange.request_id || latest.request_id || 'unknown');
  const ageClass = promptDebugAgeClass(exchange && exchange.created_at || latest.created_at);
  const levelClass = promptDebugLevelClass(latest);
  const card = document.createElement('article');
  card.className = 'prompt-debug-record ' + ageClass + ' ' + levelClass;
  const roleText = [exchange && exchange.execution_role, exchange && exchange.target_id, exchange && exchange.caller].filter(Boolean).join(' · ');
  const body = document.createElement('div');
  body.className = 'prompt-debug-record-body';
  body.innerHTML = '<header class="prompt-debug-record-head">' +
    '<div><div class="prompt-debug-character-name">' + esc(heading || exchange.agent_id || '-') + '</div><strong>' + esc(requestID) + '</strong><div class="small">' + esc(roleText || promptDebugMeta(latest)) + '</div></div>' +
    '<div class="prompt-debug-badges"><span class="prompt-debug-age ' + ageClass + '">' + esc(ageClass.replace('age-', '')) + '</span><span class="prompt-debug-level ' + levelClass + '">' + esc(latest.level || 'info') + '</span></div>' +
    '</header>';
  items.forEach((item) => body.appendChild(promptDebugStageElement(item)));
  card.appendChild(body);
  return card;
}

function promptDebugStageElement(item) {
  const stage = document.createElement('section');
  const ageClass = promptDebugAgeClass(item.created_at);
  const levelClass = promptDebugLevelClass(item);
  stage.className = 'prompt-debug-stage ' + ageClass + ' ' + levelClass;
  const blocks = Array.isArray(item.system_prompt_blocks) ? item.system_prompt_blocks : [];
  const blockHTML = blocks.length ? '<div class="prompt-debug-blocks">' + blocks.map((block) =>
    '<div class="prompt-debug-block ' + promptDebugBlockClass(block.label) + '"><div class="prompt-debug-block-label">' + esc(promptDebugBlockPath(block)) + ' <span>lines ' + esc(String(block.start_line || '-')) + '–' + esc(String(block.end_line || '-')) + '</span></div><pre>' + esc(block.text || '') + '</pre></div>'
  ).join('') + '</div>' : '<div class="small prompt-debug-no-system">Prompt Contextを抽出できませんでした（非JSONまたは型metadataなし）</div>';
  stage.innerHTML =
    '<div class="prompt-debug-stage-head"><span class="prompt-debug-stage-name">' + esc(item.stage || '-') + '</span><span>' + esc(fdt(item.created_at)) + '</span><span class="prompt-debug-stage-badge ' + levelClass + '">' + esc(item.level || 'info') + '</span></div>' +
    '<div class="small prompt-debug-stage-meta">' + esc(promptDebugMeta(item)) + ' · ' + esc(String(item.payload_bytes || 0)) + ' bytes · sha256 ' + esc(short(item.payload_sha256 || '-', 20)) + '</div>' +
    blockHTML +
    '<details class="prompt-debug-raw"><summary>送信Payload全文</summary><pre>' + esc(item.payload_text || '') + '</pre></details>';
  return stage;
}

function bindPromptDebugControls() {
  const button = document.getElementById('promptDebugRefreshBtn');
  if (!button || button.dataset.bound === 'true') return;
  button.dataset.bound = 'true';
  button.addEventListener('click', () => {
    button.disabled = true;
    Promise.resolve(typeof refreshPromptDebugData === 'function' ? refreshPromptDebugData() : null).finally(() => { button.disabled = false; });
  });
}
