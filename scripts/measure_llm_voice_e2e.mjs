#!/usr/bin/env node
import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { chromium } from 'playwright';

const DEFAULT_URL = 'http://127.0.0.1:18790/viewer?tab=timeline';
const DEFAULT_WAV = 'tmp/stt_inputs/client_stt_input_20260609_140311.wav';
const DEFAULT_TARGET_BYTES = 820000;
const DEFAULT_CHROMIUM = '/home/nyukimi/.cache/ms-playwright/chromium-1226/chrome-linux64/chrome';

export function parseArgs(argv) {
  const args = {
    url: DEFAULT_URL,
    wav: DEFAULT_WAV,
    targetBytes: DEFAULT_TARGET_BYTES,
    outJson: '',
    headless: true,
    timeoutMs: 45000,
    executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE || '',
  };
  for (let i = 0; i < argv.length; i += 1) {
    const item = argv[i];
    if (item === '--url') args.url = argv[++i];
    else if (item === '--wav') args.wav = argv[++i];
    else if (item === '--target-bytes') args.targetBytes = Number(argv[++i]) || args.targetBytes;
    else if (item === '--out-json') args.outJson = argv[++i];
    else if (item === '--timeout-ms') args.timeoutMs = Number(argv[++i]) || args.timeoutMs;
    else if (item === '--executable-path') args.executablePath = argv[++i];
    else if (item === '--headed') args.headless = false;
    else if (item === '--headless') args.headless = true;
    else if (item === '--help' || item === '-h') args.help = true;
    else throw new Error(`unknown arg: ${item}`);
  }
  if (!args.executablePath && existsSync(DEFAULT_CHROMIUM)) {
    args.executablePath = DEFAULT_CHROMIUM;
  }
  return args;
}

export function usage() {
  return [
    'Usage: node scripts/measure_llm_voice_e2e.mjs [options]',
    '',
    'Options:',
    `  --url <url>                 Viewer URL (default: ${DEFAULT_URL})`,
    `  --wav <path>                Fake mic WAV (default: ${DEFAULT_WAV})`,
    `  --target-bytes <bytes>      Stop after this many VDS bytes (default: ${DEFAULT_TARGET_BYTES})`,
    '  --out-json <path>           Write result JSON',
    '  --timeout-ms <ms>           Per-phase timeout (default: 45000)',
    '  --executable-path <path>    Chromium executable path',
    '  --headed                    Run headed',
    '  --headless                  Run headless',
  ].join('\n');
}

export function deriveTimings(marks) {
  const numberOrNull = (value) => (typeof value === 'number' && Number.isFinite(value) ? value : null);
  const commit = numberOrNull(marks?.commit);
  const firstDelta = numberOrNull(marks?.first_delta);
  const final = numberOrNull(marks?.final);
  return {
    commit_to_first_delta: commit !== null && firstDelta !== null ? firstDelta - commit : null,
    commit_to_final: commit !== null && final !== null ? final - commit : null,
    first_delta_to_final: firstDelta !== null && final !== null ? final - firstDelta : null,
  };
}

export function shortText(text, limit = 400) {
  const value = String(text || '').replace(/\s+/g, ' ').trim();
  return value.length > limit ? `${value.slice(0, limit)}...` : value;
}

function gitShortCommit() {
  try {
    return execFileSync('git', ['rev-parse', '--short', 'HEAD'], { encoding: 'utf8' }).trim();
  } catch (_) {
    return '';
  }
}

function originOf(url) {
  const parsed = new URL(url);
  return `${parsed.protocol}//${parsed.host}`;
}

async function snapshot(page, startedAt, ok, error, extras = {}) {
  return page.evaluate(({ startedAt, ok, error, extras }) => {
    const trace = Array.isArray(window.__llmVoiceTrace) ? window.__llmVoiceTrace : [];
    const first = {};
    const last = {};
    for (const item of trace) {
      const rel = item.ts - startedAt;
      const event = { ...item, rel };
      if (!(item.name in first)) first[item.name] = event;
      last[item.name] = event;
    }
    const mark = (key, source = first) => source[key] ? source[key].rel : null;
    const state = typeof vdsState !== 'undefined' ? vdsState : {};
    const vdsTexts = Array.from(document.querySelectorAll('.msg.vds-local-response .mc'))
      .map((el) => el.dataset.raw || el.textContent || '')
      .filter((text) => String(text || '').trim());
    const chatTexts = Array.from(document.querySelectorAll('.msg .mc'))
      .slice(-4)
      .map((el) => el.dataset.raw || el.textContent || '')
      .filter((text) => String(text || '').trim());
    const finalEvent = first['recv:llm.final'] || last['recv:llm.final'] || null;
    return {
      ok,
      error,
      marks_ms: {
        ws_construct: mark('ws_construct'),
        ws_open: mark('ws_open'),
        session_start: mark('send:session.start'),
        session_ready: mark('recv:session.ready'),
        first_pcm: mark('send:pcm'),
        last_pcm: mark('send:pcm', last),
        commit: mark('send:session.commit'),
        first_delta: mark('recv:llm.delta'),
        final: mark('recv:llm.final'),
        ws_close: mark('ws_close'),
      },
      llm_metrics: finalEvent && finalEvent.metrics ? finalEvent.metrics : {},
      audioBytes: Number(window.__llmVoiceAudioBytes || 0),
      progressFrames: Number(window.__llmVoiceProgressFrames || 0),
      sentAudioFrames: Number(state.sentAudioFrames || 0),
      chunkSamples: Number(state.chunkSamples || 0),
      chat_preview: vdsTexts.length ? vdsTexts[vdsTexts.length - 1] : (chatTexts.length ? chatTexts[chatTexts.length - 1] : ''),
      vds_chat_preview: vdsTexts.length ? vdsTexts[vdsTexts.length - 1] : '',
      chat_recent: chatTexts,
      trace,
      ...extras,
    };
  }, { startedAt, ok, error, extras });
}

async function installPageHooks(page) {
  await page.addInitScript(() => {
    const OriginalWebSocket = window.WebSocket;
    window.__llmVoiceTrace = [];
    window.__llmVoiceAudioBytes = 0;
    window.__llmVoiceProgressFrames = 0;
    window.__llmVoiceMark = (name, detail = {}) => {
      window.__llmVoiceTrace.push({ name, ts: Date.now(), ...detail });
    };
    window.WebSocket = class extends OriginalWebSocket {
      constructor(url, protocols) {
        super(url, protocols);
        this.__isLLMVoice = String(url || '').includes('/voice-chat');
        if (!this.__isLLMVoice) return;
        window.__llmVoiceMark('ws_construct', { url: String(url) });
        this.addEventListener('open', () => window.__llmVoiceMark('ws_open'));
        this.addEventListener('close', () => window.__llmVoiceMark('ws_close'));
        this.addEventListener('error', () => window.__llmVoiceMark('ws_error'));
        this.addEventListener('message', (event) => {
          if (typeof event.data !== 'string') return;
          try {
            const msg = JSON.parse(event.data);
            if (msg.type === 'session.progress') {
              window.__llmVoiceProgressFrames += 1;
              return;
            }
            window.__llmVoiceMark(`recv:${msg.type || 'message'}`, {
              textLen: typeof msg.text === 'string' ? msg.text.length : 0,
              metrics: msg.metrics || null,
            });
          } catch (_) {
            window.__llmVoiceMark('recv:non_json');
          }
        });
      }
      send(data) {
        if (this.__isLLMVoice) {
          if (typeof data === 'string') {
            try {
              const msg = JSON.parse(data);
              window.__llmVoiceMark(`send:${msg.type || 'message'}`);
            } catch (_) {
              window.__llmVoiceMark('send:text');
            }
          } else {
            const bytes = data && typeof data.byteLength === 'number' ? data.byteLength : 0;
            window.__llmVoiceAudioBytes += bytes;
            if (window.__llmVoiceAudioBytes === bytes || window.__llmVoiceAudioBytes % 160000 < bytes) {
              window.__llmVoiceMark('send:pcm', { bytes: window.__llmVoiceAudioBytes });
            }
          }
        }
        return super.send(data);
      }
    };
  });
}

export async function runMeasurement(args) {
  const repo = process.cwd();
  const wavPath = path.resolve(repo, args.wav);
  if (!existsSync(wavPath)) {
    throw new Error(`wav not found: ${wavPath}`);
  }
  const launchArgs = [
    '--use-fake-ui-for-media-stream',
    '--use-fake-device-for-media-stream',
    `--use-file-for-fake-audio-capture=${wavPath}`,
    '--autoplay-policy=no-user-gesture-required',
  ];
  const launchOptions = { headless: args.headless, args: launchArgs };
  if (args.executablePath) launchOptions.executablePath = args.executablePath;

  const browser = await chromium.launch(launchOptions);
  const context = await browser.newContext({ permissions: ['microphone'], viewport: { width: 1366, height: 900 } });
  const page = await context.newPage();
  await context.grantPermissions(['microphone'], { origin: originOf(args.url) });
  await installPageHooks(page);

  const requestFailures = [];
  const nonOKResponses = [];
  const viewerSendRequests = [];
  page.on('requestfailed', (req) => requestFailures.push(`${req.method()} ${req.url()} ${req.failure()?.errorText || ''}`));
  page.on('response', (resp) => {
    if (resp.status() >= 400) nonOKResponses.push(`${resp.status()} ${resp.url()}`);
  });
  page.on('request', (req) => {
    if (req.url().includes('/viewer/send')) {
      viewerSendRequests.push({ method: req.method(), url: req.url(), postData: shortText(req.postData() || '', 300) });
    }
  });

  const startedAt = Date.now();
  let result;
  let beforeStop = null;
  try {
    await page.goto(args.url, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#micBtn', { timeout: 15000 });
    await page.evaluate(() => {
      if (typeof vdsState !== 'undefined') vdsState.voiceInputMode = 'vds_sub';
      if (typeof sttState !== 'undefined') sttState.voiceInputMode = 'vds_sub';
    });
    await page.click('#micBtn');
    await page.waitForFunction(() => window.__llmVoiceTrace.some((e) => e.name === 'recv:session.ready'), null, {
      timeout: args.timeoutMs,
    });
    await page.waitForFunction((targetBytes) => window.__llmVoiceAudioBytes >= targetBytes, args.targetBytes, {
      timeout: args.timeoutMs,
    });
    beforeStop = await page.evaluate(() => ({
      audioBytes: Number(window.__llmVoiceAudioBytes || 0),
      sentAudioBytes: Number((typeof vdsState !== 'undefined' && vdsState.sentAudioBytes) || 0),
      sentAudioFrames: Number((typeof vdsState !== 'undefined' && vdsState.sentAudioFrames) || 0),
      chunkSamples: Number((typeof vdsState !== 'undefined' && vdsState.chunkSamples) || 0),
      vadSpeechActive: Boolean(typeof vdsState !== 'undefined' && vdsState.vadSpeechActive),
      isStopping: Boolean(typeof vdsState !== 'undefined' && vdsState.isStopping),
    }));
    await page.click('#micBtn');
    await page.waitForFunction(() => window.__llmVoiceTrace.some((e) => e.name === 'recv:llm.final' || e.name === 'recv:error'), null, {
      timeout: args.timeoutMs,
    });
    const hasFinal = await page.evaluate(() => window.__llmVoiceTrace.some((e) => e.name === 'recv:llm.final'));
    result = await snapshot(page, startedAt, hasFinal, hasFinal ? '' : 'received error before llm.final', { beforeStop });
  } catch (err) {
    result = await snapshot(page, startedAt, false, String(err && err.message ? err.message : err), { beforeStop });
  } finally {
    await browser.close();
  }

  result.commit = gitShortCommit();
  result.timestamp = new Date().toISOString();
  result.url = args.url;
  result.audio_file = args.wav;
  result.targetBytes = args.targetBytes;
  result.derived_ms = deriveTimings(result.marks_ms);
  result.viewer_send_count = viewerSendRequests.length;
  result.viewer_send_requests = viewerSendRequests;
  result.request_failures = requestFailures;
  result.non_ok_responses = nonOKResponses;
  if (viewerSendRequests.length > 0) {
    result.ok = false;
    result.error = result.error || '/viewer/send was called on LLM voice success path';
  }
  return result;
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  if (args.help) {
    console.log(usage());
    return;
  }
  const result = await runMeasurement(args);
  const json = `${JSON.stringify(result, null, 2)}\n`;
  if (args.outJson) {
    mkdirSync(path.dirname(args.outJson), { recursive: true });
    writeFileSync(args.outJson, json, 'utf8');
  }
  process.stdout.write(json);
  process.exit(result.ok ? 0 : 2);
}

const thisFile = fileURLToPath(import.meta.url);
if (process.argv[1] && path.resolve(process.argv[1]) === thisFile) {
  main().catch((err) => {
    console.error(err && err.stack ? err.stack : err);
    process.exit(1);
  });
}
