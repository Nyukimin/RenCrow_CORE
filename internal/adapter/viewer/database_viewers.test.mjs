import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

function viewerContext(responseFor) {
  const elements = new Map();
  const element = (id) => {
    if (!elements.has(id)) {
      elements.set(id, {
        id,
        innerHTML: '',
        textContent: '',
        title: '',
        value: '',
        addEventListener() {},
      });
    }
    return elements.get(id);
  };
  const context = {
    URLSearchParams,
    document: {
      getElementById: element,
      addEventListener() {},
    },
    fetch: async (path) => ({
      ok: true,
      json: async () => responseFor(path),
      text: async () => '',
    }),
  };
  vm.runInNewContext(
    fs.readFileSync('internal/adapter/viewer/assets/js/tabs/database-viewers.js', 'utf8'),
    context,
  );
  return {context, element};
}

test('archive Viewer renders only archive endpoint data and escapes cells', async () => {
  let requested = '';
  const {context, element} = viewerContext((path) => {
    requested = path;
    return {
      available: true,
      total: 1,
      db_path: '/state/memory_archive.db',
      items: [{
        start_time: '2026-08-09',
        session_id: 'session-1',
        domain: 'movie',
        summary: '<script>bad()</script>',
        keywords: ['映画'],
        is_novel: true,
      }],
    };
  });
  element('memoryArchiveSession').value = 'session-1';
  element('memoryArchiveDomain').value = 'movie';
  await context.refreshMemoryArchiveDatabase();
  assert.match(requested, /^\/viewer\/databases\/conversation-archive\?/);
  assert.match(requested, /session_id=session-1/);
  assert.equal(element('memoryArchiveCount').textContent, '1');
  assert.match(element('memoryArchiveBody').innerHTML, /&lt;script&gt;bad\(\)&lt;\/script&gt;/);
  assert.doesNotMatch(element('memoryArchiveBody').innerHTML, /<script>/);
});

test('Glossary and Tool Registry Viewers keep their data in separate tables', async () => {
  const {context, element} = viewerContext((path) => {
    if (path.startsWith('/viewer/databases/glossary')) {
      return {available: true, total: 1, items: [{term: 'D0', explanation: 'root', category: 'movie', source: 'spec'}]};
    }
    return {available: false, total: 0, error: 'database is not configured', items: []};
  });
  await context.refreshGlossaryDatabase();
  await context.refreshToolRegistryDatabase();
  assert.match(element('glossaryDbBody').innerHTML, /D0/);
  assert.doesNotMatch(element('toolRegistryBody').innerHTML, /D0/);
  assert.equal(element('toolRegistryStatus').textContent, 'database is not configured');
  assert.match(element('toolRegistryBody').innerHTML, /登録Toolはありません/);
});

test('Tool Registry Viewer renders effective tool origin and registry source', async () => {
  const {context, element} = viewerContext(() => ({
    available: true,
    total: 1,
    items: [{name: 'browser.run', description: 'browser', origin: 'rencrow_tools', source: 'builtin'}],
  }));
  await context.refreshToolRegistryDatabase();
  assert.equal(element('toolRegistryCount').textContent, '1');
  assert.match(element('toolRegistryBody').innerHTML, /rencrow_tools/);
  assert.match(element('toolRegistryBody').innerHTML, /builtin/);
});

test('DB Catalog renders summary and escaped metadata in its own panel', async () => {
  let requested = '';
  const {context, element} = viewerContext((path) => {
    requested = path;
    return {
      available: true,
      total: 20,
      summary: {available: 2, unavailable: 3, restricted: 14, blocked: 1},
      items: [{name: '<catalog>', status: 'restricted', owner: 'CORE', categories: ['memory'], safe_operations: ['describe'], tool_id: '', sensitivity: 'private', reason: 'owner_service_only', physical_key: 'storage.databases.conversation_l1'}],
    };
  });
  await context.refreshDataCapabilityCatalog();
  assert.equal(requested, '/viewer/databases/catalog');
  assert.equal(element('dbCatalogAvailable').textContent, '2');
  assert.equal(element('dbCatalogUnavailable').textContent, '3');
  assert.equal(element('dbCatalogRestricted').textContent, '14');
  assert.equal(element('dbCatalogBlocked').textContent, '1');
  assert.match(element('dbCatalogBody').innerHTML, /&lt;catalog&gt;/);
  assert.match(element('dbCatalogBody').innerHTML, />restricted</);
  assert.doesNotMatch(element('glossaryDbBody').innerHTML, /catalog/);
});

test('DB Catalog has separate desktop and mobile navigation with responsive summary', () => {
  const html = fs.readFileSync('internal/adapter/viewer/viewer.html', 'utf8');
  const viewerJS = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const css = fs.readFileSync('internal/adapter/viewer/assets/css/viewer.css', 'utf8');
  assert.match(html, /<option value="db-catalog">DB Catalog<\/option>/);
  assert.match(html, /data-tab="db-catalog"[^>]*>DB Catalog<\/button>/);
  assert.match(html, /id="panel-db-catalog"/);
  assert.match(viewerJS, /'db-catalog': document\.getElementById\('panel-db-catalog'\)/);
  assert.match(viewerJS, /tab === 'db-catalog' && typeof refreshDataCapabilityCatalog === 'function'/);
  assert.match(css, /\.db-catalog-summary\{grid-template-columns:repeat\(4/);
  assert.match(css, /@media \(max-width: 640px\)[\s\S]*\.db-catalog-summary\{grid-template-columns:repeat\(2/);
});

test('Memory database panels keep Knowledge Memory and Archive out of Conversation L1', () => {
  const html = fs.readFileSync('internal/adapter/viewer/viewer.html', 'utf8');
  const viewerJS = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const memoryJS = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/memory.js', 'utf8');
  const panel = (id) => {
    const start = html.indexOf(`<section id="panel-${id}"`);
    assert.ok(start >= 0, `missing panel ${id}`);
    const end = html.indexOf('</section>', start);
    assert.ok(end > start, `unclosed panel ${id}`);
    return html.slice(start, end);
  };
  const l1 = panel('memory');
  const archive = panel('memory-archive');
  const knowledge = panel('knowledge-memory');
  assert.doesNotMatch(l1, /knowledgeMemoryBody|knowledgeMemoryDetail|memoryArchiveBody|memoryArchiveSession|memoryL2Count/);
  assert.doesNotMatch(archive, /knowledgeMemoryBody|knowledgeMemoryDetail/);
  assert.doesNotMatch(knowledge, /memoryArchiveBody|memoryArchiveSession/);
  assert.match(viewerJS, /'knowledge-memory': document\.getElementById\('panel-knowledge-memory'\)/);
  assert.match(viewerJS, /const memoryDbTabs = new Set\(\[[^\]]*'knowledge-memory'/s);
  assert.match(viewerJS, /tab === 'knowledge-memory' && typeof refreshKnowledgeMemoryLedger === 'function'/);

  const renderStart = memoryJS.indexOf('function renderMemoryLayers');
  const renderEnd = memoryJS.indexOf('function refreshMemoryLayers', renderStart);
  assert.ok(renderStart >= 0 && renderEnd > renderStart, 'memory layer renderer boundary is missing');
  assert.doesNotMatch(memoryJS.slice(renderStart, renderEnd), /\b[lL]2\b|memoryL2Count/);
  const snapshotStart = memoryJS.indexOf('function refreshMemorySnapshot');
  const snapshotEnd = memoryJS.indexOf('\nfunction postMemoryAction', snapshotStart);
  assert.ok(snapshotStart >= 0 && snapshotEnd > snapshotStart, 'memory snapshot refresh boundary is missing');
  assert.doesNotMatch(memoryJS.slice(snapshotStart, snapshotEnd), /refreshKnowledgeMemoryLedger\(\)/);
});
