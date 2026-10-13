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

  root.createSTTPrerollBuffer = createSTTPrerollBuffer;
  root.createSTTUtteranceID = createSTTUtteranceID;
})(typeof globalThis !== 'undefined' ? globalThis : this);
