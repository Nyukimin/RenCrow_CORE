// Collection tab: read-only inspection of the IdleChat daily seed cache.
const collectionViewState = {
  collection: null,
  loading: false,
};

function collectionEscape(value) {
  return String(value == null ? '' : value)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function collectionDateTime(value) {
  if (!value) return '-';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  return new Intl.DateTimeFormat('ja-JP', {
    timeZone: 'Asia/Tokyo',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).format(date) + ' JST';
}

function collectionSafeURL(value) {
  try {
    const parsed = new URL(String(value || ''), window.location.origin);
    return parsed.protocol === 'http:' || parsed.protocol === 'https:' ? parsed.href : '';
  } catch (_) {
    return '';
  }
}

function collectionSortedEntries(counts) {
  return Object.entries(counts || {}).sort((left, right) => {
    if (right[1] !== left[1]) return right[1] - left[1];
    return left[0].localeCompare(right[0]);
  });
}

function collectionSetText(id, value) {
  const element = document.getElementById(id);
  if (element) element.textContent = String(value);
}

function collectionStatusLabel(value) {
  const labels = {
    empty: '未収集', pending: '待機中', enriching: '処理中', ready: '完了', partial: '一部完了', fallback: '確認不能',
  };
  return labels[String(value || '')] || String(value || '未収集');
}

function renderCollectionData() {
  const collection = collectionViewState.collection || {
    status: 'empty', enrichment_status: '', items: [], sources: [], tools: [], category_counts: {}, source_counts: {},
  };
  const items = Array.isArray(collection.items) ? collection.items : [];
  const sources = Array.isArray(collection.sources) ? collection.sources : [];
  const categoryEntries = collectionSortedEntries(collection.category_counts);
  const sourceFilter = document.getElementById('collectionSourceFilter');
  const categoryFilter = document.getElementById('collectionCategoryFilter');
  const query = String(sourceFilter && sourceFilter.value || '').trim().toLowerCase();
  const category = String(categoryFilter && categoryFilter.value || '').trim();
  const visibleItems = items.filter((item) => {
    if (category && String(item.category || '') !== category) return false;
    if (!query) return true;
    const termText = (Array.isArray(item.term_notes) ? item.term_notes : [])
      .map((note) => String(note.term || '') + ' ' + String(note.explanation || '')).join(' ');
    return [item.title, item.source, item.source_type, item.category, termText, item.summary, item.perspective]
      .some((value) => String(value || '').toLowerCase().includes(query));
  });

  const status = document.getElementById('collectionStatus');
  if (status) {
    status.textContent = collectionViewState.loading ? '読込中' : collectionStatusLabel(collection.status);
    status.classList.toggle('warn', !collectionViewState.loading && collection.status !== 'ready');
  }
  collectionSetText('collectionFetchedAt', collectionDateTime(collection.fetched_at));
  collectionSetText('collectionNextRunAt', collectionDateTime(collection.next_run_at));
  collectionSetText('collectionSchedule', (collection.schedule || '04:00') + ' ' + (collection.timezone || 'JST'));
  collectionSetText('collectionEnrichmentStatus', collectionStatusLabel(collection.enrichment_status || 'pending'));
  collectionSetText('collectionEnrichedAt', collectionDateTime(collection.enriched_at));
  collectionSetText('collectionTotal', collection.total || 0);
  collectionSetText('collectionWikipediaCount', collection.wikipedia_count || 0);
  collectionSetText('collectionCategoryCount', categoryEntries.length);
  collectionSetText('collectionSourceCount', sources.filter((source) => source.enabled).length);
  collectionSetText('collectionVisibleCount', String(visibleItems.length) + '件表示');
	collectionSetText('collectionSkillID', collection.skill_id || '-');

  if (categoryFilter) {
    const selected = categoryFilter.value;
    categoryFilter.innerHTML = '<option value="">すべてのカテゴリ</option>' + categoryEntries
      .map(([name, count]) => '<option value="' + collectionEscape(name) + '">' + collectionEscape(name) + ' (' + count + ')</option>')
      .join('');
    categoryFilter.value = categoryEntries.some(([name]) => name === selected) ? selected : '';
  }

  const summary = document.getElementById('collectionCategorySummary');
  if (summary) {
    summary.innerHTML = categoryEntries.length
      ? categoryEntries.map(([name, count]) => '<button class="collection-category-chip" type="button" data-collection-category="' + collectionEscape(name) + '"><span>' + collectionEscape(name) + '</span><strong>' + count + '</strong></button>').join('')
      : '<span class="daily-desk-muted">カテゴリ別集計はまだありません。</span>';
    summary.querySelectorAll('[data-collection-category]').forEach((button) => {
      button.addEventListener('click', () => {
        if (categoryFilter) categoryFilter.value = button.dataset.collectionCategory || '';
        renderCollectionData();
      });
    });
  }

  const itemList = document.getElementById('collectionItems');
  if (itemList) {
    itemList.innerHTML = visibleItems.length ? visibleItems.map((item) => {
      const url = collectionSafeURL(item.url);
      const title = collectionEscape(item.title || '無題');
		const termNotes = Array.isArray(item.term_notes) ? item.term_notes : [];
		const termNotesHTML = termNotes.length
		  ? '<ul class="collection-term-notes">' + termNotes.map((note) => {
		    const sourceURL = collectionSafeURL(note.source_url);
		    const source = sourceURL
		      ? ' <a href="' + collectionEscape(sourceURL) + '" target="_blank" rel="noopener noreferrer">参照元</a>'
		      : '';
		    const statusLabel = note.status === 'confirmed' ? '検索確認済み'
		      : note.status === 'contextual' ? '本文文脈'
		      : note.status === 'unresolved' ? '未解決' : '確認不能';
		    return '<li><strong>' + collectionEscape(note.term || '用語') + '</strong><span>' + collectionEscape(note.explanation || '説明はありません。') + '</span><small>' + statusLabel + source + '</small></li>';
		  }).join('') + '</ul>'
		  : '<p>補足が必要な用語はありません。</p>';
      const linkedTitle = url
        ? '<a href="' + collectionEscape(url) + '" target="_blank" rel="noopener noreferrer">' + title + '</a>'
        : title;
      return '<article class="collection-item">' +
        '<div class="collection-item-meta"><span>' + collectionEscape(item.category || '未分類') + '</span><span>' + collectionEscape(item.source_type || '種別不明') + '</span></div>' +
        '<h4>' + linkedTitle + '</h4>' +
		'<div class="collection-item-source">' + collectionEscape(item.source || '取得元不明') + '</div>' +
		'<section class="collection-annotation collection-terms"><strong>用語補足</strong>' + termNotesHTML + '</section>' +
        '<section class="collection-annotation collection-summary"><strong>サマリ</strong><p>' + collectionEscape(item.summary || 'サマリはまだありません。') + '</p></section>' +
		'<section class="collection-annotation collection-perspective"><strong>Shiroの見解</strong><p>' + collectionEscape(item.perspective || '見解はまだありません。') + '</p></section>' +
        '</article>';
    }).join('') : '<div class="daily-desk-card daily-desk-muted">該当する収集項目はありません。</div>';
  }

  const tools = document.getElementById('collectionTools');
  if (tools) {
    tools.innerHTML = (Array.isArray(collection.tools) ? collection.tools : [])
      .map((tool) => '<span class="desk-pill">' + collectionEscape(tool) + '</span>').join('');
  }
  const sourceList = document.getElementById('collectionSources');
  if (sourceList) {
    sourceList.innerHTML = sources.length ? sources.map((source) => {
      const target = source.query || source.url || '-';
      const sourceURL = collectionSafeURL(source.url);
      const targetHTML = sourceURL
        ? '<a href="' + collectionEscape(sourceURL) + '" target="_blank" rel="noopener noreferrer">' + collectionEscape(target) + '</a>'
        : collectionEscape(target);
      return '<div class="collection-source' + (source.enabled ? '' : ' disabled') + '">' +
        '<div><strong>' + collectionEscape(source.name) + '</strong><span>' + collectionEscape(source.category) + ' · ' + collectionEscape(source.kind) + '</span></div>' +
        '<div class="collection-source-target">' + targetHTML + '</div>' +
        '<span class="desk-pill' + (source.enabled ? '' : ' warn') + '">' + (source.enabled ? '有効' : '無効') + (source.limit ? ' / 上限' + source.limit + '件' : '') + '</span>' +
        '</div>';
    }).join('') : '<div class="daily-desk-muted">取得先は設定されていません。</div>';
  }
}

function refreshCollectionData() {
  if (collectionViewState.loading) return;
  collectionViewState.loading = true;
  const error = document.getElementById('collectionError');
  if (error) error.hidden = true;
  renderCollectionData();
  fetch('/viewer/idlechat/collection')
    .then((response) => {
      if (!response.ok) throw new Error('HTTP ' + response.status + ' ' + response.statusText);
      return response.json();
    })
    .then((payload) => {
      collectionViewState.collection = payload && payload.collection ? payload.collection : null;
    })
    .catch((requestError) => {
      if (error) {
		error.textContent = '収集情報を取得できません: ' + String(requestError && requestError.message || requestError);
        error.hidden = false;
      }
    })
    .finally(() => {
      collectionViewState.loading = false;
      renderCollectionData();
    });
}

const collectionRefreshButton = document.getElementById('collectionRefreshBtn');
const collectionCategoryFilter = document.getElementById('collectionCategoryFilter');
const collectionSourceFilter = document.getElementById('collectionSourceFilter');
if (collectionRefreshButton) collectionRefreshButton.addEventListener('click', refreshCollectionData);
if (collectionCategoryFilter) collectionCategoryFilter.addEventListener('change', renderCollectionData);
if (collectionSourceFilter) collectionSourceFilter.addEventListener('input', renderCollectionData);
