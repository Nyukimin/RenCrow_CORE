// Movie database tab: paged catalog view for eiga.com-derived SQLite data.
const movieDbState = {
  mode: 'movies',
  q: '',
  role: '',
  source: '',
  limit: 25,
  offset: 0,
  total: 0,
  selectedID: '',
  history: [],
  historyIndex: -1,
  searchTimer: null,
  fetchBusy: false,
};

function movieDbEl(id) {
  return document.getElementById(id);
}

function movieDbSetMode(mode) {
  movieDbState.mode = mode === 'people' ? 'people' : 'movies';
  movieDbState.offset = 0;
  movieDbState.selectedID = '';
  movieDbState.history = [];
  movieDbState.historyIndex = -1;
  movieDbClearDetail();
  movieDbSetDetailOpen(false);
  const moviesBtn = movieDbEl('movieDbModeMovies');
  const peopleBtn = movieDbEl('movieDbModePeople');
  const role = movieDbEl('movieDbRoleFilter');
  const source = movieDbEl('movieDbSourceFilter');
  if (moviesBtn) moviesBtn.classList.toggle('active', movieDbState.mode === 'movies');
  if (peopleBtn) peopleBtn.classList.toggle('active', movieDbState.mode === 'people');
  if (role) role.disabled = movieDbState.mode !== 'movies';
  if (source) source.disabled = movieDbState.mode !== 'movies';
  movieDbRefreshList();
}

function movieDbQueryParams(action) {
  const params = new URLSearchParams();
  params.set('action', action || movieDbState.mode);
  params.set('limit', String(movieDbState.limit));
  params.set('offset', String(movieDbState.offset));
  if (movieDbState.q) params.set('q', movieDbState.q);
  if (movieDbState.mode === 'movies') {
    if (movieDbState.role) params.set('role', movieDbState.role);
    if (movieDbState.source) params.set('source', movieDbState.source);
  } else {
    params.set('role', '出演');
  }
  return params;
}

function movieDbRefreshStats() {
  fetch('/viewer/movie-catalog?action=stats', {cache: 'no-store'})
    .then((r) => r.json())
    .then((data) => {
      const stats = data && data.stats ? data.stats : {};
      const status = movieDbEl('movieDbStatus');
      if (movieDbEl('movieDbMovieCount')) movieDbEl('movieDbMovieCount').textContent = String(stats.movies || 0);
      if (movieDbEl('movieDbPersonCount')) movieDbEl('movieDbPersonCount').textContent = String(stats.people || 0);
      if (movieDbEl('movieDbEdgeCount')) movieDbEl('movieDbEdgeCount').textContent = String(stats.movie_people || 0);
      if (status) status.textContent = data && data.available ? ('DB: ' + (data.db_path || '-')) : '映画データベースが見つかりません';
    })
    .catch((err) => {
      const status = movieDbEl('movieDbStatus');
      if (status) status.textContent = '映画データベースを読み込めません: ' + String(err && err.message ? err.message : err);
    });
}

function movieDbRefreshList() {
  const title = movieDbEl('movieDbListTitle');
  const rows = movieDbEl('movieDbRows');
  const detail = movieDbEl('movieDbDetail');
  if (title) title.textContent = movieDbState.mode === 'people' ? '俳優' : '映画';
  if (rows) rows.innerHTML = '<div class="daily-desk-muted">loading...</div>';
  if (detail && !movieDbState.selectedID) detail.textContent = '項目を選ぶと詳細を表示します。';
  const params = movieDbQueryParams(movieDbState.mode);
  fetch('/viewer/movie-catalog?' + params.toString(), {cache: 'no-store'})
    .then((r) => {
      if (!r.ok) return r.text().then((text) => { throw new Error(text || ('HTTP ' + String(r.status))); });
      return r.json();
    })
    .then((data) => {
      movieDbState.total = Number(data.total || 0);
      if (!data.available) {
        movieDbRenderUnavailable(data.error || 'movie catalog database not found');
        return;
      }
      const items = Array.isArray(data.items) ? data.items : [];
      movieDbRenderRows(items);
      movieDbRenderPageInfo();
      movieDbUpdatePager();
      movieDbUpdateHistoryButtons();
    })
    .catch((err) => {
      movieDbRenderUnavailable(String(err && err.message ? err.message : err));
    });
}

function movieDbClearDetail() {
  const detail = movieDbEl('movieDbDetail');
  if (detail) detail.textContent = '項目を選ぶと詳細を表示します。';
}

function movieDbRenderUnavailable(message) {
  const rows = movieDbEl('movieDbRows');
  const detail = movieDbEl('movieDbDetail');
  if (rows) rows.innerHTML = '<div class="daily-desk-muted">' + esc(message) + '</div>';
  if (detail) detail.textContent = '映画 DB を作成してから再読み込みしてください。';
  movieDbState.total = 0;
  movieDbRenderPageInfo();
  movieDbUpdatePager();
  movieDbUpdateHistoryButtons();
}

function movieDbRenderRows(items) {
  const rows = movieDbEl('movieDbRows');
  if (!rows) return;
  if (!items.length) {
    rows.innerHTML = '<div class="daily-desk-muted">該当する項目はありません。</div>';
    return;
  }
  rows.innerHTML = '<table class="movie-db-table"><thead>' + movieDbTableHeadHTML() + '</thead><tbody>' +
    items.map((item) => movieDbState.mode === 'people' ? movieDbPersonRowHTML(item) : movieDbMovieRowHTML(item)).join('') +
    '</tbody></table>';
  rows.querySelectorAll('.movie-db-row').forEach((row) => {
    row.addEventListener('click', () => movieDbOpenDetail(row.dataset.id || ''));
  });
  rows.querySelectorAll('.movie-db-assessment-choice').forEach((choice) => {
    choice.addEventListener('click', (ev) => ev.stopPropagation());
  });
  rows.querySelectorAll('.movie-db-assessment-toggle').forEach((control) => {
    control.addEventListener('change', () => movieDbSetAssessment(control));
  });
}

function movieDbTableHeadHTML() {
  if (movieDbState.mode === 'people') {
    return '<tr><th>俳優</th><th>知ってる</th><th>知らない</th><th>好き</th><th>嫌い</th></tr>';
  }
  return '<tr><th>映画</th><th>見た</th><th>見てない</th><th>好き</th><th>嫌い</th></tr>';
}

function movieDbMovieRowHTML(item) {
  const id = movieDbItemID(item);
  return '<tr class="movie-db-row' + (movieDbState.selectedID === id ? ' active' : '') + '" data-id="' + escAttr(id) + '">' +
    '<td class="movie-db-title-cell">' + esc(item.title || '-') + '</td>' +
    '<td>' + movieDbAssessmentToggleHTML(item, 'familiarity', 'seen', '見た') + '</td>' +
    '<td>' + movieDbAssessmentToggleHTML(item, 'familiarity', 'unseen', '見てない') + '</td>' +
    '<td>' + movieDbAssessmentToggleHTML(item, 'sentiment', 'like', '好き') + '</td>' +
    '<td>' + movieDbAssessmentToggleHTML(item, 'sentiment', 'dislike', '嫌い') + '</td>' +
    '</tr>';
}

function movieDbPersonRowHTML(item) {
  const id = movieDbItemID(item);
  return '<tr class="movie-db-row' + (movieDbState.selectedID === id ? ' active' : '') + '" data-id="' + escAttr(id) + '">' +
    '<td class="movie-db-title-cell">' + esc(item.name || '-') + '</td>' +
    '<td>' + movieDbAssessmentToggleHTML(item, 'familiarity', 'known', '知ってる') + '</td>' +
    '<td>' + movieDbAssessmentToggleHTML(item, 'familiarity', 'unknown', '知らない') + '</td>' +
    '<td>' + movieDbAssessmentToggleHTML(item, 'sentiment', 'like', '好き') + '</td>' +
    '<td>' + movieDbAssessmentToggleHTML(item, 'sentiment', 'dislike', '嫌い') + '</td>' +
    '</tr>';
}

function movieDbAssessmentToggleHTML(item, dimension, value, label) {
  const kind = movieDbState.mode === 'people' ? 'person' : 'movie';
  const id = String(kind === 'person' ? (item.person_id || '') : (item.movie_id || ''));
  const targetLabel = String(kind === 'person' ? (item.name || id) : (item.title || id));
  const selectedValue = String(item && item[dimension] ? item[dimension] : '');
  const checked = selectedValue === value ? ' checked' : '';
  const disabled = id ? '' : ' disabled';
  return '<label class="movie-db-assessment-choice">' +
    '<input class="movie-db-assessment-toggle" type="checkbox"' +
      ' data-kind="' + escAttr(kind) + '"' +
      ' data-target-id="' + escAttr(id) + '"' +
      ' data-target-label="' + escAttr(targetLabel) + '"' +
      ' data-dimension="' + escAttr(dimension) + '"' +
      ' data-value="' + escAttr(value) + '"' +
      ' data-selected-value="' + escAttr(selectedValue) + '"' + checked + disabled + '>' +
    '<span>' + esc(label) + '</span>' +
    '</label>';
}

function movieDbSetAssessment(control) {
  if (!control || !control.dataset) return;
  const kind = String(control.dataset.kind || '');
  const targetID = String(control.dataset.targetId || '');
  const targetLabel = String(control.dataset.targetLabel || targetID);
  const dimension = String(control.dataset.dimension || '');
  const selectedValue = String(control.dataset.selectedValue || '');
  const value = control.checked ? String(control.dataset.value || '') : '';
  if (!kind || !targetID || !dimension) return;

  const row = control.closest('.movie-db-row');
  const group = row
    ? Array.from(row.querySelectorAll('.movie-db-assessment-toggle[data-dimension="' + dimension + '"]'))
    : [control];
  group.forEach((item) => {
    item.checked = value !== '' && item.dataset.value === value;
    item.disabled = true;
  });
  movieDbSetSaveStatus('保存中...', '');

  fetch('/viewer/movie-catalog/preference', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({
      kind: kind,
      target_id: targetID,
      target_label: targetLabel,
      dimension: dimension,
      value: value,
      generated_by: 'viewer',
    }),
  })
    .then((r) => {
      if (!r.ok) return r.text().then((text) => { throw new Error(text || ('HTTP ' + String(r.status))); });
      return r.json();
    })
    .then((data) => {
      const detail = data && data.detail ? data.detail : {};
      const savedValue = String(detail[dimension] || '');
      group.forEach((item) => {
        item.dataset.selectedValue = savedValue;
        item.checked = savedValue !== '' && item.dataset.value === savedValue;
      });
      movieDbSetSaveStatus('保存済み', 'ok');
      if (movieDbState.selectedID === targetID) {
        window.setTimeout(() => movieDbOpenDetail(targetID, {skipHistory: true}), 80);
      }
    })
    .catch((err) => {
      group.forEach((item) => {
        item.checked = selectedValue !== '' && item.dataset.value === selectedValue;
      });
      movieDbSetSaveStatus('保存できません: ' + String(err && err.message ? err.message : err), 'err');
    })
    .finally(() => {
      group.forEach((item) => {
        item.disabled = false;
      });
    });
}

function movieDbSetSaveStatus(message, kind) {
  const status = movieDbEl('movieDbSaveStatus');
  if (!status) return;
  status.textContent = message;
  status.classList.toggle('ok', kind === 'ok');
  status.classList.toggle('err', kind === 'err');
}

function movieDbWatchedBadgeHTML(item) {
  const count = Number(item && item.watch_count ? item.watch_count : 0);
  if (!item || !item.watched) return '<span class="movie-db-watch-badge off">-</span>';
  return '<span class="movie-db-watch-badge">見た' + (count > 1 ? ' ' + esc(String(count)) : '') + '</span>';
}

function movieDbFavoriteBadgeHTML(item) {
  const count = Number(item && item.preference_count ? item.preference_count : 0);
  if (!item || !item.favorite) return '<span class="movie-db-favorite-badge off">-</span>';
  return '<span class="movie-db-favorite-badge">好き' + (count > 1 ? ' ' + esc(String(count)) : '') + '</span>';
}

function movieDbFavoriteToggleHTML(item) {
  const id = item && item.person_id ? String(item.person_id) : '';
  const name = item && item.name ? String(item.name) : id;
  const checked = item && item.favorite ? ' checked' : '';
  const disabled = id ? '' : ' disabled';
  return '<label class="movie-db-favorite-toggle-wrap">' +
    '<input class="movie-db-favorite-toggle" type="checkbox" data-person-id="' + escAttr(id) + '" data-person-name="' + escAttr(name) + '"' + checked + disabled + '>' +
    '<span>好き</span>' +
    '</label>';
}

function movieDbSetPersonFavorite(control) {
  const personID = control && control.dataset ? String(control.dataset.personId || '') : '';
  const personName = control && control.dataset ? String(control.dataset.personName || personID) : personID;
  if (!personID) return;
  control.disabled = true;
  fetch('/viewer/movie-catalog/preference', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({
      kind: 'person',
      target_id: personID,
      target_label: personName,
      favorite: Boolean(control.checked),
      signal_type: 'actor_affinity',
      generated_by: 'viewer',
    }),
  })
    .then((r) => {
      if (!r.ok) return r.text().then((text) => { throw new Error(text || ('HTTP ' + String(r.status))); });
      return r.json();
    })
    .then(() => {
      movieDbRefreshStats();
      movieDbRefreshList();
      if (movieDbState.mode === 'people' && movieDbState.selectedID === personID) {
        window.setTimeout(() => movieDbOpenDetail(personID, {skipHistory: true}), 80);
      }
    })
    .catch((err) => {
      control.checked = !control.checked;
      window.alert('好き設定を更新できません: ' + String(err && err.message ? err.message : err));
    })
    .finally(() => {
      control.disabled = false;
    });
}

function movieDbItemID(item) {
  return String(movieDbState.mode === 'people' ? item.person_id : item.movie_id);
}

function movieDbRenderPageInfo() {
  const info = movieDbEl('movieDbPageInfo');
  if (!info) return;
  const start = movieDbState.total === 0 ? 0 : movieDbState.offset + 1;
  const end = Math.min(movieDbState.offset + movieDbState.limit, movieDbState.total);
  info.textContent = String(start) + '-' + String(end) + ' / ' + String(movieDbState.total);
}

function movieDbUpdatePager() {
  const atStart = movieDbState.offset <= 0;
  const atEnd = movieDbState.offset + movieDbState.limit >= movieDbState.total;
  ['movieDbFirst', 'movieDbFirstBottom', 'movieDbPrev', 'movieDbPrevBottom'].forEach((id) => {
    const btn = movieDbEl(id);
    if (btn) btn.disabled = atStart;
  });
  ['movieDbNext', 'movieDbNextBottom'].forEach((id) => {
    const btn = movieDbEl(id);
    if (btn) btn.disabled = atEnd;
  });
}

function movieDbOpenDetail(id, options) {
  if (!id) return;
  const opts = options || {};
  movieDbState.selectedID = id;
  if (!opts.skipHistory) movieDbPushHistory(movieDbState.mode, id);
  const detail = movieDbEl('movieDbDetail');
  if (detail) detail.textContent = 'loading...';
  movieDbSetDetailOpen(true);
  movieDbRefreshListSelection();
  const params = new URLSearchParams();
  params.set('action', movieDbState.mode === 'people' ? 'person' : 'movie');
  params.set('id', id);
  fetch('/viewer/movie-catalog?' + params.toString(), {cache: 'no-store'})
    .then((r) => {
      if (!r.ok) return r.text().then((text) => { throw new Error(text || ('HTTP ' + String(r.status))); });
      return r.json();
    })
    .then((data) => {
      if (!data.available) throw new Error(data.error || 'movie catalog unavailable');
      movieDbRenderDetail(data.detail || {});
      movieDbUpdateHistoryButtons();
    })
    .catch((err) => {
      if (detail) detail.textContent = '詳細を読み込めません: ' + String(err && err.message ? err.message : err);
    });
}

function movieDbRefreshListSelection() {
  const rows = movieDbEl('movieDbRows');
  if (!rows) return;
  Array.from(rows.querySelectorAll('.movie-db-row')).forEach((row) => {
    row.classList.toggle('active', row.dataset.id === movieDbState.selectedID);
  });
}

function movieDbPushHistory(mode, id) {
  const current = movieDbState.history[movieDbState.historyIndex];
  if (current && current.mode === mode && current.id === id) return;
  movieDbState.history = movieDbState.history.slice(0, movieDbState.historyIndex + 1);
  movieDbState.history.push({mode, id});
  movieDbState.historyIndex = movieDbState.history.length - 1;
  movieDbUpdateHistoryButtons();
}

function movieDbNavigateHistory(delta) {
  const nextIndex = movieDbState.historyIndex + delta;
  if (nextIndex < 0 || nextIndex >= movieDbState.history.length) return;
  movieDbState.historyIndex = nextIndex;
  const entry = movieDbState.history[movieDbState.historyIndex];
  if (!entry) return;
  movieDbState.mode = entry.mode === 'people' ? 'people' : 'movies';
  movieDbState.selectedID = entry.id;
  movieDbSetModeControls();
  movieDbRefreshList();
  window.setTimeout(() => movieDbOpenDetail(entry.id, {skipHistory: true}), 80);
  movieDbUpdateHistoryButtons();
}

function movieDbUpdateHistoryButtons() {
  const canBack = movieDbState.historyIndex > 0;
  const canForward = movieDbState.historyIndex >= 0 && movieDbState.historyIndex < movieDbState.history.length - 1;
  ['movieDbBack', 'movieDbDetailBack'].forEach((id) => {
    const btn = movieDbEl(id);
    if (btn) btn.disabled = !canBack;
  });
  const forward = movieDbEl('movieDbForward');
  if (forward) forward.disabled = !canForward;
}

function movieDbSetModeControls() {
  const moviesBtn = movieDbEl('movieDbModeMovies');
  const peopleBtn = movieDbEl('movieDbModePeople');
  const role = movieDbEl('movieDbRoleFilter');
  const source = movieDbEl('movieDbSourceFilter');
  if (moviesBtn) moviesBtn.classList.toggle('active', movieDbState.mode === 'movies');
  if (peopleBtn) peopleBtn.classList.toggle('active', movieDbState.mode === 'people');
  if (role) role.disabled = movieDbState.mode !== 'movies';
  if (source) source.disabled = movieDbState.mode !== 'movies';
}

function movieDbSetDetailOpen(open) {
  const main = document.querySelector('#panel-movie-db .movie-db-main');
  if (main) main.classList.toggle('has-detail', Boolean(open));
}

function movieDbCloseDetail() {
  movieDbState.selectedID = '';
  movieDbClearDetail();
  movieDbSetDetailOpen(false);
  movieDbRefreshListSelection();
}

function movieDbRenderDetail(detail) {
  const target = movieDbEl('movieDbDetail');
  if (!target) return;
  if (movieDbState.mode === 'people') {
    const person = detail.person || {};
    const links = Array.isArray(detail.links) ? detail.links : [];
    target.innerHTML = '<h3>' + esc(person.name || '-') + '</h3>' +
      '<div class="movie-db-detail-meta">' + movieDbFavoriteBadgeHTML(person) + '<span class="daily-desk-muted">' + movieDbExternalLink(person.url) + ' / ' + esc(person.person_id || '-') + '</span></div>' +
      movieDbProfileHTML(person.profile || '') +
      '<h4>略歴</h4><div class="daily-desk-body">' + esc(person.biography || '-') + '</div>' +
      '<h4>出演・関連映画</h4>' + movieDbPersonLinksHTML(links);
    return;
  }
  const movie = detail.movie || {};
  const links = Array.isArray(detail.links) ? detail.links : [];
  const watchEvents = Array.isArray(detail.watch_events) ? detail.watch_events : [];
  target.innerHTML = '<h3>' + esc(movie.title || '-') + '</h3>' +
    '<div class="movie-db-detail-meta">' + movieDbWatchedBadgeHTML(movie) + '<span class="daily-desk-muted">' + movieDbExternalLink(movie.url) + ' / ' + esc(movie.movie_id || '-') + '</span></div>' +
    movieDbWatchEventsHTML(watchEvents) +
    '<h4>あらすじ</h4><div class="daily-desk-body">' + esc(movie.synopsis || '-') + '</div>' +
    '<h4>キャスト・スタッフ</h4>' + movieDbMovieLinksHTML(links);
}

function movieDbExternalLink(url) {
  if (!url) return '-';
  return '<a class="movie-db-external" href="' + esc(url) + '" target="_blank" rel="noreferrer">映画.com</a>';
}

function movieDbProfileHTML(raw) {
  if (!raw) return '';
  try {
    const profile = JSON.parse(raw);
    const keys = Object.keys(profile || {});
    if (!keys.length) return '';
    return '<h4>プロフィール</h4>' + keys.slice(0, 8).map((key) => (
      '<div class="desk-row"><span>' + esc(key) + '</span><span>' + esc(profile[key]) + '</span></div>'
    )).join('');
  } catch (_) {
    return '';
  }
}

function movieDbMovieLinksHTML(links) {
  if (!links.length) return '<div class="daily-desk-muted">リンクはありません。</div>';
  return '<div class="movie-db-link-list">' + links.slice(0, 120).map((link) => (
    '<div class="movie-db-link-item">' +
      movieDbLinkedPersonControl(link) +
      '<div class="daily-desk-muted">' + esc(link.role || '-') + ' / ' + esc(link.source || '-') + '</div>' +
    '</div>'
  )).join('') + '</div>';
}

function movieDbWatchEventsHTML(items) {
  if (!items.length) return '';
  return '<h4>鑑賞履歴</h4><div class="movie-db-watch-list">' + items.slice(0, 12).map((item) => (
    '<div class="movie-db-watch-item">' +
      '<span>' + esc(item.watched_at || item.created_at || '-') + '</span>' +
      '<span class="daily-desk-muted">' + esc(item.source || '-') + (item.source_batch_id ? ' / ' + esc(item.source_batch_id) : '') + '</span>' +
    '</div>'
  )).join('') + '</div>';
}

function movieDbPersonLinksHTML(links) {
  if (!links.length) return '<div class="daily-desk-muted">リンクはありません。</div>';
  return '<div class="movie-db-link-list">' + links.slice(0, 120).map((link) => (
    '<div class="movie-db-link-item">' +
      movieDbLinkedMovieControl(link) +
      '<div class="daily-desk-muted">' + esc(link.role || '-') + ' / ' + esc(link.source || '-') + '</div>' +
    '</div>'
  )).join('') + '</div>';
}

function movieDbLinkedPersonControl(link) {
  const id = link && link.person_id ? String(link.person_id) : '';
  const url = link && link.person_url ? String(link.person_url) : '';
  const name = link && link.person_name ? String(link.person_name) : '-';
  const favorite = link && link.person_favorite ? movieDbFavoriteBadgeHTML({favorite: true, preference_count: 1}) : '';
  if (link && link.person_fetched && id) {
    return '<div class="movie-db-link-head"><button class="ctl-btn" type="button" onclick="movieDbSetModeAndOpen(&quot;people&quot;,&quot;' + escAttr(id) + '&quot;)">' + esc(name) + '</button>' + favorite + '</div>';
  }
  return '<div class="movie-db-link-head">' +
    '<span class="movie-db-link-title">' + esc(name) + '</span>' +
    favorite +
    movieDbFetchLinkButton('person', url) +
    '</div>';
}

function movieDbLinkedMovieControl(link) {
  const id = link && link.movie_id ? String(link.movie_id) : '';
  const url = link && link.movie_url ? String(link.movie_url) : '';
  const title = link && link.movie_title ? String(link.movie_title) : '-';
  const watched = link && link.movie_watched ? movieDbWatchedBadgeHTML({watched: true, watch_count: 1}) : '';
  if (link && link.movie_fetched && id) {
    return '<div class="movie-db-link-head"><button class="ctl-btn" type="button" onclick="movieDbSetModeAndOpen(&quot;movies&quot;,&quot;' + escAttr(id) + '&quot;)">' + esc(title) + '</button>' + watched + '</div>';
  }
  return '<div class="movie-db-link-head">' +
    '<span class="movie-db-link-title">' + esc(title) + '</span>' +
    movieDbFetchLinkButton('movie', url) +
    '</div>';
}

function movieDbFetchLinkButton(kind, url) {
  if (!url) return '<span class="daily-desk-muted">未取得</span>';
  return '<button class="ctl-btn movie-db-link-fetch" type="button" onclick="movieDbFetchLinked(&quot;' + escAttr(kind) + '&quot;,&quot;' + escAttr(url) + '&quot;)">取得</button>';
}

function movieDbFetchLinked(kind, url) {
  const kindEl = movieDbEl('movieDbFetchKind');
  const maxPagesEl = movieDbEl('movieDbFetchMaxPages');
  const followEl = movieDbEl('movieDbFetchFollow');
  if (kindEl) kindEl.value = kind === 'person' ? 'person' : 'movie';
  if (maxPagesEl && (!maxPagesEl.value || Number(maxPagesEl.value) > 5)) maxPagesEl.value = '5';
  if (followEl) followEl.checked = false;
  movieDbFetchFromWindow(url);
}

function movieDbSetModeAndOpen(mode, id) {
  movieDbState.mode = mode === 'people' ? 'people' : 'movies';
  movieDbState.offset = 0;
  movieDbState.selectedID = id;
  movieDbSetModeControls();
  movieDbRefreshList();
  window.setTimeout(() => movieDbOpenDetail(id), 120);
}

function movieDbBind() {
  const panel = movieDbEl('panel-movie-db');
  if (!panel || panel.dataset.bound === '1') return;
  panel.dataset.bound = '1';
  const search = movieDbEl('movieDbSearch');
  const role = movieDbEl('movieDbRoleFilter');
  const source = movieDbEl('movieDbSourceFilter');
  const refresh = movieDbEl('movieDbRefresh');
  const prev = movieDbEl('movieDbPrev');
  const next = movieDbEl('movieDbNext');
  const first = movieDbEl('movieDbFirst');
  const prevBottom = movieDbEl('movieDbPrevBottom');
  const nextBottom = movieDbEl('movieDbNextBottom');
  const firstBottom = movieDbEl('movieDbFirstBottom');
  const limit = movieDbEl('movieDbLimit');
  const back = movieDbEl('movieDbBack');
  const forward = movieDbEl('movieDbForward');
  const detailBack = movieDbEl('movieDbDetailBack');
  const detailClose = movieDbEl('movieDbDetailClose');
  const fetchBtn = movieDbEl('movieDbFetchBtn');
  const movies = movieDbEl('movieDbModeMovies');
  const people = movieDbEl('movieDbModePeople');
  if (movies) movies.addEventListener('click', () => movieDbSetMode('movies'));
  if (people) people.addEventListener('click', () => movieDbSetMode('people'));
  if (search) search.addEventListener('input', () => {
    window.clearTimeout(movieDbState.searchTimer);
    movieDbState.searchTimer = window.setTimeout(() => {
      movieDbState.q = search.value.trim();
      movieDbState.offset = 0;
      movieDbState.selectedID = '';
      movieDbClearDetail();
      movieDbSetDetailOpen(false);
      movieDbRefreshList();
    }, 240);
  });
  if (role) role.addEventListener('change', () => {
    movieDbState.role = role.value;
    movieDbState.offset = 0;
    movieDbState.selectedID = '';
    movieDbClearDetail();
    movieDbSetDetailOpen(false);
    movieDbRefreshList();
  });
  if (source) source.addEventListener('change', () => {
    movieDbState.source = source.value;
    movieDbState.offset = 0;
    movieDbState.selectedID = '';
    movieDbClearDetail();
    movieDbSetDetailOpen(false);
    movieDbRefreshList();
  });
  if (refresh) refresh.addEventListener('click', () => {
    movieDbRefreshStats();
    movieDbRefreshList();
  });
  const firstPage = () => {
    movieDbState.offset = 0;
    movieDbRefreshList();
  };
  const prevPage = () => {
    movieDbState.offset = Math.max(0, movieDbState.offset - movieDbState.limit);
    movieDbRefreshList();
  };
  const nextPage = () => {
    if (movieDbState.offset + movieDbState.limit < movieDbState.total) {
      movieDbState.offset += movieDbState.limit;
      movieDbRefreshList();
    }
  };
  if (first) first.addEventListener('click', firstPage);
  if (firstBottom) firstBottom.addEventListener('click', firstPage);
  if (prev) prev.addEventListener('click', prevPage);
  if (prevBottom) prevBottom.addEventListener('click', prevPage);
  if (next) next.addEventListener('click', nextPage);
  if (nextBottom) nextBottom.addEventListener('click', nextPage);
  if (limit) limit.addEventListener('change', () => {
    movieDbState.limit = Math.max(10, Math.min(50, Number(limit.value || 25) || 25));
    movieDbState.offset = 0;
    movieDbRefreshList();
  });
  if (back) back.addEventListener('click', () => movieDbNavigateHistory(-1));
  if (forward) forward.addEventListener('click', () => movieDbNavigateHistory(1));
  if (detailBack) detailBack.addEventListener('click', () => movieDbNavigateHistory(-1));
  if (detailClose) detailClose.addEventListener('click', movieDbCloseDetail);
  if (fetchBtn) fetchBtn.addEventListener('click', () => movieDbFetchFromWindow());
  const candidates = movieDbEl('movieDbFetchCandidates');
  if (candidates) {
    candidates.addEventListener('click', (evt) => {
      const btn = evt.target && evt.target.closest ? evt.target.closest('.movie-db-fetch-candidate') : null;
      if (!btn) return;
      movieDbFetchFromWindow(btn.dataset.url || '');
    });
  }
  document.querySelectorAll('[data-tab="movie-db"]').forEach((btn) => {
    btn.addEventListener('click', () => {
      movieDbRefreshStats();
      movieDbRefreshList();
    });
  });
  movieDbRefreshStats();
  movieDbRefreshList();
}

document.addEventListener('DOMContentLoaded', movieDbBind);

function movieDbFetchFromWindow(forcedURL) {
  if (movieDbState.fetchBusy) return;
  const kindEl = movieDbEl('movieDbFetchKind');
  const queryEl = movieDbEl('movieDbFetchQuery');
  const followEl = movieDbEl('movieDbFetchFollow');
  const filmographyEl = movieDbEl('movieDbFetchFilmography');
  const maxPagesEl = movieDbEl('movieDbFetchMaxPages');
  const btn = movieDbEl('movieDbFetchBtn');
  const status = movieDbEl('movieDbFetchStatus');
  const candidates = movieDbEl('movieDbFetchCandidates');
  const raw = forcedURL || (queryEl ? queryEl.value.trim() : '');
  const isURL = /^https?:\/\/eiga\.com\/(movie|person)\/\d+\/?$/.test(raw);
  const forcedKind = isURL && /\/person\//.test(raw) ? 'person' : (isURL && /\/movie\//.test(raw) ? 'movie' : '');
  const payload = {
    kind: forcedKind || (kindEl ? kindEl.value : 'movie'),
    query: isURL ? '' : raw,
    url: isURL ? raw : '',
    max_pages: Math.max(1, Math.min(20, Number(maxPagesEl && maxPagesEl.value ? maxPagesEl.value : 5) || 5)),
    follow_links: followEl ? Boolean(followEl.checked) : true,
    include_person_filmography: filmographyEl ? Boolean(filmographyEl.checked) : true,
  };
  if (!payload.query && !payload.url) {
    movieDbSetFetchStatus('映画名・人物名、または映画.com URLを入力してください。', 'err');
    return;
  }
  movieDbState.fetchBusy = true;
  if (btn) btn.disabled = true;
  if (candidates) candidates.innerHTML = '';
  movieDbSetFetchStatus('取得中... max_pages=' + String(payload.max_pages), '');
  fetch('/viewer/movie-catalog/fetch', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(payload),
  })
    .then((r) => r.json().then((data) => ({ok: r.ok, status: r.status, data})))
    .then((res) => {
      if (!res.ok) {
        movieDbRenderFetchFailure(res.data || {}, res.status);
        return;
      }
      const data = res.data || {};
      const lines = String(data.stdout || '').trim().split('\n').filter(Boolean);
      movieDbSetFetchStatus('取得完了: ' + (lines[lines.length - 1] || data.url || '-'), 'ok');
      movieDbRefreshStats();
      movieDbRefreshList();
      if (movieDbState.selectedID) window.setTimeout(() => movieDbOpenDetail(movieDbState.selectedID), 250);
    })
    .catch((err) => {
      movieDbSetFetchStatus('取得に失敗しました: ' + String(err && err.message ? err.message : err), 'err');
    })
    .finally(() => {
      movieDbState.fetchBusy = false;
      if (btn) btn.disabled = false;
    });
}

function movieDbRenderFetchFailure(data, statusCode) {
  const candidates = Array.isArray(data.candidates) ? data.candidates : [];
  if (candidates.length) {
    movieDbSetFetchStatus('候補が複数あります。取得する項目を選んでください。', '');
    movieDbRenderFetchCandidates(candidates);
    return;
  }
  if (data && data.status === 'candidates') {
    movieDbSetFetchStatus('ローカル候補がありません。映画.comの作品URLまたは人物URLを貼って取得してください。', 'err');
    movieDbRenderFetchURLHint(data);
    return;
  }
  const message = data && data.error ? data.error : ('HTTP ' + String(statusCode || '-'));
  movieDbSetFetchStatus('取得できません: ' + message, 'err');
}

function movieDbRenderFetchURLHint(data) {
  const target = movieDbEl('movieDbFetchCandidates');
  if (!target) return;
  const query = data && data.query ? String(data.query) : '';
  const searchURL = query ? 'https://eiga.com/search/' + encodeURIComponent(query) + '/' : '';
  const searchLink = searchURL
    ? '<a class="movie-db-external" href="' + escAttr(searchURL) + '" target="_blank" rel="noreferrer">映画.comで検索を開く</a>'
    : '';
  target.innerHTML = '<div class="movie-db-fetch-hint">' +
    '<div>URL例: https://eiga.com/movie/103262/</div>' +
    (searchLink ? '<div>' + searchLink + '</div>' : '') +
    '</div>';
}

function movieDbRenderFetchCandidates(items) {
  const target = movieDbEl('movieDbFetchCandidates');
  if (!target) return;
  target.innerHTML = items.map((item) => {
    const label = item.kind === 'person' ? (item.name || '-') : (item.title || '-');
    const meta = item.kind + ' / ' + (item.id || '-');
    return '<button class="ctl-btn movie-db-fetch-candidate" type="button" data-url="' + escAttr(item.url || '') + '">' +
      esc(label) + '<br><span class="daily-desk-muted">' + esc(meta) + '</span></button>';
  }).join('');
}

function movieDbSetFetchStatus(message, kind) {
  const status = movieDbEl('movieDbFetchStatus');
  if (!status) return;
  status.textContent = message;
  status.classList.toggle('ok', kind === 'ok');
  status.classList.toggle('err', kind === 'err');
}
