#!/usr/bin/env node

import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import process from 'node:process';
import {chromium, firefox, request, webkit} from 'playwright';

const baseURL = String(process.env.RENCROW_E2E_BASE_URL || 'http://127.0.0.1:18791').replace(/\/$/, '');
const browserName = String(process.env.RENCROW_E2E_BROWSER || 'firefox').toLowerCase();
const browserType = {chromium, firefox, webkit}[browserName];
const artifactDir = path.resolve(process.env.RENCROW_E2E_ARTIFACT_DIR || 'output/playwright/to-be-ops-live-e2e');
const headless = process.env.RENCROW_E2E_HEADED !== '1';
const scenario = String(process.env.RENCROW_E2E_SCENARIO || 'unavailable').toLowerCase();
const runFaultMatrix = process.env.RENCROW_E2E_FAULT_MATRIX === '1';

if (!browserType) {
  throw new Error(`unsupported RENCROW_E2E_BROWSER: ${browserName}`);
}

const targetPaths = [
  '/viewer/advisors',
  '/viewer/knowledge-relations/summary',
  '/viewer/revenue/opportunities',
  '/viewer/revenue/economic-tasks',
  '/viewer/revenue/economic-reflections',
  '/viewer/revenue',
  '/viewer/recall/traces',
];

function targetPath(rawURL) {
  const pathname = new URL(rawURL).pathname;
  return targetPaths.includes(pathname) ? pathname : '';
}

async function seedEconomicRecords(runID) {
  const api = await request.newContext({baseURL});
  const recordCount = scenario === 'populated' ? 7 : 1;
  const longSuffix = '0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ';
  let opportunityID = '';
  let taskID = '';
  let reflectionID = '';
  try {
    for (let index = 0; index < recordCount; index += 1) {
      const suffix = index === recordCount - 1 ? `${runID}-${longSuffix}` : `${runID}-${index}`;
      opportunityID = `opp-browser-e2e-${suffix}`;
      taskID = `task-browser-e2e-${suffix}`;
      reflectionID = `reflection-browser-e2e-${suffix}`;
      const opportunity = await api.post('/viewer/revenue/opportunities', {
        data: {
          opportunity_id: opportunityID,
          source_kind: 'browser_e2e',
          title: `Browser E2E draft opportunity ${index}`,
          expected_revenue: index === 0 ? 0 : 3000 + index,
          expected_cost: index === 0 ? 0 : 800,
          approval_state: 'draft',
        },
      });
      assert.equal(opportunity.status(), 201, `seed opportunity failed: ${opportunity.status()} ${await opportunity.text()}`);

      const task = await api.post('/viewer/revenue/economic-tasks', {
        data: {
          task_id: taskID,
          opportunity_id: opportunityID,
          agent_id: 'shiro',
          task_kind: 'billing',
          status: 'draft',
          approval_mode: 'human_required',
        },
      });
      assert.equal(task.status(), 201, `seed economic task failed: ${task.status()} ${await task.text()}`);

      const reflection = await api.post('/viewer/revenue/economic-reflections', {
        data: {
          reflection_id: reflectionID,
          opportunity_id: opportunityID,
          outcome: 'drafted',
          lessons: ['browser E2E safe fixture'],
        },
      });
      assert.equal(reflection.status(), 201, `seed reflection failed: ${reflection.status()} ${await reflection.text()}`);
    }
  } finally {
    await api.dispose();
  }
  return {opportunityID, taskID, reflectionID, recordCount};
}

async function verifyViewport(browser, viewport, fixture) {
  const context = await browser.newContext({viewport});
  const page = await context.newPage();
  const consoleErrors = [];
  const pageErrors = [];
  const failedRequests = [];
  const targetResponses = [];

  page.on('console', (message) => {
    if (message.type() === 'error') {
      const location = message.location();
      consoleErrors.push({text: message.text(), url: location.url || '', line: location.lineNumber || 0});
    }
  });
  page.on('pageerror', (error) => pageErrors.push(error.message));
  page.on('requestfailed', (req) => failedRequests.push(`${req.method()} ${req.url()} ${req.failure()?.errorText || ''}`));
  page.on('response', (response) => {
    const pathname = targetPath(response.url());
    if (pathname) targetResponses.push({path: pathname, method: response.request().method(), status: response.status()});
  });

  try {
    await page.goto(`${baseURL}/viewer?tab=ops`, {waitUntil: 'domcontentloaded', timeout: 30000});
    await page.waitForSelector('body[data-viewer-tab="ops"]', {timeout: 15000});
    await page.waitForFunction(() => {
      const summary = document.getElementById('toBeOpsSummary');
      return summary?.getAttribute('aria-busy') === 'false' && summary.querySelectorAll('[data-to-be-block]').length === 5;
    }, null, {timeout: 15000});

    const ui = await page.evaluate(({opportunityID, taskID, width}) => {
      const summary = document.getElementById('toBeOpsSummary');
      const blocks = Array.from(summary.querySelectorAll('[data-to-be-block]'));
      const details = Array.from(summary.querySelectorAll('details'));
      const rects = blocks.map((block) => {
        const rect = block.getBoundingClientRect();
        const style = getComputedStyle(block);
        return {
          key: block.getAttribute('data-to-be-block'),
          title: block.querySelector('h3')?.textContent?.trim() || '',
          metricCount: block.querySelectorAll('.ops-to-be-metric').length,
          x: rect.x,
          width: rect.width,
          scrollWidth: block.scrollWidth,
          clientWidth: block.clientWidth,
          pointerEvents: style.pointerEvents,
          position: style.position,
          zIndex: style.zIndex,
          backdropFilter: style.backdropFilter,
        };
      });
      const economicText = summary.querySelector('[data-to-be-block="economic-objective"]')?.textContent || '';
      const approvalText = summary.querySelector('[data-to-be-block="approval-queue"]')?.textContent || '';
      return {
        viewportWidth: width,
        titles: rects.map((item) => item.title),
        keys: rects.map((item) => item.key),
        statuses: blocks.map((block) => block.querySelector('.ops-to-be-status')?.textContent?.trim() || ''),
        metricCounts: rects.map((item) => item.metricCount),
        detailsCount: details.length,
        openDetails: details.filter((item) => item.open).length,
        tableCount: summary.querySelectorAll('table').length,
        buttonCount: summary.querySelectorAll('button').length,
        documentOverflow: document.documentElement.scrollWidth > document.documentElement.clientWidth,
        cardOverflow: rects.some((item) => item.scrollWidth > item.clientWidth + 1),
        gridColumns: getComputedStyle(summary).gridTemplateColumns,
        rects,
        economicHasFixture: economicText.includes(opportunityID),
        approvalHasFixture: approvalText.includes(taskID) && approvalText.includes(opportunityID),
        advisorHasFixture: (summary.querySelector('[data-to-be-block="advisor-agent"]')?.textContent || '').includes('advisor-run-e2e'),
        traceHasFixture: (summary.querySelector('[data-to-be-block="recent-trace"]')?.textContent || '').includes('response-e2e'),
        forbiddenTextVisible: /raw_output|prompt body|api[_-]?key|secret value/i.test(summary.textContent || ''),
      };
    }, {...fixture, width: viewport.width});

    const expectedTitles = ['Advisor / Agent', 'Knowledge Relation', 'Economic Objective', 'Approval Queue', 'Recent Trace'];
    assert.deepEqual(ui.titles, expectedTitles);
    assert.deepEqual(ui.keys, ['advisor-agent', 'knowledge-relation', 'economic-objective', 'approval-queue', 'recent-trace']);
    const expectedStatuses = scenario === 'populated'
      ? ['ok', 'ok', 'ok', 'warning', 'ok']
      : ['warning', 'unavailable', 'ok', 'warning', 'unavailable'];
    assert.deepEqual(ui.statuses, expectedStatuses);
    assert.deepEqual(ui.metricCounts, [5, 5, 5, 5, 5]);
    assert.equal(ui.detailsCount, 5);
    assert.equal(ui.openDetails, 0);
    assert.equal(ui.tableCount, 0);
    assert.equal(ui.buttonCount, 0);
    assert.equal(ui.documentOverflow, false);
    assert.equal(ui.cardOverflow, false);
    assert.equal(ui.economicHasFixture, true, 'Economic Objective must show the real API fixture ID');
    assert.equal(ui.approvalHasFixture, true, 'Approval Queue must show the real API task and target IDs');
    assert.equal(ui.advisorHasFixture, scenario === 'populated');
    assert.equal(ui.traceHasFixture, scenario === 'populated');
    assert.equal(ui.forbiddenTextVisible, false);
    assert.ok(ui.rects.every((item) => item.pointerEvents === 'auto'));
    assert.ok(ui.rects.every((item) => item.position === 'static'));
    if (viewport.width <= 640) {
      assert.equal(ui.rects.length, 5);
      assert.ok(ui.rects.every((item) => Math.abs(item.x - ui.rects[0].x) < 1));
    }

    for (const pathname of targetPaths) {
      const response = targetResponses.find((item) => item.path === pathname && item.method === 'GET');
      assert.ok(response, `browser did not request real API ${pathname}`);
      assert.equal(response.status, 200, `real API ${pathname} returned ${response.status}`);
    }
    const targetConsoleErrors = consoleErrors.filter((item) => {
      return item.url.includes('/tabs/to-be-ops.js') || targetPaths.some((pathname) => item.text.includes(pathname));
    });
    assert.deepEqual(pageErrors, []);
    assert.equal(targetConsoleErrors.length, 0, `To-Be console errors: ${JSON.stringify(targetConsoleErrors)}`);
    assert.equal(failedRequests.filter((line) => targetPaths.some((pathname) => line.includes(pathname))).length, 0);

    const label = `${viewport.width}x${viewport.height}`;
    await page.screenshot({path: path.join(artifactDir, `${label}.png`), fullPage: true});
    await page.locator('[data-to-be-block="economic-objective"]').screenshot({path: path.join(artifactDir, `${label}-economic-objective.png`)});
    for (const summary of await page.locator('#toBeOpsSummary details > summary').all()) {
      await summary.click();
    }
    const expanded = await page.evaluate(({opportunityID, taskID}) => {
      const block = document.querySelector('[data-to-be-block="approval-queue"]');
      const details = block?.querySelector('details');
      const text = details?.textContent || '';
      return {
        open: details?.open === true,
        openCount: document.querySelectorAll('#toBeOpsSummary details[open]').length,
        hasFixture: text.includes(opportunityID) && text.includes(taskID),
        documentOverflow: document.documentElement.scrollWidth > document.documentElement.clientWidth,
        blockOverflow: block ? block.scrollWidth > block.clientWidth + 1 : true,
      };
    }, fixture);
    assert.equal(expanded.open, true);
    assert.equal(expanded.openCount, 5);
    assert.equal(expanded.hasFixture, true);
    assert.equal(expanded.documentOverflow, false);
    assert.equal(expanded.blockOverflow, false);
    if (viewport.width <= 640) {
      const approvalCard = page.locator('[data-to-be-block="approval-queue"]');
      const traceCard = page.locator('[data-to-be-block="recent-trace"]');
      if (!(await approvalCard.locator('details').evaluate((element) => element.open))) await approvalCard.locator('details > summary').click();
      await approvalCard.evaluate((element) => element.scrollIntoView({block: 'center'}));
      await page.waitForTimeout(50);
      assert.equal(await approvalCard.evaluate((element) => {
        const input = document.querySelector('.input-bar');
        return !input || element.getBoundingClientRect().bottom <= input.getBoundingClientRect().top + 1;
      }), true, 'Approval details must be scrollable above the fixed input bar');
      await approvalCard.screenshot({path: path.join(artifactDir, `${label}-approval-open.png`)});
      if (!(await traceCard.locator('details').evaluate((element) => element.open))) await traceCard.locator('details > summary').click();
      await traceCard.evaluate((element) => element.scrollIntoView({block: 'center'}));
      await page.waitForTimeout(50);
      assert.equal(await traceCard.evaluate((element) => {
        const input = document.querySelector('.input-bar');
        return !input || element.getBoundingClientRect().bottom <= input.getBoundingClientRect().top + 1;
      }), true, 'Recent Trace details must be scrollable above the fixed input bar');
      await traceCard.screenshot({path: path.join(artifactDir, `${label}-recent-trace-open.png`)});
    } else {
      await page.locator('#toBeOpsSummary').screenshot({path: path.join(artifactDir, `${label}-all-details-open.png`)});
    }

    const responseCountsBeforeRefresh = Object.fromEntries(targetPaths.map((pathname) => [pathname, targetResponses.filter((item) => item.path === pathname && item.method === 'GET').length]));
    await page.evaluate(() => window.refreshToBeOpsData());
    await page.waitForFunction(() => document.getElementById('toBeOpsSummary')?.getAttribute('aria-busy') === 'false');
    for (const pathname of targetPaths) {
      const count = targetResponses.filter((item) => item.path === pathname && item.method === 'GET').length;
      assert.ok(count > responseCountsBeforeRefresh[pathname], `refresh did not request ${pathname} again`);
    }
    if (scenario === 'populated' && viewport.width >= 1000) {
      await page.reload({waitUntil: 'domcontentloaded'});
      await page.waitForFunction(() => document.getElementById('toBeOpsSummary')?.getAttribute('aria-busy') === 'false');
      assert.match(await page.locator('[data-to-be-block="economic-objective"]').textContent(), new RegExp(fixture.opportunityID));
    }
    return {label, ui, expanded, responseCountsBeforeRefresh, targetResponses, consoleErrors, targetConsoleErrors, pageErrors, failedRequests};
  } finally {
    await context.close();
  }
}

async function verifyFaultCase(browser, fault) {
  const context = await browser.newContext({viewport: {width: 1440, height: 900}});
  const page = await context.newPage();
  try {
    await page.route(`**${fault.path}*`, async (route) => {
      if (fault.kind === 'http500') {
        await route.fulfill({status: 500, contentType: 'application/json', body: '{"error":"synthetic fault"}'});
      } else if (fault.kind === 'malformed') {
        await route.fulfill({status: 200, contentType: 'application/json', body: '{not-json'});
      } else if (fault.kind === 'blocked') {
        await route.fulfill({status: 200, contentType: 'application/json', body: '{"status":"blocked","opportunities":[],"opportunity_count":0}'});
      } else if (fault.kind === 'timeout') {
        await new Promise((resolve) => setTimeout(resolve, 5500));
        await route.fulfill({status: 200, contentType: 'application/json', body: '{"status":"ok","items":[]}'}).catch(() => {});
      }
    });
    const startedAt = Date.now();
    await page.goto(`${baseURL}/viewer?tab=ops`, {waitUntil: 'domcontentloaded', timeout: 30000});
    await page.waitForFunction(() => document.getElementById('toBeOpsSummary')?.getAttribute('aria-busy') === 'false', null, {timeout: 12000});
    const elapsedMS = Date.now() - startedAt;
    const statuses = await page.locator('#toBeOpsSummary .ops-to-be-status').allTextContents();
    assert.equal(statuses[fault.blockIndex], fault.expectedStatus);
    assert.equal(await page.locator('#toBeOpsSummary [data-to-be-block]').count(), 5);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false);
    if (fault.kind === 'timeout') assert.ok(elapsedMS < 8000, `timeout fallback took ${elapsedMS}ms`);
    await page.locator('#toBeOpsSummary').screenshot({path: path.join(artifactDir, `fault-${fault.name}.png`)});
    return {name: fault.name, kind: fault.kind, path: fault.path, expectedStatus: fault.expectedStatus, statuses, elapsedMS, passed: true};
  } finally {
    await context.close();
  }
}

async function verifyFaults(browser) {
  const faults = [
    {name: 'advisor-http-500', kind: 'http500', path: '/viewer/advisors', blockIndex: 0, expectedStatus: 'unavailable'},
    {name: 'tasks-malformed-json', kind: 'malformed', path: '/viewer/revenue/economic-tasks', blockIndex: 2, expectedStatus: 'unavailable'},
    {name: 'opportunity-blocked', kind: 'blocked', path: '/viewer/revenue/opportunities', blockIndex: 2, expectedStatus: 'blocked'},
    {name: 'trace-timeout', kind: 'timeout', path: '/viewer/recall/traces', blockIndex: 4, expectedStatus: 'unavailable'},
  ];
  const results = [];
  for (const fault of faults) results.push(await verifyFaultCase(browser, fault));
  return results;
}

async function main() {
  fs.mkdirSync(artifactDir, {recursive: true});
  const runID = Date.now().toString(36);
  const report = {ok: false, baseURL, browser: browserName, scenario, runID, viewports: [], faultCases: [], stories: []};
  let browser;
  try {
    const fixture = await seedEconomicRecords(runID);
    report.fixture = fixture;
    browser = await browserType.launch({headless});
    for (const viewport of [{width: 1440, height: 900}, {width: 390, height: 844}]) {
      report.viewports.push(await verifyViewport(browser, viewport, fixture));
    }
    if (runFaultMatrix) report.faultCases = await verifyFaults(browser);
    report.stories = [
      {story_id: 'TOBE-E2E-001', area: 'network', feature: 'real-api', status: 'test_passed', evidence: `${scenario}/${browserName}: seven real GET endpoints returned 200`},
      {story_id: 'TOBE-E2E-002', area: 'viewer', feature: 'summary', status: 'test_passed', evidence: `${scenario}/${browserName}: five blocks and five metrics each`},
      {story_id: 'TOBE-E2E-003', area: 'viewer', feature: 'details', status: 'test_passed', evidence: `${scenario}/${browserName}: all five details opened`},
      {story_id: 'TOBE-E2E-004', area: 'responsive', feature: 'desktop-mobile', status: 'test_passed', evidence: `${scenario}/${browserName}: 1440x900 and 390x844 without overflow`},
      {story_id: scenario === 'populated' ? 'TOBE-E2E-005' : 'TOBE-E2E-006', area: 'data', feature: scenario, status: 'test_passed', evidence: `${scenario}/${browserName}: expected status and fixture matrix`},
      {story_id: 'TOBE-E2E-007', area: 'refresh', feature: 'repeat-fetch-reload', status: 'test_passed', evidence: `${scenario}/${browserName}: manual refresh${scenario === 'populated' ? ' and reload persistence' : ''}`},
      {story_id: 'TOBE-E2E-008', area: 'security', feature: 'safe-projection', status: 'test_passed', evidence: `${scenario}/${browserName}: no raw prompt/output/secret text`},
    ];
    if (runFaultMatrix) report.stories.push({story_id: 'TOBE-E2E-009', area: 'resilience', feature: 'fault-matrix', status: 'test_passed', evidence: `${scenario}/${browserName}: 500, malformed JSON, blocked, timeout`});
    report.ok = true;
  } catch (error) {
    report.error = error?.stack || String(error);
  } finally {
    if (browser) await browser.close();
    fs.writeFileSync(path.join(artifactDir, 'report.json'), `${JSON.stringify(report, null, 2)}\n`);
  }
  process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
  if (!report.ok) process.exitCode = 1;
}

await main();
