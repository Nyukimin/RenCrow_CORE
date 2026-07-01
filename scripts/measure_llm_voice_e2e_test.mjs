#!/usr/bin/env node
import test from 'node:test';
import assert from 'node:assert/strict';

import { deriveTimings, parseArgs, shortText, usage } from './measure_llm_voice_e2e.mjs';

test('parseArgs accepts required measurement options', () => {
  const args = parseArgs([
    '--url', 'http://127.0.0.1:18790/viewer?tab=timeline',
    '--wav', 'tmp/input.wav',
    '--target-bytes', '12345',
    '--out-json', 'tmp/out.json',
    '--timeout-ms', '999',
    '--headed',
  ]);
  assert.equal(args.url, 'http://127.0.0.1:18790/viewer?tab=timeline');
  assert.equal(args.wav, 'tmp/input.wav');
  assert.equal(args.targetBytes, 12345);
  assert.equal(args.outJson, 'tmp/out.json');
  assert.equal(args.timeoutMs, 999);
  assert.equal(args.headless, false);
});

test('deriveTimings returns commit boundary durations', () => {
  assert.deepEqual(
    deriveTimings({
      commit: 1000,
      first_delta: 1450,
      final: 1800,
    }),
    {
      commit_to_first_delta: 450,
      commit_to_final: 800,
      first_delta_to_final: 350,
    },
  );
});

test('deriveTimings tolerates missing marks', () => {
  assert.deepEqual(deriveTimings({ commit: 1000 }), {
    commit_to_first_delta: null,
    commit_to_final: null,
    first_delta_to_final: null,
  });
});

test('shortText compacts whitespace and truncates', () => {
  assert.equal(shortText(' a\n b\t c ', 10), 'a b c');
  assert.equal(shortText('1234567890abcdef', 10), '1234567890...');
});

test('usage documents required CLI flags', () => {
  const text = usage();
  assert.match(text, /--out-json/);
  assert.match(text, /--target-bytes/);
  assert.match(text, /--url/);
  assert.match(text, /--wav/);
});
