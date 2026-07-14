import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';
import {createRequire} from 'node:module';

const require = createRequire(import.meta.url);

test('To-Be Ops renders exactly five safe summary blocks', () => {
  const {buildToBeOpsViewModel, renderToBeOpsHTML} = require('./assets/js/tabs/to-be-ops.js');
  const model = buildToBeOpsViewModel({
    advisor: {
      status: 'ok',
      summary: {advisor_count: 1, recent_run_count: 1, failed_run_count: 0, score_snapshot_count: 1, profile_count: 8, policy_decision_count: 1},
      recent_runs: [{run_id: 'run-with-a-very-long-identifier-1234567890', advisor_id: 'codex', status: 'completed', raw_output: 'DO_NOT_RENDER_RAW_OUTPUT'}],
      policy_decisions: [{decision_id: 'policy-1', agent_id: 'shiro', action: 'external_publish', decision: 'approval_required', reason: 'DO_NOT_RENDER_POLICY_REASON'}],
    },
    knowledge: {enabled: false, status: 'unavailable', warnings: ['disabled by config'], summary: {entity_count: 0, relation_count: 0, max_hop: 2, last_build_status: 'not_run'}},
    opportunities: {status: 'blocked', opportunities: [{opportunity_id: 'opp-1', title: 'Draft'}], opportunity_count: 1},
    tasks: {status: 'ok', economic_tasks: [{task_id: 'task-1', opportunity_id: 'opp-1', task_kind: 'billing', status: 'draft', approval_mode: 'human_required'}], task_count: 1},
    reflections: {status: 'ok', economic_reflections: [{reflection_id: 'reflection-1', opportunity_id: 'opp-1', outcome: 'drafted'}], reflection_count: 1},
    revenue: {
      economic_objective: {enabled: false, draft_only: true, external_action_blocked: true},
      human_decisions: [{decision_id: 'decision-1', decision_type: 'billing', subject_id: 'opp-1', approval_status: 'pending', gate_status: 'needs_review', description: 'DO_NOT_RENDER_DESCRIPTION'}],
    },
    traces: {items: [{ResponseID: 'response-1', Role: 'mio', Items: [{Kind: 'knowledge_relation', SourceID: 'source-1', Status: 'injected', Summary: 'DO_NOT_RENDER_TRACE_SUMMARY', PromptSection: 'knowledge'}]}]},
    errors: {},
  });

  assert.equal(model.length, 5);
  assert.deepEqual(model.map((block) => block.key), ['advisor-agent', 'knowledge-relation', 'economic-objective', 'approval-queue', 'recent-trace']);
  assert.ok(model.every((block) => ['ok', 'warning', 'blocked', 'unavailable'].includes(block.status)));
  assert.notEqual(model[1].status, 'blocked', 'enabled=false must not be rendered as a red blocked error');
  assert.equal(model[2].status, 'warning', 'disabled Economic Objective must be neutral warning, not green or red');

  const html = renderToBeOpsHTML(model);
  assert.equal((html.match(/data-to-be-block=/g) || []).length, 5);
  assert.equal((html.match(/<details/g) || []).length, 5);
  assert.doesNotMatch(html, /<details[^>]*\sopen/);
  assert.doesNotMatch(html, /<table|<button/i);
  assert.doesNotMatch(html, /DO_NOT_RENDER_RAW_OUTPUT|DO_NOT_RENDER_POLICY_REASON|DO_NOT_RENDER_DESCRIPTION|DO_NOT_RENDER_TRACE_SUMMARY/);
  assert.match(html, /run-with-a-very-long-identifier-1234567890/);
  assert.match(html, /task task-1 · target opp-1 · billing · human_required/);
  assert.match(html, /decision decision-1 · target opp-1 · billing · pending/);
});

test('To-Be Ops is wired into the existing Ops tab and mobile layout', () => {
  const html = fs.readFileSync('internal/adapter/viewer/viewer.html', 'utf8');
  const viewer = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const css = fs.readFileSync('internal/adapter/viewer/assets/css/tabs/ops.css', 'utf8');

  assert.match(html, /id="toBeOpsSummary"/);
  assert.match(html, /To-Be Summary/);
  assert.match(html, /to-be-ops\.js/);
  assert.doesNotMatch(html, /data-tab="to-be"/);
  assert.match(viewer, /refreshToBeOpsData/);
  assert.match(css, /\.ops-to-be-grid/);
  assert.match(css, /overflow-wrap:anywhere/);
  assert.match(css, /@media \(max-width: 640px\)[\s\S]*\.ops-to-be-grid\{grid-template-columns:minmax\(0,1fr\)\}/);
});
