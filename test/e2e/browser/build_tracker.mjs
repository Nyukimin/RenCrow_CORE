#!/usr/bin/env node

import fs from 'node:fs';
import path from 'node:path';
import process from 'node:process';

const runDir = path.resolve(process.env.RENCROW_E2E_RUN_DIR || '');
const browserRoot = path.join(runDir, 'browser');
const expectedReports = Number(process.env.RENCROW_E2E_EXPECTED_REPORTS || 0);
const reportPaths = [];

function walk(dir) {
  if (!fs.existsSync(dir)) return;
  for (const entry of fs.readdirSync(dir, {withFileTypes: true})) {
    const target = path.join(dir, entry.name);
    if (entry.isDirectory()) walk(target);
    else if (entry.name === 'report.json') reportPaths.push(target);
  }
}

walk(browserRoot);
const reports = reportPaths.map((file) => ({file: path.relative(runDir, file), ...JSON.parse(fs.readFileSync(file, 'utf8'))}));
const storyMap = new Map();
for (const report of reports) {
  for (const story of report.stories || []) {
    const current = storyMap.get(story.story_id) || {
      story_id: story.story_id,
      area: story.area,
      feature: story.feature,
      user_story: `運用者は ${story.feature} を実ブラウザで確認できる。`,
      expected_behavior: story.evidence,
      entry_point: '/viewer?tab=ops',
      test_steps: 'isolated real server, real API/store, browser assertions',
      evidence: [],
      status: 'test_passed',
      severity: 'low',
      error_type: '',
      error_summary: '',
      fix_commit: '',
      retest_status: 'test_passed',
      owner: 'RenCrow_CORE',
      notes: '',
    };
    current.evidence.push(`${report.file}: ${story.evidence}`);
    if (!report.ok || story.status !== 'test_passed') {
      current.status = 'test_failed';
      current.retest_status = 'test_failed';
      current.severity = 'high';
      current.error_type = 'implementation';
      current.error_summary = report.error || 'browser assertion failed';
    }
    storyMap.set(story.story_id, current);
  }
}

const reportCountOK = expectedReports === 0 || reports.length === expectedReports;
const allReportsOK = reportCountOK && reports.every((report) => report.ok);
const stories = [...storyMap.values()].sort((a, b) => a.story_id.localeCompare(b.story_id));
const tracker = {
  scope: 'affected_area: Viewer To-Be Ops',
  generated_at: new Date().toISOString(),
  run_dir: runDir,
  expected_reports: expectedReports,
  report_count: reports.length,
  reports: reports.map(({file, ok, scenario, browser, error}) => ({file, ok, scenario, browser, error: error || ''})),
  total_stories: stories.length,
  passed: stories.filter((story) => story.status === 'test_passed').length,
  failed: stories.filter((story) => story.status === 'test_failed').length,
  blocked: stories.filter((story) => story.status === 'blocked').length,
  deferred: stories.filter((story) => story.status === 'deferred').length,
  critical_remaining: 0,
  high_remaining: stories.filter((story) => story.status === 'test_failed' && story.severity === 'high').length,
  launch_recommendation: allReportsOK ? 'go' : 'no_go',
  stories,
};
fs.writeFileSync(path.join(runDir, 'tracker.json'), `${JSON.stringify(tracker, null, 2)}\n`);
process.stdout.write(`${JSON.stringify({ok: allReportsOK, tracker: path.join(runDir, 'tracker.json'), reports: reports.length, stories: stories.length})}\n`);
if (!allReportsOK) process.exitCode = 1;
