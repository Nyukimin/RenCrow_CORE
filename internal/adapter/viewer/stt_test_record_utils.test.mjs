import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

function loadUtils() {
  const code = fs.readFileSync('internal/adapter/viewer/assets/js/stt_test_record_utils.js', 'utf8');
  const sandbox = {};
  vm.createContext(sandbox);
  vm.runInContext(code, sandbox);
  return sandbox;
}

function writeTone(pcm16, start, length, amplitude) {
  for (let i = 0; i < length; i++) {
    pcm16[start + i] = amplitude;
  }
}

test('trimSTTPcmSilence removes leading and trailing silence', () => {
  const utils = loadUtils();
  const pcm16 = new Int16Array(1600);
  writeTone(pcm16, 320, 800, 4000);
  const trimmed = utils.trimSTTPcmSilence(pcm16, { minLevel: 8, minVoiceMs: 100, frameSamples: 160 });
  assert.ok(trimmed.length > 0);
  assert.ok(trimmed.length < pcm16.length);
  assert.equal(trimmed[0], 4000);
  const toneSamples = Array.from(trimmed).filter((value) => value === 4000).length;
  assert.ok(toneSamples >= 700);
});

test('trimSTTPcmSilence keeps short internal pauses', () => {
  const utils = loadUtils();
  const pcm16 = new Int16Array(3200);
  writeTone(pcm16, 160, 480, 5000);
  writeTone(pcm16, 960, 480, 5000);
  writeTone(pcm16, 1760, 480, 5000);
  const trimmed = utils.trimSTTPcmSilence(pcm16, { minLevel: 8, minVoiceMs: 300, frameSamples: 160 });
  assert.ok(trimmed.length >= 2400);
});

test('trimSTTPcmSilence edgeOnly keeps internal pauses but removes outer silence', () => {
  const utils = loadUtils();
  const pcm16 = new Int16Array(4800);
  writeTone(pcm16, 800, 3200, 5000);
  const trimmed = utils.trimSTTPcmSilence(pcm16, { minLevel: 8, minVoiceMs: 300, frameSamples: 160, edgeOnly: true });
  assert.ok(trimmed.length >= 3000);
  assert.equal(trimmed[0], 5000);
});

test('trimSTTPcmSilence returns empty array for silence-only input', () => {
  const utils = loadUtils();
  const trimmed = utils.trimSTTPcmSilence(new Int16Array(1600), { minLevel: 8 });
  assert.equal(trimmed.length, 0);
});
