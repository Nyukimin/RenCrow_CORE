'use strict';

// Atlas reads the public projection and exposes the authenticated CORE owner
// operations required to complete user decisions. Owner credentials stay only
// in this page's memory and are never written to browser storage.
let atlasProjection = atlasEmptyProjection();
let atlasFetchError = '';
let atlasLoading = false;
let atlasActiveTab = 'current';
const atlasBacklogFilters = {concept: '', priority: '', owner: ''};
let atlasItemDetailState = atlasEmptyItemDetailState();
let atlasOwnerToken = '';
let atlasOwnerBusy = false;
let atlasOwnerReceipt = null;
let atlasOwnerError = '';

function atlasEmptyItemDetailState(requestID = 0) {
  return {
    itemID: '',
    scope: '',
    item: null,
    specifications: [],
    artifacts: Object.create(null),
    loading: false,
    error: '',
    requestID,
  };
}

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
    pipeline: [],
    queueFreezes: [],
    closureReceipts: [],
    stageReceipts: [],
    maturationMetrics: {},
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

function atlasRawItemID(item) {
  if (typeof item === 'string') return item.trim();
  const itemID = String(atlasField(item, ['item_id', 'id', 'unit_id'], '') || '').trim();
  if (itemID) return itemID;
  const featureID = String(atlasField(item, ['feature_id', 'key'], '') || '').trim();
  return featureID ? 'atlas:' + featureID : '';
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
  return atlasField(item, ['observed_at', 'observedAt', 'verified_at', 'verifiedAt', 'received_at', 'fetched_at', 'ingested_at', 'created_at', 'updated_at', 'timestamp', 'last_verified'], '');
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
    pipeline: atlasList(source.pipeline),
    queueFreezes: atlasList(source.queue_freezes || source.queueFreezes),
    closureReceipts: atlasList(source.closure_receipts || source.closureReceipts),
    stageReceipts: atlasList(source.stage_receipts || source.stageReceipts),
    maturationMetrics: source.maturation_metrics && typeof source.maturation_metrics === 'object' ? source.maturation_metrics : {},
  };
}

function atlasCurrentItems() {
  // Current is authoritative DONE-only. Catalog/features are metadata and
  // must never be presented as completed runtime functionality.
  return atlasProjection.current;
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
  const metrics = atlasProjection.maturationMetrics;
  const decisions = Number(metrics.decision_count || 0);
  root.innerHTML = [
    atlasSummaryCard('Current', String(current.length), 'DONE-only current projection', current.length ? 'ready' : 'empty'),
    atlasSummaryCard('Radar', String(atlasProjection.radar.length), 'new information to review', atlasProjection.radar.length ? 'pending' : 'empty'),
    atlasSummaryCard('Backlog', String(atlasProjection.backlog.length), 'concept-state items', atlasProjection.backlog.length ? 'pending' : 'empty'),
    atlasSummaryCard('Active Unit', activeTitle, 'Global WIP = 1', atlasProjection.active ? 'active' : 'empty'),
    atlasSummaryCard('Queue', String(atlasProjection.queue.length), 'adopted implementation units', atlasProjection.queue.length ? 'pending' : 'empty'),
    atlasSummaryCard('Catalog', String(catalogCount), 'catalog + feature entries', catalogCount ? 'ready' : 'empty'),
    atlasSummaryCard('Maturation', String(decisions), '30-day revalidation decisions', decisions ? 'ready' : 'empty'),
  ].join('');
}

function atlasDetailSources(item) {
  const sources = [];
  if (!item || typeof item !== 'object') return sources;
  sources.push(item);
  for (const key of ['design_card', 'designCard', 'purpose_card', 'purposeCard', 'card']) {
    const value = item[key];
    if (value && typeof value === 'object' && !Array.isArray(value)) sources.push(value);
  }
  return sources;
}

function atlasDetailField(item, keys, fallback = '') {
  for (const source of atlasDetailSources(item)) {
    const value = atlasField(source, keys, '');
    if (value !== '') return value;
  }
  return fallback;
}

function atlasHasValue(value) {
  if (value === null || value === undefined) return false;
  if (Array.isArray(value)) return value.length > 0;
  if (typeof value === 'string') return value.trim() !== '';
  return true;
}

function atlasDetailMarkup(label, value) {
  const content = atlasHasValue(value)
    ? atlasEscape(value)
    : '<span class="atlas-detail-unavailable">Unavailable</span>';
  return '<div class="atlas-detail-field"><dt>' + atlasEscape(label) + '</dt><dd>' + content + '</dd></div>';
}

function atlasDetailTrigger(item) {
  const itemID = atlasRawItemID(item);
  if (!itemID) return '';
  return '<button class="atlas-detail-trigger" type="button" data-atlas-item-id="' + atlasEscapeAttr(itemID) + '" aria-label="Open Atlas item detail">Details</button>';
}

function atlasSpecificationID(specification) {
  if (typeof specification === 'string') return specification.trim();
  return String(atlasField(specification, ['spec_id', 'specification_id', 'artifact_id', 'id', 'key'], '') || '').trim();
}

function atlasSpecificationList(value) {
  if (Array.isArray(value)) return value;
  if (!value || typeof value !== 'object') return typeof value === 'string' && value.trim() ? [value] : [];
  const nested = atlasList(value);
  if (nested.length) return nested;
  return atlasSpecificationID(value) ? [value] : [];
}

function atlasReferenceList(value) {
  if (Array.isArray(value)) return value;
  if (!atlasHasValue(value)) return [];
  return [value];
}

function atlasSpecificationRefs(payload, item) {
  const candidates = [];
  if (payload && typeof payload === 'object') {
    candidates.push(payload.specifications, payload.specification_refs, payload.spec_refs, payload.specification);
  }
  if (item && typeof item === 'object') {
    candidates.push(item.specifications, item.specification_refs, item.spec_refs, item.specification_ids, item.specification);
  }
  for (const candidate of candidates) {
    const references = atlasSpecificationList(candidate);
    if (references.length) return atlasUniqueSpecifications(references);
  }
  return [];
}

function atlasUniqueSpecifications(references) {
  const seen = new Set();
  return references.filter((reference) => {
    const id = atlasSpecificationID(reference);
    let key = id;
    if (!key) {
      try { key = JSON.stringify(reference); } catch (_) { key = String(reference); }
    }
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

function atlasRenderSpecificationArtifacts() {
  const references = atlasItemDetailState.specifications;
  if (!references.length) {
    return atlasEmpty('Specification unavailable', 'No allow-listed specification reference is present for this item.', true);
  }
  return '<div class="atlas-spec-list">' + references.map((reference) => {
    const specID = atlasSpecificationID(reference);
    const result = specID ? atlasItemDetailState.artifacts[specID] : null;
    if (!specID || !result || result.error || !result.artifact) {
      const detail = result && result.error ? result.error : 'Specification ID is unavailable; no body was requested.';
      return '<article class="atlas-spec-block"><div class="atlas-spec-heading"><strong>' + atlasEscape(specID || 'Specification') + '</strong></div>' + atlasEmpty('Specification unavailable', detail, true) + '</article>';
    }
    const artifact = result.artifact;
    const availability = artifact.content_available !== undefined ? artifact.content_available : artifact.body_available;
    const contentAvailable = availability !== false && String(availability || '').toLowerCase() !== 'false';
    const hasContent = contentAvailable && atlasHasValue(artifact.content);
    const body = hasContent
      ? '<pre class="atlas-spec-body">' + atlasEscape(artifact.content, 'Specification body unavailable') + '</pre>'
      : atlasEmpty('Specification body unavailable', 'Metadata was returned, but no readable body is available.', true);
    return '<article class="atlas-spec-block"><div class="atlas-spec-heading"><div><strong>' + atlasEscape(atlasField(artifact, ['title', 'name'], specID)) + '</strong><div class="atlas-code">' + atlasEscape(specID) + '</div></div>' + atlasBadge(atlasField(artifact, ['status', 'state'], 'available'), 'available') + '</div>' +
      '<dl class="atlas-detail-grid atlas-spec-meta">' +
      atlasDetailMarkup('Type', atlasField(artifact, ['type', 'kind'], '')) +
      atlasDetailMarkup('Source', atlasField(artifact, ['source', 'source_ref', 'locator'], '')) +
      atlasDetailMarkup('Revision', atlasField(artifact, ['revision', 'source_revision', 'commit', 'sha'], '')) +
      atlasDetailMarkup('Content SHA-256', atlasField(artifact, ['content_sha256', 'sha256'], '')) +
      atlasDetailMarkup('Captured At', atlasFormatTime(atlasField(artifact, ['captured_at', 'created_at', 'updated_at'], ''))) +
      '</dl>' + body + '</article>';
  }).join('') + '</div>';
}

function atlasRenderDetailSources(item) {
  const references = atlasReferenceList(atlasDetailField(item, ['source_refs', 'sourceRefs', 'sources'], []));
  if (!references.length) return atlasEmpty('Sources unavailable', 'No source references were returned for this item.', true);
  return '<ul class="atlas-detail-list">' + references.map((reference) => {
    if (typeof reference === 'string') return '<li>' + atlasEscape(reference) + '</li>';
    return '<li><strong>' + atlasEscape(atlasField(reference, ['type', 'source_type'], 'Source')) + '</strong><span>' + atlasEscape(atlasField(reference, ['locator', 'url', 'source'], 'Unavailable')) + '</span><span>Strength: ' + atlasEscape(atlasField(reference, ['strength', 'source_strength'], 'unresolved')) + '</span><span>' + atlasEscape(atlasField(reference, ['repository', 'revision', 'content_hash', 'sha256'], '')) + '</span></li>';
  }).join('') + '</ul>';
}

function atlasRenderDetailEvidence(item) {
  const references = atlasReferenceList(atlasDetailField(item, ['evidence_refs', 'evidenceRefs', 'evidence'], []));
  if (!references.length) return atlasEmpty('Evidence unavailable', 'No item evidence references were returned.', true);
  return '<ul class="atlas-detail-list">' + references.map((reference) => {
    if (typeof reference === 'string') return '<li>' + atlasEscape(reference) + '</li>';
    const passed = reference.passed === true ? 'passed' : reference.passed === false ? 'failed' : atlasField(reference, ['status', 'state', 'result'], 'recorded');
    return '<li><strong>' + atlasEscape(atlasField(reference, ['stage', 'kind', 'type'], 'Evidence')) + '</strong>' + atlasBadge(passed) + '<span>' + atlasEscape(atlasField(reference, ['ref', 'evidence_ref', 'id', 'commit', 'trace_id'], 'Unavailable')) + '</span><span>' + atlasEscape(atlasFormatTime(atlasField(reference, ['observed_at', 'created_at', 'timestamp'], ''))) + '</span></li>';
  }).join('') + '</ul>';
}

function atlasLatestRevalidation(item) {
  const records = atlasList(atlasField(item, ['revalidation_records'], []));
  return records.length ? records[records.length - 1] : null;
}

function atlasRenderRevalidationRecord(item) {
  const record = atlasLatestRevalidation(item);
  if (!record) return atlasEmpty('No revalidation receipt', 'RenCrow has not produced a maturation decision for this item.');
  const fields = [
    ['Decision', ['decision']], ['Reason', ['reason']], ['Maturation Days', ['maturation_days']],
    ['Necessity', ['necessity']], ['Duplication', ['duplication']], ['Mergeability', ['mergeability']],
    ['Architecture', ['architectural_consistency', 'architecture_impact']],
    ['Technology', ['technology_validity', 'technology_changes']],
    ['Implementation Value', ['implementation_value']], ['Timing', ['timing']],
    ['Related Backlogs', ['related_backlogs']], ['Conflicting Specs', ['conflicting_specs']],
    ['Merged Into', ['merged_into']], ['Next Review Trigger', ['next_review_trigger']],
    ['Review Agents', ['review_agents']], ['Bypass', ['bypass_reason']],
  ];
  return '<dl class="atlas-detail-grid">' + fields.map((field) => atlasDetailMarkup(field[0], atlasField(record, field[1], ''))).join('') + '</dl>';
}

function atlasOwnerHeaders() {
  return {
    'Authorization': 'Bearer ' + atlasOwnerToken,
    'Content-Type': 'application/json',
    'X-RenCrow-Client': 'RenCrow_CMD',
    'X-RenCrow-Interaction-Profile': 'cmd-control',
  };
}

function atlasReceiptMarkup() {
  if (atlasOwnerError) return '<div class="atlas-owner-receipt state-error"><strong>Request failed</strong><span>' + atlasEscape(atlasOwnerError) + '</span></div>';
  if (!atlasOwnerReceipt) return '<div class="atlas-owner-receipt"><strong>Receipt</strong><span>No owner action has been submitted in this page session.</span></div>';
  const record = atlasOwnerReceipt.revalidation_record || null;
  const item = atlasOwnerReceipt.item || null;
  const result = [
    atlasField(item, ['item_id'], atlasField(atlasOwnerReceipt, ['item_id'], '')),
    atlasField(record, ['decision'], ''),
    atlasField(record, ['revalidation_date'], atlasField(item, ['updated_at'], '')),
    atlasOwnerReceipt.duplicate === true ? 'duplicate' : '',
  ].filter(Boolean).join(' / ');
  return '<div class="atlas-owner-receipt state-idle"><strong>Receipt</strong><span>' + atlasEscape(result || 'CORE accepted the request') + '</span></div>';
}

function atlasRenderOwnerCredentials() {
  return '<section class="atlas-owner-card"><div class="atlas-subsection-head"><h4>Authenticated owner operation</h4><span>token is kept in memory only</span></div>' +
    '<label class="atlas-owner-field"><span>CORE owner token</span><input type="password" autocomplete="off" data-atlas-owner-token value="' + atlasEscapeAttr(atlasOwnerToken) + '" placeholder="Bearer token"></label>' +
    atlasReceiptMarkup() + '</section>';
}

function atlasRenderIntakeForm() {
  return '<section class="atlas-owner-card"><div class="atlas-subsection-head"><h4>Backlog Intake</h4><span>registration is not adoption</span></div>' +
    '<div class="atlas-owner-grid"><label class="atlas-owner-field"><span>Title</span><input data-atlas-intake="title" maxlength="240"></label>' +
    '<label class="atlas-owner-field"><span>Purpose</span><input data-atlas-intake="purpose" maxlength="1000"></label>' +
    '<label class="atlas-owner-field atlas-owner-wide"><span>Problem / context</span><textarea data-atlas-intake="problem" rows="3" maxlength="8000"></textarea></label>' +
    '<label class="atlas-owner-field"><span>Owner module</span><input data-atlas-intake="owner_module" placeholder="RenCrow_CORE"></label>' +
    '<label class="atlas-owner-field"><span>Priority</span><select data-atlas-intake="priority"><option>normal</option><option>high</option><option>urgent</option><option>low</option></select></label>' +
    '<label class="atlas-owner-field atlas-owner-wide"><span>Source URL or reference</span><input data-atlas-intake="source_locator" placeholder="https://… or an immutable reference"></label></div>' +
    '<div class="atlas-owner-actions"><button class="ctl-btn" type="button" data-atlas-owner-action="intake"' + (atlasOwnerBusy ? ' disabled' : '') + '>Register for maturation</button></div></section>';
}

function atlasRenderMaturationMetrics() {
  const metrics = atlasProjection.maturationMetrics || {};
  const percent = (value) => (Number(value || 0) * 100).toFixed(1) + '%';
  return '<section class="atlas-owner-card"><div class="atlas-subsection-head"><h4>Maturation metrics</h4><span>' + atlasEscape(String(metrics.window_days || 30)) + ' days</span></div><dl class="atlas-detail-grid">' +
    atlasDetailMarkup('Created', metrics.created_in_window) + atlasDetailMarkup('Creation / day', Number(metrics.creation_rate_per_day || 0).toFixed(2)) +
    atlasDetailMarkup('PROMOTE', String(metrics.promoted_count || 0) + ' / ' + percent(metrics.promotion_rate)) +
    atlasDetailMarkup('MERGE', String(metrics.merged_count || 0) + ' / ' + percent(metrics.merge_rate)) +
    atlasDetailMarkup('HOLD', String(metrics.held_count || 0) + ' / ' + percent(metrics.hold_rate)) +
    atlasDetailMarkup('DROP', String(metrics.dropped_count || 0) + ' / ' + percent(metrics.drop_rate)) +
    atlasDetailMarkup('Average maturation', Number(metrics.average_maturation_days || 0).toFixed(1) + ' days') +
    atlasDetailMarkup('Backlog growth', metrics.backlog_growth || 0) + '</dl></section>';
}

function atlasRenderOwnerDecision(item) {
  const concept = String(atlasField(item, ['concept_state'], '') || '').toUpperCase();
  const maturation = String(atlasField(item, ['maturation_state'], 'MATURATION') || '').toUpperCase();
  const eligibleAt = atlasField(item, ['maturation_eligible_at'], '');
  const latest = atlasLatestRevalidation(item);
  const recommendation = latest ? atlasField(latest, ['decision'], '') : 'Run RenCrow revalidation';
  const reason = latest ? atlasField(latest, ['reason'], '') : 'No semantic decision has been recorded.';
  return '<section class="atlas-detail-section atlas-owner-control"><div class="atlas-subsection-head"><h4>Maturation decision</h4><span>CORE owner boundary</span></div>' +
    '<dl class="atlas-detail-grid">' + atlasDetailMarkup('Current state', concept + ' / ' + maturation) + atlasDetailMarkup('Eligible at', atlasFormatTime(eligibleAt)) + atlasDetailMarkup('Recommended', recommendation) + atlasDetailMarkup('Recommendation reason', reason) + '</dl>' +
    '<div class="atlas-decision-impact-grid"><article><strong>PROMOTE</strong><span>Implementation Queueへの移動資格を与えます。</span><small>Risk: 新しい責務・維持コストを受け入れます。</small></article>' +
    '<article><strong>HOLD</strong><span>価値を保持し、event triggerまで実装しません。</span><small>Risk: 依存変化の見落としや機会遅延。</small></article>' +
    '<article><strong>DROP</strong><span>理由と履歴を保持して非採用にします。</span><small>Risk: 将来価値を捨てるため再発見が必要。</small></article>' +
    '<article><strong>MERGE</strong><span>RenCrowだけが実在する関連Itemへ統合できます。</span><small>Risk: 責務境界の誤統合。owner forceは禁止。</small></article></div>' +
    '<div class="atlas-owner-grid"><label class="atlas-owner-field"><span>Force decision</span><select data-atlas-decision><option value="">Select…</option><option value="PROMOTE">PROMOTE</option><option value="HOLD">HOLD</option><option value="DROP">DROP</option></select></label>' +
    '<label class="atlas-owner-field"><span>Emergency bypass</span><select data-atlas-bypass><option value="">None</option><option value="security_issue">Security issue</option><option value="data_loss_risk">Data loss risk</option><option value="production_failure">Production failure</option><option value="breaking_change">Breaking change</option><option value="bug_fix">Clear bug fix</option><option value="runtime_continuity">Runtime continuity</option></select></label>' +
    '<label class="atlas-owner-field atlas-owner-wide"><span>Reason</span><textarea data-atlas-decision-reason rows="3" maxlength="4000" placeholder="Decision rationale and impact"></textarea></label>' +
    '<label class="atlas-owner-field atlas-owner-wide"><span>HOLD next review trigger</span><input data-atlas-next-trigger maxlength="1000" placeholder="event that should trigger revalidation"></label></div>' +
    '<div class="atlas-owner-actions"><button class="ctl-btn" type="button" data-atlas-owner-action="revalidate"' + (atlasOwnerBusy ? ' disabled' : '') + '>Run RenCrow revalidation</button>' +
    '<button class="ctl-btn" type="button" data-atlas-owner-action="force"' + (atlasOwnerBusy ? ' disabled' : '') + '>Submit forced decision</button>' +
    (concept === 'RADAR' ? '<button class="ctl-btn" type="button" data-atlas-owner-action="candidate"' + (atlasOwnerBusy ? ' disabled' : '') + '>Start maturation</button>' : '') +
    (maturation === 'PROMOTED' ? '<button class="ctl-btn" type="button" data-atlas-owner-action="adopt"' + (atlasOwnerBusy ? ' disabled' : '') + '>Move to Implementation Queue</button>' : '') + '</div>' +
    '<p class="atlas-owner-help">Normal revalidation is generated by RenCrow after 7 days. Forced MERGE is intentionally unavailable. Early forced decisions require an allowed emergency bypass.</p>' +
    atlasReceiptMarkup() + '</section>';
}

function atlasRenderEnrichment() {
  return '<section class="atlas-detail-section atlas-owner-control"><div class="atlas-subsection-head"><h4>Maturation enrichment</h4><span>bounded mutable fields</span></div>' +
    '<div class="atlas-owner-grid"><label class="atlas-owner-field atlas-owner-wide"><span>Source URL / reference</span><input data-atlas-enrich="source_locator"></label>' +
    '<label class="atlas-owner-field"><span>Related Backlog ID</span><input data-atlas-enrich="related_id"></label>' +
    '<label class="atlas-owner-field"><span>Priority</span><select data-atlas-enrich="priority"><option value="">unchanged</option><option>normal</option><option>high</option><option>urgent</option><option>low</option></select></label>' +
    '<label class="atlas-owner-field atlas-owner-wide"><span>Body addition / replacement</span><textarea data-atlas-enrich="body" rows="3"></textarea></label>' +
    '<label class="atlas-owner-check"><input type="checkbox" data-atlas-enrich="material_change"><span>Major semantic change: restart the 7-day maturation period</span></label>' +
    '<label class="atlas-owner-field atlas-owner-wide"><span>Major change reason</span><input data-atlas-enrich="material_change_reason"></label></div>' +
    '<div class="atlas-owner-actions"><button class="ctl-btn" type="button" data-atlas-owner-action="enrich"' + (atlasOwnerBusy ? ' disabled' : '') + '>Save enrichment</button></div></section>';
}

function atlasRenderItemDetail(scope) {
  if (!atlasItemDetailState.itemID || atlasItemDetailState.scope !== scope) return '';
  const item = atlasItemDetailState.item || {item_id: atlasItemDetailState.itemID};
  const title = atlasItemTitle(item, 'Atlas item detail');
  if (atlasItemDetailState.loading) {
    return '<section class="atlas-item-detail" aria-live="polite"><div class="atlas-detail-head"><div><span class="atlas-kicker">ITEM DETAIL</span><h3>' + atlasEscape(title) + '</h3><div class="atlas-code">' + atlasEscape(atlasItemDetailState.itemID) + '</div></div></div>' + atlasEmpty('Loading item detail', 'Reading the read-only Atlas item and specification references.') + '</section>';
  }
  if (atlasItemDetailState.error) {
    return '<section class="atlas-item-detail" aria-live="polite"><div class="atlas-detail-head"><div><span class="atlas-kicker">ITEM DETAIL</span><h3>' + atlasEscape(title) + '</h3><div class="atlas-code">' + atlasEscape(atlasItemDetailState.itemID) + '</div></div></div>' + atlasEmpty('Item detail unavailable', atlasItemDetailState.error, true) + '</section>';
  }
  const cardFields = [
    ['Purpose', ['purpose', 'design_purpose', 'goal', 'objective']],
    ['Problem', ['problem', 'problem_statement', 'challenge', 'pain_point']],
    ['Idea', ['idea', 'proposal', 'solution', 'approach']],
    ['Background', ['background', 'context', 'rationale']],
    ['Expected Effect', ['expected_effect', 'expectedEffect', 'expected_outcome', 'outcome', 'benefit', 'benefits']],
    ['Relations', ['relation_refs', 'relations', 'related_ids', 'depends_on']],
  ];
  const ownershipFields = [
    ['Lifecycle Owner', ['owner_module', 'ownerModule']],
    ['Target Modules', ['target_modules', 'targetModules']],
    ['Consumer Modules', ['consumer_modules', 'consumerModules']],
    ['Affected Modules', ['affected_modules', 'affectedModules']],
  ];
  const sourceRefs = atlasReferenceList(atlasDetailField(item, ['source_refs', 'sourceRefs', 'sources'], []));
  const sourceStrengths = Array.from(new Set(sourceRefs.map((reference) => typeof reference === 'object' && reference ? atlasField(reference, ['strength', 'source_strength'], '') : '').filter(Boolean)));
  const sourceStrength = atlasDetailField(item, ['source_strength', 'sourceStrength', 'evidence_strength', 'source_confidence', 'confidence'], '') || sourceStrengths;
  const conceptState = atlasDetailField(item, ['concept_state', 'conceptState'], '');
  const deliveryState = atlasDetailField(item, ['delivery_state', 'deliveryState'], '');
  const implementationStatus = atlasDetailField(item, ['implementation_status', 'implementationStatus'], '') || [conceptState, deliveryState].filter(Boolean).join(' / ');
  return '<section class="atlas-item-detail" aria-live="polite"><div class="atlas-detail-head"><div><span class="atlas-kicker">ITEM DETAIL / OWNER-CONTROLLED</span><h3>' + atlasEscape(title) + '</h3><div class="atlas-code">' + atlasEscape(atlasItemDetailState.itemID) + '</div></div><button class="atlas-detail-close" type="button" data-atlas-detail-close="true">Close</button></div>' +
    '<section class="atlas-detail-section"><div class="atlas-subsection-head"><h4>Design Card</h4><span>Purpose and intent</span></div><dl class="atlas-detail-grid">' + cardFields.map((field) => atlasDetailMarkup(field[0], atlasDetailField(item, field[1], ''))).join('') + '</dl></section>' +
    '<section class="atlas-detail-section"><div class="atlas-subsection-head"><h4>Ownership and Module Scope</h4><span>Lifecycle responsibility and verification impact</span></div><dl class="atlas-detail-grid">' + ownershipFields.map((field) => atlasDetailMarkup(field[0], atlasDetailField(item, field[1], ''))).join('') + '</dl></section>' +
    '<section class="atlas-detail-section"><div class="atlas-subsection-head"><h4>Specification</h4><span>allow-listed metadata and body</span></div>' + atlasRenderSpecificationArtifacts() + '</section>' +
    '<section class="atlas-detail-section"><div class="atlas-subsection-head"><h4>Sources</h4><span>read-only references</span></div>' + atlasRenderDetailSources(item) + '</section>' +
    '<dl class="atlas-detail-grid atlas-detail-status">' + atlasDetailMarkup('Source Strength', sourceStrength) + atlasDetailMarkup('Implementation Status', implementationStatus) + '</dl>' +
    '<section class="atlas-detail-section"><div class="atlas-subsection-head"><h4>Evidence</h4><span>item evidence references</span></div>' + atlasRenderDetailEvidence(item) + '</section>' +
    '<section class="atlas-detail-section"><div class="atlas-subsection-head"><h4>Revalidation history</h4><span>latest append-only record</span></div>' + atlasRenderRevalidationRecord(item) + '</section>' +
    atlasRenderOwnerDecision(item) + atlasRenderEnrichment() + '</section>';
}

function atlasBindItemDetailControls(view) {
  view.querySelectorAll('[data-atlas-item-id]').forEach((button) => {
    button.addEventListener('click', () => atlasOpenItemDetail(button.dataset.atlasItemId, view.dataset.atlasView || atlasActiveTab));
  });
  view.querySelectorAll('[data-atlas-detail-close]').forEach((button) => {
    button.addEventListener('click', () => {
      atlasItemDetailState = atlasEmptyItemDetailState(atlasItemDetailState.requestID + 1);
      atlasRender();
    });
  });
  atlasBindOwnerControls(view);
}

function atlasOwnerValue(root, selector) {
  const input = root.querySelector(selector);
  return input ? String(input.value || '').trim() : '';
}

async function atlasOwnerPost(path, payload) {
  if (!atlasOwnerToken.trim()) throw new Error('CORE owner token is required');
  const response = await fetch(path, {method: 'POST', headers: atlasOwnerHeaders(), body: JSON.stringify(payload || {})});
  let body = null;
  try { body = await response.json(); } catch (_) { body = null; }
  if (!response.ok) {
    const detail = body && (body.error || body.message) ? (body.error || body.message) : 'HTTP ' + String(response.status);
    throw new Error(String(detail));
  }
  return body || {};
}

async function atlasRunOwnerAction(action, root) {
  if (atlasOwnerBusy) return;
  // Rendering replaces the form controls. Capture the visible owner input
  // before showing the busy state so the request reflects the clicked form.
  const input = {
    itemID: atlasItemDetailState.itemID,
    title: atlasOwnerValue(root, '[data-atlas-intake="title"]'),
    purpose: atlasOwnerValue(root, '[data-atlas-intake="purpose"]'),
    problem: atlasOwnerValue(root, '[data-atlas-intake="problem"]'),
    ownerModule: atlasOwnerValue(root, '[data-atlas-intake="owner_module"]'),
    intakePriority: atlasOwnerValue(root, '[data-atlas-intake="priority"]'),
    intakeLocator: atlasOwnerValue(root, '[data-atlas-intake="source_locator"]'),
    decision: atlasOwnerValue(root, '[data-atlas-decision]'),
    reason: atlasOwnerValue(root, '[data-atlas-decision-reason]'),
    bypass: atlasOwnerValue(root, '[data-atlas-bypass]'),
    nextTrigger: atlasOwnerValue(root, '[data-atlas-next-trigger]'),
    enrichLocator: atlasOwnerValue(root, '[data-atlas-enrich="source_locator"]'),
    relatedID: atlasOwnerValue(root, '[data-atlas-enrich="related_id"]'),
    enrichPriority: atlasOwnerValue(root, '[data-atlas-enrich="priority"]'),
    body: atlasOwnerValue(root, '[data-atlas-enrich="body"]'),
    materialChange: Boolean(root.querySelector('[data-atlas-enrich="material_change"]')?.checked),
    materialChangeReason: atlasOwnerValue(root, '[data-atlas-enrich="material_change_reason"]'),
  };
  atlasOwnerBusy = true;
  atlasOwnerError = '';
  atlasOwnerReceipt = null;
  atlasRender();
  try {
    let result;
    if (action === 'intake') {
      if (!input.title) throw new Error('Title is required');
      const payload = {title: input.title, purpose: input.purpose, problem: input.problem, owner_module: input.ownerModule, priority: input.intakePriority, source: 'ren'};
      if (input.intakeLocator) payload.source_refs = [{type: 'owner_input', locator: input.intakeLocator}];
      result = await atlasOwnerPost('/v1/atlas/intake', payload);
    } else {
      const itemID = input.itemID;
      if (!itemID) throw new Error('Atlas item is not selected');
      const base = '/v1/atlas/items/' + encodeURIComponent(itemID) + '/';
      if (action === 'candidate') {
        result = await atlasOwnerPost(base + 'candidate', {});
      } else if (action === 'revalidate') {
        result = await atlasOwnerPost(base + 'revalidate', {});
      } else if (action === 'force') {
        if (!input.decision || !input.reason) throw new Error('Force decision and reason are required');
        const payload = {decision: input.decision, reason: input.reason, forced: true};
        if (input.bypass) payload.bypass_reason = input.bypass;
        if (input.nextTrigger) payload.next_review_trigger = input.nextTrigger;
        result = await atlasOwnerPost(base + 'revalidate', payload);
      } else if (action === 'adopt') {
        if (!input.reason) throw new Error('Implementation Queue reason is required');
        result = await atlasOwnerPost(base + 'adopt', {reason: input.reason});
      } else if (action === 'enrich') {
        const payload = {material_change: input.materialChange};
        if (input.enrichLocator) payload.source_refs = [{type: 'owner_input', locator: input.enrichLocator}];
        if (input.relatedID) payload.related_ids = [input.relatedID];
        if (input.enrichPriority) payload.priority = input.enrichPriority;
        if (input.body) payload.body = input.body;
        if (input.materialChangeReason) payload.material_change_reason = input.materialChangeReason;
        result = await atlasOwnerPost(base + 'enrich', payload);
      } else {
        throw new Error('Unknown owner operation');
      }
    }
    atlasOwnerReceipt = result;
    await refreshAtlas();
    const returnedItem = result && result.item;
    if (returnedItem && atlasItemDetailState.itemID === returnedItem.item_id) {
      await atlasOpenItemDetail(returnedItem.item_id, 'backlog');
    }
  } catch (error) {
    atlasOwnerError = String(error && error.message ? error.message : error);
  } finally {
    atlasOwnerBusy = false;
    atlasRender();
  }
}

function atlasBindOwnerControls(root) {
  root.querySelectorAll('[data-atlas-owner-token]').forEach((input) => {
    if (input.dataset.bound === '1') return;
    input.dataset.bound = '1';
    input.addEventListener('input', () => { atlasOwnerToken = String(input.value || ''); });
  });
  root.querySelectorAll('[data-atlas-owner-action]').forEach((button) => {
    if (button.dataset.bound === '1') return;
    button.dataset.bound = '1';
    button.addEventListener('click', () => atlasRunOwnerAction(button.dataset.atlasOwnerAction || '', root));
  });
}

async function atlasOpenItemDetail(itemID, scope) {
  const normalizedID = String(itemID || '').trim();
  if (!normalizedID) return;
  const requestID = atlasItemDetailState.requestID + 1;
  atlasItemDetailState = atlasEmptyItemDetailState(requestID);
  atlasItemDetailState.itemID = normalizedID;
  atlasItemDetailState.scope = scope;
  atlasItemDetailState.loading = true;
  atlasRender();
  try {
    const response = await fetch('/viewer/atlas/items/' + encodeURIComponent(itemID), {cache: 'no-store'});
    if (!response.ok) throw new Error('HTTP ' + String(response.status));
    const payload = await response.json();
    const item = payload && payload.item && typeof payload.item === 'object' ? payload.item : payload;
    if (!item || typeof item !== 'object' || Array.isArray(item)) throw new Error('invalid Atlas item detail');
    const specifications = atlasSpecificationRefs(payload, item);
    const artifacts = Object.create(null);
    for (const reference of specifications) {
      const specID = atlasSpecificationID(reference);
      if (!specID) {
        artifacts[''] = {error: 'Specification ID is unavailable'};
        continue;
      }
      try {
        const specificationResponse = await fetch('/viewer/atlas/specifications/' + encodeURIComponent(specID), {cache: 'no-store'});
        if (!specificationResponse.ok) throw new Error('HTTP ' + String(specificationResponse.status));
        const specificationPayload = await specificationResponse.json();
        const artifact = specificationPayload && specificationPayload.specification && typeof specificationPayload.specification === 'object'
          ? specificationPayload.specification
          : specificationPayload;
        if (!artifact || typeof artifact !== 'object' || Array.isArray(artifact)) throw new Error('invalid SpecificationArtifact');
        artifacts[specID] = {artifact};
      } catch (error) {
        artifacts[specID] = {error: String(error && error.message ? error.message : error)};
      }
    }
    if (requestID !== atlasItemDetailState.requestID) return;
    atlasItemDetailState.item = item;
    atlasItemDetailState.specifications = specifications;
    atlasItemDetailState.artifacts = artifacts;
  } catch (error) {
    if (requestID !== atlasItemDetailState.requestID) return;
    atlasItemDetailState.error = String(error && error.message ? error.message : error);
  } finally {
    if (requestID === atlasItemDetailState.requestID) {
      atlasItemDetailState.loading = false;
      atlasRender();
    }
  }
}

function atlasRenderCurrent(view) {
  const items = atlasCurrentItems();
  if (!items.length) {
    view.innerHTML = atlasEmpty('Current is empty', 'No current, feature, or catalog entries are available.');
    return;
  }
  const sourceName = atlasProjection.current.length ? 'current' : (atlasProjection.features.length ? 'features' : 'catalog');
  view.innerHTML = '<div class="atlas-view-heading"><div><span class="atlas-kicker">CURRENT</span><h3>現在の機能</h3><p>実装状態は読み取り専用の Atlas projection から表示します。</p></div><span class="atlas-source-note">source: ' + atlasEscape(sourceName) + '</span></div>' +
    '<div class="atlas-table-wrap"><table class="atlas-table"><thead><tr><th>Feature</th><th>Category</th><th>Owner</th><th>Concept</th><th>Delivery</th><th>Implementation revision</th><th>Evidence</th></tr></thead><tbody>' +
    items.map((item) => {
      let evidence;
      if (item && typeof item === 'object' && Object.prototype.hasOwnProperty.call(item, 'evidence_refs')) {
        evidence = item.evidence_refs;
      } else if (item && typeof item === 'object' && Object.prototype.hasOwnProperty.call(item, 'evidenceRefs')) {
        evidence = item.evidenceRefs;
      } else {
        evidence = atlasField(item, ['evidence_count', 'evidenceCount', 'evidence'], '');
      }
      const evidenceValue = Array.isArray(evidence) ? evidence.length : atlasDisplay(evidence, '0');
      return '<tr><td><strong>' + atlasEscape(atlasItemTitle(item)) + '</strong><div class="atlas-code">' + atlasEscape(atlasItemID(item)) + '</div><div class="atlas-detail-actions">' + atlasDetailTrigger(item) + '</div></td>' +
        '<td>' + atlasEscape(atlasItemCategory(item)) + '</td><td>' + atlasEscape(atlasItemOwner(item)) + '</td>' +
        '<td>' + atlasBadge(atlasConceptState(item)) + '</td><td>' + atlasBadge(atlasDeliveryState(item)) + '</td>' +
        '<td class="atlas-code">' + atlasEscape(atlasField(item, ['implementation_revision', 'implementationRevision', 'revision', 'source_revision', 'commit', 'sha'], '-')) + '</td>' +
        '<td>' + atlasEscape(evidenceValue) + '</td></tr>';
    }).join('') + '</tbody></table></div>' + atlasRenderItemDetail('current');
  atlasBindItemDetailControls(view);
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
  const pending = allItems.filter((item) => ['MATURATION', 'REVALIDATION', 'HOLD'].includes(String(atlasField(item, ['maturation_state'], 'MATURATION')).toUpperCase())).length;
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
    view.innerHTML = '<div class="atlas-view-heading"><div><span class="atlas-kicker">BACKLOG</span><h3>技術熟成領域</h3></div><span class="atlas-source-note">pending 0 / total 0</span></div>' + atlasRenderOwnerCredentials() + atlasRenderIntakeForm() + atlasRenderMaturationMetrics() + filterBar + atlasEmpty('Backlog is empty', 'No Atlas backlog items are available.');
    atlasBindBacklogFilters(view);
    atlasBindOwnerControls(view);
    return;
  }
  view.innerHTML = '<div class="atlas-view-heading"><div><span class="atlas-kicker">BACKLOG</span><h3>技術熟成領域</h3><p>登録は採用ではありません。7日経過後もRenCrowの再検証結果と理由を確認して判断します。</p></div><span class="atlas-source-note">pending ' + String(pending) + ' / total ' + String(allItems.length) + ' / filtered ' + String(filtered.length) + '</span></div>' + atlasRenderOwnerCredentials() + atlasRenderIntakeForm() + atlasRenderMaturationMetrics() + filterBar +
    '<div class="atlas-table-wrap"><table class="atlas-table"><thead><tr><th>Item</th><th>Concept State</th><th>Priority</th><th>Owner</th><th>Summary</th><th>Updated</th></tr></thead><tbody>' +
    (filtered.length ? filtered.map((item) => '<tr><td><strong>' + atlasEscape(atlasItemTitle(item)) + '</strong><div class="atlas-code">' + atlasEscape(atlasItemID(item)) + '</div><div class="atlas-detail-actions">' + atlasDetailTrigger(item) + '</div></td>' +
      '<td>' + atlasBadge(atlasConceptState(item)) + '</td><td>' + atlasEscape(atlasField(item, ['priority', 'importance'], '-')) + '</td>' +
      '<td>' + atlasEscape(atlasItemOwner(item)) + '</td><td class="atlas-wrap">' + atlasEscape(atlasField(item, ['summary', 'body', 'description', 'note'], '-')) + '</td>' +
      '<td class="atlas-time">' + atlasEscape(atlasFormatTime(atlasField(item, ['updated_at', 'created_at', 'timestamp'], ''))) + '</td></tr>').join('') : '<tr><td colspan="6">' + atlasEmpty('No matching backlog items', 'Change the read-only filters to inspect other states.') + '</td></tr>') +
    '</tbody></table></div>' + atlasRenderItemDetail('backlog');
  atlasBindBacklogFilters(view);
  atlasBindItemDetailControls(view);
  atlasBindOwnerControls(view);
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

function atlasRenderStages(entry) {
  const stages = atlasList(atlasField(entry, ['stages', 'pipeline', 'delivery_stages', 'deliveryStages'], []));
  if (!stages.length) return atlasEmpty('Pipeline unavailable', 'CORE returned no authoritative stage projection for this implementation unit.', true);
  return '<ol class="atlas-stage-list">' + stages.map((stage, index) => {
    const value = typeof stage === 'string' ? stage : atlasField(stage, ['status', 'state', 'result'], 'pending');
    const reason = typeof stage === 'object' ? atlasField(stage, ['reason', 'summary', 'detail'], '') : '';
    const verified = typeof stage === 'object' ? atlasList(atlasField(stage, ['verified_evidence_refs', 'verifiedEvidenceRefs'], [])) : [];
    const claims = typeof stage === 'object' ? atlasList(atlasField(stage, ['evidence_refs', 'evidenceRefs'], [])) : [];
    const evidenceDetail = claims.length ? '<p>Evidence: ' + atlasEscape(String(verified.length) + ' CORE verified / ' + String(claims.length) + ' claims') + '</p>' : '';
    return '<li class="atlas-stage ' + atlasStatusClass(value) + '"><span class="atlas-stage-marker">' + atlasEscape(String(index + 1)) + '</span><div><strong>' + atlasEscape(atlasStageName(stage, index)) + '</strong><span>' + atlasBadge(value, 'pending') + '</span>' + (reason ? '<p>' + atlasEscape(reason) + '</p>' : '') + evidenceDetail + '</div></li>';
  }).join('') + '</ol>';
}

function atlasRenderQueue(queue) {
  if (!queue.length) return atlasEmpty('Queue is empty', 'No adopted implementation units are waiting.');
  return '<div class="atlas-table-wrap"><table class="atlas-table"><thead><tr><th>Unit</th><th>Owner</th><th>Concept</th><th>Delivery</th><th>Queued</th></tr></thead><tbody>' + queue.map((item) => '<tr><td><strong>' + atlasEscape(atlasItemTitle(item, 'Implementation unit')) + '</strong><div class="atlas-code">' + atlasEscape(atlasItemID(item)) + '</div></td><td>' + atlasEscape(atlasItemOwner(item)) + '</td><td>' + atlasBadge(atlasConceptState(item)) + '</td><td>' + atlasBadge(atlasDeliveryState(item)) + '</td><td class="atlas-time">' + atlasEscape(atlasFormatTime(atlasField(item, ['queued_at', 'created_at', 'timestamp'], ''))) + '</td></tr>').join('') + '</tbody></table></div>';
}

function atlasPipelineUnitID(entry) {
  return atlasDisplay(atlasField(entry, ['unit_id', 'unitID', 'implementation_unit_id', 'implementationUnit'], ''), '-');
}

function atlasRenderPipelineRecords(entry) {
  const freezes = atlasList(atlasField(entry, ['queue_freezes', 'queueFreezes'], []));
  const closures = atlasList(atlasField(entry, ['closure_receipts', 'closureReceipts'], []));
  const activeLease = atlasField(entry, ['active_lease', 'activeLease'], null);
  const records = [];
  if (activeLease && typeof activeLease === 'object') {
    records.push('<div class="atlas-pipeline-record atlas-pipeline-lease"><strong>Active lease</strong><span>' + atlasEscape(atlasField(activeLease, ['holder_unit_id', 'holderUnitID', 'stage'], '-')) + '</span></div>');
  }
  freezes.forEach((freeze) => {
    const state = atlasField(freeze, ['status', 'state'], 'active');
    records.push('<div class="atlas-pipeline-record atlas-pipeline-freeze"><strong>Queue freeze: ' + atlasEscape(state) + '</strong><span>' + atlasEscape(atlasField(freeze, ['reason_code', 'reasonCode', 'invalidated_from_stage'], 'Unavailable')) + '</span><span>replacement: ' + atlasEscape(atlasField(freeze, ['replacement_unit_id', 'replacementUnitID'], 'Unavailable')) + '</span></div>');
  });
  closures.forEach((closure) => {
    const state = atlasField(closure, ['status', 'phase'], 'unavailable');
    records.push('<div class="atlas-pipeline-record atlas-pipeline-closure"><strong>Closure: ' + atlasEscape(state) + '</strong><span>lease released: ' + atlasEscape(atlasField(closure, ['lease_released', 'leaseReleased'], 'Unavailable')) + '</span><span>' + atlasEscape(atlasField(closure, ['completed_at', 'updated_at'], 'Unavailable')) + '</span></div>');
  });
  return records.length ? '<div class="atlas-pipeline-records">' + records.join('') + '</div>' : '<div class="atlas-pipeline-records"><span class="atlas-detail-unavailable">No lease, freeze, or closure receipt is available.</span></div>';
}

function atlasRenderPipelineUnit(entry, activeUnitID) {
  const unitID = atlasPipelineUnitID(entry);
  const active = unitID === activeUnitID;
  const state = atlasField(entry, ['delivery_state', 'deliveryState', 'state', 'status'], 'unavailable');
  return '<article class="atlas-pipeline-unit' + (active ? ' atlas-pipeline-unit-active' : '') + '"><div class="atlas-active-head"><div><span class="atlas-kicker">' + (active ? 'ACTIVE IMPLEMENTATION UNIT' : 'IMPLEMENTATION UNIT') + '</span><h3>' + atlasEscape(atlasItemTitle(entry, 'Implementation unit')) + '</h3><div class="atlas-code">' + atlasEscape(unitID) + '</div></div>' + atlasBadge(state, 'unavailable') + '</div><div class="atlas-active-meta"><span>Owner: <strong>' + atlasEscape(atlasItemOwner(entry)) + '</strong></span><span>Concept: ' + atlasBadge(atlasConceptState(entry)) + '</span><span>Revision: <strong>' + atlasEscape(atlasField(entry, ['implementation_revision', 'implementationRevision', 'revision'], 'unavailable')) + '</strong></span></div><div class="atlas-stage-heading"><h4>Stages</h4><span>Specification → Done</span></div>' + atlasRenderStages(entry) + atlasRenderPipelineRecords(entry) + '</article>';
}

function atlasRenderPipeline(view) {
  const active = atlasProjection.active;
  const activeUnitID = active ? atlasDisplay(atlasField(active, ['implementation_unit_id', 'implementationUnit', 'unit_id'], ''), '') : '';
  const units = atlasProjection.pipeline.slice();
  if (!units.length && active) units.push(active);
  const pipelineBlock = units.length ? '<div class="atlas-pipeline-grid">' + units.map((entry) => atlasRenderPipelineUnit(entry, activeUnitID)).join('') + '</div>' : atlasEmpty('No implementation unit', 'Global WIP = 1; CORE returned no implementation-unit projection.');
  view.innerHTML = '<div class="atlas-view-heading"><div><span class="atlas-kicker">PIPELINE</span><h3>Implementation Pipeline</h3><p>COREのauthoritative Delivery State、verified Evidence、Lease、Freeze、Closure Receiptを表示します。</p></div><div class="atlas-wip"><span>Global WIP</span><strong>1</strong></div></div>' + pipelineBlock + '<section class="atlas-subsection"><div class="atlas-subsection-head"><h4>Queue</h4><span>read-only</span></div>' + atlasRenderQueue(atlasProjection.queue) + '</section>';
}

function atlasRenderEvidence(view) {
  const items = atlasProjection.evidence;
  if (!items.length) {
    view.innerHTML = '<div class="atlas-view-heading"><div><span class="atlas-kicker">EVIDENCE</span><h3>Implementation Evidence</h3></div></div>' + atlasEmpty('Evidence is empty', 'No stage evidence is available in the projection.');
    return;
  }
  view.innerHTML = '<div class="atlas-view-heading"><div><span class="atlas-kicker">EVIDENCE</span><h3>Stage evidence timeline</h3><p>実装完了の根拠を stage ごとに確認します。</p></div></div><ol class="atlas-evidence-timeline">' + items.map((item, index) => {
    const stage = atlasField(item, ['stage', 'stage_name', 'kind', 'type'], 'Evidence');
    const verified = Boolean(atlasField(item, ['verified'], false)) || String(atlasField(item, ['verification_result', 'verificationResult'], '')).toLowerCase() === 'verified';
    const claim = Boolean(atlasField(item, ['passed'], false));
    const status = verified ? 'verified' : (claim ? 'claim only' : 'unverified');
    const verifier = atlasField(item, ['verifier'], 'Unavailable');
    const hash = atlasField(item, ['sha256', 'hash'], 'Unavailable');
    const revision = atlasField(item, ['revision'], 'Unavailable');
    const observedAt = atlasField(item, ['observed_at', 'observedAt', 'verified_at'], 'Unavailable');
    return '<li class="atlas-evidence-item ' + atlasStatusClass(status) + '"><span class="atlas-evidence-line"></span><div class="atlas-evidence-marker">' + atlasEscape(String(index + 1)) + '</div><article><div class="atlas-evidence-head"><strong>' + atlasEscape(stage) + '</strong>' + atlasBadge(status) + '</div><p>Claim: ' + atlasEscape(claim ? 'passed' : 'not passed') + ' / CORE verified: ' + atlasEscape(verified ? 'verified' : 'not verified') + '</p><div class="atlas-evidence-meta"><span>verifier: ' + atlasEscape(verifier) + '</span><span>hash: ' + atlasEscape(hash) + '</span><span>revision: ' + atlasEscape(revision) + '</span><span>observed_at: ' + atlasEscape(observedAt) + '</span><span>ref: ' + atlasEscape(atlasField(item, ['ref', 'evidence_ref', 'id', 'commit', 'trace_id'], '-')) + '</span></div></article></li>';
  }).join('') + '</ol>';
}

function atlasRenderModules(view) {
  const modules = atlasProjection.modules;
  if (!modules.length) {
    view.innerHTML = '<div class="atlas-view-heading"><div><span class="atlas-kicker">MODULES</span><h3>EcoSystem modules</h3></div></div>' + atlasEmpty('Modules are empty', 'No module revision or runtime health projection is available.');
    return;
  }
  view.innerHTML = '<div class="atlas-view-heading"><div><span class="atlas-kicker">MODULES</span><h3>EcoSystem module status</h3><p>catalog metadata と runtime evidence を分離して表示します。</p></div></div><div class="atlas-module-grid">' + modules.map((module) => {
    const runtimeAvailable = atlasField(module, ['runtime_evidence_available', 'runtimeEvidenceAvailable'], false) === true;
    const health = runtimeAvailable ? atlasField(module, ['runtime_health', 'runtimeHealth', 'health', 'status'], 'unavailable') : 'unavailable';
    const revision = runtimeAvailable ? atlasField(module, ['revision', 'source_revision', 'commit', 'sha'], 'unavailable') : 'unavailable';
    const catalogRevision = atlasField(module, ['catalog_revision', 'revision'], 'Unavailable');
    const catalogHealth = atlasField(module, ['catalog_runtime_health'], 'Unavailable');
    return '<article class="atlas-module-card"><div class="atlas-module-head"><strong>' + atlasEscape(atlasField(module, ['name', 'module', 'id', 'component'], 'Module')) + '</strong>' + atlasBadge(health) + '</div><dl><div><dt>Runtime revision</dt><dd class="atlas-code">' + atlasEscape(revision) + '</dd></div><div><dt>Catalog revision</dt><dd class="atlas-code">' + atlasEscape(catalogRevision) + '</dd></div><div><dt>Catalog health label</dt><dd>' + atlasEscape(catalogHealth) + '</dd></div><div><dt>Last verified</dt><dd>' + atlasEscape(runtimeAvailable ? atlasFormatTime(atlasField(module, ['last_verified', 'lastVerified', 'verified_at'], 'unavailable')) : 'unavailable') + '</dd></div><div><dt>Owner</dt><dd>' + atlasEscape(atlasField(module, ['owner', 'owner_module'], '-')) + '</dd></div></dl></article>';
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
    Boolean(atlasProjection.active) || atlasProjection.evidence.length > 0 || atlasProjection.modules.length > 0 || atlasProjection.pipeline.length > 0 || atlasProjection.queueFreezes.length > 0 || atlasProjection.closureReceipts.length > 0;
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
