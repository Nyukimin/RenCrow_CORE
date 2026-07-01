#!/usr/bin/env node
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, existsSync } from 'node:fs';
import { createServer } from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repo = path.resolve(__dirname, '../..');
const script = path.join(__dirname, 'run_browser_actor.mjs');
const fixture = path.join(__dirname, 'fixtures/basic_form.html');
const externalFixture = path.join(__dirname, 'fixtures/external_effect.html');

function runActor(input) {
  const proc = spawnSync('node', [script, 'run', '--json'], {
    cwd: repo,
    input: JSON.stringify(input),
    encoding: 'utf8',
  });
  let body;
  try {
    body = JSON.parse(proc.stdout);
  } catch (err) {
    throw new Error(`failed to parse stdout: ${err.message}\nstdout=${proc.stdout}\nstderr=${proc.stderr}`);
  }
  return { proc, body };
}

function runActorAsync(input) {
  return new Promise((resolve, reject) => {
    const child = spawn('node', [script, 'run', '--json'], {
      cwd: repo,
      stdio: ['pipe', 'pipe', 'pipe'],
    });
    let stdout = '';
    let stderr = '';
    child.stdout.setEncoding('utf8');
    child.stderr.setEncoding('utf8');
    child.stdout.on('data', chunk => { stdout += chunk; });
    child.stderr.on('data', chunk => { stderr += chunk; });
    child.on('error', reject);
    child.on('close', status => {
      let body;
      try {
        body = JSON.parse(stdout);
      } catch (err) {
        reject(new Error(`failed to parse stdout: ${err.message}\nstdout=${stdout}\nstderr=${stderr}`));
        return;
      }
      resolve({ proc: { status, stdout, stderr }, body });
    });
    child.stdin.end(JSON.stringify(input));
  });
}

function baseRequest(name) {
  const dir = mkdtempSync(path.join(os.tmpdir(), `browser_actor_${name}_`));
  return {
    schema_version: '1.0',
    run_id: `test_${name}_${process.pid}`,
    start_url: pathToFileURL(fixture).href,
    allowed_origins: ['file://'],
    artifact_dir: dir,
    headless: true,
    timeout_ms: 30000,
    max_actions: 20,
    save_trace: true,
    save_screenshot: true,
    mask_secrets: true,
    actions: [
      { type: 'open' },
      { type: 'wait_for_selector', selector: '#name' },
      { type: 'fill', selector: '#name', value: 'RenCrow' },
      { type: 'fill', selector: '#password', value: 'secret-password-token' },
      { type: 'click', selector: '#draft' },
      { type: 'screenshot', name: 'filled' },
      { type: 'snapshot' },
      { type: 'extract_text', selector: 'body' },
    ],
  };
}

function startCookieServer() {
  return new Promise(resolve => {
    const server = createServer((_req, res) => {
      res.setHeader('Set-Cookie', 'actor_session=remembered; Path=/; SameSite=Lax');
      res.setHeader('Content-Type', 'text/html; charset=utf-8');
      res.end('<!doctype html><html><body><h1 id="ready">Cookie Fixture</h1></body></html>');
    });
    server.listen(0, '127.0.0.1', () => {
      const address = server.address();
      resolve({ server, origin: `http://127.0.0.1:${address.port}` });
    });
  });
}

{
  const proc = spawnSync('node', [script, 'doctor', '--json'], { cwd: repo, encoding: 'utf8' });
  const body = JSON.parse(proc.stdout);
  assert.equal(body.schema_version, '1.0');
  assert.equal(body.ok, true, proc.stderr + proc.stdout);
}

{
  const req = baseRequest('basic');
  const { proc, body } = runActor(req);
  assert.equal(proc.status, 0, proc.stderr + proc.stdout);
  assert.equal(body.status, 'completed');
  assert.equal(body.risk_level, 'draft_input');
  assert.ok(existsSync(path.join(req.artifact_dir, 'filled.png')));
  assert.ok(existsSync(path.join(req.artifact_dir, 'snapshot.json')));
  assert.ok(existsSync(path.join(req.artifact_dir, 'extracted_text.json')));
  assert.ok(existsSync(path.join(req.artifact_dir, 'requests.jsonl')));
  assert.ok(existsSync(path.join(req.artifact_dir, 'responses.jsonl')));
  const actions = readFileSync(path.join(req.artifact_dir, 'actions.jsonl'), 'utf8');
  assert.doesNotMatch(actions, /secret-password-token/);
  const snapshot = readFileSync(path.join(req.artifact_dir, 'snapshot.json'), 'utf8');
  const extracted = readFileSync(path.join(req.artifact_dir, 'extracted_text.json'), 'utf8');
  const requests = readFileSync(path.join(req.artifact_dir, 'requests.jsonl'), 'utf8');
  const responses = readFileSync(path.join(req.artifact_dir, 'responses.jsonl'), 'utf8');
  assert.doesNotMatch(snapshot, /secret-password-token/);
  assert.doesNotMatch(extracted, /secret-password-token/);
  assert.doesNotMatch(requests, /secret-password-token/);
  assert.doesNotMatch(responses, /secret-password-token/);
}

{
  const req = baseRequest('profile');
  req.profile_id = `profile_${process.pid}`;
  req.storage_state_path = path.join(os.tmpdir(), 'browser_profiles', req.profile_id, 'storage_state.json');
  const { proc, body } = runActor(req);
  assert.equal(proc.status, 0, proc.stderr + proc.stdout);
  assert.equal(body.status, 'completed');
  assert.ok(existsSync(req.storage_state_path));
  const state = readFileSync(req.storage_state_path, 'utf8');
  assert.doesNotMatch(state, /secret-password-token/);
}

{
  const { server, origin } = await startCookieServer();
  try {
    const req = baseRequest('profile_cookie');
    req.profile_id = `profile_cookie_${process.pid}`;
    req.start_url = `${origin}/`;
    req.allowed_origins = [origin];
    req.storage_state_path = path.join(os.tmpdir(), 'browser_profiles', req.profile_id, 'storage_state.json');
    req.actions = [
      { type: 'open' },
      { type: 'wait_for_selector', selector: '#ready' },
    ];
    const { proc, body } = await runActorAsync(req);
    assert.equal(proc.status, 0, proc.stderr + proc.stdout);
    assert.equal(body.status, 'completed');
    assert.ok(existsSync(req.storage_state_path));
    const state = readFileSync(req.storage_state_path, 'utf8');
    assert.match(state, /actor_session/);
    assert.match(state, /remembered/);
  } finally {
    await new Promise(resolve => server.close(resolve));
  }
}

{
  const req = baseRequest('blocked');
  req.start_url = pathToFileURL(externalFixture).href;
  req.actions = [{ type: 'open' }, { type: 'click', selector: '#send' }];
  const { proc, body } = runActor(req);
  assert.equal(proc.status, 1);
  assert.equal(body.status, 'failed');
  assert.equal(body.error.code, 'PERMISSION_DENIED');
  assert.equal(body.error.details.matched_rule, 'submit_keyword');
}

{
  const req = baseRequest('origin');
  req.allowed_origins = ['https://example.com'];
  const { proc, body } = runActor(req);
  assert.equal(proc.status, 1);
  assert.equal(body.error.code, 'PERMISSION_DENIED');
}

console.log('browser_actor tests passed');
