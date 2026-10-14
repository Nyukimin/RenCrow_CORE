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

// RenCrow_STT decides where an utterance ends. Its final (or an error, or the
// close CORE makes after an upstream failure) can therefore reach the Viewer
// while the Viewer's own VAD still believes the user is speaking and before
// the Viewer sent stop. Nothing used to end the utterance in that case: the
// Viewer sent stop on its own VAD silence and then waited 90 seconds for a
// final that STT, having closed the utterance already, never sent again.

function speakingState(overrides) {
  return { isStopping: false, vadSpeechActive: true, ws: { id: 'ws' }, ...overrides };
}

function recordingActions() {
  const calls = [];
  return { calls, closeWS() { calls.push('closeWS'); }, completeStop() { calls.push('completeStop'); } };
}

test('V1: a final that closes the utterance while the Viewer is speaking ends it and closes the websocket', () => {
  const utils = loadUtils();
  for (const endReason of ['silence', 'idle_timeout', 'stop', 'restart', '', undefined]) {
    const state = speakingState();
    const actions = recordingActions();
    const ended = utils.endSTTUtteranceFromServer(state, { type: 'final', text: '', end_reason: endReason }, actions);
    assert.equal(ended, true, `end_reason=${endReason}`);
    assert.equal(state.isStopping, true);
    assert.equal(state.vadSpeechActive, false);
    assert.deepEqual(actions.calls, ['closeWS'], 'closing the socket lets onclose reach completeSTTStop; it is not called directly');
  }
});

test('V1: without a websocket the utterance is completed directly', () => {
  const utils = loadUtils();
  const state = speakingState({ ws: null });
  const actions = recordingActions();
  assert.equal(utils.endSTTUtteranceFromServer(state, { type: 'final', end_reason: 'silence' }, actions), true);
  assert.deepEqual(actions.calls, ['completeStop']);
  assert.equal(state.isStopping, true, 'completeSTTStop returns at once unless isStopping is set first');
});

test('V2: a final that ends only because the utterance hit the maximum length lets the Viewer go on', () => {
  const utils = loadUtils();
  const state = speakingState();
  const actions = recordingActions();
  assert.equal(utils.endSTTUtteranceFromServer(state, { type: 'final', text: 'long speech', end_reason: 'max_length' }, actions), false);
  assert.deepEqual(state, speakingState());
  assert.deepEqual(actions.calls, []);
});

test('V3: an error while the Viewer is speaking ends the utterance as well', () => {
  const utils = loadUtils();
  const state = speakingState();
  const actions = recordingActions();
  assert.equal(utils.endSTTUtteranceFromServer(state, { type: 'error', code: 'stt_failed', utterance_id: 'v1' }, actions), true);
  assert.equal(state.isStopping, true);
  assert.equal(state.vadSpeechActive, false);
  assert.deepEqual(actions.calls, ['closeWS']);

  const sessionLevel = speakingState();
  assert.equal(utils.endSTTUtteranceFromServer(sessionLevel, { type: 'error', code: 'stt_failed' }, recordingActions()), true);
});

test('V4: a close the Viewer did not ask for, while it is speaking, completes the stop', () => {
  const utils = loadUtils();
  // CORE closes the connection after it completed the waiting utterances because
  // the stream failed; onclose has already set ws to null.
  const state = speakingState({ ws: null });
  const actions = recordingActions();
  assert.equal(utils.endSTTUtteranceFromServer(state, { type: 'close' }, actions), true);
  assert.equal(state.isStopping, true);
  assert.equal(state.vadSpeechActive, false);
  assert.deepEqual(actions.calls, ['completeStop']);
});

test('V5: while the Viewer is already stopping, or not speaking, nothing is closed twice', () => {
  const utils = loadUtils();
  const events = [
    { type: 'final', end_reason: 'silence' },
    { type: 'final', end_reason: 'stop' },
    { type: 'error', code: 'x' },
    { type: 'close' },
  ];
  for (const state of [speakingState({ isStopping: true, vadSpeechActive: false }), speakingState({ isStopping: true }), speakingState({ vadSpeechActive: false })]) {
    for (const event of events) {
      const actions = recordingActions();
      const before = { ...state };
      assert.equal(utils.endSTTUtteranceFromServer(state, event, actions), false, JSON.stringify([before, event]));
      assert.deepEqual(state, before);
      assert.deepEqual(actions.calls, []);
    }
  }
});

test('events that carry no end of the utterance never end it', () => {
  const utils = loadUtils();
  for (const type of ['partial', 'draft', 'speech_start', 'ready', 'session_info', 'cancelled', 'status', '', undefined]) {
    const state = speakingState();
    const actions = recordingActions();
    assert.equal(utils.endSTTUtteranceFromServer(state, { type }, actions), false, String(type));
    assert.deepEqual(actions.calls, []);
  }
  assert.equal(utils.shouldEndSTTUtteranceOnServerEvent(null, { type: 'final' }), false);
  assert.equal(utils.shouldEndSTTUtteranceOnServerEvent(speakingState(), null), false);
});

test('the decision is made once: a second event for the same utterance finds it already ending', () => {
  const utils = loadUtils();
  const state = speakingState();
  const actions = recordingActions();
  assert.equal(utils.endSTTUtteranceFromServer(state, { type: 'final', end_reason: 'silence' }, actions), true);
  assert.equal(utils.endSTTUtteranceFromServer(state, { type: 'error', code: 'x' }, actions), false);
  assert.equal(utils.endSTTUtteranceFromServer(state, { type: 'close' }, actions), false);
  assert.deepEqual(actions.calls, ['closeWS']);
});
