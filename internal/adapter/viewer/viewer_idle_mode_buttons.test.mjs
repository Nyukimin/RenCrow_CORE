import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

class FakeClassList {
  constructor() {
    this.values = new Set();
  }
  add(...names) {
    names.forEach((name) => this.values.add(name));
  }
  remove(...names) {
    names.forEach((name) => this.values.delete(name));
  }
  contains(name) {
    return this.values.has(name);
  }
  toggle(name, force) {
    const enabled = force === undefined ? !this.values.has(name) : Boolean(force);
    if (enabled) this.values.add(name);
    else this.values.delete(name);
    return enabled;
  }
}

class FakeElement {
  constructor(id = '') {
    this.id = id;
    this.children = [];
    this.classList = new FakeClassList();
    this.listeners = {};
    this.style = {};
    this.dataset = {};
    this.attributes = {};
    this.disabled = false;
    this.textContent = '';
    this.className = '';
    this.innerHTML = '';
    this.tabIndex = 0;
  }
  addEventListener(type, fn) {
    this.listeners[type] = fn;
  }
  setAttribute(name, value) {
    this.attributes[name] = String(value);
  }
  appendChild(child) {
    this.children.push(child);
    return child;
  }
  click() {
    if (this.disabled) return undefined;
    if (this.listeners.click) return this.listeners.click({preventDefault() {}});
    return undefined;
  }
  querySelectorAll() {
    return [];
  }
}

function tick() {
  return new Promise((resolve) => setImmediate(resolve));
}

function sourceBetween(html, startNeedle, endNeedle) {
  const start = html.indexOf(startNeedle);
  const end = html.indexOf(endNeedle, start);
  assert.ok(start >= 0, `start not found: ${startNeedle}`);
  assert.ok(end > start, `end not found: ${endNeedle}`);
  return html.slice(start, end);
}

function loadIdleModeHarness() {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const idleJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/idlechat.js', 'utf8');
  const source = `
const state = { idleChat: { selectedMode: 'manual', mode: '', manualMode: false, chatActive: false, currentTopic: '', history: [], selectedTopicPlaybackID: 'word:item-1', topicStockPlayback: {can_next: true, can_previous: false} } };
const idleStartBtn = document.getElementById('idleStart');
const idleModeNormalBtn = document.getElementById('idleModeNormal');
const idleModeForecastBtn = document.getElementById('idleModeForecast');
const idleModeStorySimpleBtn = document.getElementById('idleModeStorySimple');
const idleStopBtn = document.getElementById('idleStop');
const idlePlaybackPlayBtn = document.getElementById('idlePlaybackPlay');
const idlePlaybackNextBtn = document.getElementById('idlePlaybackNext');
const idlePlaybackPreviousBtn = document.getElementById('idlePlaybackPrevious');
const idleStateEl = document.getElementById('idleState');
const chat = document.getElementById('chat');
const idleLiveLog = document.getElementById('idleLiveLog');
const idleLiveTab = document.getElementById('idleSubtabLive');
const idleSummaryTab = document.getElementById('idleSubtabSummary');
const idleHistoryTab = document.getElementById('idleSubtabHistory');
idleLiveTab.dataset.idleView = 'live';
idleSummaryTab.dataset.idleView = 'summary';
idleHistoryTab.dataset.idleView = 'history';
const idleViewLive = document.getElementById('idleViewLive');
const idleViewSummary = document.getElementById('idleViewSummary');
const idleViewHistory = document.getElementById('idleViewHistory');
const idleSubtabs = [idleLiveTab, idleSummaryTab, idleHistoryTab];
const idleSubviews = [idleViewLive, idleViewSummary, idleViewHistory];
` + idleJs + sourceBetween(js, "if (idlePlaybackPlayBtn) idlePlaybackPlayBtn.addEventListener", 'function stateClass') + `
globalThis.__idleHarness = {
  state,
  setIdleSelectedMode,
  setIdleSelectedView,
  renderIdleChat,
  idleStartBtn,
  idleModeNormalBtn,
  idleModeForecastBtn,
  idleModeStorySimpleBtn,
  idleStopBtn,
  idlePlaybackPlayBtn,
  idlePlaybackNextBtn,
  idlePlaybackPreviousBtn,
  idleLiveTab,
  idleSummaryTab,
  idleHistoryTab,
  idleViewLive,
  idleViewSummary,
  idleViewHistory,
};
`;

  const elements = new Map();
  const localStore = new Map();
  const fetchCalls = [];
  const context = {
    document: {
      getElementById(id) {
        if (!elements.has(id)) elements.set(id, new FakeElement(id));
        return elements.get(id);
      },
      createElement: () => new FakeElement(),
	  querySelector: () => null,
    },
    localStorage: {
      getItem: (key) => localStore.get(key) || null,
      setItem: (key, value) => localStore.set(key, String(value)),
    },
    fetch: async (path, init = {}) => {
      fetchCalls.push({path: String(path), method: init.method || 'GET', body: init.body || ''});
      return {
        ok: true,
        json: async () => ({ok: true, mode: '', manual_mode: false, chat_active: false, current_topic: '', topic_stock_playback: {current: {id: 'word:item-1'}, can_next: true, can_previous: false}}),
		text: async () => '',
      };
    },
    console: {error() {}},
    renderIdleChat() {},
    setBadge() {},
    stripIdleTopicCategory: (s) => String(s || ''),
    esc: (s) => String(s || ''),
    short: (s, n) => {
      const value = String(s || '');
      return value.length > n ? value.slice(0, n) + '...' : value;
    },
    fdt: (s) => String(s || ''),
    fmt: (s) => String(s || ''),
    copyTextPayload() {},
    showToast() {},
  };
  vm.createContext(context);
  vm.runInContext(source, context);
  context.__idleHarness.elements = elements;
  return {harness: context.__idleHarness, fetchCalls, localStore};
}

test('idle playback buttons post play, next, and previous actions', async () => {
  const {harness, fetchCalls} = loadIdleModeHarness();

  await harness.idlePlaybackPlayBtn.click();
  await tick();
  await tick();
  let call = fetchCalls.filter((c) => c.method === 'POST').at(-1);
  assert.equal(call.path, '/viewer/idlechat/playback');
  assert.deepEqual(JSON.parse(call.body), {action: 'play', item_id: 'word:item-1'});

  harness.idlePlaybackNextBtn.disabled = false;
  await harness.idlePlaybackNextBtn.click();
  await tick();
  await tick();
  call = fetchCalls.filter((c) => c.method === 'POST').at(-1);
  assert.deepEqual(JSON.parse(call.body), {action: 'next'});

  harness.idlePlaybackPreviousBtn.disabled = false;
  await harness.idlePlaybackPreviousBtn.click();
  await tick();
  await tick();
  call = fetchCalls.filter((c) => c.method === 'POST').at(-1);
  assert.deepEqual(JSON.parse(call.body), {action: 'previous'});
});

test('idle bottom controls contain exactly the requested three playback buttons', () => {
  const html = fs.readFileSync('internal/adapter/viewer/viewer.html', 'utf8');
  const controls = sourceBetween(html, '<div class="idle-tools">', '<span id="idleState"');
  assert.equal((controls.match(/<button/g) || []).length, 3);
  assert.match(controls, />前の話題<\/button>/);
  assert.match(controls, />再生<\/button>/);
  assert.match(controls, />スキップ（次の話題）<\/button>/);
});

test('idle chat history renders full topic without ellipsis truncation', () => {
  const {harness} = loadIdleModeHarness();
  const longTopic = '今日のお題（external）: ' + '長いお題です。'.repeat(20);
  harness.state.idleChat.history = [{
    title: 'title',
    topic: longTopic,
    turns: 1,
    loop_restarted: false,
    started_at: '',
    ended_at: '',
    summary: '',
    transcript: [],
  }];

  harness.renderIdleChat();

  const row = harness.elements.get('idlechatBody').children[0];
  assert.ok(row.innerHTML.includes('長いお題です。'.repeat(20)));
  assert.equal(row.innerHTML.includes('...'), false);
});

test('idle subview buttons switch summary review into a distinct view', () => {
  const {harness, localStore} = loadIdleModeHarness();

  harness.setIdleSelectedView('summary');

  assert.equal(harness.state.idleChat.selectedView, 'summary');
  assert.equal(localStore.get('idlechat.selectedView'), 'summary');
  assert.equal(harness.idleSummaryTab.classList.contains('active'), true);
  assert.equal(harness.idleLiveTab.classList.contains('active'), false);
  assert.equal(harness.idleViewSummary.classList.contains('active'), true);
  assert.equal(harness.idleViewLive.classList.contains('active'), false);

  harness.idleHistoryTab.click();
  assert.equal(harness.state.idleChat.selectedView, 'history');
  assert.equal(harness.idleViewHistory.classList.contains('active'), true);
  assert.equal(harness.idleViewSummary.classList.contains('active'), false);
});
