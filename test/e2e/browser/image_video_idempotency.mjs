#!/usr/bin/env node

import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {chromium} from 'playwright';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../..');
const imageScript = fs.readFileSync(
  path.join(repoRoot, 'internal/adapter/viewer/assets/js/tabs/image.js'),
  'utf8',
);
const origin = 'https://image-video-idempotency.test';
const pendingKey = 'rencrow.image.video.pendingRequest';
const jobKey = 'rencrow.image.video.lastJobId';

function fixtureHTML() {
  const script = imageScript.replaceAll('</script', '<\\/script');
  return `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Image video idempotency fixture</title></head>
<body>
  <textarea id="imagePrompt"></textarea>
  <textarea id="imageNegativePrompt"></textarea>
  <input id="imageSeed" value="1">
  <select id="imageOutputKind"><option value="image">Image</option><option value="video">Video</option></select>
  <div id="imageOnlyOptions"></div>
  <div id="videoOptions"></div>
  <select id="videoMode"><option value="t2v">T2V</option><option value="i2v">I2V</option></select>
  <div id="videoInputImageRow"><input id="videoInputImage" type="file" accept="image/png,image/jpeg"></div>
  <input id="videoSeed" value="42">
  <button id="imageGenerateBtn" type="button">Generate image</button>
  <div id="imageStatus"></div>
  <div id="imageResultEmpty"></div>
  <figure id="imageResultFigure"><img id="imageResult"></figure>
  <div id="imageResultPrompt"></div>
  <div id="imageResultMeta"></div>
  <div id="imageResultKicker"></div>
  <main class="image-main"></main>
  <div id="imageError" hidden></div>
  <section id="videoResultPanel" hidden>
    <div id="videoJobStatus"></div>
    <progress id="videoProgress"></progress>
    <video id="videoResult" hidden></video>
    <a id="videoDownload" hidden>Download</a>
  </section>
  <script>${script}</script>
</body>
</html>`;
}

async function installRoutes(page, actions, calls) {
  const serverJobs = new Map();
  await page.route(`${origin}/**`, async (route) => {
    const request = route.request();
    const requestURL = new URL(request.url());
    if (requestURL.pathname === '/' && request.method() === 'GET') {
      await route.fulfill({status: 200, contentType: 'text/html', body: fixtureHTML()});
      return;
    }
    if (requestURL.pathname === '/favicon.ico') {
      await route.fulfill({status: 204, body: ''});
      return;
    }
    if (requestURL.pathname === '/viewer/image/video/generations' && request.method() === 'POST') {
      const pendingRaw = await page.evaluate((key) => localStorage.getItem(key), pendingKey);
      const requestIdempotencyKey = request.headers()['idempotency-key'] || '';
      let jobID = serverJobs.get(requestIdempotencyKey);
      if (!jobID) {
        jobID = `vid_retry_${serverJobs.size + 1}`;
        serverJobs.set(requestIdempotencyKey, jobID);
      }
      calls.push({
        idempotencyKey: requestIdempotencyKey,
        jobID,
        pending: pendingRaw ? JSON.parse(pendingRaw) : null,
        contentType: request.headers()['content-type'] || '',
        body: request.postDataBuffer(),
      });
      const action = actions.shift();
      if (action === 'drop') {
        // Model an accepted job whose 202 response was lost on the browser connection.
        await route.abort();
        return;
      }
      await route.fulfill({
        status: 202,
        contentType: 'application/json',
        body: JSON.stringify({id: jobID, status: 'queued'}),
      });
      return;
    }
    const statusPrefix = '/viewer/image/video/generations/';
    if (requestURL.pathname.startsWith(statusPrefix) && request.method() === 'GET') {
      const jobID = requestURL.pathname.slice(statusPrefix.length);
      if (!Array.from(serverJobs.values()).includes(jobID)) {
        await route.fulfill({status: 404, contentType: 'application/json', body: '{}'});
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({id: jobID, status: 'queued'}),
      });
      return;
    }
    await route.abort();
  });
  return serverJobs;
}

async function waitForSubmissionResult(page, accepted) {
  if (accepted) {
    await page.waitForFunction((key) =>
      /^vid_retry_[0-9]+$/.test(localStorage.getItem(key) || '') &&
      localStorage.getItem('rencrow.image.video.pendingRequest') === null, jobKey);
    return;
  }
  await page.waitForFunction(() =>
    !document.getElementById('imageGenerateBtn').disabled &&
    document.getElementById('imageStatus').textContent === 'failed');
}

async function prepareVideo(page, mode, promptText) {
  await page.selectOption('#imageOutputKind', 'video');
  await page.selectOption('#videoMode', mode);
  await page.fill('#imagePrompt', promptText);
  await page.fill('#videoSeed', '42');
}

async function testT2VRetry(browser) {
  const context = await browser.newContext();
  const page = await context.newPage();
  const calls = [];
  const serverJobs = await installRoutes(page, ['drop', 'accept'], calls);
  await page.goto(origin, {waitUntil: 'domcontentloaded'});
  await prepareVideo(page, 't2v', 'A paper boat crossing a quiet lake');
  await page.click('#imageGenerateBtn');
  await waitForSubmissionResult(page, false);
  await page.click('#imageGenerateBtn');
  await waitForSubmissionResult(page, true);

  assert.equal(calls.length, 2, 'T2V should send one retry after the lost response');
  assert.match(calls[0].idempotencyKey, /^[0-9a-f]{32}$/);
  assert.equal(calls[0].idempotencyKey, calls[1].idempotencyKey, 'T2V retry must reuse its persisted key');
  assert.equal(calls[0].jobID, calls[1].jobID, 'lost 202 retry must recover the accepted T2V job');
  assert.equal(serverJobs.size, 1, 'same T2V key must not create a duplicate job');
  for (const call of calls) {
    assert.ok(call.pending, 'pending key must be persisted before POST');
    assert.equal(call.pending.key, call.idempotencyKey);
    assert.match(call.pending.fingerprint, /^[0-9a-f]{64}$/);
    assert.equal(call.pending.mode, 't2v');
    assert.match(call.contentType, /^multipart\/form-data;/);
  }
  await context.close();
}

async function testI2VReloadRetryAndImageChange(browser) {
  const context = await browser.newContext();
  const page = await context.newPage();
  const calls = [];
  const serverJobs = await installRoutes(page, ['drop', 'drop', 'accept'], calls);
  await page.goto(origin, {waitUntil: 'domcontentloaded'});
  await prepareVideo(page, 'i2v', 'A paper boat crossing a quiet lake');
  const imageA = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x01, 0x02, 0x03]);
  await page.locator('#videoInputImage').setInputFiles({
    name: 'same-name.png',
    mimeType: 'image/png',
    buffer: imageA,
  });
  await page.click('#imageGenerateBtn');
  await waitForSubmissionResult(page, false);

  await page.reload({waitUntil: 'domcontentloaded'});
  await page.waitForFunction(() =>
    document.getElementById('videoJobStatus').textContent.includes('同じ開始画像'));
  await prepareVideo(page, 'i2v', 'A paper boat crossing a quiet lake');
  await page.locator('#videoInputImage').setInputFiles({
    name: 'same-name.png',
    mimeType: 'image/png',
    buffer: imageA,
  });
  await page.click('#imageGenerateBtn');
  await waitForSubmissionResult(page, false);

  await page.reload({waitUntil: 'domcontentloaded'});
  await page.waitForFunction(() =>
    document.getElementById('videoJobStatus').textContent.includes('同じ開始画像'));
  await prepareVideo(page, 'i2v', 'A paper boat crossing a quiet lake');
  const imageB = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x01, 0x02, 0x04]);
  await page.locator('#videoInputImage').setInputFiles({
    name: 'same-name.png',
    mimeType: 'image/png',
    buffer: imageB,
  });
  await page.click('#imageGenerateBtn');
  await waitForSubmissionResult(page, true);

  assert.equal(calls.length, 3, 'I2V should retry twice after simulated response loss');
  assert.equal(calls[0].idempotencyKey, calls[1].idempotencyKey, 'same image after reload must reuse the pending key');
  assert.equal(calls[0].jobID, calls[1].jobID, 'lost 202 retry must recover the accepted I2V job');
  assert.equal(calls[0].pending.fingerprint, calls[1].pending.fingerprint);
  assert.notEqual(calls[1].idempotencyKey, calls[2].idempotencyKey, 'different image bytes must create a new request key');
  assert.notEqual(calls[1].jobID, calls[2].jobID, 'changed image must create a separate I2V job');
  assert.equal(serverJobs.size, 2, 'same I2V key reuses one job while changed image creates one new job');
  assert.notEqual(calls[1].pending.fingerprint, calls[2].pending.fingerprint, 'I2V fingerprint must include image bytes');
  for (const call of calls) {
    assert.ok(call.pending, 'I2V pending key must be persisted before POST');
    assert.equal(call.pending.key, call.idempotencyKey);
    assert.equal(call.pending.mode, 'i2v');
    assert.match(call.pending.fingerprint, /^[0-9a-f]{64}$/);
    assert.match(call.contentType, /^multipart\/form-data;/);
    assert.ok(call.body.includes(Buffer.from('same-name.png')), 'I2V multipart request must include the selected image');
  }
  await context.close();
}

const browser = await chromium.launch({headless: true});
try {
  await testT2VRetry(browser);
  await testI2VReloadRetryAndImageChange(browser);
  process.stdout.write('PASS: T2V and I2V response-loss idempotency retries\n');
} finally {
  await browser.close();
}
