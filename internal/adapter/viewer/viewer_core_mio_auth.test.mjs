import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8').replace(/\r\n/g, '\n');
const viewerHTML = fs.readFileSync('internal/adapter/viewer/viewer.html', 'utf8');

function sourceBetween(source, startNeedle, endNeedle) {
  const start = source.indexOf(startNeedle);
  const end = source.indexOf(endNeedle, start);
  assert.ok(start >= 0, `start not found: ${startNeedle}`);
  assert.ok(end > start, `end not found: ${endNeedle}`);
  return source.slice(start, end);
}

function htmlEscape(value) {
  return String(value || '').replace(/[&<>"']/g, (character) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  })[character]);
}

function loadRouteHarness() {
  const source = `
function coreViewerHeaders(extra = {}) {
  return Object.assign({'X-RenCrow-Client':'RenCrow_CORE','X-RenCrow-Interaction-Profile':'core-viewer'}, extra || {});
}
const VIEWER_PRIVATE_MIO_SSE_MAX_FRAME_CHARS = 1 << 20;
const VIEWER_PRIVATE_MIO_SSE_MAX_ID_CHARS = 1024;
let viewerMioOwnerCredential = 'owner-secret';
function esc(value) { return globalThis.__escape(value); }
` + sourceBetween(viewerJs, 'function viewerSendUsesPrivateMio', 'function setViewerMioAuthStatus') + `
globalThis.__routeHarness = {coreViewerHeaders, viewerMioSendHeaders, parseViewerPrivateMioMediaURL, renderViewerTextWithProtectedMedia, createViewerMioSSEParser};
`;
  const context = vm.createContext({
    URL,
    window: {location: {origin: 'https://core.example'}},
    __escape: htmlEscape,
  });
  vm.runInContext(source, context);
  return context.__routeHarness;
}

function fakeResponse(status, contentType, contentRange, bytes) {
  let canceled = false;
  const reader = {
    async read() {
      if (bytes.length === 0) return {done: true};
      const value = bytes.shift();
      return {done: false, value};
    },
    async cancel() { canceled = true; bytes.length = 0; },
    releaseLock() {},
  };
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: {get(name) {
      const values = {
        'Content-Type': contentType,
        'Content-Range': contentRange,
        'Content-Length': bytes.reduce((total, item) => total + item.byteLength, 0),
      };
      return values[name] ?? null;
    }},
    body: {
      getReader() { return reader; },
      async cancel() { await reader.cancel(); },
    },
    wasCanceled() { return canceled; },
  };
}

function loadMediaHarness(fetchHandler) {
  const requested = [];
  let revoked = '';
  let blobSequence = 0;
  const source = `
const VIEWER_PRIVATE_MIO_MEDIA_PREVIEW_MAX_BYTES = 64 * 1024 * 1024;
let viewerMioOwnerCredential = 'owner-secret';
let viewerMioAuthGeneration = 1;
let viewerMioPrivateMediaController = null;
let viewerMioActiveMediaPreview = null;
function coreViewerHeaders(extra = {}) { return Object.assign({'X-RenCrow-Client':'RenCrow_CORE','X-RenCrow-Interaction-Profile':'core-viewer'}, extra || {}); }
function parseViewerPrivateMioMediaURL(raw) {
  const origin = 'https://core.example';
  let url;
  try { url = new URL(raw, origin); } catch (_) { return null; }
  if (url.origin !== origin || url.username || url.password || url.hash || url.search !== '?to=mio') return null;
  const match = /^\\/viewer\\/(music\\/artifacts|movie\\/jobs)\\/([A-Za-z0-9_-]{1,256})\\/download$/.exec(url.pathname);
  if (!match || match[2].length > (match[1] === 'movie/jobs' ? 128 : 256)) return null;
  const path = url.pathname + url.search;
  if (String(raw).startsWith('/') ? String(raw) !== path : String(raw) !== url.origin + path) return null;
  return {id: match[2], kind: match[1] === 'movie/jobs' ? 'video' : 'audio', path, url: url.origin + path};
}
function showToast() {}
` + sourceBetween(viewerJs, 'function revokeViewerMioMediaPreview', 'function clearViewerMioPrivateFrames') +
sourceBetween(viewerJs, 'function viewerMioMediaNotice', 'async function sendViewerMessage') + `
globalThis.__mediaHarness = {loadViewerMioProtectedMedia, revokeViewerMioMediaPreview, viewerMioReadBoundedResponse, get active() { return viewerMioActiveMediaPreview; }};
`;
  const context = vm.createContext({
    URL: Object.assign(function TestURL(...args) { return new URL(...args); }, {
      createObjectURL() { return 'blob:test-' + (++blobSequence); },
      revokeObjectURL(value) { revoked = value; },
    }),
    Blob,
    AbortController,
    fetch(url, init) { requested.push({url, init}); return fetchHandler(url, init); },
    document: {
      createElement(tag) {
        return {
          tagName: tag,
          children: [],
          attrs: {},
          dataset: {},
          textContent: '',
          setAttribute(key, value) { this.attrs[key] = value; },
          appendChild(child) { child.parentNode = this; this.children.push(child); return child; },
          insertBefore(child, before) { child.parentNode = this; const index = this.children.indexOf(before); this.children.splice(index < 0 ? this.children.length : index, 0, child); return child; },
          removeAttribute(key) { delete this.attrs[key]; },
          pause() { this.paused = true; },
          load() { this.loaded = true; },
        };
      },
    },
    __requested: requested,
  });
  vm.runInContext(source, context);
  const previewParent = {
    children: [],
    appendChild(child) { child.parentNode = this; this.children.push(child); },
    removeChild(child) { this.children = this.children.filter((item) => item !== child); child.parentNode = null; },
  };
  const button = {closest(selector) { return selector === '.mb' ? previewParent : null; }};
  return {api: context.__mediaHarness, requested, previewParent, button, revoked: () => revoked};
}

test('Mio owner header stays scoped to Mio sends and protected CORE routes', () => {
  const harness = loadRouteHarness();
  assert.deepEqual(JSON.parse(JSON.stringify(harness.coreViewerHeaders())), {
    'X-RenCrow-Client': 'RenCrow_CORE',
    'X-RenCrow-Interaction-Profile': 'core-viewer',
  });
  assert.equal(harness.viewerMioSendHeaders({to: 'mio'}).Authorization, 'Bearer owner-secret');
  assert.equal(harness.viewerMioSendHeaders({to: ''}).Authorization, 'Bearer owner-secret');
  assert.equal(Object.hasOwn(harness.viewerMioSendHeaders({to: 'shiro'}), 'Authorization'), false);

  const accepted = harness.parseViewerPrivateMioMediaURL('/viewer/movie/jobs/job_opaque/download?to=mio');
  assert.deepEqual(JSON.parse(JSON.stringify(accepted)), {
    id: 'job_opaque', kind: 'video', path: '/viewer/movie/jobs/job_opaque/download?to=mio',
    url: 'https://core.example/viewer/movie/jobs/job_opaque/download?to=mio',
  });
  for (const url of [
    'https://evil.example/viewer/movie/jobs/job_opaque/download?to=mio',
    'https://user:password@core.example/viewer/movie/jobs/job_opaque/download?to=mio',
    '/viewer/movie/jobs/job_opaque/download?to=mio&token=secret',
    '/viewer/movie/jobs/%2e%2e/download?to=mio',
    '/viewer/movie/jobs/job_opaque/download/extra?to=mio',
    '/viewer/movie/jobs/job_opaque/download?to=shiro',
  ]) assert.equal(harness.parseViewerPrivateMioMediaURL(url), null, url);
  const markup = harness.renderViewerTextWithProtectedMedia('[movie](/viewer/movie/jobs/job_opaque/download?to=mio)');
  assert.match(markup, /class="viewer-private-media"/);
  assert.match(markup, /data-viewer-media-path="\/viewer\/movie\/jobs\/job_opaque\/download\?to=mio"/);
  assert.doesNotMatch(harness.renderViewerTextWithProtectedMedia('[external](https://evil.example/movie.mp4)'), /viewer-private-media/);
});

test('the actual send request carries the owner bearer only for Mio', async () => {
  const requested = [];
  const diagnostics = [];
  let resolvePrivateResponse;
  const source = `
let speechCancelGeneration = 0;
let viewerMioOwnerCredential = '';
let viewerMioAuthGeneration = 0;
let viewerMioPrivateRetryDisabled = false;
let viewerMioLastEventID = '';
const viewerControl = {clientId:'test-tab'}; const viewerForegroundPresence = {ready:true};
function coreViewerHeaders(extra = {}) { return Object.assign({'X-RenCrow-Client':'RenCrow_CORE','X-RenCrow-Interaction-Profile':'core-viewer'}, extra || {}); }
function buildViewerPresenceReference() { return {protocol_version:2}; }
function buildViewerSendRequest(message) { return {message, to: globalThis.__recipient}; }
let latencyClock = 0;
function nowLatencyMS() { return ++latencyClock; }
function recordLatencyMetric(kind, point, options) { globalThis.__diagnostics.push({kind, point, options}); }
function short(value) { return String(value || ''); }
function acceptSpeechCancelCapability() {}
function setViewerMioAuthStatus() {}
` + sourceBetween(viewerJs, 'function viewerSendUsesPrivateMio', 'function setViewerMioAuthStatus') + `
function cancelViewerMioPrivateStream() {}
function revokeViewerMioMediaPreview() {}
function clearViewerMioPrivateFrames() {}
` + sourceBetween(viewerJs, 'function logoutViewerMioOwner', 'function scheduleViewerMioReconnect') + `
` + sourceBetween(viewerJs, 'async function sendViewerMessage', 'async function requestRepairFromChat') + `
viewerMioOwnerCredential = 'owner-secret';
globalThis.__sendHarness = {send: sendViewerMessage, logout: logoutViewerMioOwner};
`;
  const context = vm.createContext({
    __recipient: 'mio',
    __diagnostics: diagnostics,
    document: {getElementById() { return null; }},
    fetch(url, init) {
      requested.push({url, init});
      if (requested.length === 1) return new Promise((resolve) => { resolvePrivateResponse = resolve; });
      return Promise.resolve({ok:true, status:204, json:async () => null});
    },
  });
  vm.runInContext(source, context);
  const privateSend = context.__sendHarness.send('private request');
  assert.equal(diagnostics.length, 0, 'private send start does not enter shared latency diagnostics');
  context.__sendHarness.logout();
  resolvePrivateResponse({ok:true, status:204, json:async () => null});
  await privateSend;
  assert.equal(diagnostics.length, 0, 'logout before the response leaves no private send diagnostics when SSE delivered no events');
  context.__recipient = 'shiro';
  await context.__sendHarness.send('ordinary request');
  assert.equal(requested[0].init.headers.Authorization, 'Bearer owner-secret');
  assert.equal(requested[1].init.headers.Authorization, undefined);
  assert.equal(JSON.parse(requested[0].init.body).message, 'private request');
  assert.doesNotMatch(requested[0].init.body, /owner-secret/);
  assert.deepEqual(diagnostics.map(({point}) => point), ['viewer_send_start', 'viewer_send_response'], 'public diagnostics remain intact');
  assert.doesNotMatch(JSON.stringify(diagnostics), /private request|owner-secret/);
});

test('authenticated Mio SSE parser handles chunked UTF-8, CRLF, multiline data, IDs and reset frames within a frame bound', () => {
  const harness = loadRouteHarness();
  const frames = [];
  const parser = harness.createViewerMioSSEParser((frame) => frames.push(frame));
  const payload = 'id: evt_01234567-89ab-7abc-8def-0123456789ab\r\n: heartbeat\r\nevent: message\r\ndata: {"content":"split 🧪"}\r\ndata: second-line\r\nretry: 1500\r\n\r\nid: reset\nevent: reset\ndata: {"reason":"coverage"}\n\nid:\n\n';
  const bytes = new TextEncoder().encode(payload);
  const decoder = new TextDecoder();
  for (let offset = 0; offset < bytes.length; offset++) {
    parser.push(decoder.decode(bytes.subarray(offset, offset + 1), {stream: true}));
  }
  parser.push(decoder.decode(), true);
  parser.finish();
  const observed = JSON.parse(JSON.stringify(frames));
  assert.equal(observed.length, 3);
  assert.deepEqual(observed[0], {
    data: '{"content":"split 🧪"}\nsecond-line',
    event: 'message', id: 'evt_01234567-89ab-7abc-8def-0123456789ab', hasID: true,
  });
  assert.deepEqual(observed[1], {data: '{"reason":"coverage"}', event: 'reset', id: 'reset', hasID: true});
  assert.deepEqual(observed[2], {data: '', event: 'message', id: '', hasID: true});
  assert.throws(() => harness.createViewerMioSSEParser(() => {}).push('data: ' + 'x'.repeat((1 << 20) + 2)), /frame limit/);
});

test('native private stream uses owner proof, stops on 401/403 and rejects late events after logout', async () => {
  const requested = [];
  const timers = [];
  const events = [];
  const elements = {
    mioOwnerCredential: {value: 'owner-secret'},
    mioOwnerLogout: {hidden: true},
    mioOwnerAuthStatus: {textContent: '', className: ''},
  };
  const source = `
const VIEWER_PRIVATE_MIO_SSE_MAX_FRAME_CHARS = 1 << 20;
const VIEWER_PRIVATE_MIO_SSE_MAX_ID_CHARS = 1024;
const VIEWER_PRIVATE_MIO_SSE_DECODE_CHUNK_BYTES = 64 * 1024;
const MAX_SEEN_EVENTS = 4000;
let viewerMioOwnerCredential = 'owner-secret';
let viewerMioAuthGeneration = 2;
let viewerMioPrivateStreamController = null;
let viewerMioPrivateMediaController = null;
let viewerMioReconnectTimer = null;
let viewerMioReconnectDelayMS = 1000;
let viewerMioPrivateRetryDisabled = false;
let viewerMioLastEventID = 'evt_01234567-89ab-7abc-8def-0123456789ab';
const viewerMioPrivateEvents = new Map();
const viewerMioPrivateTaskIDs = new Set();
const viewerMioPrivateSessionIDs = new Set();
const viewerMioPrivateAgentChanges = [];
const viewerMioPrivateDebugTraces = new Set();
let viewerMioActiveMediaPreview = null;
const AGENTS = ['mio','shiro'];
const state = {logs: [], tasks: {}, sessions: {}, agents: {mio: {id:'mio',preview:'public'}}, openTasks: {}, ops: {}, debug: {thinkTrace: []}};
const seenEventKeys = new Set();
const seenEventQueue = [];
const seenTaskNotificationKeys = new Set();
const idleLiveRenderedLog = [];
let derivedDirty = false;
function eventKey(event) { return String(event.type) + ':' + String(event.task_id || event.session_id || ''); }
function taskNotificationEventKey(event) { return event.task_id || ''; }
function coreViewerHeaders(extra = {}) { return Object.assign({'X-RenCrow-Client':'RenCrow_CORE','X-RenCrow-Interaction-Profile':'core-viewer'}, extra || {}); }
function ingestEvent(event, isPrivate) { globalThis.__events.push({event, isPrivate}); }
function resetTimeline() {}
const chatAudioSync = {resetChat() {}, resetIdleChat() {}};
function resetIdleChatLiveForControlChange() {}
` + sourceBetween(viewerJs, 'function createViewerMioSSEParser', 'function setViewerMioAuthStatus') +
sourceBetween(viewerJs, 'function setViewerMioAuthStatus', 'function viewerMioMediaNotice') + `
globalThis.__streamHarness = {openViewerMioPrivateStream, ingestViewerMioPrivateSSEFrame, logoutViewerMioOwner, noteViewerMioPrivateEvent, captureViewerMioPrivateAgentState, get generation() {return viewerMioAuthGeneration;}, get retryDisabled() {return viewerMioPrivateRetryDisabled;}, set retryDisabled(value) {viewerMioPrivateRetryDisabled = value;}, get credential() {return viewerMioOwnerCredential;}, get lastID() {return viewerMioLastEventID;}, state};
`;
  const context = vm.createContext({
    AbortController,
    TextDecoder,
    Uint8Array,
    URL,
    console,
    __events: events,
    document: {getElementById(id) { return elements[id] || null; }},
    fetch(url, init) {
      requested.push({url, init});
      return Promise.resolve(context.__fetchResponse);
    },
    setTimeout(callback, delay) { timers.push({callback, delay}); return timers.length; },
    clearTimeout() {},
    __fetchResponse: {status: 401, ok: false, headers: {get() { return null; }}, body: {async cancel() {}}},
  });
  vm.runInContext(source, context);
  const harness = context.__streamHarness;
  await harness.openViewerMioPrivateStream(2);
  assert.equal(requested[0].url, '/viewer/events?to=mio');
  assert.equal(requested[0].init.headers.Authorization, 'Bearer owner-secret');
  assert.equal(requested[0].init.headers['Last-Event-ID'], 'evt_01234567-89ab-7abc-8def-0123456789ab');
  assert.equal(harness.retryDisabled, true);
  assert.equal(timers.length, 0);

  context.__fetchResponse = {status: 403, ok: false, headers: {get() { return null; }}, body: {async cancel() {}}};
  harness.retryDisabled = false;
  await harness.openViewerMioPrivateStream(2);
  assert.equal(harness.retryDisabled, true);
  assert.equal(timers.length, 0);

  const deltaEventID = 'evt_11234567-89ab-7abc-8def-0123456789ab';
  const nextEventID = 'evt_21234567-89ab-7abc-8def-0123456789ab';
  const streamBytes = new TextEncoder().encode(
    'id: ' + deltaEventID + '\ndata: {"type":"agent.response_delta","from":"mio","to":"user","content":"partial fragment"}\n\n'
    + 'id: ' + nextEventID + '\ndata: {"type":"agent.response","from":"mio","to":"user","content":"complete result"}\n\n'
  );
  let streamRead = false;
  context.__fetchResponse = {
    status: 200,
    ok: true,
    headers: {get(name) { return name.toLowerCase() === 'content-type' ? 'text/event-stream; charset=utf-8' : null; }},
    body: {getReader() { return {
      async read() {
        if (streamRead) return {done:true};
        streamRead = true;
        return {done:false, value:streamBytes};
      },
      async cancel() {}, releaseLock() {},
    }; }},
  };
  harness.retryDisabled = false;
  await harness.openViewerMioPrivateStream(2);
  assert.equal(events.length, 1);
  assert.equal(events[0].isPrivate, true);
  assert.equal(events[0].event.type, 'agent.response', 'streaming deltas are ignored by the timeline ingress');
  assert.equal(events[0].event.content, 'complete result', 'the durable final response remains visible');
  assert.equal(harness.lastID, nextEventID);
  assert.equal(timers.length, 1);
  assert.equal(elements.mioOwnerCredential.value, '');
  assert.equal(elements.mioOwnerLogout.hidden, false);

  timers[0].callback();
  await new Promise((resolve) => globalThis.setTimeout(resolve, 0));
  assert.equal(requested.length, 4);
  assert.equal(requested[3].init.headers['Last-Event-ID'], nextEventID, 'reconnect sends the canonical cursor from the private stream');

  const oldGeneration = harness.generation;
  harness.logoutViewerMioOwner();
  harness.ingestViewerMioPrivateSSEFrame({data: '{"type":"message.received"}', event: 'message', id: '', hasID: false}, oldGeneration);
  assert.equal(events.length, 1, 'a late frame after logout is ignored');
  assert.equal(harness.credential, '');

  const escapedStream = sourceBetween(viewerJs, 'function connect()', '// === STT (Speech-to-Text) realtime PCM16 streaming ===');
  assert.match(escapedStream, /new EventSource\('\/viewer\/events'\)/);
  assert.doesNotMatch(escapedStream, /Authorization|viewerMioOwnerCredential|\?to=mio/);
  assert.doesNotMatch(viewerHTML, /localStorage[^\n]*mioOwnerCredential|name="mioOwnerCredential"/);
});

test('private Mio events bypass shared latency diagnostics while public events still record', () => {
  const diagnostics = [];
  const source = `
const MAX_LOGS = 120; const MAX_SEEN_EVENTS = 4000; const AGENTS = ['mio','shiro'];
const state = {logs: [], tasks: {}, sessions: {}, agents: {}, openTasks: {}, ops: {}, debug: {thinkTrace: []}};
const seenEventKeys = new Set(); const seenTaskNotificationKeys = new Set();
const viewerMioPrivateEvents = new Map();
let derivedDirty = false;
function handleViewerActiveControlEvent() {}
function isCurrentIdleChatPlaybackEvent() { return true; }
function isStaleIdleChatEvent() { return false; }
function eventKey(event) { return String(event.type) + ':' + String(event.task_id || event.session_id || ''); }
function rememberEventKey(key) { seenEventKeys.add(key); }
function captureViewerMioPrivateAgentState() { return null; }
function nowLatencyMS() { return 17; }
function noteViewerEventLatency(event) { globalThis.__diagnostics.push(event); }
function upsertSession() {} function upsertTask() {} function updateAgents() {}
function addMsgToTimeline() {} function addIdleMsgToTimeline() {} function addIdleSummaryToTimeline() {}
function noteViewerMioPrivateEvent(event) { viewerMioPrivateEvents.set(eventKey(event), event); }
function handleTTSAudioEvent() {} function updateLive2DOnMessage() {}
` + sourceBetween(viewerJs, 'function ingestEvent(ev, privateMioEvent = false)', 'function normalizeNotificationAssignee') + `
globalThis.__ingestHarness = {ingestEvent, state, viewerMioPrivateEvents};
`;
  const context = vm.createContext({__diagnostics: diagnostics});
  vm.runInContext(source, context);
  const publicEvent = {type: 'message.received', from: 'user', to: 'shiro', task_id: 'public-task', content: 'public content'};
  const privateEvent = {type: 'message.received', from: 'user', to: 'mio', task_id: 'private-task', session_id: 'cmd:ren', content: 'private owner content'};
  context.__ingestHarness.ingestEvent(publicEvent, false);
  context.__ingestHarness.ingestEvent(privateEvent, true);
  assert.deepEqual(diagnostics, [publicEvent], 'private replay/live events never reach shared diagnostics');
  assert.equal(context.__ingestHarness.state.logs.length, 2, 'both event types still reach the viewer timeline');
  assert.equal(context.__ingestHarness.viewerMioPrivateEvents.size, 1, 'the private event remains tracked for logout cleanup');
});

test('logout removes private Mio rows and derived previews, clears the credential and revokes media state', () => {
  const elements = {
    mioOwnerCredential: {value: 'owner-secret'},
    mioOwnerLogout: {hidden: false},
    mioOwnerAuthStatus: {textContent: '', className: ''},
  };
  const event = {type: 'message.received', from: 'user', to: 'mio', task_id: 'private-task', session_id: 'cmd:ren', timestamp: '2026-11-28T00:00:00Z', content: 'private body'};
  const key = 'message.received:private-task';
  const source = `
const VIEWER_PRIVATE_MIO_SSE_MAX_FRAME_CHARS = 1 << 20;
const VIEWER_PRIVATE_MIO_SSE_MAX_ID_CHARS = 1024;
const VIEWER_PRIVATE_MIO_SSE_DECODE_CHUNK_BYTES = 64 * 1024;
const MAX_SEEN_EVENTS = 4000;
let viewerMioOwnerCredential = 'owner-secret'; let viewerMioAuthGeneration = 1;
let viewerMioPrivateStreamController = null; let viewerMioPrivateMediaController = null;
let viewerMioReconnectTimer = null; let viewerMioReconnectDelayMS = 1000; let viewerMioPrivateRetryDisabled = false;
let viewerMioLastEventID = 'evt_01234567-89ab-7abc-8def-0123456789ab';
const viewerMioPrivateEvents = new Map(); const viewerMioPrivateTaskIDs = new Set(); const viewerMioPrivateSessionIDs = new Set();
const viewerMioPrivateAgentChanges = []; const viewerMioPrivateDebugTraces = new Set(); const viewerMioPrivateIdleRenderEntries = new Set();
let viewerMioActiveMediaPreview = null; const AGENTS = ['mio','shiro'];
const state = {logs: [globalThis.__event], tasks: {'private-task': {preview: 'private body'}}, sessions: {'cmd:ren': {lastUserMessage: 'private body'}}, agents: {mio: {id:'mio',preview:'public'}}, openTasks: {mio: {'private-task': {text:'private body'}}}, ops: {}, debug: {thinkTrace: [globalThis.__trace]}};
viewerMioPrivateDebugTraces.add(globalThis.__trace);
const seenEventKeys = new Set([globalThis.__key]); const seenEventQueue = [globalThis.__key]; const seenTaskNotificationKeys = new Set();
const idleLiveRenderedLog = [];
let derivedDirty = false;
function eventKey(event) { return String(event.type) + ':' + String(event.task_id || event.session_id || ''); }
function taskNotificationEventKey(event) { return event.task_id || ''; }
function resetTimeline() { globalThis.__resetCount++; }
const chatAudioSync = {resetChat() {}, resetIdleChat() {}};
function resetIdleChatLiveForControlChange() {}
` + sourceBetween(viewerJs, 'function createViewerMioSSEParser', 'function setViewerMioAuthStatus') +
sourceBetween(viewerJs, 'function setViewerMioAuthStatus', 'function viewerMioMediaNotice') + `
globalThis.__logoutHarness = {logoutViewerMioOwner, noteViewerMioPrivateEvent, captureViewerMioPrivateAgentState, state, get credential() {return viewerMioOwnerCredential;}, get lastID() {return viewerMioLastEventID;}};
`;
  const context = vm.createContext({
    AbortController, URL, console,
    __event: event, __key: key, __trace: {text:'private body'}, __resetCount: 0,
    document: {getElementById(id) { return elements[id] || null; }},
  });
  vm.runInContext(source, context);
  const harness = context.__logoutHarness;
  const before = harness.captureViewerMioPrivateAgentState(event);
  harness.state.agents.mio.preview = 'private body';
  harness.noteViewerMioPrivateEvent(event, before, -1);
  harness.state.debug.thinkTrace.push(context.__trace);
  context.__logoutHarness.logoutViewerMioOwner();
  assert.equal(harness.credential, '');
  assert.equal(harness.lastID, '');
  assert.equal(elements.mioOwnerCredential.value, '');
  assert.equal(elements.mioOwnerLogout.hidden, true);
  assert.equal(harness.state.logs.length, 0);
  assert.equal(harness.state.tasks['private-task'], undefined);
  assert.equal(harness.state.sessions['cmd:ren'], undefined);
  assert.equal(harness.state.openTasks.mio['private-task'], undefined);
  assert.equal(harness.state.agents.mio.preview, 'public');
  assert.equal(harness.state.debug.thinkTrace.length, 0);
  assert.equal(context.__resetCount, 1);
});

test('fixed composer height tracks auth wrapping, logout, textarea, attachments and viewport reflow', () => {
  let measuredHeight = 188.5;
  let observedComposer = null;
  let resizeCallback = null;
  const properties = new Map();
  const windowListeners = new Map();
  const composer = {getBoundingClientRect() { return {height: measuredHeight}; }};
  const source = sourceBetween(viewerJs, 'let viewerComposerResizeObserver = null;', 'function coreViewerHeaders') + `
globalThis.__composerHarness = {bindViewerComposerHeight, get observer() { return viewerComposerResizeObserver; }};
`;
  class FakeResizeObserver {
    constructor(callback) { resizeCallback = callback; }
    observe(element) { observedComposer = element; }
  }
  const context = vm.createContext({
    document: {
      documentElement: {style: {
        getPropertyValue(name) { return properties.get(name) || ''; },
        setProperty(name, value) { properties.set(name, String(value)); },
      }},
      querySelector(selector) { return selector === '.input-bar' ? composer : null; },
    },
    ResizeObserver: FakeResizeObserver,
    window: {addEventListener(name, callback) { windowListeners.set(name, callback); }},
  });
  vm.runInContext(source, context);
  const harness = context.__composerHarness;
  harness.bindViewerComposerHeight();
  assert.equal(properties.get('--input-h'), '189px', 'the initial variable stores the measured outer height');
  assert.equal(observedComposer, composer);
  assert.ok(harness.observer);

  for (const [scenario, height] of [
    ['wrapped connection status', 277.2],
    ['logout status shrink', 158.1],
    ['expanded textarea', 320.4],
    ['attachment tray', 346],
    ['compact or orientation reflow', 218.6],
  ]) {
    measuredHeight = height;
    resizeCallback();
    assert.equal(properties.get('--input-h'), Math.ceil(height) + 'px', scenario);
  }

  measuredHeight = 204.3;
  windowListeners.get('resize')();
  assert.equal(properties.get('--input-h'), '205px', 'window resize also refreshes the measurement');
});

test('protected media probes size before fetch, enforces the bounded Blob and revokes it on replacement/logout', async () => {
  const calls = [];
  const small = loadMediaHarness(async (_url, init) => {
    calls.push(init.headers.Range);
    if (calls.length === 1) return fakeResponse(206, 'video/mp4', 'bytes 0-0/4', [new Uint8Array([0])]);
    return fakeResponse(206, 'video/mp4', 'bytes 0-3/4', [new Uint8Array([0, 0]), new Uint8Array([0, 0])]);
  });
  assert.equal(small.requested.length, 0, 'no automatic media fetch occurs before an explicit load');
  await small.api.loadViewerMioProtectedMedia(small.button, {path: '/viewer/movie/jobs/job_opaque/download?to=mio', kind: 'video'});
  assert.deepEqual(calls, ['bytes=0-0', 'bytes=0-3']);
  assert.equal(small.requested[0].url, '/viewer/movie/jobs/job_opaque/download?to=mio');
  assert.equal(small.requested[0].init.headers.Authorization, 'Bearer owner-secret');
  assert.equal(small.api.active.player.tagName, 'video');
  assert.equal(small.api.active.player.preload, 'metadata');
  const blobURL = small.api.active.blobURL;
  small.api.revokeViewerMioMediaPreview();
  assert.equal(small.revoked(), blobURL);
  assert.equal(small.previewParent.children.length, 0);

  const large = loadMediaHarness(async () => fakeResponse(206, 'video/mp4', 'bytes 0-0/67108865', [new Uint8Array([0])]));
  await large.api.loadViewerMioProtectedMedia(large.button, {path: '/viewer/movie/jobs/job_opaque/download?to=mio', kind: 'video'});
  assert.equal(large.requested.length, 1, 'oversized objects are rejected after the one byte probe');
  assert.match(large.api.active.preview.children[0].textContent, /larger than 64 MiB/);
  assert.equal(large.api.active.player, null);

  const bounded = loadMediaHarness(async () => { throw new Error('reader-only test must not fetch'); });
  let canceled = false;
  const overflow = {
    headers: {get() { return null; }},
    body: {getReader() {
      let index = 0;
      return {
        async read() { return index++ === 0 ? {done:false, value:new Uint8Array(3)} : {done:false, value:new Uint8Array(2)}; },
        async cancel() { canceled = true; },
        releaseLock() {},
      };
    }},
  };
  await assert.rejects(bounded.api.viewerMioReadBoundedResponse(overflow, 4, 'video/mp4'), /64 MiB preview limit/);
  assert.equal(canceled, true, 'an over-cap body is canceled while reading');
});
