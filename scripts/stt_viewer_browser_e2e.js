#!/usr/bin/env node
const { chromium } = require('playwright');
const path = require('path');

function parseArgs(argv) {
  const args = {
    url: 'http://127.0.0.1:18790/viewer?tab=timeline',
    wav: 'tmp/client_stt_input_latest.wav',
    realMic: false,
    requireFinal: true,
    requireSend: true,
    speakMs: 0,
    manualStop: false,
    manualTimeoutMs: 120000,
    partialTimeoutMs: 30000,
    finalTimeoutMs: 70000,
    headless: true,
    requireTTSInterrupt: false,
  };
  for (let i = 0; i < argv.length; i += 1) {
    const a = argv[i];
    if (a === '--url') args.url = argv[++i];
    else if (a === '--wav') args.wav = argv[++i];
    else if (a === '--real-mic') args.realMic = true;
    else if (a === '--no-require-final') args.requireFinal = false;
    else if (a === '--no-require-send') args.requireSend = false;
    else if (a === '--speak-ms') args.speakMs = Number(argv[++i]) || 0;
    else if (a === '--manual-stop') args.manualStop = true;
    else if (a === '--manual-timeout-ms') args.manualTimeoutMs = Number(argv[++i]) || args.manualTimeoutMs;
    else if (a === '--partial-timeout-ms') args.partialTimeoutMs = Number(argv[++i]) || args.partialTimeoutMs;
    else if (a === '--final-timeout-ms') args.finalTimeoutMs = Number(argv[++i]) || args.finalTimeoutMs;
    else if (a === '--headed') args.headless = false;
    else if (a === '--headless') args.headless = true;
    else if (a === '--require-tts-interrupt') args.requireTTSInterrupt = true;
    else throw new Error(`unknown arg: ${a}`);
  }
  if (args.realMic && args.speakMs <= 0) args.manualStop = true;
  return args;
}

function originOf(url) {
  const u = new URL(url);
  return `${u.protocol}//${u.host}`;
}

async function waitOrNull(page, fn, timeout) {
  try {
    await page.waitForFunction(fn, null, { timeout });
    return true;
  } catch (_) {
    return false;
  }
}

function framePayloadBytes(payload) {
  if (typeof payload === 'string') return Buffer.byteLength(payload);
  if (Buffer.isBuffer(payload)) return payload.length;
  if (payload instanceof Uint8Array) return payload.byteLength;
  return 0;
}

function framePayloadText(payload) {
  if (typeof payload === 'string') return payload;
  if (Buffer.isBuffer(payload)) return payload.toString('utf8');
  if (payload instanceof Uint8Array) return Buffer.from(payload).toString('utf8');
  return '';
}

function parseJSONFrames(frames) {
  return frames.flatMap(text => {
    try {
      return [JSON.parse(text)];
    } catch (_) {
      return [];
    }
  });
}

const SILENT_WAV_DATA_URL = 'data:audio/wav;base64,UklGRigAAABXQVZFZm10IBAAAAABAAEAQB8AAEAfAAABAAgAZGF0YQQAAAAA';

async function main() {
  const args = parseArgs(process.argv.slice(2));
  const repo = process.cwd();
  const launchArgs = ['--use-fake-ui-for-media-stream', '--autoplay-policy=no-user-gesture-required'];
  if (!args.realMic) {
    launchArgs.push('--use-fake-device-for-media-stream');
    launchArgs.push(`--use-file-for-fake-audio-capture=${path.resolve(repo, args.wav)}`);
  }

  const browser = await chromium.launch({ headless: args.headless, args: launchArgs });
  const page = await browser.newPage({ viewport: { width: 1366, height: 900 } });
  await page.context().grantPermissions(['microphone'], { origin: originOf(args.url) });

  const wsSent = [];
  const wsRecv = [];
  const consoleLines = [];
  page.on('websocket', ws => {
    ws.on('framesent', frame => wsSent.push(frame.payload));
    ws.on('framereceived', frame => wsRecv.push(frame.payload));
  });
  page.on('console', msg => consoleLines.push(`${msg.type()}: ${msg.text()}`));
  page.on('pageerror', err => consoleLines.push(`pageerror: ${err.message}`));

  let sendBody = '';
  const failedRequests = [];
  const nonOKResponses = [];
  page.on('requestfailed', req => failedRequests.push(`${req.method()} ${req.url()} ${req.failure()?.errorText || ''}`));
  page.on('response', resp => {
    if (resp.status() >= 400) nonOKResponses.push(`${resp.status()} ${resp.url()}`);
  });
  await page.route('**/viewer/send', route => {
    sendBody = route.request().postData() || '';
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ok: true }) });
  });

  await page.goto(args.url, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('body[data-viewer-tab="timeline"]', { timeout: 10000 });
  let ttsInterruptBefore = null;
  if (args.requireTTSInterrupt) {
    ttsInterruptBefore = await page.evaluate(async (silentWavURL) => {
      viewerControl.activeAudioViewerId = viewerControl.clientId;
      const audio = new Audio();
      audio.src = silentWavURL;
      ttsPlayback.audio = audio;
      ttsPlayback.playing = true;
      ttsPlayback.currentCharacterId = 'mio';
      ttsPlayback.currentText = 'stt tts interrupt speech';
      ttsPlayback.currentDisplayText = 'STT TTS interrupt display';
      ttsPlayback.currentSessionId = 'stt-tts-e2e-session';
      ttsPlayback.currentChunkIndex = 0;
      ttsPlayback.currentUtteranceId = 'stt-tts-e2e-utterance-0';
      ttsPlayback.currentResponseId = 'stt-tts-e2e-response';
      setCentralTTSSpeechText(
        'mio',
        'STT TTS interrupt display',
        'stt-tts-e2e-session',
        0,
        'stt-tts-e2e-utterance-0',
        'stt-tts-e2e-response',
      );
      await new Promise(resolve => setTimeout(resolve, 120));
      return {
        playing: Boolean(ttsPlayback.playing),
        current_session_id: String(ttsPlayback.currentSessionId || ''),
        current_response_id: String(ttsPlayback.currentResponseId || ''),
        audio_src: String((ttsPlayback.audio && ttsPlayback.audio.src) || ''),
        chat_children: document.querySelector('#chat') ? document.querySelector('#chat').children.length : -1,
      };
    }, SILENT_WAV_DATA_URL);
  }
  await page.click('#micBtn');
  await page.waitForFunction(() => document.querySelector('#micState')?.textContent?.includes('on'), null, { timeout: 10000 });
  const ttsInterrupted = args.requireTTSInterrupt
    ? await waitOrNull(page, () => {
        return Boolean(
          typeof ttsPlayback !== 'undefined'
          && ttsPlayback.playing === false
          && String(ttsPlayback.currentSessionId || '') === ''
          && String((ttsPlayback.audio && ttsPlayback.audio.src) || '') === ''
        );
      }, 10000)
    : false;

  let sawPartial = false;
  if (args.manualStop) {
    console.error('[stt-viewer-browser-e2e] Microphone is ON. Speak clearly, then click the mic button in the browser to stop.');
    await page.waitForFunction(() => document.querySelector('#micState')?.textContent?.includes('off'), null, { timeout: args.manualTimeoutMs });
  } else if (args.speakMs > 0) {
    await page.waitForTimeout(args.speakMs);
    sawPartial = (await page.textContent('#sttCaption').catch(() => '')).includes('暫定字幕:');
    await page.click('#micBtn');
  } else {
    sawPartial = await waitOrNull(page, () => (document.querySelector('#sttCaption')?.textContent || '').includes('暫定字幕:'), args.partialTimeoutMs);
    await page.click('#micBtn');
  }

  const partialCaption = await page.textContent('#sttCaption').catch(() => '');
  if (!sawPartial) sawPartial = partialCaption.includes('暫定字幕:');
  const sawFinal = await waitOrNull(page, () => (document.querySelector('#sttCaption')?.textContent || '').includes('確定字幕:'), args.finalTimeoutMs);
  const finalCaption = await page.textContent('#sttCaption').catch(() => '');
  if (!sawPartial) sawPartial = finalCaption.includes('暫定字幕:');
  const deadline = Date.now() + (args.requireSend ? 15000 : 1000);
  while (!sendBody && Date.now() < deadline) await page.waitForTimeout(200);
  let ttsInterruptAfter = null;
  if (args.requireTTSInterrupt) {
    ttsInterruptAfter = await page.evaluate(async (silentWavURL) => {
      const chatEl = document.querySelector('#chat');
      const before = chatEl ? chatEl.children.length : -1;
      chatAudioSync.handleEvent({
        type: 'tts.audio_chunk',
        content: JSON.stringify({
          audio_url: silentWavURL,
          session_id: 'stt-tts-e2e-session',
          response_id: 'stt-tts-e2e-response',
          utterance_id: 'stt-tts-e2e-utterance-1',
          chunk_index: 1,
          character_id: 'mio',
          text: 'stale interrupted speech',
          display_text: 'STT TTS stale interrupted display',
        }),
      });
      await new Promise(resolve => setTimeout(resolve, 120));
      const after = chatEl ? chatEl.children.length : -1;
      return {
        playing: Boolean(ttsPlayback.playing),
        current_session_id: String(ttsPlayback.currentSessionId || ''),
        audio_src: String((ttsPlayback.audio && ttsPlayback.audio.src) || ''),
        queue_length: Array.isArray(ttsPlayback.queue) ? ttsPlayback.queue.length : -1,
        chat_children_before_stale: before,
        chat_children_after_stale: after,
        stale_chunk_dropped: before === after && !ttsPlayback.playing && (Array.isArray(ttsPlayback.queue) ? ttsPlayback.queue.length === 0 : true),
      };
    }, SILENT_WAV_DATA_URL);
  }

  const sentText = wsSent.filter(p => typeof p === 'string');
  const sentBinary = wsSent.filter(p => typeof p !== 'string');
  const sentBinaryBytes = sentBinary.reduce((sum, p) => sum + framePayloadBytes(p), 0);
  const recvFrames = wsRecv.map(framePayloadText).filter(Boolean);
  const recvEvents = parseJSONFrames(recvFrames);
  const recvEventTypes = recvEvents.map(ev => String(ev.type || '')).filter(Boolean);
  const recvText = recvFrames.join('\n');
  const result = {
    ok: true,
    url: args.url,
    real_mic: args.realMic,
    manual_stop: args.manualStop,
    saw_partial: sawPartial,
    saw_final: sawFinal,
    partial_caption: partialCaption,
    final_caption: finalCaption,
    sent_start: sentText.some(p => p.includes('"type":"start"')),
    sent_stop: sentText.some(p => p.includes('"type":"stop"')),
    sent_binary: sentBinary.length > 0,
    sent_binary_frames: sentBinary.length,
    sent_binary_bytes: sentBinaryBytes,
    sent_binary_seconds_16k_mono_pcm16: Math.round((sentBinaryBytes / 32000) * 100) / 100,
    recv_partial: recvEventTypes.includes('partial') || recvEventTypes.includes('draft') || sawPartial,
    recv_final: recvEventTypes.includes('final'),
    recv_error: recvEventTypes.includes('error'),
    recv_event_types: recvEventTypes,
    recv_text_frames: recvFrames.length,
    recv_recent: recvFrames.slice(-8).map(s => s.slice(0, 240)),
    chat_send_observed: Boolean(sendBody),
    tts_interrupt_before: ttsInterruptBefore,
    tts_interrupted_on_stt_start: Boolean(ttsInterrupted),
    tts_interrupt_after: ttsInterruptAfter,
    tts_stale_chunk_dropped: Boolean(ttsInterruptAfter && ttsInterruptAfter.stale_chunk_dropped),
    send_message: sendBody ? JSON.parse(sendBody).message || '' : '',
    input_value: await page.inputValue('#inp').catch(() => ''),
    session: await page.textContent('#sttSessionState').catch(() => ''),
    mic: await page.textContent('#micState').catch(() => ''),
    conn: await page.textContent('#sttConnState').catch(() => ''),
    stt_console: consoleLines.filter(line => line.includes('[STT]') || line.includes('Viewer send')).slice(-20),
    failed_requests: failedRequests.slice(-20),
    non_ok_responses: nonOKResponses.slice(-20),
    recent_console: consoleLines.slice(-20),
  };

  const failures = [];
  if (!result.sent_start) failures.push('missing start control');
  if (!result.sent_binary) failures.push('missing binary PCM chunks');
  if (!result.sent_stop) failures.push('missing stop control');
  if (args.requireFinal && !result.recv_final) failures.push('missing final event');
  if (args.requireFinal && !result.saw_final) failures.push('missing final caption');
  if (args.requireSend && !result.chat_send_observed) failures.push('missing /viewer/send');
  if (args.requireSend && !String(result.send_message || '').trim()) failures.push('empty send message');
  if (args.requireTTSInterrupt && !(result.tts_interrupt_before && result.tts_interrupt_before.playing)) failures.push('tts did not start before STT');
  if (args.requireTTSInterrupt && !result.tts_interrupted_on_stt_start) failures.push('tts was not interrupted on STT start');
  if (args.requireTTSInterrupt && !result.tts_stale_chunk_dropped) failures.push('stale tts chunk was not dropped after STT start');
  if (failures.length > 0) {
    result.ok = false;
    result.failures = failures;
  }

  console.log(JSON.stringify(result, null, 2));
  await browser.close();
  process.exit(result.ok ? 0 : 2);
}

main().catch(err => {
  console.error(err && err.stack ? err.stack : err);
  process.exit(1);
});
