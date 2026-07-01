#!/usr/bin/env node
import { chromium } from 'playwright';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import process from 'node:process';
import { fileURLToPath } from 'node:url';

const SCHEMA_VERSION = '1.0';
const MASK = '[MASKED]';
const ERROR = {
  VALIDATION_FAILED: 'VALIDATION_FAILED',
  PERMISSION_DENIED: 'PERMISSION_DENIED',
  TIMEOUT: 'TIMEOUT',
  NOT_FOUND: 'NOT_FOUND',
  INTERNAL_ERROR: 'INTERNAL_ERROR',
};
const SUBMIT_KEYWORDS = [
  'submit', 'send', 'post', 'publish', 'buy', 'purchase', 'checkout', 'delete', 'remove',
  'reserve', 'confirm', 'apply', 'upload', '支払', '購入', '投稿', '送信', '削除', '予約',
  '確定', '申し込', '申込',
];
const SUPPORTED_ACTIONS = new Set([
  'open',
  'wait_for_selector',
  'click',
  'fill',
  'press',
  'screenshot',
  'snapshot',
  'extract_text',
  'close',
]);
const WRITE_ROOTS = [
  'workspace/browser_runs',
  'tmp/browser_runs',
  'output/playwright',
];
const PROFILE_ROOTS = [
  'workspace/browser_profiles',
  'tmp/browser_profiles',
];
const CHROMIUM_ARGS = [
  '--no-sandbox',
  '--disable-setuid-sandbox',
  '--disable-gpu-sandbox',
  '--disable-dev-shm-usage',
];

function nowISO() {
  return new Date().toISOString();
}

function stableRunID() {
  const stamp = new Date().toISOString().replace(/[-:.TZ]/g, '').slice(0, 14);
  return `browser_run_${stamp}_${process.pid}`;
}

function errorObject(code, message, details = undefined) {
  const out = { code, message };
  if (details && Object.keys(details).length > 0) out.details = details;
  return out;
}

function baseResponse(req = {}, status = 'failed') {
  return {
    schema_version: SCHEMA_VERSION,
    run_id: req.run_id || stableRunID(),
    status,
    warnings: [],
    error: null,
  };
}

function printJSON(value) {
  process.stdout.write(`${JSON.stringify(value, null, 2)}\n`);
}

function log(message) {
  process.stderr.write(`[browser_actor] ${message}\n`);
}

async function readStdin() {
  let data = '';
  for await (const chunk of process.stdin) data += chunk;
  return data;
}

function asBool(value, fallback) {
  return typeof value === 'boolean' ? value : fallback;
}

function asInt(value, fallback) {
  const n = Number(value);
  return Number.isFinite(n) ? Math.trunc(n) : fallback;
}

function normalizeRequest(input) {
  const req = { ...input };
  req.schema_version = req.schema_version || SCHEMA_VERSION;
  req.run_id = String(req.run_id || stableRunID()).trim();
  req.goal = String(req.goal || '').trim();
  req.start_url = String(req.start_url || '').trim();
  req.profile_id = String(req.profile_id || '').trim();
  req.storage_state_path = String(req.storage_state_path || '').trim();
  if (req.profile_id && !req.storage_state_path) {
    req.storage_state_path = path.join('workspace/browser_profiles', req.profile_id, 'storage_state.json');
  }
  req.headless = asBool(req.headless, true);
  req.viewport = req.viewport && typeof req.viewport === 'object' ? req.viewport : { width: 1366, height: 900 };
  req.viewport.width = asInt(req.viewport.width, 1366);
  req.viewport.height = asInt(req.viewport.height, 900);
  req.allowed_origins = Array.isArray(req.allowed_origins) ? req.allowed_origins.map(v => String(v).trim()).filter(Boolean) : [];
  req.timeout_ms = asInt(req.timeout_ms, 30000);
  req.max_actions = asInt(req.max_actions, 30);
  req.save_trace = asBool(req.save_trace, true);
  req.save_screenshot = asBool(req.save_screenshot, true);
  req.mask_secrets = asBool(req.mask_secrets, true);
  req.actions = Array.isArray(req.actions) ? req.actions.map(a => ({ ...a, type: String(a?.type || '').trim() })) : [];
  if (!String(req.artifact_dir || '').trim()) {
    req.artifact_dir = path.join('workspace/browser_runs', req.run_id);
  } else {
    req.artifact_dir = String(req.artifact_dir).trim();
  }
  return req;
}

function validateRequest(req) {
  const issues = [];
  if (req.schema_version !== SCHEMA_VERSION) issues.push('unsupported schema_version');
  if (!/^[a-zA-Z0-9_.-]+$/.test(req.run_id)) issues.push('run_id must match ^[a-zA-Z0-9_.-]+$');
  if (req.profile_id && !/^[a-zA-Z0-9_.-]+$/.test(req.profile_id)) issues.push('profile_id must match ^[a-zA-Z0-9_.-]+$');
  if (!req.start_url) issues.push('start_url is required');
  if (!Array.isArray(req.actions) || req.actions.length === 0) issues.push('actions is required');
  if (req.actions.length > req.max_actions) issues.push('actions exceeds max_actions');
  if (req.timeout_ms <= 0) issues.push('timeout_ms must be positive');
  if (req.max_actions <= 0 || req.max_actions > 100) issues.push('max_actions must be 1..100');
  if (req.viewport.width < 100 || req.viewport.height < 100) issues.push('viewport must be at least 100x100');
  for (const [i, action] of req.actions.entries()) {
    if (!SUPPORTED_ACTIONS.has(action.type)) issues.push(`unsupported action at ${i}: ${action.type}`);
    if (String(action.selector || '').length > 1000) issues.push(`selector too long at ${i}`);
    if (String(action.value || '').length > 10000) issues.push(`value too long at ${i}`);
    if (action.type === 'wait_for_selector' && !String(action.selector || '').trim()) issues.push(`selector is required at ${i}`);
    if (action.type === 'click' && !String(action.selector || '').trim()) issues.push(`selector is required at ${i}`);
    if (action.type === 'fill' && !String(action.selector || '').trim()) issues.push(`selector is required at ${i}`);
    if (action.type === 'fill' && !Object.prototype.hasOwnProperty.call(action, 'value')) issues.push(`value is required at ${i}`);
    if (action.type === 'press' && !String(action.key || '').trim()) issues.push(`key is required at ${i}`);
    if (action.type === 'screenshot' && !/^[a-zA-Z0-9_.-]+$/.test(String(action.name || 'final'))) {
      issues.push(`screenshot name must be path-safe at ${i}`);
    }
    if (action.type === 'extract_text' && !String(action.selector || '').trim()) issues.push(`selector is required at ${i}`);
  }
  if (!artifactPathAllowed(req.artifact_dir)) issues.push('artifact_dir is outside allowed roots');
  if (req.storage_state_path && !profilePathAllowed(req.storage_state_path)) issues.push('storage_state_path is outside allowed profile roots');
  return issues;
}

function artifactPathAllowed(value) {
  if (!value || value.includes('\0')) return false;
  const abs = path.resolve(value);
  const cwd = process.cwd();
  for (const root of WRITE_ROOTS) {
    const rootAbs = path.resolve(cwd, root);
    if (abs === rootAbs || abs.startsWith(`${rootAbs}${path.sep}`)) return true;
  }
  const tmpRoot = path.resolve(os.tmpdir());
  return abs === tmpRoot || abs.startsWith(`${tmpRoot}${path.sep}`);
}

function profilePathAllowed(value) {
  if (!value || value.includes('\0')) return false;
  const abs = path.resolve(value);
  const cwd = process.cwd();
  for (const root of PROFILE_ROOTS) {
    const rootAbs = path.resolve(cwd, root);
    if (abs === rootAbs || abs.startsWith(`${rootAbs}${path.sep}`)) return true;
  }
  const tmpProfileRoot = path.resolve(os.tmpdir(), 'browser_profiles');
  return abs === tmpProfileRoot || abs.startsWith(`${tmpProfileRoot}${path.sep}`);
}

async function fileExists(value) {
  try {
    await fs.access(value);
    return true;
  } catch {
    return false;
  }
}

function originOf(rawURL) {
  const parsed = new URL(rawURL);
  if (parsed.protocol === 'file:') return 'file://';
  return parsed.origin;
}

function ensureAllowedOrigin(rawURL, allowedOrigins) {
  const origin = originOf(rawURL);
  const allow = allowedOrigins.length > 0 ? allowedOrigins : [origin];
  if (!allow.includes(origin)) {
    return { ok: false, origin, allowed: allow };
  }
  return { ok: true, origin, allowed: allow };
}

function actionProbeText(action) {
  return [
    action.type,
    action.selector || '',
    action.name || '',
    action.key || '',
    action.text || '',
  ].join(' ').toLowerCase();
}

function classifyRisk(req) {
  for (const [i, action] of req.actions.entries()) {
    if (!SUPPORTED_ACTIONS.has(action.type)) {
      return { risk: 'blocked', reason: 'unsupported_action', action_index: i, action_type: action.type };
    }
    const probe = actionProbeText(action);
    if (SUBMIT_KEYWORDS.some(keyword => probe.includes(keyword.toLowerCase()))) {
      return { risk: 'external_effect', reason: 'submit_keyword', action_index: i, action_type: action.type };
    }
    if (action.type === 'press' && String(action.key || '').toLowerCase() === 'enter') {
      return { risk: 'external_effect', reason: 'enter_key_submit_guard', action_index: i, action_type: action.type };
    }
  }
  if (req.actions.some(a => a.type === 'fill')) return { risk: 'draft_input' };
  if (req.actions.some(a => a.type === 'click')) return { risk: 'navigation' };
  return { risk: 'read_only' };
}

function maskText(value) {
  let s = String(value ?? '');
  s = s.replace(/(authorization\s*[:=]\s*)(bearer\s+)?[a-z0-9._~+/=-]+/gi, `$1${MASK}`);
  s = s.replace(/(cookie\s*[:=]\s*)[^;\n\r]+/gi, `$1${MASK}`);
  s = s.replace(/(set-cookie\s*[:=]\s*)[^;\n\r]+/gi, `$1${MASK}`);
  s = s.replace(/((password|token|secret|apikey|api_key|session|csrf)\s*[:=]\s*)[^\s"'&]+/gi, `$1${MASK}`);
  return s;
}

function maskHeaders(headers) {
  const out = {};
  for (const [key, value] of Object.entries(headers || {})) {
    const lk = key.toLowerCase();
    if (lk === 'cookie' || lk === 'authorization' || lk === 'set-cookie') out[lk] = MASK;
    else out[lk] = maskText(value);
  }
  return out;
}

function maskAction(action) {
  const out = { ...action };
  if (String(out.selector || '').toLowerCase().includes('password')) out.value = MASK;
  if (/password|token|secret|apikey|api_key|session|csrf/i.test(String(out.name || ''))) out.value = MASK;
  return out;
}

async function ensureDir(dir) {
  await fs.mkdir(dir, { recursive: true });
}

async function appendJSONL(file, value) {
  await fs.appendFile(file, `${JSON.stringify(value)}\n`, 'utf8');
}

async function writeJSON(file, value) {
  await fs.writeFile(file, JSON.stringify(value, null, 2), 'utf8');
}

function actionResult(id, action, status, extra = {}) {
  return {
    action_id: id,
    type: action.type,
    status,
    ...extra,
  };
}

async function runDoctor() {
  const checks = [];
  const add = (name, ok, status, detail = '') => checks.push({ name, ok, status, detail });
  add('node', true, 'ok', process.version);
  try {
    const browser = await chromium.launch({ headless: true, args: CHROMIUM_ARGS });
    const page = await browser.newPage();
    await page.setContent('<html><title>doctor</title><body>ok</body></html>');
    await page.screenshot({ path: path.join(os.tmpdir(), `browser_actor_doctor_${process.pid}.png`) });
    await browser.close();
    add('playwright_chromium', true, 'ok', 'headless launch succeeded');
  } catch (err) {
    add('playwright_chromium', false, 'fail', err.message);
  }
  const ok = checks.every(c => c.ok);
  return { schema_version: SCHEMA_VERSION, ok, checks };
}

async function executeRun(req) {
  const response = {
    ...baseResponse(req, 'failed'),
    risk_level: '',
    started_at: nowISO(),
    completed_at: '',
    start_url: req.start_url,
    final_url: '',
    title: '',
    artifact_dir: req.artifact_dir,
    artifacts: {},
    actions: [],
  };

  const validationIssues = validateRequest(req);
  if (validationIssues.length > 0) {
    response.error = errorObject(ERROR.VALIDATION_FAILED, validationIssues.join('; '));
    response.completed_at = nowISO();
    return response;
  }
  const originCheck = ensureAllowedOrigin(req.start_url, req.allowed_origins);
  if (!originCheck.ok) {
    response.error = errorObject(ERROR.PERMISSION_DENIED, 'start_url origin is not allowed', originCheck);
    response.completed_at = nowISO();
    return response;
  }
  const risk = classifyRisk(req);
  response.risk_level = risk.risk;
  if (risk.risk === 'external_effect' || risk.risk === 'blocked') {
    response.error = errorObject(ERROR.PERMISSION_DENIED, 'external effect action is blocked without human approval', {
      action_index: risk.action_index,
      action_type: risk.action_type,
      matched_rule: risk.reason,
    });
    response.completed_at = nowISO();
    return response;
  }

  const artifactDir = req.artifact_dir;
  await ensureDir(artifactDir);
  const files = {
    run: path.join(artifactDir, 'run.json'),
    actions: path.join(artifactDir, 'actions.jsonl'),
    console: path.join(artifactDir, 'console.jsonl'),
    network: path.join(artifactDir, 'network.jsonl'),
    requests: path.join(artifactDir, 'requests.jsonl'),
    responses: path.join(artifactDir, 'responses.jsonl'),
    snapshot: path.join(artifactDir, 'snapshot.json'),
    extracted_text: path.join(artifactDir, 'extracted_text.json'),
    screenshot: path.join(artifactDir, 'final.png'),
    trace: path.join(artifactDir, 'trace.zip'),
  };
  if (req.storage_state_path) files.storage_state = req.storage_state_path;
  response.artifacts = files;

  let browser;
  let context;
  try {
    browser = await chromium.launch({ headless: req.headless, args: CHROMIUM_ARGS });
    const contextOpts = {
      viewport: req.viewport,
    };
    if (req.storage_state_path && await fileExists(req.storage_state_path)) contextOpts.storageState = req.storage_state_path;
    context = await browser.newContext(contextOpts);
    const page = await context.newPage();

    page.on('console', msg => {
      appendJSONL(files.console, {
        ts: nowISO(),
        type: msg.type(),
        text: req.mask_secrets ? maskText(msg.text()) : msg.text(),
      }).catch(() => {});
    });
    page.on('request', request => {
      appendJSONL(files.network, {
        ts: nowISO(),
        request_id: request.url(),
        method: request.method(),
        url: request.url(),
        origin: safeOrigin(request.url()),
        path: safePath(request.url()),
        resource_type: request.resourceType(),
        request_headers: req.mask_secrets ? maskHeaders(request.headers()) : request.headers(),
      }).catch(() => {});
      appendJSONL(files.requests, {
        request_id: request.url(),
        method: request.method(),
        url: request.url(),
        headers: req.mask_secrets ? maskHeaders(request.headers()) : request.headers(),
      }).catch(() => {});
    });
    page.on('response', async resp => {
      appendJSONL(files.network, {
        ts: nowISO(),
        request_id: resp.url(),
        method: resp.request().method(),
        url: resp.url(),
        origin: safeOrigin(resp.url()),
        path: safePath(resp.url()),
        status: resp.status(),
        resource_type: resp.request().resourceType(),
        response_headers: req.mask_secrets ? maskHeaders(resp.headers()) : resp.headers(),
      }).catch(() => {});
      appendJSONL(files.responses, {
        request_id: resp.url(),
        url: resp.url(),
        status: resp.status(),
      }).catch(() => {});
    });
    page.on('request', request => {
      if (!['GET', 'HEAD'].includes(request.method().toUpperCase())) {
        response.warnings.push(`blocked non-read method observed: ${request.method()} ${request.url()}`);
      }
    });

    if (req.save_trace) {
      await context.tracing.start({ screenshots: true, snapshots: true });
    }

    for (let i = 0; i < req.actions.length; i += 1) {
      const action = req.actions[i];
      const actionID = `act_${i + 1}`;
      const started = nowISO();
      try {
        await executeAction(page, action, req, files);
        const currentOriginCheck = ensureAllowedOrigin(page.url(), req.allowed_origins.length > 0 ? req.allowed_origins : [originCheck.origin]);
        if (!currentOriginCheck.ok) {
          throw Object.assign(new Error('navigation origin is not allowed'), { code: ERROR.PERMISSION_DENIED, details: currentOriginCheck });
        }
        const completed = nowISO();
        const result = actionResult(actionID, maskAction(action), 'completed', { started_at: started, completed_at: completed });
        response.actions.push(result);
        await appendJSONL(files.actions, result);
      } catch (err) {
        const completed = nowISO();
        const result = actionResult(actionID, maskAction(action), 'failed', {
          started_at: started,
          completed_at: completed,
          error: req.mask_secrets ? maskText(err.message) : err.message,
        });
        response.actions.push(result);
        await appendJSONL(files.actions, result);
        response.error = errorObject(err.code || ERROR.INTERNAL_ERROR, req.mask_secrets ? maskText(err.message) : err.message, err.details);
        break;
      }
    }

    if (req.save_screenshot) {
      await page.screenshot({ path: files.screenshot, fullPage: true });
    }
    if (req.save_trace) {
      await context.tracing.stop({ path: files.trace });
    }
    response.final_url = page.url();
    response.title = await page.title().catch(() => '');
    if (!response.error && response.warnings.some(w => w.startsWith('blocked non-read method observed'))) {
      response.error = errorObject(ERROR.PERMISSION_DENIED, 'non-read HTTP method observed during browser run');
    }
    response.status = response.error ? 'failed' : 'completed';
  } catch (err) {
    response.error = errorObject(err.code || ERROR.INTERNAL_ERROR, req.mask_secrets ? maskText(err.message) : err.message, err.details);
    response.status = 'failed';
  } finally {
    if (context && req.storage_state_path) {
      try {
        await ensureDir(path.dirname(req.storage_state_path));
        await context.storageState({ path: req.storage_state_path });
      } catch (err) {
        response.warnings.push(`failed to save storage state: ${req.mask_secrets ? maskText(err.message) : err.message}`);
      }
    }
    if (context) {
      await context.close().catch(() => {});
    }
    if (browser) {
      await browser.close().catch(() => {});
    }
    response.completed_at = nowISO();
    if (artifactPathAllowed(req.artifact_dir)) {
      await ensureDir(req.artifact_dir).catch(() => {});
      await writeJSON(path.join(req.artifact_dir, 'run.json'), response).catch(() => {});
    }
  }
  return response;
}

function safeOrigin(rawURL) {
  try { return originOf(rawURL); } catch { return ''; }
}

function safePath(rawURL) {
  try {
    const u = new URL(rawURL);
    return u.pathname;
  } catch {
    return '';
  }
}

async function executeAction(page, action, req, files) {
  const timeout = asInt(action.timeout_ms, Math.min(req.timeout_ms, 30000));
  switch (action.type) {
    case 'open':
      await page.goto(req.start_url, { waitUntil: 'domcontentloaded', timeout });
      break;
    case 'wait_for_selector':
      await page.waitForSelector(action.selector, { state: 'visible', timeout });
      break;
    case 'click':
      await page.click(action.selector, { timeout });
      break;
    case 'fill':
      await page.fill(action.selector, String(action.value ?? ''), { timeout });
      break;
    case 'press':
      await page.keyboard.press(action.key);
      break;
    case 'screenshot': {
      const name = action.name || 'final';
      const file = path.join(req.artifact_dir, `${name}.png`);
      await page.screenshot({ path: file, fullPage: true });
      break;
    }
    case 'snapshot': {
      const text = await page.locator('body').innerText({ timeout: 3000 }).catch(() => '');
      const dom = await page.evaluate(() => ({
        activeElement: document.activeElement ? {
          tagName: document.activeElement.tagName,
          id: document.activeElement.id || '',
          name: document.activeElement.getAttribute('name') || '',
        } : null,
        links: Array.from(document.querySelectorAll('a')).slice(0, 50).map(a => ({
          text: a.textContent || '',
          href: a.href || '',
          id: a.id || '',
        })),
        buttons: Array.from(document.querySelectorAll('button')).slice(0, 50).map(button => ({
          text: button.textContent || '',
          id: button.id || '',
          type: button.getAttribute('type') || '',
        })),
        inputs: Array.from(document.querySelectorAll('input, textarea, select')).slice(0, 50).map(input => ({
          tagName: input.tagName,
          id: input.id || '',
          name: input.getAttribute('name') || '',
          type: input.getAttribute('type') || '',
        })),
      })).catch(() => null);
      await writeJSON(files.snapshot, {
        ts: nowISO(),
        url: page.url(),
        title: await page.title().catch(() => ''),
        text: req.mask_secrets ? maskText(text) : text,
        dom,
      });
      break;
    }
    case 'extract_text': {
      const text = await page.locator(action.selector).innerText({ timeout });
      await writeJSON(files.extracted_text, {
        ts: nowISO(),
        selector: action.selector,
        text: req.mask_secrets ? maskText(text) : text,
      });
      break;
    }
    case 'close':
      await page.close();
      break;
    default:
      throw Object.assign(new Error(`unsupported action: ${action.type}`), { code: ERROR.VALIDATION_FAILED });
  }
}

async function main() {
  const [cmd, ...args] = process.argv.slice(2);
  const jsonOut = args.includes('--json');
  if (cmd === 'doctor') {
    const result = await runDoctor();
    printJSON(result);
    process.exit(result.ok ? 0 : 1);
  }
  if (cmd !== 'run') {
    printJSON({ schema_version: SCHEMA_VERSION, status: 'failed', error: errorObject(ERROR.VALIDATION_FAILED, 'usage: run_browser_actor.mjs [run|doctor] --json') });
    process.exit(2);
  }
  if (!jsonOut) {
    printJSON({ schema_version: SCHEMA_VERSION, status: 'failed', error: errorObject(ERROR.VALIDATION_FAILED, '--json is required') });
    process.exit(2);
  }
  let raw = '';
  let req = {};
  try {
    raw = await readStdin();
    req = normalizeRequest(JSON.parse(raw));
  } catch (err) {
    const resp = baseResponse({}, 'failed');
    resp.error = errorObject(ERROR.VALIDATION_FAILED, `invalid JSON input: ${err.message}`);
    printJSON(resp);
    process.exit(2);
  }
  log(`run ${req.run_id} start`);
  const response = await executeRun(req);
  printJSON(response);
  process.exit(response.status === 'completed' ? 0 : 1);
}

main().catch(err => {
  const resp = baseResponse({}, 'failed');
  resp.error = errorObject(ERROR.INTERNAL_ERROR, err && err.stack ? err.stack : String(err));
  printJSON(resp);
  process.exit(1);
});
