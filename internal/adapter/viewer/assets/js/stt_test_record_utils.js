(function (root) {
  'use strict';

  const DEFAULT_SAMPLE_RATE = 16000;
  const DEFAULT_FRAME_SAMPLES = 160;
  const DEFAULT_MIN_LEVEL = 8;
  const DEFAULT_MIN_VOICE_MS = 300;

  function pcmFrameLevel(pcm16, start, frameSamples) {
    const end = Math.min(start + frameSamples, pcm16.length);
    if (end <= start) return 0;
    let sumSquares = 0;
    for (let i = start; i < end; i++) {
      const sample = Number(pcm16[i]) || 0;
      sumSquares += sample * sample;
    }
    const rms = Math.sqrt(sumSquares / (end - start));
    return Math.max(0, Math.min(100, Math.round((rms / 2400) * 100)));
  }

  function trimSTTPcmSilence(pcm16, options) {
    const opts = options || {};
    const sampleRate = Number(opts.sampleRate) || DEFAULT_SAMPLE_RATE;
    const frameSamples = Math.max(1, Number(opts.frameSamples) || DEFAULT_FRAME_SAMPLES);
    const minLevel = Number.isFinite(opts.minLevel) ? opts.minLevel : DEFAULT_MIN_LEVEL;
    const minVoiceMs = Number.isFinite(opts.minVoiceMs) ? opts.minVoiceMs : DEFAULT_MIN_VOICE_MS;
    const minVoiceFrames = Math.max(1, Math.ceil((minVoiceMs * sampleRate) / (1000 * frameSamples)));

    if (!pcm16 || pcm16.length === 0) {
      return new Int16Array(0);
    }

    let firstVoiceFrame = -1;
    for (let frame = 0, offset = 0; offset < pcm16.length; frame++, offset += frameSamples) {
      if (pcmFrameLevel(pcm16, offset, frameSamples) >= minLevel) {
        firstVoiceFrame = frame;
        break;
      }
    }
    if (firstVoiceFrame < 0) {
      return new Int16Array(0);
    }

    let lastVoiceFrame = firstVoiceFrame;
    if (opts.edgeOnly) {
      for (let frame = firstVoiceFrame, offset = firstVoiceFrame * frameSamples; offset < pcm16.length; frame++, offset += frameSamples) {
        if (pcmFrameLevel(pcm16, offset, frameSamples) >= minLevel) {
          lastVoiceFrame = frame;
        }
      }
    } else {
      let trailingSilentFrames = 0;
      for (let frame = firstVoiceFrame, offset = firstVoiceFrame * frameSamples; offset < pcm16.length; frame++, offset += frameSamples) {
        if (pcmFrameLevel(pcm16, offset, frameSamples) >= minLevel) {
          lastVoiceFrame = frame;
          trailingSilentFrames = 0;
          continue;
        }
        trailingSilentFrames += 1;
        if (trailingSilentFrames >= minVoiceFrames) {
          break;
        }
        lastVoiceFrame = frame;
      }
    }

    const start = firstVoiceFrame * frameSamples;
    const end = Math.min(pcm16.length, (lastVoiceFrame + 1) * frameSamples);
    if (end <= start) {
      return new Int16Array(0);
    }
    return pcm16.slice(start, end);
  }

  root.trimSTTPcmSilence = trimSTTPcmSilence;
  root.pcmFrameLevel = pcmFrameLevel;
})(typeof globalThis !== 'undefined' ? globalThis : this);
