// Read-only database-specific Viewer panels under the left Memory accordion.
function databaseViewerElement(id) {
  return document.getElementById(id);
}

function databaseViewerEscape(value) {
  return String(value == null ? '' : value)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function databaseViewerRequest(path) {
  return fetch(path, {cache: 'no-store'}).then((response) => {
    if (!response.ok) {
      return response.text().then((body) => {
        throw new Error(body || ('HTTP ' + String(response.status)));
      });
    }
    return response.json();
  });
}

function databaseViewerStatus(status, data, label) {
  if (!status) return;
  if (!data || !data.available) {
    status.textContent = data && data.error ? data.error : (label + ' database unavailable');
    status.title = data && data.db_path ? data.db_path : '';
    return;
  }
  status.textContent = 'DBを読込済み';
  status.title = data.db_path || '';
}

function refreshMemoryArchiveDatabase() {
  const body = databaseViewerElement('memoryArchiveBody');
  const count = databaseViewerElement('memoryArchiveCount');
  const status = databaseViewerElement('memoryArchiveStatus');
  const params = new URLSearchParams({limit: '100'});
  const session = databaseViewerElement('memoryArchiveSession');
  const domain = databaseViewerElement('memoryArchiveDomain');
  if (session && session.value.trim()) params.set('session_id', session.value.trim());
  if (domain && domain.value.trim()) params.set('domain', domain.value.trim());
  if (status) status.textContent = '読み込み中...';
  return databaseViewerRequest('/viewer/databases/conversation-archive?' + params.toString())
    .then((data) => {
      const items = data && Array.isArray(data.items) ? data.items : [];
      if (count) count.textContent = String(Number(data && data.total || 0));
      databaseViewerStatus(status, data, 'Conversation Archive');
      if (!body) return;
      body.innerHTML = items.length ? items.map((item) => (
        '<tr><td>' + databaseViewerEscape(item.start_time || '-') + '</td>' +
        '<td>' + databaseViewerEscape(item.session_id || '-') + '</td>' +
        '<td>' + databaseViewerEscape(item.domain || '-') + '</td>' +
        '<td>' + databaseViewerEscape(item.summary || '-') + '</td>' +
        '<td>' + databaseViewerEscape(Array.isArray(item.keywords) ? item.keywords.join(', ') : '-') + '</td>' +
        '<td>' + (item.is_novel ? 'yes' : '-') + '</td></tr>'
      )).join('') : '<tr><td colspan="6">Archiveに該当データはありません。</td></tr>';
    })
    .catch((error) => {
      if (count) count.textContent = '0';
      if (status) status.textContent = '読込失敗: ' + String(error && error.message ? error.message : error);
      if (body) body.innerHTML = '<tr><td colspan="6">Archiveを読み込めません。</td></tr>';
    });
}

function refreshGlossaryDatabase() {
  const body = databaseViewerElement('glossaryDbBody');
  const count = databaseViewerElement('glossaryDbCount');
  const status = databaseViewerElement('glossaryDbStatus');
  if (status) status.textContent = '読み込み中...';
  return databaseViewerRequest('/viewer/databases/glossary?limit=100')
    .then((data) => {
      const items = data && Array.isArray(data.items) ? data.items : [];
      if (count) count.textContent = String(Number(data && data.total || 0));
      databaseViewerStatus(status, data, 'Glossary');
      if (!body) return;
      body.innerHTML = items.length ? items.map((item) => (
        '<tr><td>' + databaseViewerEscape(item.updated_at || '-') + '</td>' +
        '<td>' + databaseViewerEscape(item.term || '-') + '</td>' +
        '<td>' + databaseViewerEscape(item.category || '-') + '</td>' +
        '<td>' + databaseViewerEscape(item.explanation || '-') + '</td>' +
        '<td>' + databaseViewerEscape(item.source || '-') + '</td></tr>'
      )).join('') : '<tr><td colspan="5">用語集にデータはありません。</td></tr>';
    })
    .catch((error) => {
      if (count) count.textContent = '0';
      if (status) status.textContent = '読込失敗: ' + String(error && error.message ? error.message : error);
      if (body) body.innerHTML = '<tr><td colspan="5">用語集を読み込めません。</td></tr>';
    });
}

function refreshToolRegistryDatabase() {
  const body = databaseViewerElement('toolRegistryBody');
  const count = databaseViewerElement('toolRegistryCount');
  const status = databaseViewerElement('toolRegistryStatus');
  const platform = databaseViewerElement('toolRegistryPlatform');
  const params = new URLSearchParams({limit: '200'});
  if (platform && platform.value) params.set('platform', platform.value);
  if (status) status.textContent = '読み込み中...';
  return databaseViewerRequest('/viewer/databases/tool-registry?' + params.toString())
    .then((data) => {
      const items = data && Array.isArray(data.items) ? data.items : [];
      if (count) count.textContent = String(Number(data && data.total || 0));
      databaseViewerStatus(status, data, 'Tool Registry');
      if (!body) return;
      body.innerHTML = items.length ? items.map((item) => (
        '<tr><td>' + databaseViewerEscape(item.name || '-') + '</td>' +
        '<td>' + databaseViewerEscape(item.description || '-') + '</td>' +
        '<td>' + databaseViewerEscape(Array.isArray(item.platforms) ? item.platforms.join(', ') : '-') + '</td>' +
        '<td>' + databaseViewerEscape(item.source || '-') + '</td>' +
        '<td>' + databaseViewerEscape(item.created_by || '-') + '</td>' +
        '<td>' + databaseViewerEscape(item.created_at || '-') + '</td></tr>'
      )).join('') : '<tr><td colspan="6">登録Toolはありません。</td></tr>';
    })
    .catch((error) => {
      if (count) count.textContent = '0';
      if (status) status.textContent = '読込失敗: ' + String(error && error.message ? error.message : error);
      if (body) body.innerHTML = '<tr><td colspan="6">Tool Registryを読み込めません。</td></tr>';
    });
}

function bindDatabaseViewerControls() {
  const archiveRefresh = databaseViewerElement('memoryArchiveRefreshBtn');
  const glossaryRefresh = databaseViewerElement('glossaryDbRefreshBtn');
  const toolRefresh = databaseViewerElement('toolRegistryRefreshBtn');
  const toolPlatform = databaseViewerElement('toolRegistryPlatform');
  if (archiveRefresh) archiveRefresh.addEventListener('click', refreshMemoryArchiveDatabase);
  if (glossaryRefresh) glossaryRefresh.addEventListener('click', refreshGlossaryDatabase);
  if (toolRefresh) toolRefresh.addEventListener('click', refreshToolRegistryDatabase);
  if (toolPlatform) toolPlatform.addEventListener('change', refreshToolRegistryDatabase);
}

document.addEventListener('DOMContentLoaded', bindDatabaseViewerControls);
