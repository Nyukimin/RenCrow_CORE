#!/usr/bin/env node
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const DEFAULT_URL = 'http://127.0.0.1:18790/viewer?tab=timeline';
const DEFAULT_WAV = 'tmp/stt_inputs/client_stt_input_20260609_140311.wav';
const DEFAULT_WS_URL = 'ws://192.168.1.207:8081/v1/chat/audio/sessions';
const DEFAULT_OUT_DIR = 'tmp/llm_voice_latency';
const DEFAULT_ROUNDS = 3;
const DEFAULT_TARGET_BYTES = 820000;
const DEFAULT_MAX_DELTA_EVENTS = 1;
const DEFAULT_TARGET_FINAL_MS = 3000;
const DEFAULT_MAX_VIEWER_GAP_MS = 500;

export function parseArgs(argv) {
  const args = {
    url: DEFAULT_URL,
    wav: DEFAULT_WAV,
    wsUrl: DEFAULT_WS_URL,
    outDir: DEFAULT_OUT_DIR,
    rounds: DEFAULT_ROUNDS,
    targetBytes: DEFAULT_TARGET_BYTES,
    maxDeltaEvents: DEFAULT_MAX_DELTA_EVENTS,
    targetFinalMs: DEFAULT_TARGET_FINAL_MS,
    maxViewerGapMs: DEFAULT_MAX_VIEWER_GAP_MS,
    timeoutMs: 45000,
    skipDirectGate: false,
  };
  for (let i = 0; i < argv.length; i += 1) {
    const item = argv[i];
    if (item === '--url') args.url = argv[++i];
    else if (item === '--wav') args.wav = argv[++i];
    else if (item === '--ws-url') args.wsUrl = argv[++i];
    else if (item === '--out-dir') args.outDir = argv[++i];
    else if (item === '--rounds') args.rounds = Number(argv[++i]) || args.rounds;
    else if (item === '--target-bytes') args.targetBytes = Number(argv[++i]) || args.targetBytes;
    else if (item === '--max-delta-events') args.maxDeltaEvents = Number(argv[++i]);
    else if (item === '--target-final-ms') args.targetFinalMs = Number(argv[++i]) || args.targetFinalMs;
    else if (item === '--max-viewer-gap-ms') args.maxViewerGapMs = Number(argv[++i]) || args.maxViewerGapMs;
    else if (item === '--timeout-ms') args.timeoutMs = Number(argv[++i]) || args.timeoutMs;
    else if (item === '--skip-direct-gate') args.skipDirectGate = true;
    else if (item === '--help' || item === '-h') args.help = true;
    else throw new Error(`unknown arg: ${item}`);
  }
  return args;
}

export function usage() {
  return [
    'Usage: node scripts/verify_llm_voice_latency.mjs [options]',
    '',
    'Runs RenCrow_LLM direct delta gate, then Viewer fake-mic LLM voice E2E rounds.',
    '',
    'Options:',
    `  --url <url>                 Viewer URL (default: ${DEFAULT_URL})`,
    `  --ws-url <url>              RenCrow_LLM audio session WS (default: ${DEFAULT_WS_URL})`,
    `  --wav <path>                Fake mic WAV (default: ${DEFAULT_WAV})`,
    `  --out-dir <path>            Output directory (default: ${DEFAULT_OUT_DIR})`,
    `  --rounds <n>                Viewer E2E rounds (default: ${DEFAULT_ROUNDS})`,
    `  --target-bytes <bytes>      Viewer audio bytes target (default: ${DEFAULT_TARGET_BYTES})`,
    `  --max-delta-events <n>      Direct gate max llm.delta events (default: ${DEFAULT_MAX_DELTA_EVENTS})`,
    `  --target-final-ms <ms>      Median commit->final target (default: ${DEFAULT_TARGET_FINAL_MS})`,
    `  --max-viewer-gap-ms <ms>    Median Viewer/RenCrow_LLM gap target (default: ${DEFAULT_MAX_VIEWER_GAP_MS})`,
    '  --timeout-ms <ms>           Viewer E2E timeout per phase (default: 45000)',
    '  --skip-direct-gate          Run Viewer rounds without direct delta gate',
  ].join('\n');
}

export function median(values) {
  const nums = values.filter((v) => Number.isFinite(v)).sort((a, b) => a - b);
  if (nums.length === 0) return null;
  const mid = Math.floor(nums.length / 2);
  return nums.length % 2 ? nums[mid] : (nums[mid - 1] + nums[mid]) / 2;
}

export function summarizeViewerResults(results, thresholds = {}) {
  const targetFinalMs = thresholds.targetFinalMs ?? DEFAULT_TARGET_FINAL_MS;
  const maxViewerGapMs = thresholds.maxViewerGapMs ?? DEFAULT_MAX_VIEWER_GAP_MS;
  const commitToFinal = results.map((r) => Number(r?.derived_ms?.commit_to_final));
  const llmFinal = results.map((r) => Number(r?.llm_metrics?.commit_to_final_ms));
  const viewerGap = results.map((r, idx) => {
    const viewer = commitToFinal[idx];
    const llm = llmFinal[idx];
    return Number.isFinite(viewer) && Number.isFinite(llm) ? viewer - llm : NaN;
  });
  const viewerSendCount = results.reduce((sum, r) => sum + Number(r?.viewer_send_count || 0), 0);
  const okCount = results.filter((r) => r?.ok === true).length;
  const summary = {
    rounds: results.length,
    ok_count: okCount,
    viewer_send_count: viewerSendCount,
    commit_to_final_ms: {
      values: commitToFinal.filter((v) => Number.isFinite(v)),
      median: median(commitToFinal),
      worst: commitToFinal.filter((v) => Number.isFinite(v)).reduce((max, v) => Math.max(max, v), null),
    },
    llm_commit_to_final_ms: {
      values: llmFinal.filter((v) => Number.isFinite(v)),
      median: median(llmFinal),
    },
    viewer_gap_ms: {
      values: viewerGap.filter((v) => Number.isFinite(v)),
      median: median(viewerGap),
      worst: viewerGap.filter((v) => Number.isFinite(v)).reduce((max, v) => Math.max(max, v), null),
    },
  };
  const failures = [];
  if (okCount !== results.length) failures.push(`ok_count=${okCount}/${results.length}`);
  if (viewerSendCount !== 0) failures.push(`viewer_send_count=${viewerSendCount}`);
  if (summary.commit_to_final_ms.median === null) failures.push('missing commit_to_final_ms');
  else if (summary.commit_to_final_ms.median >= targetFinalMs) failures.push(`median_commit_to_final_ms=${summary.commit_to_final_ms.median} >= ${targetFinalMs}`);
  if (summary.viewer_gap_ms.median === null) failures.push('missing viewer_gap_ms');
  else if (summary.viewer_gap_ms.median >= maxViewerGapMs) failures.push(`median_viewer_gap_ms=${summary.viewer_gap_ms.median} >= ${maxViewerGapMs}`);
  summary.passed = failures.length === 0;
  summary.failures = failures;
  return summary;
}

function runCommand(command, args, options = {}) {
  const proc = spawnSync(command, args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], ...options });
  return {
    code: proc.status ?? 1,
    stdout: proc.stdout || '',
    stderr: proc.stderr || '',
  };
}

function timestamp() {
  return new Date().toISOString().replace(/[-:]/g, '').replace(/\.\d+Z$/, 'Z');
}

function readJSON(file) {
  return JSON.parse(readFileSync(file, 'utf8'));
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  if (args.help) {
    console.log(usage());
    return;
  }
  if (!existsSync(args.wav)) {
    throw new Error(`wav not found: ${args.wav}`);
  }
  mkdirSync(args.outDir, { recursive: true });
  const stamp = timestamp();
  const report = {
    timestamp: new Date().toISOString(),
    args,
    direct_gate: null,
    viewer_results: [],
    summary: null,
  };

  if (!args.skipDirectGate) {
    const directJSON = path.join(args.outDir, `verify_direct_gate_${stamp}.json`);
    const directMD = path.join(args.outDir, `verify_direct_gate_${stamp}.md`);
    const direct = runCommand('python3', [
      'scripts/vds_e2e_probe.py',
      '--ws-url', args.wsUrl,
      '--wav', args.wav,
      '--rounds', '1',
      '--wait', '90',
      '--chunk-ms', '200',
      '--require-llm-final',
      '--max-delta-events', String(args.maxDeltaEvents),
      '--write-md', directMD,
    ]);
    writeFileSync(directJSON, direct.stdout, 'utf8');
    report.direct_gate = {
      code: direct.code,
      json: directJSON,
      markdown: directMD,
      stderr: direct.stderr.trim(),
    };
    if (direct.code !== 0) {
      const summaryPath = path.join(args.outDir, `verify_llm_voice_latency_${stamp}.json`);
      report.summary = { passed: false, failures: [`direct_delta_gate_exit=${direct.code}`] };
      writeFileSync(summaryPath, `${JSON.stringify(report, null, 2)}\n`, 'utf8');
      console.log(JSON.stringify(report, null, 2));
      process.exit(4);
    }
  }

  for (let i = 1; i <= args.rounds; i += 1) {
    const outJson = path.join(args.outDir, `verify_viewer_round_${i}_${stamp}.json`);
    const run = runCommand('node', [
      'scripts/measure_llm_voice_e2e.mjs',
      '--url', args.url,
      '--wav', args.wav,
      '--target-bytes', String(args.targetBytes),
      '--timeout-ms', String(args.timeoutMs),
      '--out-json', outJson,
    ]);
    const result = existsSync(outJson) ? readJSON(outJson) : { ok: false, error: run.stderr || run.stdout };
    report.viewer_results.push({ code: run.code, json: outJson, result });
  }

  report.summary = summarizeViewerResults(
    report.viewer_results.map((item) => item.result),
    { targetFinalMs: args.targetFinalMs, maxViewerGapMs: args.maxViewerGapMs },
  );
  const summaryPath = path.join(args.outDir, `verify_llm_voice_latency_${stamp}.json`);
  writeFileSync(summaryPath, `${JSON.stringify(report, null, 2)}\n`, 'utf8');
  console.log(JSON.stringify(report, null, 2));
  process.exit(report.summary.passed ? 0 : 5);
}

const thisFile = fileURLToPath(import.meta.url);
if (process.argv[1] && path.resolve(process.argv[1]) === thisFile) {
  main().catch((err) => {
    console.error(err && err.stack ? err.stack : err);
    process.exit(1);
  });
}
