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
const state = { idleChat: { selectedMode: 'manual', mode: '', manualMode: false, chatActive: false, currentTopic: '', history: [] } };
const idleStartBtn = document.getElementById('idleStart');
const idleModeNormalBtn = document.getElementById('idleModeNormal');
const idleModeForecastBtn = document.getElementById('idleModeForecast');
const idleModeStorySimpleBtn = document.getElementById('idleModeStorySimple');
const idleStopBtn = document.getElementById('idleStop');
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
` + idleJs + sourceBetween(js, 'idleStartBtn.addEventListener', 'function stateClass') + `
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
    },
    localStorage: {
      getItem: (key) => localStore.get(key) || null,
      setItem: (key, value) => localStore.set(key, String(value)),
    },
    fetch: async (path, init = {}) => {
      fetchCalls.push({path: String(path), method: init.method || 'GET'});
      return {
        ok: true,
        json: async () => ({ok: true, mode: '', manual_mode: false, chat_active: false, current_topic: ''}),
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

test('idle mode buttons select forecast and simple story start endpoints', async () => {
  const {harness, fetchCalls, localStore} = loadIdleModeHarness();

  harness.setIdleSelectedMode('story');
  assert.equal(harness.state.idleChat.selectedMode, 'manual');
  assert.equal(localStore.get('idlechat.selectedMode'), 'manual');

  harness.idleModeForecastBtn.click();
  assert.equal(harness.state.idleChat.selectedMode, 'forecast');
  assert.equal(localStore.get('idlechat.selectedMode'), 'forecast');
  assert.equal(harness.idleModeForecastBtn.classList.contains('is-selected'), true);
  await harness.idleStartBtn.click();
  await tick();
  await tick();
  assert.deepEqual(fetchCalls.filter((c) => c.method === 'POST').at(-1), {path: '/viewer/idlechat/forecast', method: 'POST'});

  harness.idleModeStorySimpleBtn.click();
  assert.equal(harness.state.idleChat.selectedMode, 'story-simple');
  assert.equal(localStore.get('idlechat.selectedMode'), 'story-simple');
  assert.equal(harness.idleModeStorySimpleBtn.classList.contains('is-selected'), true);
  await harness.idleStartBtn.click();
  await tick();
  await tick();
  assert.deepEqual(fetchCalls.filter((c) => c.method === 'POST').at(-1), {path: '/viewer/idlechat/story-simple', method: 'POST'});
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
