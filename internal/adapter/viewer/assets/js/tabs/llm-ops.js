/* LLM Ops tab module: llmfit hardware capability and model fit UI.
 * Layers: pure view-model builders -> pure HTML renderers -> DOM/IO refresh.
 * Pure functions are exported via module.exports for Node tests. */
(function(root) {
  'use strict';

  const NODE_STATUSES = new Set(['online', 'offline', 'stale', 'disabled']);
  const SUMMARY_STATUSES = new Set(['ok', 'warning', 'blocked', 'unavailable']);
  const CONFIDENCE_LABELS = {
    measured_local: 'LOCAL MEASURED',
    measured_community: 'COMMUNITY MEASURED',
    calibrated: 'CALIBRATED',
    estimated: 'ESTIMATED',
    unsupported: 'UNSUPPORTED',
  };
  const FIT_LABELS = {
    perfect: 'PERFECT',
    good: 'GOOD',
    marginal: 'MARGINAL',
    too_tight: 'TOO TIGHT',
    unsupported: 'UNSUPPORTED',
  };
  const MODELS_PER_NODE = 20;
  const TOP_FIT_ROWS = 5;
  const FETCH_TIMEOUT_MS = 5000;
  const REFRESH_TIMEOUT_MS = 30000;

  function arrayValue(value) {
    return Array.isArray(value) ? value : [];
  }

  function objectValue(value) {
    return value && typeof value === 'object' && !Array.isArray(value) ? value : {};
  }

  function textValue(value, fallback) {
    const text = String(value === undefined || value === null ? '' : value).trim();
    return text || fallback || '';
  }

  function numberOrNull(value) {
    if (value === undefined || value === null || value === '') return null;
    const number = Number(value);
    return Number.isFinite(number) ? number : null;
  }

  function normalizeLlmOpsNodeStatus(value) {
    const normalized = textValue(value).toLowerCase();
    return NODE_STATUSES.has(normalized) ? normalized : 'unknown';
  }

  function nodeStatusOf(value) {
    const key = normalizeLlmOpsNodeStatus(value);
    if (key !== 'unknown') return {key, label: key.toUpperCase()};
    const raw = textValue(value);
    return {key: 'unknown', label: raw ? raw.toUpperCase() : 'UNKNOWN'};
  }

  function llmOpsConfidence(value) {
    const raw = textValue(value);
    const key = raw.toLowerCase();
    if (Object.prototype.hasOwnProperty.call(CONFIDENCE_LABELS, key)) {
      return {key: key.replace(/_/g, '-'), label: CONFIDENCE_LABELS[key], known: true};
    }
    return {key: 'unknown', label: raw ? raw.toUpperCase() : 'UNKNOWN', known: false};
  }

  function llmOpsFit(value) {
    const raw = textValue(value);
    const key = raw.toLowerCase();
    if (Object.prototype.hasOwnProperty.call(FIT_LABELS, key)) {
      return {key: key.replace(/_/g, '-'), label: FIT_LABELS[key], known: true};
    }
    return {key: 'unknown', label: raw ? raw.replace(/_/g, ' ').toUpperCase() : 'UNKNOWN', known: false};
  }

  function formatLlmOpsTps(value) {
    const number = numberOrNull(value);
    if (number === null) return '-';
    return number.toFixed(1) + ' tok/s';
  }

  function formatLlmOpsGb(value) {
    const number = numberOrNull(value);
    if (number === null) return '-';
    return (number >= 100 ? number.toFixed(0) : number.toFixed(1)) + ' GB';
  }

  function formatLlmOpsCount(value) {
    const number = numberOrNull(value);
    if (number === null) return '-';
    return Math.round(number).toLocaleString('en-US');
  }

  function formatLlmOpsPct(value) {
    const number = numberOrNull(value);
    if (number === null) return '-';
    return number.toFixed(0) + '%';
  }

  function formatLlmOpsMs(value) {
    const number = numberOrNull(value);
    if (number === null) return '-';
    return Math.round(number).toLocaleString('en-US') + ' ms';
  }

  function formatLlmOpsScore(value) {
    const number = numberOrNull(value);
    if (number === null) return '-';
    return Number.isInteger(number) ? String(number) : number.toFixed(1);
  }

  function formatLlmOpsTime(value) {
    const text = textValue(value);
    if (!text) return '-';
    const date = new Date(text);
    if (Number.isNaN(date.getTime())) return '-';
    try {
      return date.toLocaleString('ja-JP', {hour12: false, timeZone: 'Asia/Tokyo'});
    } catch (_) {
      return date.toISOString();
    }
  }

  function quantLabel(value) {
    return textValue(value, '-');
  }

  function latestTimestamp(values) {
    let best = '';
    let bestTime = -Infinity;
    for (const value of values) {
      const text = textValue(value);
      if (!text) continue;
      const time = new Date(text).getTime();
      if (Number.isNaN(time) || time <= bestTime) continue;
      best = text;
      bestTime = time;
    }
    return best;
  }

  // llmfit groups identical cards into one gpus[] element carrying a count;
  // a missing or invalid count means one device.
  function gpuDeviceCount(gpu) {
    const count = numberOrNull(objectValue(gpu).count);
    return count !== null && count >= 1 ? Math.round(count) : 1;
  }

  // Sums values only when every value is known. One unknown (null) member
  // makes the total unknown: a partial sum must not pose as a node total, and
  // an empty list has no total.
  function sumIfAllKnown(values) {
    if (!values.length) return null;
    let total = 0;
    for (const value of values) {
      const number = numberOrNull(value);
      if (number === null) return null;
      total += number;
    }
    return total;
  }

  function buildLlmOpsNodeCard(node, options) {
    const source = objectValue(node);
    const settings = objectValue(options);
    const nodeId = textValue(source.node_id, '-');
    const gpus = arrayValue(source.gpus).map(objectValue);
    const gpuCount = numberOrNull(source.gpu_count);
    const hasGpu = source.has_gpu === true || gpus.length > 0 || (gpuCount !== null && gpuCount > 0);
    const gpuNameCounts = new Map();
    gpus.forEach((gpu) => {
      const name = textValue(gpu.name);
      if (name) gpuNameCounts.set(name, (gpuNameCounts.get(name) || 0) + gpuDeviceCount(gpu));
    });
    let gpuText = 'none';
    if (hasGpu) {
      if (gpuNameCounts.size) {
        gpuText = Array.from(gpuNameCounts, ([name, count]) => (count > 1 ? count + 'x ' : '') + name).join(', ');
      } else {
        gpuText = String(gpuCount || gpus.length || 1) + ' GPU';
      }
    }
    // vram_gb is per device, so a grouped entry contributes vram_gb * count.
    const vramTotal = sumIfAllKnown(gpus.map((gpu) => {
      const perDevice = numberOrNull(gpu.vram_gb);
      return perDevice === null ? null : perDevice * gpuDeviceCount(gpu);
    }));
    // available_vram_gb is null when llmfit could not read free memory; the
    // card says so instead of showing a number (never "0.0 GB free").
    const vramAvailable = sumIfAllKnown(gpus.map((gpu) => gpu.available_vram_gb));
    let vramText = formatLlmOpsGb(vramTotal);
    if (vramTotal !== null) vramText += vramAvailable !== null ? ' (' + formatLlmOpsGb(vramAvailable) + ' free)' : ' (free unknown)';
    if (source.unified_memory === true) vramText += ' unified';
    const ramTotal = numberOrNull(source.total_ram_gb);
    const ramAvailable = numberOrNull(source.available_ram_gb);
    let ramText = formatLlmOpsGb(ramTotal);
    if (ramAvailable !== null) ramText += ' (' + formatLlmOpsGb(ramAvailable) + ' free)';
    const cpuCores = numberOrNull(source.cpu_cores);
    const cpuName = textValue(source.cpu_name, '-');
    const cpuText = cpuCores !== null && cpuCores > 0 ? cpuName + ' (' + String(Math.round(cpuCores)) + ' cores)' : cpuName;
    const status = settings.held ? {key: 'stale', label: 'STALE'} : nodeStatusOf(source.status);
    return {
      nodeId,
      name: textValue(source.node_name, nodeId),
      statusKey: status.key,
      statusLabel: status.label,
      held: settings.held === true,
      os: textValue(source.os, '-'),
      cpu: cpuText,
      gpu: gpuText,
      hasGpu,
      vram: vramText,
      ram: ramText,
      backend: textValue(source.backend, '-'),
      collectedAt: textValue(source.collected_at),
      lastUpdated: formatLlmOpsTime(source.collected_at),
      source: textValue(source.source, 'llmfit'),
      error: textValue(source.error),
    };
  }

  function buildLlmOpsFitRow(model, node, options) {
    const source = objectValue(model);
    const host = objectValue(node);
    const settings = objectValue(options);
    const context = objectValue(source.context);
    const memory = objectValue(source.memory);
    const performance = objectValue(source.performance);
    const scores = objectValue(source.scores);
    const fit = llmOpsFit(source.fit_level);
    const confidence = llmOpsConfidence(source.estimate_confidence);
    const status = settings.held ? {key: 'stale', label: 'STALE'} : nodeStatusOf(host.status);
    const nodeId = textValue(host.node_id, '-');
    const modelId = textValue(source.model_id, '-');
    return {
      key: nodeId + '|' + modelId,
      modelId,
      provider: textValue(source.provider),
      parameterCount: textValue(source.parameter_count),
      paramsB: numberOrNull(source.params_b),
      isMoe: source.is_moe === true,
      nodeId,
      nodeName: textValue(host.node_name, nodeId),
      nodeStatusKey: status.key,
      nodeStatusLabel: status.label,
      fitKey: fit.key,
      fitLabel: fit.label,
      score: numberOrNull(source.score),
      scores: {
        quality: numberOrNull(scores.quality),
        speed: numberOrNull(scores.speed),
        fit: numberOrNull(scores.fit),
        context: numberOrNull(scores.context),
      },
      runtime: textValue(source.runtime, '-'),
      runMode: textValue(source.run_mode, '-'),
      quant: quantLabel(source.best_quant),
      context: {
        native: numberOrNull(context.native),
        usable: numberOrNull(context.usable),
        evaluated: numberOrNull(context.evaluated),
      },
      memory: {
        requiredGb: numberOrNull(memory.required_gb),
        availableGb: numberOrNull(memory.available_gb),
        utilizationPct: numberOrNull(memory.utilization_pct),
      },
      estimatedTps: numberOrNull(performance.estimated_tps),
      measuredTps: numberOrNull(performance.measured_tps),
      prefillTps: numberOrNull(performance.prefill_tps),
      ttftMs: numberOrNull(performance.ttft_ms),
      confidenceKey: confidence.key,
      confidenceLabel: confidence.label,
      confidenceKnown: confidence.known,
      installed: source.installed === true,
      diskSizeGb: numberOrNull(source.disk_size_gb),
      capabilities: arrayValue(source.capabilities).map((item) => textValue(item)).filter(Boolean),
      license: textValue(source.license),
      notes: arrayValue(source.notes).map((item) => textValue(item)).filter(Boolean),
      held: settings.held === true,
    };
  }

  function compareFitRows(a, b) {
    const scoreA = a.score === null ? -Infinity : a.score;
    const scoreB = b.score === null ? -Infinity : b.score;
    if (scoreA !== scoreB) return scoreB - scoreA;
    if (a.modelId !== b.modelId) return a.modelId < b.modelId ? -1 : 1;
    if (a.nodeId !== b.nodeId) return a.nodeId < b.nodeId ? -1 : 1;
    return 0;
  }

  function summaryBlock(key, label, value, detail, status) {
    return {key, label, value: String(value), detail: String(detail), status: SUMMARY_STATUSES.has(status) ? status : 'warning'};
  }

  function countBy(items, pick) {
    const counts = {};
    for (const item of items) {
      const key = pick(item);
      counts[key] = (counts[key] || 0) + 1;
    }
    return counts;
  }

  function buildLlmOpsSummary(source, nodeCards, fitRows, modelErrorCount) {
    const sourceStatus = {online: 'ok', stale: 'warning', offline: 'blocked', empty: 'unavailable'}[source.state] || 'warning';
    const nodeCounts = countBy(nodeCards, (card) => card.statusKey);
    const online = nodeCounts.online || 0;
    const offline = nodeCounts.offline || 0;
    const stale = nodeCounts.stale || 0;
    const disabled = nodeCounts.disabled || 0;
    let nodeStatus = 'unavailable';
    if (online > 0) nodeStatus = 'ok';
    else if (stale > 0) nodeStatus = 'warning';
    else if (nodeCards.length > 0) nodeStatus = 'blocked';

    const fitCounts = countBy(fitRows, (row) => row.fitKey);
    const perfect = fitCounts.perfect || 0;
    const good = fitCounts.good || 0;
    const marginal = fitCounts.marginal || 0;
    const tooTight = fitCounts['too-tight'] || 0;
    let fitStatus = 'unavailable';
    if (perfect + good > 0) fitStatus = 'ok';
    else if (marginal > 0) fitStatus = 'warning';
    else if (fitRows.length > 0) fitStatus = 'blocked';
    const fitDetailParts = [perfect + ' perfect', good + ' good', marginal + ' marginal', tooTight + ' too tight'];
    if (modelErrorCount > 0) fitDetailParts.push(modelErrorCount + ' node(s) unavailable');

    const measured = fitRows.filter((row) => row.measuredTps !== null).length;
    const estimatedOnly = fitRows.filter((row) => row.measuredTps === null && row.estimatedTps !== null).length;
    const noTps = fitRows.length - measured - estimatedOnly;
    let tpsStatus = 'unavailable';
    if (measured > 0) tpsStatus = 'ok';
    else if (fitRows.length > 0) tpsStatus = 'warning';

    const best = fitRows.length ? fitRows[0] : null;
    let bestStatus = 'unavailable';
    if (best) {
      if (best.fitKey === 'perfect' || best.fitKey === 'good') bestStatus = 'ok';
      else if (best.fitKey === 'marginal') bestStatus = 'warning';
      else bestStatus = 'blocked';
    }
    const bestDetail = best
      ? [best.nodeName, best.fitLabel, 'est ' + formatLlmOpsTps(best.estimatedTps), 'meas ' + formatLlmOpsTps(best.measuredTps), best.confidenceLabel].join(' · ')
      : 'No model fit rows.';

    return [
      summaryBlock('source', 'llmfit', source.label, 'Last Updated ' + source.lastUpdated + (source.note ? ' · ' + source.note : ''), sourceStatus),
      summaryBlock('nodes', 'Nodes', online + ' online', offline + ' offline · ' + stale + ' stale · ' + disabled + ' disabled', nodeStatus),
      summaryBlock('fits', 'Model fits', fitRows.length + ' rows', fitDetailParts.join(' · '), fitStatus),
      summaryBlock('tps', 'Measured / Estimated', measured + ' measured / ' + estimatedOnly + ' estimated only', noTps + ' without TPS · Estimated and Measured are never merged', tpsStatus),
      summaryBlock('best', 'Best fit', best ? best.modelId : '-', bestDetail, bestStatus),
    ];
  }

  function buildLlmOpsViewModel(input) {
    const data = objectValue(input);
    const errors = objectValue(data.errors);
    const modelErrors = objectValue(errors.models);
    const previous = data.previous && typeof data.previous === 'object' ? data.previous : null;
    const nodesResponse = objectValue(data.nodes);
    const nodeModels = objectValue(data.nodeModels);
    let nodeCards = [];
    let fitRows = [];
    let source;

    if (errors.nodes) {
      const previousCards = previous ? arrayValue(previous.nodeCards) : [];
      const previousRows = previous ? arrayValue(previous.fitRows) : [];
      const previousSource = previous ? objectValue(previous.source) : {};
      if (previousCards.length || previousRows.length) {
        nodeCards = previousCards.map((card) => Object.assign({}, card, {statusKey: 'stale', statusLabel: 'STALE', held: true}));
        fitRows = previousRows.map((row) => Object.assign({}, row, {nodeStatusKey: 'stale', nodeStatusLabel: 'STALE', held: true}));
        source = {
          state: 'offline', label: 'LLMFIT OFFLINE', stale: true,
          lastUpdatedRaw: textValue(previousSource.lastUpdatedRaw),
          lastUpdated: textValue(previousSource.lastUpdated, '-'),
          note: 'llmfit unreachable. Showing held values.',
        };
      } else {
        source = {state: 'offline', label: 'LLMFIT OFFLINE', stale: false, lastUpdatedRaw: '', lastUpdated: '-', note: 'llmfit unreachable. No held values.'};
      }
    } else {
      const nodeList = arrayValue(nodesResponse.nodes).map(objectValue);
      nodeCards = nodeList.map((node) => buildLlmOpsNodeCard(node));
      for (const node of nodeList) {
        const nodeId = textValue(node.node_id);
        const response = objectValue(nodeModels[nodeId]);
        for (const model of arrayValue(response.models)) {
          fitRows.push(buildLlmOpsFitRow(model, node));
        }
      }
      fitRows.sort(compareFitRows);
      const lastUpdatedRaw = latestTimestamp(nodeList.map((node) => node.collected_at).concat([nodesResponse.generated_at]));
      const counts = countBy(nodeCards, (card) => card.statusKey);
      let state = 'online';
      let label = 'LLMFIT ONLINE';
      if (!nodeCards.length) {
        state = 'empty';
        label = 'NO NODES';
      } else if (!counts.online) {
        if (counts.stale) {
          state = 'stale';
          label = 'LLMFIT STALE';
        } else {
          state = 'offline';
          label = 'LLMFIT OFFLINE';
        }
      }
      source = {state, label, stale: state === 'stale', lastUpdatedRaw, lastUpdated: formatLlmOpsTime(lastUpdatedRaw), note: ''};
    }

    const modelErrorCount = Object.keys(modelErrors).length;
    return {
      source,
      summary: buildLlmOpsSummary(source, nodeCards, fitRows, modelErrorCount),
      nodeCards,
      fitRows,
      errors: {nodes: Boolean(errors.nodes), models: Object.keys(modelErrors)},
    };
  }

  function buildLlmOpsMatrixRows(matrixResponse) {
    const response = objectValue(matrixResponse);
    return arrayValue(response.nodes).map(objectValue).map((node) => {
      const status = nodeStatusOf(node.status);
      const fit = llmOpsFit(node.fit_level);
      const confidence = llmOpsConfidence(node.estimate_confidence);
      return {
        nodeId: textValue(node.node_id, '-'),
        statusKey: status.key,
        statusLabel: status.label,
        fitKey: fit.key,
        fitLabel: fit.label,
        score: numberOrNull(node.score),
        quant: quantLabel(node.best_quant),
        runtime: textValue(node.runtime, '-'),
        usableContext: numberOrNull(node.usable_context),
        estimatedTps: numberOrNull(node.estimated_tps),
        measuredTps: numberOrNull(node.measured_tps),
        confidenceKey: confidence.key,
        confidenceLabel: confidence.label,
      };
    });
  }

  function escapeLlmOpsHTML(value) {
    return String(value === undefined || value === null ? '' : value)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }

  function badgeHTML(kind, key, label) {
    return '<span class="llm-ops-badge ' + escapeLlmOpsHTML(kind + '-' + key) + '">' + escapeLlmOpsHTML(label) + '</span>';
  }

  function metricHTML(className, label, value) {
    return '<div class="' + className + '"><dt>' + escapeLlmOpsHTML(label) + '</dt><dd>' + escapeLlmOpsHTML(value) + '</dd></div>';
  }

  function renderLlmOpsSummaryHTML(model) {
    const view = objectValue(model);
    const source = objectValue(view.source);
    return arrayValue(view.summary).map((block) => {
      const status = SUMMARY_STATUSES.has(block.status) ? block.status : 'warning';
      let valueHTML = escapeLlmOpsHTML(block.value);
      if (block.key === 'source') {
        valueHTML = badgeHTML('source', textValue(source.state, 'empty'), textValue(source.label, 'UNKNOWN'));
        if (source.stale) valueHTML += badgeHTML('source', 'stale', 'STALE');
      }
      return '<article class="llm-ops-summary-block status-' + status + '" data-llm-ops-summary="' + escapeLlmOpsHTML(block.key) + '">' +
        '<div class="llm-ops-summary-label">' + escapeLlmOpsHTML(block.label) + '</div>' +
        '<div class="llm-ops-summary-value">' + valueHTML + '</div>' +
        '<div class="llm-ops-summary-detail">' + escapeLlmOpsHTML(block.detail) + '</div>' +
        '</article>';
    }).join('');
  }

  function renderLlmOpsNodeCardsHTML(cards, options) {
    const list = arrayValue(cards);
    const settings = objectValue(options);
    if (!list.length) return '<p class="llm-ops-empty">' + escapeLlmOpsHTML(settings.emptyText || 'No nodes reported.') + '</p>';
    return list.map((card) => {
      const heldClass = card.held ? ' is-held' : '';
      return '<article class="llm-ops-node-card state-' + escapeLlmOpsHTML(card.statusKey) + heldClass + '" data-llm-ops-node="' + escapeLlmOpsHTML(card.nodeId) + '">' +
        '<header class="llm-ops-node-head"><h3 class="llm-ops-node-name">' + escapeLlmOpsHTML(card.name) + '</h3>' + badgeHTML('state', card.statusKey, card.statusLabel) + '</header>' +
        '<dl class="llm-ops-node-metrics">' +
        metricHTML('llm-ops-node-metric', 'GPU', card.gpu) +
        metricHTML('llm-ops-node-metric', 'VRAM', card.vram) +
        metricHTML('llm-ops-node-metric', 'RAM', card.ram) +
        metricHTML('llm-ops-node-metric', 'Backend', card.backend) +
        metricHTML('llm-ops-node-metric', 'CPU', card.cpu) +
        metricHTML('llm-ops-node-metric', 'Last Updated', card.lastUpdated) +
        '</dl>' +
        (card.error ? '<p class="llm-ops-node-error">' + escapeLlmOpsHTML(card.error) + '</p>' : '') +
        '</article>';
    }).join('');
  }

  function tpsCellHTML(kind, value) {
    return '<td class="llm-ops-num"><span class="llm-ops-tps-kind">' + kind + '</span> ' + escapeLlmOpsHTML(formatLlmOpsTps(value)) + '</td>';
  }

  function renderLlmOpsFitRowsHTML(rows, options) {
    const list = arrayValue(rows);
    const settings = objectValue(options);
    if (!list.length) return '<tr><td colspan="9" class="llm-ops-empty-cell">' + escapeLlmOpsHTML(settings.emptyText || 'No model fit rows.') + '</td></tr>';
    return list.map((row) => {
      const heldClass = row.held ? ' is-held' : '';
      return '<tr class="llm-ops-fit-row fit-' + escapeLlmOpsHTML(row.fitKey) + heldClass + '" data-llm-ops-row="' + escapeLlmOpsHTML(row.key) + '">' +
        '<td class="llm-ops-fit-model"><button type="button" class="llm-ops-detail-btn" data-llm-ops-detail="' + escapeLlmOpsHTML(row.key) + '">' + escapeLlmOpsHTML(row.modelId) + '</button></td>' +
        '<td class="llm-ops-fit-node">' + escapeLlmOpsHTML(row.nodeName) + (row.held ? ' ' + badgeHTML('state', 'stale', 'STALE') : '') + '</td>' +
        '<td>' + badgeHTML('fit', row.fitKey, row.fitLabel) + '</td>' +
        '<td>' + escapeLlmOpsHTML(row.quant) + '</td>' +
        '<td>' + escapeLlmOpsHTML(row.runtime) + '</td>' +
        '<td class="llm-ops-num">' + escapeLlmOpsHTML(formatLlmOpsCount(objectValue(row.context).usable)) + '</td>' +
        tpsCellHTML('est', row.estimatedTps) +
        tpsCellHTML('meas', row.measuredTps) +
        '<td>' + badgeHTML('conf', row.confidenceKey, row.confidenceLabel) + '</td>' +
        '</tr>';
    }).join('');
  }

  function detailGroupHTML(title, items) {
    return '<section class="llm-ops-detail-group"><h4>' + escapeLlmOpsHTML(title) + '</h4><dl>' +
      items.map(([label, value]) => metricHTML('llm-ops-detail-item', label, value)).join('') +
      '</dl></section>';
  }

  function renderLlmOpsMatrixHTML(rows, state) {
    if (state === 'loading') return '<p class="llm-ops-empty">Loading fit across nodes.</p>';
    if (state === 'error') return '<p class="llm-ops-empty">Fit across nodes unavailable.</p>';
    const list = arrayValue(rows);
    if (!list.length) return '<p class="llm-ops-empty">No nodes reported for this model.</p>';
    return '<table class="debug-table llm-ops-matrix-table">' +
      '<thead><tr><th>Node</th><th>Status</th><th>Fit</th><th>Quant</th><th>Runtime</th><th>Usable Context</th><th>Estimated TPS</th><th>Measured TPS</th><th>Confidence</th></tr></thead><tbody>' +
      list.map((row) => '<tr>' +
        '<td class="llm-ops-fit-node">' + escapeLlmOpsHTML(row.nodeId) + '</td>' +
        '<td>' + badgeHTML('state', row.statusKey, row.statusLabel) + '</td>' +
        '<td>' + badgeHTML('fit', row.fitKey, row.fitLabel) + '</td>' +
        '<td>' + escapeLlmOpsHTML(row.quant) + '</td>' +
        '<td>' + escapeLlmOpsHTML(row.runtime) + '</td>' +
        '<td class="llm-ops-num">' + escapeLlmOpsHTML(formatLlmOpsCount(row.usableContext)) + '</td>' +
        tpsCellHTML('est', row.estimatedTps) +
        tpsCellHTML('meas', row.measuredTps) +
        '<td>' + badgeHTML('conf', row.confidenceKey, row.confidenceLabel) + '</td>' +
        '</tr>').join('') +
      '</tbody></table>';
  }

  function renderLlmOpsDetailHTML(row, options) {
    if (!row || typeof row !== 'object') return '';
    const settings = objectValue(options);
    const scores = objectValue(row.scores);
    const context = objectValue(row.context);
    const memory = objectValue(row.memory);
    let params = textValue(row.parameterCount);
    if (!params && row.paramsB !== null && row.paramsB !== undefined) params = String(row.paramsB) + 'B';
    if (!params) params = '-';
    if (row.isMoe) params += ' (MoE)';
    const badges = badgeHTML('fit', row.fitKey, row.fitLabel) + badgeHTML('conf', row.confidenceKey, row.confidenceLabel) + (row.held ? badgeHTML('state', 'stale', 'STALE') : '');
    const notes = arrayValue(row.notes);
    const capabilities = arrayValue(row.capabilities);
    let extraHTML = '';
    if (capabilities.length) extraHTML += '<p class="llm-ops-detail-notes">Capabilities: ' + escapeLlmOpsHTML(capabilities.join(', ')) + '</p>';
    if (notes.length) extraHTML += '<div class="llm-ops-detail-notes">Notes<ul>' + notes.map((note) => '<li>' + escapeLlmOpsHTML(note) + '</li>').join('') + '</ul></div>';
    return '<div class="llm-ops-detail-head">' +
      '<div class="llm-ops-detail-title"><h3>' + escapeLlmOpsHTML(row.modelId) + '</h3><span class="llm-ops-detail-node">' + escapeLlmOpsHTML(row.nodeName) + '</span>' + badges + '</div>' +
      '<button type="button" class="llm-ops-detail-close" data-llm-ops-detail-close="true">Close</button>' +
      '</div>' +
      '<div class="llm-ops-detail-grid">' +
      detailGroupHTML('Scores', [
        ['Quality', formatLlmOpsScore(scores.quality)],
        ['Speed', formatLlmOpsScore(scores.speed)],
        ['Fit', formatLlmOpsScore(scores.fit)],
        ['Context', formatLlmOpsScore(scores.context)],
        ['Overall', formatLlmOpsScore(row.score)],
      ]) +
      detailGroupHTML('Runtime', [
        ['Runtime', textValue(row.runtime, '-')],
        ['Run Mode', textValue(row.runMode, '-')],
        ['Quant', textValue(row.quant, '-')],
        ['Provider', textValue(row.provider, '-')],
        ['Params', params],
        ['Installed', row.installed ? 'yes' : 'no'],
        ['Disk', formatLlmOpsGb(row.diskSizeGb)],
        ['License', textValue(row.license, '-')],
      ]) +
      detailGroupHTML('Context', [
        ['Native', formatLlmOpsCount(context.native)],
        ['Usable', formatLlmOpsCount(context.usable)],
        ['Evaluated', formatLlmOpsCount(context.evaluated)],
      ]) +
      detailGroupHTML('Memory', [
        ['Required', formatLlmOpsGb(memory.requiredGb)],
        ['Available', formatLlmOpsGb(memory.availableGb)],
        ['Utilization', formatLlmOpsPct(memory.utilizationPct)],
      ]) +
      detailGroupHTML('Performance', [
        ['Estimated TPS', formatLlmOpsTps(row.estimatedTps)],
        ['Measured TPS', formatLlmOpsTps(row.measuredTps)],
        ['Prefill TPS', formatLlmOpsTps(row.prefillTps)],
        ['TTFT', formatLlmOpsMs(row.ttftMs)],
      ]) +
      detailGroupHTML('Estimate Basis', [
        ['Confidence', textValue(row.confidenceLabel, 'UNKNOWN')],
        ['Source', 'llmfit'],
      ]) +
      '</div>' +
      extraHTML +
      '<div class="llm-ops-matrix"><div class="llm-ops-matrix-title">Fit across nodes</div>' +
      '<div data-llm-ops-matrix="true">' + renderLlmOpsMatrixHTML(settings.matrixRows, settings.matrixState || 'loading') + '</div></div>';
  }

  function summarizeLlmOpsRefreshResult(result) {
    const response = objectValue(result);
    const refreshed = arrayValue(response.refreshed).map((item) => textValue(item)).filter(Boolean);
    const failed = objectValue(response.failed);
    const failedIds = Object.keys(failed);
    const parts = ['Refreshed ' + refreshed.length + ' node(s)'];
    if (failedIds.length) {
      parts.push('failed ' + failedIds.length + ': ' + failedIds.map((id) => id + ' (' + textValue(failed[id], 'unknown') + ')').join(', '));
    }
    parts.push('at ' + formatLlmOpsTime(response.generated_at));
    return parts.join(' · ');
  }

  async function fetchLlmOpsJSON(path, options) {
    const settings = objectValue(options);
    const fetchImpl = typeof settings.fetchImpl === 'function' ? settings.fetchImpl : fetch;
    const configuredTimeout = Number(settings.timeoutMS);
    const timeoutMS = Number.isFinite(configuredTimeout) && configuredTimeout > 0 ? configuredTimeout : FETCH_TIMEOUT_MS;
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), timeoutMS);
    try {
      const init = {method: settings.method || 'GET', headers: {'Accept': 'application/json'}, signal: controller.signal};
      const response = await fetchImpl(path, init);
      if (!response.ok) throw new Error('request unavailable (' + response.status + ')');
      return response.json();
    } finally {
      clearTimeout(timeout);
    }
  }

  const state = {
    refreshSequence: 0,
    matrixSequence: 0,
    lastModel: null,
    rowsByKey: new Map(),
    detailKey: '',
    manualRefreshInFlight: false,
    bound: false,
  };

  function llmOpsElements() {
    if (typeof document === 'undefined') return null;
    const panel = document.getElementById('panel-llm-ops');
    if (!panel) return null;
    return {
      panel,
      summary: document.getElementById('llmOpsSummary'),
      nodes: document.getElementById('llmOpsNodeCards'),
      topBody: document.getElementById('llmOpsTopFitBody'),
      fitBody: document.getElementById('llmOpsFitBody'),
      fitCount: document.getElementById('llmOpsFitCount'),
      detail: document.getElementById('llmOpsDetail'),
      refreshBtn: document.getElementById('llmOpsRefreshBtn'),
      refreshNote: document.getElementById('llmOpsRefreshNote'),
    };
  }

  function setBusy(element, busy) {
    if (element && typeof element.setAttribute === 'function') element.setAttribute('aria-busy', busy ? 'true' : 'false');
  }

  function hideLlmOpsDetail() {
    state.detailKey = '';
    const els = llmOpsElements();
    if (!els || !els.detail) return;
    els.detail.hidden = true;
    els.detail.innerHTML = '';
  }

  async function loadLlmOpsMatrix(row) {
    const sequence = ++state.matrixSequence;
    let rows = [];
    let matrixState = 'ready';
    try {
      const response = await fetchLlmOpsJSON('/viewer/llm-ops/model/matrix?model_id=' + encodeURIComponent(row.modelId));
      rows = buildLlmOpsMatrixRows(response);
    } catch (_) {
      matrixState = 'error';
    }
    if (sequence !== state.matrixSequence || state.detailKey !== row.key) return;
    const els = llmOpsElements();
    const host = els && els.detail && typeof els.detail.querySelector === 'function' ? els.detail.querySelector('[data-llm-ops-matrix]') : null;
    if (host) host.innerHTML = renderLlmOpsMatrixHTML(rows, matrixState);
  }

  function showLlmOpsDetail(key) {
    const els = llmOpsElements();
    if (!els || !els.detail) return;
    const row = state.rowsByKey.get(key);
    if (!row) {
      hideLlmOpsDetail();
      return;
    }
    state.detailKey = key;
    els.detail.hidden = false;
    els.detail.innerHTML = renderLlmOpsDetailHTML(row, {matrixState: 'loading'});
    loadLlmOpsMatrix(row);
  }

  function bindLlmOpsEvents(els) {
    if (state.bound) return;
    state.bound = true;
    if (els.refreshBtn && typeof els.refreshBtn.addEventListener === 'function') {
      els.refreshBtn.addEventListener('click', () => { requestLlmOpsRefresh(); });
    }
    if (typeof els.panel.addEventListener === 'function') {
      els.panel.addEventListener('click', (event) => {
        const target = event && event.target && typeof event.target.closest === 'function' ? event.target : null;
        if (!target) return;
        const detailButton = target.closest('[data-llm-ops-detail]');
        if (detailButton) {
          showLlmOpsDetail(detailButton.getAttribute('data-llm-ops-detail') || '');
          return;
        }
        if (target.closest('[data-llm-ops-detail-close]')) hideLlmOpsDetail();
      });
    }
  }

  function renderLlmOpsModel(model, els) {
    const rows = arrayValue(model.fitRows);
    if (els.summary) els.summary.innerHTML = renderLlmOpsSummaryHTML(model);
    if (els.nodes) {
      const emptyText = model.errors && model.errors.nodes ? 'llmfit unreachable. No node data held.' : 'No nodes reported by llmfit.';
      els.nodes.innerHTML = renderLlmOpsNodeCardsHTML(model.nodeCards, {emptyText});
    }
    if (els.topBody) els.topBody.innerHTML = renderLlmOpsFitRowsHTML(rows.slice(0, TOP_FIT_ROWS));
    if (els.fitBody) els.fitBody.innerHTML = renderLlmOpsFitRowsHTML(rows);
    if (els.fitCount) els.fitCount.textContent = rows.length + ' rows';
    state.rowsByKey = new Map(rows.map((row) => [row.key, row]));
    if (state.detailKey && state.rowsByKey.has(state.detailKey)) showLlmOpsDetail(state.detailKey);
    else if (state.detailKey) hideLlmOpsDetail();
  }

  async function refreshLlmOpsData() {
    const els = llmOpsElements();
    if (!els) return null;
    bindLlmOpsEvents(els);
    const sequence = ++state.refreshSequence;
    setBusy(els.summary, true);
    setBusy(els.nodes, true);
    const data = {errors: {models: {}}, nodeModels: {}, previous: state.lastModel};
    try {
      data.nodes = await fetchLlmOpsJSON('/viewer/llm-ops/nodes');
    } catch (_) {
      data.errors.nodes = true;
    }
    if (sequence !== state.refreshSequence) return null;
    if (!data.errors.nodes) {
      const nodeIds = arrayValue(objectValue(data.nodes).nodes).map((node) => textValue(objectValue(node).node_id)).filter(Boolean);
      const settled = await Promise.allSettled(nodeIds.map((nodeId) => {
        return fetchLlmOpsJSON('/viewer/llm-ops/node/models?node_id=' + encodeURIComponent(nodeId) + '&limit=' + MODELS_PER_NODE);
      }));
      if (sequence !== state.refreshSequence) return null;
      settled.forEach((result, index) => {
        if (result.status === 'fulfilled') data.nodeModels[nodeIds[index]] = result.value;
        else data.errors.models[nodeIds[index]] = true;
      });
    }
    const model = buildLlmOpsViewModel(data);
    if (!data.errors.nodes) state.lastModel = model;
    renderLlmOpsModel(model, els);
    setBusy(els.summary, false);
    setBusy(els.nodes, false);
    return model;
  }

  async function requestLlmOpsRefresh() {
    const els = llmOpsElements();
    if (!els) return null;
    if (state.manualRefreshInFlight) return null;
    state.manualRefreshInFlight = true;
    const button = els.refreshBtn;
    if (button) {
      button.disabled = true;
      setBusy(button, true);
    }
    if (els.refreshNote) els.refreshNote.textContent = 'Refreshing llmfit observations...';
    let result = null;
    try {
      try {
        result = await fetchLlmOpsJSON('/viewer/llm-ops/refresh', {method: 'POST', timeoutMS: REFRESH_TIMEOUT_MS});
        if (els.refreshNote) els.refreshNote.textContent = summarizeLlmOpsRefreshResult(result);
      } catch (err) {
        if (els.refreshNote) els.refreshNote.textContent = 'Refresh request failed: ' + textValue(err && err.message, 'unknown error');
      }
      await refreshLlmOpsData();
    } finally {
      state.manualRefreshInFlight = false;
      if (button) {
        button.disabled = false;
        setBusy(button, false);
      }
    }
    return result;
  }

  const api = {
    buildLlmOpsViewModel,
    buildLlmOpsNodeCard,
    buildLlmOpsFitRow,
    buildLlmOpsMatrixRows,
    renderLlmOpsSummaryHTML,
    renderLlmOpsNodeCardsHTML,
    renderLlmOpsFitRowsHTML,
    renderLlmOpsDetailHTML,
    renderLlmOpsMatrixHTML,
    refreshLlmOpsData,
    requestLlmOpsRefresh,
    fetchLlmOpsJSON,
    normalizeLlmOpsNodeStatus,
    llmOpsConfidence,
    llmOpsFit,
    formatLlmOpsTps,
    formatLlmOpsGb,
    formatLlmOpsCount,
    formatLlmOpsTime,
    summarizeLlmOpsRefreshResult,
    escapeLlmOpsHTML,
  };
  if (root) {
    root.refreshLlmOpsData = refreshLlmOpsData;
    root.requestLlmOpsRefresh = requestLlmOpsRefresh;
  }
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof window !== 'undefined' ? window : globalThis);
