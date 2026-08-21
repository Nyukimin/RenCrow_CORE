'use strict';

// Atlas is a read-only projection for the CORE Debug Viewer. It intentionally
// keeps no durable state and never calls an Atlas write endpoint.
let atlasProjection = atlasEmptyProjection();
let atlasFetchError = '';
let atlasLoading = false;
let atlasActiveTab = 'current';
const atlasBacklogFilters = {concept: '', priority: '', owner: ''};

function atlasEmptyProjection() {
  return {
    catalog: [],
    features: [],
    current: [],
    radar: [],
    backlog: [],
    queue: [],
    active: null,
    evidence: [],
    modules: [],
  };
}

function atlasList(value) {
  if (Array.isArray(value)) return value;
  if (!value || typeof value !== 'object') return [];
  for (const key of ['items', 'features', 'entries', 'records', 'data', 'results']) {
    if (Array.isArray(value[key])) return value[key];
  }
  return [];
}

function atlasField(value, keys, fallback = '') {
  if (!value || typeof value !== 'object') return fallback;
  for (const key of keys) {
    const candidate = value[key];
    if (candidate !== null && candidate !== undefined && String(candidate).trim() !== '') return candidate;
  }
  return fallback;
}

function atlasDisplay(value, fallback = '-') {
  if (value === null || value === undefined || value === '') return fallback;
  if (Array.isArray(value)) return value.map((item) => atlasDisplay(item, '')).filter(Boolean).join(', ') || fallback;
  if (typeof value === 'object') {
    const label = atlasField(value, ['title', 'name', 'label', 'id', 'item_id', 'unit_id'], '');
    if (label) return atlasDisplay(label, fallback);
    try { return JSON.stringify(value); } catch (_) { return fallback; }
  }
  return String(value);
}

function atlasEscape(value, fallback = '-') {
  const node = document.createElement('span');
  node.textContent = atlasDisplay(value, fallback);
  return node.innerHTML;
}

function atlasEscapeAttr(value, fallback = '') {
  return atlasEscape(value, fallback)
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function atlasItemTitle(item, fallback = 'Untitled Atlas item') {
  if (typeof item === 'string') return item || fallback;
  return atlasDisplay(atlasField(item, ['title', 'name', 'label', 'feature', 'summary', 'item_id', 'id'], ''), fallback);
}

function atlasItemID(item) {
  return atlasDisplay(atlasField(item, ['item_id', 'id', 'unit_id', 'feature_id', 'key'], ''), '-');
}

function atlasItemCategory(item) {
  return atlasDisplay(atlasField(item, ['category', 'domain', 'area', 'kind', 'type'], ''), '-');
}

function atlasItemOwner(item) {
  return atlasDisplay(atlasField(item, ['owner', 'owner_module', 'module', 'maintainer'], ''), '-');
}

function atlasConceptState(item) {
  return atlasDisplay(atlasField(item, ['concept_state', 'conceptState', 'state', 'status'], ''), '-');
}

function atlasDeliveryState(item) {
  return atlasDisplay(atlasField(item, ['delivery_state', 'deliveryState', 'implementation_state', 'status'], ''), '-');
}

function atlasTimestamp(item) {
  return atlasField(item, ['received_at', 'fetched_at', 'ingested_at', 'created_at', 'updated_at', 'timestamp', 'last_verified'], '');
}

function atlasFormatTime(value) {
  if (!value) return '-';
  try {
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return atlasDisplay(value);
    return date.toLocaleString('ja-JP', {hour12: false, timeZone: 'Asia/Tokyo'});
  } catch (_) {
    return atlasDisplay(value);
  }
}

function atlasStatusClass(value) {
  const status = String(value || '').toLowerCase().replace(/[-\s]+/g, '_');
  if (/(failed|failure|blocked|error|rejected|unavailable|conflict|unsupported)/.test(status)) return 'state-error';
  if (/(active|running|implementing|in_progress|deploy|restart)/.test(status)) return 'state-running';
  if (/(pending|queued|candidate|waiting|review|deferred|testing|fixing)/.test(status)) return 'state-thinking';
  if (/(passed|pass|done|ok|ready|live_verified|completed|verified)/.test(status)) return 'state-idle';
  return 'state-offline';
}

function atlasBadge(value, fallback = '-') {
  const label = atlasDisplay(value, fallback);
  return '<span class="badge ' + atlasStatusClass(label) + '">' + atlasEscape(label) + '</span>';
}

function atlasFirstObject(payload, keys) {
  for (const key of keys) {
    const value = payload ? payload[key] : null;
    if (value && typeof value === 'object' && !Array.isArray(value)) return value;
  }
  return null;
}

function atlasNormalizeProjection(payload) {
  const source = payload && typeof payload === 'object' && !Array.isArray(payload) ? payload : {};
  return {
    catalog: atlasList(source.catalog),
    features: atlasList(source.features),
    current: atlasList(source.current),
    radar: atlasList(source.radar),
    backlog: atlasList(source.backlog),
    queue: atlasList(source.queue),
    active: atlasFirstObject(source, ['active', 'active_unit', 'activeUnit', 'active_item']),
    evidence: atlasList(source.evidence),
    modules: atlasList(source.modules),
  };
}

function atlasCurrentItems() {
  if (atlasProjection.current.length) return atlasProjection.current;
  if (atlasProjection.features.length) return atlasProjection.features;
  return atlasProjection.catalog;
}

function atlasRoot() {
  return document.getElementById('atlasRoot');
}

function atlasSetStatus(label, stateName) {
  const status = document.getElementById('atlasStatus');
  if (!status) return;
  status.className = 'badge ' + atlasStatusClass(stateName || label);
  status.textContent = label;
  status.setAttribute('aria-label', 'Atlas status: ' + label);
}

function atlasEmpty(title, detail, unavailable = false) {
  return '<div class="atlas-empty' + (unavailable ? ' atlas-empty-unavailable' : '') + '">' +
    '<strong>' + atlasEscape(title) + '</strong>' +
    (detail ? '<span>' + atlasEscape(detail) + '</span>' : '') +
    '</div>';
}

function atlasSummaryCard(label, value, detail, stateName) {
  return '<article class="atlas-summary-card">' +
    '<span class="atlas-summary-label">' + atlasEscape(label) + '</span>' +
    '<strong class="atlas-summary-value ' + (stateName ? atlasStatusClass(stateName) : '') + '">' + atlasEscape(value) + '</strong>' +
    '<span class="atlas-summary-detail">' + atlasEscape(detail) + '</span>' +
    '</article>';
}

function atlasRenderSummary() {
  const root = document.getElementById('atlasSummaryCards');
  if (!root) return;
  if (atlasFetchError) {
    root.innerHTML = [
      atlasSummaryCard('Projection', 'unavailable', atlasFetchError, 'unavailable'),
      atlasSummaryCard('Current', '-', 'Atlas projection could not be read', 'unavailable'),
      atlasSummaryCard('Queue', '-', 'Atlas projection could not be read', 'unavailable'),
      atlasSummaryCard('Modules', '-', 'Atlas projection could not be read', 'unavailable'),
    ].join('');
    return;
  }
  const current = atlasCurrentItems();
  const catalogCount = atlasProjection.catalog.length + atlasProjection.features.length;
  const activeTitle = atlasProjection.active ? atlasItemTitle(atlasProjection.active, 'Active unit') : 'None';
  root.innerHTML = [
    atlasSummaryCard('Current', String(current.length), atlasProjection.current.length ? 'current projection' : 'catalog/features fallback', current.length ? 'ready' : 'empty'),
    atlasSummaryCard('Radar', String(atlasProjection.radar.length), 'new information to review', atlasProjection.radar.length ? 'pending' : 'empty'),
    atlasSummaryCard('Backlog', String(atlasProjection.backlog.length), 'concept-state items', atlasProjection.backlog.length ? 'pending' : 'empty'),
    atlasSummaryCard('Active Unit', activeTitle, 'Global WIP = 1', atlasProjection.active ? 'active' : 'empty'),
    atlasSummaryCard('Queue', String(atlasProjection.queue.length), 'adopted implementation units', atlasProjection.queue.length ? 'pending' : 'empty'),
    atlasSummaryCard('Catalog', String(catalogCount), 'catalog + feature entries', catalogCount ? 'ready' : 'empty'),
  ].join('');
}

function atlasRenderCurrent(view) {
  const items = atlasCurrentItems();
  if (!items.length) {
    view.innerHTML = atlasEmpty('Current is empty', 'No current, feature, or catalog entries are available.');
    return;
  }
  const sourceName = atlasProjection.current.length ? 'current' : (atlasProjection.features.length ? 'features' : 'catalog');
  view.innerHTML = '<div class="atlas-view-heading"><div><span class="atlas-kicker">CURRENT</span><h3>現在の機能</h3><p>実装状態は読み取り専用の Atlas projection から表示します。</p></div><span class="atlas-source-note">source: ' + atlasEscape(sourceName) + '</span></div>' +
    '<div class="atlas-table-wrap"><table class="atlas-table"><thead><tr><th>Feature</th><th>Category</th><th>Owner</th><th>Concept</th><th>Delivery</th><th>Revision</th><th>Evidence</th></tr></thead><tbody>' +
    items.map((item) => {
      const evidence = atlasField(item, ['evidence_count', 'evidenceCount', 'evidence'], '');
      const evidenceValue = Array.isArray(evidence) ? evidence.length : atlasDisplay(evidence, '0');
      return '<tr><td><strong>' + atlasEscape(atlasItemTitle(item)) + '</strong><div class="atlas-code">' + atlasEscape(atlasItemID(item)) + '</div></td>' +
        '<td>' + atlasEscape(atlasItemCategory(item)) + '</td><td>' + atlasEscape(atlasItemOwner(item)) + '</td>' +
        '<td>' + atlasBadge(atlasConceptState(item)) + '</td><td>' + atlasBadge(atlasDeliveryState(item)) + '</td>' +
        '<td class="atlas-code">' + atlasEscape(atlasField(item, ['revision', 'source_revision', 'commit', 'sha'], '-')) + '</td>' +
        '<td>' + atlasEscape(evidenceValue) + '</td></tr>';
    }).join('') + '</tbody></table></div>';
}

function atlasRenderRadar(view) {
  const items = atlasProjection.radar.slice().sort((a, b) => {
    const left = new Date(atlasTimestamp(a)).getTime();
    const right = new Date(atlasTimestamp(b)).getTime();
    if (Number.isNaN(left) || Number.isNaN(right)) return 0;
    return right - left;
  });
  if (!items.length) {
    view.innerHTML = atlasEmpty('Radar is empty', 'No newly ingested information is available.');
    return;
  }
  view.innerHTML = '<div class="atlas-view-heading"><div><span class="atlas-kicker">RADAR</span><h3>新しく入った情報</h3><p>取得日時の新しい順に表示しています。</p></div></div>' +
    '<div class="atlas-table-wrap"><table class="atlas-table"><thead><tr><th>Received</th><th>Source</th><th>Title</th><th>Kind</th><th>Summary</th><th>Status</th></tr></thead><tbody>' +
    items.map((item) => '<tr><td class="atlas-time">' + atlasEscape(atlasFormatTime(atlasTimestamp(item))) + '</td>' +
      '<td>' + atlasEscape(atlasField(item, ['source_type', 'source', 'origin'], '-')) + '</td>' +
      '<td><strong>' + atlasEscape(atlasItemTitle(item)) + '</strong><div class="atlas-code">' + atlasEscape(atlasItemID(item)) + '</div></td>' +
      '<td>' + atlasEscape(atlasField(item, ['kind', 'type', 'category'], '-')) + '</td>' +
      '<td class="atlas-wrap">' + atlasEscape(atlasField(item, ['summary', 'body', 'description', 'note'], '-')) + '</td>' +
      '<td>' + atlasBadge(atlasField(item, ['status', 'state'], '-')) + '</td></tr>').join('') + '</tbody></table></div>';
}

function atlasFilterValues(items, keys) {
  return Array.from(new Set(items.map((item) => atlasDisplay(atlasField(item, keys, ''), '')).filter(Boolean))).sort();
}

function atlasRenderFilter(label, filterKey, values) {
  return '<label class="atlas-filter"><span>' + atlasEscape(label) + '</span><select data-atlas-filter="' + atlasEscapeAttr(filterKey) + '"><option value="">All</option>' +
    values.map((value) => '<option value="' + atlasEscapeAttr(value) + '"' + (atlasBacklogFilters[filterKey] === value ? ' selected' : '') + '>' + atlasEscape(value) + '</option>').join('') + '</select></label>';
}

function atlasRenderBacklog(view) {
  const allItems = atlasProjection.backlog;
  const filtered = allItems.filter((item) => {
    const concept = atlasDisplay(atlasField(item, ['concept_state', 'conceptState', 'state', 'status'], ''), '');
    const priority = atlasDisplay(atlasField(item, ['priority', 'importance'], ''), '');
    const owner = atlasDisplay(atlasField(item, ['owner', 'owner_module', 'module', 'maintainer'], ''), '');
    return (!atlasBacklogFilters.concept || concept === atlasBacklogFilters.concept) &&
      (!atlasBacklogFilters.priority || priority === atlasBacklogFilters.priority) &&
      (!atlasBacklogFilters.owner || owner === atlasBacklogFilters.owner);
  });
  const filterBar = '<div class="atlas-filters">' +
    atlasRenderFilter('Concept State', 'concept', atlasFilterValues(allItems, ['concept_state', 'conceptState', 'state', 'status'])) +
    atlasRenderFilter('Priority', 'priority', atlasFilterValues(allItems, ['priority', 'importance'])) +
    atlasRenderFilter('Owner', 'owner', atlasFilterValues(allItems, ['owner', 'owner_module', 'module', 'maintainer'])) +
    '</div>';
  if (!allItems.length) {
    view.innerHTML = '<div class="atlas-view-heading"><div><span class="atlas-kicker">BACKLOG</span><h3>検討済み項目</h3></div></div>' + filterBar + atlasEmpty('Backlog is empty', 'No Atlas backlog items are available.');
    atlasBindBacklogFilters(view);
    return;
  }
  view.innerHTML = '<div class="atlas-view-heading"><div><span class="atlas-kicker">BACKLOG</span><h3>検討済み項目</h3><p>Concept State、Priority、Owner で絞り込めます。</p></div><span class="atlas-source-note">' + String(filtered.length) + ' / ' + String(allItems.length) + '</span></div>' + filterBar +
    '<div class="atlas-table-wrap"><table class="atlas-table"><thead><tr><th>Item</th><th>Concept State</th><th>Priority</th><th>Owner</th><th>Summary</th><th>Updated</th></tr></thead><tbody>' +
    (filtered.length ? filtered.map((item) => '<tr><td><strong>' + atlasEscape(atlasItemTitle(item)) + '</strong><div class="atlas-code">' + atlasEscape(atlasItemID(item)) + '</div></td>' +
      '<td>' + atlasBadge(atlasConceptState(item)) + '</td><td>' + atlasEscape(atlasField(item, ['priority', 'importance'], '-')) + '</td>' +
      '<td>' + atlasEscape(atlasItemOwner(item)) + '</td><td class="atlas-wrap">' + atlasEscape(atlasField(item, ['summary', 'body', 'description', 'note'], '-')) + '</td>' +
      '<td class="atlas-time">' + atlasEscape(atlasFormatTime(atlasField(item, ['updated_at', 'created_at', 'timestamp'], ''))) + '</td></tr>').join('') : '<tr><td colspan="6">' + atlasEmpty('No matching backlog items', 'Change the read-only filters to inspect other states.') + '</td></tr>') +
    '</tbody></table></div>';
  atlasBindBacklogFilters(view);
}

function atlasBindBacklogFilters(view) {
  view.querySelectorAll('[data-atlas-filter]').forEach((select) => {
    select.addEventListener('change', () => {
      const key = select.dataset.atlasFilter;
      if (!Object.prototype.hasOwnProperty.call(atlasBacklogFilters, key)) return;
      atlasBacklogFilters[key] = select.value;
      atlasRender();
    });
  });
}

function atlasStageName(stage, index) {
  if (typeof stage === 'string') return stage;
  return atlasDisplay(atlasField(stage, ['name', 'stage', 'label', 'key', 'id'], ''), 'Stage ' + String(index + 1));
}

function atlasRenderStages(active) {
  const stages = atlasList(atlasField(active, ['stages', 'pipeline', 'delivery_stages', 'deliveryStages'], []));
  if (!stages.length) return atlasEmpty('No stage evidence', 'The active unit has no stage list in the projection.');
  return '<ol class="atlas-stage-list">' + stages.map((stage, index) => {
    const value = typeof stage === 'string' ? stage : atlasField(stage, ['status', 'state', 'result'], 'pending');
    return '<li class="atlas-stage ' + atlasStatusClass(value) + '"><span class="atlas-stage-marker">' + atlasEscape(String(index + 1)) + '</span><div><strong>' + atlasEscape(atlasStageName(stage, index)) + '</strong><span>' + atlasBadge(value, 'pending') + '</span>' + (typeof stage === 'object' ? '<p>' + atlasEscape(atlasField(stage, ['summary', 'detail', 'evidence'], ''), '') + '</p>' : '') + '</div></li>';
  }).join('') + '</ol>';
}

function atlasRenderQueue(queue) {
  if (!queue.length) return atlasEmpty('Queue is empty', 'No adopted implementation units are waiting.');
  return '<div class="atlas-table-wrap"><table class="atlas-table"><thead><tr><th>Unit</th><th>Owner</th><th>Concept</th><th>Delivery</th><th>Queued</th></tr></thead><tbody>' + queue.map((item) => '<tr><td><strong>' + atlasEscape(atlasItemTitle(item, 'Implementation unit')) + '</strong><div class="atlas-code">' + atlasEscape(atlasItemID(item)) + '</div></td><td>' + atlasEscape(atlasItemOwner(item)) + '</td><td>' + atlasBadge(atlasConceptState(item)) + '</td><td>' + atlasBadge(atlasDeliveryState(item)) + '</td><td class="atlas-time">' + atlasEscape(atlasFormatTime(atlasField(item, ['queued_at', 'created_at', 'timestamp'], ''))) + '</td></tr>').join('') + '</tbody></table></div>';
}

function atlasRenderPipeline(view) {
  const active = atlasProjection.active;
  const activeBlock = active ? '<article class="atlas-active-unit"><div class="atlas-active-head"><div><span class="atlas-kicker">ACTIVE IMPLEMENTATION UNIT</span><h3>' + atlasEscape(atlasItemTitle(active, 'Active unit')) + '</h3><div class="atlas-code">' + atlasEscape(atlasItemID(active)) + '</div></div>' + atlasBadge(atlasField(active, ['delivery_state', 'deliveryState', 'state', 'status'], 'active'), 'active') + '</div><div class="atlas-active-meta"><span>Owner: <strong>' + atlasEscape(atlasItemOwner(active)) + '</strong></span><span>Concept: ' + atlasBadge(atlasConceptState(active)) + '</span></div><div class="atlas-stage-heading"><h4>Stages</h4><span>passed / active / pending / failed / blocked</span></div>' + atlasRenderStages(active) + '</article>' : atlasEmpty('No active implementation unit', 'Global WIP = 1; the pipeline currently has no active unit.');
  view.innerHTML = '<div class="atlas-view-heading"><div><span class="atlas-kicker">PIPELINE</span><h3>Implementation Pipeline</h3><p>Active Unit を上部に固定し、Global WIP = 1 を明示します。</p></div><div class="atlas-wip"><span>Global WIP</span><strong>1</strong></div></div>' + activeBlock + '<section class="atlas-subsection"><div class="atlas-subsection-head"><h4>Queue</h4><span>read-only</span></div>' + atlasRenderQueue(atlasProjection.queue) + '</section>';
}

function atlasRenderEvidence(view) {
  const items = atlasProjection.evidence;
  if (!items.length) {
    view.innerHTML = '<div class="atlas-view-heading"><div><span class="atlas-kicker">EVIDENCE</span><h3>Implementation Evidence</h3></div></div>' + atlasEmpty('Evidence is empty', 'No stage evidence is available in the projection.');
    return;
  }
  view.innerHTML = '<div class="atlas-view-heading"><div><span class="atlas-kicker">EVIDENCE</span><h3>Stage evidence timeline</h3><p>実装完了の根拠を stage ごとに確認します。</p></div></div><ol class="atlas-evidence-timeline">' + items.map((item, index) => {
    const stage = atlasField(item, ['stage', 'stage_name', 'kind', 'type'], 'Evidence');
    const status = atlasField(item, ['status', 'state', 'result'], 'recorded');
    return '<li class="atlas-evidence-item ' + atlasStatusClass(status) + '"><span class="atlas-evidence-line"></span><div class="atlas-evidence-marker">' + atlasEscape(String(index + 1)) + '</div><article><div class="atlas-evidence-head"><strong>' + atlasEscape(stage) + '</strong>' + atlasBadge(status) + '</div><p>' + atlasEscape(atlasField(item, ['summary', 'detail', 'description', 'message'], '-')) + '</p><div class="atlas-evidence-meta"><span>' + atlasEscape(atlasField(item, ['evidence_type', 'source_type', 'type'], '-')) + '</span><span>' + atlasEscape(atlasField(item, ['ref', 'evidence_ref', 'id', 'commit', 'trace_id'], '-')) + '</span><time>' + atlasEscape(atlasFormatTime(atlasTimestamp(item))) + '</time></div></article></li>';
  }).join('') + '</ol>';
}

function atlasRenderModules(view) {
  const modules = atlasProjection.modules;
  if (!modules.length) {
    view.innerHTML = '<div class="atlas-view-heading"><div><span class="atlas-kicker">MODULES</span><h3>EcoSystem modules</h3></div></div>' + atlasEmpty('Modules are empty', 'No module revision or runtime health projection is available.');
    return;
  }
  view.innerHTML = '<div class="atlas-view-heading"><div><span class="atlas-kicker">MODULES</span><h3>EcoSystem module status</h3><p>revision、runtime health、last verified を表示します。</p></div></div><div class="atlas-module-grid">' + modules.map((module) => {
    const health = atlasField(module, ['runtime_health', 'runtimeHealth', 'health', 'status'], 'unknown');
    return '<article class="atlas-module-card"><div class="atlas-module-head"><strong>' + atlasEscape(atlasField(module, ['name', 'module', 'id', 'component'], 'Module')) + '</strong>' + atlasBadge(health) + '</div><dl><div><dt>Revision</dt><dd class="atlas-code">' + atlasEscape(atlasField(module, ['revision', 'source_revision', 'commit', 'sha'], '-')) + '</dd></div><div><dt>Last verified</dt><dd>' + atlasEscape(atlasFormatTime(atlasField(module, ['last_verified', 'lastVerified', 'verified_at'], ''))) + '</dd></div><div><dt>Owner</dt><dd>' + atlasEscape(atlasField(module, ['owner', 'owner_module'], '-')) + '</dd></div></dl></article>';
  }).join('') + '</div>';
}

function atlasRenderView(view) {
  switch (atlasActiveTab) {
    case 'radar': atlasRenderRadar(view); break;
    case 'backlog': atlasRenderBacklog(view); break;
    case 'pipeline': atlasRenderPipeline(view); break;
    case 'evidence': atlasRenderEvidence(view); break;
    case 'modules': atlasRenderModules(view); break;
    default: atlasRenderCurrent(view);
  }
}

function atlasRender() {
  const root = atlasRoot();
  if (!root) return;
  atlasRenderSummary();
  const hasProjectionData = atlasCurrentItems().length > 0 || atlasProjection.radar.length > 0 ||
    atlasProjection.backlog.length > 0 || atlasProjection.queue.length > 0 ||
    Boolean(atlasProjection.active) || atlasProjection.evidence.length > 0 || atlasProjection.modules.length > 0;
  atlasSetStatus(atlasLoading ? 'loading' : (atlasFetchError ? 'unavailable' : (hasProjectionData ? 'ready' : 'empty')), atlasLoading ? 'pending' : (atlasFetchError ? 'unavailable' : (hasProjectionData ? 'ready' : 'empty')));
  const views = Array.from(root.querySelectorAll('.atlas-view'));
  const tabs = Array.from(root.querySelectorAll('[data-atlas-tab]'));
  tabs.forEach((tab) => {
    const selected = tab.dataset.atlasTab === atlasActiveTab;
    tab.classList.toggle('active', selected);
    tab.setAttribute('aria-selected', selected ? 'true' : 'false');
  });
  views.forEach((view) => {
    const selected = view.dataset.atlasView === atlasActiveTab;
    view.hidden = !selected;
    if (selected) {
      if (atlasLoading) view.innerHTML = atlasEmpty('Loading Atlas projection', 'Reading the read-only CORE projection.');
      else if (atlasFetchError) view.innerHTML = atlasEmpty('Atlas unavailable', atlasFetchError, true);
      else atlasRenderView(view);
    }
  });
}

async function refreshAtlas() {
  if (atlasLoading) return;
  atlasLoading = true;
  atlasFetchError = '';
  atlasRender();
  try {
    const response = await fetch('/viewer/atlas', {cache: 'no-store'});
    if (!response.ok) throw new Error('HTTP ' + String(response.status));
    const payload = await response.json();
    if (!payload || typeof payload !== 'object' || Array.isArray(payload)) throw new Error('invalid Atlas projection');
    atlasProjection = atlasNormalizeProjection(payload);
  } catch (error) {
    atlasProjection = atlasEmptyProjection();
    atlasFetchError = String(error && error.message ? error.message : error);
  } finally {
    atlasLoading = false;
    atlasRender();
  }
}

function bindAtlasControls() {
  const root = atlasRoot();
  if (!root) return;
  root.querySelectorAll('[data-atlas-tab]').forEach((tab) => {
    tab.addEventListener('click', () => {
      atlasActiveTab = tab.dataset.atlasTab || 'current';
      atlasRender();
    });
  });
  const refresh = document.getElementById('atlasRefreshBtn');
  if (refresh) refresh.addEventListener('click', refreshAtlas);
  atlasRender();
}

bindAtlasControls();
