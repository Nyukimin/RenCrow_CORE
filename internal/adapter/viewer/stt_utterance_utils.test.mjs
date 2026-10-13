import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

function loadUtils() {
  const code = fs.readFileSync('internal/adapter/viewer/assets/js/stt_utterance_utils.js', 'utf8');
  const sandbox = {};
  vm.createContext(sandbox);
  vm.runInContext(code, sandbox);
  return sandbox;
}

function ramp(start, length) {
  const out = new Int16Array(length);
  for (let i = 0; i < length; i++) out[i] = start + i;
  return out;
}

test('preroll buffer keeps only the most recent preRollMs of audio', () => {
  const utils = loadUtils();
  const preroll = utils.createSTTPrerollBuffer({ sampleRate: 16000, preRollMs: 300 });
  // 10 frames of 100 ms = 1 s; only the last 300 ms (4800 samples) may remain.
  for (let i = 0; i < 10; i++) preroll.push(ramp(i * 1600, 1600));
  assert.equal(preroll.length, 4800);
  const taken = preroll.take();
  assert.equal(taken.length, 4800);
  assert.equal(taken[0], 7 * 1600, 'oldest retained sample is the start of the 8th frame');
  assert.equal(taken[taken.length - 1], 10 * 1600 - 1, 'newest sample is last, so audio stays contiguous');
});

test('preroll buffer trims inside a frame instead of dropping or keeping it whole', () => {
  const utils = loadUtils();
  const preroll = utils.createSTTPrerollBuffer({ sampleRate: 16000, preRollMs: 300 });
  preroll.push(ramp(0, 4096));
  preroll.push(ramp(4096, 4096));
  const taken = preroll.take();
  assert.equal(taken.length, 4800);
  assert.equal(taken[0], 8192 - 4800);
  assert.equal(taken[taken.length - 1], 8191);
});

test('preroll take empties the buffer and push copies its input', () => {
  const utils = loadUtils();
  const preroll = utils.createSTTPrerollBuffer({ sampleRate: 16000, preRollMs: 300 });
  const frame = ramp(100, 800);
  preroll.push(frame);
  frame.fill(0); // the capture loop reuses its buffers
  const taken = preroll.take();
  assert.equal(taken[0], 100);
  assert.equal(taken[799], 899);
  assert.equal(preroll.length, 0);
  assert.equal(preroll.take().length, 0);
});

test('preroll buffer under the limit returns everything it has and clear discards it', () => {
  const utils = loadUtils();
  const preroll = utils.createSTTPrerollBuffer({ sampleRate: 16000, preRollMs: 300 });
  preroll.push(ramp(0, 1000));
  assert.equal(preroll.length, 1000);
  preroll.clear();
  assert.equal(preroll.length, 0);
  assert.equal(preroll.take().length, 0);
});

test('preroll buffer with preRollMs 0 retains nothing', () => {
  const utils = loadUtils();
  const preroll = utils.createSTTPrerollBuffer({ sampleRate: 16000, preRollMs: 0 });
  preroll.push(ramp(0, 1600));
  assert.equal(preroll.length, 0);
});

test('preroll buffer defaults to 300 ms at 16 kHz', () => {
  const utils = loadUtils();
  const preroll = utils.createSTTPrerollBuffer();
  preroll.push(new Int16Array(16000));
  assert.equal(preroll.length, 4800);
});

test('preroll buffer ignores empty and invalid input', () => {
  const utils = loadUtils();
  const preroll = utils.createSTTPrerollBuffer();
  preroll.push(null);
  preroll.push(undefined);
  preroll.push(new Int16Array(0));
  assert.equal(preroll.length, 0);
});

test('utterance ids are short printable and unique per counter', () => {
  const utils = loadUtils();
  const a = utils.createSTTUtteranceID(1700000000000, 1);
  const b = utils.createSTTUtteranceID(1700000000000, 2);
  assert.notEqual(a, b);
  for (const id of [a, b]) {
    assert.match(id, /^[A-Za-z0-9_-]{1,64}$/, 'must satisfy CORE utterance id rules (printable, at most 64 chars)');
  }
});
