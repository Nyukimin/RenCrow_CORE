import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

const viewerJS = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');

function sourceBetween(startNeedle, endNeedle) {
  const start = viewerJS.indexOf(startNeedle);
  const end = viewerJS.indexOf(endNeedle, start);
  assert.ok(start >= 0, `start not found: ${startNeedle}`);
  assert.ok(end > start, `end not found: ${endNeedle}`);
  return viewerJS.slice(start, end);
}

function loadPresenceHarness(fetch) {
  const source = `
const viewerControl = {clientId: 'viewer-tab-test'};
const viewerForegroundPresence = {
  protocolVersion: 2, surface: 'other', runtimeEpoch: '', registrationReceipt: '',
  updateSeq: 0, claimSeq: 0, stateRevision: 0, registered: false, claimed: false,
  ready: false, requestInFlight: false, requestSettled: null, sendInFlight: false, releasePromise: null, heartbeatTimer: null,
};
` + sourceBetween('function coreViewerHeaders(', 'function handleViewerActiveControlEvent') + `
globalThis.__presenceHarness = {viewerControl, viewerForegroundPresence, maintainViewerForegroundPresence, releaseViewerForegroundPresence, buildViewerPresenceReference, coreViewerHeaders, document};
`;
  const elements = new Map();
  const context = {
    document: {
      visibilityState: 'visible',
      getElementById(id) {
        if (!elements.has(id)) elements.set(id, {disabled: false, title: '', textContent: '', className: ''});
        return elements.get(id);
      },
    },
    fetch,
    console: {warn() {}},
    applyIdleChatControlSnapshot() {},
    invalidateIdleChatControlSnapshot() {},
  };
  vm.createContext(context);
  vm.runInContext(source, context);
  context.__presenceHarness.elements = elements;
  return context.__presenceHarness;
}

function loadSendHarness(fetch, ready = true, attachments = []) {
  const source = `
const viewerForegroundPresence = {ready: ${JSON.stringify(ready)}};
const viewerControl = {clientId: 'viewer-tab-test'};
const speechCancelGeneration = 0;
function buildViewerSendRequest(message) { return {message}; }
function buildViewerPresenceReference() {
  if (!viewerForegroundPresence.ready) throw new Error('CORE foreground presence is not ready');
  return {protocol_version: 2, runtime_epoch: 'epoch-1', registration_receipt: 'receipt-1', claim_seq: 7};
}
function coreViewerHeaders(extra = {}) {
  return Object.assign({'X-RenCrow-Client':'RenCrow_CORE','X-RenCrow-Interaction-Profile':'core-viewer'}, extra || {});
}
` + sourceBetween('async function sendViewerMessage(', 'async function requestRepairFromChat') + `
globalThis.__sendHarness = {sendViewerMessage};
`;
  const forms = [];
  class FakeFormData {
    constructor() { this.entries = []; forms.push(this); }
    append(key, value) { this.entries.push([String(key), value]); }
  }
  const context = {
    fetch,
    FormData: FakeFormData,
    attachments,
    acceptSpeechCancelCapability() {},
    scheduleViewerEventReconnect() {},
  };
  vm.createContext(context);
  vm.runInContext(source, context);
  context.__sendHarness.forms = forms;
  return context.__sendHarness;
}

function loadIdleFenceHarness() {
  const source = `
const state = {idleChat: {controlSnapshot: null}};
const viewerForegroundPresence = {ready: true, runtimeEpoch: 'epoch-1'};
let audioResets = 0;
let liveResets = 0;
const chatAudioSync = {resetIdleChat() { audioResets += 1; }};
function resetIdleChatLiveForControlChange() { liveResets += 1; }
function isIdleChatSessionId(sessionID) { return String(sessionID || '').startsWith('idle-') || String(sessionID || '').startsWith('forecast-') || String(sessionID || '').startsWith('story-'); }
` + sourceBetween('function idlePlaybackEventEnvelope(', 'function handleTTSAudioEvent') + `
globalThis.__idleFenceHarness = {
  state,
  applyIdleChatControlSnapshot,
  isCurrentIdleChatPlaybackEvent,
  resetCounts: () => ({audioResets, liveResets}),
};
`;
  const context = {};
  vm.createContext(context);
  vm.runInContext(source, context);
  return context.__idleFenceHarness;
}

test('CORE Viewer claims the V2 other surface with a profile checked request', async () => {
  const calls = [];
  const presence = loadPresenceHarness(async (path, init) => {
    const body = JSON.parse(init.body);
    calls.push({path, init, body});
    const response = body.action === 'register'
      ? {ok: true, protocol_version: 2, runtime_epoch: 'epoch-1', registration_receipt: 'receipt-1', last_update_seq: 0}
      : {ok: true, protocol_version: 2, runtime_epoch: 'epoch-1', registration_receipt: 'receipt-1', accepted_update_seq: 1, current_claim_seq: 1, state_revision: 1, playback_generation: 4, playback_state: 'stopped', playback_active: false, foreground_ready: true};
    return {ok: true, json: async () => response};
  });

  await presence.maintainViewerForegroundPresence();

  assert.equal(calls.length, 2);
  assert.deepEqual(calls.map((call) => call.body.action), ['register', 'claim']);
  assert.equal(calls[0].body.surface, undefined);
  assert.equal(calls[1].body.surface, 'other');
  assert.equal(calls[1].body.update_seq, 1);
  assert.equal(calls[1].init.headers['X-RenCrow-Client'], 'RenCrow_CORE');
  assert.equal(calls[1].init.headers['X-RenCrow-Interaction-Profile'], 'core-viewer');
  assert.equal(presence.viewerForegroundPresence.ready, true);
  assert.deepEqual(JSON.parse(JSON.stringify(presence.buildViewerPresenceReference())), {
    protocol_version: 2,
    runtime_epoch: 'epoch-1',
    registration_receipt: 'receipt-1',
    claim_seq: 1,
  });
});

test('pagehide waits for an in-flight claim and releases its accepted sequence', async () => {
  const calls = [];
  let resolveClaim;
  let claimStartedResolve;
  const claimStarted = new Promise((resolve) => { claimStartedResolve = resolve; });
  const presence = loadPresenceHarness(async (path, init) => {
    const body = JSON.parse(init.body);
    calls.push({path, init, body});
    if (body.action === 'register') {
      return {ok: true, json: async () => ({ok: true, protocol_version: 2, runtime_epoch: 'epoch-1', registration_receipt: 'receipt-1', last_update_seq: 0})};
    }
    if (body.action === 'claim') {
      claimStartedResolve();
      return new Promise((resolve) => {
        resolveClaim = () => resolve({ok: true, json: async () => ({ok: true, protocol_version: 2, runtime_epoch: 'epoch-1', registration_receipt: 'receipt-1', accepted_update_seq: 1, current_claim_seq: 1, state_revision: 1, playback_generation: 4, playback_state: 'stopped', playback_active: false, foreground_ready: true})});
      });
    }
    return {ok: true, json: async () => ({ok: true, protocol_version: 2})};
  });

  const claiming = presence.maintainViewerForegroundPresence();
  await claimStarted;
  assert.equal(calls.map((call) => call.body.action).join(','), 'register,claim');
  presence.document.visibilityState = 'hidden';
  const releasing = presence.releaseViewerForegroundPresence();
  resolveClaim();
  await claiming;
  await releasing;

  const releaseCall = calls.find((call) => call.body.action === 'release');
  assert.ok(releaseCall);
  assert.equal(releaseCall.body.update_seq, 2);
  assert.equal(releaseCall.body.claim_seq, 1);
  assert.equal(presence.viewerForegroundPresence.ready, false);
});

test('send is blocked until the V2 foreground claim is ready', async () => {
  let calls = 0;
  const send = loadSendHarness(async () => {
    calls += 1;
    return {ok: true, status: 200, json: async () => ({})};
  }, false);

  await assert.rejects(send.sendViewerMessage('hello'), /presence is not ready/);
  assert.equal(calls, 0);
});

test('JSON send includes the active claim reference and core-viewer profile', async () => {
  const calls = [];
  const send = loadSendHarness(async (path, init) => {
    calls.push({path, init});
    return {ok: true, status: 200, json: async () => ({})};
  });

  await send.sendViewerMessage('hello');

  assert.equal(calls.length, 1);
  assert.equal(calls[0].path, '/viewer/send');
  assert.equal(calls[0].init.headers['X-RenCrow-Client'], 'RenCrow_CORE');
  assert.equal(calls[0].init.headers['X-RenCrow-Interaction-Profile'], 'core-viewer');
  assert.deepEqual(JSON.parse(calls[0].init.body).presence, {
    protocol_version: 2,
    runtime_epoch: 'epoch-1',
    registration_receipt: 'receipt-1',
    claim_seq: 7,
  });
});

test('multipart send serializes the active claim reference and core-viewer profile', async () => {
  const calls = [];
  const send = loadSendHarness(async (path, init) => {
    calls.push({path, init});
    return {ok: true, status: 200, json: async () => ({})};
  });

  await send.sendViewerMessage('hello', [{name: 'notes.txt'}]);

  assert.equal(calls.length, 1);
  assert.equal(calls[0].path, '/viewer/send');
  assert.equal(calls[0].init.headers['X-RenCrow-Interaction-Profile'], 'core-viewer');
  const fields = new Map(send.forms[0].entries);
  assert.equal(fields.get('viewer_client_id'), 'viewer-tab-test');
  assert.deepEqual(JSON.parse(fields.get('presence')), {
    protocol_version: 2,
    runtime_epoch: 'epoch-1',
    registration_receipt: 'receipt-1',
    claim_seq: 7,
  });
});

test('IdleChat display events must match the current control epoch and generation', () => {
  const idle = loadIdleFenceHarness();
  const event = {type: 'idlechat.message', runtime_epoch: 'epoch-1', playback_generation: 4};

  assert.equal(idle.applyIdleChatControlSnapshot({runtime_epoch: 'epoch-1', state_revision: 2, playback_generation: 4, playback_active: true}), true);
  assert.equal(idle.isCurrentIdleChatPlaybackEvent(event), true);
  assert.equal(idle.isCurrentIdleChatPlaybackEvent({...event, playback_generation: 3}), false);
  assert.equal(idle.isCurrentIdleChatPlaybackEvent({...event, runtime_epoch: 'old-epoch'}), false);
  assert.equal(idle.applyIdleChatControlSnapshot({runtime_epoch: 'old-epoch', state_revision: 99, playback_generation: 99, playback_active: true}), false);

  assert.equal(idle.applyIdleChatControlSnapshot({runtime_epoch: 'epoch-1', state_revision: 3, playback_generation: 5, playback_active: false}), true);
  assert.equal(idle.isCurrentIdleChatPlaybackEvent(event), false);
  assert.equal(idle.isCurrentIdleChatPlaybackEvent({...event, playback_generation: 5}), false);
  assert.deepEqual(JSON.parse(JSON.stringify(idle.resetCounts())), {audioResets: 2, liveResets: 2});
});
