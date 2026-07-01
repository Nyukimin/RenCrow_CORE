// Reports tab module: human-readable report views built from Evidence and Verification.
let deskSelectedReportID = '';

function deskFilteredReports() {
  const filter = document.getElementById('reportFilter');
  const value = filter ? filter.value : '';
  const reads = deskReportReads();
  return deskAllReports().filter((r) => {
    if (value === 'unread') return !reads[r.report_id];
    if (value) return r.status === value;
    return true;
  });
}

function renderReportsDesk() {
  const listEl = document.getElementById('reportList');
  const detailEl = document.getElementById('reportDetail');
  if (!listEl || !detailEl) return;
  const reports = deskFilteredReports();
  if (!deskSelectedReportID && reports[0]) deskSelectedReportID = reports[0].report_id;
  if (deskSelectedReportID && !reports.some((r) => r.report_id === deskSelectedReportID)) {
    deskSelectedReportID = reports[0] ? reports[0].report_id : '';
  }
  listEl.innerHTML = reports.length ? reports.map(renderReportCard).join('') : '<div class="daily-desk-card daily-desk-muted">Report はまだありません。</div>';
  const selected = reports.find((r) => r.report_id === deskSelectedReportID) || reports[0] || null;
  detailEl.innerHTML = selected ? renderReportDetail(selected) : 'No report selected';
}

function renderReportCard(report) {
  const reads = deskReportReads();
  const unread = reads[report.report_id] ? '' : ' unread';
  const active = report.report_id === deskSelectedReportID ? ' active' : '';
  return '<article class="report-card' + active + unread + '" onclick="deskSelectReport(\'' + escAttr(report.report_id) + '\')">' +
    '<div class="desk-row"><strong>' + esc(short(report.title || '-', 88)) + '</strong><span class="desk-pill">' + esc(report.status || '-') + '</span></div>' +
    '<div class="desk-code">' + esc(report.job_id || '-') + '</div>' +
    '<div class="daily-desk-muted">' + esc(fdt(report.created_at)) + '</div>' +
  '</article>';
}

function renderReportDetail(report) {
  return '<h3>' + esc(report.title || '-') + '</h3>' +
    '<div class="desk-row"><span>Status</span><span class="desk-pill">' + esc(report.status || '-') + '</span></div>' +
    '<div class="desk-row"><span>Job ID</span><span class="desk-code">' + esc(report.job_id || '-') + '</span></div>' +
    '<h4>要約</h4><div class="daily-desk-body">' + esc(report.summary || '-') + '</div>' +
    renderReportSection('何を変更したか', report.changed) +
    renderReportSection('何を確認したか', report.verified) +
    renderReportSection('失敗したこと', report.failed) +
    renderReportSection('未確認のこと', report.unconfirmed) +
    renderReportRefs(report.evidence_refs) +
    renderReportSection('次の判断', report.next_decision);
}

function renderReportSection(title, items) {
  const list = Array.isArray(items) ? items : [];
  return '<h4>' + esc(title) + '</h4>' + (list.length ? '<ul>' + list.map((it) => '<li>' + esc(String(it || '-')) + '</li>').join('') + '</ul>' : '<div class="daily-desk-muted">-</div>');
}

function renderReportRefs(refs) {
  const list = Array.isArray(refs) ? refs : [];
  if (!list.length) return '<h4>Evidence</h4><div class="daily-desk-muted">-</div>';
  return '<h4>Evidence</h4>' + list.map((ref) => (
    '<div class="desk-item"><span class="desk-pill">' + esc(ref.type || '-') + '</span> <span class="desk-code">' + esc(ref.id || '-') + '</span> <button class="ctl-btn" onclick="deskOpenReportRef(\'' + escAttr(ref.type || '') + '\', \'' + escAttr(ref.id || '') + '\')">Open</button></div>'
  )).join('');
}

function deskSelectReport(id) {
  deskSelectedReportID = id;
  renderReportsDesk();
}

function deskSelectedReport() {
  return deskAllReports().find((r) => r.report_id === deskSelectedReportID) || null;
}

function deskOpenReportRef(type, id) {
  if (!id) return;
  if (type === 'evidence' || type === 'verification') {
    state.pendingEvidenceJobID = id;
    switchTab('jobs');
    openEvidence(id);
    return;
  }
  switchTab('jobs');
}

function deskReportSummaryText(report) {
  if (!report) return '';
  return [
    'report_id=' + report.report_id,
    'job_id=' + report.job_id,
    'status=' + report.status,
    'summary=' + report.summary,
  ].join(' | ');
}

function deskReportMarkdown(report) {
  if (!report) return '';
  const section = (title, items) => {
    const list = Array.isArray(items) && items.length ? items : ['-'];
    return '## ' + title + '\n' + list.map((it) => '- ' + String(it || '-')).join('\n');
  };
  return [
    '# 作業完了報告: ' + (report.title || '-'),
    '## 要約\n' + (report.summary || '-'),
    section('何を変更したか', report.changed),
    section('何を確認したか', report.verified),
    '## 検証結果\n' + (report.status || '-'),
    section('失敗したこと', report.failed),
    section('未確認のこと', report.unconfirmed),
    section('Evidence', (report.evidence_refs || []).map((r) => (r.type || '-') + ': ' + (r.id || '-'))),
    section('成果物', report.artifacts || []),
    section('次の判断', report.next_decision),
  ].join('\n\n');
}

function deskMarkSelectedReportRead() {
  const report = deskSelectedReport();
  if (!report) return;
  const reads = deskReportReads();
  reads[report.report_id] = new Date().toISOString();
  deskSaveReportReads(reads);
  renderReportsDesk();
  renderHomeDesk();
}

function bindReportsDeskControls() {
  const filter = document.getElementById('reportFilter');
  const copySummary = document.getElementById('reportCopySummaryBtn');
  const copyMarkdown = document.getElementById('reportCopyMarkdownBtn');
  const markRead = document.getElementById('reportMarkReadBtn');
  if (filter && filter.dataset.bound !== '1') {
    filter.dataset.bound = '1';
    filter.addEventListener('change', renderReportsDesk);
  }
  if (copySummary && copySummary.dataset.bound !== '1') {
    copySummary.dataset.bound = '1';
    copySummary.addEventListener('click', () => copyTextPayload(copySummary, deskReportSummaryText(deskSelectedReport())));
  }
  if (copyMarkdown && copyMarkdown.dataset.bound !== '1') {
    copyMarkdown.dataset.bound = '1';
    copyMarkdown.addEventListener('click', () => copyTextPayload(copyMarkdown, deskReportMarkdown(deskSelectedReport())));
  }
  if (markRead && markRead.dataset.bound !== '1') {
    markRead.dataset.bound = '1';
    markRead.addEventListener('click', deskMarkSelectedReportRead);
  }
}
