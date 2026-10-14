(function (root) {
  'use strict';

  const DEFAULT_SAMPLE_RATE = 16000;
  const DEFAULT_PREROLL_MS = 300;

  // The Viewer VAD fires only once the level is already above the start
  // threshold, so the first syllable is quiet enough to be missed. The preroll
  // buffer keeps the most recent PCM16 samples while no utterance is active;
  // beginSTTUtterance prepends them so the utterance starts before the VAD did.
  function createSTTPrerollBuffer(options) {
    const opts = options || {};
    const sampleRate = Number(opts.sampleRate) > 0 ? Number(opts.sampleRate) : DEFAULT_SAMPLE_RATE;
    const preRollMs = Number.isFinite(opts.preRollMs) ? Math.max(0, opts.preRollMs) : DEFAULT_PREROLL_MS;
    const maxSamples = Math.round((sampleRate * preRollMs) / 1000);
    let chunks = [];
    let total = 0;

    function trim() {
      while (total > maxSamples && chunks.length > 0) {
        const excess = total - maxSamples;
        if (chunks[0].length <= excess) {
          total -= chunks[0].length;
          chunks.shift();
        } else {
          chunks[0] = chunks[0].subarray(excess);
          total -= excess;
        }
      }
    }

    return {
      push(pcm16) {
        if (!pcm16 || pcm16.length === 0 || maxSamples === 0) return;
        chunks.push(Int16Array.from(pcm16)); // the capture loop reuses its buffers
        total += pcm16.length;
        trim();
      },
      take() {
        const out = new Int16Array(total);
        let offset = 0;
        for (const chunk of chunks) {
          out.set(chunk, offset);
          offset += chunk.length;
        }
        chunks = [];
        total = 0;
        return out;
      },
      clear() {
        chunks = [];
        total = 0;
      },
      get length() {
        return total;
      },
    };
  }

  // CORE accepts an utterance_id of at most 64 printable characters and reports
  // it back on speech_start and on the terminal event.
  function createSTTUtteranceID(nowMs, counter) {
    return 'v' + Math.max(0, Math.floor(Number(nowMs) || 0)).toString(36) + '-' + Math.max(0, Math.floor(Number(counter) || 0)).toString(36);
  }

  // RenCrow_STT decides where an utterance ends, so its final can reach the
  // Viewer while the Viewer's own VAD still believes the user is speaking and
  // before the Viewer sent stop. The Viewer then has nothing to wait for: STT
  // closed the utterance and a stop would name one that is gone. This is the one
  // decision for the three ways the server ends it:
  //   - a final (except one that only continues a long utterance: max_length),
  //   - an error (a session-level error ends the connection, an utterance-level
  //     one ends the utterance, and the Viewer has one utterance per connection),
  //   - the connection closing (CORE completes the waiting utterances and closes
  //     it when the stream to STT fails).
  // state: { isStopping, vadSpeechActive }. event: { type: 'final' | 'error' | 'close', end_reason }.
  function shouldEndSTTUtteranceOnServerEvent(state, event) {
    if (!state || !event || state.isStopping || !state.vadSpeechActive) return false;
    switch (event.type) {
      case 'close':
      case 'error':
        return true;
      case 'final':
        return String(event.end_reason || '') !== 'max_length';
      default:
        return false;
    }
  }

  // Ends the utterance when shouldEndSTTUtteranceOnServerEvent says so and
  // returns whether it did. isStopping is set first because completeSTTStop
  // returns at once without it. With a websocket the utterance is completed by
  // closing it (onclose reaches completeSTTStop); without one, directly.
  // actions: { closeWS(), completeStop() }.
  function endSTTUtteranceFromServer(state, event, actions) {
    if (!shouldEndSTTUtteranceOnServerEvent(state, event)) return false;
    state.isStopping = true;
    state.vadSpeechActive = false;
    const act = actions || {};
    if (state.ws) {
      if (typeof act.closeWS === 'function') act.closeWS();
    } else if (typeof act.completeStop === 'function') {
      act.completeStop();
    }
    return true;
  }

  // The Viewer sends stop without an utterance_id, so RenCrow_STT applies it to
  // the utterance it announced that is in progress. When STT already closed that
  // utterance (silence-first), nothing is left to close, and STT answers the stop
  // with a final that has no utterance_id and empty_reason no_active_utterance,
  // behind the final of the closed utterance. That answer ends no utterance: it
  // only tells a client that waits for the stop's final that nothing is coming.
  // A final that names an utterance is never this answer.
  function isSTTStopAnswerWithoutUtterance(event) {
    if (!event || event.type !== 'final') return false;
    if (event.utterance_id) return false;
    return String(event.empty_reason || '') === 'no_active_utterance';
  }

  root.createSTTPrerollBuffer = createSTTPrerollBuffer;
  root.createSTTUtteranceID = createSTTUtteranceID;
  root.shouldEndSTTUtteranceOnServerEvent = shouldEndSTTUtteranceOnServerEvent;
  root.endSTTUtteranceFromServer = endSTTUtteranceFromServer;
  root.isSTTStopAnswerWithoutUtterance = isSTTStopAnswerWithoutUtterance;
})(typeof globalThis !== 'undefined' ? globalThis : this);
