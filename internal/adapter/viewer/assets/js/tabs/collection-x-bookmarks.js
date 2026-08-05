// Read-only X Bookmark projection within the Information Collection tab.
const xBookmarkViewState = {
  page: null,
  loading: false,
  limit: 12,
  offset: 0,
  searchTimer: null,
};

const xBookmarkMajorLabels = {
  ai: 'AI・モデル', creative: '画像・映像・音声・デザイン', engineering: '開発・Tool・運用',
  finance: '株式・金融調査', research: '調査・時系列情報', business: '事業・収益化',
  learning: '教育・説明資料', general: 'その他の参照資料',
};

const xBookmarkMinorLabels = {
  ai_tip: 'AI Tips', llm_catalog: 'LLMカタログ', agent_automation: 'Agent自動化',
  image_prompt: '画像prompt', creative_reference: '制作リファレンス', video_audio_recipe: '映像・音声レシピ',
  code_recipe: 'コードレシピ', tool_catalog: 'Toolカタログ', workflow_runbook: '運用手順', security_advisory: 'セキュリティ',
  equity_research: '株式調査', research_digest: '調査資料', news_watch: 'ニュース監視',
  business_idea: '事業アイデア', education_reference: '学習資料', general_reference: '一般資料',
};

function xBookmarkEscape(value) {
  return String(value == null ? '' : value)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function xBookmarkDateTime(value) {
  if (!value) return '-';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  return new Intl.DateTimeFormat('ja-JP', {
    timeZone: 'Asia/Tokyo', year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit',
  }).format(date) + ' JST';
}

function xBookmarkSafeURL(value) {
  try {
    const parsed = new URL(String(value || ''), window.location.origin);
    return parsed.protocol === 'http:' || parsed.protocol === 'https:' ? parsed.href : '';
  } catch (_) {
    return '';
  }
}

function xBookmarkSetText(id, value) {
  const element = document.getElementById(id);
  if (element) element.textContent = String(value);
}

function xBookmarkLabel(labels, value) {
  return labels[String(value || '')] || String(value || '未分類');
}

function xBookmarkSortedCounts(counts) {
  return Object.entries(counts || {}).sort((left, right) => right[1] - left[1] || left[0].localeCompare(right[0]));
}

function xBookmarkSelectOptions(element, counts, labels, emptyLabel) {
  if (!element) return;
  const selected = element.value;
  const entries = xBookmarkSortedCounts(counts);
  element.innerHTML = '<option value="">' + xBookmarkEscape(emptyLabel) + '</option>' + entries.map(([value, count]) =>
    '<option value="' + xBookmarkEscape(value) + '">' + xBookmarkEscape(xBookmarkLabel(labels, value)) + ' (' + Number(count || 0) + ')</option>'
  ).join('');
  element.value = entries.some(([value]) => value === selected) ? selected : '';
}

function xBookmarkAuthor(item) {
  const username = String(item.author_username || '').trim();
  const name = String(item.author_name || '').trim();
  if (name && username) return name + ' (@' + username.replace(/^@/, '') + ')';
  if (username) return '@' + username.replace(/^@/, '');
  return name || '投稿者情報なし';
}

function xBookmarkReferenceStatus(reference) {
  const labels = {
    content_fetched: '本文取得済み', legacy_vault_reference: '本文未取得', url_only: '本文未取得',
    not_attempted: '未取得', fetch_failed: '取得失敗', blocked_private_network: '安全制約で取得不可',
    fetch_skipped_limit: '取得待ち', quoted_post: '引用投稿',
  };
  return labels[String(reference && reference.capture_status || '')] || String(reference && reference.capture_status || '状態不明');
}

function xBookmarkReferenceAuthor(reference) {
  const username = String(reference && reference.author_username || '').trim().replace(/^@/, '');
  const name = String(reference && reference.author_name || '').trim();
  if (name && username) return name + ' (@' + username + ')';
  if (username) return '@' + username;
  return name;
}

function xBookmarkReferenceCard(reference, index) {
  const kind = String(reference && reference.kind || 'external_url');
  const target = xBookmarkSafeURL(reference && (reference.resolved_url || reference.status_url || reference.url));
  const fallbackTitle = kind === 'x_post' ? (xBookmarkReferenceAuthor(reference) || '引用X投稿') : (target || 'リンク先');
  const title = xBookmarkEscape(reference && (reference.page_title || reference.display_text) || fallbackTitle);
  const titleHTML = target
    ? '<a href="' + xBookmarkEscape(target) + '" target="_blank" rel="noopener noreferrer">' + title + '</a>'
    : title;
  const description = String(reference && (reference.page_description || reference.preview_text) || '').trim();
  const body = String(reference && (reference.body_text || reference.text) || '').trim();
  const error = String(reference && reference.fetch_error || '').trim();
  const bodyHTML = body
    ? '<details class="collection-x-bookmark-reference-body"><summary>リンク先本文を表示</summary><pre>' + xBookmarkEscape(body) + '</pre></details>'
    : '<p class="collection-x-bookmark-reference-empty">本文未取得' + (error ? '（' + xBookmarkEscape(error) + '）' : '') + '</p>';
  return '<article class="collection-x-bookmark-reference">' +
    '<div class="collection-x-bookmark-reference-head"><span>参照 ' + Number(index + 1) + '</span><span>' + xBookmarkEscape(xBookmarkReferenceStatus(reference)) + '</span></div>' +
    '<h5>' + titleHTML + '</h5>' +
    (description ? '<p class="collection-x-bookmark-reference-description">' + xBookmarkEscape(description) + '</p>' : '') +
    bodyHTML + '</article>';
}

function xBookmarkReferences(item) {
  const references = Array.isArray(item.references) ? item.references : [];
  if (!references.length) return '';
  return '<details class="collection-x-bookmark-references"><summary>リンク先情報 ' + references.length + '件</summary>' +
    '<div class="collection-x-bookmark-reference-list">' + references.map(xBookmarkReferenceCard).join('') + '</div></details>';
}

function xBookmarkCard(item) {
  const sourceURL = xBookmarkSafeURL(item.source_url);
  const title = xBookmarkEscape(item.title || '無題');
  const titleHTML = sourceURL
    ? '<a href="' + xBookmarkEscape(sourceURL) + '" target="_blank" rel="noopener noreferrer">' + title + '</a>'
    : title;
  const tags = Array.isArray(item.use_case_tags) ? item.use_case_tags : [];
  const tagHTML = tags.length ? tags.map((tag) =>
    '<span class="collection-x-bookmark-tag"><b>' + xBookmarkEscape(xBookmarkLabel(xBookmarkMajorLabels, tag.major)) + '</b>' +
    '<span>' + xBookmarkEscape(xBookmarkLabel(xBookmarkMinorLabels, tag.minor)) + '</span>' +
    '<small>' + Math.round(Number(tag.confidence || 0) * 100) + '%</small></span>'
  ).join('') : '<span class="collection-x-bookmark-tag warn">未分類</span>';
  const review = item.needs_review
    ? '<span class="collection-x-bookmark-review warn">要確認</span>'
    : '<span class="collection-x-bookmark-review">分類済み</span>';
  const evidence = tags.flatMap((tag) => Array.isArray(tag.evidence) ? tag.evidence : []).slice(0, 8);
  const evidenceHTML = evidence.length
    ? '<div class="collection-x-bookmark-evidence"><strong>分類根拠</strong><span>' + xBookmarkEscape(evidence.join(' / ')) + '</span></div>'
    : '';
  return '<article class="collection-x-bookmark-card">' +
    '<div class="collection-x-bookmark-card-meta">' + review +
      '<span>' + xBookmarkEscape(item.validation_status || 'pending') + '</span>' +
      '<span>' + xBookmarkEscape(xBookmarkAuthor(item)) + '</span></div>' +
    '<h4>' + titleHTML + '</h4>' +
    '<div class="collection-x-bookmark-tags">' + tagHTML + '</div>' +
    evidenceHTML +
    xBookmarkReferences(item) +
    '<details class="collection-item-details"><summary>本文を表示</summary><pre>' + xBookmarkEscape(item.raw_text || '本文はありません。') + '</pre></details>' +
    '<div class="collection-x-bookmark-foot"><span>画像・メディア ' + Number(item.media_count || 0) + '件</span>' +
      '<span>参照リンク ' + Number(item.reference_count || 0) + '件</span><span>更新 ' + xBookmarkEscape(xBookmarkDateTime(item.updated_at)) + '</span></div>' +
    '</article>';
}

function renderXBookmarkData() {
  const page = xBookmarkViewState.page || {items: [], total: 0, limit: xBookmarkViewState.limit, offset: xBookmarkViewState.offset, summary: {total: 0, needs_review: 0, major_counts: {}, minor_counts: {}}};
  const items = Array.isArray(page.items) ? page.items : [];
  const summary = page.summary || {};
  xBookmarkSetText('collectionXBookmarkTotal', summary.total || 0);
  xBookmarkSetText('collectionXBookmarkReviewCount', summary.needs_review || 0);
  xBookmarkSetText('collectionXBookmarkVisibleCount', items.length);
  xBookmarkSetText('collectionXBookmarkStatus', xBookmarkViewState.loading ? '読込中' : '読み取り専用');
  xBookmarkSelectOptions(document.getElementById('collectionXBookmarkMajorFilter'), summary.major_counts, xBookmarkMajorLabels, 'すべての大分類');
  xBookmarkSelectOptions(document.getElementById('collectionXBookmarkMinorFilter'), summary.minor_counts, xBookmarkMinorLabels, 'すべての中分類');

  const list = document.getElementById('collectionXBookmarkItems');
  if (list) {
    list.innerHTML = items.length
      ? items.map(xBookmarkCard).join('')
      : '<div class="daily-desk-card daily-desk-muted">該当するX Bookmarkはありません。</div>';
  }
  const limit = Number(page.limit || xBookmarkViewState.limit);
  const offset = Number(page.offset || 0);
  const total = Number(page.total || 0);
  const pageNumber = total ? Math.floor(offset / limit) + 1 : 0;
  const pageCount = total ? Math.ceil(total / limit) : 0;
  xBookmarkSetText('collectionXBookmarkPage', pageNumber + ' / ' + pageCount + '（該当 ' + total + '件）');
  const prev = document.getElementById('collectionXBookmarkPrev');
  const next = document.getElementById('collectionXBookmarkNext');
  if (prev) prev.disabled = xBookmarkViewState.loading || offset <= 0;
  if (next) next.disabled = xBookmarkViewState.loading || offset + limit >= total;
}

function refreshXBookmarkData(options) {
  if (xBookmarkViewState.loading) return;
  if (options && options.resetOffset) xBookmarkViewState.offset = 0;
  const major = document.getElementById('collectionXBookmarkMajorFilter');
  const minor = document.getElementById('collectionXBookmarkMinorFilter');
  const review = document.getElementById('collectionXBookmarkReviewFilter');
  const search = document.getElementById('collectionXBookmarkSearch');
  const params = new URLSearchParams({limit: String(xBookmarkViewState.limit), offset: String(xBookmarkViewState.offset)});
  if (major && major.value) params.set('major', major.value);
  if (minor && minor.value) params.set('minor', minor.value);
  if (review && review.value) params.set('review', review.value);
  if (search && search.value.trim()) params.set('q', search.value.trim());
  const endpoint = '/viewer/source-registry?action=x-bookmarks';
  const error = document.getElementById('collectionXBookmarkError');
  if (error) error.hidden = true;
  xBookmarkViewState.loading = true;
  renderXBookmarkData();
  fetch(endpoint + '&' + params.toString())
    .then((response) => {
      if (!response.ok) throw new Error('HTTP ' + response.status + ' ' + response.statusText);
      return response.json();
    })
    .then((payload) => { xBookmarkViewState.page = payload; })
    .catch((requestError) => {
      if (error) {
        error.textContent = 'X Bookmarkを取得できません: ' + String(requestError && requestError.message || requestError);
        error.hidden = false;
      }
    })
    .finally(() => {
      xBookmarkViewState.loading = false;
      renderXBookmarkData();
    });
}

['collectionXBookmarkMajorFilter', 'collectionXBookmarkMinorFilter', 'collectionXBookmarkReviewFilter'].forEach((id) => {
  const element = document.getElementById(id);
  if (element) element.addEventListener('change', () => refreshXBookmarkData({resetOffset: true}));
});
const xBookmarkSearch = document.getElementById('collectionXBookmarkSearch');
if (xBookmarkSearch) xBookmarkSearch.addEventListener('input', () => {
  window.clearTimeout(xBookmarkViewState.searchTimer);
  xBookmarkViewState.searchTimer = window.setTimeout(() => refreshXBookmarkData({resetOffset: true}), 300);
});
const xBookmarkPrev = document.getElementById('collectionXBookmarkPrev');
if (xBookmarkPrev) xBookmarkPrev.addEventListener('click', () => {
  xBookmarkViewState.offset = Math.max(0, xBookmarkViewState.offset - xBookmarkViewState.limit);
  refreshXBookmarkData();
});
const xBookmarkNext = document.getElementById('collectionXBookmarkNext');
if (xBookmarkNext) xBookmarkNext.addEventListener('click', () => {
  xBookmarkViewState.offset += xBookmarkViewState.limit;
  refreshXBookmarkData();
});

if (document.body && document.body.dataset.xBookmarkAutoload === 'true') {
  refreshXBookmarkData();
}
