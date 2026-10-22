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
const viewerCSS = fs.readFileSync(path.join(repoRoot, 'internal/adapter/viewer/assets/css/viewer.css'), 'utf8');
const deskCSS = fs.readFileSync(path.join(repoRoot, 'internal/adapter/viewer/assets/css/tabs/desk.css'), 'utf8');
const imageCSS = fs.readFileSync(path.join(repoRoot, 'internal/adapter/viewer/assets/css/tabs/image.css'), 'utf8');
const origin = 'https://image-video-idempotency.test';
const pendingKey = 'rencrow.image.video.pendingRequest';
const jobKey = 'rencrow.image.video.lastJobId';
const credential = 'browser-test-credential-0123456789abcdef0123456789abcdef';
const maxImageUploadBytes = 20 * 1024 * 1024;
const resultPNG = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScL/nwAAAABJRU5ErkJggg==', 'base64');

function fixtureHTML() {
  const script = imageScript.replaceAll('</script', '<\\/script');
  const style = `${viewerCSS}\n${deskCSS}\n${imageCSS}`;
  return `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Image and Movie fixture</title><style>${style}</style></head>
<body>
  <section id="panel-image" class="panel active">
  <section class="desk-screen desk-workspace image-workspace">
    <aside class="desk-side image-side">
      <form id="imageGenerationForm" class="daily-desk-card image-prompt-card" novalidate>
        <label for="imageMode">Mode</label>
        <select id="imageMode" class="desk-select">
          <option value="t2i">Text to Image</option>
          <option value="pose" disabled>Pose (unavailable)</option>
          <option value="clothing" disabled>Clothing (unavailable)</option>
        </select>
        <p id="imageEditOptionsStatus" role="status" aria-live="polite"></p>
        <label id="imagePromptLabel" for="imagePrompt">Prompt</label>
        <textarea id="imagePrompt" class="daily-desk-input image-prompt" rows="5" maxlength="8000"></textarea>
        <div id="imageEditInputs" class="image-edit-inputs" hidden>
          <p id="imageEditModelLabel" class="image-profile-note"><strong>Model:</strong> Qwen Image Edit 2511</p>
          <label for="imagePersonImage">Person image (PNG or JPEG)</label>
          <input id="imagePersonImage" type="file" accept="image/png,image/jpeg,.png,.jpg,.jpeg">
          <label id="imageReferenceImageLabel" for="imageReferenceImage">Reference image (PNG or JPEG)</label>
          <input id="imageReferenceImage" type="file" accept="image/png,image/jpeg,.png,.jpg,.jpeg">
          <span class="image-profile-note">各画像は最大20 MiB</span>
        </div>
        <div id="imageTextToImageControls">
          <label for="imageNegativePrompt">Negative prompt</label>
          <textarea id="imageNegativePrompt" class="daily-desk-input image-negative-prompt" rows="2"></textarea>
        </div>
        <label for="imageSeed">Seed</label>
        <input id="imageSeed" class="desk-input" type="number" value="1" min="-1" max="9007199254740991" step="1">
        <p id="imageOptionsStatus" role="status" aria-live="polite">Loading image models</p>
        <div id="imageOptionsError" class="image-options-error" role="alert" hidden></div>
        <button id="imageOptionsRetry" type="button" hidden>Retry options</button>
        <fieldset id="imageDynamicOptions" class="image-dynamic-options" disabled hidden>
          <label for="imageModel">Model</label><select id="imageModel"></select>
          <div class="image-dimension-fields">
            <label for="imageWidth">Width</label><input id="imageWidth" type="number">
            <label for="imageHeight">Height</label><input id="imageHeight" type="number">
          </div>
          <label for="imageSteps">Steps</label><input id="imageSteps" type="number">
          <label for="imageCfgScale">CFG scale</label><input id="imageCfgScale" type="number">
          <label id="imageLoraStrengthField" for="imageLoraStrength" hidden>MOSS DMD LoRA strength</label>
          <input id="imageLoraStrength" type="number" hidden>
          <label id="imageFaceDetailerField" class="image-checkbox-option" for="imageFaceDetailer" hidden><input id="imageFaceDetailer" type="checkbox"><span>FaceDetailer</span></label>
        </fieldset>
        <button id="imageGenerateBtn" class="desk-primary-btn" type="submit" disabled>Generate image</button>
      </form>
    </aside>
    <section class="desk-main image-main">
      <div id="imageStatus" class="desk-status-pill" role="status" aria-live="polite"></div>
      <div id="imageResultEmpty" class="daily-desk-card image-empty"></div>
      <figure id="imageResultFigure" class="image-result" hidden><img id="imageResult" alt="Generated image"><figcaption id="imageResultPrompt"></figcaption></figure>
      <div id="imageResultMeta"></div>
      <div id="imageError" class="image-error" role="alert" hidden></div>
    </section>
  </section>
  </section>
  <section id="panel-movie" class="panel">
    <div class="desk-screen desk-workspace movie-workspace">
      <aside class="desk-side">
        <form id="movieGenerationForm" class="daily-desk-card image-prompt-card">
          <label for="moviePrompt">Prompt</label>
          <textarea id="moviePrompt"></textarea>
          <label for="movieMode">Video mode</label>
          <select id="movieMode"><option value="t2v">T2V</option><option value="i2v">I2V</option></select>
          <p id="movieOptionsStatus" role="status" aria-live="polite">Loading video options</p>
          <div id="movieOptionsError" role="alert" hidden></div>
          <button id="movieOptionsRetry" type="button" hidden>Retry options</button>
          <label for="movieAspectRatio">Aspect ratio</label>
          <select id="movieAspectRatio" disabled></select>
          <dl class="video-fixed-settings">
            <dt>Model</dt><dd id="movieModel">—</dd>
            <dt>Resolution</dt><dd><span id="movieMegapixels">—</span> MP</dd>
            <dt>Duration</dt><dd><span id="movieDurationSeconds">—</span> s</dd>
            <dt>Steps</dt><dd id="movieSteps">—</dd>
          </dl>
          <div id="movieInputImageRow"><input id="movieInputImage" type="file" accept="image/png,image/jpeg"></div>
          <input id="movieSeed" value="42">
          <button id="movieGenerateBtn" type="submit">Generate video</button>
        </form>
        <section id="videoAuthPanel">
          <form id="videoLoginForm">
            <input id="videoCredential" type="password">
            <button id="videoLoginButton" type="submit">Sign in</button>
          </form>
          <button id="videoLogoutButton" type="button" hidden>Sign out</button>
          <p id="videoAuthStatus"></p>
        </section>
      </aside>
      <section class="desk-main movie-main">
        <div id="movieStatus"></div>
        <div id="movieError" hidden></div>
        <div id="videoResultEmpty"></div>
        <section id="videoResultPanel" hidden>
          <div id="videoJobStatus"></div>
          <progress id="videoProgress"></progress>
          <video id="videoResult" hidden></video>
          <a id="videoDownload" hidden>Download</a>
        </section>
      </section>
    </div>
  </section>
  <script>${script}</script>
</body>
</html>`;
}

async function installRoutes(page, actions, calls, options = {}) {
  const serverJobs = new Map();
  let authenticated = Boolean(options.authenticated);
  const loginRequests = [];
  const statusRequests = [];
  const imageCalls = [];
  const videoOptionRequests = [];
  if (options.savedJob) serverJobs.set('saved', options.savedJob);
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
    if (requestURL.pathname === '/viewer/image/options' && request.method() === 'GET') {
      const payload = options.imageOptions || {
        ok: true,
        default_model: 'moss_profile',
        models: [{
          id: 'moss_profile',
          engine: 'comfyui',
          defaults: {width: 768, height: 768, steps: 24, cfg_scale: 4, face_detailer: false, lora_strength: 0.7},
          limits: {
            width: {min: 256, max: 1536, multiple_of: 64},
            height: {min: 256, max: 1536, multiple_of: 64},
            steps: {min: 1, max: 50},
            cfg_scale: {min: 1, max: 12, step: 0.5},
            lora_strength: {min: 0, max: 1, step: 0.05},
          },
          features: {face_detailer: true, lora_strength: true},
        }],
      };
      await route.fulfill({status: 200, contentType: 'application/json', body: JSON.stringify(payload)});
      return;
    }
    if (requestURL.pathname === '/viewer/image/edit/options' && request.method() === 'GET') {
      const status = options.editOptionsStatus || 200;
      const payload = options.editOptions || {ok: true, operations: ['pose', 'clothing']};
      await route.fulfill({status, contentType: 'application/json', body: JSON.stringify(payload)});
      return;
    }
    if (requestURL.pathname === '/viewer/image/generate' && request.method() === 'POST') {
      const body = JSON.parse(request.postData() || '{}');
      const id = `img_ui_${imageCalls.length + 1}`;
      imageCalls.push({kind: 't2i', body});
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ok: true, id, profile: body.model || 'non_comfy_default', prompt: body.prompt, image: {id, url: `/viewer/image/result?id=${id}`, width: 1, height: 1}}),
      });
      return;
    }
    if (requestURL.pathname === '/viewer/image/edit' && request.method() === 'POST') {
      const id = `img_ui_${imageCalls.length + 1}`;
      imageCalls.push({kind: 'edit', contentType: request.headers()['content-type'] || '', body: request.postDataBuffer() || Buffer.alloc(0)});
      const textBody = imageCalls.at(-1).body.toString('latin1');
      const operationMatch = textBody.match(/name="operation"\r\n\r\n([^\r]+)/);
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ok: true, id, operation: operationMatch ? operationMatch[1] : '', prompt: 'Edit result', profile: 'qwen-image-edit-2511', image: {id, url: `/viewer/image/result?id=${id}`, width: 1, height: 1}}),
      });
      return;
    }
    if (requestURL.pathname === '/viewer/image/result' && request.method() === 'GET') {
      await route.fulfill({status: 200, contentType: 'image/png', body: resultPNG});
      return;
    }
    if (requestURL.pathname === '/viewer/image/video/options' && request.method() === 'GET') {
      const requestedMode = requestURL.searchParams.get('mode');
      videoOptionRequests.push({mode: requestedMode, authenticated});
      if (options.videoOptionsUnavailable && options.videoOptionsUnavailable[requestedMode]) {
        await route.fulfill({status: 503, contentType: 'application/json', body: JSON.stringify({message: 'options temporarily unavailable'})});
        return;
      }
      const defaults = {
        t2v: {
          model: 'hunyuan-video-t2v', mode: 't2v',
          aspect_ratios: ['1:1 (Square)', '4:3 (Standard)', '9:16 (Portrait Widescreen)', '16:9 (Widescreen)'],
          default_aspect_ratio: '16:9 (Widescreen)', megapixels: 0.2, duration_seconds: 5, steps: 8,
        },
        i2v: {
          model: 'hunyuan-video-i2v', mode: 'i2v',
          aspect_ratios: ['1:1 (Square)', '9:16 (Portrait Widescreen)'],
          default_aspect_ratio: '1:1 (Square)', megapixels: 0.2, duration_seconds: 5, steps: 8,
        },
      };
      const payload = options.videoOptions && options.videoOptions[requestedMode]
        ? options.videoOptions[requestedMode]
        : defaults[requestedMode];
      if (!payload) {
        await route.fulfill({status: 400, contentType: 'application/json', body: JSON.stringify({message: 'invalid mode'})});
        return;
      }
      await route.fulfill({status: 200, contentType: 'application/json', body: JSON.stringify(payload)});
      return;
    }
    if (requestURL.pathname === '/viewer/image/video/session' && request.method() === 'GET') {
      const sessionAtRequestStart = authenticated;
      if (options.sessionDelayMs) await new Promise((resolve) => setTimeout(resolve, options.sessionDelayMs));
      const sessionForResponse = options.snapshotSession ? sessionAtRequestStart : authenticated;
      await route.fulfill({
        status: sessionForResponse ? 200 : 401,
        contentType: 'application/json',
        body: JSON.stringify({authenticated: sessionForResponse}),
      });
      return;
    }
    if (requestURL.pathname === '/viewer/image/video/session' && request.method() === 'POST') {
      const payload = JSON.parse(request.postData() || '{}');
      loginRequests.push(payload);
      authenticated = payload.credential === credential;
      await route.fulfill({
        status: authenticated ? 200 : 401,
        contentType: 'application/json',
        body: JSON.stringify({authenticated}),
      });
      return;
    }
    if (requestURL.pathname === '/viewer/image/video/session' && request.method() === 'DELETE') {
      authenticated = false;
      await route.fulfill({status: 204, body: ''});
      return;
    }
    if (requestURL.pathname === '/viewer/image/video/generations' && request.method() === 'POST') {
      if (!authenticated) {
        await route.fulfill({status: 404, contentType: 'application/json', body: '{}'});
        return;
      }
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
      if (!authenticated) {
        await route.fulfill({status: 404, contentType: 'application/json', body: '{}'});
        return;
      }
      statusRequests.push(jobID);
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
  return {serverJobs, loginRequests, statusRequests, imageCalls, videoOptionRequests};
}

async function signInVideo(page) {
  await showMoviePanel(page);
  await page.locator('#videoCredential').fill(credential);
  await page.click('#videoLoginButton');
  await page.waitForFunction(() => document.getElementById('videoAuthStatus').textContent === '動画を利用できます。');
  assert.equal(await page.locator('#videoCredential').inputValue(), '', 'credential field is cleared after login');
  assert.equal(await page.evaluate(() => location.href.includes('browser-test-credential')), false, 'credential is not in the URL');
  assert.equal(await page.evaluate(() => Object.values(localStorage).some((value) => value.includes('browser-test-credential'))), false, 'credential is not in localStorage');
  assert.equal(await page.evaluate(() => Object.values(sessionStorage).some((value) => value.includes('browser-test-credential'))), false, 'credential is not in sessionStorage');
}

async function showMoviePanel(page) {
  await page.evaluate(() => {
    document.getElementById('panel-image').classList.remove('active');
    document.getElementById('panel-movie').classList.add('active');
  });
}

async function waitForSubmissionResult(page, accepted) {
  if (accepted) {
    await page.waitForFunction((key) =>
      /^vid_retry_[0-9]+$/.test(localStorage.getItem(key) || '') &&
      localStorage.getItem('rencrow.image.video.pendingRequest') === null, jobKey);
    return;
  }
  await page.waitForFunction(() =>
    !document.getElementById('movieGenerateBtn').disabled &&
    document.getElementById('movieStatus').textContent === 'failed');
}

async function prepareVideo(page, mode, promptText) {
  await showMoviePanel(page);
  await page.selectOption('#movieMode', mode);
  await page.fill('#moviePrompt', promptText);
  await page.fill('#movieSeed', '42');
}

async function testImageModesAndUploads(browser) {
  const context = await browser.newContext({viewport: {width: 1366, height: 900}});
  const page = await context.newPage();
  const browserErrors = [];
  page.on('pageerror', (error) => browserErrors.push(error.message));
  page.on('console', (message) => {
    if (message.type() === 'error') browserErrors.push(message.text());
  });
  const routes = await installRoutes(page, [], [], {authenticated: true});
  await page.goto(origin, {waitUntil: 'domcontentloaded'});
  await page.waitForFunction(() => document.getElementById('imageOptionsStatus').textContent.includes('画像モデルを利用できます'));
  await page.waitForFunction(() => !document.querySelector('#imageMode option[value="pose"]').disabled);

  assert.equal(await page.locator('#imageMode').inputValue(), 't2i');
  assert.equal(await page.locator('#imageMode option[value="clothing"]').isDisabled(), false);
  assert.equal(await page.locator('#imageModel').inputValue(), 'moss_profile');
  assert.equal(await page.locator('#imageLoraStrength').isVisible(), true, 'MOSS LoRA control follows advertised features');
  assert.equal(await page.locator('#imageFaceDetailerField').isVisible(), true, 'MOSS FaceDetailer control follows advertised features');
  const desktopLayout = await page.locator('.image-workspace').evaluate((node) => ({
    display: getComputedStyle(node).display,
    columns: getComputedStyle(node).gridTemplateColumns.split(' ').length,
  }));
  assert.equal(desktopLayout.display, 'grid');
  assert.equal(desktopLayout.columns, 2, 'desktop Image workspace has separate controls and result columns');

  await page.fill('#imagePrompt', 'A paper crane on a quiet desk');
  await page.fill('#imageNegativePrompt', 'blurry');
  await page.fill('#imageSeed', '42');
  await page.click('#imageGenerateBtn');
  await page.waitForFunction(() => document.getElementById('imageStatus').textContent === 'complete');
  assert.deepEqual(routes.imageCalls[0], {
    kind: 't2i',
    body: {
      prompt: 'A paper crane on a quiet desk',
      negative_prompt: 'blurry',
      seed: 42,
      model: 'moss_profile',
      width: 768,
      height: 768,
      steps: 24,
      cfg_scale: 4,
      face_detailer: false,
      lora_strength: 0.7,
    },
  }, 'Text to Image sends the advertised MOSS model controls');
  assert.equal(await page.locator('#imageResultFigure').isVisible(), true, 'T2I result appears on the right');

  const person = {name: 'person.png', mimeType: 'image/png', buffer: Buffer.from([0x89, 0x50, 0x4e, 0x47, 1, 2, 3])};
  const poseReference = {name: 'pose-reference.jpg', mimeType: 'image/jpeg', buffer: Buffer.from([0xff, 0xd8, 0xff, 1, 2, 3])};
  await page.selectOption('#imageMode', 'pose');
  assert.match(await page.locator('#imageEditModelLabel').textContent(), /Qwen Image Edit 2511/);
  assert.equal(await page.locator('#imageReferenceImageLabel').textContent(), 'Pose reference (PNG or JPEG)');
  await page.fill('#imagePrompt', 'Use the reference body pose');
  await page.fill('#imageSeed', '-1');
  await page.locator('#imagePersonImage').setInputFiles(person);
  await page.locator('#imageReferenceImage').setInputFiles(poseReference);
  await page.click('#imageGenerateBtn');
  await page.waitForFunction(() => document.getElementById('imageStatus').textContent === 'complete');
  assert.equal(routes.imageCalls[1].kind, 'edit');
  assert.match(routes.imageCalls[1].contentType, /^multipart\/form-data; boundary=/);
  for (const field of ['operation', 'person', 'reference', 'prompt', 'seed']) {
    assert.ok(routes.imageCalls[1].body.includes(Buffer.from(`name="${field}"`)), `Pose multipart request includes ${field}`);
  }
  assert.ok(routes.imageCalls[1].body.includes(Buffer.from('pose-reference.jpg')));
  assert.ok(routes.imageCalls[1].body.includes(Buffer.from('\r\n\r\npose\r\n')));
  assert.ok(routes.imageCalls[1].body.includes(Buffer.from('\r\n\r\nUse the reference body pose\r\n')));
  assert.ok(routes.imageCalls[1].body.includes(Buffer.from('\r\n\r\n-1\r\n')));
  assert.match(await page.locator('#imageResultMeta').textContent(), /Image edit \/ pose/);

  const clothingReference = {name: 'coat.jpg', mimeType: 'image/jpeg', buffer: Buffer.from([0xff, 0xd8, 0xff, 4, 5, 6])};
  await page.selectOption('#imageMode', 'clothing');
  assert.match(await page.locator('#imageEditModelLabel').textContent(), /Qwen Image Edit 2511/);
  assert.equal(await page.locator('#imageReferenceImageLabel').textContent(), 'Clothing reference (PNG or JPEG)');
  await page.fill('#imagePrompt', 'Use the coat from the reference');
  await page.fill('#imageSeed', '7');
  await page.locator('#imagePersonImage').setInputFiles(person);
  await page.locator('#imageReferenceImage').setInputFiles(clothingReference);
  await page.click('#imageGenerateBtn');
  await page.waitForFunction(() => document.getElementById('imageStatus').textContent === 'complete');
  assert.equal(routes.imageCalls[2].kind, 'edit');
  assert.ok(routes.imageCalls[2].body.includes(Buffer.from('\r\n\r\nclothing\r\n')));
  assert.ok(routes.imageCalls[2].body.includes(Buffer.from('coat.jpg')));
  assert.match(await page.locator('#imageResultMeta').textContent(), /Image edit \/ clothing/);

  const callCountBeforeOversize = routes.imageCalls.length;
  await page.locator('#imagePersonImage').setInputFiles({
    name: 'too-large.png',
    mimeType: 'image/png',
    buffer: Buffer.alloc(maxImageUploadBytes + 1),
  });
  await page.click('#imageGenerateBtn');
  await page.waitForFunction(() => document.getElementById('imageError').textContent.includes('20 MiB'));
  assert.equal(routes.imageCalls.length, callCountBeforeOversize, 'oversized person uploads are rejected before network I/O');

  await page.setViewportSize({width: 390, height: 844});
  const mobileLayout = await page.locator('.image-workspace').evaluate((node) => ({
    columns: getComputedStyle(node).gridTemplateColumns.split(' ').length,
    documentWidth: document.documentElement.scrollWidth,
    viewportWidth: window.innerWidth,
  }));
  assert.equal(mobileLayout.columns, 1, 'narrow Image workspace stacks controls and result');
  assert.ok(mobileLayout.documentWidth <= mobileLayout.viewportWidth, 'narrow Image workspace has no horizontal overflow');
  await page.locator('#imageResultPrompt').evaluate((node) => { node.textContent = 'Long generated prompt '.repeat(160); });
  const captionFits = await page.locator('#imageResultPrompt').evaluate((node) => node.scrollWidth <= node.clientWidth);
  assert.equal(captionFits, true, 'long result prompt wraps inside the image result column');
  assert.deepEqual(browserErrors, [], 'Image interactions have no browser console or page errors');
  await context.close();
}

async function testNonComfyOptionsFallbackAndUnavailableEdits(browser) {
  const context = await browser.newContext();
  const page = await context.newPage();
  const routes = await installRoutes(page, [], [], {
    imageOptions: {ok: true, default_model: 'non_comfy_default', models: []},
    editOptionsStatus: 404,
    editOptions: {ok: false, error_code: 'IMAGE_EDIT_UNAVAILABLE', message: 'edits are not configured'},
  });
  await page.goto(origin, {waitUntil: 'domcontentloaded'});
  await page.waitForFunction(() => document.getElementById('imageOptionsStatus').textContent.includes('non_comfy_default'));
  await page.waitForFunction(() => document.getElementById('imageEditOptionsStatus').textContent.includes('not configured'));
  assert.equal(await page.locator('#imageDynamicOptions').isVisible(), false, 'empty model list hides model-specific controls');
  assert.equal(await page.locator('#imageGenerateBtn').isDisabled(), false, 'valid empty models fallback still allows T2I');
  assert.equal(await page.locator('#imageMode option[value="pose"]').isDisabled(), true);
  assert.equal(await page.locator('#imageMode option[value="clothing"]').isDisabled(), true);

  await page.fill('#imagePrompt', 'Default backend profile image');
  await page.fill('#imageSeed', '9');
  await page.click('#imageGenerateBtn');
  await page.waitForFunction(() => document.getElementById('imageStatus').textContent === 'complete');
  assert.deepEqual(routes.imageCalls[0], {
    kind: 't2i',
    body: {prompt: 'Default backend profile image', negative_prompt: '', seed: 9},
  }, 'non-Comfy fallback relies on the backend default and omits profile-specific parameters');
  await context.close();
}

async function testT2VRetry(browser) {
  const context = await browser.newContext();
  const page = await context.newPage();
  const calls = [];
  const routes = await installRoutes(page, ['drop', 'accept'], calls);
  await page.goto(origin, {waitUntil: 'domcontentloaded'});
  await page.waitForFunction(() => document.getElementById('movieOptionsStatus').textContent.includes('available'));
  assert.equal(routes.videoOptionRequests[0].authenticated, false, 'video options are available before browser login');
  assert.equal(await page.locator('#movieModel').textContent(), 'hunyuan-video-t2v');
  assert.equal(await page.locator('#movieMegapixels').textContent(), '0.2');
  assert.equal(await page.locator('#movieDurationSeconds').textContent(), '5');
  assert.equal(await page.locator('#movieSteps').textContent(), '8');
  await signInVideo(page);
  await prepareVideo(page, 't2v', 'A paper boat crossing a quiet lake');
  await page.selectOption('#movieAspectRatio', '4:3 (Standard)');
  await page.click('#movieGenerateBtn');
  await waitForSubmissionResult(page, false);
  await page.click('#movieGenerateBtn');
  await waitForSubmissionResult(page, true);

  assert.equal(calls.length, 2, 'T2V should send one retry after the lost response');
  assert.match(calls[0].idempotencyKey, /^[0-9a-f]{32}$/);
  assert.equal(calls[0].idempotencyKey, calls[1].idempotencyKey, 'T2V retry must reuse its persisted key');
  assert.equal(calls[0].jobID, calls[1].jobID, 'lost 202 retry must recover the accepted T2V job');
  assert.equal(routes.serverJobs.size, 1, 'same T2V key must not create a duplicate job');
  assert.deepEqual(routes.loginRequests, [{credential}], 'login submits the credential to the session endpoint');
  for (const call of calls) {
    assert.ok(call.pending, 'pending key must be persisted before POST');
    assert.equal(call.pending.key, call.idempotencyKey);
    assert.match(call.pending.fingerprint, /^[0-9a-f]{64}$/);
    assert.equal(call.pending.mode, 't2v');
    assert.equal(call.pending.version, 2);
    assert.equal(call.pending.aspect_ratio, '4:3 (Standard)');
    assert.match(call.body.toString('latin1'), /name="aspect_ratio"\r\n\r\n4:3 \(Standard\)\r\n/);
    assert.match(call.contentType, /^multipart\/form-data;/);
  }
  const previousJobID = await page.evaluate((key) => localStorage.getItem(key), jobKey);
  await page.selectOption('#movieAspectRatio', '1:1 (Square)');
  await page.click('#movieGenerateBtn');
  await page.waitForFunction(({pendingKey, jobKey, previousJobID}) =>
    localStorage.getItem(pendingKey) === null &&
    localStorage.getItem(jobKey) !== null &&
    localStorage.getItem(jobKey) !== previousJobID,
  {pendingKey, jobKey, previousJobID});
  assert.equal(calls.length, 3);
  assert.notEqual(calls[2].idempotencyKey, calls[1].idempotencyKey, 'changing aspect ratio creates a distinct idempotency key');
  assert.match(calls[2].body.toString('latin1'), /name="aspect_ratio"\r\n\r\n1:1 \(Square\)\r\n/);
  await context.close();
}

async function testT2VReloadOptionsFailureRetryPreservesAspect(browser) {
  const context = await browser.newContext();
  const page = await context.newPage();
  const calls = [];
  const videoOptionsUnavailable = {t2v: false};
  const routes = await installRoutes(page, ['drop', 'accept'], calls, {
    authenticated: true,
    videoOptionsUnavailable,
  });
  await page.goto(origin, {waitUntil: 'domcontentloaded'});
  await page.waitForFunction(() => document.getElementById('movieOptionsStatus').textContent.includes('available'));
  await prepareVideo(page, 't2v', 'Retry with the original aspect after reload');
  await page.selectOption('#movieAspectRatio', '4:3 (Standard)');
  await page.click('#movieGenerateBtn');
  await waitForSubmissionResult(page, false);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].pending.aspect_ratio, '4:3 (Standard)');

  videoOptionsUnavailable.t2v = true;
  await page.reload({waitUntil: 'domcontentloaded'});
  await showMoviePanel(page);
  await page.waitForFunction(() =>
    document.getElementById('movieOptionsStatus').textContent.includes('unavailable') &&
    !document.getElementById('movieOptionsRetry').hidden,
  );
  assert.equal(await page.locator('#movieGenerateBtn').isDisabled(), true, 'generation remains disabled while owner options are unavailable');

  videoOptionsUnavailable.t2v = false;
  await page.click('#movieOptionsRetry');
  await page.waitForFunction(() => document.getElementById('movieOptionsStatus').textContent.includes('available'));
  assert.equal(await page.locator('#movieAspectRatio').inputValue(), '4:3 (Standard)', 'options retry restores the uncertain request aspect');
  await page.fill('#moviePrompt', 'Retry with the original aspect after reload');
  await page.click('#movieGenerateBtn');
  await waitForSubmissionResult(page, true);

  assert.equal(calls.length, 2, 'uncertain request is retried with the same key rather than creating a second job');
  assert.equal(calls[1].idempotencyKey, calls[0].idempotencyKey);
  assert.equal(calls[1].jobID, calls[0].jobID);
  assert.equal(calls[1].pending.aspect_ratio, '4:3 (Standard)');
  assert.match(calls[1].body.toString('latin1'), /name="aspect_ratio"\r\n\r\n4:3 \(Standard\)\r\n/);
  assert.equal(routes.serverJobs.size, 1);
  await context.close();
}

async function testMovieOptionsAreModeSpecific(browser) {
  const context = await browser.newContext({viewport: {width: 390, height: 844}});
  const page = await context.newPage();
  const routes = await installRoutes(page, [], [], {});
  await page.goto(origin, {waitUntil: 'domcontentloaded'});
  await showMoviePanel(page);
  await page.waitForFunction(() => document.getElementById('movieOptionsStatus').textContent.includes('available'));
  assert.equal(await page.locator('#movieMode').inputValue(), 't2v');
  assert.equal(await page.locator('#movieModel').textContent(), 'hunyuan-video-t2v');
  assert.equal(await page.locator('#movieAspectRatio').inputValue(), '16:9 (Widescreen)');
  assert.deepEqual(routes.videoOptionRequests[0], {mode: 't2v', authenticated: false});

  await page.selectOption('#movieMode', 'i2v');
  await page.waitForFunction(() => document.getElementById('movieModel').textContent === 'hunyuan-video-i2v');
  assert.equal(await page.locator('#movieAspectRatio').inputValue(), '1:1 (Square)');
  assert.deepEqual(routes.videoOptionRequests.at(-1), {mode: 'i2v', authenticated: false});
  const layout = await page.locator('.movie-workspace').evaluate((node) => ({
    columns: getComputedStyle(node).gridTemplateColumns.split(' ').length,
    documentWidth: document.documentElement.scrollWidth,
    viewportWidth: window.innerWidth,
  }));
  assert.equal(layout.columns, 1, 'narrow Movie controls remain in a single column');
  assert.ok(layout.documentWidth <= layout.viewportWidth, 'Movie controls do not overflow narrow viewport');
  await context.close();
}

async function testI2VReloadRetryAndImageChange(browser) {
  const context = await browser.newContext();
  const page = await context.newPage();
  const calls = [];
  const routes = await installRoutes(page, ['drop', 'drop', 'accept'], calls);
  await page.goto(origin, {waitUntil: 'domcontentloaded'});
  await signInVideo(page);
  await prepareVideo(page, 'i2v', 'A paper boat crossing a quiet lake');
  const imageA = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x01, 0x02, 0x03]);
  await page.locator('#movieInputImage').setInputFiles({
    name: 'same-name.png',
    mimeType: 'image/png',
    buffer: imageA,
  });
  await page.click('#movieGenerateBtn');
  await waitForSubmissionResult(page, false);

  await page.reload({waitUntil: 'domcontentloaded'});
  await page.waitForFunction(() => document.getElementById('videoJobStatus').textContent.includes('同じ開始画像'));
  await prepareVideo(page, 'i2v', 'A paper boat crossing a quiet lake');
  await page.locator('#movieInputImage').setInputFiles({
    name: 'same-name.png',
    mimeType: 'image/png',
    buffer: imageA,
  });
  await page.click('#movieGenerateBtn');
  await waitForSubmissionResult(page, false);

  await page.reload({waitUntil: 'domcontentloaded'});
  await page.waitForFunction(() => document.getElementById('videoJobStatus').textContent.includes('同じ開始画像'));
  await prepareVideo(page, 'i2v', 'A paper boat crossing a quiet lake');
  const imageB = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x01, 0x02, 0x04]);
  await page.locator('#movieInputImage').setInputFiles({
    name: 'same-name.png',
    mimeType: 'image/png',
    buffer: imageB,
  });
  await page.click('#movieGenerateBtn');
  await waitForSubmissionResult(page, true);

  assert.equal(calls.length, 3, 'I2V should retry twice after simulated response loss');
  assert.equal(calls[0].idempotencyKey, calls[1].idempotencyKey, 'same image after reload must reuse the pending key');
  assert.equal(calls[0].jobID, calls[1].jobID, 'lost 202 retry must recover the accepted I2V job');
  assert.equal(calls[0].pending.fingerprint, calls[1].pending.fingerprint);
  assert.notEqual(calls[1].idempotencyKey, calls[2].idempotencyKey, 'different image bytes must create a new request key');
  assert.notEqual(calls[1].jobID, calls[2].jobID, 'changed image must create a separate I2V job');
  assert.equal(routes.serverJobs.size, 2, 'same I2V key reuses one job while changed image creates one new job');
  assert.notEqual(calls[1].pending.fingerprint, calls[2].pending.fingerprint, 'I2V fingerprint must include image bytes');
  for (const call of calls) {
    assert.ok(call.pending, 'I2V pending key must be persisted before POST');
    assert.equal(call.pending.key, call.idempotencyKey);
    assert.equal(call.pending.mode, 'i2v');
    assert.equal(call.pending.aspect_ratio, '1:1 (Square)');
    assert.match(call.pending.fingerprint, /^[0-9a-f]{64}$/);
    assert.match(call.body.toString('latin1'), /name="aspect_ratio"\r\n\r\n1:1 \(Square\)\r\n/);
    assert.match(call.contentType, /^multipart\/form-data;/);
    assert.ok(call.body.includes(Buffer.from('same-name.png')), 'I2V multipart request must include the selected image');
  }
  await context.close();
}

async function testLoginRequiredAndSavedJobResumesAfterAuthentication(browser) {
  const context = await browser.newContext();
  const page = await context.newPage();
  const calls = [];
  const routes = await installRoutes(page, [], calls, {savedJob: 'vid_saved'});
  await page.addInitScript((key) => localStorage.setItem(key, 'vid_saved'), jobKey);
  await page.goto(origin, {waitUntil: 'domcontentloaded'});
  await showMoviePanel(page);
  await page.waitForFunction(() => document.getElementById('videoJobStatus').textContent.includes('認証すると保存した動画'));
  assert.deepEqual(routes.statusRequests, [], 'saved job is not queried before authentication');
  await signInVideo(page);
  await page.waitForFunction(() => document.getElementById('videoJobStatus').textContent.includes('queued'));
  assert.deepEqual(routes.statusRequests, ['vid_saved'], 'saved job resumes after successful login');
  assert.equal(await page.locator('#videoLogoutButton').isVisible(), true, 'sign-out is visible while authenticated');
  await page.click('#videoLogoutButton');
  await page.waitForFunction(() => document.getElementById('videoAuthStatus').textContent.includes('サインアウトしました'));
  assert.equal(await page.locator('#videoLoginForm').isVisible(), true, 'login form returns after sign-out');
  assert.equal(await page.locator('#videoLogoutButton').isVisible(), false, 'sign-out hides after sign-out');
  assert.equal(await page.evaluate((key) => localStorage.getItem(key), jobKey), 'vid_saved', 'sign-out preserves the saved job for the next login');
  await context.close();
}

async function testDelayedSessionDoubleClickSubmitsOnlyOnce(browser) {
  const context = await browser.newContext();
  const page = await context.newPage();
  const calls = [];
  await installRoutes(page, ['accept'], calls, {authenticated: true, sessionDelayMs: 100});
  await page.goto(origin, {waitUntil: 'domcontentloaded'});
  await prepareVideo(page, 't2v', 'One job after a delayed session check');
  await page.locator('#movieGenerateBtn').evaluate((button) => {
    button.click();
    button.click();
  });
  await waitForSubmissionResult(page, true);
  assert.equal(calls.length, 1, 'rapid clicks while session validation is pending submit one generation request');
  await context.close();
}

async function testLateInitialSessionDoesNotUndoLogin(browser) {
  const context = await browser.newContext();
  const page = await context.newPage();
  const calls = [];
  await installRoutes(page, [], calls, {sessionDelayMs: 600, snapshotSession: true});
  await page.goto(origin, {waitUntil: 'domcontentloaded'});
  await signInVideo(page);
  await page.waitForTimeout(650);
  assert.equal(await page.locator('#videoLoginForm').isVisible(), false, 'late unauthenticated startup response cannot undo successful login');
  assert.equal(await page.locator('#videoLogoutButton').isVisible(), true, 'authenticated sign-out remains visible after the stale response');
  await context.close();
}

const browser = await chromium.launch({headless: true});
try {
  await testImageModesAndUploads(browser);
  await testNonComfyOptionsFallbackAndUnavailableEdits(browser);
  await testMovieOptionsAreModeSpecific(browser);
  await testT2VRetry(browser);
  await testT2VReloadOptionsFailureRetryPreservesAspect(browser);
  await testI2VReloadRetryAndImageChange(browser);
  await testLoginRequiredAndSavedJobResumesAfterAuthentication(browser);
  await testDelayedSessionDoubleClickSubmitsOnlyOnce(browser);
  await testLateInitialSessionDoesNotUndoLogin(browser);
  process.stdout.write('PASS: Image T2I/Pose/Clothing, non-Comfy fallback, upload boundary, responsive layout, and Movie auth/idempotency flows\n');
} finally {
  await browser.close();
}
