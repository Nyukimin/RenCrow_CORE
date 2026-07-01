// Investment tab module: stock/ETF learning foundation status.

function investmentStateView() {
  return state.investment || {};
}

function investmentStatusClass(status) {
  const s = String(status || '').toLowerCase();
  if (s === 'ok' || s === 'success') return 'running';
  if (s === 'warn' || s === 'partial') return 'thinking';
  if (s === 'unavailable' || s === 'fail' || s === 'blocked') return 'error';
  return 'idle';
}

function investmentValueText(value, fallback) {
  if (value == null || value === '') return fallback || '-';
  return String(value);
}

function formatInvestmentRate(value) {
  if (value == null || value === '') return '-';
  const n = Number(value);
  if (!Number.isFinite(n)) return String(value);
  return (n >= 0 ? '+' : '') + (n * 100).toFixed(2) + '%';
}

function formatInvestmentScore(value) {
  if (value == null || value === '') return '-';
  const n = Number(value);
  if (!Number.isFinite(n)) return String(value);
  return n.toFixed(2);
}

function investmentSummaryCards(data) {
  const snapshot = data.snapshot || {};
  const summary = data.summary || {};
  const health = [
    summary.FailFetches ? ('fetch fail sources=' + summary.FailFetches) : 'fetch ok',
    summary.PartialFetches ? ('partial sources=' + summary.PartialFetches) : 'no partial',
    summary.StaleSources ? ('stale sources=' + summary.StaleSources) : 'no stale sources',
  ].join(' / ');
  return [
    {
      title: 'Snapshot',
      big: investmentValueText(snapshot.snapshot_date || summary.snapshot_date, '-'),
      sub: investmentValueText(snapshot.status || summary.snapshot_status, 'no snapshot'),
    },
    {
      title: 'Health',
      big: summary.OpenStopEvents > 0 ? 'stop' : (summary.OpenWarnEvents > 0 ? 'warn' : 'ok'),
      sub: health,
    },
    {
      title: 'Features',
      big: String(summary.FeatureRows || data.featureRows.length || 0),
      sub: summary.LatestFeatureWeekEnd ? ('latest week_end: ' + summary.LatestFeatureWeekEnd) : 'no weekly features',
    },
    {
      title: 'Events',
      big: String(summary.EventRows || data.eventRows.length || 0),
      sub: summary.OpenStopEvents ? ('open stop=' + summary.OpenStopEvents) : 'event log available',
    },
  ];
}

function renderInvestmentSummaryCards(target, data) {
  if (!target) return;
  const cards = investmentSummaryCards(data).map((item) => (
    '<div class="daily-desk-card">' +
      '<h3>' + esc(item.title) + '</h3>' +
      '<div class="ops-big">' + esc(item.big) + '</div>' +
      '<div class="ops-sub">' + esc(item.sub) + '</div>' +
    '</div>'
  ));
  target.innerHTML = cards.join('');
}

function renderInvestmentDesk() {
  const data = investmentStateView();
  const badge = document.getElementById('investmentStateBadge');
  const statusCard = document.getElementById('investmentStatusCard');
  const summaryCards = document.getElementById('investmentSummaryCards');
  const snapshotCard = document.getElementById('investmentSnapshotCard');
  const healthCard = document.getElementById('investmentHealthCard');
  const snapshotBody = document.getElementById('investmentSnapshotBody');
  const sourceBody = document.getElementById('investmentSourceBody');
  const featureBody = document.getElementById('investmentFeatureBody');
  const eventBody = document.getElementById('investmentEventBody');
  if (!badge || !statusCard || !summaryCards || !snapshotCard || !healthCard || !snapshotBody || !sourceBody || !featureBody || !eventBody) return;

  const status = String(data.status || (data.available ? 'ok' : 'unavailable')).toLowerCase();
  badge.className = 'desk-status-pill ' + investmentStatusClass(status);
  badge.textContent = data.loading ? 'loading' : (data.status || 'unavailable');

  statusCard.innerHTML =
    '<h3>Viewer Endpoint</h3>' +
    '<div class="desk-row"><span>Status</span><span class="desk-pill ' + investmentStatusClass(status) + '">' + esc(data.status || 'unavailable') + '</span></div>' +
    '<div class="desk-row"><span>Message</span><span>' + esc(data.statusMessage || '-') + '</span></div>' +
    '<div class="desk-row"><span>DB path</span><span class="desk-code">' + esc(data.dbPath || '-') + '</span></div>' +
    '<div class="desk-row"><span>Refreshed</span><span>' + esc(data.refreshedAt ? fdt(data.refreshedAt) : '-') + '</span></div>';

  renderInvestmentSummaryCards(summaryCards, data);

  const snapshot = data.snapshot || {};
  snapshotCard.innerHTML =
    '<h3>Latest Snapshot</h3>' +
    '<div class="desk-row"><span>Date</span><span>' + esc(snapshot.snapshot_date || '-') + '</span></div>' +
    '<div class="desk-row"><span>Status</span><span class="desk-pill ' + investmentStatusClass(snapshot.status || data.status) + '">' + esc(snapshot.status || '-') + '</span></div>' +
    '<div class="desk-row"><span>DB Hash</span><span class="desk-code">' + esc(short(snapshot.db_hash || '-', 22)) + '</span></div>' +
    '<div class="desk-row"><span>Features Hash</span><span class="desk-code">' + esc(short(snapshot.features_hash || '-', 22)) + '</span></div>' +
    '<div class="desk-row"><span>Range</span><span>' + esc((snapshot.data_start_date || '-') + ' → ' + (snapshot.data_end_date || '-')) + '</span></div>' +
    '<div class="desk-row"><span>Missing</span><span>' + esc(snapshot.missing_rate == null ? '-' : formatInvestmentScore(snapshot.missing_rate)) + '</span></div>' +
    '<div class="desk-row"><span>Notes</span><span>' + esc(short(snapshot.notes || '-', 120)) + '</span></div>';

  const summary = data.summary || {};
  healthCard.innerHTML =
    '<h3>Health</h3>' +
    '<div class="desk-row"><span>Feature rows</span><span>' + esc(String(summary.FeatureRows || 0)) + '</span></div>' +
    '<div class="desk-row"><span>Event rows</span><span>' + esc(String(summary.EventRows || 0)) + '</span></div>' +
    '<div class="desk-row"><span>Open stop</span><span class="desk-pill ' + (summary.OpenStopEvents > 0 ? 'danger' : '') + '">' + esc(String(summary.OpenStopEvents || 0)) + '</span></div>' +
    '<div class="desk-row"><span>Open warn</span><span>' + esc(String(summary.OpenWarnEvents || 0)) + '</span></div>' +
    '<div class="desk-row"><span>Stale sources</span><span>' + esc(String(summary.StaleSources || 0)) + '</span></div>' +
    '<div class="desk-row"><span>Latest week_end</span><span>' + esc(summary.LatestFeatureWeekEnd || '-') + '</span></div>';

  snapshotBody.innerHTML = (Array.isArray(data.recentSnapshots) && data.recentSnapshots.length ? data.recentSnapshots : [snapshot]).filter(Boolean).map((row) => (
    '<tr>' +
      '<td>' + esc(row.snapshot_date || '-') + '</td>' +
      '<td><span class="desk-pill ' + investmentStatusClass(row.status || data.status) + '">' + esc(row.status || '-') + '</span></td>' +
      '<td class="desk-code">' + esc(short(row.db_hash || '-', 20)) + '</td>' +
      '<td class="desk-code">' + esc(short(row.features_hash || '-', 20)) + '</td>' +
      '<td>' + esc((row.data_start_date || '-') + ' → ' + (row.data_end_date || '-')) + '</td>' +
      '<td>' + esc(row.missing_rate == null ? '-' : formatInvestmentScore(row.missing_rate)) + '</td>' +
      '<td>' + esc(row.created_at ? fdt(row.created_at) : '-') + '</td>' +
    '</tr>'
  )).join('');

  sourceBody.innerHTML = (Array.isArray(data.sourceHealth) ? data.sourceHealth : []).map((row) => (
    '<tr>' +
      '<td>' + esc(row.source_name || '-') + '</td>' +
      '<td><span class="desk-pill ' + investmentStatusClass(row.latest_status) + '">' + esc(row.latest_status || '-') + '</span></td>' +
      '<td>' + esc(row.last_fetch_at ? fdt(row.last_fetch_at) : '-') + '</td>' +
      '<td>' + esc(String(row.success_count || 0)) + '</td>' +
      '<td>' + esc(String(row.partial_count || 0)) + '</td>' +
      '<td>' + esc(String(row.fail_count || 0)) + '</td>' +
      '<td>' + esc(String(row.rows_fetched || 0)) + '</td>' +
    '</tr>'
  )).join('') || '<tr><td colspan="7" class="small">No source health rows</td></tr>';

  featureBody.innerHTML = (Array.isArray(data.featureRows) ? data.featureRows : []).map((row) => (
    '<tr>' +
      '<td>' + esc(row.week_end || '-') + '</td>' +
      '<td>' + esc(row.instrument || '-') + '</td>' +
      '<td>' + esc(formatInvestmentRate(row.ret_1w)) + '</td>' +
      '<td>' + esc(formatInvestmentRate(row.ret_12w)) + '</td>' +
      '<td>' + esc(formatInvestmentRate(row.vol_12w)) + '</td>' +
      '<td>' + esc(formatInvestmentScore(row.event_risk_score)) + '</td>' +
      '<td>' + esc(short(row.flags || '-', 40)) + '</td>' +
    '</tr>'
  )).join('') || '<tr><td colspan="7" class="small">No weekly features yet</td></tr>';

  eventBody.innerHTML = (Array.isArray(data.eventRows) ? data.eventRows : []).map((row) => (
    '<tr>' +
      '<td>' + esc(row.event_ts ? fdt(row.event_ts) : '-') + '</td>' +
      '<td><span class="desk-pill ' + investmentStatusClass(row.level) + '">' + esc(row.level || '-') + '</span></td>' +
      '<td>' + esc(row.scope || '-') + '</td>' +
      '<td>' + esc(short(row.reason || '-', 80)) + '</td>' +
      '<td>' + esc(formatInvestmentScore(row.event_risk_score)) + '</td>' +
      '<td>' + esc(row.resolved_at ? fdt(row.resolved_at) : '-') + '</td>' +
    '</tr>'
  )).join('') || '<tr><td colspan="6" class="small">No events logged yet</td></tr>';
}

function refreshInvestmentData() {
  const data = investmentStateView();
  data.loading = true;
  data.fetchError = '';
  fetch('/viewer/investment/status?limit=20')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => { throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'investment data unavailable')); });
      }
      return r.json();
    })
    .then((payload) => {
      state.investment = Object.assign({}, data, {
        available: Boolean(payload.available),
        status: String(payload.status || (payload.available ? 'ok' : 'unavailable')),
        statusMessage: String(payload.status_message || ''),
        dbPath: String(payload.db_path || ''),
        refreshedAt: String(payload.refreshed_at || ''),
        snapshot: payload.snapshot || null,
        recentSnapshots: Array.isArray(payload.recent_snapshots) ? payload.recent_snapshots : [],
        sourceHealth: Array.isArray(payload.source_health) ? payload.source_health : [],
        featureRows: Array.isArray(payload.feature_rows) ? payload.feature_rows : [],
        eventRows: Array.isArray(payload.event_rows) ? payload.event_rows : [],
        summary: payload.summary || {},
        loading: false,
      });
      renderInvestmentDesk();
    })
    .catch((err) => {
      state.investment = Object.assign({}, data, {
        available: false,
        status: 'unavailable',
        statusMessage: String(err && err.message ? err.message : err),
        fetchError: String(err && err.message ? err.message : err),
        loading: false,
      });
      renderInvestmentDesk();
      console.error(err);
    });
}

function bindInvestmentDeskControls() {
  const btn = document.getElementById('investmentRefreshBtn');
  if (!btn || btn.dataset.bound === '1') return;
  btn.dataset.bound = '1';
  btn.addEventListener('click', refreshInvestmentData);
}
