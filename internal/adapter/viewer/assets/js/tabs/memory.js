// Memory tab module: memory snapshots, layers, source registry, and recall traces.
function renderMemorySnapshot() {
  const snap = state.memory.snapshot || {};
  const fetchError = String(state.memory.memorySnapshotFetchError || '');
  const actionError = String(state.memory.memoryActionError || '');
  const memory = Array.isArray(snap.memory) ? snap.memory : [];
  const news = Array.isArray(snap.news) ? snap.news : [];
  const digests = Array.isArray(snap.digests) ? snap.digests : [];
  const knowledge = Array.isArray(snap.knowledge) ? snap.knowledge : [];
  const memoryBody = document.getElementById('memoryBody');
  const newsBody = document.getElementById('newsPackBody');
  const digestBody = document.getElementById('digestBody');
  const memoryCount = document.getElementById('memoryCount');
  const newsPackCount = document.getElementById('newsPackCount');
  const digestCount = document.getElementById('digestCount');
  const knowledgeCount = document.getElementById('knowledgeCount');
  if (memoryCount) memoryCount.textContent = fetchError ? '0' : String(memory.length);
  if (newsPackCount) newsPackCount.textContent = fetchError ? '0' : String(news.length);
  if (digestCount) digestCount.textContent = fetchError ? '0' : String(digests.length);
  if (knowledgeCount) knowledgeCount.textContent = fetchError ? '0' : String(knowledge.length);
  if (memoryBody) {
    memoryBody.innerHTML = '';
    if (fetchError) {
      memoryBody.innerHTML = '<tr><td colspan="7" class="small">Memory snapshot unavailable: ' + esc(fetchError) + '</td></tr>';
    } else if (actionError) {
      memoryBody.innerHTML = '<tr><td colspan="7" class="small">Memory action unavailable: ' + esc(actionError) + '</td></tr>';
    } else if (memory.length === 0) {
      memoryBody.innerHTML = '<tr><td colspan="7" class="small">No memory for selected namespace</td></tr>';
    } else {
      memory.forEach((m) => {
        const tr = document.createElement('tr');
        const id = esc(m.ID || m.id || '');
        tr.innerHTML =
          '<td>' + esc(m.Layer || m.layer || '-') + '</td>' +
          '<td><span class="badge state-idle">' + esc(m.MemoryState || m.memory_state || '-') + '</span></td>' +
          '<td class="code">' + esc(m.Namespace || m.namespace || '-') + '</td>' +
          '<td>' + esc(m.Speaker || m.speaker || '-') + '</td>' +
          '<td>' + esc(short(m.Message || m.message || '-', 180)) + '</td>' +
          '<td>' + esc(fdt(m.UpdatedAt || m.updated_at || m.CreatedAt || m.created_at)) + '</td>' +
          '<td>' +
            '<button class="ctl-btn" onclick="setMemoryState(&quot;' + id + '&quot;, &quot;candidate&quot;)">Candidate</button> ' +
            '<button class="ctl-btn" onclick="setMemoryState(&quot;' + id + '&quot;, &quot;confirmed&quot;)">Confirm</button> ' +
            '<button class="ctl-btn" onclick="promoteMemory(&quot;' + id + '&quot;)">Promote</button>' +
          '</td>';
        memoryBody.appendChild(tr);
      });
    }
  }
  if (newsBody) {
    newsBody.innerHTML = '';
    if (fetchError) {
      newsBody.innerHTML = '<tr><td colspan="5" class="small">Memory snapshot news unavailable: ' + esc(fetchError) + '</td></tr>';
    } else if (news.length === 0) {
      newsBody.innerHTML = '<tr><td colspan="5" class="small">No news pack items</td></tr>';
    } else {
      news.forEach((n) => {
        const tr = document.createElement('tr');
        const urls = n.SourceURL || n.source_url || '';
        tr.innerHTML =
          '<td>' + esc(fdt(n.PublishedAt || n.published_at || n.FetchedAt || n.fetched_at)) + '</td>' +
          '<td>' + esc(n.Category || n.category || '-') + '</td>' +
          '<td class="code">' + esc(short(urls || n.SourceID || n.source_id || '-', 80)) + '</td>' +
          '<td>' + esc(short(n.SummaryDraft || n.summary_draft || n.RawText || n.raw_text || '-', 180)) + '</td>' +
          '<td>' + esc((n.Keywords || n.keywords || []).join ? (n.Keywords || n.keywords || []).join(', ') : '-').replace(/,/g, ', ') + '</td>';
        newsBody.appendChild(tr);
      });
    }
  }
  if (digestBody) {
    digestBody.innerHTML = '';
    if (fetchError) {
      digestBody.innerHTML = '<tr><td colspan="4" class="small">Memory snapshot digests unavailable: ' + esc(fetchError) + '</td></tr>';
    } else if (digests.length === 0) {
      digestBody.innerHTML = '<tr><td colspan="4" class="small">No daily digests</td></tr>';
    } else {
      digests.forEach((d) => {
        const tr = document.createElement('tr');
        tr.innerHTML =
          '<td>' + esc(d.DigestSlot || d.digest_slot || '-') + '</td>' +
          '<td>' + esc(d.Category || d.category || '-') + '</td>' +
          '<td>' + esc(short(d.DigestText || d.digest_text || '-', 220)) + '</td>' +
          '<td>' + esc(fdt(d.CreatedAt || d.created_at)) + '</td>';
        digestBody.appendChild(tr);
      });
    }
  }
  renderNewsPackPanel();
}

function renderMemoryLayers() {
  const layers = state.memory.layers || {};
  const error = layers._error || '';
  const l0 = Array.isArray(layers.l0) ? layers.l0 : [];
  const l1 = Array.isArray(layers.l1) ? layers.l1 : [];
  const l2 = Array.isArray(layers.l2) ? layers.l2 : [];
  const l3 = Array.isArray(layers.l3) ? layers.l3 : [];
  const body = document.getElementById('memoryLayerBody');
  const l0Count = document.getElementById('memoryL0Count');
  const l2Count = document.getElementById('memoryL2Count');
  const l3Count = document.getElementById('memoryL3Count');
  if (l0Count) l0Count.textContent = String(l0.length);
  if (l2Count) l2Count.textContent = String(l2.length);
  if (l3Count) l3Count.textContent = String(l3.length);
  if (!body) return;
  body.innerHTML = '';
  if (error) {
    body.innerHTML = '<tr><td colspan="5" class="small">Memory Layers unavailable: ' + esc(error) + '</td></tr>';
    return;
  }
  const rows = [];
  const pushMemory = (layer, item) => {
    rows.push({
      layer,
      scope: item.Namespace || item.namespace || item.SessionID || item.session_id || '-',
      kind: item.MemoryState || item.memory_state || item.Speaker || item.speaker || '-',
      summary: item.Message || item.message || '-',
      updated: item.UpdatedAt || item.updated_at || item.CreatedAt || item.created_at || '',
    });
  };
  l0.forEach((item) => pushMemory('L0', item));
  l1.forEach((item) => pushMemory('L1', item));
  l2.forEach((item) => rows.push({
    layer: 'L2',
    scope: item.Domain || item.domain || '-',
    kind: 'thread_summary',
    summary: item.Summary || item.summary || '-',
    updated: item.EndTime || item.end_time || item.StartTime || item.start_time || '',
  }));
  l3.forEach((item) => pushMemory('L3', item));
  if (rows.length === 0) {
    body.innerHTML = '<tr><td colspan="5" class="small">No L0/L2/L3 memory layers</td></tr>';
    return;
  }
  rows.forEach((row) => {
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td>' + esc(row.layer) + '</td>' +
      '<td class="code">' + esc(row.scope) + '</td>' +
      '<td>' + esc(row.kind) + '</td>' +
      '<td>' + esc(short(row.summary, 220)) + '</td>' +
      '<td>' + esc(fdt(row.updated)) + '</td>';
    body.appendChild(tr);
  });
}

function refreshMemoryLayers() {
  const params = new URLSearchParams();
  params.set('limit', '20');
  if (memorySession && memorySession.value.trim()) params.set('session_id', memorySession.value.trim());
  if (memoryNamespace && memoryNamespace.value.trim()) params.set('namespace', memoryNamespace.value.trim());
  if (memoryDomain && memoryDomain.value.trim()) params.set('domain', memoryDomain.value.trim());
  fetch('/viewer/memory/layers?' + params.toString())
    .then((r) => {
      if (!r.ok) {
        return r.text().then((body) => {
          throw new Error(body || ('memory layers fetch failed: HTTP ' + r.status));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.memory.layers = data || {l0: [], l1: [], l2: [], l3: []};
      renderMemoryLayers();
    })
    .catch((err) => {
      state.memory.layers = {l0: [], l1: [], l2: [], l3: [], l3_qdrant: [], _error: err && err.message ? err.message : String(err)};
      renderMemoryLayers();
      console.error(err);
    });
}

function renderMemoryRecallPack() {
  const body = document.getElementById('recallPackBody');
  const count = document.getElementById('recallPackCount');
  const pack = state.memory.recallPack || {};
  const items = Array.isArray(pack.items) ? pack.items : [];
  const error = String(state.memory.recallPackFetchError || '');
  if (count) count.textContent = error ? '0' : String(items.length);
  if (!body) return;
  body.innerHTML = '';
  if (error) {
    body.innerHTML = '<tr><td colspan="7" class="small">Recall Pack unavailable: ' + esc(error) + '</td></tr>';
    return;
  }
  if (items.length === 0) {
    body.innerHTML = '<tr><td colspan="7" class="small">思い出したことはまだありません</td></tr>';
    return;
  }
  items.forEach((item) => {
    const eventIDs = item.event_ids || item.EventIDs || [];
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td>' + esc(item.layer || item.Layer || '-') + '</td>' +
      '<td class="code">' + esc(item.namespace || item.Namespace || '-') + '</td>' +
      '<td><span class="badge state-idle">' + esc(item.state || item.State || '-') + '</span></td>' +
      '<td>' + esc(String(item.score || item.Score || 0)) + '</td>' +
      '<td>' + esc(item.kind || item.Kind || '-') + '</td>' +
      '<td>' + esc(short(item.summary || item.Summary || '-', 220)) + '</td>' +
      '<td class="code">' + esc(short(Array.isArray(eventIDs) ? eventIDs.join(', ') : String(eventIDs || '-'), 120)) + '</td>';
    body.appendChild(tr);
  });
}

function refreshMemoryRecallPack() {
  const params = new URLSearchParams();
  params.set('limit', '20');
  params.set('user_id', 'ren');
  if (memorySession && memorySession.value.trim()) params.set('session_id', memorySession.value.trim());
  if (memoryDomain && memoryDomain.value.trim()) params.set('domain', memoryDomain.value.trim());
  fetch('/viewer/memory/recall-pack?' + params.toString())
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'recall pack unavailable'));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.memory.recallPackFetchError = '';
      state.memory.recallPack = data || {items: []};
      renderMemoryRecallPack();
    })
    .catch((err) => {
      state.memory.recallPackFetchError = String(err && err.message ? err.message : err);
      state.memory.recallPack = {items: []};
      renderMemoryRecallPack();
      console.error(err);
    });
}

function renderUserMemory() {
  const body = document.getElementById('userMemoryBody');
  const count = document.getElementById('userMemoryCount');
  const items = Array.isArray(state.memory.userMemory) ? state.memory.userMemory : [];
  const error = String(state.memory.userMemoryFetchError || '');
  if (count) count.textContent = error ? '0' : String(items.length);
  if (!body) return;
  body.innerHTML = '';
  if (error) {
    body.innerHTML = '<tr><td colspan="6" class="small">User Memory unavailable: ' + esc(error) + '</td></tr>';
    return;
  }
  if (items.length === 0) {
    body.innerHTML = '<tr><td colspan="6" class="small">user:ren の記憶はまだありません</td></tr>';
    return;
  }
  items.forEach((item) => {
    const id = item.id || item.ID || '';
    const evidence = item.evidence_event_ids || item.EvidenceEventIDs || [];
    const stateValue = item.state || item.State || '-';
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td><span class="badge state-idle">' + esc(stateValue) + '</span></td>' +
      '<td>' + esc(item.type || item.Type || '-') + '</td>' +
      '<td>' + esc(short(item.statement || item.Statement || '-', 260)) + '</td>' +
      '<td class="code">' + esc(short(Array.isArray(evidence) ? evidence.join(', ') : String(evidence || '-'), 120)) + '</td>' +
      '<td>' + esc(fdt(item.updated_at || item.UpdatedAt || item.created_at || item.CreatedAt)) + '</td>' +
      '<td>' +
        '<button class="ctl-btn" onclick="setUserMemoryState(&quot;' + esc(id) + '&quot;,&quot;confirmed&quot;)">Confirm</button> ' +
        '<button class="ctl-btn" onclick="setUserMemoryState(&quot;' + esc(id) + '&quot;,&quot;pinned&quot;)">Pin</button> ' +
        '<button class="ctl-btn" onclick="forgetUserMemory(&quot;' + esc(id) + '&quot;)">Forget</button>' +
      '</td>';
    body.appendChild(tr);
  });
}

function refreshUserMemory() {
  const params = new URLSearchParams();
  params.set('user_id', 'ren');
  params.set('limit', '50');
  fetch('/viewer/memory/user?' + params.toString())
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'user memory unavailable'));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.memory.userMemoryFetchError = '';
      state.memory.userMemory = Array.isArray(data.items) ? data.items : [];
      renderUserMemory();
    })
    .catch((err) => {
      state.memory.userMemoryFetchError = String(err && err.message ? err.message : err);
      state.memory.userMemory = [];
      renderUserMemory();
      console.error(err);
    });
}

function memoryEventNamespaceValue() {
  if (memoryEventNamespace && memoryEventNamespace.value.trim()) return memoryEventNamespace.value.trim();
  if (memoryNamespace && memoryNamespace.value.trim()) return memoryNamespace.value.trim();
  return 'kb:web';
}

function eventPayloadSummary(ev) {
  const payload = ev.Payload || ev.payload || {};
  try {
    return JSON.stringify(payload);
  } catch (_) {
    return String(payload || '-');
  }
}

function renderMemoryEvents() {
  const eventBody = document.getElementById('memoryEventBody');
  const cacheBody = document.getElementById('searchCacheBody');
  const eventCount = document.getElementById('memoryEventCount');
  const cacheCount = document.getElementById('searchCacheCount');
  const fetchError = String(state.memory.memoryEventsFetchError || '');
  const events = Array.isArray(state.memory.events) ? state.memory.events : [];
  const searchCache = Array.isArray(state.memory.searchCache) ? state.memory.searchCache : [];
  if (eventCount) eventCount.textContent = fetchError ? '0' : String(events.length);
  if (cacheCount) cacheCount.textContent = fetchError ? '0' : String(searchCache.length);
  if (eventBody) {
    eventBody.innerHTML = '';
    if (fetchError) {
      eventBody.innerHTML = '<tr><td colspan="5" class="small">Memory events unavailable: ' + esc(fetchError) + '</td></tr>';
    } else if (events.length === 0) {
      eventBody.innerHTML = '<tr><td colspan="5" class="small">No L1 event log entries</td></tr>';
    } else {
      events.forEach((ev) => {
        const tr = document.createElement('tr');
        tr.innerHTML =
          '<td>' + esc(fdt(ev.CreatedAt || ev.created_at)) + '</td>' +
          '<td class="code">' + esc(ev.Namespace || ev.namespace || '-') + '</td>' +
          '<td>' + esc(ev.EventType || ev.event_type || '-') + '</td>' +
          '<td>' + esc(ev.Source || ev.source || '-') + '</td>' +
          '<td class="code">' + esc(short(eventPayloadSummary(ev), 220)) + '</td>';
        eventBody.appendChild(tr);
      });
    }
  }
  if (cacheBody) {
    cacheBody.innerHTML = '';
    if (fetchError) {
      cacheBody.innerHTML = '<tr><td colspan="5" class="small">Search cache unavailable: ' + esc(fetchError) + '</td></tr>';
    } else if (searchCache.length === 0) {
      cacheBody.innerHTML = '<tr><td colspan="5" class="small">No search cache entries</td></tr>';
    } else {
      searchCache.forEach((entry) => {
        const urls = entry.SourceURLs || entry.source_urls || [];
        const tr = document.createElement('tr');
        tr.innerHTML =
          '<td>' + esc(fdt(entry.RetrievedAt || entry.retrieved_at)) + '</td>' +
          '<td>' + esc(entry.Provider || entry.provider || '-') + '</td>' +
          '<td>' + esc(short(entry.RawQuery || entry.raw_query || entry.NormalizedQuery || entry.normalized_query || '-', 140)) + '</td>' +
          '<td>' + esc(fdt(entry.ExpiresAt || entry.expires_at)) + '</td>' +
          '<td class="code">' + esc(short(Array.isArray(urls) ? urls.join(', ') : String(urls || '-'), 160)) + '</td>';
        cacheBody.appendChild(tr);
      });
    }
  }
  if (typeof renderWebGatherDiagnostics === 'function') renderWebGatherDiagnostics();
}

function webGatherStagingItems() {
  const items = Array.isArray(state.memory.sourceRegistryStaging) ? state.memory.sourceRegistryStaging : [];
  return items.filter((item) => {
    const meta = item.Meta || item.meta || {};
    const sourceID = String(item.SourceID || item.source_id || '');
    return meta.tool === 'rencrow-web-gather' ||
      meta.fetcher === 'web_gather' ||
      meta.security_warning_source === 'web_gather' ||
      sourceID.startsWith('web:');
  });
}

function webGatherSourceEntries() {
  const entries = Array.isArray(state.memory.sourceRegistry) ? state.memory.sourceRegistry : [];
  return entries.filter((entry) => String(entry.kind || entry.Kind || '') === 'web_gather');
}

function webGatherSearchEntries() {
  const entries = Array.isArray(state.memory.searchCache) ? state.memory.searchCache : [];
  return entries.filter((entry) => {
    const provider = String(entry.Provider || entry.provider || '');
    const urls = entry.SourceURLs || entry.source_urls || [];
    return provider === 'searxng' || provider === 'local_cache' || (Array.isArray(urls) && urls.some((u) => String(u).startsWith('http')));
  });
}

function webGatherFailureCounts(entries) {
  const counts = {blocked: 0, timeout: 0, extraction_failed: 0, failed: 0};
  entries.forEach((entry) => {
    const status = String(entry.last_status || entry.LastStatus || '');
    const err = String(entry.last_error || entry.LastError || '').toLowerCase();
    if (status !== 'error' && !err) return;
    counts.failed += 1;
    if (err.includes('timeout') || err.includes('timed out')) counts.timeout += 1;
    if (err.includes('blocked') || err.includes('captcha') || err.includes('bot challenge')) counts.blocked += 1;
    if (err.includes('extract') || err.includes('empty content') || err.includes('unsupported content')) counts.extraction_failed += 1;
  });
  return counts;
}

function renderWebGatherDiagnostics() {
  const summaryBody = document.getElementById('webGatherSummaryBody');
  const recentBody = document.getElementById('webGatherRecentBody');
  if (!summaryBody && !recentBody) return;
  const staging = webGatherStagingItems();
  const sources = webGatherSourceEntries();
  const searches = webGatherSearchEntries();
  const failures = webGatherFailureCounts(sources);
  const latestSearch = searches.length ? searches[0] : null;
  const rows = [
    ['recent search query', latestSearch ? short(latestSearch.RawQuery || latestSearch.raw_query || latestSearch.NormalizedQuery || latestSearch.normalized_query || '-', 160) : '-'],
    ['search provider', latestSearch ? (latestSearch.Provider || latestSearch.provider || '-') : '-'],
    ['search result count', latestSearch ? String((latestSearch.SourceURLs || latestSearch.source_urls || []).length || '-') : '0'],
    ['staging count', String(staging.length)],
    ['fetch success / failed', String(staging.length) + ' / ' + String(failures.failed)],
    ['blocked / timeout / extraction_failed', String(failures.blocked) + ' / ' + String(failures.timeout) + ' / ' + String(failures.extraction_failed)],
  ];
  if (summaryBody) {
    summaryBody.innerHTML = '';
    rows.forEach((row) => {
      const tr = document.createElement('tr');
      tr.innerHTML = '<td>' + esc(row[0]) + '</td><td class="code">' + esc(row[1]) + '</td>';
      summaryBody.appendChild(tr);
    });
  }
  if (recentBody) {
    recentBody.innerHTML = '';
    if (staging.length === 0) {
      recentBody.innerHTML = '<tr><td colspan="5" class="small">No Web Gather staging items</td></tr>';
      return;
    }
    staging.slice(0, 8).forEach((item) => {
      const warnings = sourceRegistryWarningCount(item);
      const warningLabel = warnings > 0 ? '<span class="badge warn">' + esc(String(warnings)) + '</span>' : '<span class="badge">0</span>';
      const id = esc(item.id || item.ID || '');
      const tr = document.createElement('tr');
      tr.innerHTML =
        '<td>' + esc(item.validation_status || item.ValidationStatus || '-') + '</td>' +
        '<td class="code">' + esc(short(item.source_id || item.SourceID || item.source_url || item.SourceURL || '-', 58)) + '</td>' +
        '<td>' + esc(short(item.summary_draft || item.SummaryDraft || item.raw_text || item.RawText || '-', 110)) + '</td>' +
        '<td>' + warningLabel + '</td>' +
        '<td><button class="ctl-btn" onclick="validateSourceRegistryStaging(&quot;' + id + '&quot;)">Validate</button></td>';
      recentBody.appendChild(tr);
    });
  }
}

function knowledgeMemoryID(type, item) {
  if (!item) return '';
  if (type === 'personal_archive') return item.entry_id || item.EntryID || '';
  if (type === 'creative_knowledge' || type === 'news_knowledge') return item.item_id || item.ItemID || '';
  if (type === 'daily_intake_rule') return item.rule_id || item.RuleID || '';
  if (type === 'temporal_marker') return item.marker_id || item.MarkerID || '';
  if (type === 'dream_run') return item.run_id || item.RunID || '';
  return item.id || item.ID || '';
}

function knowledgeMemoryTitle(type, item) {
  if (!item) return '-';
  return item.title || item.Title || item.topic || item.Topic || item.summary || item.Summary ||
    item.summary_draft || item.SummaryDraft || item.raw_text || item.RawText || type;
}

function knowledgeMemorySource(item) {
  if (!item) return '-';
  return item.source_url || item.SourceURL || item.source_id || item.SourceID || item.user_id || item.UserID || item.namespace || item.Namespace || '-';
}

function knowledgeMemoryStatus(type, item) {
  if (!item) return '-';
  const status = item.status || item.Status || item.review_status || item.ReviewStatus || item.memory_state || item.MemoryState || item.validation_status || item.ValidationStatus;
  if (status) return status;
  if (type === 'daily_intake_rule') return item.enabled || item.Enabled ? 'enabled' : 'disabled';
  return '-';
}

function knowledgeMemoryUpdated(item) {
  if (!item) return '';
  return item.updated_at || item.UpdatedAt || item.created_at || item.CreatedAt || item.fetched_at || item.FetchedAt || '';
}

function knowledgeMemoryWarningCount(item) {
  if (!item) return 0;
  const meta = item.meta || item.Meta || {};
  const warnings = item.security_warnings || item.SecurityWarnings || item.prompt_injection_warnings || item.PromptInjectionWarnings ||
    meta.security_warnings || meta.prompt_injection_warnings || meta.warnings || item.warnings || item.Warnings;
  if (Array.isArray(warnings)) return warnings.length;
  if (typeof warnings === 'number') return warnings;
  return warnings ? 1 : 0;
}

function knowledgeMemoryWarningList(item) {
  if (!item) return [];
  const meta = item.meta || item.Meta || {};
  const warnings = item.security_warnings || item.SecurityWarnings || item.prompt_injection_warnings || item.PromptInjectionWarnings ||
    meta.security_warnings || meta.prompt_injection_warnings || meta.warnings || item.warnings || item.Warnings;
  if (Array.isArray(warnings)) return warnings.map((warning) => String(warning || '').trim()).filter(Boolean);
  if (typeof warnings === 'number') return warnings > 0 ? ['warning_count=' + warnings] : [];
  return warnings ? [String(warnings)] : [];
}

function knowledgeMemoryCompressedText(item) {
  if (!item) return '';
  return item.compressed_text || item.CompressedText || item.compressed_summary || item.CompressedSummary ||
    item.summary_draft || item.SummaryDraft || item.summary || item.Summary || '';
}

function knowledgeMemoryOriginalText(item) {
  if (!item) return '';
  return item.original_text || item.OriginalText || item.raw_text || item.RawText || item.body || item.Body || '';
}

function knowledgeMemoryFlags(type, item) {
  const flags = [];
  if (knowledgeMemoryOriginalText(item)) flags.push('original');
  if (Boolean(item && (item.protected || item.Protected))) flags.push('protected');
  if (knowledgeMemoryCompressedText(item)) flags.push('compressed');
  if (knowledgeMemoryWarningCount(item) > 0) flags.push('warning');
  if (type === 'dream_run') flags.push('review');
  return flags;
}

function knowledgeMemoryRelationKeys(item) {
  if (!item) return [];
  const keys = [
    item.staging_id, item.StagingID,
    item.source_ref, item.SourceRef,
    item.source_id, item.SourceID,
    item.source_url, item.SourceURL,
    item.url, item.URL,
    item.event_id, item.EventID,
  ];
  return keys.map((v) => String(v || '').trim()).filter(Boolean);
}

function sourceRegistryStagingRelationKeys(item) {
  if (!item) return [];
  const keys = [
    item.id, item.ID,
    item.source_id, item.SourceID,
    item.source_url, item.SourceURL,
    item.event_id, item.EventID,
  ];
  return keys.map((v) => String(v || '').trim()).filter(Boolean);
}

function relationIntersects(left, right) {
  if (!Array.isArray(left) || !Array.isArray(right) || left.length === 0 || right.length === 0) return false;
  const set = new Set(left);
  return right.some((key) => set.has(key));
}

function relatedSourceRegistryStagingItems(item) {
  const keys = knowledgeMemoryRelationKeys(item);
  const staging = Array.isArray(state.memory.sourceRegistryStaging) ? state.memory.sourceRegistryStaging : [];
  return staging.filter((candidate) => relationIntersects(keys, sourceRegistryStagingRelationKeys(candidate)));
}

function relatedKnowledgeMemoryRows(stagingItem) {
  const keys = sourceRegistryStagingRelationKeys(stagingItem);
  return knowledgeMemoryRows(state.memory.knowledgeMemory || {}).filter((row) => relationIntersects(keys, knowledgeMemoryRelationKeys(row.item)));
}

function sourceRegistryWarningCount(item) {
  const meta = item && item.meta ? item.meta : {};
  const warnings = meta.prompt_injection_warnings || meta.security_warnings || meta.warnings || [];
  if (Array.isArray(warnings)) return warnings.length;
  if (typeof warnings === 'number') return warnings;
  return warnings ? 1 : 0;
}

function sourceRegistryStagingReviewSummary(item) {
  if (!item) return '-';
  const warnings = sourceRegistryWarningCount(item);
  const status = item.validation_status || item.ValidationStatus || '-';
  const kind = item.kind || item.Kind || '-';
  const id = item.id || item.ID || '-';
  const source = item.source_id || item.SourceID || item.source_url || item.SourceURL || '-';
  return [
    'id=' + id,
    'status=' + status,
    'kind=' + kind,
    'warnings=' + warnings,
    'source=' + source,
  ].join(' / ');
}

function knowledgeMemoryPromoteState(type, item, relatedStaging) {
  const status = knowledgeMemoryStatus(type, item);
  const warningCount = knowledgeMemoryWarningCount(item);
  const protectedOriginal = Boolean(item && (item.protected || item.Protected));
  const related = Array.isArray(relatedStaging) ? relatedStaging : [];
  const validatedRelated = related.filter((staging) => String(staging.validation_status || staging.ValidationStatus || '') === 'validated').length;
  const blocked = [];
  if (warningCount > 0) blocked.push('warnings');
  if (protectedOriginal && type === 'personal_archive') blocked.push('protected_original');
  if (related.length > 0 && validatedRelated === 0) blocked.push('related_staging_not_validated');
  if (String(status || '') === 'rejected') blocked.push('rejected');
  return {
    status,
    warningCount,
    protectedOriginal,
    relatedCount: related.length,
    validatedRelated,
    blocked,
  };
}

function renderKnowledgeMemoryReviewComparison(type, item, relatedStaging) {
  const stateLine = knowledgeMemoryPromoteState(type, item, relatedStaging);
  const warnings = knowledgeMemoryWarningList(item);
  const relatedLines = Array.isArray(relatedStaging) && relatedStaging.length ?
    relatedStaging.map((staging) => sourceRegistryStagingReviewSummary(staging)) :
    ['-'];
  const blocked = stateLine.blocked.length ? stateLine.blocked.join(', ') : 'none';
  return [
    'knowledge_status=' + (stateLine.status || '-'),
    'warning_count=' + stateLine.warningCount,
    'protected_original=' + String(stateLine.protectedOriginal),
    'related_staging=' + stateLine.relatedCount,
    'related_validated=' + stateLine.validatedRelated,
    'promote_blockers=' + blocked,
    '',
    'warnings:',
    warnings.length ? warnings.map((warning) => '- ' + warning).join('\n') : '-',
    '',
    'related staging:',
    relatedLines.join('\n'),
  ].join('\n');
}

function knowledgeMemoryReviewActions(type, id, status) {
  if (!['creative_knowledge', 'news_knowledge', 'daily_intake_rule'].includes(String(type || ''))) return '';
  if (!id) return '';
  const payload = encodeURIComponent(JSON.stringify({detail_type: type, id}));
  const current = String(status || '');
  if (current === 'promoted' || current === 'enabled' || current === 'rejected') {
    return '<button class="ctl-btn" onclick="fetchMemoryKnowledgeDetail(&quot;' + esc(type) + '&quot;,&quot;' + esc(id) + '&quot;)">Detail</button>';
  }
  return [
    '<button class="ctl-btn" onclick="fetchMemoryKnowledgeDetail(&quot;' + esc(type) + '&quot;,&quot;' + esc(id) + '&quot;)">Detail</button>',
    '<button class="ctl-btn" onclick="reviewKnowledgeMemoryItem(&quot;' + payload + '&quot;,&quot;approved&quot;,false)">Review</button>',
    '<button class="ctl-btn" onclick="reviewKnowledgeMemoryItem(&quot;' + payload + '&quot;,&quot;approved&quot;,true)">Promote</button>',
    '<button class="ctl-btn" onclick="reviewKnowledgeMemoryItem(&quot;' + payload + '&quot;,&quot;rejected&quot;,false)">Reject</button>',
  ].join(' ');
}

function knowledgeMemoryCurrentFilters() {
  const type = document.getElementById('knowledgeMemoryTypeFilter');
  const review = document.getElementById('knowledgeMemoryReviewFilter');
  const flag = document.getElementById('knowledgeMemoryFlagFilter');
  return {
    type: type ? String(type.value || '') : '',
    review: review ? String(review.value || '') : '',
    flag: flag ? String(flag.value || '') : '',
  };
}

function knowledgeMemoryRows(data) {
  const km = data || {};
  const groups = [
    ['personal_archive', km.personal_archive || []],
    ['creative_knowledge', km.creative_knowledge || []],
    ['news_knowledge', km.news_knowledge || []],
    ['daily_intake_rule', km.daily_intake_rules || []],
    ['temporal_marker', km.temporal_markers || []],
    ['dream_run', km.dream_runs || []],
  ];
  const rows = [];
  groups.forEach(([type, items]) => {
    (Array.isArray(items) ? items : []).forEach((item) => {
      rows.push({
        type,
        id: knowledgeMemoryID(type, item),
        title: knowledgeMemoryTitle(type, item),
        source: knowledgeMemorySource(item),
        status: knowledgeMemoryStatus(type, item),
        flags: knowledgeMemoryFlags(type, item),
        relatedStaging: relatedSourceRegistryStagingItems(item),
        updated: knowledgeMemoryUpdated(item),
        item,
      });
    });
  });
  return rows;
}

function renderKnowledgeMemoryLedger() {
  const fetchError = String(state.memory.knowledgeMemoryFetchError || '');
  const km = state.memory.knowledgeMemory || {};
  ['knowledgeMemoryTypeFilter', 'knowledgeMemoryReviewFilter', 'knowledgeMemoryFlagFilter'].forEach((id) => {
    const el = document.getElementById(id);
    if (el) el.onchange = renderKnowledgeMemoryLedger;
  });
  const personal = fetchError ? [] : (Array.isArray(km.personal_archive) ? km.personal_archive : []);
  const creative = fetchError ? [] : (Array.isArray(km.creative_knowledge) ? km.creative_knowledge : []);
  const news = fetchError ? [] : (Array.isArray(km.news_knowledge) ? km.news_knowledge : []);
  const intake = fetchError ? [] : (Array.isArray(km.daily_intake_rules) ? km.daily_intake_rules : []);
  const temporal = fetchError ? [] : (Array.isArray(km.temporal_markers) ? km.temporal_markers : []);
  const dreams = fetchError ? [] : (Array.isArray(km.dream_runs) ? km.dream_runs : []);
  const personalCount = document.getElementById('knowledgePersonalCount');
  const sourceCount = document.getElementById('knowledgeSourceCount');
  const dreamCount = document.getElementById('knowledgeDreamCount');
  if (personalCount) personalCount.textContent = String(personal.length);
  if (sourceCount) sourceCount.textContent = String(creative.length + news.length + intake.length + temporal.length);
  if (dreamCount) dreamCount.textContent = String(dreams.length);
  const body = document.getElementById('knowledgeMemoryBody');
  if (body) {
    body.innerHTML = '';
    if (fetchError) {
      body.innerHTML = '<tr><td colspan="9" class="small">Knowledge memory ledger unavailable: ' + esc(fetchError) + '</td></tr>';
    } else {
      const filters = knowledgeMemoryCurrentFilters();
      const rows = knowledgeMemoryRows(km).filter((row) => {
        if (filters.type && row.type !== filters.type) return false;
        if (filters.review && String(row.status || '') !== filters.review) return false;
        if (filters.flag && !(Array.isArray(row.flags) && row.flags.includes(filters.flag))) return false;
        return true;
      });
      if (rows.length === 0) {
        body.innerHTML = '<tr><td colspan="9" class="small">No knowledge memory ledger items</td></tr>';
      } else {
        rows.forEach((row) => {
          const flags = Array.isArray(row.flags) && row.flags.length ? row.flags.map((flag) => '<span class="badge ' + (flag === 'warning' ? 'warn' : '') + '">' + esc(flag) + '</span>').join(' ') : '<span class="badge">-</span>';
          const related = Array.isArray(row.relatedStaging) && row.relatedStaging.length ? row.relatedStaging.map((item) => '<span class="badge">' + esc(short(item.id || item.ID || item.source_id || item.SourceID || '-', 26)) + '</span>').join(' ') : '<span class="badge">-</span>';
          const tr = document.createElement('tr');
          tr.innerHTML =
            '<td>' + esc(row.type) + '</td>' +
            '<td class="code">' + esc(short(row.id || '-', 48)) + '</td>' +
            '<td>' + esc(short(row.title || '-', 140)) + '</td>' +
            '<td class="code">' + esc(short(row.source || '-', 90)) + '</td>' +
            '<td>' + esc(row.status || '-') + '</td>' +
            '<td>' + flags + '</td>' +
            '<td>' + related + '</td>' +
            '<td>' + esc(fdt(row.updated)) + '</td>' +
            '<td>' + knowledgeMemoryReviewActions(row.type, row.id, row.status) + '</td>';
          body.appendChild(tr);
        });
      }
    }
  }
  renderKnowledgeMemoryDetail();
}

function renderKnowledgeMemoryDetail() {
  const el = document.getElementById('knowledgeMemoryDetail');
  if (!el) return;
  const detail = state.memory.knowledgeMemoryDetail || null;
  if (!detail) {
    el.innerHTML = '';
    return;
  }
  if (detail.error) {
    el.innerHTML = '<span class="badge warn">' + esc(detail.error) + '</span>';
    return;
  }
  const item = detail.item || {};
  const original = knowledgeMemoryOriginalText(item);
  const compressed = knowledgeMemoryCompressedText(item);
  const warnings = knowledgeMemoryWarningCount(item);
  const review = knowledgeMemoryStatus(detail.detail_type || '', item);
  const related = relatedSourceRegistryStagingItems(item);
  const relatedText = related.length ? related.map((staging) => String(staging.id || staging.ID || '-') + ' / ' + String(staging.validation_status || staging.ValidationStatus || '-')).join('\n') : '-';
  const reviewComparison = renderKnowledgeMemoryReviewComparison(detail.detail_type || '', item, related);
  const reviewResult = state.memory.knowledgeMemoryReviewResult || null;
  el.innerHTML =
    '<div><span class="badge">detail</span> ' +
    '<span class="code">' + esc(detail.detail_type || '-') + ':' + esc(detail.id || '-') + '</span></div>' +
    '<div class="grid" style="margin-top:8px">' +
      '<div class="card"><h4>Original / Protected</h4><div class="small">protected=' + esc(String(Boolean(item.protected || item.Protected))) + '</div><pre style="white-space:pre-wrap;max-height:160px;overflow:auto">' + esc(original || '-') + '</pre></div>' +
      '<div class="card"><h4>Compressed / Summary</h4><pre style="white-space:pre-wrap;max-height:160px;overflow:auto">' + esc(compressed || '-') + '</pre></div>' +
      '<div class="card"><h4>Warning / Review</h4><div class="small">warnings=' + esc(String(warnings)) + '</div><div class="small">review_status=' + esc(review || '-') + '</div></div>' +
      '<div class="card"><h4>Review / Promote Comparison</h4><pre style="white-space:pre-wrap;max-height:180px;overflow:auto">' + esc(reviewComparison) + '</pre></div>' +
      '<div class="card"><h4>Review Result</h4><pre style="white-space:pre-wrap;max-height:180px;overflow:auto">' + esc(reviewResult ? JSON.stringify(reviewResult, null, 2) : '-') + '</pre></div>' +
      '<div class="card"><h4>Related Source Registry Staging</h4><pre style="white-space:pre-wrap;max-height:160px;overflow:auto">' + esc(relatedText) + '</pre></div>' +
    '</div>' +
    '<pre style="white-space:pre-wrap;max-height:220px;overflow:auto">' + esc(JSON.stringify(item, null, 2)) + '</pre>';
}

function reviewKnowledgeMemoryItem(encodedPayload, reviewStatus, promote) {
  let payload;
  try {
    payload = JSON.parse(decodeURIComponent(encodedPayload || '{}'));
  } catch (err) {
    state.memory.knowledgeMemoryReviewResult = {status: 'failed', error: 'invalid knowledge memory review payload'};
    renderKnowledgeMemoryDetail();
    return;
  }
  payload.review_status = reviewStatus;
  payload.promote = Boolean(promote);
  payload.reviewed_by = 'viewer';
  fetch('/viewer/knowledge-memory/review', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(payload),
  })
    .then((r) => r.json().then((data) => ({ok: r.ok, status: r.status, data})).catch(() => ({ok: r.ok, status: r.status, data: {}})))
    .then(({ok, status, data}) => {
      state.memory.knowledgeMemoryReviewResult = ok ? data : {status: 'failed', http_status: status, response: data};
      refreshKnowledgeMemoryLedger();
      if (payload.detail_type && payload.id) fetchMemoryKnowledgeDetail(payload.detail_type, payload.id);
    })
    .catch((err) => {
      state.memory.knowledgeMemoryReviewResult = {status: 'failed', error: String(err && err.message ? err.message : err)};
      renderKnowledgeMemoryDetail();
    });
}

function refreshKnowledgeMemoryLedger() {
  fetch('/viewer/knowledge-memory?limit=20')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'knowledge memory ledger unavailable'));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.memory.knowledgeMemoryFetchError = '';
      state.memory.knowledgeMemory = data || {};
      renderKnowledgeMemoryLedger();
    })
    .catch((err) => {
      const message = String(err && err.message ? err.message : err);
      state.memory.knowledgeMemoryFetchError = message;
      state.memory.knowledgeMemory = {
        personal_archive: [],
        creative_knowledge: [],
        news_knowledge: [],
        daily_intake_rules: [],
        temporal_markers: [],
        dream_runs: [],
      };
      state.memory.knowledgeMemoryDetail = {error: message};
      renderKnowledgeMemoryLedger();
      console.error(err);
    });
}

function fetchMemoryKnowledgeDetail(detailType, id) {
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
      state.memory.knowledgeMemoryDetail = data;
      renderKnowledgeMemoryDetail();
    })
    .catch((err) => {
      state.memory.knowledgeMemoryDetail = {error: String(err && err.message ? err.message : err), detail_type: type, id: detailID};
      renderKnowledgeMemoryDetail();
      console.error(err);
    });
}

function refreshMemoryEvents() {
  const params = new URLSearchParams();
  params.set('limit', '20');
  params.set('namespace', memoryEventNamespaceValue());
  fetch('/viewer/memory/events?' + params.toString())
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'memory events unavailable'));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.memory.memoryEventsFetchError = '';
      state.memory.events = Array.isArray(data.events) ? data.events : [];
      state.memory.searchCache = Array.isArray(data.search_cache) ? data.search_cache : [];
      renderMemoryEvents();
    })
    .catch((err) => {
      state.memory.memoryEventsFetchError = String(err && err.message ? err.message : err);
      state.memory.events = [];
      state.memory.searchCache = [];
      renderMemoryEvents();
      console.error(err);
    });
}

function domainGraphAssertionCounts(items) {
  const domains = new Set();
  const sources = new Set();
  let latest = '';
  (Array.isArray(items) ? items : []).forEach((item) => {
    const domain = String(item.domain || item.Domain || '').trim();
    const source = String(item.source_id || item.SourceID || '').trim();
    const updated = String(item.updated_at || item.UpdatedAt || item.created_at || item.CreatedAt || '').trim();
    if (domain) domains.add(domain);
    if (source) sources.add(source);
    if (updated && (!latest || updated > latest)) latest = updated;
  });
  return {domains: domains.size, sources: sources.size, latest};
}

function domainGraphCurrentFilterText() {
  const parts = [];
  const domain = document.getElementById('domainGraphDomain');
  const entityType = document.getElementById('domainGraphEntityType');
  const sourceID = document.getElementById('domainGraphSourceID');
  const status = document.getElementById('domainGraphStatusFilter');
  if (domain && String(domain.value || '').trim()) parts.push('domain=' + String(domain.value || '').trim());
  if (entityType && String(entityType.value || '').trim()) parts.push('entity_type=' + String(entityType.value || '').trim());
  if (sourceID && String(sourceID.value || '').trim()) parts.push('source_id=' + String(sourceID.value || '').trim());
  parts.push('validation_status=' + (status && status.value ? status.value : 'validated'));
  return parts.join(' / ');
}

function domainGraphRedactedEvidence(value) {
  if (Array.isArray(value)) return value.map(domainGraphRedactedEvidence);
  if (!value || typeof value !== 'object') return value;
  const out = {};
  Object.keys(value).forEach((key) => {
    const normalized = String(key || '').toLowerCase();
    if (['raw_text', 'original_text', 'body', 'content', 'text', 'authorization', 'cookie', 'token', 'secret'].includes(normalized)) {
      out[key] = '[redacted]';
      return;
    }
    out[key] = domainGraphRedactedEvidence(value[key]);
  });
  return out;
}

function domainGraphEvidenceText(item) {
  const evidence = domainGraphRedactedEvidence(item.evidence || item.Evidence || {});
  return JSON.stringify(evidence, null, 2);
}

function renderDomainGraphAssertions() {
  const body = document.getElementById('domainGraphAssertionBody');
  const statusEl = document.getElementById('domainGraphAssertionStatus');
  const totalEl = document.getElementById('domainGraphAssertionCount');
  const domainEl = document.getElementById('domainGraphDomainCount');
  const sourceEl = document.getElementById('domainGraphSourceCount');
  const error = String(state.memory.domainGraphAssertionsFetchError || '');
  const meta = state.memory.domainGraphAssertionsMeta || {limit: 50, offset: 0, total: 0};
  const items = error ? [] : (Array.isArray(state.memory.domainGraphAssertions) ? state.memory.domainGraphAssertions : []);
  const counts = domainGraphAssertionCounts(items);
  if (totalEl) totalEl.textContent = error ? '0' : String(meta.total || items.length);
  if (domainEl) domainEl.textContent = error ? '0' : String(counts.domains);
  if (sourceEl) sourceEl.textContent = error ? '0' : String(counts.sources);
  if (statusEl) {
    statusEl.innerHTML = error
      ? '<span class="badge warn">Domain Graph unavailable: ' + esc(error) + '</span>'
      : '<span class="badge">total=' + esc(String(meta.total || items.length)) + ' / domains=' + esc(String(counts.domains)) + ' / sources=' + esc(String(counts.sources)) + ' / latest=' + esc(fdt(counts.latest)) + '</span> <span class="code">' + esc(domainGraphCurrentFilterText()) + '</span>';
  }
  if (!body) return;
  body.innerHTML = '';
  if (error) {
    body.innerHTML = '<tr><td colspan="8" class="small">Domain Graph unavailable: ' + esc(error) + '</td></tr>';
    return;
  }
  if (items.length === 0) {
    body.innerHTML = '<tr><td colspan="8" class="small">No domain graph assertions for current filter</td></tr>';
    return;
  }
  items.forEach((item) => {
    const sourceURL = item.source_url || item.SourceURL || '';
    const rawHash = item.raw_hash || item.RawHash || '-';
    const stagingID = item.staging_id || item.StagingID || '-';
    const validationStatus = item.validation_status || item.ValidationStatus || '-';
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td>' + esc(item.domain || item.Domain || '-') + '</td>' +
      '<td>' + esc(item.entity_type || item.EntityType || '-') + '</td>' +
      '<td class="code">' + esc(short(item.entity_id || item.EntityID || '-', 52)) + '</td>' +
      '<td>' + esc(item.relation_type || item.RelationType || '-') + '</td>' +
      '<td>' + esc(String(item.confidence ?? item.Confidence ?? '-')) + '</td>' +
      '<td class="code">' + esc(short(item.source_id || item.SourceID || sourceURL || '-', 58)) + '</td>' +
      '<td>' +
        esc(short(item.summary || item.Summary || '-', 150)) +
        '<details class="domain-graph-details"><summary>details</summary>' +
          '<pre>source_url=' + esc(sourceURL || '-') + '\nraw_hash=' + esc(rawHash) + '\nstaging_id=' + esc(stagingID) + '\nvalidation_status=' + esc(validationStatus) + '\nevidence=' + esc(domainGraphEvidenceText(item)) + '</pre>' +
        '</details>' +
      '</td>' +
      '<td>' + esc(fdt(item.created_at || item.CreatedAt)) + '</td>';
    body.appendChild(tr);
  });
}

function refreshDomainGraphAssertions() {
  const params = new URLSearchParams();
  params.set('limit', '50');
  const domain = document.getElementById('domainGraphDomain');
  const entityType = document.getElementById('domainGraphEntityType');
  const sourceID = document.getElementById('domainGraphSourceID');
  const status = document.getElementById('domainGraphStatusFilter');
  if (domain && String(domain.value || '').trim()) params.set('domain', String(domain.value || '').trim());
  if (entityType && String(entityType.value || '').trim()) params.set('entity_type', String(entityType.value || '').trim());
  if (sourceID && String(sourceID.value || '').trim()) params.set('source_id', String(sourceID.value || '').trim());
  if (status && status.value) params.set('validation_status', status.value);
  fetch('/viewer/domain-graph/assertions?' + params.toString())
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'domain graph unavailable'));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.memory.domainGraphAssertionsFetchError = '';
      state.memory.domainGraphAssertions = Array.isArray(data.items) ? data.items : [];
      state.memory.domainGraphAssertionsMeta = {
        limit: Number(data.limit || 0),
        offset: Number(data.offset || 0),
        total: Number(data.total || state.memory.domainGraphAssertions.length),
      };
      renderDomainGraphAssertions();
    })
    .catch((err) => {
      state.memory.domainGraphAssertionsFetchError = String(err && err.message ? err.message : err);
      state.memory.domainGraphAssertions = [];
      state.memory.domainGraphAssertionsMeta = {limit: 50, offset: 0, total: 0};
      renderDomainGraphAssertions();
      console.error(err);
    });
}

function renderSourceRegistry() {
  const body = document.getElementById('sourceRegistryBody');
  if (!body) return;
  const fetchError = String(state.memory.sourceRegistryFetchError || '');
  const entries = Array.isArray(state.memory.sourceRegistry) ? state.memory.sourceRegistry : [];
  body.innerHTML = '';
  if (fetchError) {
    body.innerHTML = '<tr><td colspan="7" class="small">Source Registry unavailable: ' + esc(fetchError) + '</td></tr>';
    return;
  }
  if (entries.length === 0) {
    body.innerHTML = '<tr><td colspan="7" class="small">No source registry entries</td></tr>';
    return;
  }
  entries.forEach((s) => {
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td class="code">' + esc(s.source_id || '-') + '</td>' +
      '<td>' + esc(s.kind || '-') + '</td>' +
      '<td>' + esc(String(s.trust_score ?? '-')) + '</td>' +
      '<td>' + esc(String(s.fetch_interval_sec || '-')) + '</td>' +
      '<td>' + esc((s.enabled ? 'enabled' : 'disabled') + (s.last_status ? ' / ' + s.last_status : '')) + '</td>' +
      '<td class="code">' + esc(short(s.url || '-', 110)) + '</td>' +
      '<td><button class="ctl-btn" onclick="runSourceRegistryEntry(&quot;' + esc(s.source_id || '') + '&quot;)">Run</button></td>';
    body.appendChild(tr);
  });
  if (typeof renderWebGatherDiagnostics === 'function') renderWebGatherDiagnostics();
}

function renderSourceRegistryRunStatus() {
  const el = document.getElementById('sourceRegistryRunStatus');
  if (!el) return;
  const run = state.memory.sourceRegistryLastRun || null;
  if (!run || !run.result) {
    el.innerHTML = '';
    return;
  }
  const result = run.result;
  const warnings = Number(result.Warnings ?? result.warnings ?? 0);
  const parts = [
    'staged=' + esc(String(result.Staged ?? result.staged ?? 0)),
    'validated=' + esc(String(result.Validated ?? result.validated ?? 0)),
    'warnings=' + esc(String(warnings)),
  ];
  const cls = warnings > 0 ? 'badge warn' : 'badge';
  el.innerHTML = '<span class="' + cls + '">Source Registry run: ' + parts.join(' / ') + '</span>';
}

function setSourceRegistryActionStatus(message, warn) {
  const el = document.getElementById('sourceRegistryRunStatus');
  if (!el) return;
  el.innerHTML = message ? '<span class="' + (warn ? 'badge warn' : 'badge') + '">' + esc(message) + '</span>' : '';
}

function renderSourceRegistryStaging() {
  const body = document.getElementById('sourceRegistryStagingBody');
  if (!body) return;
  const fetchError = String(state.memory.sourceRegistryStagingFetchError || '');
  const items = Array.isArray(state.memory.sourceRegistryStaging) ? state.memory.sourceRegistryStaging : [];
  body.innerHTML = '';
  if (fetchError) {
    body.innerHTML = '<tr><td colspan="9" class="small">Source Registry staging unavailable: ' + esc(fetchError) + '</td></tr>';
    return;
  }
  if (items.length === 0) {
    body.innerHTML = '<tr><td colspan="9" class="small">No source registry staging items</td></tr>';
    return;
  }
  items.forEach((item) => {
    const warnings = sourceRegistryWarningCount(item);
    const warningLabel = warnings > 0 ? '<span class="badge warn">' + esc(String(warnings)) + '</span>' : '<span class="badge">0</span>';
    const relatedKnowledge = relatedKnowledgeMemoryRows(item);
    const relatedKnowledgeLabel = relatedKnowledge.length ? relatedKnowledge.map((row) => '<span class="badge">' + esc(short(row.type + ':' + row.id, 30)) + '</span>').join(' ') : '<span class="badge">-</span>';
    const id = esc(item.id || '');
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td class="code">' + esc(short(item.id || '-', 46)) + '</td>' +
      '<td>' + esc(item.validation_status || '-') + '</td>' +
      '<td>' + esc(item.kind || '-') + '</td>' +
      '<td class="code">' + esc(item.namespace || '-') + '</td>' +
      '<td class="code">' + esc(short(item.source_id || item.source_url || '-', 50)) + '</td>' +
      '<td>' + esc(short(item.summary_draft || item.raw_text || '-', 90)) + '</td>' +
      '<td>' + warningLabel + '</td>' +
      '<td>' + relatedKnowledgeLabel + '</td>' +
      '<td>' +
        '<button class="ctl-btn" onclick="validateSourceRegistryStaging(&quot;' + id + '&quot;)">Validate</button> ' +
        '<button class="ctl-btn" onclick="promoteSourceRegistryStaging(&quot;' + id + '&quot;,&quot;news&quot;)">News</button> ' +
        '<button class="ctl-btn" onclick="promoteSourceRegistryStaging(&quot;' + id + '&quot;,&quot;knowledge&quot;)">Knowledge</button> ' +
        '<button class="ctl-btn" onclick="promoteSourceRegistryStaging(&quot;' + id + '&quot;,&quot;memory&quot;)">Memory</button> ' +
        '<button class="ctl-btn" onclick="promoteSourceRegistryStaging(&quot;' + id + '&quot;,&quot;domain_graph&quot;)">Graph</button>' +
      '</td>';
    body.appendChild(tr);
  });
  if (typeof renderWebGatherDiagnostics === 'function') renderWebGatherDiagnostics();
}

function setSourceRegistryStagingStatus(message, warn) {
  const el = document.getElementById('sourceRegistryStagingStatusLine');
  if (!el) return;
  el.innerHTML = message ? '<span class="' + (warn ? 'badge warn' : 'badge') + '">' + esc(message) + '</span>' : '';
}

function sourceRegistryInputValue(id) {
  const el = document.getElementById(id);
  return el ? String(el.value || '').trim() : '';
}

function refreshSourceRegistryStaging() {
  const statusEl = document.getElementById('sourceRegistryStagingStatus');
  const status = statusEl && statusEl.value ? statusEl.value : 'pending';
  fetch('/viewer/source-registry?action=staging&status=' + encodeURIComponent(status) + '&limit=20')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'source registry staging unavailable'));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.memory.sourceRegistryStagingFetchError = '';
      state.memory.sourceRegistryStaging = Array.isArray(data.items) ? data.items : [];
      renderSourceRegistryStaging();
      setSourceRegistryStagingStatus('staging=' + state.memory.sourceRegistryStaging.length, false);
    })
    .catch((err) => {
      state.memory.sourceRegistryStagingFetchError = String(err && err.message ? err.message : err);
      state.memory.sourceRegistryStaging = [];
      renderSourceRegistryStaging();
      setSourceRegistryStagingStatus('staging unavailable: ' + state.memory.sourceRegistryStagingFetchError, true);
      console.error(err);
    });
}

function validateSourceRegistryStaging(id) {
  const stagingID = String(id || '').trim();
  if (!stagingID) return;
  const trustEl = document.getElementById('sourceRegistryStagingTrust');
  const trustRaw = trustEl ? trustEl.value.trim() : '';
  const minTrust = trustRaw ? Number(trustRaw) : 0.5;
  fetch('/viewer/source-registry?action=validate', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({id: stagingID, minimum_trust_score: Number.isFinite(minTrust) ? minTrust : 0.5}),
  }).then((r) => {
    if (!r.ok) {
      return r.text().then((text) => {
        throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'source registry staging validation failed'));
      });
    }
    return r.json();
  }).then((data) => {
    const result = data && data.result ? data.result : {};
    setSourceRegistryStagingStatus('validated=' + (result.Status || result.status || '-'), !(result.Passed || result.passed));
    refreshSourceRegistryStaging();
    refreshMemorySnapshot();
  }).catch((err) => {
    setSourceRegistryStagingStatus(err.message || String(err), true);
    console.error(err);
  });
}

function promoteSourceRegistryStaging(id, target) {
  const stagingID = String(id || '').trim();
  const promotionTarget = String(target || '').trim();
  if (!stagingID || !promotionTarget) return;
  const payload = {id: stagingID, target: promotionTarget};
  if (promotionTarget === 'news') {
    const category = document.getElementById('sourceRegistryStagingCategory');
    payload.category = category && category.value.trim() ? category.value.trim() : 'general';
  } else if (promotionTarget === 'knowledge') {
    const domain = document.getElementById('sourceRegistryStagingDomain');
    payload.domain = domain && domain.value.trim() ? domain.value.trim() : 'general';
  } else if (promotionTarget === 'memory') {
    const namespace = document.getElementById('sourceRegistryStagingNamespace');
    payload.target_namespace = namespace ? namespace.value.trim() : '';
  } else if (promotionTarget === 'domain_graph') {
    payload.domain = sourceRegistryInputValue('domainGraphDomain') || sourceRegistryInputValue('sourceRegistryStagingGraphDomain') || 'movie';
    payload.entity_type = sourceRegistryInputValue('sourceRegistryStagingGraphEntityType') || 'work';
    const entityID = sourceRegistryInputValue('sourceRegistryStagingGraphEntityID');
    const relationType = sourceRegistryInputValue('sourceRegistryStagingGraphRelation');
    const confidenceRaw = sourceRegistryInputValue('sourceRegistryStagingGraphConfidence');
    if (entityID) payload.entity_id = entityID;
    if (relationType) payload.relation_type = relationType;
    if (confidenceRaw) {
      const confidence = Number(confidenceRaw);
      if (Number.isFinite(confidence)) payload.confidence = confidence;
    }
  }
  fetch('/viewer/source-registry?action=promote', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(payload),
  }).then((r) => {
    if (!r.ok) {
      return r.text().then((text) => {
        throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'source registry staging promotion failed'));
      });
    }
    return r.json();
  }).then((data) => {
    setSourceRegistryStagingStatus('promoted=' + (data.target || promotionTarget), false);
    refreshSourceRegistryStaging();
    refreshMemorySnapshot();
    if (promotionTarget === 'domain_graph') refreshDomainGraphAssertions();
  }).catch((err) => {
    setSourceRegistryStagingStatus(err.message || String(err), true);
    console.error(err);
  });
}

function runSourceRegistryEntry(sourceID) {
  const id = String(sourceID || '').trim();
  if (!id) return;
  fetch('/viewer/source-registry?action=run&source_id=' + encodeURIComponent(id), {
    method: 'POST',
  }).then((r) => {
    if (!r.ok) {
      return r.text().then((text) => {
        throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'source registry run failed'));
      });
    }
    return r.json();
  }).then((data) => {
    state.memory.sourceRegistryLastRun = data;
    renderSourceRegistryRunStatus();
    refreshSourceRegistry();
    refreshSourceRegistryStaging();
    refreshMemorySnapshot();
  }).catch((err) => {
    state.memory.sourceRegistryLastRun = {result: {warnings: 1}, error: String(err && err.message ? err.message : err)};
    const el = document.getElementById('sourceRegistryRunStatus');
    if (el) el.innerHTML = '<span class="badge warn">Source Registry run unavailable: ' + esc(state.memory.sourceRegistryLastRun.error) + '</span>';
    console.error(err);
  });
}

function refreshSourceRegistry() {
  fetch('/viewer/source-registry')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'source registry unavailable'));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.memory.sourceRegistryFetchError = '';
      state.memory.sourceRegistry = Array.isArray(data.entries) ? data.entries : [];
      renderSourceRegistry();
      refreshSourceRegistryStaging();
    })
    .catch((err) => {
      state.memory.sourceRegistryFetchError = String(err && err.message ? err.message : err);
      state.memory.sourceRegistry = [];
      renderSourceRegistry();
      console.error(err);
    });
}

function saveSourceRegistryEntry() {
  const payload = {
    source_id: document.getElementById('sourceRegistryID').value.trim(),
    url: document.getElementById('sourceRegistryURL').value.trim(),
    kind: document.getElementById('sourceRegistryKind').value,
    trust_score: Number(document.getElementById('sourceRegistryTrust').value || '0.5'),
    fetch_interval_sec: Number(document.getElementById('sourceRegistryInterval').value || '3600'),
    license_note: document.getElementById('sourceRegistryLicense').value.trim() || 'manual registration',
    enabled: document.getElementById('sourceRegistryEnabled').checked,
    meta: {},
  };
  const namespace = document.getElementById('sourceRegistryNamespace').value.trim();
  if (namespace) payload.meta.namespace = namespace;
  fetch('/viewer/source-registry', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(payload),
  }).then((r) => {
    if (!r.ok) {
      return r.text().then((text) => {
        throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'source registry save failed'));
      });
    }
    return r.json();
  }).then(() => {
    setSourceRegistryActionStatus('Source Registry source saved', false);
    refreshSourceRegistry();
  }).catch((err) => {
    setSourceRegistryActionStatus('Source Registry save unavailable: ' + String(err && err.message ? err.message : err), true);
    console.error(err);
  });
}

function exportSourceRegistryYAML() {
  fetch('/viewer/source-registry?format=yaml')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'source registry export failed'));
        });
      }
      return r.text();
    })
    .then((text) => {
      if (sourceRegistryYAML) sourceRegistryYAML.value = text;
      setSourceRegistryActionStatus('Source Registry YAML exported', false);
    })
    .catch((err) => {
      setSourceRegistryActionStatus('Source Registry export unavailable: ' + String(err && err.message ? err.message : err), true);
      console.error(err);
    });
}

function importSourceRegistryYAML() {
  if (!sourceRegistryYAML || !sourceRegistryYAML.value.trim()) return;
  fetch('/viewer/source-registry?format=yaml', {
    method: 'POST',
    headers: {'Content-Type': 'application/x-yaml'},
    body: sourceRegistryYAML.value,
  }).then((r) => {
    if (!r.ok) {
      return r.text().then((text) => {
        throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'source registry import failed'));
      });
    }
    return r.json();
  }).then(() => {
    setSourceRegistryActionStatus('Source Registry YAML imported', false);
    refreshSourceRegistry();
  }).catch((err) => {
    setSourceRegistryActionStatus('Source Registry import unavailable: ' + String(err && err.message ? err.message : err), true);
    console.error(err);
  });
}

function refreshMemorySnapshot() {
  const params = new URLSearchParams();
  params.set('limit', '20');
  if (memoryNamespace && memoryNamespace.value.trim()) params.set('namespace', memoryNamespace.value.trim());
  if (memoryCategory && memoryCategory.value.trim()) params.set('category', memoryCategory.value.trim());
  if (memoryDomain && memoryDomain.value.trim()) params.set('domain', memoryDomain.value.trim());
  fetch('/viewer/memory/snapshot?' + params.toString())
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'memory snapshot unavailable'));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.memory.memorySnapshotFetchError = '';
      state.memory.snapshot = data || {};
      renderMemorySnapshot();
      refreshMemoryLayers();
      refreshMemoryEvents();
      refreshMemoryRecallPack();
      refreshUserMemory();
      refreshKnowledgeMemoryLedger();
      refreshSourceRegistry();
      refreshDomainGraphAssertions();
    })
    .catch((err) => {
      state.memory.memorySnapshotFetchError = String(err && err.message ? err.message : err);
      state.memory.snapshot = {memory: [], news: [], digests: [], knowledge: []};
      renderMemorySnapshot();
      console.error(err);
    });
}

function postMemoryAction(url, payload) {
  return fetch(url, {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(payload),
  }).then((r) => {
    if (!r.ok) {
      return r.text().then((text) => {
        throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'memory action failed'));
      });
    }
    return r.json();
  }).then(() => {
    state.memory.memoryActionError = '';
    refreshMemorySnapshot();
  }).catch((err) => {
    state.memory.memoryActionError = String(err && err.message ? err.message : err);
    renderMemorySnapshot();
    console.error(err);
  });
}

function setMemoryState(id, memoryState) {
  if (!id) return;
  postMemoryAction('/viewer/memory/state', {id, memory_state: memoryState});
}

function memoryPromotePayload(id) {
  const targetKind = memoryPromoteKind ? memoryPromoteKind.value.trim() : '';
  const targetID = memoryPromoteID ? memoryPromoteID.value.trim() : '';
  if (!id || !targetKind || !targetID) return null;
  return {
    id,
    target_kind: targetKind,
    target_id: targetID,
    promoted_by: 'viewer',
  };
}

function promoteMemory(id) {
  const payload = memoryPromotePayload(id);
  if (!payload) return;
  postMemoryAction('/viewer/memory/promote', payload);
}

function setUserMemoryState(id, memoryState) {
  if (!id) return;
  postMemoryAction('/viewer/memory/user/state', {id, state: memoryState, reason: 'viewer'});
}

function forgetUserMemory(id) {
  if (!id) return;
  postMemoryAction('/viewer/memory/user/forget', {id, reason: 'viewer'});
}

function renderRecallTraces() {
  const body = document.getElementById('recallTraceBody');
  if (!body) return;
  const fetchError = String(state.memory.recallTraceFetchError || '');
  const traces = Array.isArray(state.memory.traces) ? state.memory.traces : [];
  body.innerHTML = '';
  if (fetchError) {
    body.innerHTML = '<tr><td colspan="12" class="small">Recall traces unavailable: ' + esc(fetchError) + '</td></tr>';
    return;
  }
  if (traces.length === 0) {
    body.innerHTML = '<tr><td colspan="12" class="small">No recall traces yet</td></tr>';
    return;
  }
  traces.forEach((trace) => {
    const items = Array.isArray(trace.Items || trace.items) ? (trace.Items || trace.items) : [];
    if (items.length === 0) {
      const tr = document.createElement('tr');
      tr.innerHTML =
        '<td class="code">' + esc(trace.ResponseID || trace.response_id || '-') + '</td>' +
        '<td>' + esc(trace.Role || trace.role || '-') + '</td>' +
        '<td colspan="9" class="small">No referenced recall items</td>' +
        '<td>' + esc(fdt(trace.CreatedAt || trace.created_at)) + '</td>';
      body.appendChild(tr);
      return;
    }
    items.forEach((item) => {
      const urls = item.SourceURLs || item.source_urls || [];
      const status = recallTraceItemStatus(item);
      const section = item.PromptSection || item.prompt_section || '-';
      const tokenCount = item.TokenCount ?? item.token_count ?? '-';
      const tr = document.createElement('tr');
      tr.innerHTML =
        '<td class="code">' + esc(trace.ResponseID || trace.response_id || '-') + '</td>' +
        '<td>' + esc(trace.Role || trace.role || '-') + '</td>' +
        '<td>' + esc(item.Layer || item.layer || '-') + '</td>' +
        '<td>' + esc(item.Kind || item.kind || '-') + '</td>' +
        '<td>' + esc(status) + '</td>' +
        '<td class="code">' + esc(section) + '</td>' +
        '<td>' + esc(String(tokenCount)) + '</td>' +
        '<td>' + esc(recallTraceWarning(item, status)) + '</td>' +
        '<td>' + esc(short(item.Reason || item.reason || '-', 140)) + '</td>' +
        '<td>' + esc(short(item.Summary || item.summary || item.Query || item.query || '-', 180)) + '</td>' +
        '<td class="code">' + esc(short(Array.isArray(urls) ? urls.join(', ') : String(urls || '-'), 100)) + '</td>' +
        '<td>' + esc(fdt(trace.CreatedAt || trace.created_at)) + '</td>';
      body.appendChild(tr);
    });
  });
}

function recallTraceItemStatus(item) {
  const status = String(item.Status || item.status || '').trim();
  if (status) return status;
  const decision = String(item.Decision || item.decision || '').trim();
  if (decision === 'included') return 'injected';
  if (decision === 'rejected') return 'filtered';
  return decision || '-';
}

function recallTraceWarning(item, status) {
  const text = [
    status,
    item.Reason || item.reason || '',
    item.Sensitivity || item.sensitivity || '',
  ].join(' ').toLowerCase();
  if (text.includes('superseded')) return 'superseded';
  if (text.includes('stale') || text.includes('decayed')) return 'stale';
  if (text.includes('sensitive')) return 'sensitive';
  if (text.includes('candidate') || text.includes('staging') || text.includes('raw')) return 'unpromoted';
  if (text.includes('budget')) return 'budget';
  if (String(status || '').startsWith('filtered_')) return 'filtered';
  return '';
}

function refreshRecallTraces() {
  const params = new URLSearchParams();
  params.set('limit', '20');
  fetch('/viewer/recall/traces?' + params.toString())
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'recall traces unavailable'));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.memory.recallTraceFetchError = '';
      state.memory.traces = Array.isArray(data.items) ? data.items : [];
      renderRecallTraces();
      renderNewsPackPanel();
    })
    .catch((err) => {
      state.memory.recallTraceFetchError = String(err && err.message ? err.message : err);
      state.memory.traces = [];
      renderRecallTraces();
      renderNewsPackPanel();
      console.error(err);
    });
}
