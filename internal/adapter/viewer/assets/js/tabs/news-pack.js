// News Pack tab module: news source inspection and recall usage links.
function newsItems() {
  const snap = state.memory.snapshot || {};
  return Array.isArray(snap.news) ? snap.news : [];
}

function newsDigests() {
  const snap = state.memory.snapshot || {};
  return Array.isArray(snap.digests) ? snap.digests : [];
}

function newsSourceURL(item) {
  return String((item && (item.SourceURL || item.source_url)) || '').trim();
}

function newsSourceID(item) {
  return String((item && (item.SourceID || item.source_id)) || '').trim();
}

function newsSummary(item) {
  return String((item && (item.SummaryDraft || item.summary_draft || item.RawText || item.raw_text)) || '-');
}

function newsKeywords(item) {
  const raw = item && (item.Keywords || item.keywords);
  return Array.isArray(raw) ? raw : [];
}

function newsUsageMatches(item) {
  const url = newsSourceURL(item);
  const sourceID = newsSourceID(item);
  if (!url && !sourceID) return [];
  const traces = Array.isArray(state.memory.traces) ? state.memory.traces : [];
  const matches = [];
  traces.forEach((trace) => {
    const items = Array.isArray(trace.Items || trace.items) ? (trace.Items || trace.items) : [];
    items.forEach((ri) => {
      const urls = ri.SourceURLs || ri.source_urls || [];
      const urlText = Array.isArray(urls) ? urls.join(' ') : String(urls || '');
      const summary = String(ri.Summary || ri.summary || '');
      if ((url && urlText.indexOf(url) >= 0) || (sourceID && summary.indexOf(sourceID) >= 0)) {
        matches.push({trace, item: ri, source: url || sourceID});
      }
    });
  });
  return matches;
}

function newsUsageCount(item) {
  return newsUsageMatches(item).length;
}

function newsRelatedMemoryMatches(item) {
  const related = [];
  newsUsageMatches(item).forEach((match) => {
    const traceItems = Array.isArray(match.trace.Items || match.trace.items) ? (match.trace.Items || match.trace.items) : [];
    traceItems.forEach((ri) => {
      const kind = String(ri.Kind || ri.kind || '');
      if (kind === 'search_cache') return;
      related.push({
        layer: ri.Layer || ri.layer || '-',
        kind: kind || '-',
        reason: ri.Reason || ri.reason || ri.Decision || ri.decision || '-',
        summary: ri.Summary || ri.summary || '-',
      });
    });
  });
  return related;
}

function renderNewsPackPanel() {
  const fetchError = String(state.memory.newsPackFetchError || '');
  const news = newsItems();
  const digests = newsDigests();
  const body = document.getElementById('newsPackPanelBody');
  const digestBody = document.getElementById('newsDigestPanelBody');
  const detail = document.getElementById('newsPackDetail');
  const usageBody = document.getElementById('newsUsageBody');
  const relatedBody = document.getElementById('newsRelatedMemoryBody');
  const newsCount = document.getElementById('newsPackPanelCount');
  const digestCount = document.getElementById('newsDigestPanelCount');
  const usageCountEl = document.getElementById('newsUsageCount');
  if (newsCount) newsCount.textContent = String(news.length);
  if (digestCount) digestCount.textContent = String(digests.length);
  const totalUsage = news.reduce((sum, item) => sum + newsUsageCount(item), 0);
  if (usageCountEl) usageCountEl.textContent = String(totalUsage);

  if (fetchError) {
    if (newsCount) newsCount.textContent = '0';
    if (digestCount) digestCount.textContent = '0';
    if (usageCountEl) usageCountEl.textContent = '0';
    state.memory.selectedNewsIndex = 0;
    if (body) body.innerHTML = '<tr><td colspan="5" class="small">News Pack unavailable: ' + esc(fetchError) + '</td></tr>';
    if (digestBody) digestBody.innerHTML = '<tr><td colspan="5" class="small">News digests unavailable: ' + esc(fetchError) + '</td></tr>';
    if (detail) detail.innerHTML = '<h4>Source Detail</h4><div class="small">News Pack unavailable: ' + esc(fetchError) + '</div>';
    if (usageBody) usageBody.innerHTML = '<tr><td colspan="4" class="small">News recall usage unavailable: ' + esc(fetchError) + '</td></tr>';
    if (relatedBody) relatedBody.innerHTML = '<tr><td colspan="4" class="small">News related memory unavailable: ' + esc(fetchError) + '</td></tr>';
    return;
  }

  if (state.memory.selectedNewsIndex < 0 || state.memory.selectedNewsIndex >= news.length) {
    state.memory.selectedNewsIndex = 0;
  }
  const selected = news[state.memory.selectedNewsIndex] || null;

  if (body) {
    body.innerHTML = '';
    if (news.length === 0) {
      body.innerHTML = '<tr><td colspan="5" class="small">No news pack items</td></tr>';
    } else {
      news.forEach((item, idx) => {
        const tr = document.createElement('tr');
        if (idx === state.memory.selectedNewsIndex) tr.classList.add('evi-selected');
        const url = newsSourceURL(item);
        const source = url || newsSourceID(item) || '-';
        tr.innerHTML =
          '<td>' + esc(fdt(item.PublishedAt || item.published_at || item.FetchedAt || item.fetched_at)) + '</td>' +
          '<td>' + esc(item.Category || item.category || '-') + '</td>' +
          '<td class="code">' + esc(short(source, 90)) + '</td>' +
          '<td>' + esc(short(newsSummary(item), 220)) + '</td>' +
          '<td><button class="ctl-btn" onclick="selectNewsPackItem(' + idx + ')">' + esc(String(newsUsageCount(item))) + '</button></td>';
        tr.addEventListener('click', (evt) => {
          if (evt.target && evt.target.tagName === 'BUTTON') return;
          selectNewsPackItem(idx);
        });
        body.appendChild(tr);
      });
    }
  }

  if (detail) {
    if (!selected) {
      detail.innerHTML = '<h4>Source Detail</h4><div class="small">No news selected</div>';
    } else {
      const url = newsSourceURL(selected);
      const source = url || newsSourceID(selected) || '-';
      const link = url ? '<a href="' + esc(url) + '" target="_blank" rel="noopener noreferrer">' + esc(short(url, 130)) + '</a>' : esc(source);
      detail.innerHTML =
        '<h4>' + esc(short(newsSummary(selected), 90)) + '</h4>' +
        '<div class="row"><span>Category</span><span>' + esc(selected.Category || selected.category || '-') + '</span></div>' +
        '<div class="row"><span>Source</span><span class="code">' + link + '</span></div>' +
        '<div class="row"><span>Published</span><span>' + esc(fdt(selected.PublishedAt || selected.published_at || selected.FetchedAt || selected.fetched_at)) + '</span></div>' +
        '<div class="row"><span>Source ID</span><span class="code">' + esc(newsSourceID(selected) || '-') + '</span></div>' +
        '<div class="row"><span>Keywords</span><span>' + esc(newsKeywords(selected).join(', ') || '-') + '</span></div>' +
        '<div class="ops-sub">' + esc(newsSummary(selected)) + '</div>';
    }
  }

  if (digestBody) {
    digestBody.innerHTML = '';
    if (digests.length === 0) {
      digestBody.innerHTML = '<tr><td colspan="5" class="small">No daily digests</td></tr>';
    } else {
      digests.forEach((d) => {
        const ids = d.NewsIDs || d.news_ids || [];
        const tr = document.createElement('tr');
        tr.innerHTML =
          '<td>' + esc(d.DigestSlot || d.digest_slot || '-') + '</td>' +
          '<td>' + esc(d.Category || d.category || '-') + '</td>' +
          '<td>' + esc(short(d.DigestText || d.digest_text || '-', 260)) + '</td>' +
          '<td class="code">' + esc(short(Array.isArray(ids) ? ids.join(', ') : String(ids || '-'), 120)) + '</td>' +
          '<td>' + esc(fdt(d.CreatedAt || d.created_at)) + '</td>';
        digestBody.appendChild(tr);
      });
    }
  }

  if (usageBody) {
    usageBody.innerHTML = '';
    const matches = selected ? newsUsageMatches(selected) : [];
    if (matches.length === 0) {
      usageBody.innerHTML = '<tr><td colspan="4" class="small">No recall usage for selected news</td></tr>';
    } else {
      matches.forEach((match) => {
        const tr = document.createElement('tr');
        tr.innerHTML =
          '<td class="code">' + esc(match.trace.ResponseID || match.trace.response_id || '-') + '</td>' +
          '<td>' + esc(match.trace.Role || match.trace.role || '-') + '</td>' +
          '<td class="code">' + esc(short(match.source || '-', 120)) + '</td>' +
          '<td>' + esc(fdt(match.trace.CreatedAt || match.trace.created_at)) + '</td>';
        usageBody.appendChild(tr);
      });
    }
  }
  if (relatedBody) {
    relatedBody.innerHTML = '';
    const related = selected ? newsRelatedMemoryMatches(selected) : [];
    if (related.length === 0) {
      relatedBody.innerHTML = '<tr><td colspan="4" class="small">No related memory for selected news</td></tr>';
    } else {
      related.forEach((item) => {
        const tr = document.createElement('tr');
        tr.innerHTML =
          '<td class="code">' + esc(item.layer) + '</td>' +
          '<td>' + esc(item.kind) + '</td>' +
          '<td>' + esc(short(item.reason, 160)) + '</td>' +
          '<td>' + esc(short(item.summary, 220)) + '</td>';
        relatedBody.appendChild(tr);
      });
    }
  }
}

function selectNewsPackItem(idx) {
  state.memory.selectedNewsIndex = idx;
  renderNewsPackPanel();
}

function refreshNewsPack() {
  if (newsPackCategory && memoryCategory) {
    memoryCategory.value = newsPackCategory.value.trim();
  }
  const params = new URLSearchParams();
  params.set('limit', '30');
  if (newsPackCategory && newsPackCategory.value.trim()) params.set('category', newsPackCategory.value.trim());
  fetch('/viewer/memory/snapshot?' + params.toString())
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'news pack unavailable'));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.memory.newsPackFetchError = '';
      state.memory.snapshot = data || {};
      renderMemorySnapshot();
      renderNewsPackPanel();
      refreshRecallTraces();
    })
    .catch((err) => {
      state.memory.newsPackFetchError = String(err && err.message ? err.message : err);
      state.memory.snapshot = Object.assign({}, state.memory.snapshot || {}, {news: [], digests: []});
      renderNewsPackPanel();
      console.error(err);
    });
}
