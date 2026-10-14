#!/usr/bin/env node
const { chromium } = require('playwright');
const fs = require('fs');
const os = require('os');
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
    // V6: stand in for the /stt relay and make the server end the utterance first.
    fakeSttEarlyFinal: false,
    // V6b: the stop's answer (a final without utterance_id) follows the final of the utterance.
    fakeSttStopAnswer: false,
    executablePath: '',
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
    else if (a === '--fake-stt-early-final') args.fakeSttEarlyFinal = true;
    else if (a === '--fake-stt-stop-answer') args.fakeSttStopAnswer = true;
    else if (a === '--executable-path') args.executablePath = argv[++i];
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


// ---------------------------------------------------------------------------
// V6 (docs/06 WS /stt, Viewer): the server ends the utterance before the Viewer.
//
// RenCrow_STT decides where an utterance ends. When it closes one before the
// Viewer's own VAD does (noise that fooled the VAD, a short silence_end) the
// final reaches the Viewer before the Viewer sent stop, and STT never sends a
// second one for the stop that follows. The Viewer used to send stop anyway and
// wait 90 seconds for that final. This mode stands in for /stt with
// page.routeWebSocket, so no STT and no relay is needed, and checks that the
// Viewer ends the utterance, closes the socket, sends no stop, and takes the
// next utterance on a new connection without waiting.
//
// The page still has to be served by a CORE (--url); see docs/調査 for how to
// start an isolated one.
// ---------------------------------------------------------------------------

// Writes a 16 kHz mono PCM16 WAV of tone bursts separated by silence. Chromium's
// fake capture device loops it, so the Viewer's VAD fires again and again.
function writeToneWAV(file, bursts, toneMs, silenceMs) {
  const rate = 16000;
  const samples = [];
  for (let b = 0; b < bursts; b += 1) {
    const toneSamples = Math.round(rate * toneMs / 1000);
    for (let i = 0; i < toneSamples; i += 1) {
      samples.push(Math.round(9000 * Math.sin(2 * Math.PI * 220 * i / rate) + 5000 * Math.sin(2 * Math.PI * 660 * i / rate)));
    }
    for (let i = 0; i < Math.round(rate * silenceMs / 1000); i += 1) samples.push(0);
  }
  const data = Buffer.alloc(samples.length * 2);
  samples.forEach((v, i) => data.writeInt16LE(Math.max(-32768, Math.min(32767, v)), i * 2));
  const header = Buffer.alloc(44);
  header.write('RIFF', 0);
  header.writeUInt32LE(36 + data.length, 4);
  header.write('WAVEfmt ', 8);
  header.writeUInt32LE(16, 16);
  header.writeUInt16LE(1, 20);
  header.writeUInt16LE(1, 22);
  header.writeUInt32LE(rate, 24);
  header.writeUInt32LE(rate * 2, 28);
  header.writeUInt16LE(2, 32);
  header.writeUInt16LE(16, 34);
  header.write('data', 36);
  header.writeUInt32LE(data.length, 40);
  fs.writeFileSync(file, Buffer.concat([header, data]));
}

// Opens the Viewer in a browser whose fake microphone plays tone bursts, so the
// Viewer's VAD fires again and again, with the permissions the Viewer needs.
async function openFakeMicViewer(args, tmpPrefix) {
  const wav = path.join(fs.mkdtempSync(path.join(os.tmpdir(), tmpPrefix)), 'tone.wav');
  writeToneWAV(wav, 1, 1500, 3000);
  const launchArgs = [
    '--use-fake-ui-for-media-stream',
    '--autoplay-policy=no-user-gesture-required',
    '--use-fake-device-for-media-stream',
    `--use-file-for-fake-audio-capture=${wav}`,
  ];
  const launchOptions = { headless: args.headless, args: launchArgs };
  if (args.executablePath) launchOptions.executablePath = args.executablePath;
  const browser = await chromium.launch(launchOptions);
  const page = await browser.newPage({ viewport: { width: 1366, height: 900 } });
  await page.context().grantPermissions(['microphone'], { origin: originOf(args.url) });
  return { browser, page };
}

async function runFakeSTTEarlyFinal(args) {
  const { browser, page } = await openFakeMicViewer(args, 'stt-early-final-');

  const connections = [];
  const consoleLines = [];
  page.on('console', msg => consoleLines.push(`${msg.type()}: ${msg.text()}`));
  page.on('pageerror', err => consoleLines.push(`pageerror: ${err.message}`));
  // Persisting the capture and the autotest are not what is under test, and the
  // autotest would send the same audio to a real /stt.
  await page.route('**/viewer/stt/**', route => route.fulfill({ status: 200, contentType: 'application/json', body: '{"ok":true}' }));
  await page.route('**/viewer/send', route => route.fulfill({ status: 200, contentType: 'application/json', body: '{"ok":true}' }));

  await page.routeWebSocket(/\/stt(\?|$)/, ws => {
    const conn = { opened: Date.now(), frames: [], closed: false, utteranceID: '' };
    connections.push(conn);
    ws.send(JSON.stringify({ type: 'session_info', provider: 'rencrow_stt', seq: 1 }));
    ws.send(JSON.stringify({ type: 'ready', sample_rate: 16000, protocol: 'utterance.v1', max_utterance_ms: 30000, seq: 2 }));
    let announced = false;
    ws.onMessage(message => {
      if (typeof message === 'string') {
        let parsed = {};
        try { parsed = JSON.parse(message); } catch (_) { /* not JSON */ }
        conn.frames.push({ kind: 'text', type: parsed.type || '' });
        if (parsed.type === 'start' && parsed.utterance_id) conn.utteranceID = parsed.utterance_id;
        return;
      }
      conn.frames.push({ kind: 'binary' });
      if (announced) return;
      announced = true;
      // STT closes the utterance by itself and says nothing of recognition: an
      // empty final that reaches the Viewer before any stop of its own.
      const id = conn.utteranceID || 'srv-1';
      ws.send(JSON.stringify({ type: 'speech_start', utterance_id: id, offset_ms: 0, seq: 3 }));
      ws.send(JSON.stringify({ type: 'final', utterance_id: id, text: '', end_reason: 'silence', empty_reason: 'too_short', audio_ms: 200, target_called: false, seq: 4 }));
      conn.finalSentAt = Date.now();
    });
    // A route that handles the page's close must complete it, or the page's onclose never fires.
    ws.onClose(() => {
      conn.closed = true;
      conn.closedAt = Date.now();
      ws.close();
    });
  });

  await page.goto(args.url, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('body[data-viewer-tab="timeline"]', { timeout: 10000 });
  await page.click('#micBtn');
  await page.waitForFunction(() => document.querySelector('#micState')?.textContent?.includes('on'), null, { timeout: 10000 });

  // Two utterances, each ended by the server. The wait is far below the Viewer's 90 s final wait.
  let gotTwo = false;
  const deadline = Date.now() + args.finalTimeoutMs;
  while (Date.now() < deadline && !gotTwo) {
    gotTwo = connections.length >= 2 && connections[0].closed;
    if (!gotTwo) await page.waitForTimeout(200);
  }

  const state = await page.evaluate(() => ({
    isStopping: sttState.isStopping,
    vadSpeechActive: sttState.vadSpeechActive,
    session: document.querySelector('#sttSessionState')?.textContent || '',
    caption: document.querySelector('#sttCaption')?.textContent || '',
  }));
  const first = connections[0] || { frames: [], closed: false };
  const stopFrames = connections.reduce((n, c) => n + c.frames.filter(f => f.kind === 'text' && f.type === 'stop').length, 0);
  const failures = [];
  if (!gotTwo) failures.push('the Viewer did not move on to a second utterance after the server ended the first');
  if (!first.closed) failures.push('the Viewer did not close the socket of the utterance the server ended');
  if (first.frames.some(f => f.kind === 'text' && f.type === 'stop')) failures.push('the Viewer sent stop for an utterance that was already over');
  if (/timed out waiting for final/.test(state.session) || /timed out waiting for final/.test(state.caption)) failures.push('the 90 s final wait expired');
  if (consoleLines.some(line => line.startsWith('pageerror:'))) failures.push('the page raised an error');
  const result = {
    ok: failures.length === 0,
    mode: 'fake-stt-early-final',
    url: args.url,
    connections: connections.length,
    first_connection_closed_by_viewer: Boolean(first.closed),
    first_connection_ms_to_close: first.closedAt && first.finalSentAt ? first.closedAt - first.finalSentAt : null,
    stop_frames_sent: stopFrames,
    viewer_state: state,
    failures,
    recent_console: consoleLines.slice(-20),
  };
  console.log(JSON.stringify(result, null, 2));
  await browser.close();
  process.exit(result.ok ? 0 : 2);
}

// ---------------------------------------------------------------------------
// V6b (contract 3.5, 9.3, 9.8): the answer to a stop STT had nothing to close for.
//
// When the Viewer's id-less stop reaches STT and no announced utterance is in
// progress, STT answers it with a final that has no utterance_id (empty_reason
// no_active_utterance). That final ends no utterance. The case that reaches the
// Viewer with its socket still open: an utterance hit the maximum length and its
// final (end_reason max_length) was relayed while the user went on speaking; the
// speech then ended before the rest reached the minimum length STT announces, so
// the stop finds nothing to close. The Viewer must not lose the text of the
// final it already has (caption, recorded capture), and the answer must end its
// wait for a final. This mode stands in for /stt with page.routeWebSocket and
// checks: the text reached chat once, the caption still shows it after the
// socket closed, the socket closed without the 90 s wait, the stop carried no
// utterance_id, and the next utterance is taken on a new connection.
// ---------------------------------------------------------------------------
async function runFakeSTTStopAnswer(args) {
  const { browser, page } = await openFakeMicViewer(args, 'stt-stop-answer-');
  const recognized = '確認テスト（最大長で区切られた前半）';
  const connections = [];
  const sends = [];
  const consoleLines = [];
  page.on('console', msg => consoleLines.push(`${msg.type()}: ${msg.text()}`));
  page.on('pageerror', err => consoleLines.push(`pageerror: ${err.message}`));
  await page.route('**/viewer/stt/**', route => route.fulfill({ status: 200, contentType: 'application/json', body: '{"ok":true}' }));
  await page.route('**/viewer/send', route => {
    sends.push(route.request().postData() || '');
    return route.fulfill({ status: 200, contentType: 'application/json', body: '{"ok":true}' });
  });

  await page.routeWebSocket(/\/stt(\?|$)/, ws => {
    const conn = { frames: [], closed: false, utteranceID: '', stopAt: 0 };
    connections.push(conn);
    ws.send(JSON.stringify({ type: 'session_info', provider: 'rencrow_stt', seq: 1 }));
    ws.send(JSON.stringify({ type: 'ready', sample_rate: 16000, protocol: 'utterance.v1', max_utterance_ms: 30000, seq: 2 }));
    let binaryFrames = 0;
    ws.onMessage(message => {
      if (typeof message === 'string') {
        let parsed = {};
        try { parsed = JSON.parse(message); } catch (_) { /* not JSON */ }
        conn.frames.push({ kind: 'text', type: parsed.type || '', hasID: Boolean(parsed.utterance_id) });
        if (parsed.type === 'start' && parsed.utterance_id) conn.utteranceID = parsed.utterance_id;
        if (parsed.type === 'stop' && !conn.stopAt) {
          conn.stopAt = Date.now();
          // Nothing announced is in progress: STT answers the stop without an utterance_id.
          ws.send(JSON.stringify({ type: 'final', text: '', empty_reason: 'no_active_utterance', end_reason: 'stop', audio_ms: 0, target_called: false, seq: 5 }));
        }
        return;
      }
      conn.frames.push({ kind: 'binary' });
      binaryFrames += 1;
      const id = conn.utteranceID || 'srv-1';
      if (binaryFrames === 1) {
        ws.send(JSON.stringify({ type: 'speech_start', utterance_id: id, offset_ms: 0, seq: 3 }));
      } else if (binaryFrames === 5) {
        // The utterance hit the maximum length: the Viewer goes on.
        ws.send(JSON.stringify({ type: 'final', utterance_id: id, text: recognized, end_reason: 'max_length', audio_ms: 30000, target_called: true, seq: 4 }));
      }
    });
    // A route that handles the page's close must complete it, or the page's onclose never fires.
    ws.onClose(() => {
      conn.closed = true;
      conn.closedAt = Date.now();
      ws.close();
    });
  });

  await page.goto(args.url, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('body[data-viewer-tab="timeline"]', { timeout: 10000 });
  await page.click('#micBtn');
  await page.waitForFunction(() => document.querySelector('#micState')?.textContent?.includes('on'), null, { timeout: 10000 });

  // The first utterance ends with the answer above. The next utterance clears the
  // caption, so it is read right after the first socket closed, before that.
  const caption = () => page.textContent('#sttCaption').then(t => String(t || '').replace(/\s+/g, ' ').trim()).catch(() => '');
  const captionSamples = [];
  const deadline = Date.now() + args.finalTimeoutMs;
  while (Date.now() < deadline && !(connections[0] && connections[0].closed)) await page.waitForTimeout(50);
  const first = connections[0] || { frames: [], closed: false };
  if (first.closedAt) {
    const wait = Math.max(0, first.closedAt + 400 - Date.now());
    if (wait > 0) await page.waitForTimeout(wait);
    captionSamples.push(await caption());
  }
  // The next utterance comes on a new connection.
  let moved = false;
  while (Date.now() < deadline && !moved) {
    moved = connections.length >= 2;
    if (!moved) await page.waitForTimeout(200);
  }
  const state = await page.evaluate(() => ({
    isStopping: sttState.isStopping,
    session: document.querySelector('#sttSessionState')?.textContent || '',
  }));
  const sentMessages = sends.map(body => { try { return JSON.parse(body).message || ''; } catch (_) { return ''; } });
  const stopFrames = first.frames.filter(f => f.kind === 'text' && f.type === 'stop');
  const failures = [];
  if (stopFrames.length === 0) failures.push('the Viewer sent no stop, so the race under test did not happen');
  if (stopFrames.some(f => f.hasID)) failures.push('the Viewer named an utterance in its stop');
  if (!moved) failures.push('the Viewer did not move on to a second utterance');
  if (!first.closed) failures.push('the Viewer did not close the socket after the answer to its stop');
  if (first.closedAt && first.stopAt && first.closedAt - first.stopAt > 5000) failures.push('the socket was closed late after the answer to the stop');
  if (sentMessages.filter(m => m === recognized).length !== 1) failures.push(`the recognized text must reach chat exactly once, got ${JSON.stringify(sentMessages)}`);
  if (!captionSamples.some(c => c.includes(recognized))) failures.push(`the caption lost the recognized text: ${JSON.stringify(captionSamples)}`);
  if (/timed out waiting for final/.test(state.session) || captionSamples.some(c => /timed out waiting for final/.test(c))) failures.push('the 90 s final wait expired');
  if (consoleLines.some(line => line.startsWith('pageerror:'))) failures.push('the page raised an error');
  const result = {
    ok: failures.length === 0,
    mode: 'fake-stt-stop-answer',
    url: args.url,
    connections: connections.length,
    first_connection_closed_by_viewer: Boolean(first.closed),
    first_connection_ms_stop_to_close: first.closedAt && first.stopAt ? first.closedAt - first.stopAt : null,
    stop_frames_sent: stopFrames.length,
    chat_messages: sentMessages,
    caption_after_close: captionSamples,
    viewer_state: state,
    failures,
    recent_console: consoleLines.slice(-12),
  };
  console.log(JSON.stringify(result, null, 2));
  await browser.close();
  process.exit(result.ok ? 0 : 2);
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  if (args.fakeSttEarlyFinal) return runFakeSTTEarlyFinal(args);
  if (args.fakeSttStopAnswer) return runFakeSTTStopAnswer(args);
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
