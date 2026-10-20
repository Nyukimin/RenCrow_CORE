import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import test from 'node:test';

const require = createRequire(import.meta.url);
const { initialize, validateDiagnosticManifest, validateManifest } = require('./speech_eval.js');

const DIAGNOSTIC_SET_ID = 'diagnostic-20261020-mio';

function manifestFixture() {
  return {
    schema_version: 'rencrow.speech-eval/1',
    set_id: 'trial-set-01',
    truth_key: 'private-answer-map',
    items: Array.from({ length: 20 }, (_, index) => ({
      id: String(index + 1).padStart(2, '0'),
      text: `Evaluation prompt ${index + 1}`,
      intent: 'gentle reassurance',
      cue_applicable: index % 2 === 0,
      cue_hint: index % 2 === 0 ? 'セリフの直前に短い間を置く' : '',
      a_url: `/viewer/tts/audio?path=speech-eval/trial-set-01/${index + 1}-a.wav`,
      b_url: `/viewer/tts/audio?path=speech-eval/trial-set-01/${index + 1}-b.wav`,
    })),
  };
}

function diagnosticManifestFixture() {
  const variants = [
    { id: 'original', label: '原音' },
    { id: 'gain-6db', label: '<img src=x onerror=alert(1)>' },
    { id: 'legacy', label: '旧経路の後処理' },
  ];
  return {
    schema_version: 'rencrow.speech-diagnostic/1',
    set_id: DIAGNOSTIC_SET_ID,
    text: '大丈夫です。ここで一緒に整理していきましょう。',
    groups: ['neutral', 'expressive'].map((id) => ({
      id,
      label: id === 'neutral' ? '通常' : '感情表現',
      clips: variants.map((variant) => ({
        ...variant,
        url: `/viewer/tts/audio?path=speech-eval/${DIAGNOSTIC_SET_ID}/case01-${id}-${variant.id}.wav`,
      })),
    })),
  };
}

class FakeElement {
  constructor(tagName = 'div', id = '') {
    this.tagName = tagName.toUpperCase();
    this.id = id;
    this.children = [];
    this.dataset = {};
    this.listeners = new Map();
    this.attributes = {};
    this.classList = { add() {}, remove() {}, toggle() {} };
    this.value = '';
    this.disabled = false;
    this.hidden = false;
    this.controls = false;
    this.preload = '';
    this.src = '';
    this.currentTime = 0;
    this.pauseCount = 0;
    this._textContent = '';
  }

  set textContent(value) {
    this._textContent = String(value ?? '');
    this.children = [];
  }

  get textContent() {
    return this._textContent + this.children.map((child) => child.textContent).join('');
  }

  addEventListener(type, callback) {
    if (!this.listeners.has(type)) this.listeners.set(type, []);
    this.listeners.get(type).push(callback);
  }

  dispatch(type) {
    for (const callback of this.listeners.get(type) || []) callback({ target: this, currentTarget: this });
  }

  appendChild(child) {
    this.children.push(child);
    return child;
  }

  replaceChildren(...children) {
    this._textContent = '';
    this.children = [...children];
  }

  setAttribute(name, value) {
    this.attributes[name] = String(value);
  }

  querySelectorAll(selector) {
    const matches = (element) => {
      if (selector === 'audio') return element.tagName === 'AUDIO';
      if (selector === 'select[data-rating]') return element.tagName === 'SELECT' && Boolean(element.dataset.rating);
      if (selector === '[data-rating]') return Boolean(element.dataset.rating);
      return false;
    };
    const found = [];
    const visit = (element) => {
      for (const child of element.children) {
        if (matches(child)) found.push(child);
        visit(child);
      }
    };
    visit(this);
    return found;
  }

  pause() {
    this.pauseCount += 1;
  }

  play() {
    this.dispatch('play');
    return Promise.resolve();
  }

  click() {
    this.dispatch('click');
  }
}

class FakeDocument {
  constructor() {
    this.elements = new Map();
    for (const id of [
      'speechEvalStatus',
      'speechEvalProgress',
      'speechEvalCaseNumber',
      'speechEvalText',
      'speechEvalIntent',
      'speechEvalCueHint',
      'speechEvalPair',
      'speechEvalPreference',
      'speechEvalPrevious',
      'speechEvalNext',
      'speechEvalExportJson',
      'speechEvalExportCsv',
      'speechEvalDiagnosticPanel',
      'speechEvalDiagnosticText',
      'speechEvalDiagnosticGroups',
    ]) {
      this.elements.set(id, new FakeElement('div', id));
    }
    this.elements.get('speechEvalDiagnosticPanel').hidden = true;
    this.elements.get('speechEvalDiagnosticPanel').appendChild(this.elements.get('speechEvalDiagnosticText'));
    this.elements.get('speechEvalDiagnosticPanel').appendChild(this.elements.get('speechEvalDiagnosticGroups'));
    this.downloads = [];
  }

  getElementById(id) {
    return this.elements.get(id) || null;
  }

  createElement(tagName) {
    const element = new FakeElement(tagName);
    if (tagName === 'a') this.downloads.push(element);
    return element;
  }
}

function fakeBrowser(manifest, storage = new Map(), diagnosticResponse = null, search = '') {
  const blobs = new Map();
  let blobIndex = 0;
  const document = new FakeDocument();
  const storageAPI = {
    getItem(key) {
      return storage.has(key) ? storage.get(key) : null;
    },
    setItem(key, value) {
      storage.set(key, value);
    },
  };
  const browser = {
    document,
    localStorage: storageAPI,
    location: { origin: 'http://localhost:18790', search },
    Blob,
    requests: [],
    URL: {
      createObjectURL(blob) {
        const url = `blob:test-${blobIndex++}`;
        blobs.set(url, blob);
        return url;
      },
      revokeObjectURL() {},
    },
    async fetch(url, options) {
      const request = { url, options };
      browser.requests.push(request);
      if (url === '/viewer/tts/audio?path=speech-eval/manifest.json') {
        browser.manifestRequest = request;
        return { ok: true, async json() { return manifest; } };
      }
      if (url === '/viewer/tts/audio?path=speech-eval/diagnostic.json') {
        browser.diagnosticRequest = request;
        if (!diagnosticResponse || !diagnosticResponse.ok) return diagnosticResponse || { ok: false, status: 404 };
        return { ok: true, async json() { return diagnosticResponse.manifest; } };
      }
      return { ok: false, status: 404 };
    },
  };
  browser.blobs = blobs;
  browser.storage = storage;
  return browser;
}

function countElements(root, tagName) {
  let count = 0;
  const visit = (element) => {
    if (element.tagName === tagName) count += 1;
    for (const child of element.children) visit(child);
  };
  visit(root);
  return count;
}

test('manifest validation requires the public 20-item schema and safe local audio paths', () => {
  const normalized = validateManifest(manifestFixture(), 'http://localhost:18790');
  assert.equal(normalized.items.length, 20);
  assert.equal(normalized.items[0].a_url, '/viewer/tts/audio?path=speech-eval/trial-set-01/1-a.wav');
  assert.equal(Object.keys(normalized.items[0]).length, 7);
  assert.equal(normalized.items[0].cue_hint, 'セリフの直前に短い間を置く');
  assert.equal(normalized.items[1].cue_hint, '');
  assert.equal(Object.hasOwn(normalized, 'truth_key'), false);
  assert.equal(Object.hasOwn(normalized.items[0], 'truth_key'), false);

  const tooShort = manifestFixture();
  tooShort.items.pop();
  assert.throws(() => validateManifest(tooShort, 'http://localhost:18790'), /20/);

  const unsafe = manifestFixture();
  unsafe.items[0].a_url = 'https://other.example/audio.wav';
  assert.throws(() => validateManifest(unsafe, 'http://localhost:18790'), /audio|音声/i);

  const traversal = manifestFixture();
  traversal.items[0].b_url = '/viewer/tts/audio?path=speech-eval%2F..%2Fsecret.wav';
  assert.throws(() => validateManifest(traversal, 'http://localhost:18790'), /audio|音声/i);

  const mismatchedSet = manifestFixture();
  mismatchedSet.items[0].a_url = '/viewer/tts/audio?path=speech-eval/another-set/01-a.wav';
  assert.throws(() => validateManifest(mismatchedSet, 'http://localhost:18790'), /audio|音声/i);

  const otherServedFile = manifestFixture();
  otherServedFile.items[0].a_url = '/viewer/tts/audio?path=speech-eval/trial-set-01/manifest.json';
  assert.throws(() => validateManifest(otherServedFile, 'http://localhost:18790'), /audio|音声/i);

  const missingCueHint = manifestFixture();
  delete missingCueHint.items[0].cue_hint;
  assert.throws(() => validateManifest(missingCueHint, 'http://localhost:18790'), /cue_hint/i);

  const hintWithoutCue = manifestFixture();
  hintWithoutCue.items[1].cue_hint = 'キューのヒント';
  assert.throws(() => validateManifest(hintWithoutCue, 'http://localhost:18790'), /cue_hint/i);
});

test('diagnostic comparison validates its fixed groups and renders native audio players separately', async () => {
  assert.equal(typeof validateDiagnosticManifest, 'function', 'diagnostic schema validation should be available');
  const rawDiagnostic = diagnosticManifestFixture();
  const normalized = validateDiagnosticManifest(rawDiagnostic, 'http://localhost:18790');
  assert.equal(normalized.groups.length, 2);
  assert.deepEqual(normalized.groups.map((group) => group.id), ['neutral', 'expressive']);
  assert.equal(normalized.groups[0].clips.length, 3);
  assert.equal(normalized.groups[0].clips[0].url, `/viewer/tts/audio?path=speech-eval/${DIAGNOSTIC_SET_ID}/case01-neutral-original.wav`);

  const browser = fakeBrowser(manifestFixture(), new Map(), { ok: true, manifest: rawDiagnostic }, '?diagnostic=mio');
  const app = await initialize(browser);
  const panel = browser.document.getElementById('speechEvalDiagnosticPanel');
  const groups = browser.document.getElementById('speechEvalDiagnosticGroups');
  const diagnosticAudios = groups.querySelectorAll('audio');
  const pairAudios = browser.document.getElementById('speechEvalPair').querySelectorAll('audio');

  assert.equal(browser.diagnosticRequest.url, '/viewer/tts/audio?path=speech-eval/diagnostic.json');
  assert.equal(browser.diagnosticRequest.options.credentials, 'omit');
  assert.equal(panel.hidden, false);
  assert.equal(groups.children.length, 2);
  assert.equal(diagnosticAudios.length, 6);
  assert.equal(diagnosticAudios.every((audio) => audio.controls), true);
  assert.ok(panel.textContent.includes(rawDiagnostic.text));
  assert.ok(panel.textContent.includes('<img src=x onerror=alert(1)>'));
  assert.equal(countElements(panel, 'IMG'), 0, 'labels should be inserted as text, not parsed as HTML');

  await diagnosticAudios[0].play();
  await diagnosticAudios[1].play();
  assert.ok(diagnosticAudios[0].pauseCount > 0, 'diagnostic playback should stop another diagnostic clip');
  await pairAudios[0].play();
  assert.ok(diagnosticAudios[1].pauseCount > 0, 'A/B playback should stop diagnostic playback');
  assert.equal(diagnosticAudios[1].currentTime, 0);

  app.navigate(1);
  assert.ok(pairAudios[0].pauseCount > 0, 'A/B navigation should continue to stop previous A/B playback');
  const nextPairAudio = browser.document.getElementById('speechEvalPair').querySelectorAll('audio')[0];
  await diagnosticAudios[2].play();
  assert.ok(nextPairAudio.pauseCount > 0, 'diagnostic playback should still stop A/B after navigation');
});

test('diagnostic manifest rejects external, traversal, cross-set, and non-WAV audio URLs', () => {
  assert.equal(typeof validateDiagnosticManifest, 'function', 'diagnostic schema validation should be available');
  const external = diagnosticManifestFixture();
  external.groups[0].clips[0].url = 'https://other.example/audio.wav';
  assert.throws(() => validateDiagnosticManifest(external, 'http://localhost:18790'), /audio|音声/i);

  const traversal = diagnosticManifestFixture();
  traversal.groups[0].clips[0].url = `/viewer/tts/audio?path=speech-eval/${DIAGNOSTIC_SET_ID}/../secret.wav`;
  assert.throws(() => validateDiagnosticManifest(traversal, 'http://localhost:18790'), /audio|音声/i);

  const crossSet = diagnosticManifestFixture();
  crossSet.groups[0].clips[0].url = '/viewer/tts/audio?path=speech-eval/another-set/case01.wav';
  assert.throws(() => validateDiagnosticManifest(crossSet, 'http://localhost:18790'), /audio|音声/i);

  const extraParameter = diagnosticManifestFixture();
  extraParameter.groups[0].clips[0].url += '&download=1';
  assert.throws(() => validateDiagnosticManifest(extraParameter, 'http://localhost:18790'), /audio|音声/i);

  const wrongExtension = diagnosticManifestFixture();
  wrongExtension.groups[0].clips[0].url = `/viewer/tts/audio?path=speech-eval/${DIAGNOSTIC_SET_ID}/case01-neutral-original.mp3`;
  assert.throws(() => validateDiagnosticManifest(wrongExtension, 'http://localhost:18790'), /audio|音声/i);

  const scriptPath = diagnosticManifestFixture();
  scriptPath.groups[0].clips[0].url = `/viewer/tts/audio?path=speech-eval/${DIAGNOSTIC_SET_ID}/%3Cscript%3E.wav`;
  assert.throws(() => validateDiagnosticManifest(scriptPath, 'http://localhost:18790'), /audio|音声/i);
});

test('missing optional diagnostic manifest hides only the comparison and preserves the 20-case page', async () => {
  const browser = fakeBrowser(manifestFixture(), new Map(), { ok: false, status: 404 }, '?diagnostic=mio');
  await initialize(browser);

  assert.equal(browser.diagnosticRequest?.url, '/viewer/tts/audio?path=speech-eval/diagnostic.json');
  assert.equal(browser.document.getElementById('speechEvalDiagnosticPanel').hidden, true);
  assert.equal(browser.document.getElementById('speechEvalDiagnosticGroups').children.length, 0);
  assert.equal(browser.document.getElementById('speechEvalText').textContent, 'Evaluation prompt 1');
  assert.equal(browser.document.getElementById('speechEvalPair').querySelectorAll('audio').length, 2);
  assert.equal(browser.document.getElementById('speechEvalProgress').textContent, '0 / 20 件評価済み');
  assert.match(browser.document.getElementById('speechEvalStatus').textContent, /20件のケースを読み込みました/);
});

test('standard 20-case page does not request the optional diagnostic manifest', async () => {
  const browser = fakeBrowser(manifestFixture());
  await initialize(browser);

  assert.equal(browser.document.getElementById('speechEvalDiagnosticPanel').hidden, true);
  assert.equal(browser.requests.length, 1);
  assert.equal(browser.requests[0].url, '/viewer/tts/audio?path=speech-eval/manifest.json');
  assert.equal(browser.diagnosticRequest, undefined);
  assert.equal(browser.document.getElementById('speechEvalText').textContent, 'Evaluation prompt 1');
  assert.equal(browser.document.getElementById('speechEvalPair').querySelectorAll('audio').length, 2);
  assert.match(browser.document.getElementById('speechEvalStatus').textContent, /20件のケースを読み込みました/);
});

test('ratings persist per set, case navigation stops old audio, and reload restores saved scores', async () => {
  const source = manifestFixture();
  const browser = fakeBrowser(source);
  const app = await initialize(browser);

  assert.equal(browser.manifestRequest.url, '/viewer/tts/audio?path=speech-eval/manifest.json');
  assert.equal(browser.manifestRequest.options.credentials, 'omit');
  assert.equal(browser.document.getElementById('speechEvalText').textContent, source.items[0].text);
  assert.equal(browser.document.getElementById('speechEvalIntent').textContent, source.items[0].intent);
  assert.equal(browser.document.getElementById('speechEvalCueHint').textContent, source.items[0].cue_hint);

  const firstAudio = browser.document.getElementById('speechEvalPair').querySelectorAll('audio')[0];
  await firstAudio.play();
  app.setRating('01', 'a', 'intent_fit', 5);
  app.setRating('01', 'a', 'cue_position_fit', 4);
  app.setPreference('01', 'tie');
  app.navigate(1);
  assert.ok(firstAudio.pauseCount > 0, 'switching cases should stop the previous audio');

  const saved = [...browser.storage.values()].map((value) => JSON.parse(value))[0];
  assert.equal(saved.set_id, source.set_id);
  assert.equal(saved.cases['01'].a.intent_fit, 5);
  assert.equal(saved.cases['01'].a.cue_position_fit, 4);
  assert.equal(saved.cases['01'].preference, 'tie');

  const restoredBrowser = fakeBrowser(source, browser.storage);
  await initialize(restoredBrowser);
  const restoredRating = restoredBrowser.document.getElementById('speechEvalPair')
    .querySelectorAll('select[data-rating]')
    .find((select) => select.dataset.clip === 'a' && select.dataset.rating === 'intent_fit');
  assert.equal(restoredRating.value, '5');
  assert.equal(restoredBrowser.document.getElementById('speechEvalPreference').value, 'tie');
});

test('JSON and CSV exports contain ratings and preference without truth data or audio URLs', async () => {
  const source = manifestFixture();
  const browser = fakeBrowser(source);
  const app = await initialize(browser);
  app.setRating('01', 'a', 'intent_fit', 4);
  app.setPreference('01', 'a');
  app.exportJSON();
  app.exportCSV();

  const downloads = browser.document.downloads;
  assert.equal(downloads.length, 2);
  const json = await browser.blobs.get(downloads[0].href).text();
  const csv = await browser.blobs.get(downloads[1].href).text();
  const exported = JSON.parse(json);
  assert.equal(exported.set_id, 'trial-set-01');
  assert.equal(exported.cases[0].cue_hint, source.items[0].cue_hint);
  assert.equal(exported.cases[0].preference, 'a');
  assert.equal(exported.cases[0].ratings.a.intent_fit, 4);
  assert.equal(Object.hasOwn(exported, 'truth_key'), false);
  assert.doesNotMatch(json, /private-answer-map|a_url|b_url/);
  assert.match(csv, /"set_id","case_id","text","intent","cue_hint","cue_applicable"/);
  assert.match(csv, /"trial-set-01","01"/);
  assert.doesNotMatch(csv, /truth_key|private-answer-map|speech-eval\/trial-set-01\/1-a\.wav/);
});

test('blank ratings and preference clear saved values and update progress, exports, and reload', async () => {
  const source = manifestFixture();
  const browser = fakeBrowser(source);
  const app = await initialize(browser);
  const pair = browser.document.getElementById('speechEvalPair');

  for (const select of pair.querySelectorAll('select[data-rating]')) {
    select.value = '1';
    select.dispatch('change');
  }
  const preference = browser.document.getElementById('speechEvalPreference');
  preference.value = 'a';
  preference.dispatch('change');
  assert.equal(browser.document.getElementById('speechEvalProgress').textContent, '1 / 20 件評価済み');

  const intentFit = pair.querySelectorAll('select[data-rating]')
    .find((select) => select.dataset.clip === 'a' && select.dataset.rating === 'intent_fit');
  intentFit.value = '';
  intentFit.dispatch('change');
  preference.value = '';
  preference.dispatch('change');

  assert.equal(browser.document.getElementById('speechEvalProgress').textContent, '0 / 20 件評価済み');
  const saved = [...browser.storage.values()].map((value) => JSON.parse(value))[0];
  assert.equal(Object.hasOwn(saved.cases['01'].a, 'intent_fit'), false);
  assert.equal(saved.cases['01'].preference, null);

  app.exportJSON();
  const exported = JSON.parse(await browser.blobs.get(browser.document.downloads[0].href).text());
  assert.equal(exported.cases[0].ratings.a.intent_fit, null);
  assert.equal(exported.cases[0].preference, null);
  app.exportCSV();
  const csv = await browser.blobs.get(browser.document.downloads[1].href).text();
  const firstRow = csv.split('\r\n')[1].split(',');
  assert.equal(firstRow[6], '""');
  assert.equal(firstRow[16], '""');

  const restoredBrowser = fakeBrowser(source, browser.storage);
  await initialize(restoredBrowser);
  const restoredRating = restoredBrowser.document.getElementById('speechEvalPair')
    .querySelectorAll('select[data-rating]')
    .find((select) => select.dataset.clip === 'a' && select.dataset.rating === 'intent_fit');
  assert.equal(restoredRating.value, '');
  assert.equal(restoredBrowser.document.getElementById('speechEvalPreference').value, '');
  assert.equal(restoredBrowser.document.getElementById('speechEvalProgress').textContent, '0 / 20 件評価済み');
});
