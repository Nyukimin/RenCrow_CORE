#!/usr/bin/env node
import test from 'node:test';
import assert from 'node:assert/strict';

import { median, parseArgs, summarizeViewerResults, usage } from './verify_llm_voice_latency.mjs';

test('parseArgs accepts verifier options', () => {
  const args = parseArgs([
    '--url', 'http://viewer.local/viewer',
    '--ws-url', 'ws://llm/audio',
    '--wav', 'tmp/a.wav',
    '--out-dir', 'tmp/out',
    '--rounds', '5',
    '--target-bytes', '123',
    '--max-delta-events', '2',
    '--target-final-ms', '2500',
    '--max-viewer-gap-ms', '300',
    '--timeout-ms', '999',
    '--skip-direct-gate',
  ]);
  assert.equal(args.url, 'http://viewer.local/viewer');
  assert.equal(args.wsUrl, 'ws://llm/audio');
  assert.equal(args.wav, 'tmp/a.wav');
  assert.equal(args.outDir, 'tmp/out');
  assert.equal(args.rounds, 5);
  assert.equal(args.targetBytes, 123);
  assert.equal(args.maxDeltaEvents, 2);
  assert.equal(args.targetFinalMs, 2500);
  assert.equal(args.maxViewerGapMs, 300);
  assert.equal(args.timeoutMs, 999);
  assert.equal(args.skipDirectGate, true);
});

test('median handles odd even and empty values', () => {
  assert.equal(median([3, 1, 2]), 2);
  assert.equal(median([10, 2, 4, 8]), 6);
  assert.equal(median([NaN]), null);
});

test('summarizeViewerResults passes when final and gap targets pass', () => {
  const summary = summarizeViewerResults([
    {
      ok: true,
      viewer_send_count: 0,
      derived_ms: { commit_to_final: 2400 },
      llm_metrics: { commit_to_final_ms: 2100 },
    },
    {
      ok: true,
      viewer_send_count: 0,
      derived_ms: { commit_to_final: 2600 },
      llm_metrics: { commit_to_final_ms: 2250 },
    },
    {
      ok: true,
      viewer_send_count: 0,
      derived_ms: { commit_to_final: 2200 },
      llm_metrics: { commit_to_final_ms: 2000 },
    },
  ], { targetFinalMs: 3000, maxViewerGapMs: 500 });
  assert.equal(summary.passed, true);
  assert.equal(summary.commit_to_final_ms.median, 2400);
  assert.equal(summary.viewer_gap_ms.median, 300);
});

test('summarizeViewerResults fails on viewer send or slow median', () => {
  const summary = summarizeViewerResults([
    {
      ok: true,
      viewer_send_count: 1,
      derived_ms: { commit_to_final: 5000 },
      llm_metrics: { commit_to_final_ms: 1200 },
    },
  ], { targetFinalMs: 3000, maxViewerGapMs: 500 });
  assert.equal(summary.passed, false);
  assert.match(summary.failures.join(' '), /viewer_send_count=1/);
  assert.match(summary.failures.join(' '), /median_commit_to_final_ms=5000/);
  assert.match(summary.failures.join(' '), /median_viewer_gap_ms=3800/);
});

test('usage documents direct gate and threshold options', () => {
  const text = usage();
  assert.match(text, /--max-delta-events/);
  assert.match(text, /--target-final-ms/);
  assert.match(text, /--max-viewer-gap-ms/);
});
