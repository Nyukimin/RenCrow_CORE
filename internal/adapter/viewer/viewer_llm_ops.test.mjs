import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import {createRequire} from 'node:module';
import {fileURLToPath} from 'node:url';

const require = createRequire(import.meta.url);
const here = path.dirname(fileURLToPath(import.meta.url));
const read = (relative) => fs.readFileSync(path.join(here, relative), 'utf8');
const llmOps = () => require('./assets/js/tabs/llm-ops.js');

const sampleNode = {
  node_id: 'node-a', node_name: 'Node A', status: 'online', os: 'linux',
  cpu_name: 'Sample CPU', cpu_cores: 16, total_ram_gb: 64, available_ram_gb: 40,
  has_gpu: true, gpu_count: 2,
  gpus: [
    {name: 'Sample GPU', vram_gb: 24, available_vram_gb: 20, memory_bandwidth_gbs: 900},
    {name: 'Sample GPU', vram_gb: 24, available_vram_gb: 22, memory_bandwidth_gbs: 900},
  ],
  unified_memory: false, backend: 'cuda', collected_at: '2026-09-15T01:02:03Z', source: 'llmfit', error: '',
};

function sampleModel(overrides) {
  return Object.assign({
    model_id: 'sample/model-7b', provider: 'sample', parameter_count: '7B', params_b: 7, is_moe: false,
    fit_level: 'good', score: 82.5, scores: {quality: 70, speed: 80, fit: 90, context: 85},
    runtime: 'llama.cpp', run_mode: 'gpu', best_quant: 'Q4_K_M',
    context: {native: 131072, usable: 32768, evaluated: 8192},
    memory: {required_gb: 5.5, available_gb: 20, utilization_pct: 27},
    performance: {estimated_tps: 42, measured_tps: null, prefill_tps: null, ttft_ms: null},
    estimate_confidence: 'estimated', installed: true, disk_size_gb: 4.1,
    capabilities: ['chat'], license: 'apache-2.0', notes: ['sample note'],
  }, overrides || {});
}

function sampleInput(overrides) {
  return Object.assign({
    nodes: {generated_at: '2026-09-15T01:03:00Z', nodes: [sampleNode]},
    nodeModels: {
      'node-a': {generated_at: '2026-09-15T01:03:00Z', node_id: 'node-a', status: 'online', collected_at: '2026-09-15T01:02:03Z', error: '', models: [sampleModel()]},
    },
    errors: {models: {}},
  }, overrides || {});
}

test('LLM Ops normalizes node status and keeps unknown values visible as labels', () => {
  const {buildLlmOpsNodeCard, normalizeLlmOpsNodeStatus} = llmOps();
  const cases = [
    ['online', 'online', 'ONLINE'],
    ['OFFLINE', 'offline', 'OFFLINE'],
    ['stale', 'stale', 'STALE'],
    ['disabled', 'disabled', 'DISABLED'],
    ['degraded', 'unknown', 'DEGRADED'],
    [null, 'unknown', 'UNKNOWN'],
  ];
  for (const [input, key, label] of cases) {
    assert.equal(normalizeLlmOpsNodeStatus(input), key);
    const card = buildLlmOpsNodeCard({node_id: 'n1', status: input});
    assert.equal(card.statusKey, key);
    assert.equal(card.statusLabel, label);
  }
});

test('LLM Ops node card carries GPU / VRAM / RAM / Backend / Last Updated', () => {
  const {buildLlmOpsNodeCard, renderLlmOpsNodeCardsHTML} = llmOps();
  const card = buildLlmOpsNodeCard(sampleNode);
  assert.equal(card.name, 'Node A');
  assert.equal(card.gpu, '2x Sample GPU');
  assert.equal(card.vram, '48.0 GB (42.0 GB free)');
  assert.equal(card.ram, '64.0 GB (40.0 GB free)');
  assert.equal(card.backend, 'cuda');
  assert.equal(card.cpu, 'Sample CPU (16 cores)');
  assert.notEqual(card.lastUpdated, '-');

  const missing = buildLlmOpsNodeCard({node_id: 'n2', status: 'offline', collected_at: null, has_gpu: false, error: 'unreachable'});
  assert.equal(missing.name, 'n2');
  assert.equal(missing.gpu, 'none');
  assert.equal(missing.vram, '-');
  assert.equal(missing.ram, '-');
  assert.equal(missing.backend, '-');
  assert.equal(missing.lastUpdated, '-');

  const html = renderLlmOpsNodeCardsHTML([card, missing]);
  for (const label of ['GPU', 'VRAM', 'RAM', 'Backend', 'CPU', 'Last Updated']) {
    assert.match(html, new RegExp('<dt>' + label + '</dt>'));
  }
  assert.match(html, /llm-ops-badge state-online">ONLINE</);
  assert.match(html, /llm-ops-badge state-offline">OFFLINE</);
  assert.match(html, /llm-ops-node-error">unreachable</);
  assert.doesNotMatch(html, /null|undefined/);
});

test('LLM Ops fit row shows "-" for null best_quant and separates Estimated from Measured', () => {
  const {buildLlmOpsFitRow, renderLlmOpsFitRowsHTML, formatLlmOpsTps} = llmOps();
  const noTps = buildLlmOpsFitRow(sampleModel({
    best_quant: null,
    performance: {estimated_tps: null, measured_tps: null, prefill_tps: null, ttft_ms: null},
  }), sampleNode);
  assert.equal(noTps.quant, '-');
  assert.equal(noTps.estimatedTps, null);
  assert.equal(noTps.measuredTps, null);
  assert.equal(formatLlmOpsTps(noTps.estimatedTps), '-');
  assert.equal(formatLlmOpsTps(noTps.measuredTps), '-');
  assert.equal(formatLlmOpsTps(0), '0.0 tok/s', 'numeric zero stays distinct from nil');

  const estimatedOnly = buildLlmOpsFitRow(sampleModel(), sampleNode);
  const measured = buildLlmOpsFitRow(sampleModel({
    model_id: 'sample/model-measured',
    performance: {estimated_tps: 40, measured_tps: 37.4, prefill_tps: 300, ttft_ms: 120},
    estimate_confidence: 'measured_local',
  }), sampleNode);

  const html = renderLlmOpsFitRowsHTML([noTps, estimatedOnly, measured]);
  assert.doesNotMatch(html, /null|undefined/);
  assert.match(html, /<span class="llm-ops-tps-kind">est<\/span> 42\.0 tok\/s/);
  assert.match(html, /<span class="llm-ops-tps-kind">meas<\/span> -/);
  assert.match(html, /<span class="llm-ops-tps-kind">meas<\/span> 37\.4 tok\/s/);
  const tpsNumbers = html.match(/\d+\.\d tok\/s/g) || [];
  const labelledTps = html.match(/llm-ops-tps-kind">(est|meas)<\/span> \d+\.\d tok\/s/g) || [];
  assert.ok(tpsNumbers.length >= 3);
  assert.equal(tpsNumbers.length, labelledTps.length, 'every tok/s value must carry an est/meas label');
});

test('LLM Ops maps confidence to distinct labels and keeps unknown values apart', () => {
  const {llmOpsConfidence, buildLlmOpsFitRow, renderLlmOpsFitRowsHTML} = llmOps();
  const expected = {
    measured_local: 'LOCAL MEASURED',
    measured_community: 'COMMUNITY MEASURED',
    calibrated: 'CALIBRATED',
    estimated: 'ESTIMATED',
    unsupported: 'UNSUPPORTED',
  };
  for (const [input, label] of Object.entries(expected)) {
    const confidence = llmOpsConfidence(input);
    assert.equal(confidence.label, label);
    assert.equal(confidence.known, true);
    assert.equal(confidence.key, input.replace(/_/g, '-'));
  }
  const unknown = llmOpsConfidence('vendor_claimed');
  assert.equal(unknown.known, false);
  assert.equal(unknown.key, 'unknown');
  assert.equal(unknown.label, 'VENDOR_CLAIMED');
  assert.ok(!Object.values(expected).includes(unknown.label));

  const rows = Object.keys(expected).concat(['vendor_claimed']).map((confidence, index) => {
    return buildLlmOpsFitRow(sampleModel({model_id: 'model-' + index, estimate_confidence: confidence}), sampleNode);
  });
  const html = renderLlmOpsFitRowsHTML(rows);
  for (const [input, label] of Object.entries(expected)) {
    assert.match(html, new RegExp('llm-ops-badge conf-' + input.replace(/_/g, '-') + '">' + label + '<'));
  }
  assert.match(html, /llm-ops-badge conf-unknown">VENDOR_CLAIMED</);
});

test('LLM Ops keeps Native / Usable / Evaluated context separately', () => {
  const {buildLlmOpsFitRow, renderLlmOpsFitRowsHTML, renderLlmOpsDetailHTML, llmOpsFit} = llmOps();
  const row = buildLlmOpsFitRow(sampleModel(), sampleNode);
  assert.deepEqual(row.context, {native: 131072, usable: 32768, evaluated: 8192});
  assert.equal(llmOpsFit('too_tight').label, 'TOO TIGHT');
  assert.equal(llmOpsFit('too_tight').key, 'too-tight');

  const listHTML = renderLlmOpsFitRowsHTML([row]);
  assert.match(listHTML, /32,768/);
  assert.doesNotMatch(listHTML, /131,072|8,192/, 'list view shows Usable only');

  const detail = renderLlmOpsDetailHTML(row, {matrixState: 'loading'});
  assert.match(detail, /<dt>Native<\/dt><dd>131,072<\/dd>/);
  assert.match(detail, /<dt>Usable<\/dt><dd>32,768<\/dd>/);
  assert.match(detail, /<dt>Evaluated<\/dt><dd>8,192<\/dd>/);
  assert.match(detail, /<dt>Estimated TPS<\/dt><dd>42\.0 tok\/s<\/dd>/);
  assert.match(detail, /<dt>Measured TPS<\/dt><dd>-<\/dd>/);
  assert.match(detail, /<dt>Quality<\/dt>[\s\S]*<dt>Speed<\/dt>[\s\S]*<dt>Fit<\/dt>[\s\S]*<dt>Context<\/dt>[\s\S]*<dt>Overall<\/dt>/);
  assert.match(detail, /<dt>Confidence<\/dt><dd>ESTIMATED<\/dd>/);
  assert.match(detail, /<dt>Utilization<\/dt><dd>27%<\/dd>/);
  assert.match(detail, /Loading fit across nodes/);
});

test('LLM Ops shows LLMFIT OFFLINE with Last Updated and marks held values STALE', () => {
  const {buildLlmOpsViewModel, renderLlmOpsSummaryHTML, renderLlmOpsNodeCardsHTML, renderLlmOpsFitRowsHTML} = llmOps();
  const good = buildLlmOpsViewModel(sampleInput());
  assert.equal(good.source.label, 'LLMFIT ONLINE');
  assert.equal(good.source.stale, false);
  assert.equal(good.summary.length, 5);
  assert.deepEqual(good.summary.map((block) => block.key), ['source', 'nodes', 'fits', 'tps', 'best']);
  assert.equal(good.nodeCards[0].statusLabel, 'ONLINE');
  assert.equal(good.fitRows.length, 1);
  assert.match(renderLlmOpsSummaryHTML(good), /source-online">LLMFIT ONLINE</);

  const held = buildLlmOpsViewModel({errors: {nodes: true, models: {}}, previous: good});
  assert.equal(held.source.label, 'LLMFIT OFFLINE');
  assert.equal(held.source.stale, true);
  assert.equal(held.source.lastUpdated, good.source.lastUpdated);
  assert.notEqual(held.source.lastUpdated, '-');
  assert.equal(held.nodeCards[0].statusLabel, 'STALE');
  assert.equal(held.fitRows[0].held, true);
  const summaryHTML = renderLlmOpsSummaryHTML(held);
  assert.match(summaryHTML, /source-offline">LLMFIT OFFLINE</);
  assert.match(summaryHTML, /source-stale">STALE</);
  assert.match(summaryHTML, /Last Updated /);
  assert.match(renderLlmOpsNodeCardsHTML(held.nodeCards), /state-stale">STALE</);
  assert.match(renderLlmOpsFitRowsHTML(held.fitRows), /llm-ops-fit-row fit-good is-held/);

  const empty = buildLlmOpsViewModel({errors: {nodes: true}});
  assert.equal(empty.source.label, 'LLMFIT OFFLINE');
  assert.equal(empty.nodeCards.length, 0);
  assert.equal(empty.source.lastUpdated, '-');

  const allOffline = buildLlmOpsViewModel(sampleInput({nodes: {nodes: [Object.assign({}, sampleNode, {status: 'offline'})]}}));
  assert.equal(allOffline.source.label, 'LLMFIT OFFLINE');
  const allStale = buildLlmOpsViewModel(sampleInput({nodes: {nodes: [Object.assign({}, sampleNode, {status: 'stale'})]}}));
  assert.equal(allStale.source.label, 'LLMFIT STALE');
  const noNodes = buildLlmOpsViewModel(sampleInput({nodes: {nodes: []}, nodeModels: {}}));
  assert.equal(noNodes.source.label, 'NO NODES');
});

test('LLM Ops escapes external strings before rendering', () => {
  const api = llmOps();
  const evil = '<script>alert(1)</script>';
  const input = sampleInput({
    nodes: {nodes: [Object.assign({}, sampleNode, {node_name: evil, backend: '"><img src=x onerror=alert(1)>', error: evil})]},
    nodeModels: {'node-a': {models: [sampleModel({model_id: evil, best_quant: evil, runtime: evil, notes: [evil], estimate_confidence: evil, fit_level: evil})]}},
  });
  const model = api.buildLlmOpsViewModel(input);
  const matrixRows = api.buildLlmOpsMatrixRows({nodes: [{node_id: evil, status: evil, fit_level: evil, best_quant: null, runtime: evil, estimate_confidence: evil}]});
  const html = [
    api.renderLlmOpsSummaryHTML(model),
    api.renderLlmOpsNodeCardsHTML(model.nodeCards),
    api.renderLlmOpsFitRowsHTML(model.fitRows),
    api.renderLlmOpsDetailHTML(model.fitRows[0], {matrixState: 'ready', matrixRows}),
  ].join('');
  assert.doesNotMatch(html, /<script>/i);
  assert.doesNotMatch(html, /<img /i);
  assert.match(html, /&lt;script&gt;alert\(1\)&lt;\/script&gt;/);
  assert.match(html, /&quot;&gt;&lt;img src=x onerror=alert\(1\)&gt;/);
});

test('LLM Ops manual refresh prevents double submission and re-fetches afterwards', async () => {
  const {requestLlmOpsRefresh, summarizeLlmOpsRefreshResult} = llmOps();
  assert.match(
    summarizeLlmOpsRefreshResult({generated_at: '2026-09-15T01:03:00Z', refreshed: ['node-a'], failed: {'node-b': 'timeout'}}),
    /Refreshed 1 node\(s\) · failed 1: node-b \(timeout\) · at /,
  );

  const fakeElement = () => ({
    innerHTML: '', textContent: '', hidden: false, disabled: false, attrs: {},
    setAttribute(key, value) { this.attrs[key] = String(value); },
    getAttribute(key) { return this.attrs[key]; },
    addEventListener() {},
    querySelector() { return null; },
  });
  const ids = ['panel-llm-ops', 'llmOpsSummary', 'llmOpsNodeCards', 'llmOpsTopFitBody', 'llmOpsFitBody', 'llmOpsFitCount', 'llmOpsDetail', 'llmOpsRefreshBtn', 'llmOpsRefreshNote'];
  const elements = Object.fromEntries(ids.map((id) => [id, fakeElement()]));
  const calls = [];
  let releasePost = () => {};
  const postGate = new Promise((resolve) => { releasePost = resolve; });
  const previousDocument = globalThis.document;
  const previousFetch = globalThis.fetch;
  globalThis.document = {getElementById: (id) => elements[id] || null};
  globalThis.fetch = async (url, init) => {
    const method = (init && init.method) || 'GET';
    calls.push(method + ' ' + url);
    if (method === 'POST') await postGate;
    return {ok: true, json: async () => (url.includes('/llm-ops/nodes') ? {nodes: []} : {refreshed: ['node-a'], failed: {}})};
  };
  try {
    const first = requestLlmOpsRefresh();
    const second = await requestLlmOpsRefresh();
    assert.equal(second, null, 'second press while a refresh is in flight must be ignored');
    assert.equal(elements.llmOpsRefreshBtn.disabled, true);
    assert.equal(elements.llmOpsRefreshBtn.getAttribute('aria-busy'), 'true');
    releasePost();
    const result = await first;
    assert.deepEqual(result.refreshed, ['node-a']);
    assert.equal(calls.filter((call) => call.startsWith('POST ')).length, 1);
    assert.equal(calls[0], 'POST /viewer/llm-ops/refresh');
    assert.ok(calls.includes('GET /viewer/llm-ops/nodes'), 'nodes are re-fetched after the refresh request');
    assert.equal(elements.llmOpsRefreshBtn.disabled, false);
    assert.equal(elements.llmOpsRefreshBtn.getAttribute('aria-busy'), 'false');
    assert.match(elements.llmOpsRefreshNote.textContent, /Refreshed 1 node\(s\)/);
    assert.match(elements.llmOpsSummary.innerHTML, /NO NODES/);
    assert.equal(elements.llmOpsSummary.getAttribute('aria-busy'), 'false');
  } finally {
    globalThis.document = previousDocument;
    globalThis.fetch = previousFetch;
  }
});

test('LLM Ops API fetch has a finite timeout', async () => {
  const {fetchLlmOpsJSON} = llmOps();
  const fetchImpl = (_path, options) => new Promise((_resolve, reject) => {
    options.signal.addEventListener('abort', () => reject(new Error('aborted')), {once: true});
  });
  await assert.rejects(fetchLlmOpsJSON('/viewer/slow', {fetchImpl, timeoutMS: 10}), /aborted/);
});

test('LLM Ops tab is wired into viewer.html and viewer.js', () => {
  const html = read('viewer.html');
  const viewer = read('assets/js/viewer.js');
  assert.match(html, /<button class="tab-btn" data-tab="llm-ops">LLM Ops<\/button>/);
  assert.match(html, /<option value="llm-ops">LLM Ops<\/option>/);
  assert.match(html, /<section id="panel-llm-ops" class="panel llm-ops-panel">/);
  assert.match(html, /<link rel="stylesheet" href="\/viewer\/assets\/css\/tabs\/llm-ops\.css\?v=[^"]+">/);
  assert.match(html, /<script src="\/viewer\/assets\/js\/tabs\/llm-ops\.js\?v=[^"]+" defer><\/script>/);
  assert.ok(html.indexOf('tabs/llm-ops.js') < html.indexOf('/viewer/assets/js/viewer.js?v='), 'llm-ops.js must load before viewer.js');
  for (const id of ['llmOpsSummary', 'llmOpsNodeCards', 'llmOpsTopFitBody', 'llmOpsFitBody', 'llmOpsDetail', 'llmOpsRefreshBtn', 'llmOpsRefreshNote']) {
    assert.match(html, new RegExp('id="' + id + '"'));
  }
  assert.match(html, /Manual Refresh/);
  assert.match(viewer, /'llm-ops': document\.getElementById\('panel-llm-ops'\)/);
  assert.match(viewer, /if \(tab === 'llm-ops' && typeof refreshLlmOpsData === 'function'\) refreshLlmOpsData\(\);/);

  const panel = html.slice(html.indexOf('id="panel-llm-ops"'), html.indexOf('id="panel-overview"'));
  assert.match(panel, /<details class="llm-ops-details" id="llmOpsFitDetails">/);
  assert.doesNotMatch(panel, /<details[^>]*\sopen/);
  assert.ok(panel.indexOf('id="llmOpsFitBody"') > panel.indexOf('<details class="llm-ops-details"'), 'full fit table is behind details');
  assert.ok(panel.indexOf('id="llmOpsTopFitBody"') < panel.indexOf('<details class="llm-ops-details"'), 'top fits stay in the first view');
});

test('LLM Ops CSS collapses to one column at 640px and keeps text readable', () => {
  const css = read('assets/css/tabs/llm-ops.css');
  assert.match(css, /@media \(max-width: 640px\)\{[\s\S]*?\.llm-ops-summary\{grid-template-columns:minmax\(0,1fr\)\}/);
  assert.match(css, /@media \(max-width: 640px\)\{[\s\S]*?\.llm-ops-node-grid\{grid-template-columns:minmax\(0,1fr\)\}/);
  assert.match(css, /overflow-wrap:anywhere/);
  assert.match(css, /#panel-llm-ops \.debug-table\{display:block;[^}]*overflow-x:auto\}/);
  assert.doesNotMatch(css, /font-size:\s*[0-9](?:\.\d+)?px/, 'no font-size below 10px');
});

test('LLM Ops CSS keeps Model / Node columns and Best fit card wide enough to read', () => {
  const css = read('assets/css/tabs/llm-ops.css');
  // `#panel-llm-ops *{min-width:0}` outranks class selectors, so the column floors must be scoped under the panel id.
  const model = css.match(/#panel-llm-ops \.llm-ops-fit-table td\.llm-ops-fit-model\{[^}]*min-width:(\d+)px/);
  assert.ok(model, 'model cell min-width is scoped under #panel-llm-ops');
  assert.ok(Number(model[1]) >= 200, 'model column floor is wide enough to wrap a long model id at word boundaries');
  const node = css.match(/#panel-llm-ops \.llm-ops-fit-table td\.llm-ops-fit-node[^{]*\{[^}]*min-width:(\d+)px/);
  assert.ok(node, 'node cell min-width is scoped under #panel-llm-ops');
  assert.ok(Number(node[1]) >= 80);
  assert.match(css, /@media \(max-width: 1199px\)\{[\s\S]*?\.llm-ops-summary\{grid-template-columns:repeat\(3,minmax\(0,1fr\)\)\}/, 'summary drops to three columns before cards get too narrow for model ids');
  assert.ok(css.indexOf('@media (max-width: 1199px)') < css.indexOf('@media (max-width: 980px)'), 'narrower breakpoints must come later so they win the cascade');
});

test('LLM Ops node test is listed in the local test plan', () => {
  const plan = JSON.parse(fs.readFileSync(path.join(here, '..', '..', '..', 'scripts', 'test-local.plan.json'), 'utf8'));
  const step = plan.steps.find((item) => item.name === 'viewer-node');
  assert.ok(step, 'viewer-node step exists');
  assert.ok(step.arguments.includes('internal/adapter/viewer/viewer_llm_ops.test.mjs'));
});
