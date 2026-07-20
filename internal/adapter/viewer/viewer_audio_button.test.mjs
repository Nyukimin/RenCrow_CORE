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
    this.dataset = {};
    this.style = {};
    this.attributes = {};
    this.listeners = {};
    this.textContent = '';
    this.innerHTML = '';
    this.title = '';
  }
  set className(value) {
    this._className = String(value || '');
    this.classList = new FakeClassList();
    this._className.split(/\s+/).filter(Boolean).forEach((name) => this.classList.add(name));
  }
  get className() {
    return this._className || '';
  }
  appendChild(child) {
    this.children.push(child);
    return child;
  }
  setAttribute(name, value) {
    this.attributes[name] = String(value);
  }
  getAttribute(name) {
    return this.attributes[name] || '';
  }
  addEventListener(type, fn) {
    this.listeners[type] = fn;
  }
  querySelector(selector) {
    if (selector === '.mc') {
      if (!this._mc) {
        this._mc = new FakeElement('mc');
        const match = String(this.innerHTML || '').match(/<div class="mc">([\s\S]*?)<\/div>/);
        if (match) this._mc.textContent = match[1];
      }
      return this._mc;
    }
    return null;
  }
  click() {
    if (this.listeners.click) return this.listeners.click({preventDefault() {}});
  }
  remove() {}
  removeAttribute(name) {
    delete this.attributes[name];
  }
  scrollIntoView() {}
}

class FakeAudio {
  constructor() {
    FakeAudio.instances.push(this);
    this.listeners = {};
    this.dataset = {};
    this.attributes = {};
    this.style = {};
    this.readyState = 4;
    this.currentTime = 0;
    this.muted = false;
    this.preload = '';
    this.src = '';
    this.paused = true;
    this.playsInline = false;
  }
  addEventListener(type, fn) {
    this.listeners[type] = fn;
  }
  setAttribute(name, value) {
    this.attributes[name] = String(value);
  }
  getAttribute(name) {
    return this.attributes[name] || '';
  }
  play() {
    const outcome = FakeAudio.playOutcomes.shift();
    if (outcome instanceof Error) return Promise.reject(outcome);
    this.paused = false;
    return Promise.resolve();
  }
  pause() {
    this.paused = true;
  }
  load() {}
  removeAttribute(name) {
    if (name === 'src') this.src = '';
  }
}
FakeAudio.playOutcomes = [];
FakeAudio.instances = [];

function loadAudioHarness(options = {}) {
  FakeAudio.playOutcomes = [...(options.playOutcomes || [])];
  FakeAudio.instances = [];
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const timelineJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/timeline.js', 'utf8');
  const idleJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/idlechat.js', 'utf8');
  const start = js.indexOf('const ttsPlayback = {');
  const end = js.indexOf('let sending = false;');
  assert.ok(start > 0, 'ttsPlayback block not found');
  assert.ok(end > start, 'audio handler block end not found');
  const audioBlock = js.slice(start, end);
  const labDateTimeInitStart = audioBlock.indexOf('refreshLabDateTimePanel();');
  const audioButtonBindingStart = audioBlock.indexOf('bindTTSAudioButton(audioBtn);');
  assert.ok(labDateTimeInitStart >= 0, 'lab date time init not found');
  assert.ok(audioButtonBindingStart > labDateTimeInitStart, 'audio button binding not found');
  const audioHarnessSource = audioBlock.slice(0, labDateTimeInitStart) + audioBlock.slice(audioButtonBindingStart);
  const source = timelineJs + '\n' + idleJs + '\n' + audioHarnessSource + `
	globalThis.__viewerAudioHarness = {
	  state,
	  ttsPlayback,
	  viewerControl,
  updateAudioButton,
  enqueueTTSAudio,
  toggleTTSAudio,
  setCentralTTSSpeechText,
  addIdleMsgToTimeline,
  addIdleSummaryToTimeline,
  hydrateIdleLiveTranscript,
  idleLiveRenderTarget,
  clearIdleLivePendingForAudioOwnerTransfer,
  chatAudioSync,
  resolveTTSPlaybackURL,
  handleViewerActiveControlEvent,
  isIdleChatActiveForTTS,
  idleLiveRenderedLog,
	};
`;

  const elements = new Map();
  const body = new FakeElement('body');
  const main = new FakeElement('main');
  const document = {
    body,
    createElement: (tag) => new FakeElement(tag),
    addEventListener: () => {},
    getElementById: (id) => {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    querySelector: (selector) => selector === 'main' ? main : new FakeElement(selector),
    querySelectorAll: () => [],
  };
  body.className = options.liveMode ? 'live-mode' : '';
  const timers = [];
  const localStore = new Map();
  const context = {
    document,
    console: {error() {}, warn() {}},
    window: {
      addEventListener() {},
      location: {protocol: options.protocol || 'http:', href: (options.protocol || 'http:') + '//viewer.local/viewer', search: options.search || ''},
      history: null,
      innerWidth: options.mobile ? 390 : 1280,
      matchMedia: options.mobile
        ? () => ({matches: true, addEventListener() {}, removeEventListener() {}})
        : () => ({matches: false, addEventListener() {}, removeEventListener() {}}),
    },
    navigator: {maxTouchPoints: options.mobile ? 1 : 0},
    fetch: options.fetch,
    localStorage: {
      getItem: (key) => localStore.get(key) || null,
      setItem: (key, value) => localStore.set(key, String(value)),
      removeItem: (key) => localStore.delete(key),
    },
    HTMLMediaElement: {HAVE_CURRENT_DATA: 2},
    URL,
    Audio: FakeAudio,
    state: {
      idleChat: {
        chatActive: true,
        interrupted: false,
      },
      logs: [],
      agents: {},
      openTasks: {},
	  debug: {
		latencyMetrics: [],
		latencyLatest: {},
		latencySeen: {},
	  },
    },
    MAX_TIMELINE_NODES: 400,
    mainEl: document.querySelector('main'),
    chat: document.getElementById('chat'),
    idleLiveLog: document.getElementById('idleLiveLog'),
    ctr: document.getElementById('ctr'),
    cnt: document.getElementById('cnt'),
    latestBtn: document.getElementById('latestBtn'),
    toastEl: document.getElementById('toast'),
    thinkingBubbles: {},
    setTimeout: (fn, _ms) => {
      fn.ms = _ms;
      timers.push(fn);
      return timers.length;
    },
    setInterval: () => 0,
    clearTimeout: () => {},
    clearLipSyncSpeaking() {},
    setLipSyncSpeaking() {},
    scrollToBottom() {},
    refreshMemorySnapshot() {},
    refreshMemoryEvents() {},
    refreshDomainGraphAssertions() {},
    saveSourceRegistryEntry() {},
    exportSourceRegistryYAML() {},
    importSourceRegistryYAML() {},
    refreshSourceRegistryStaging() {},
    refreshNewsPack() {},
    renderRoleSelector() {},
    renderSystem() {},
	formatLabDateTime: () => '2026-07-20 12:00:00',
    ftime: () => '12:00:00',
    stripIdleTopicCategory: (s) => String(s || '').replace(/^今日のお題(?:（[^）]+）)*[:：]\s*/, '今日のお題：').trim(),
    normalizeViewerDisplayText: (s) => String(s || '').replace(/^今日のお題(?:（[^）]+）)*[:：]\s*/, '今日のお題：').trim(),
    fmt: (s) => String(s || ''),
    ag: (id) => ({c: id === 'shiro' ? '#22d3ee' : '#f472b6', l: id || 'mio', e: ''}),
  };
  vm.createContext(context);
  vm.runInContext(source, context);
	context.__viewerAudioHarness.ttsPlayback.audioEnabled = options.audioEnabled !== false;
	context.__viewerAudioHarness.updateAudioButton();
  return {harness: context.__viewerAudioHarness, elements, timers};
}

function liveTimestamp(offsetMs = 0) {
  return new Date(Date.now() + offsetMs).toISOString();
}

test('idlechat live render target is central chat only in live mode', () => {
  const normal = loadAudioHarness();
  assert.equal(normal.harness.idleLiveRenderTarget(), normal.elements.get('idleLiveLog'));

  const live = loadAudioHarness({liveMode: true});
  assert.equal(live.harness.idleLiveRenderTarget(), live.elements.get('chat'));
  assert.notEqual(live.harness.idleLiveRenderTarget(), live.elements.get('idleLiveLog'));
});

test('tts playback url uses same-origin proxy for absolute audio urls', () => {
  const {harness} = loadAudioHarness();

  const got = harness.resolveTTSPlaybackURL('http://192.168.1.207:7870/audio/sample.wav', '');

  assert.equal(got, '/viewer/tts/audio?url=http%3A%2F%2F192.168.1.207%3A7870%2Faudio%2Fsample.wav');
});

test('speaker button can turn ready audio off without stopping central chat fallback', async () => {
  const {harness, elements, timers} = loadAudioHarness();
  const audioBtn = elements.get('audioBtn');

  harness.ttsPlayback.unlocked = true;
  harness.ttsPlayback.blocked = false;
  harness.updateAudioButton();
  assert.equal(audioBtn.getAttribute('aria-label'), '音声は有効です');

  await audioBtn.click();

  assert.equal(harness.ttsPlayback.audioEnabled, false);
  assert.equal(harness.ttsPlayback.playing, false);
  assert.equal(audioBtn.getAttribute('aria-label'), '音声はOFFです。タップしてON');
  assert.equal(audioBtn.textContent, '🔇');

  harness.enqueueTTSAudio('/audio/a.wav', 'mio', 'session-1', 'default', 0, 'speech', '中央表示です。', '', 'u1');
  assert.equal(harness.ttsPlayback.queue.length, 0);
  assert.equal(harness.ttsPlayback.fallbackActive, true);
  assert.equal(elements.get('chat').children.at(-1)._mc.textContent, '中央表示です。');

  timers.shift()();
  assert.equal(harness.ttsPlayback.fallbackActive, false);
});

test('mobile speaker tap can turn ready audio off', async () => {
  const {harness, elements} = loadAudioHarness({mobile: true});
  const audioBtn = elements.get('audioBtn');

  harness.ttsPlayback.unlocked = true;
  harness.ttsPlayback.blocked = false;
  harness.updateAudioButton();
  assert.equal(audioBtn.getAttribute('aria-label'), '音声は有効です');

  await audioBtn.click();

  assert.equal(harness.ttsPlayback.audioEnabled, false);
  assert.equal(harness.ttsPlayback.unlocked, false);
  assert.equal(audioBtn.getAttribute('aria-label'), '音声はOFFです。タップしてON');
  assert.equal(audioBtn.textContent, '🔇');
});

test('mobile speaker tap replays queued tts after autoplay block instead of losing it to fallback', async () => {
  const err = new Error('play() failed because the user did not interact with the document first');
  err.name = 'NotAllowedError';
  const {harness, elements} = loadAudioHarness({mobile: true, playOutcomes: [err]});

  harness.enqueueTTSAudio('/audio/a.wav', 'mio', 'session-mobile', 'default', 0, 'speech', '音声で聞きたい本文です。', '', 'mobile-u1');
  await Promise.resolve();
  await Promise.resolve();

  assert.equal(harness.ttsPlayback.blocked, true);
  assert.equal(harness.ttsPlayback.queue.length, 1);
  assert.equal(elements.get('chat').children.at(-1)._mc.textContent, '音声で聞きたい本文です。');

  await harness.toggleTTSAudio();
  await Promise.resolve();
  await Promise.resolve();

  assert.equal(harness.ttsPlayback.blocked, false);
  assert.equal(harness.ttsPlayback.unlocked, true);
  assert.equal(harness.ttsPlayback.queue.length, 0);
  assert.equal(harness.ttsPlayback.playing, true);
});

test('autoplay blocked idlechat audio sends failed playback ack without dropping retry queue', async () => {
  const err = new Error('play() failed because the user did not interact with the document first');
  err.name = 'NotAllowedError';
  const fetchCalls = [];
  const {harness, elements, timers} = loadAudioHarness({
    mobile: true,
    playOutcomes: [err],
    fetch: (url, init) => {
      fetchCalls.push({url, init});
      return Promise.resolve({ok: true, json: () => Promise.resolve({})});
    },
  });

  harness.enqueueTTSAudio('/audio/a.wav', 'mio', 'idle-autoplay', 'default', 0, 'speech', '音声ブロック時の本文です。', 'idle-autoplay:0000', 'idle-autoplay:utt:0000');
  harness.chatAudioSync.markSessionCompleted('idle-autoplay', 'idle-autoplay:0000');
  await Promise.resolve();
  await Promise.resolve();

  assert.equal(harness.ttsPlayback.blocked, true);
  assert.equal(harness.ttsPlayback.queue.length, 1);
  assert.ok(elements.get('idleLiveLog').children.at(-1)._mc.innerHTML.includes('TTS_AUDIO_BLOCKED'));
  assert.equal(elements.get('idleLiveLog').children.at(-1)._mc.innerHTML.includes('音声ブロック時の本文です。'), false);

  timers.shift()();
  await Promise.resolve();

  const ack = fetchCalls.find((call) => call.url === '/viewer/tts/playback-ack');
  assert.ok(ack, 'autoplay block should still ack playback failure');
  const payload = JSON.parse(ack.init.body);
  assert.equal(payload.response_id, 'idle-autoplay:0000');
  assert.equal(payload.status, 'error');
  assert.match(payload.error, /blocked autoplay|did not interact/i);
  assert.equal(harness.ttsPlayback.queue.length, 1);
});

test('idlechat first audio chunk starts before second chunk or session completion', async () => {
  const {harness} = loadAudioHarness();

  harness.enqueueTTSAudio('/audio/first.wav', 'mio', 'idle-fast-start', 'default', 0, 'first speech', '一つ目です。', 'idle-fast-start:0000', 'idle-fast-start:utt:0000');
  await Promise.resolve();

  assert.equal(harness.ttsPlayback.currentChunkIndex, 0);
  assert.equal(harness.ttsPlayback.audio.src, '/audio/first.wav');
  assert.equal(harness.ttsPlayback.playing, true);
  assert.equal(harness.ttsPlayback.queue.length, 0);
});

test('tts queue preloads the next audio chunk without starting it', async () => {
  const {harness} = loadAudioHarness();

  harness.enqueueTTSAudio('/audio/first.wav', 'mio', 'preload-session', 'default', 0, 'first speech', '一つ目です。', '', 'preload-0');
  harness.enqueueTTSAudio('/audio/second.wav', 'mio', 'preload-session', 'default', 1, 'second speech', '二つ目です。', '', 'preload-1');
  await Promise.resolve();

  assert.equal(harness.ttsPlayback.currentChunkIndex, 0);
  assert.equal(harness.ttsPlayback.audio.src, '/audio/first.wav');
  assert.equal(harness.ttsPlayback.queue.length, 1);
  const preloaded = FakeAudio.instances.find((audio) => audio !== harness.ttsPlayback.audio && audio.src === '/audio/second.wav');
  assert.ok(preloaded, 'expected queued audio to be preloaded');
  assert.equal(preloaded.preload, 'auto');
  assert.equal(preloaded.paused, true);
});

test('chat output interrupt stops current audio and drops stale chat chunks', async () => {
  const {harness, elements} = loadAudioHarness();

  harness.enqueueTTSAudio('/audio/first.wav', 'mio', 'chat-interrupt-session', 'default', 0, 'first speech', '一つ目です。', 'chat-interrupt-response', 'chat-interrupt-0');
  harness.enqueueTTSAudio('/audio/second.wav', 'mio', 'chat-interrupt-session', 'default', 1, 'second speech', '二つ目です。', 'chat-interrupt-response', 'chat-interrupt-1');
  await Promise.resolve();
  harness.setCentralTTSSpeechText('mio', '表示中です。', 'display-interrupt-session', 0, 'display-interrupt-0', 'display-interrupt-response');

  assert.equal(harness.ttsPlayback.playing, true);
  assert.equal(harness.ttsPlayback.audio.src, '/audio/first.wav');
  assert.equal(harness.ttsPlayback.queue.length, 1);
  const beforeCount = elements.get('chat').children.length;

  harness.chatAudioSync.resetChat('user_input');

  assert.equal(harness.ttsPlayback.playing, false);
  assert.equal(harness.ttsPlayback.audio.src, '');
  assert.equal(harness.ttsPlayback.audioEnabled, true);
  assert.equal(harness.ttsPlayback.queue.length, 0);

  harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      audio_url: '/audio/stale.wav',
      session_id: 'chat-interrupt-session',
      response_id: 'chat-interrupt-response',
      utterance_id: 'chat-interrupt-2',
      chunk_index: 2,
      character_id: 'mio',
      text: 'stale speech',
      display_text: '古い続きです。',
    }),
  });
  harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      audio_url: '/audio/stale-visible.wav',
      session_id: 'display-interrupt-session',
      response_id: 'display-interrupt-response',
      utterance_id: 'display-interrupt-1',
      chunk_index: 1,
      character_id: 'mio',
      text: 'stale visible speech',
      display_text: '表示中だった古い続きです。',
    }),
  });
  await Promise.resolve();

  assert.equal(harness.ttsPlayback.playing, false);
  assert.equal(harness.ttsPlayback.queue.length, 0);
  assert.equal(harness.ttsPlayback.audio.src, '');
  assert.equal(elements.get('chat').children.length, beforeCount);
});

test('audio error does not start the next tts chunk until fallback delay completes', async () => {
  const {harness, elements, timers} = loadAudioHarness();

  harness.enqueueTTSAudio('/audio/first.wav', 'mio', 'serial-error-session', 'default', 0, 'first speech', '一つ目です。', '', 'serial-error-0');
  harness.enqueueTTSAudio('/audio/second.wav', 'mio', 'serial-error-session', 'default', 1, 'second speech', '二つ目です。', '', 'serial-error-1');
  await Promise.resolve();

  assert.equal(harness.ttsPlayback.currentChunkIndex, 0);
  assert.equal(harness.ttsPlayback.audio.src, '/audio/first.wav');
  assert.equal(harness.ttsPlayback.queue.length, 1);

  harness.ttsPlayback.audio.listeners.error();

  assert.equal(harness.ttsPlayback.audio.src, '/audio/first.wav');
  assert.equal(harness.ttsPlayback.queue.length, 1);
  assert.equal(harness.ttsPlayback.fallbackActive, true);
  assert.equal(elements.get('chat').children.at(-1)._mc.textContent, '一つ目です。');

  timers.shift()();

  assert.equal(harness.ttsPlayback.currentChunkIndex, 1);
  assert.equal(harness.ttsPlayback.audio.src, '/audio/second.wav');
  assert.equal(harness.ttsPlayback.queue.length, 0);
});

test('natural audio end waits for tail gap before starting next tts chunk', async () => {
  const {harness, elements, timers} = loadAudioHarness();

  harness.enqueueTTSAudio('/audio/first.wav', 'mio', 'tail-gap-session', 'default', 0, 'first speech', '一つ目です。', '', 'tail-gap-0');
  harness.enqueueTTSAudio('/audio/second.wav', 'mio', 'tail-gap-session', 'default', 1, 'second speech', '二つ目です。', '', 'tail-gap-1');
  await Promise.resolve();

  assert.equal(harness.ttsPlayback.currentChunkIndex, 0);
  assert.equal(harness.ttsPlayback.audio.src, '/audio/first.wav');
  assert.equal(harness.ttsPlayback.queue.length, 1);
  assert.equal(elements.get('chat').children.at(-1)._mc.textContent, '一つ目です。');

  harness.ttsPlayback.audio.listeners.ended();

  assert.equal(harness.ttsPlayback.playing, false);
  assert.equal(harness.ttsPlayback.tailActive, true);
  assert.equal(harness.ttsPlayback.audio.src, '/audio/first.wav');
  assert.equal(harness.ttsPlayback.queue.length, 1);
  assert.equal(timers.at(-1).ms, 240);
  assert.ok(!String(elements.get('chat').children.at(-1)._mc.textContent || '').includes('二つ目です。'));

  timers.pop()();
  await Promise.resolve();

  assert.equal(harness.ttsPlayback.tailActive, false);
  assert.equal(harness.ttsPlayback.currentChunkIndex, 1);
  assert.equal(harness.ttsPlayback.audio.src, '/audio/second.wav');
  assert.equal(harness.ttsPlayback.queue.length, 0);
  assert.ok(String(elements.get('chat').children.at(-1)._mc.textContent || '').includes('二つ目です。'));
});

test('natural audio end uses a longer tail gap when next tts speaker changes', async () => {
  const {harness, timers} = loadAudioHarness();

  harness.enqueueTTSAudio('/audio/mio.wav', 'mio', 'speaker-gap-session', 'default', 0, 'mio speech', 'みおです。', '', 'speaker-gap-0');
  harness.enqueueTTSAudio('/audio/shiro.wav', 'shiro', 'speaker-gap-session', 'default', 1, 'shiro speech', 'しろです。', '', 'speaker-gap-1');
  await Promise.resolve();

  harness.ttsPlayback.audio.listeners.ended();

  assert.equal(harness.ttsPlayback.tailActive, true);
  assert.equal(harness.ttsPlayback.currentChunkIndex, -1);
  assert.equal(harness.ttsPlayback.queue.length, 1);
  assert.equal(timers.at(-1).ms, 420);
});

test('idlechat message creates a pending bubble without showing full text before tts chunk', () => {
  const {harness, elements} = loadAudioHarness();
  const idleLiveLog = elements.get('idleLiveLog');

  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'mio',
    to: 'shiro',
    content: 'TTSを待たずに表示する発話です。',
    session_id: 'idle-visible-1',
    timestamp: '2026-05-09T00:00:00+09:00',
  });

  assert.equal(idleLiveLog.children.length, 1);
  assert.equal(idleLiveLog.children[0]._mc.textContent, '');
  assert.equal(idleLiveLog.children[0].classList.contains('idle-pending-tts'), true);
  assert.equal(idleLiveLog.children[0].innerHTML.includes('TTSを待たずに表示する発話です。'), false);
});

test('idlechat pending message shows traceable tts error instead of fallback text', () => {
  const {harness, elements, timers} = loadAudioHarness({search: '?idle_raw=1'});
  const idleLiveLog = elements.get('idleLiveLog');

  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'mio',
    to: 'shiro',
    content: '編集後の発話です。',
    raw_content: 'Mio: 編集前の素の応答です。',
    session_id: 'idle-raw-1',
    message_id: 'idle-raw-1:msg:0001',
    turn_index: 1,
    timestamp: '2026-05-09T00:00:00+09:00',
  });

  assert.equal(idleLiveLog.children.length, 1);
  assert.equal(idleLiveLog.children[0]._mc.textContent, '');
  assert.equal(idleLiveLog.children[0]._mc.innerHTML.includes('編集前（テストモード）'), false);
  assert.equal(timers.at(-1).ms, 60000);

  timers.at(-1)();

  assert.ok(idleLiveLog.children[0]._mc.innerHTML.includes('TTS_CHUNK_TIMEOUT'));
  assert.ok(idleLiveLog.children[0]._mc.innerHTML.includes('session_id'));
  assert.ok(idleLiveLog.children[0]._mc.innerHTML.includes('idle-raw-1'));
  assert.ok(idleLiveLog.children[0]._mc.innerHTML.includes('message_id'));
  assert.ok(idleLiveLog.children[0]._mc.innerHTML.includes('idle-raw-1:msg:0001'));
  assert.equal(idleLiveLog.children[0]._mc.innerHTML.includes('編集後の発話です。'), false);
  assert.equal(idleLiveLog.children[0]._mc.innerHTML.includes('編集前（テストモード）'), false);
  assert.equal(idleLiveLog.children[0]._mc.innerHTML.includes('Mio: 編集前の素の応答です。'), false);
  assert.equal(idleLiveLog.children[0].classList.contains('idle-tts-error'), true);
  assert.equal(idleLiveLog.children[0].classList.contains('idle-display-error'), true);
});

test('idlechat audio off renders the message immediately without arming a tts timeout', () => {
  const {harness, elements, timers} = loadAudioHarness();
  const idleLiveLog = elements.get('idleLiveLog');
  harness.ttsPlayback.audioEnabled = false;

  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'mio',
    to: 'shiro',
    content: '音声オフでは待たずにそのまま見せます。',
    session_id: 'idle-audio-off-1',
    message_id: 'idle-audio-off-1:msg:0001',
    turn_index: 1,
    timestamp: '2026-05-09T00:00:00+09:00',
  });

  assert.equal(idleLiveLog.children.length, 1);
  assert.equal(timers.length, 0);
  assert.ok(idleLiveLog.children[0]._mc.textContent.includes('音声オフでは待たずにそのまま見せます。'));
  assert.equal(idleLiveLog.children[0]._mc.textContent.includes('TTS_CHUNK_TIMEOUT'), false);
  assert.equal(idleLiveLog.children[0].classList.contains('idle-pending-tts'), false);
});

test('idlechat tts reveals pending message in sync with display chunks', () => {
  const {harness, elements} = loadAudioHarness();
  const idleLiveLog = elements.get('idleLiveLog');

  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'mio',
    to: 'shiro',
    content: '表示済みの発話をそのまま口パク対象にします。',
    session_id: 'idle-reuse-1',
    message_id: 'idle-reuse-1:msg:0001',
    turn_index: 1,
    timestamp: '2026-05-09T00:00:00+09:00',
  });
  const rendered = idleLiveLog.children[0];

  harness.setCentralTTSSpeechText('mio', '表示済みの発話を、', 'idle-reuse-1', 0, 'idle-reuse-1:msg:0001:utt:0000', 'idle-reuse-1:0000', 'idle-reuse-1:msg:0001', 1);

  assert.equal(idleLiveLog.children.length, 1);
  assert.equal(idleLiveLog.children[0], rendered);
  assert.equal(rendered._mc.textContent, '表示済みの発話を、');
  assert.equal(rendered._mc.textContent.includes('表示済みの発話をそのまま口パク対象にします。'), false);
  assert.equal(rendered.classList.contains('idle-pending-tts'), false);
  assert.ok(rendered.classList.contains('tts-current'));

  harness.setCentralTTSSpeechText('mio', 'チャンク単位で表示します。', 'idle-reuse-1', 1, 'idle-reuse-1:msg:0001:utt:0001', 'idle-reuse-1:0000', 'idle-reuse-1:msg:0001', 1);
  assert.equal(rendered._mc.textContent, '表示済みの発話を、チャンク単位で表示します。');
  assert.equal(rendered._mc.textContent.includes('😊'), false);
});

test('idlechat keeps appending chunks after audio end before the next chunk of the same message', () => {
  const {harness, elements} = loadAudioHarness();
  const idleLiveLog = elements.get('idleLiveLog');

  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'mio',
    to: 'shiro',
    content: '複数chunkでも表示本文はイベント本文のままです。',
    session_id: 'idle-same-message-chunks',
    message_id: 'idle-same-message-chunks:msg:0001',
    turn_index: 1,
    timestamp: '2026-05-09T00:00:00+09:00',
  });
  const rendered = idleLiveLog.children[0];

  harness.setCentralTTSSpeechText('mio', '複数chunkでも表示本文は', 'idle-same-message-chunks', 1, 'idle-same-message-chunks:msg:0001:utt:0000', 'idle-same-message-chunks:0001', 'idle-same-message-chunks:msg:0001', 1);
  harness.setCentralTTSSpeechText('', '');
  harness.setCentralTTSSpeechText('mio', 'イベント本文のままです。', 'idle-same-message-chunks', 2, 'idle-same-message-chunks:msg:0001:utt:0001', 'idle-same-message-chunks:0001', 'idle-same-message-chunks:msg:0001', 1);

  assert.equal(idleLiveLog.children.length, 1);
  assert.equal(idleLiveLog.children[0], rendered);
  assert.equal(rendered._mc.textContent, '複数chunkでも表示本文はイベント本文のままです。');
  assert.equal(rendered._mc.innerHTML.includes('イベント本文のままです。 イベント本文のままです。'), false);
});

test('live mode keeps pending idlechat messages hidden until tts reveals display chunk text', () => {
  const {harness, elements} = loadAudioHarness({liveMode: true});
  const chat = elements.get('chat');

  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'mio',
    to: 'shiro',
    content: 'ライブ画面では空の待機吹き出しを出しません。',
    session_id: 'idle-live-hidden-1',
    message_id: 'idle-live-hidden-1:msg:0001',
    turn_index: 1,
    timestamp: liveTimestamp(1000),
  });

  assert.equal(chat.children.length, 0);

  harness.setCentralTTSSpeechText('mio', '断片です。', 'idle-live-hidden-1', 0, 'idle-live-hidden-1:msg:0001:utt:0000', 'idle-live-hidden-1:0000', 'idle-live-hidden-1:msg:0001', 1);

  assert.equal(chat.children.length, 1);
  assert.equal(chat.children[0]._mc.textContent, '断片です。');
  assert.equal(chat.children[0]._mc.textContent.includes('ライブ画面では空の待機吹き出しを出しません。'), false);
  assert.equal(chat.children[0].classList.contains('idle-pending-tts'), false);
});

test('summary tts does not duplicate an already rendered idlechat summary', () => {
  const {harness, elements} = loadAudioHarness();
  const idleLiveLog = elements.get('idleLiveLog');

  harness.addIdleSummaryToTimeline({
    type: 'idlechat.summary',
    from: 'shiro',
    to: 'user',
    content: '今回のまとめです。\nMioとShiroは表示契約を確認しました。',
    session_id: 'idle-summary-suppress',
    message_id: 'idle-summary-suppress:summary',
    timestamp: '2026-05-09T00:00:00+09:00',
  });
  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'shiro',
    to: 'user',
    content: '今回のまとめです。\nMioとShiroは表示契約を確認しました。',
    session_id: 'idle-summary-suppress',
    message_id: 'idle-summary-suppress:summary:tts',
    turn_index: 99,
    timestamp: '2026-05-09T00:00:01+09:00',
  });

  harness.setCentralTTSSpeechText('shiro', '今回のまとめです。', 'idle-summary-suppress', 0, 'idle-summary-suppress:summary:tts:utt:0000', 'idle-summary-suppress:summary:tts', 'idle-summary-suppress:summary:tts', 99);

  assert.equal(idleLiveLog.children.length, 1);
  assert.equal(idleLiveLog.children[0].classList.contains('idle-kind-summary'), true);
  assert.ok(idleLiveLog.children[0]._mc.textContent.includes('今回のまとめです。'));
  assert.ok(harness.idleLiveRenderedLog.some((item) =>
    item.kind === 'speech_tts_suppressed' &&
    item.message_id === 'idle-summary-suppress:summary:tts'
  ));
});

test('live mode does not consume idlechat pending message without stable tts identity', () => {
  const {harness, elements} = loadAudioHarness({liveMode: true});
  const chat = elements.get('chat');

  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'mio',
    to: 'shiro',
    content: 'IDがある待機発話です。',
    session_id: 'idle-live-strict-id',
    message_id: 'idle-live-strict-id:msg:0001',
    turn_index: 1,
    timestamp: liveTimestamp(1000),
  });

  harness.setCentralTTSSpeechText('mio', 'IDなしTTS断片です。', 'idle-live-strict-id', 0, 'idle-live-strict-id:utt:orphan', 'idle-live-strict-id:0000');

  assert.equal(chat.children.length, 1);
  assert.ok(chat.children[0]._mc.innerHTML.includes('TTS_IDENTITY_MISSING'));
  assert.equal(chat.children[0]._mc.innerHTML.includes('IDなしTTS断片です。'), false);
  assert.equal(chat.children[0].dataset.messageId || '', '');
  assert.ok(harness.idleLiveRenderedLog.some((item) => item.kind === 'tts_identity_error' && item.content.includes('TTS_IDENTITY_MISSING')));
});

test('idlechat audio playback reveals the message matching the tts message id', async () => {
  const {harness, elements} = loadAudioHarness({liveMode: true});
  const chat = elements.get('chat');

  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'mio',
    to: 'shiro',
    content: '1番目の発言全文です。',
    session_id: 'idle-audio-match',
    message_id: 'idle-audio-match:msg:0001',
    turn_index: 1,
    timestamp: liveTimestamp(1000),
  });
  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'mio',
    to: 'shiro',
    content: '2番目の発言全文です。',
    session_id: 'idle-audio-match',
    message_id: 'idle-audio-match:msg:0002',
    turn_index: 2,
    timestamp: liveTimestamp(2000),
  });

  harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      session_id: 'idle-audio-match',
      response_id: 'idle-audio-match:0002',
      message_id: 'idle-audio-match:msg:0002',
      turn_index: 2,
      utterance_id: 'idle-audio-match:msg:0002:utt:0000',
      chunk_index: 0,
      character_id: 'mio',
      text: '2番目のチャンクです。',
      display_text: '2番目のチャンクです。',
      audio_url: '/audio/idle-audio-match-2.wav',
    }),
  });
  harness.chatAudioSync.markSessionCompleted('idle-audio-match', 'idle-audio-match:0002');
  await Promise.resolve();

  assert.equal(chat.children.length, 1);
  assert.equal(chat.children[0].dataset.messageId, 'idle-audio-match:msg:0002');
  assert.equal(chat.children[0]._mc.textContent, '2番目のチャンクです。');
});

test('idlechat audio end clears the active tts marker from the spoken message', async () => {
  const {harness, elements} = loadAudioHarness({liveMode: true});
  const chat = elements.get('chat');

  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'shiro',
    to: 'mio',
    content: '音声終了後に再生中表示を残しません。',
    session_id: 'idle-clear-current',
    message_id: 'idle-clear-current:msg:0001',
    turn_index: 1,
    timestamp: liveTimestamp(1000),
  });
  harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      session_id: 'idle-clear-current',
      response_id: 'idle-clear-current:0001',
      message_id: 'idle-clear-current:msg:0001',
      turn_index: 1,
      utterance_id: 'idle-clear-current:msg:0001:utt:0000',
      chunk_index: 0,
      character_id: 'shiro',
      text: '再生中の断片です。',
      display_text: '再生中の断片です。',
      audio_url: '/audio/idle-clear-current.wav',
    }),
  });
  harness.chatAudioSync.markSessionCompleted('idle-clear-current', 'idle-clear-current:0001');
  await Promise.resolve();

  assert.equal(chat.children.length, 1);
  assert.equal(chat.children[0].classList.contains('tts-current'), true);

  harness.ttsPlayback.audio.listeners.ended();

  assert.equal(chat.children[0].classList.contains('tts-current'), false);
  assert.equal(chat.children[0]._mc.textContent, '再生中の断片です。');
});

test('live mode does not render idlechat topic tts in the central chat window', () => {
  const {harness, elements} = loadAudioHarness({liveMode: true});
  const chat = elements.get('chat');

  harness.setCentralTTSSpeechText('user', '今日のお題：中央に出さない話題です。', 'idle-live-topic-1', 0, 'topic-0', 'idle-live-topic-1:topic');

  assert.equal(chat.children.length, 0);
});

test('live mode does not render active transcript without tts chunks', () => {
  const {harness, elements} = loadAudioHarness({liveMode: true});
  const chat = elements.get('chat');

  harness.hydrateIdleLiveTranscript('idle-current', [
    {type: 'idlechat.message', from: 'mio', to: 'shiro', content: 'Mioが先に話します。', session_id: 'idle-current', message_id: 'idle-current:msg:0001', turn_index: 1, timestamp: liveTimestamp()},
    {type: 'idlechat.message', from: 'shiro', to: 'mio', content: 'Shiroが次に返します。', session_id: 'idle-current', message_id: 'idle-current:msg:0002', turn_index: 2, timestamp: liveTimestamp(1000)},
  ]);

  assert.equal(chat.children.length, 0);

  harness.setCentralTTSSpeechText('mio', 'MioのTTS断片です。', 'idle-current', 0, 'idle-current:msg:0001:utt:0000', 'idle-current:0001', 'idle-current:msg:0001', 1);

  assert.equal(chat.children.length, 1);
  assert.equal(chat.children[0]._mc.textContent, 'MioのTTS断片です。');
});

test('message id match does not authorize tts chunk text rendering without a display event', () => {
  const {harness, elements} = loadAudioHarness({liveMode: true});
  const chat = elements.get('chat');

  harness.setCentralTTSSpeechText(
    'mio',
    'TTSだけから本文を作ってはいけません。',
    'idle-no-display-source',
    0,
    'idle-no-display-source:msg:0001:utt:0000',
    'idle-no-display-source:0001',
    'idle-no-display-source:msg:0001',
    1
  );

  assert.equal(chat.children.length, 1);
  assert.ok(chat.children[0]._mc.innerHTML.includes('TTS_DISPLAY_SOURCE_MISSING'));
  assert.equal(chat.children[0]._mc.innerHTML.includes('TTSだけから本文を作ってはいけません。'), false);
  assert.ok(harness.idleLiveRenderedLog.some((item) =>
    item.kind === 'tts_playback_error' &&
    item.message_id === 'idle-no-display-source:msg:0001' &&
    item.content.includes('TTS_DISPLAY_SOURCE_MISSING')
  ));
});

test('live mode sorts idlechat messages by turn index even when events arrive out of order', () => {
  const {harness, elements} = loadAudioHarness({liveMode: true});
  const chat = elements.get('chat');

  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'shiro',
    to: 'mio',
    content: '2番目のShiroです。',
    session_id: 'idle-order-id',
    message_id: 'idle-order-id:msg:0002',
    turn_index: 2,
    timestamp: liveTimestamp(2000),
  });
  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'mio',
    to: 'shiro',
    content: '1番目のMioです。',
    session_id: 'idle-order-id',
    message_id: 'idle-order-id:msg:0001',
    turn_index: 1,
    timestamp: liveTimestamp(1000),
  });

  assert.equal(chat.children.length, 0);
  harness.setCentralTTSSpeechText('shiro', '断片2', 'idle-order-id', 0, 'idle-order-id:msg:0002:utt:0000', 'idle-order-id:0002', 'idle-order-id:msg:0002', 2);
  harness.setCentralTTSSpeechText('mio', '断片1', 'idle-order-id', 0, 'idle-order-id:msg:0001:utt:0000', 'idle-order-id:0001', 'idle-order-id:msg:0001', 1);

  assert.equal(chat.children.length, 2);
  assert.equal(chat.children[0]._mc.textContent, '断片1');
  assert.equal(chat.children[1]._mc.textContent, '断片2');
});

test('live mode deduplicates hydrate, idlechat event, and synced tts for the same message id', () => {
  const {harness, elements} = loadAudioHarness({liveMode: true});
  const chat = elements.get('chat');

  harness.hydrateIdleLiveTranscript('idle-dedupe', [
    {
      type: 'idlechat.message',
      from: 'mio',
      to: 'shiro',
      content: '一度だけ表示される発言です。',
      session_id: 'idle-dedupe',
      message_id: 'idle-dedupe:msg:0001',
      turn_index: 1,
      timestamp: liveTimestamp(1000),
    },
  ]);
  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'mio',
    to: 'shiro',
    content: '一度だけ表示される発言です。',
    session_id: 'idle-dedupe',
    message_id: 'idle-dedupe:msg:0001',
    turn_index: 1,
    timestamp: liveTimestamp(1000),
  });
  harness.setCentralTTSSpeechText('mio', 'TTS断片です。', 'idle-dedupe', 0, 'idle-dedupe:msg:0001:utt:0000', 'idle-dedupe:0001', 'idle-dedupe:msg:0001', 1);

  assert.equal(chat.children.length, 1);
  assert.equal(chat.children[0]._mc.textContent, 'TTS断片です。');
});

test('live mode pending tts timeout renders traceable error instead of full response fallback', () => {
  const {harness, elements, timers} = loadAudioHarness({liveMode: true});
  const chat = elements.get('chat');

  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'mio',
    to: 'shiro',
    content: 'fallbackで丸ごと再表示してはいけない本文です。',
    session_id: 'idle-live-no-fallback',
    message_id: 'idle-live-no-fallback:msg:0001',
    turn_index: 1,
    timestamp: liveTimestamp(1000),
  });

  assert.equal(chat.children.length, 0);
  assert.equal(timers.at(-1).ms, 60000);

  timers.at(-1)();

  assert.equal(chat.children.length, 1);
  assert.ok(chat.children[0]._mc.innerHTML.includes('TTS_CHUNK_TIMEOUT'));
  assert.ok(chat.children[0]._mc.innerHTML.includes('idle-live-no-fallback:msg:0001'));
  assert.equal(chat.children[0]._mc.innerHTML.includes('fallbackで丸ごと再表示してはいけない本文です。'), false);
  assert.equal(harness.idleLiveRenderedLog.some((item) => item.kind === 'speech_fallback' && item.message_id === 'idle-live-no-fallback:msg:0001'), false);
  assert.ok(harness.idleLiveRenderedLog.some((item) => item.kind === 'speech_tts_error' && item.message_id === 'idle-live-no-fallback:msg:0001' && item.content.includes('TTS_CHUNK_TIMEOUT')));
});

test('live mode non-active audio viewer does not arm idlechat pending timeout', () => {
  const {harness, elements, timers} = loadAudioHarness({liveMode: true});
  const chat = elements.get('chat');
  harness.viewerControl.activeAudioViewerId = 'viewer-tab-other';

  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'mio',
    to: 'shiro',
    content: '非active viewerではpendingを作りません。',
    session_id: 'idle-non-active-owner',
    message_id: 'idle-non-active-owner:msg:0001',
    turn_index: 1,
    timestamp: liveTimestamp(1000),
  });

  assert.equal(chat.children.length, 0);
  assert.equal(timers.length, 0);
  assert.ok(harness.idleLiveRenderedLog.some((item) =>
    item.kind === 'pending_skipped' &&
    item.message_id === 'idle-non-active-owner:msg:0001' &&
    item.content.includes('NON_ACTIVE_AUDIO_VIEWER_PENDING_SKIPPED')
  ));
});

test('live mode owner transfer clears old pending without timeout fallback display', () => {
  const {harness, elements, timers} = loadAudioHarness({liveMode: true});
  const chat = elements.get('chat');
  harness.viewerControl.activeAudioViewerId = harness.viewerControl.clientId;

  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'shiro',
    to: 'mio',
    content: 'ownerを失ったらtimeout表示しません。',
    session_id: 'idle-owner-transfer',
    message_id: 'idle-owner-transfer:msg:0001',
    turn_index: 1,
    timestamp: liveTimestamp(1000),
  });
  assert.equal(timers.length, 1);

  harness.handleViewerActiveControlEvent({
    type: 'viewer.active_control',
    content: JSON.stringify({
      kind: 'audio',
      action: 'claim',
      viewer_client_id: 'viewer-tab-new-owner',
      active_audio_viewer_id: 'viewer-tab-new-owner',
      active_input_viewer_id: '',
    }),
  });
  timers[0]();

  assert.equal(chat.children.length, 0);
  assert.ok(harness.idleLiveRenderedLog.some((item) =>
    item.kind === 'pending_skipped' &&
    item.message_id === 'idle-owner-transfer:msg:0001' &&
    item.content.includes('NON_ACTIVE_AUDIO_VIEWER_PENDING_SKIPPED')
  ));
});

test('live mode ignores historical idlechat messages instead of arming pending tts timeout', () => {
  const {harness, elements, timers} = loadAudioHarness({liveMode: true});
  const chat = elements.get('chat');

  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'mio',
    to: 'shiro',
    content: '履歴から再送された発話です。',
    session_id: 'idle-history-replay',
    message_id: 'idle-history-replay:msg:0001',
    turn_index: 1,
    timestamp: '2026-05-09T00:00:01Z',
  });

  assert.equal(chat.children.length, 0);
  assert.equal(timers.length, 0);
  assert.equal(harness.idleLiveRenderedLog.some((item) => item.message_id === 'idle-history-replay:msg:0001'), false);
});

test('live mode rejects conflicting idlechat identity instead of overwriting an existing message', () => {
  const {harness, elements} = loadAudioHarness({liveMode: true});
  const chat = elements.get('chat');

  harness.hydrateIdleLiveTranscript('idle-conflict', [
    {
      type: 'idlechat.message',
      from: 'mio',
      to: 'shiro',
      content: '正しい1番目です。',
      session_id: 'idle-conflict',
      message_id: 'idle-conflict:msg:0001',
      turn_index: 1,
      timestamp: liveTimestamp(1000),
    },
  ]);
  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'mio',
    to: 'shiro',
    content: '正しい1番目です。',
    session_id: 'idle-conflict',
    message_id: 'idle-conflict:msg:0001',
    turn_index: 1,
    timestamp: liveTimestamp(1000),
  });
  harness.setCentralTTSSpeechText('mio', '正しいTTS断片です。', 'idle-conflict', 0, 'idle-conflict:msg:0001:utt:0000', 'idle-conflict:0001', 'idle-conflict:msg:0001', 1);
  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'shiro',
    to: 'mio',
    content: '同じturnに別messageを混ぜようとしています。',
    session_id: 'idle-conflict',
    message_id: 'idle-conflict:msg:9999',
    turn_index: 1,
    timestamp: liveTimestamp(2000),
  });

  assert.equal(chat.children.length, 1);
  assert.ok(chat.children[0]._mc.textContent.includes('正しいTTS断片です。'));
  assert.ok(!chat.children[0]._mc.textContent.includes('別message'));
  assert.ok(harness.idleLiveRenderedLog.some((item) => item.kind === 'identity_error'));
});

test('idlechat.topic is a topic boundary and is not rendered in live central chat', () => {
  const {harness, elements} = loadAudioHarness({liveMode: true});
  const chat = elements.get('chat');

  harness.addIdleMsgToTimeline({
    type: 'idlechat.topic',
    from: 'user',
    to: 'mio',
    content: '今日のお題（external）: 記憶と風景の関係',
    session_id: 'idle-topic-type',
    message_id: 'idle-topic-type:topic',
    turn_index: 0,
    timestamp: '2026-05-09T00:00:00Z',
  });

  assert.equal(chat.children.length, 0);
});

test('idlechat tts from an older session is ignored after a new topic starts', () => {
  const {harness, elements} = loadAudioHarness();
  const idleLiveLog = elements.get('idleLiveLog');

  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'user',
    to: 'mio',
    content: '今日のお題: 現在の話題',
    session_id: 'idle-current',
    timestamp: '2026-05-09T00:00:00+09:00',
  });

  harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      session_id: 'idle-old',
      response_id: 'idle-old:0000',
      utterance_id: 'idle-old:0000',
      chunk_index: 0,
      character_id: 'mio',
      text: '古い話題のチャンクです。',
      display_text: '古い話題のチャンクです。',
      audio_url: '/audio/idle-old.wav',
    }),
  });

  assert.equal(idleLiveLog.children.length, 1);
  assert.equal(idleLiveLog.children[0].classList.contains('idle-kind-topic'), true);
  assert.equal(harness.ttsPlayback.queue.length, 0);
});

test('idlechat live timeline switches to a new topic instead of mixing topics', () => {
  const {harness, elements} = loadAudioHarness();
  const idleLiveLog = elements.get('idleLiveLog');

  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'user',
    to: 'mio',
    content: '今日のお題: 最初の話題',
    session_id: 'idle-topic-a',
    timestamp: '2026-05-09T00:00:00+09:00',
  });
  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'mio',
    to: 'shiro',
    content: '最初の話題の発話です。',
    session_id: 'idle-topic-a',
    timestamp: '2026-05-09T00:00:01+09:00',
  });
  assert.equal(idleLiveLog.children.length, 2);

  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'user',
    to: 'mio',
    content: '今日のお題: 次の話題',
    session_id: 'idle-topic-b',
    timestamp: '2026-05-09T00:01:00+09:00',
  });

  assert.equal(idleLiveLog.children.length, 1);
  assert.equal(idleLiveLog.children[0]._mc.textContent, '');
  assert.equal(idleLiveLog.children[0].classList.contains('idle-pending-tts'), true);
  assert.ok(!idleLiveLog.children[0].innerHTML.includes('最初の話題の発話です。'));
});

test('idlechat forecast topic event is treated as topic boundary', () => {
  const {harness, elements} = loadAudioHarness();
  const idleLiveLog = elements.get('idleLiveLog');

  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'user',
    to: 'mio',
    content: '今日のお題: 通常話題',
    session_id: 'idle-topic-normal',
    timestamp: '2026-05-09T00:00:00+09:00',
  });
  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'mio',
    to: 'shiro',
    content: '通常話題の発話です。',
    session_id: 'idle-topic-normal',
    timestamp: '2026-05-09T00:00:01+09:00',
  });

  harness.addIdleMsgToTimeline({
    type: 'idlechat.message',
    from: 'user',
    to: 'mio',
    content: 'お題は、未来展望の話題',
    session_id: 'forecast-topic-1',
    timestamp: '2026-05-09T00:02:00+09:00',
  });

  assert.equal(idleLiveLog.children.length, 1);
  assert.ok(idleLiveLog.children[0].classList.contains('idle-kind-topic'));
  assert.equal(idleLiveLog.children[0]._mc.textContent, '');
  assert.equal(idleLiveLog.children[0].classList.contains('idle-pending-tts'), true);
});

test('live mode audio button mirrors state and unlocks audio', async () => {
  const {harness, elements} = loadAudioHarness();
  const audioBtn = elements.get('audioBtn');
  const liveAudioBtn = elements.get('liveAudioBtn');

  harness.updateAudioButton();
  assert.equal(audioBtn.getAttribute('aria-label'), '音声を有効化');
  assert.equal(liveAudioBtn.getAttribute('aria-label'), '音声を有効化');

  await liveAudioBtn.click();

  assert.equal(harness.ttsPlayback.audioEnabled, true);
  assert.equal(harness.ttsPlayback.unlocked, true);
  assert.equal(audioBtn.getAttribute('aria-label'), '音声は有効です');
  assert.equal(liveAudioBtn.getAttribute('aria-label'), '音声は有効です');
  assert.ok(liveAudioBtn.classList.contains('ready'));
});

test('viewer audio element is prepared for inline mobile playback', async () => {
  const {harness} = loadAudioHarness();

  const audio = await harness.chatAudioSync.ensureAudio();

  assert.equal(audio.playsInline, true);
  assert.equal(audio.getAttribute('playsinline'), '');
  assert.equal(audio.getAttribute('webkit-playsinline'), '');
});

test('tts chunk is shown when audio play resolves even if media events are missed', async () => {
  const {harness, elements} = loadAudioHarness();

  harness.enqueueTTSAudio('/audio/tail.wav', 'mio', 'session-tail', 'default', 7, '末尾の音声です。', '末尾の表示です。', '', 'tail-7');
  await Promise.resolve();

  assert.equal(harness.ttsPlayback.playing, true);
  assert.equal(elements.get('chat').children.at(-1)._mc.textContent, '末尾の表示です。');
});

test('idlechat starts first chunk immediately and preloads following chunks', async () => {
  const {harness, elements} = loadAudioHarness();

  harness.enqueueTTSAudio('/audio/idle-0.wav', 'mio', 'idle-session-1', 'default', 0, '最初です。', '最初です。', '', 'idle-0');
  await Promise.resolve();

  assert.equal(harness.ttsPlayback.playing, true);
  assert.equal(harness.ttsPlayback.queue.length, 0);
  assert.equal(elements.get('idleLiveLog').children.at(-1)._mc.textContent, '最初です。');

  harness.enqueueTTSAudio('/audio/idle-1.wav', 'mio', 'idle-session-1', 'default', 1, '次です。', '次です。', '', 'idle-1');
  await Promise.resolve();

  assert.equal(harness.ttsPlayback.playing, true);
  assert.equal(harness.ttsPlayback.queue.length, 1);
  assert.ok(FakeAudio.instances.find((audio) => audio !== harness.ttsPlayback.audio && audio.src === '/audio/idle-1.wav'));
  assert.equal(elements.get('idleLiveLog').children.at(-1)._mc.textContent, '最初です。');
});

test('idlechat starts a single chunk before session completed', async () => {
  const {harness, elements} = loadAudioHarness();

  harness.enqueueTTSAudio('/audio/idle-only.wav', 'shiro', 'idle-session-done', 'default', 0, '一つだけです。', '一つだけです。', '', 'idle-only');
  await Promise.resolve();
  assert.equal(harness.ttsPlayback.playing, true);
  assert.equal(elements.get('idleLiveLog').children.at(-1)._mc.textContent, '一つだけです。');

  harness.chatAudioSync.markSessionCompleted('idle-session-done');
  await Promise.resolve();

  assert.equal(harness.ttsPlayback.playing, true);
  assert.equal(elements.get('idleLiveLog').children.at(-1)._mc.textContent, '一つだけです。');
});

test('idlechat session completed without an observed chunk does not ack playback', async () => {
  const fetchCalls = [];
  const {harness} = loadAudioHarness({
    fetch: (url, init) => {
      fetchCalls.push({url, init});
      return Promise.resolve({ok: true});
    },
  });

  harness.chatAudioSync.handleEvent({
    type: 'tts.session_completed',
    content: JSON.stringify({session_id: 'idle-missed', response_id: 'idle-missed:0000'}),
  });
  await Promise.resolve();

  assert.equal(fetchCalls.filter((call) => call.url === '/viewer/tts/playback-ack').length, 0);
});

test('idlechat audio chunk claims active audio viewer before playback ack', async () => {
  const fetchCalls = [];
  const {harness} = loadAudioHarness({
    fetch: (url, init) => {
      fetchCalls.push({url, init});
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({active_audio_viewer_id: harness.viewerControl.clientId}),
      });
    },
  });

  harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      session_id: 'idle-claim',
      response_id: 'idle-claim:0000',
      utterance_id: 'idle-claim:0000',
      chunk_index: 0,
      character_id: 'mio',
      text: '開始します。',
      display_text: '開始します。',
      audio_url: '/audio/idle-claim.wav',
    }),
  });
  harness.chatAudioSync.markSessionCompleted('idle-claim', 'idle-claim:0000');
  await Promise.resolve();

  const claim = fetchCalls.find((call) => call.url === '/viewer/active-control' && JSON.parse(call.init.body).action === 'claim');
  assert.ok(claim, 'audio chunk should claim active audio owner');
	assert.equal(JSON.parse(claim.init.body).kind, 'audio');
	assert.equal(JSON.parse(claim.init.body).viewer_client_id, harness.viewerControl.clientId);

  harness.ttsPlayback.audio.listeners.ended();
  await Promise.resolve();

  const ack = fetchCalls.find((call) => call.url === '/viewer/tts/playback-ack');
  assert.ok(ack, 'natural audio end should send playback ack');
	assert.equal(JSON.parse(ack.init.body).viewer_client_id, harness.viewerControl.clientId);
});

test('idlechat tts from a new session is accepted after an interrupt state', async () => {
  const {harness} = loadAudioHarness();
  harness.state.idleChat.chatActive = false;
  harness.state.idleChat.interrupted = true;
  harness.state.idleChat.interruptedSessionId = 'idle-old-session';

  assert.equal(harness.isIdleChatActiveForTTS('forecast-new-session'), true);
  assert.equal(harness.state.idleChat.chatActive, false);
  assert.equal(harness.state.idleChat.interrupted, true);
  assert.equal(harness.state.idleChat.interruptedSessionId, 'idle-old-session');
});

test('turning audio off releases the active audio viewer owner', async () => {
  const fetchCalls = [];
  const {harness, elements} = loadAudioHarness({
    fetch: (url, init) => {
      fetchCalls.push({url, init});
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({active_audio_viewer_id: ''}),
      });
    },
  });
  const audioBtn = elements.get('audioBtn');

  harness.ttsPlayback.audioEnabled = true;
  harness.ttsPlayback.unlocked = true;
  harness.ttsPlayback.blocked = false;
  harness.viewerControl.activeAudioViewerId = harness.viewerControl.clientId;
  await audioBtn.click();
  await Promise.resolve();

  assert.equal(harness.viewerControl.activeAudioViewerId, '');
  const release = fetchCalls.find((call) => call.url === '/viewer/active-control' && JSON.parse(call.init.body).action === 'release');
  assert.ok(release, 'audio off should release active audio owner');
  assert.equal(JSON.parse(release.init.body).kind, 'audio');
});

test('viewer active control release event clears local audio owner', () => {
  const {harness} = loadAudioHarness();
  harness.viewerControl.activeAudioViewerId = harness.viewerControl.clientId;

  harness.handleViewerActiveControlEvent({
    type: 'viewer.active_control',
    content: JSON.stringify({
      kind: 'audio',
      action: 'release',
      viewer_client_id: harness.viewerControl.clientId,
      active_audio_viewer_id: '',
      active_input_viewer_id: '',
    }),
  });

  assert.equal(harness.viewerControl.activeAudioViewerId, '');
});

test('idlechat playback ack waits for natural audio end after session completed', async () => {
  const fetchCalls = [];
  const {harness} = loadAudioHarness({
    fetch: (url, init) => {
      fetchCalls.push({url, init});
      return Promise.resolve({ok: true});
    },
  });

  harness.enqueueTTSAudio('/audio/idle-ack.wav', 'mio', 'idle-ack', 'default', 0, '再生します。', '再生します。', 'idle-ack:0000', 'idle-ack:0000');
  await Promise.resolve();

  harness.chatAudioSync.markSessionCompleted('idle-ack', 'idle-ack:0000');
  await Promise.resolve();
  assert.equal(fetchCalls.filter((call) => call.url === '/viewer/tts/playback-ack').length, 0);

  harness.ttsPlayback.audio.listeners.ended();
  await Promise.resolve();

  const ackCalls = fetchCalls.filter((call) => call.url === '/viewer/tts/playback-ack');
  assert.equal(ackCalls.length, 1);
	assert.equal(JSON.parse(ackCalls[0].init.body).status, 'ended');
});

test('idlechat display-only tts sends error ack with error_code instead of fallback status', async () => {
  const fetchCalls = [];
  const {harness, elements, timers} = loadAudioHarness({
    fetch: (url, init) => {
      fetchCalls.push({url, init});
      return Promise.resolve({ok: true});
    },
  });

  harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      session_id: 'forecast-display-only',
      response_id: 'forecast-display-only:0001',
      utterance_id: 'forecast-display-only:topic:0000:utt:0000',
      message_id: 'forecast-display-only:topic:0000',
      turn_index: 2,
      chunk_index: 0,
      character_id: 'mio',
      text: '音声URLなしの本文です。',
      display_text: '音声URLなしの本文です。',
      audio_url: '',
    }),
  });
  harness.chatAudioSync.handleEvent({
    type: 'tts.session_completed',
    content: JSON.stringify({
      session_id: 'forecast-display-only',
      response_id: 'forecast-display-only:0001',
      utterance_id: 'forecast-display-only:topic:0000:utt:0000',
      message_id: 'forecast-display-only:topic:0000',
      turn_index: 2,
      character_id: 'mio',
    }),
  });

  assert.ok(elements.get('idleLiveLog').children.at(-1)._mc.innerHTML.includes('TTS_AUDIO_MISSING'));
  assert.equal(elements.get('idleLiveLog').children.at(-1)._mc.innerHTML.includes('音声URLなしの本文です。'), false);

  timers.shift()();
  await Promise.resolve();

  const ack = fetchCalls.find((call) => call.url === '/viewer/tts/playback-ack');
  assert.ok(ack, 'display-only idlechat TTS should ack as explicit playback error');
  const payload = JSON.parse(ack.init.body);
  assert.equal(payload.response_id, 'forecast-display-only:0001');
  assert.equal(payload.status, 'error');
  assert.equal(payload.error_code, 'TTS_AUDIO_MISSING');
  assert.match(payload.error, /missing idlechat audio url/i);
});

test('idlechat playback ack keeps utterance id when session completed arrives after playback', async () => {
  const fetchCalls = [];
  const {harness} = loadAudioHarness({
    fetch: (url, init) => {
      fetchCalls.push({url, init});
      return Promise.resolve({ok: true});
    },
  });
  harness.viewerControl.activeAudioViewerId = harness.viewerControl.clientId;

  harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      session_id: 'idle-late-complete',
      response_id: 'idle-late-complete:0000',
      utterance_id: 'idle-late-complete:msg:0001:utt:0000',
      message_id: 'idle-late-complete:msg:0001',
      turn_index: 1,
      chunk_index: 0,
      character_id: 'mio',
      text: '先に再生が終わります。',
      display_text: '先に再生が終わります。',
      audio_url: '/audio/idle-late-complete.wav',
    }),
  });
  harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      session_id: 'idle-late-complete',
      response_id: 'idle-late-complete:0001',
      utterance_id: 'idle-late-complete:msg:0002:utt:0000',
      message_id: 'idle-late-complete:msg:0002',
      turn_index: 2,
      chunk_index: 1,
      character_id: 'shiro',
      text: '次の音声です。',
      display_text: '次の音声です。',
      audio_url: '/audio/idle-late-complete-2.wav',
    }),
  });
  await Promise.resolve();

  harness.ttsPlayback.audio.listeners.ended();
  await Promise.resolve();
  assert.equal(fetchCalls.filter((call) => call.url === '/viewer/tts/playback-ack').length, 0);

  harness.chatAudioSync.handleEvent({
    type: 'tts.session_completed',
    content: JSON.stringify({
      session_id: 'idle-late-complete',
      response_id: 'idle-late-complete:0000',
      utterance_id: 'idle-late-complete:msg:0001:utt:0000',
      message_id: 'idle-late-complete:msg:0001',
      turn_index: 1,
      character_id: 'mio',
    }),
  });
  await Promise.resolve();

  const ackCalls = fetchCalls.filter((call) => call.url === '/viewer/tts/playback-ack');
  assert.equal(ackCalls.length, 1);
  const payload = JSON.parse(ackCalls[0].init.body);
  assert.equal(payload.status, 'completed_after_playback');
  assert.equal(payload.utterance_id, 'idle-late-complete:msg:0001:utt:0000');
  assert.equal(payload.message_id, 'idle-late-complete:msg:0001');
  assert.equal(payload.turn_index, 1);
});

test('central chat starts a new bubble after current tts speech is cleared', () => {
  const {harness, elements} = loadAudioHarness();
  const chat = elements.get('chat');

  harness.setCentralTTSSpeechText('mio', '最初の発話です。', 'session-1', 0, 'u1');
  harness.setCentralTTSSpeechText('', '');
  harness.setCentralTTSSpeechText('mio', '次の発話です。', 'session-1', 0, 'u2');

  assert.equal(chat.children.length, 2);
  assert.equal(chat.children[0]._mc.textContent, '最初の発話です。');
  assert.equal(chat.children[1]._mc.textContent, '次の発話です。');
});

test('central chat separates adjacent tts chunks inside one bubble', () => {
  const {harness, elements} = loadAudioHarness();

  harness.setCentralTTSSpeechText('shiro', '前半です。', 'session-1', 0, 'u1');
  harness.setCentralTTSSpeechText('shiro', '後半です。', 'session-1', 1, 'u2');

  assert.equal(elements.get('chat').children.at(-1)._mc.textContent, '前半です。 後半です。');
});

test('central chat keeps different chunks from the same utterance id', () => {
  const {harness, elements} = loadAudioHarness();

  harness.setCentralTTSSpeechText('shiro', '前半です。', 'session-1', 0, 'utterance-1');
  harness.setCentralTTSSpeechText('shiro', '後半です。', 'session-1', 1, 'utterance-1');

  assert.equal(elements.get('chat').children.at(-1)._mc.textContent, '前半です。 後半です。');
});

test('tts queue keeps different audio chunks from the same utterance id', async () => {
  const {harness} = loadAudioHarness();

  harness.enqueueTTSAudio('/audio/first.wav', 'mio', 'session-1', 'default', 0, 'first speech', '一つ目です。', 'response-1', 'utterance-1');
  harness.enqueueTTSAudio('/audio/second.wav', 'mio', 'session-1', 'default', 1, 'second speech', '二つ目です。', 'response-1', 'utterance-1');
  await Promise.resolve();

  assert.equal(harness.ttsPlayback.currentChunkIndex, 0);
  assert.equal(harness.ttsPlayback.audio.src, '/audio/first.wav');
  assert.equal(harness.ttsPlayback.queue.length, 1);
  assert.equal(harness.ttsPlayback.queue[0].chunkIndex, 1);
});

test('central chat keeps same speaker speech chunks in one bubble after audio clears', () => {
  const {harness, elements} = loadAudioHarness();
  const chat = elements.get('chat');

  harness.setCentralTTSSpeechText('shiro', '前半です。', 'session-1', 0, 'speech-0');
  harness.setCentralTTSSpeechText('', '');
  harness.setCentralTTSSpeechText('shiro', '後半です。', 'session-1', 1, 'speech-1');

  assert.equal(chat.children.length, 1);
  assert.equal(chat.children[0]._mc.textContent, '前半です。 後半です。');
});

test('central chat splits same speaker speech when response id changes', () => {
  const {harness, elements} = loadAudioHarness();
  const chat = elements.get('chat');

  harness.setCentralTTSSpeechText('mio', 'ひとつめです。', 'idle-response-boundary', 0, 'chunk-0', 'idle-response-boundary:0000');
  harness.setCentralTTSSpeechText('', '');
  harness.setCentralTTSSpeechText('mio', 'ふたつめです。', 'idle-response-boundary', 1, 'chunk-1', 'idle-response-boundary:0001');

  assert.equal(chat.children.length, 0);
  assert.equal(elements.get('idleLiveLog').children.length, 2);
  assert.equal(elements.get('idleLiveLog').children[0]._mc.textContent, 'ひとつめです。');
  assert.equal(elements.get('idleLiveLog').children[1]._mc.textContent, 'ふたつめです。');
});

test('central chat splits when speaker changes even inside chunk sequence', () => {
  const {harness, elements} = loadAudioHarness();
  const chat = elements.get('chat');

  harness.setCentralTTSSpeechText('mio', 'みおの発話です。', 'session-1', 0, 'mio-0');
  harness.setCentralTTSSpeechText('shiro', 'しろの発話です。', 'session-1', 1, 'shiro-1');

  assert.equal(chat.children.length, 2);
  assert.equal(chat.children[0]._mc.textContent, 'みおの発話です。');
  assert.equal(chat.children[1]._mc.textContent, 'しろの発話です。');
});

test('central chat keeps topic announcement chunks in one bubble after audio clears', () => {
  const {harness, elements} = loadAudioHarness();
  const idleLiveLog = elements.get('idleLiveLog');

  harness.setCentralTTSSpeechText('mio', '今日のお題です、', 'idle-topic-1', 0, 'topic-0');
  harness.setCentralTTSSpeechText('', '');
  harness.setCentralTTSSpeechText('mio', '記憶と風景の関係です！', 'idle-topic-1', 1, 'topic-1');

  assert.equal(elements.get('chat').children.length, 0);
  assert.equal(idleLiveLog.children.length, 1);
  assert.equal(idleLiveLog.children[0]._mc.textContent, '今日のお題です、記憶と風景の関係です！');
});

test('central chat starts speech bubble after topic announcement completes', () => {
  const {harness, elements} = loadAudioHarness();
  const idleLiveLog = elements.get('idleLiveLog');

  harness.setCentralTTSSpeechText('mio', '今日のお題です、', 'idle-topic-1', 0, 'topic-0');
  harness.setCentralTTSSpeechText('', '');
  harness.setCentralTTSSpeechText('mio', '記憶と風景の関係です！', 'idle-topic-1', 1, 'topic-1');
  harness.setCentralTTSSpeechText('', '');
  harness.setCentralTTSSpeechText('mio', 'それ、少し切ない入口だね。', 'idle-topic-1', 0, 'speech-0');

  assert.equal(elements.get('chat').children.length, 0);
  assert.equal(idleLiveLog.children.length, 2);
  assert.equal(idleLiveLog.children[0]._mc.textContent, '今日のお題です、記憶と風景の関係です！');
  assert.equal(idleLiveLog.children[1]._mc.textContent, 'それ、少し切ない入口だね。');
});
