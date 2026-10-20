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

function sourceBetween(source, startMarker, endMarker) {
  const start = source.indexOf(startMarker);
  const end = source.indexOf(endMarker, start + startMarker.length);
  assert.ok(start >= 0, `start marker not found: ${startMarker}`);
  assert.ok(end > start, `end marker not found: ${endMarker}`);
  return source.slice(start, end);
}

function loadAudioHarness(options = {}) {
  FakeAudio.playOutcomes = [...(options.playOutcomes || [])];
  FakeAudio.instances = [];
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const memoryJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/memory.js', 'utf8');
  const timelineJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/timeline.js', 'utf8');
  const idleJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/idlechat.js', 'utf8');
  const start = js.indexOf('const ttsPlayback = {');
  const end = js.indexOf('let sending = false;');
  assert.ok(start > 0, 'ttsPlayback block not found');
  assert.ok(end > start, 'audio handler block end not found');
  const chatInterruption = sourceBetween(js, 'function acceptSpeechCancelCapability(payload, requestGeneration) {', 'function handleChatInputIntent(reason)');
  const sendViewerMessage = sourceBetween(js, 'async function sendViewerMessage(message, attachments = []) {', 'async function requestRepairFromChat()');
  const audioBlock = js.slice(start, end);
  const source = memoryJs + '\n' + timelineJs + '\n' + idleJs + '\n' + audioBlock + '\n' + chatInterruption + '\n' + sendViewerMessage + `
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
  idleLiveRenderTarget,
  chatAudioSync,
  interruptChatOutputForUserInput,
  sendViewerMessage,
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
    buildViewerSendRequest: (message) => ({message: String(message || ''), session_id: 'viewer-test-session'}),
    short: (value, limit) => String(value || '').slice(0, limit),
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

test('tts playback url uses same-origin proxy for absolute audio urls', () => {
  const {harness} = loadAudioHarness();

  const got = harness.resolveTTSPlaybackURL('http://192.168.1.207:7870/audio/sample.wav', '');

  assert.equal(got, '/viewer/tts/audio?url=http%3A%2F%2F192.168.1.207%3A7870%2Faudio%2Fsample.wav');
});

test('tts playback url preserves same-origin root-relative audio urls', () => {
  const {harness} = loadAudioHarness();
  const localAudioURL = '/viewer/tts/audio?path=voices/cache/sample.wav';

  assert.equal(harness.resolveTTSPlaybackURL(localAudioURL, ''), localAudioURL);
});

test('tts playback url proxies protocol-relative remote audio urls', () => {
  const {harness} = loadAudioHarness();

  const got = harness.resolveTTSPlaybackURL('//192.168.1.207:7870/audio/sample.wav', '');

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
  assert.equal(payload.public_playback_ref, 'idle-autoplay:0000');
  assert.equal(Object.hasOwn(payload, 'response_id'), false);
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
      public_playback_ref: 'chat-interrupt-response',
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
      public_playback_ref: 'display-interrupt-response',
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

test('SpeechPlan utterances play in order with the requested pause_after_ms', async () => {
  const {harness, timers} = loadAudioHarness();
  harness.viewerControl.activeAudioViewerId = harness.viewerControl.clientId;
  const publishChunk = (chunkIndex, pauseAfterMs) => harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      audio_url: '/audio/speech-plan-' + chunkIndex + '.wav',
      session_id: 'chat-speech-plan-session',
      public_playback_ref: 'speech-plan-response',
      turn_id: 'turn-speech-plan',
      pause_after_ms: pauseAfterMs,
      utterance_id: 'speech-plan-utterance-' + chunkIndex,
      chunk_index: chunkIndex,
      character_id: 'mio',
      text: 'speech ' + chunkIndex,
      display_text: '発話 ' + chunkIndex + '。',
    }),
  });

  publishChunk(0, 150);
  publishChunk(1, 0);
  await Promise.resolve();

  assert.equal(harness.ttsPlayback.currentChunkIndex, 0);
  assert.ok(harness.ttsPlayback.audio.src.endsWith('speech-plan-0.wav'));
  assert.equal(harness.ttsPlayback.queue.length, 1);

  harness.ttsPlayback.audio.listeners.ended();
  assert.equal(timers.at(-1).ms, 150, 'SpeechPlan pause overrides the legacy punctuation gap');
  timers.pop()();
  await Promise.resolve();

  assert.equal(harness.ttsPlayback.currentChunkIndex, 1);
  assert.ok(harness.ttsPlayback.audio.src.endsWith('speech-plan-1.wav'));
  assert.equal(harness.ttsPlayback.queue.length, 0);
});

test('SpeechPlan pause_after_ms zero overrides the legacy punctuation gap', async () => {
  const {harness, timers} = loadAudioHarness();
  harness.viewerControl.activeAudioViewerId = harness.viewerControl.clientId;
  const publishChunk = (chunkIndex) => harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      audio_url: '/audio/speech-plan-zero-' + chunkIndex + '.wav',
      session_id: 'chat-speech-plan-zero-session',
      public_playback_ref: 'speech-plan-zero-response',
      turn_id: 'turn-speech-plan-zero',
      pause_after_ms: 0,
      utterance_id: 'speech-plan-zero-utterance-' + chunkIndex,
      chunk_index: chunkIndex,
      character_id: 'mio',
      text: 'speech ' + chunkIndex,
      display_text: '発話 ' + chunkIndex + '。',
    }),
  });
  publishChunk(0);
  publishChunk(1);
  await Promise.resolve();

  harness.ttsPlayback.audio.listeners.ended();

  assert.equal(timers.at(-1).ms, 0);
  timers.pop()();
  await Promise.resolve();
  assert.equal(harness.ttsPlayback.currentChunkIndex, 1);
});

test('SpeechPlan progress reports one started and one natural ended for the active audio viewer', async () => {
  const calls = [];
  const {harness} = loadAudioHarness({
    fetch: (url, init) => {
      calls.push({url, init});
      return Promise.resolve({ok: true, status: 204});
    },
  });
  harness.viewerControl.activeAudioViewerId = harness.viewerControl.clientId;
  const speechProgressCalls = () => calls.filter((call) => call.url === '/viewer/tts/progress');
  harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      audio_url: '/audio/progress-turn.wav',
      session_id: 'chat-progress-session',
      public_playback_ref: 'progress-response',
      turn_id: 'progress-turn',
      utterance_id: 'progress-utterance',
      chunk_index: 3,
      character_id: 'mio',
      text: 'progress text',
      display_text: '進捗の音声です。',
    }),
  });
  await Promise.resolve();

  assert.equal(speechProgressCalls().length, 0, 'play() promise resolution alone is not an actual playback event');
  harness.ttsPlayback.audio.listeners.playing();
  harness.ttsPlayback.audio.listeners.timeupdate();
  harness.ttsPlayback.audio.listeners.timeupdate();
  harness.ttsPlayback.audio.listeners.ended();
  harness.ttsPlayback.audio.listeners.ended();
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(speechProgressCalls().length, 2);
  const progress = speechProgressCalls().map((call) => JSON.parse(call.init.body));
  assert.deepEqual(progress.map((item) => item.phase), ['started', 'ended']);
  progress.forEach((item) => assert.deepEqual(item, {
    public_playback_ref: 'progress-response',
    turn_id: 'progress-turn',
    utterance_index: 3,
    phase: item.phase,
    viewer_client_id: harness.viewerControl.clientId,
  }));
});

test('SpeechPlan audio error reports error while preserving Viewer fallback behavior', async () => {
  const calls = [];
  const {harness, elements} = loadAudioHarness({
    fetch: (url, init) => {
      calls.push({url, init});
      return Promise.resolve({ok: true, status: 204});
    },
  });
  harness.viewerControl.activeAudioViewerId = harness.viewerControl.clientId;
  const speechProgressCalls = () => calls.filter((call) => call.url === '/viewer/tts/progress');
  harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      audio_url: '/audio/error-progress-turn.wav',
      session_id: 'chat-error-progress-session',
      public_playback_ref: 'error-progress-response',
      turn_id: 'error-progress-turn',
      utterance_id: 'error-progress-utterance',
      chunk_index: 1,
      character_id: 'mio',
      text: 'fallback text',
      display_text: '音声失敗時もテキストを表示します。',
    }),
  });
  await Promise.resolve();

  harness.ttsPlayback.audio.listeners.error();

  assert.equal(speechProgressCalls().length, 1);
  assert.deepEqual(JSON.parse(speechProgressCalls()[0].init.body), {
    public_playback_ref: 'error-progress-response',
    turn_id: 'error-progress-turn',
    utterance_index: 1,
    phase: 'error',
    viewer_client_id: harness.viewerControl.clientId,
  });
  assert.ok(elements.get('chat').children.some((el) => el.querySelector('.mc').textContent.includes('音声失敗時もテキスト')));
});

test('SpeechPlan text fallback reports error for the active utterance', () => {
  const calls = [];
  const {harness} = loadAudioHarness({
    fetch: (url, init) => {
      calls.push({url, init});
      return Promise.resolve({ok: true, status: 204});
    },
  });
  harness.viewerControl.activeAudioViewerId = harness.viewerControl.clientId;
  const speechProgressCalls = () => calls.filter((call) => call.url === '/viewer/tts/progress');
  harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      session_id: 'chat-text-fallback-session',
      public_playback_ref: 'text-fallback-response',
      turn_id: 'text-fallback-turn',
      utterance_id: 'text-fallback-utterance',
      chunk_index: 2,
      character_id: 'mio',
      text: 'fallback text',
      display_text: '音声なしのテキストfallbackです。',
    }),
  });

  assert.equal(speechProgressCalls().length, 1);
  assert.deepEqual(JSON.parse(speechProgressCalls()[0].init.body), {
    public_playback_ref: 'text-fallback-response',
    turn_id: 'text-fallback-turn',
    utterance_index: 2,
    phase: 'error',
    viewer_client_id: harness.viewerControl.clientId,
  });
});

test('same-turn user interruption reports cancelled once for the current SpeechPlan utterance', async () => {
  const calls = [];
  const {harness} = loadAudioHarness({
    fetch: (url, init) => {
      calls.push({url, init});
      return Promise.resolve({ok: true, status: 204});
    },
  });
  harness.viewerControl.activeAudioViewerId = harness.viewerControl.clientId;
  const speechProgressCalls = () => calls.filter((call) => call.url === '/viewer/tts/progress');
  harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      audio_url: '/audio/cancelled-progress-turn.wav',
      session_id: 'chat-cancelled-progress-session',
      public_playback_ref: 'cancelled-progress-response',
      turn_id: 'cancelled-progress-turn',
      utterance_id: 'cancelled-progress-utterance',
      chunk_index: 4,
      character_id: 'mio',
      text: 'interrupt me',
      display_text: '割り込み対象の発話です。',
    }),
  });
  await Promise.resolve();
  harness.ttsPlayback.audio.listeners.playing();

  harness.interruptChatOutputForUserInput('user_input');
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(harness.ttsPlayback.playing, false);
  assert.equal(speechProgressCalls().length, 2);
  assert.deepEqual(speechProgressCalls().map((call) => JSON.parse(call.init.body).phase), ['started', 'cancelled']);
  const cancellation = JSON.parse(speechProgressCalls()[1].init.body);
  assert.equal(cancellation.turn_id, 'cancelled-progress-turn');
  assert.equal(cancellation.utterance_index, 4);
});

test('SpeechPlan progress is sent only while this tab owns audio playback', async () => {
  const calls = [];
  const {harness} = loadAudioHarness({
    fetch: (url, init) => {
      calls.push({url, init});
      return Promise.resolve({ok: true, status: 204});
    },
  });
  harness.viewerControl.activeAudioViewerId = harness.viewerControl.clientId;
  const speechProgressCalls = () => calls.filter((call) => call.url === '/viewer/tts/progress');
  harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      audio_url: '/audio-other-owner.wav',
      session_id: 'chat-other-owner-session',
      public_playback_ref: 'other-owner-response',
      turn_id: 'other-owner-turn',
      utterance_id: 'other-owner-utterance',
      chunk_index: 5,
      character_id: 'mio',
      text: 'owner switched',
      display_text: '所有者が切り替わります。',
    }),
  });
  await Promise.resolve();

  harness.viewerControl.activeAudioViewerId = 'another-viewer';
  harness.ttsPlayback.audio.listeners.playing();
  assert.equal(speechProgressCalls().length, 0);
  harness.viewerControl.activeAudioViewerId = harness.viewerControl.clientId;
  harness.ttsPlayback.audio.listeners.timeupdate();
  harness.viewerControl.activeAudioViewerId = 'another-viewer';
  harness.ttsPlayback.audio.listeners.ended();

  assert.deepEqual(speechProgressCalls().map((call) => JSON.parse(call.init.body).phase), ['started']);
});

test('SpeechPlan progress request failures do not interrupt playback or text rendering', async () => {
  const progressCalls = [];
  const {harness, elements, timers} = loadAudioHarness({
    fetch: (url, init) => {
      if (url === '/viewer/tts/progress') {
        progressCalls.push({url, init});
        return Promise.reject(new Error('progress route unavailable'));
      }
      return Promise.resolve({ok: true, status: 204});
    },
  });
  harness.viewerControl.activeAudioViewerId = harness.viewerControl.clientId;
  harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      audio_url: '/audio/progress-unavailable.wav',
      session_id: 'chat-progress-unavailable-session',
      public_playback_ref: 'progress-unavailable-response',
      turn_id: 'progress-unavailable-turn',
      utterance_id: 'progress-unavailable-utterance',
      chunk_index: 6,
      character_id: 'mio',
      text: 'still plays',
      display_text: '進捗APIが失敗しても音声を再生します。',
    }),
  });
  await Promise.resolve();

  harness.ttsPlayback.audio.listeners.playing();
  harness.ttsPlayback.audio.listeners.ended();
  await new Promise((resolve) => setImmediate(resolve));
  const retryDelays = [];
  while (true) {
    const retryIndex = timers.findIndex((timer) => timer.ms === 100 || timer.ms === 300);
    if (retryIndex < 0) break;
    const retry = timers.splice(retryIndex, 1)[0];
    retryDelays.push(retry.ms);
    retry();
    await new Promise((resolve) => setImmediate(resolve));
  }

  assert.deepEqual(progressCalls.map((call) => JSON.parse(call.init.body).phase), [
    'started', 'started', 'started',
  ]);
  assert.deepEqual(retryDelays, [100, 300]);
  assert.equal(harness.ttsPlayback.playing, false);
  assert.ok(elements.get('chat').children.some((el) => el.querySelector('.mc').textContent.includes('進捗APIが失敗しても音声')));
});

test('SpeechPlan progress phases are serialized and retries use bounded backoff', async () => {
  const calls = [];
  let startedAttempts = 0;
  const {harness, timers} = loadAudioHarness({
    fetch: (url, init) => {
      if (url !== '/viewer/tts/progress') return Promise.resolve({ok: true, status: 204});
      const payload = JSON.parse(init.body);
      calls.push(payload);
      if (payload.phase === 'started' && ++startedAttempts < 3) return Promise.reject(new Error('started unavailable'));
      return Promise.resolve({ok: true, status: 204});
    },
  });
  harness.viewerControl.activeAudioViewerId = harness.viewerControl.clientId;
  harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      audio_url: '/audio/ordered-progress.wav',
      session_id: 'chat-ordered-progress-session',
      public_playback_ref: 'ordered-progress-response',
      turn_id: 'ordered-progress-turn',
      utterance_id: 'ordered-progress-utterance',
      chunk_index: 7,
      character_id: 'mio',
      text: 'ordered phases',
      display_text: '開始後に終了を通知します。',
    }),
  });
  await Promise.resolve();
  harness.ttsPlayback.audio.listeners.playing();
  harness.ttsPlayback.audio.listeners.ended();
  assert.deepEqual(calls.map((item) => item.phase), ['started'], 'terminal must wait for the started request');
  assert.equal(harness.ttsPlayback.playing, false, 'audio completion must not wait for the network');

  const retryDelays = [];
  for (const expectedDelay of [100, 300]) {
    await new Promise((resolve) => setImmediate(resolve));
    const retryIndex = timers.findIndex((timer) => timer.ms === expectedDelay);
    assert.notEqual(retryIndex, -1, 'expected bounded retry delay ' + expectedDelay + 'ms');
    const retry = timers.splice(retryIndex, 1)[0];
    retryDelays.push(retry.ms);
    retry();
  }
  await new Promise((resolve) => setImmediate(resolve));

  assert.deepEqual(retryDelays, [100, 300]);
  assert.deepEqual(calls.map((item) => item.phase), ['started', 'started', 'started', 'ended']);
  assert.equal(harness.ttsPlayback.playing, false, 'network retries must not hold up audio completion');
});

test('SpeechPlan progress orders adjacent utterances by accepted terminal phase', async () => {
  const calls = [];
  let resolveEndedZero;
  const endedZero = new Promise((resolve) => {
    resolveEndedZero = resolve;
  });
  const {harness, timers} = loadAudioHarness({
    fetch: (url, init) => {
      if (url !== '/viewer/tts/progress') return Promise.resolve({ok: true, status: 204});
      const payload = JSON.parse(init.body);
      calls.push(payload);
      if (payload.phase === 'ended' && payload.utterance_index === 0) return endedZero;
      return Promise.resolve({ok: true, status: 204});
    },
  });
  harness.viewerControl.activeAudioViewerId = harness.viewerControl.clientId;
  for (const index of [0, 1]) {
    harness.chatAudioSync.handleEvent({
      type: 'tts.audio_chunk',
      content: JSON.stringify({
        audio_url: '/audio/ordered-turn-' + index + '.wav',
        session_id: 'chat-ordered-turn-session',
        public_playback_ref: 'ordered-turn-response',
        turn_id: 'ordered-turn-id',
        pause_after_ms: 0,
        utterance_id: 'ordered-turn-utterance-' + index,
        chunk_index: index,
        character_id: 'mio',
        text: 'utterance ' + index,
        display_text: '発話' + index,
      }),
    });
  }
  await Promise.resolve();

  harness.ttsPlayback.audio.listeners.playing();
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(calls.map((item) => [item.utterance_index, item.phase]), [[0, 'started']]);

  harness.ttsPlayback.audio.listeners.ended();
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(calls.map((item) => [item.utterance_index, item.phase]), [[0, 'started'], [0, 'ended']]);

  const tailTimer = timers.at(-1);
  assert.equal(tailTimer.ms, 0, 'the next utterance is ready without an extra delay');
  tailTimer();
  await Promise.resolve();
  harness.ttsPlayback.audio.listeners.playing();
  await new Promise((resolve) => setImmediate(resolve));

  assert.deepEqual(
    calls.map((item) => [item.utterance_index, item.phase]),
    [[0, 'started'], [0, 'ended']],
    'started(1) must wait until ended(0) is accepted',
  );

  resolveEndedZero({ok: true, status: 204});
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(calls.map((item) => [item.utterance_index, item.phase]), [
    [0, 'started'], [0, 'ended'], [1, 'started'],
  ]);
});

test('SpeechPlan progress stops later utterances after an earlier phase exhausts retries', async () => {
  const calls = [];
  const {harness, timers} = loadAudioHarness({
    fetch: (url, init) => {
      if (url !== '/viewer/tts/progress') return Promise.resolve({ok: true, status: 204});
      const payload = JSON.parse(init.body);
      calls.push(payload);
      return payload.phase === 'ended' && payload.utterance_index === 0
        ? Promise.reject(new Error('ended unavailable'))
        : Promise.resolve({ok: true, status: 204});
    },
  });
  harness.viewerControl.activeAudioViewerId = harness.viewerControl.clientId;
  for (const index of [0, 1]) {
    harness.chatAudioSync.handleEvent({
      type: 'tts.audio_chunk',
      content: JSON.stringify({
        audio_url: '/audio/retry-turn-' + index + '.wav',
        session_id: 'chat-retry-turn-session',
        public_playback_ref: 'retry-turn-response',
        turn_id: 'retry-turn-id',
        pause_after_ms: 0,
        utterance_id: 'retry-turn-utterance-' + index,
        chunk_index: index,
        character_id: 'mio',
        text: 'utterance ' + index,
        display_text: '再試行' + index,
      }),
    });
  }
  await Promise.resolve();
  harness.ttsPlayback.audio.listeners.playing();
  await new Promise((resolve) => setImmediate(resolve));
  harness.ttsPlayback.audio.listeners.ended();
  await new Promise((resolve) => setImmediate(resolve));

  const tailTimerIndex = timers.findIndex((timer) => timer.ms === 0);
  assert.notEqual(tailTimerIndex, -1, 'the next utterance remains playable while progress retries');
  timers.splice(tailTimerIndex, 1)[0]();
  await Promise.resolve();
  harness.ttsPlayback.audio.listeners.playing();
  await new Promise((resolve) => setImmediate(resolve));

  for (const expectedDelay of [100, 300]) {
    const retryIndex = timers.findIndex((timer) => timer.ms === expectedDelay);
    assert.notEqual(retryIndex, -1, 'expected bounded retry delay ' + expectedDelay + 'ms');
    timers.splice(retryIndex, 1)[0]();
    await new Promise((resolve) => setImmediate(resolve));
  }

  harness.ttsPlayback.audio.listeners.ended();
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(calls.map((item) => [item.utterance_index, item.phase]), [
    [0, 'started'], [0, 'ended'], [0, 'ended'], [0, 'ended'],
  ]);
  assert.equal(harness.ttsPlayback.playing, false, 'progress failure does not hold up playback');
});

test('a pending cancelled turn does not block progress for a later turn in the same response', async () => {
  const calls = [];
  let resolveTurnACancel;
  const turnACancel = new Promise((resolve) => {
    resolveTurnACancel = resolve;
  });
  const {harness} = loadAudioHarness({
    fetch: (url, init) => {
      if (url !== '/viewer/tts/progress') return Promise.resolve({ok: true, status: 204});
      const payload = JSON.parse(init.body);
      calls.push(payload);
      if (payload.turn_id === 'turn-a' && payload.phase === 'cancelled') return turnACancel;
      return Promise.resolve({ok: true, status: 204});
    },
  });
  harness.viewerControl.activeAudioViewerId = harness.viewerControl.clientId;
  for (const turnID of ['turn-a', 'turn-b']) {
    harness.chatAudioSync.handleEvent({
      type: 'tts.audio_chunk',
      content: JSON.stringify({
        audio_url: '/audio/' + turnID + '.wav',
        session_id: 'chat-shared-response-session',
        public_playback_ref: 'shared-response-ref',
        turn_id: turnID,
        utterance_id: turnID + '-utterance',
        chunk_index: 0,
        character_id: 'mio',
        text: turnID,
        display_text: turnID,
      }),
    });
  }
  await Promise.resolve();
  harness.ttsPlayback.audio.listeners.playing();
  await new Promise((resolve) => setImmediate(resolve));

  harness.chatAudioSync.interruptTurn('turn-a');
  await Promise.resolve();
  harness.ttsPlayback.audio.listeners.playing();
  await new Promise((resolve) => setImmediate(resolve));

  assert.deepEqual(calls.map((item) => [item.turn_id, item.phase]), [
    ['turn-a', 'started'], ['turn-a', 'cancelled'], ['turn-b', 'started'],
  ]);
  resolveTurnACancel({ok: true, status: 204});
});

test('local SpeechPlan chunks render text without entering Viewer audio or progress', async () => {
  const calls = [];
  const {harness, elements} = loadAudioHarness({
    fetch: (url, init) => {
      calls.push({url, init});
      return Promise.resolve({ok: true, status: 204});
    },
  });
  harness.viewerControl.activeAudioViewerId = harness.viewerControl.clientId;

  harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      audio_url: '/audio/local-only.wav',
      session_id: 'chat-local-output-session',
      public_playback_ref: 'local-output-response',
      turn_id: 'local-output-turn',
      playback_output: 'local',
      utterance_id: 'local-output-utterance',
      chunk_index: 0,
      character_id: 'mio',
      speech_text: 'ローカル再生中の本文',
      display_text: 'ローカル再生中の本文',
    }),
  });
  await Promise.resolve();

  assert.equal(harness.ttsPlayback.queue.length, 0, 'local output must not enter the Viewer audio queue');
  assert.equal(harness.ttsPlayback.playing, false);
  assert.ok(elements.get('chat').children.some((el) => el.querySelector('.mc').textContent.includes('ローカル再生中の本文')));
  assert.equal(calls.some((call) => call.url === '/viewer/tts/progress'), false, 'local output must not report Viewer playback progress');
});

test('same-turn cancellation stops a pending SpeechPlan progress retry', async () => {
  const calls = [];
  const {harness, timers} = loadAudioHarness({
    fetch: (url, init) => {
      if (url !== '/viewer/tts/progress') return Promise.resolve({ok: true, status: 204});
      const payload = JSON.parse(init.body);
      calls.push(payload);
      return payload.phase === 'started'
        ? Promise.reject(new Error('started unavailable'))
        : Promise.resolve({ok: true, status: 204});
    },
  });
  harness.viewerControl.activeAudioViewerId = harness.viewerControl.clientId;
  harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      audio_url: '/audio/cancel-progress-retry.wav',
      session_id: 'chat-cancel-progress-retry-session',
      public_playback_ref: 'cancel-progress-retry-response',
      turn_id: 'cancel-progress-retry-turn',
      utterance_id: 'cancel-progress-retry-utterance',
      chunk_index: 8,
      character_id: 'mio',
      text: 'cancel retry',
      display_text: '取消後は再試行しません。',
    }),
  });
  await Promise.resolve();
  harness.ttsPlayback.audio.listeners.playing();
  await new Promise((resolve) => setImmediate(resolve));
  const retryIndex = timers.findIndex((timer) => timer.ms === 100);
  assert.notEqual(retryIndex, -1, 'started failure should schedule its first bounded retry');

  harness.interruptChatOutputForUserInput('user_input');
  await new Promise((resolve) => setImmediate(resolve));
  const cancelledRetry = timers.splice(retryIndex, 1)[0];
  cancelledRetry();
  await new Promise((resolve) => setImmediate(resolve));

  assert.deepEqual(calls.map((item) => item.phase), ['started', 'cancelled']);
  assert.equal(harness.ttsPlayback.playing, false);
});

test('user input rejects a late SpeechPlan chunk with the interrupted turn ID', async () => {
  const fetchCalls = [];
  const cancelToken = 'secret-speech-cancel-capability';
  const {harness, elements} = loadAudioHarness({
    fetch: (url, init) => {
      fetchCalls.push({url, init});
      if (url === '/viewer/send') {
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({turn_id: 'turn-interrupted-by-input', speech_cancel_token: cancelToken}),
        });
      }
      if (url === '/viewer/tts/cancel') return Promise.resolve({ok: true, status: 204});
      return Promise.resolve({ok: true, status: 200});
    },
  });
  harness.viewerControl.activeAudioViewerId = harness.viewerControl.clientId;
  await harness.sendViewerMessage('new user input');
  harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      audio_url: '/audio/interrupted-turn.wav',
      session_id: 'chat-interrupted-original-session',
      public_playback_ref: 'response-before-input',
      turn_id: 'turn-interrupted-by-input',
      pause_after_ms: 300,
      utterance_id: 'interrupted-turn-utterance-0',
      chunk_index: 0,
      character_id: 'mio',
      text: 'already playing',
      display_text: '入力前の音声です。',
    }),
  });
  await Promise.resolve();
  assert.equal(harness.ttsPlayback.playing, true);
  const chatMessagesBeforeInput = elements.get('chat').children.length;

  harness.interruptChatOutputForUserInput('user_input');

  assert.equal(harness.ttsPlayback.playing, false);
  assert.equal(harness.ttsPlayback.audio.src, '');
  const cancelRequest = fetchCalls.find((call) => call.url === '/viewer/tts/cancel');
  assert.ok(cancelRequest, 'user input should cancel the accepted SpeechPlan turn');
  assert.deepEqual(JSON.parse(cancelRequest.init.body), {speech_cancel_token: cancelToken});
  harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      audio_url: '/audio/late-same-turn.wav',
      session_id: 'chat-interrupted-new-session',
      public_playback_ref: 'response-after-input',
      turn_id: 'turn-interrupted-by-input',
      pause_after_ms: 0,
      utterance_id: 'interrupted-turn-utterance-late',
      chunk_index: 1,
      character_id: 'mio',
      text: 'must not play',
      display_text: '入力後に届いた古い音声です。',
    }),
  });

  assert.equal(harness.ttsPlayback.playing, false);
  assert.equal(harness.ttsPlayback.queue.length, 0);
  assert.equal(harness.ttsPlayback.audio.src, '');
  assert.equal(elements.get('chat').children.length, chatMessagesBeforeInput);
});

test('SpeechPlan turn IDs allow a new turn in the same session and reject the interrupted turn', async () => {
  const {harness} = loadAudioHarness();
  harness.viewerControl.activeAudioViewerId = harness.viewerControl.clientId;
  const publishChunk = (turnId, utteranceId, chunkIndex) => harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      audio_url: '/audio/' + utteranceId + '.wav',
      session_id: 'shared-daily-chat-session',
      public_playback_ref: 'shared-chat-response',
      turn_id: turnId,
      pause_after_ms: 0,
      utterance_id: utteranceId,
      chunk_index: chunkIndex,
      character_id: 'mio',
      text: utteranceId,
      display_text: utteranceId,
    }),
  });

  publishChunk('interrupted-turn', 'first-turn-utterance', 0);
  await Promise.resolve();
  assert.equal(harness.ttsPlayback.currentTurnId, 'interrupted-turn');
  harness.interruptChatOutputForUserInput('user_input');
  assert.equal(harness.ttsPlayback.playing, false);

  publishChunk('next-turn', 'next-turn-utterance', 0);
  await Promise.resolve();
  assert.equal(harness.ttsPlayback.playing, true, 'a different turn must play despite sharing the session and playback ref');
  assert.equal(harness.ttsPlayback.currentTurnId, 'next-turn');
  assert.equal(harness.ttsPlayback.queue.length, 0);

  publishChunk('interrupted-turn', 'late-first-turn-utterance', 1);
  assert.equal(harness.ttsPlayback.currentTurnId, 'next-turn');
  assert.equal(harness.ttsPlayback.queue.length, 0, 'late chunks from the interrupted exact turn must be rejected');
});

test('a send receipt arriving after user input is cancelled immediately', async () => {
  const fetchCalls = [];
  let resolveSend;
  const cancelToken = 'secret-late-speech-cancel-capability';
  const {harness} = loadAudioHarness({
    fetch: (url, init) => {
      fetchCalls.push({url, init});
      if (url === '/viewer/send') return new Promise((resolve) => { resolveSend = resolve; });
      if (url === '/viewer/tts/cancel') return Promise.resolve({ok: true, status: 204});
      return Promise.resolve({ok: true, status: 200});
    },
  });
  harness.viewerControl.activeAudioViewerId = harness.viewerControl.clientId;

  const sendPromise = harness.sendViewerMessage('request still in flight');
  harness.interruptChatOutputForUserInput('user_input');
  resolveSend({
    ok: true,
    status: 200,
    json: () => Promise.resolve({turn_id: 'turn-late-receipt', speech_cancel_token: cancelToken}),
  });
  await sendPromise;
  await new Promise((resolve) => setImmediate(resolve));

  const cancelRequest = fetchCalls.find((call) => call.url === '/viewer/tts/cancel');
  assert.ok(cancelRequest, 'late acceptance must trigger cancellation without another input event');
  assert.deepEqual(JSON.parse(cancelRequest.init.body), {speech_cancel_token: cancelToken});
  harness.chatAudioSync.handleEvent({
    type: 'tts.audio_chunk',
    content: JSON.stringify({
      audio_url: '/audio/late-accepted-turn.wav',
      session_id: 'chat-late-accepted-session',
      public_playback_ref: 'late-accepted-response',
      turn_id: 'turn-late-receipt',
      pause_after_ms: 0,
      utterance_id: 'late-accepted-utterance',
      chunk_index: 0,
      character_id: 'mio',
      text: 'cancelled turn audio',
      display_text: '取消済みの遅延音声です。',
    }),
  });
  assert.equal(harness.ttsPlayback.playing, false);
  assert.equal(harness.ttsPlayback.queue.length, 0);
});

test('failed speech cancellation retains its token for a later interrupt retry', async () => {
  const fetchCalls = [];
  const cancelToken = 'secret-retry-speech-cancel-capability';
  let cancelAttempts = 0;
  const {harness} = loadAudioHarness({
    fetch: (url, init) => {
      fetchCalls.push({url, init});
      if (url === '/viewer/send') {
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({turn_id: 'turn-retry-cancel', speech_cancel_token: cancelToken}),
        });
      }
      if (url === '/viewer/tts/cancel') {
        cancelAttempts += 1;
        return Promise.resolve(cancelAttempts === 1 ? {ok: false, status: 503} : {ok: true, status: 204});
      }
      return Promise.resolve({ok: true, status: 200});
    },
  });
  await harness.sendViewerMessage('retry cancellation');

  harness.interruptChatOutputForUserInput('user_input');
  await new Promise((resolve) => setImmediate(resolve));
  harness.interruptChatOutputForUserInput('user_input_again');
  await new Promise((resolve) => setImmediate(resolve));

  const cancelRequests = fetchCalls.filter((call) => call.url === '/viewer/tts/cancel');
  assert.equal(cancelRequests.length, 2, 'a later interruption should retry the retained failed cancellation');
  cancelRequests.forEach((call) => assert.deepEqual(JSON.parse(call.init.body), {speech_cancel_token: cancelToken}));
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
      public_playback_ref: 'idle-old:0000',
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

test('idlechat timeline switches to a new topic instead of mixing topics', () => {
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
    content: JSON.stringify({session_id: 'idle-missed', public_playback_ref: 'idle-missed:0000'}),
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
      public_playback_ref: 'idle-claim:0000',
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
      public_playback_ref: 'forecast-display-only:0001',
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
      public_playback_ref: 'forecast-display-only:0001',
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
  assert.equal(payload.public_playback_ref, 'forecast-display-only:0001');
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
      public_playback_ref: 'idle-late-complete:0000',
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
      public_playback_ref: 'idle-late-complete:0001',
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
      public_playback_ref: 'idle-late-complete:0000',
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
