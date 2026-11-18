import assert from 'node:assert/strict';
import {mkdirSync, readFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {test} from 'node:test';
import {fileURLToPath} from 'node:url';
import path from 'node:path';
import {chromium} from 'playwright';

const viewerDir = path.dirname(fileURLToPath(import.meta.url));
const pageHTML = readFileSync(path.join(viewerDir, 'assets/task-center.html'), 'utf8');
const pageCSS = readFileSync(path.join(viewerDir, 'assets/css/tabs/task-center.css'), 'utf8');
const pageJS = readFileSync(path.join(viewerDir, 'assets/js/tabs/task-center.js'), 'utf8');
const mioImage = readFileSync(path.join(viewerDir, 'assets/images/mio/mio_basic.png'));
const TEST_ORIGIN = 'http://task-center.test';
const TEST_TOKEN = 'synthetic-operator-token-for-browser-test';
const RESULT_BODY = {summary: '<img src=x onerror=window.taskResultInjected=true>', completed: ['確認項目を照合しました']};
const RESULT_SHA256 = createHash('sha256').update(JSON.stringify(RESULT_BODY)).digest('hex');

function item(overrides = {}) {
  return {
    goal_id: 'goal-demo-1',
    title: '<img src=x onerror=window.taskCenterInjected=true>',
    description: '表示は文字列として安全に描画します。',
    kind_id: 'movie.catalog.lookup',
    assignee: 'midori',
    status: 'draft',
    revision: 4,
    assignment_epoch: 2,
    timing: {
      timezone: 'Asia/Tokyo',
      scheduled_start_at: null,
      not_before: null,
      allowed_windows: [],
      deadline_at: null,
      recurrence: null,
      estimated_duration_seconds: null,
    },
    timing_status: 'known',
    timing_source: 'workstream.goal',
    allowed_agents: ['mio'],
    reason_code: 'WAITING_FOR_INPUT',
    workstream_id: 'workstream-demo-1',
    created_at: '2026-11-16T01:00:00Z',
    updated_at: '2026-11-16T02:00:00Z',
    ...overrides,
  };
}

function resultItem(overrides = {}) {
  return item({
    title: 'カタログ照会結果を確認する',
    status: 'active',
    revision: 7,
    assignment_epoch: 3,
    success_criteria: ['照会結果が正しい対象に対応する'],
    verification: ['保存された成果物の内容を読む'],
    execution_verification: 'verification_pending',
    result_artifact_id: 'artifact-result-1',
    result_sha256: RESULT_SHA256,
    can_accept_result: true,
    ...overrides,
  });
}

function resultView(goal, overrides = {}) {
  return {
    goal_id: goal.goal_id,
    revision: goal.revision,
    assignment_epoch: goal.assignment_epoch,
    result_artifact_id: goal.result_artifact_id,
    result_sha256: goal.result_sha256,
    execution_verification: goal.execution_verification,
    result: RESULT_BODY,
    ...overrides,
  };
}

function acceptedMutation(goal, overrides = {}) {
  return {
    item: item({
      ...goal,
      revision: goal.revision + 1,
      status: 'completed',
      execution_verification: 'accepted',
      can_accept_result: false,
    }),
    replayed: false,
    receipt_ref: 'receipt-result-accept-1',
    ...overrides,
  };
}

function lookupInputSchema() {
  return {
    schema_id: 'movie.catalog.lookup',
    version: 1,
    fields: [
      {
        name: 'kind', label: '対象', help: '映画または人物を選びます。', type: 'enum', required: true,
        options: [{value: 'movie', label: '映画'}, {value: 'person', label: '人物'}],
      },
      {name: 'name', label: '名前', help: '既存カタログから完全一致で探します。', type: 'string', required: true},
      {
        name: 'information', label: '表示情報', help: '対象種別に合う情報を追加で読みます。', type: 'enum_by_kind', required: false,
        options_by_kind: {
          movie: [{value: '', label: '基本情報'}, {value: 'overview', label: '概要'}, {value: 'cast', label: '出演者'}, {value: 'staff', label: 'スタッフ'}],
          person: [{value: '', label: '基本情報'}, {value: 'profile', label: 'プロフィール'}, {value: 'filmography', label: '出演作品'}],
        },
      },
      {name: 'limit', label: '上限', help: '候補と関連情報の最大件数です。', type: 'integer', required: false, default: 10, minimum: 1, maximum: 20},
    ],
  };
}

function capabilities(overrides = {}) {
  return {
    kinds: [{
      kind_id: 'movie.catalog.lookup', label: '映画カタログ照会', allowed_agents: ['mio'],
      input_schema: lookupInputSchema(), requires_typed_input: true, executor_available: true,
    }],
    agents: ['mio'],
    policy_revision: 'task-policy-r7',
    owner_ref: 'workstream',
    revision: 'capabilities-r3',
    can_claim: false,
    actor_kind: 'user',
    managed_available: true,
    daily_available: true,
    can_submit: true,
    managed_reason_code: '',
    daily_reason_code: '',
    ...overrides,
  };
}

function list(items = [], overrides = {}) {
  return {items, partial: false, policy_revision: 'task-policy-r7', ...overrides};
}

function dailyValue(state, value = null, sourceRef = 'owner.daily.scheduler', reasonCode = '') {
  return {
    state,
    source_ref: sourceRef,
    ...(reasonCode ? {reason_code: reasonCode} : {}),
    value,
  };
}

function dailyAxis(state, sourceRef = 'owner.daily.scheduler', reasonCode = '') {
  return {state, source_ref: sourceRef, ...(reasonCode ? {reason_code: reasonCode} : {})};
}

function dailyRef(overrides = {}) {
  return {
    source_id: 'daily-mail-source',
    owner_ref: 'owner.daily-mail',
    resource_type: 'configuration',
    reference: '/Users/private/mail/raw.eml',
    native_id: 'mailbox-digest-config-1',
    data_scope: 'user',
    revision: 'rev-8',
    display_name: '朝の要約設定',
    filename: 'daily-digest.yaml',
    link_ref: 'owners/daily-mail/digest/schedule',
    ...overrides,
  };
}

function dailyDefinition(overrides = {}) {
  const timestamp = '2026-11-17T08:00:00+09:00';
  const ref = dailyRef();
  return {
    source_id: 'daily-mail-source',
    definition_id: 'daily-definition-mail-digest',
    kind_id: 'daily.mail.digest',
    label: '朝のメール要約',
    owner_ref: 'owner.daily-mail',
    resource_type: 'daily_definition',
    data_scope: 'user',
    implementation: dailyAxis('implemented'),
    configured: dailyAxis('configured'),
    effective: dailyAxis('enabled'),
    observed: dailyAxis('available'),
    source_observed_at: dailyValue('known', timestamp),
    timing: {
      timezone: dailyValue('known', 'Asia/Tokyo'),
      scheduled_start_at: dailyValue('none'),
      not_before: dailyValue('unknown', null, 'owner.daily.scheduler', 'observation_missing'),
      allowed_windows: dailyValue('known', []),
      deadline_at: dailyValue('unsupported', null, 'owner.daily.scheduler', 'not_supported'),
      recurrence: dailyValue('known', {frequency: 'daily', times: ['08:00'], weekdays: [], interval_seconds: null}),
      estimated_duration_seconds: dailyValue('known', 0),
    },
    next_run_at: dailyValue('known', timestamp),
    target_count: dailyValue('known', 0),
    source_refs: [ref],
    native_refs: [ref],
    native_ref_count: dailyValue('known', 1),
    native_refs_truncated: false,
    latest_evidence: {
      state: 'known',
      last_attempt_at: dailyValue('known', timestamp),
      last_success_at: dailyValue('none'),
      last_result: dailyValue('known', 'failure'),
      refs: [ref],
    },
    occurrences: [{
      occurrence_id: 'mail-digest-occurrence-20261117',
      state: 'failed',
      scheduled_at: dailyValue('known', '2026-11-16T08:00:00+09:00', 'owner.daily.scheduler.slot'),
      next_run_at: dailyValue('none'),
      last_attempt_at: dailyValue('known', timestamp),
      last_success_at: dailyValue('unknown', null, 'owner.daily.history', 'retention_unknown'),
      result: dailyValue('known', 'failure'),
      native_refs: [ref],
    }, {
      occurrence_id: 'legacy-occurrence-without-scheduled-slot',
      state: 'unknown',
      next_run_at: dailyValue('unknown'),
      last_attempt_at: dailyValue('unknown'),
      last_success_at: dailyValue('unknown'),
      result: dailyValue('unknown'),
      native_refs: [],
    }],
    occurrence_count: dailyValue('known', 1),
    occurrences_truncated: false,
    issues: [],
    ...overrides,
  };
}

function dailyList(definitions = [], overrides = {}) {
  return {
    definitions,
    partial: false,
    availability: 'available',
    issues: [],
    observed_at: '2026-11-17T08:01:00+09:00',
    ...overrides,
  };
}

async function openBrowser(t, onAPI) {
  const browser = await chromium.launch({headless: true});
  t.after(async () => browser.close());
  const context = await browser.newContext({viewport: {width: 1366, height: 900}});
  t.after(async () => context.close());
  const page = await context.newPage();
  const apiRequests = [];
  const pageErrors = [];
  page.on('pageerror', (error) => pageErrors.push(error.message));
  await page.route(`${TEST_ORIGIN}/**`, async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.pathname === '/viewer/task-center') {
      await route.fulfill({status: 200, contentType: 'text/html; charset=utf-8', body: pageHTML});
      return;
    }
    if (url.pathname === '/viewer/assets/css/tabs/task-center.css') {
      await route.fulfill({status: 200, contentType: 'text/css; charset=utf-8', body: pageCSS});
      return;
    }
    if (url.pathname === '/viewer/assets/js/tabs/task-center.js') {
      await route.fulfill({status: 200, contentType: 'text/javascript; charset=utf-8', body: pageJS});
      return;
    }
    if (url.pathname === '/viewer/assets/images/mio/mio_basic.png') {
      await route.fulfill({status: 200, contentType: 'image/png', body: mioImage});
      return;
    }
    if (url.pathname.startsWith('/api/v1/task-center/')) {
      const record = {
        method: request.method(),
        url: request.url(),
        headers: request.headers(),
        body: request.postData() ? JSON.parse(request.postData()) : null,
      };
      apiRequests.push(record);
      await onAPI(route, record, apiRequests.length);
      return;
    }
    await route.fulfill({status: 404, body: 'not found'});
  });
  await page.goto(`${TEST_ORIGIN}/viewer/task-center`);
  await page.waitForFunction(() => document.readyState === 'complete');
  return {page, apiRequests, pageErrors};
}

function json(route, payload, status = 200) {
  return route.fulfill({status, contentType: 'application/json', body: JSON.stringify(payload)});
}

async function connect(page) {
  await page.locator('#taskCenterToken').fill(TEST_TOKEN);
  await page.locator('#taskCenterConnectButton').click();
  await page.locator('#taskCenterWorkspace').waitFor({state: 'visible'});
  await page.locator('#taskCenterCapabilityState.is-ready').waitFor({state: 'visible'});
}

async function fillRequiredTaskFields(page, {title = ''} = {}) {
  await page.locator('#taskCenterAddDetails > summary').click();
  await page.locator('#taskCenterKind').selectOption('movie.catalog.lookup');
  await page.locator('[data-kind-input-name="kind"]').selectOption('movie');
  await page.locator('[data-kind-input-name="name"]').fill('星を追う子ども');
  await page.locator('[data-kind-input-name="information"]').selectOption('overview');
  await page.locator('[data-kind-input-name="limit"]').fill('8');
  if (title) await page.locator('#taskCenterTitleInput').fill(title);
  await page.locator('#taskCenterDescription').fill('第一章の草稿を作り、資料と照合してください。');
  await page.locator('#taskCenterCriteria').fill('草稿ファイルを作成する\n資料の引用箇所を確認する');
  await page.locator('#taskCenterVerification').fill('成果物のpathを確認する\n引用箇所を原資料と照合する');
}

test('native Goal states have meaningful Japanese labels; unknown states remain unconfirmed', async (t) => {
  const nativeStates = [
    ['draft', '下書き'],
    ['active', '進行中'],
    ['paused', '一時停止'],
    ['waiting', '待機中'],
    ['completed', '完了'],
    ['archived', 'アーカイブ済み'],
    ['running', '状態未確認（running）'],
    ['future_state', '状態未確認（future_state）'],
  ];
  const goals = nativeStates.map(([status], index) => item({
    goal_id: `goal-status-${index + 1}`,
    status,
    assignee: status === 'waiting' ? 'unassigned' : 'mio',
  }));
  const {page, pageErrors} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) await json(route, capabilities());
    else if (record.method === 'GET' && url.pathname.endsWith('/items')) await json(route, list(goals));
    else await json(route, {error_code: 'NOT_FOUND'}, 404);
  });

  await connect(page);
  const cards = page.locator('.task-center__item');
  await page.waitForFunction(() => document.querySelectorAll('.task-center__item').length === 8);
  for (let index = 0; index < nativeStates.length; index += 1) {
    assert.equal(await cards.nth(index).locator('.task-center__item-status').textContent(), nativeStates[index][1]);
  }
  assert.equal(await cards.nth(1).locator('.task-center__item-status').getAttribute('class'), 'task-center__item-status');
  assert.match(await cards.nth(3).locator('.task-center__item-status').getAttribute('class'), /--warning/);
  assert.match(await cards.nth(4).locator('.task-center__item-status').getAttribute('class'), /--success/);
  assert.match(await cards.nth(3).locator('.task-center__item-meta').textContent(), /担当 未割当/);
  await cards.nth(3).locator('summary').click();
  assert.match(await cards.nth(3).locator('.task-center__detail-grid').textContent(), /担当未割当/);
  assert.deepEqual(pageErrors, []);
});

test('managed-unavailable mode defaults to a typed read-only Daily view without Goal detail calls', async (t) => {
  const {page, apiRequests, pageErrors} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) {
      await json(route, capabilities({
        kinds: [], agents: [], policy_revision: '', can_claim: false, actor_kind: 'user',
        managed_available: false, daily_available: true, can_submit: false,
        managed_reason_code: 'managed_owner_unavailable', daily_reason_code: '',
      }));
    } else if (record.method === 'GET' && url.pathname.endsWith('/items')) {
      assert.equal(url.searchParams.get('view'), 'daily');
      assert.equal(url.searchParams.has('assignee'), false, 'Daily filtering is not a Goal assignee filter');
      await json(route, dailyList([dailyDefinition({
        source_refs: [dailyRef({display_name: 'owner@example.test', filename: '/Users/private/mail/raw.eml', link_ref: 'javascript:alert(1)'})],
      })]));
    } else {
      await json(route, {error_code: 'NOT_FOUND'}, 404);
    }
  });

  await connect(page);
  assert.equal(await page.locator('#taskCenterViewDaily').getAttribute('aria-selected'), 'true');
  assert.equal(await page.locator('#taskCenterViewRemaining').isDisabled(), true);
  assert.equal(await page.locator('#taskCenterViewClaimable').isDisabled(), true);
  assert.equal(await page.locator('#taskCenterViewHistory').isDisabled(), true);
  assert.equal(await page.locator('#taskCenterAddDetails').isVisible(), false);
  assert.equal(await page.locator('#taskCenterAssigneeField').isVisible(), false);
  assert.equal(await page.locator('#taskCenterDailyKindField').isVisible(), true);
  assert.match(await page.locator('#taskCenterModeNotice').textContent(), /読み取り専用.*Goal管理ownerが利用できません/);
  const filteredResponsePromise = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return url.pathname.endsWith('/items') && url.searchParams.get('kind_id') === 'daily.mail.digest';
  });
  await page.locator('#taskCenterDailyKind').selectOption('daily.mail.digest');
  const filteredResponse = await filteredResponsePromise;
  assert.equal(filteredResponse.status(), 200);
  assert.equal(new URL(filteredResponse.url()).searchParams.get('kind_id'), 'daily.mail.digest');
  assert.equal(new URL(filteredResponse.url()).searchParams.has('assignee'), false);
  assert.equal(await page.locator('.task-center__daily-definition').count(), 1);
  const card = page.locator('.task-center__daily-definition');
  assert.equal(await card.locator('.task-center__daily-axis').count(), 4);
  assert.match(await card.locator('.task-center__daily-axes').textContent(), /実装: 実装済み.*設定: 設定あり.*有効状態: 設定上有効.*観測: 観測データあり/);
  assert.match(await card.locator('.task-center__daily-meta').textContent(), /次回予定: .*2026[\/年]11[\/月]17.*8:00/);
  assert.match(await card.locator('.task-center__daily-meta').textContent(), /直近結果: 失敗記録/);
  assert.doesNotMatch(await card.textContent(), /稼働中|設定上有効.*成功/);
  assert.doesNotMatch(await card.textContent(), /owner@example\.test|Users\/private|javascript:|raw\.eml/);
  assert.equal(await card.locator('a').count(), 0, 'owner-supplied metadata must not become a URL');
  await card.locator('summary').click();
  const detailText = await card.locator('.task-center__daily-detail').textContent();
  for (const label of ['タイムゾーン', '定義上の開始予定', '開始条件', '許可時間帯', '定義上の期限', '繰り返し定義', '想定所要時間']) {
    assert.ok(detailText.includes(label), `Daily timing detail ${label} is visible`);
  }
  assert.match(detailText, /0件（knownの空配列）/);
  assert.match(detailText, /対象件数0件（known）/);
  assert.match(detailText, /想定所要時間0秒（known）/);
  assert.match(detailText, /毎日.*時刻 08:00/);
  assert.match(detailText, /none（明示的な指定なし）/);
  assert.match(detailText, /unknown（未確認）/);
  assert.match(detailText, /source: owner\.daily\.scheduler/);
  assert.match(detailText, /daily-digest\.yaml/);
  assert.match(detailText, /owners\/daily-mail\/digest\/schedule/);
  assert.match(detailText, /実在するnative occurrence/);
  assert.match(detailText, /occurrence ID/);
  const occurrenceDetails = card.locator('.task-center__daily-occurrence');
  assert.equal(await occurrenceDetails.count(), 2);
  const scheduledSlot = (index) => occurrenceDetails.nth(index).locator('.task-center__detail').filter({hasText: '開催予定時刻'}).locator('dd');
  assert.match(await scheduledSlot(0).textContent(), /2026[\/年]11[\/月]16.*8:00/);
  assert.equal(await scheduledSlot(1).textContent(), '未確認（この応答では未報告）');
  assert.doesNotMatch(detailText, /Users\/private|raw\.eml|owner@example\.test|javascript:alert/);
  assert.equal(apiRequests.filter((request) => new URL(request.url).pathname.endsWith('/item')).length, 0);
  assert.equal(apiRequests.some((request) => request.method === 'POST'), false);
  assert.equal(await page.evaluate(() => [localStorage.length, sessionStorage.length].join(',')), '0,0');
  assert.deepEqual(pageErrors, []);
});

test('Daily partial provider snapshots retain offline definitions and unknown timing states', async (t) => {
  const offline = dailyDefinition({
    definition_id: 'daily-definition-offline-source',
    kind_id: 'daily.calendar.digest',
    label: '会議予定の整理',
    implementation: dailyAxis('unimplemented', 'owner.daily.calendar'),
    configured: dailyAxis('disabled', 'owner.daily.calendar'),
    effective: dailyAxis('unavailable', 'owner.daily.calendar', 'source_unavailable'),
    observed: dailyAxis('unavailable', 'owner.daily.calendar', 'source_unavailable'),
    source_observed_at: dailyValue('unknown', null, 'owner.daily.calendar', 'observation_missing'),
    timing: {
      timezone: dailyValue('unknown'),
      scheduled_start_at: dailyValue('unknown'),
      not_before: dailyValue('unsupported', null, 'owner.daily.calendar', 'unsupported'),
      allowed_windows: dailyValue('unknown'),
      deadline_at: dailyValue('none'),
      recurrence: dailyValue('unknown'),
      estimated_duration_seconds: dailyValue('unknown'),
    },
    next_run_at: dailyValue('unknown', null, 'owner.daily.calendar', 'observation_missing'),
    target_count: dailyValue('unknown', null, 'owner.daily.calendar', 'observation_missing'),
    latest_evidence: {
      state: 'unknown',
      last_attempt_at: dailyValue('unknown'),
      last_success_at: dailyValue('unknown'),
      last_result: dailyValue('unknown'),
      refs: [],
    },
    occurrences: [],
    occurrence_count: dailyValue('unknown'),
    native_refs: [],
    native_ref_count: dailyValue('unknown'),
    issues: [{source_id: 'calendar-source', owner_ref: 'owner.daily.calendar', code: 'source_unavailable'}],
  });
  const {page, apiRequests, pageErrors} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) {
      await json(route, capabilities({
        kinds: [], agents: [], policy_revision: '', managed_available: false,
        daily_available: true, can_submit: false, managed_reason_code: 'managed_owner_unavailable',
      }));
    } else if (record.method === 'GET' && url.pathname.endsWith('/items')) {
      await json(route, dailyList([dailyDefinition(), offline], {
        partial: true,
        availability: 'partial',
        issues: [{source_id: 'calendar-source', owner_ref: 'owner.daily.calendar', code: 'source_unavailable'}],
      }));
    } else {
      await json(route, {error_code: 'NOT_FOUND'}, 404);
    }
  });

  await connect(page);
  assert.equal(await page.locator('.task-center__daily-definition').count(), 2, await page.locator('#taskCenterListState').textContent());
  assert.equal(await page.locator('#taskCenterSummaryState').textContent(), '部分取得');
  assert.match(await page.locator('#taskCenterListState').textContent(), /部分取得.*2件/);
  assert.match(await page.locator('#taskCenterItems').textContent(), /全体のsource確認事項/);
  const offlineCard = page.locator('.task-center__daily-definition').nth(1);
  assert.match(await offlineCard.locator('.task-center__daily-axes').textContent(), /実装: 未実装.*設定: 設定で無効.*有効状態: 利用不能.*観測: 観測不能/);
  await offlineCard.locator('summary').click();
  const detailText = await offlineCard.locator('.task-center__daily-detail').textContent();
  assert.match(detailText, /対象件数unknown（未確認）/);
  assert.match(detailText, /source観測時刻unknown（未確認）/);
  assert.match(detailText, /owner次回予定unknown（未確認）/);
  assert.doesNotMatch(detailText, /対象件数0件|source観測時刻未実施/);
  assert.match(detailText, /source_unavailable/);
  const dailyRequest = apiRequests.find((request) => new URL(request.url).pathname.endsWith('/items'));
  assert.equal(new URL(dailyRequest.url).searchParams.has('assignee'), false);
  assert.equal(apiRequests.filter((request) => new URL(request.url).pathname.endsWith('/item')).length, 0);
  assert.deepEqual(pageErrors, []);
});

test('meeting Daily details show source-reported checkpoint progress and reference-only minutes', async (t) => {
  const count = (value, source, state = 'known') => dailyValue(state, state === 'known' ? value : null, source, state === 'known' ? '' : 'not_observed');
  const progress = {
    attendance: {current: count(5, 'workstream.meeting.attendance'), total: count(8, 'workstream.meeting.roster')},
    delivered_reports: {current: count(4, 'workstream.meeting.delivered_reports'), total: count(8, 'workstream.meeting.roster')},
    answered_concerns: {current: count(1, 'workstream.meeting.answered_concerns'), total: count(2, 'workstream.meeting.reported_concerns')},
    unresolved_concerns: count(1, 'workstream.meeting.unresolved_concerns'),
    minutes: dailyValue('known', {
      owner_ref: 'application.taskmeeting', resource_type: 'meeting_minutes',
      reference: 'workstream.taskmeeting.minutes', native_id: 'artifact-minutes-20261118', data_scope: 'user',
      display_name: 'meeting minutes',
    }, 'workstream.taskmeeting.minutes'),
  };
  const unknownProgress = {
    attendance: {current: count(null, 'workstream.meeting.attendance', 'unknown'), total: count(8, 'workstream.meeting.roster')},
    delivered_reports: {current: count(0, 'workstream.meeting.delivered_reports'), total: count(8, 'workstream.meeting.roster')},
    answered_concerns: {current: count(null, 'workstream.meeting.answered_concerns', 'unknown'), total: count(null, 'workstream.meeting.reported_concerns', 'unknown')},
    unresolved_concerns: count(null, 'workstream.meeting.unresolved_concerns', 'unknown'),
    minutes: dailyValue('none', null, 'workstream.taskmeeting.minutes', 'not_persisted'),
  };
  const meetingOccurrence = (id, meetingProgress, includeProgress = true) => ({
    occurrence_id: id,
    state: 'running',
    scheduled_at: dailyValue('known', '2026-11-18T08:00:00+09:00', 'workstream.taskmeeting.scheduled_at'),
    next_run_at: dailyValue('unsupported', null, 'workstream.taskmeeting.next_run_at', 'single_slot'),
    last_attempt_at: dailyValue('unknown', null, 'workstream.taskmeeting.last_attempt_at', 'not_exposed'),
    last_success_at: dailyValue('unknown', null, 'workstream.taskmeeting.last_success_at', 'not_completed'),
    result: dailyValue('known', 'running', 'workstream.taskmeeting.status'),
    native_refs: [],
    ...(includeProgress ? {meeting_progress: meetingProgress} : {}),
  });
  const definition = dailyDefinition({
    kind_id: 'meeting.daily_report',
    label: '日次会議',
    resource_type: 'meeting_definition',
    occurrences: [
      meetingOccurrence('schedule-1:2026-11-18:08-00:Asia/Tokyo', progress),
      meetingOccurrence('schedule-1:2026-11-17:17-00:Asia/Tokyo', unknownProgress),
      meetingOccurrence('legacy-meeting-occurrence', null, false),
    ],
    occurrence_count: dailyValue('known', 3, 'workstream.taskmeeting.occurrences'),
  });
  const {page, pageErrors} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) await json(route, capabilities());
    else if (record.method === 'GET' && url.pathname.endsWith('/items')) {
      if (url.searchParams.get('view') === 'daily') {
        assert.equal(url.searchParams.has('assignee'), false);
        await json(route, dailyList([definition]));
      } else {
        await json(route, list());
      }
    } else await json(route, {error_code: 'NOT_FOUND'}, 404);
  });

  await connect(page);
  const dailyResponse = page.waitForResponse((response) => new URL(response.url()).searchParams.get('view') === 'daily');
  await page.locator('#taskCenterViewDaily').click();
  assert.equal((await dailyResponse).status(), 200);
  const card = page.locator('.task-center__daily-definition');
  await card.locator('summary').click();
  const progressSections = card.locator('.task-center__meeting-progress');
  assert.equal(await progressSections.count(), 3);
  const known = await progressSections.nth(0).textContent();
  assert.match(known, /出席確認（参加者）5 \/ 8（known）/);
  assert.match(known, /報告の送達確認（参加者）4 \/ 8（known）/);
  assert.match(known, /回答済み懸念／報告済み懸念1 \/ 2（known）/);
  assert.match(known, /未解決の報告済み懸念1件（known）/);
  assert.match(known, /保存済み・参照のみ[\s\S]*workstream\.taskmeeting\.minutes[\s\S]*artifact-minutes-20261118/);
  assert.doesNotMatch(known, /<img|javascript:|Users\/private/);
  assert.equal(await progressSections.nth(0).locator('a').count(), 0, 'minutes remain an owner reference without a reader route');
  const unknown = await progressSections.nth(1).textContent();
  assert.match(unknown, /出席確認（参加者）unknown（未確認）/);
  assert.match(unknown, /報告の送達確認（参加者）0 \/ 8（known）/);
  assert.match(unknown, /回答済み懸念／報告済み懸念unknown（未確認）/);
  assert.match(unknown, /議事録none（明示的な指定なし）/);
  assert.match(await progressSections.nth(2).textContent(), /進捗値が未報告です。0件とは判断していません/);
  assert.deepEqual(pageErrors, []);
});

test('meeting Daily progress rejects inconsistent counters without rendering owner input', async (t) => {
  const invalidProgress = {
    attendance: {current: dailyValue('known', 9, 'workstream.meeting.attendance'), total: dailyValue('known', 8, 'workstream.meeting.roster')},
    delivered_reports: {current: dailyValue('known', 0, 'workstream.meeting.reports'), total: dailyValue('known', 8, 'workstream.meeting.roster')},
    answered_concerns: {current: dailyValue('known', 0, 'workstream.meeting.answered'), total: dailyValue('known', 0, 'workstream.meeting.concerns')},
    unresolved_concerns: dailyValue('known', 0, 'workstream.meeting.unresolved'),
    minutes: dailyValue('known', {
      owner_ref: 'application.taskmeeting', resource_type: 'meeting_minutes', reference: 'javascript:alert(1)',
      native_id: 'artifact-minutes-1', data_scope: 'user',
    }, 'workstream.taskmeeting.minutes'),
  };
  const definition = dailyDefinition({
    kind_id: 'meeting.daily_report', resource_type: 'meeting_definition',
    occurrences: [{
      occurrence_id: 'invalid-meeting-progress', state: 'scheduled',
      next_run_at: dailyValue('unknown'), last_attempt_at: dailyValue('unknown'), last_success_at: dailyValue('unknown'),
      result: dailyValue('unknown'), native_refs: [], meeting_progress: invalidProgress,
    }],
    occurrence_count: dailyValue('known', 1, 'workstream.taskmeeting.occurrences'),
  });
  const {page, pageErrors} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) await json(route, capabilities());
    else if (record.method === 'GET' && url.pathname.endsWith('/items')) {
      await json(route, url.searchParams.get('view') === 'daily' ? dailyList([definition]) : list());
    }
    else await json(route, {error_code: 'NOT_FOUND'}, 404);
  });

  await connect(page);
  const dailyResponse = page.waitForResponse((response) => new URL(response.url()).searchParams.get('view') === 'daily');
  await page.locator('#taskCenterViewDaily').click();
  assert.equal((await dailyResponse).status(), 200);
  await page.locator('#taskCenterListState.is-error').waitFor({state: 'visible'});
  assert.match(await page.locator('#taskCenterListState').textContent(), /取得できません/);
  assert.equal(await page.locator('.task-center__daily-definition').count(), 0);
  assert.deepEqual(pageErrors, []);
});

test('Daily unavailable envelope is not an empty success and keeps registered definition rows', async (t) => {
  const unavailable = dailyDefinition({
    configured: dailyAxis('unknown', 'owner.daily.scheduler', 'source_unavailable'),
    effective: dailyAxis('unavailable', 'owner.daily.scheduler', 'source_unavailable'),
    observed: dailyAxis('unavailable', 'owner.daily.scheduler', 'source_unavailable'),
    issues: [{source_id: 'daily-mail-source', owner_ref: 'owner.daily-mail', code: 'source_unavailable'}],
  });
  const {page, pageErrors} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) {
      await json(route, capabilities({
        kinds: [], agents: [], policy_revision: '', managed_available: false, daily_available: true,
        can_submit: false, managed_reason_code: 'managed_owner_unavailable',
      }));
    } else if (record.method === 'GET' && url.pathname.endsWith('/items')) {
      await json(route, dailyList([unavailable], {partial: true, availability: 'unavailable'}));
    } else {
      await json(route, {error_code: 'NOT_FOUND'}, 404);
    }
  });

  await connect(page);
  assert.equal(await page.locator('.task-center__daily-definition').count(), 1, await page.locator('#taskCenterListState').textContent());
  assert.equal(await page.locator('#taskCenterSummaryState').textContent(), '観測不能');
  assert.match(await page.locator('#taskCenterListState').textContent(), /sourceは観測不能/);
  assert.equal(await page.locator('#taskCenterEmpty').isVisible(), false, 'retained descriptor rows are not replaced by an empty result');
  assert.deepEqual(pageErrors, []);
});

test('Daily source missing capability shows unavailable state without an empty-result fallback', async (t) => {
  const {page, apiRequests, pageErrors} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) {
      await json(route, capabilities({
        kinds: [], agents: [], policy_revision: '', managed_available: false, daily_available: false,
        can_submit: false, managed_reason_code: 'managed_owner_unavailable', daily_reason_code: 'daily_sources_unavailable',
      }));
    } else {
      await json(route, {error_code: 'NOT_FOUND'}, 404);
    }
  });

  await connect(page);
  assert.match(await page.locator('#taskCenterModeNotice').textContent(), /日常タスクsourceが利用できません/);
  assert.match(await page.locator('#taskCenterListState').textContent(), /sourceは観測不能/);
  assert.equal(await page.locator('#taskCenterSummaryState').textContent(), '観測不能');
  assert.equal(await page.locator('#taskCenterEmpty').isVisible(), false);
  assert.equal(apiRequests.filter((request) => new URL(request.url).pathname.endsWith('/items')).length, 0);
  assert.deepEqual(pageErrors, []);
});

test('explicit connection reads safely and submits the supported JST timing contract', async (t) => {
  let submitted = null;
  const {page, apiRequests, pageErrors} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) {
      await json(route, capabilities());
    } else if (record.method === 'GET' && url.pathname.endsWith('/items')) {
      await json(route, list(url.searchParams.get('view') === 'remaining' ? [item()] : []));
    } else if (record.method === 'POST' && url.pathname.endsWith('/requests')) {
      submitted = record.body;
      const accepted = item({goal_id: 'goal-created-2', title: record.body.title || '第一章の草稿を作る', status: 'draft', description: record.body.description});
      await json(route, {item: accepted, replayed: false, receipt_ref: 'receipt-demo-2'});
    } else {
      await json(route, {error_code: 'NOT_FOUND'}, 404);
    }
  });

  assert.equal(apiRequests.length, 0, 'the page must not read private API data before explicit connection');
  await connect(page);
  assert.equal(apiRequests.length, 2);
  assert.equal(apiRequests[0].headers.authorization, `Bearer ${TEST_TOKEN}`);
  assert.equal(apiRequests[0].headers['x-rencrow-client'], 'RenCrow_Viewer');
  assert.equal(apiRequests[0].headers['x-rencrow-interaction-profile'], 'task-center');
  assert.equal(new URL(apiRequests[0].url).searchParams.has('token'), false);
  assert.equal(new URL(apiRequests[1].url).searchParams.get('view'), 'remaining');
  assert.equal(new URL(apiRequests[1].url).searchParams.has('assignee'), false, 'all-in-scope is represented by omitting assignee');
  assert.equal(await page.locator('.task-center__item-title').textContent(), '<img src=x onerror=window.taskCenterInjected=true>');
  assert.equal(await page.locator('#taskCenterItems img').count(), 0, 'owner text must not become markup');
  assert.equal(await page.evaluate(() => Boolean(window.taskCenterInjected)), false);
  await page.locator('.task-center__item > summary').click();
  const itemDetailText = await page.locator('.task-center__item-detail').textContent();
  assert.match(itemDetailText, /実行予定: 指定なし/);
  assert.match(itemDetailText, /時間項目の確認状態: known/);
  assert.match(itemDetailText, /時間項目のsource: workstream\.goal/);
  assert.equal(await page.evaluate(() => [localStorage.length, sessionStorage.length].join(',')), '0,0');
  assert.equal(await page.locator('#taskCenterToken').inputValue(), '');
  await page.locator('[data-task-view="claimable"]').click();
  await page.locator('.task-center__list-state').waitFor({state: 'visible'});
  const userUnassignedList = apiRequests.filter((request) => request.method === 'GET' && new URL(request.url).pathname.endsWith('/items')).at(-1);
  assert.equal(new URL(userUnassignedList.url).searchParams.get('view'), 'remaining');
  assert.equal(new URL(userUnassignedList.url).searchParams.get('assignee'), 'unassigned');
  assert.equal(await page.locator('[data-task-view="claimable"]').textContent(), '引受待ち');
  assert.equal(await page.locator('.task-center__claim').count(), 0, 'user capabilities cannot expose Agent claim');

  assert.equal(await page.locator('#taskCenterRecurrenceFrequency').evaluate((select) =>
    Array.from(select.options).filter((option) => option.value).every((option) => option.disabled)), true);
  assert.match(await page.locator('.task-center__recurrence small').textContent(), /繰り返しタスクの追加は現在未対応/);

  await page.locator('[data-task-view="remaining"]').click();
  await fillRequiredTaskFields(page);
  await page.locator('#taskCenterAddForm details.task-center__timing > summary').click();
  await page.locator('#taskCenterScheduled').fill('2026-11-20T08:30');
  await page.locator('#taskCenterDeadline').fill('2026-11-21T18:00');
  await page.locator('[data-window-start]').fill('08:00');
  await page.locator('[data-window-end]').fill('17:00');
  await page.locator('#taskCenterDuration').fill('3600');
  await page.locator('#taskCenterSubmit').click();
  await page.locator('#taskCenterMutationMessage').waitFor({state: 'visible'});
  assert.ok(submitted);
  assert.equal(submitted.title, '', 'task title is optional and is left for the owner to derive');
  assert.equal(submitted.kind_id, 'movie.catalog.lookup');
  assert.deepEqual(submitted.kind_input, {
    kind: 'movie', name: '星を追う子ども', information: 'overview', limit: 8,
  }, 'typed input follows the owner-published fields and values');
  assert.equal(submitted.assignee, 'unassigned');
  assert.equal(submitted.expected_policy_revision, 'task-policy-r7');
  assert.equal('allowed_agents' in submitted, false, 'inherit must stay omitted instead of serializing an empty set');
  assert.equal('actor_id' in submitted, false);
  assert.equal('user_id' in submitted, false);
  assert.equal(submitted.success_criteria.length, 2);
  assert.equal(submitted.verification.length, 2);
  assert.deepEqual(Object.keys(submitted.timing).sort(), [
    'allowed_windows', 'deadline_at', 'estimated_duration_seconds', 'not_before',
    'recurrence', 'scheduled_start_at', 'timezone',
  ].sort());
  assert.equal(submitted.timing.timezone, 'Asia/Tokyo');
  assert.equal(submitted.timing.scheduled_start_at, '2026-11-20T08:30:00+09:00');
  assert.equal(submitted.timing.not_before, null);
  assert.deepEqual(submitted.timing.allowed_windows, [{start: '08:00', end: '17:00'}]);
  assert.equal(submitted.timing.deadline_at, '2026-11-21T18:00:00+09:00');
  assert.equal(submitted.timing.recurrence, null);
  assert.equal(submitted.timing.estimated_duration_seconds, 3600);
  assert.match(submitted.idempotency_key, /^[0-9a-f-]{36}$/i);
  assert.equal(new URL(apiRequests.at(-1).url).searchParams.has('token'), false);
  assert.equal(await page.locator('#taskCenterMutationReceipt').textContent().then((text) => text.includes('receipt-demo-2')), true);
  assert.deepEqual(pageErrors, []);
});

test('owner schema controls enum_by_kind options and malformed schema fails closed as text', async (t) => {
  const {page, apiRequests, pageErrors} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) {
      await json(route, capabilities());
    } else if (record.method === 'GET' && url.pathname.endsWith('/items')) {
      await json(route, list());
    } else {
      await json(route, {error_code: 'NOT_FOUND'}, 404);
    }
  });

  await connect(page);
  await page.locator('#taskCenterAddDetails > summary').click();
  await page.locator('#taskCenterKind').selectOption('movie.catalog.lookup');
  const fields = page.locator('#taskCenterKindInputFields');
  await page.locator('[data-kind-input-name="kind"]').selectOption('person');
  const information = page.locator('[data-kind-input-name="information"]');
  assert.deepEqual(await information.locator('option').evaluateAll((options) => options.map((option) => option.value)), [
    '__task_center_no_selection__', '', 'profile', 'filmography',
  ]);
  assert.equal(await page.locator('#taskCenterSubmit').isDisabled(), false);
  assert.equal(await fields.locator('img').count(), 0);
  assert.equal(apiRequests.some((request) => request.method === 'POST'), false);
  assert.deepEqual(pageErrors, []);
});

test('unavailable executor stays visible as planning-only; unknown input schema cannot submit', async (t) => {
  const unavailable = capabilities({
    kinds: [{
      kind_id: 'movie.catalog.lookup', label: 'カタログ照会', allowed_agents: ['mio'],
      input_schema: lookupInputSchema(), requires_typed_input: true, executor_available: false,
      unavailable_reason: 'movie_lookup_owner_unavailable',
    }],
  });
  const {page, apiRequests, pageErrors} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) await json(route, unavailable);
    else if (record.method === 'GET' && url.pathname.endsWith('/items')) await json(route, list());
    else await json(route, {error_code: 'NOT_FOUND'}, 404);
  });

  await connect(page);
  await page.locator('#taskCenterAddDetails > summary').click();
  await page.locator('#taskCenterKind').selectOption('movie.catalog.lookup');
  assert.match(await page.locator('#taskCenterKindSupport').textContent(), /計画登録.*実行開始・成功を意味しません.*movie_lookup_owner_unavailable.*許可Agent: mio/);
  await page.locator('[data-kind-input-name="kind"]').selectOption('movie');
  await page.locator('[data-kind-input-name="name"]').fill('星を追う子ども');
  assert.equal(await page.locator('#taskCenterSubmit').isDisabled(), false, 'planning may be recorded without an executor');
  assert.equal(await page.locator('.task-center__claim').count(), 0);
  assert.equal(apiRequests.some((request) => request.method === 'POST'), false);

  await page.reload();
  await page.waitForFunction(() => document.querySelector('#taskCenterWorkspace').hidden);
  const malformed = capabilities({
    kinds: [{
      kind_id: 'movie.catalog.lookup', label: '<img src=x onerror=window.badSchema=true>', allowed_agents: [],
      input_schema: {schema_id: 'movie.catalog.lookup', version: 1, fields: [{name: 'payload', label: '<img src=x>', type: 'object', required: true}]},
      requires_typed_input: true, executor_available: true,
    }],
  });
  await page.route(`${TEST_ORIGIN}/api/v1/task-center/capabilities`, async (route) => json(route, malformed));
  await connect(page);
  await page.locator('#taskCenterAddDetails > summary').click();
  await page.locator('#taskCenterKind').selectOption('movie.catalog.lookup');
  assert.equal(await page.locator('#taskCenterSubmit').isDisabled(), true);
  assert.match(await page.locator('#taskCenterKindSupport').textContent(), /形式に未対応/);
  assert.equal(await page.locator('#taskCenterAddDetails img').count(), 0);
  assert.equal(await page.evaluate(() => Boolean(window.badSchema)), false);
  assert.equal(apiRequests.some((request) => request.method === 'POST'), false);
  assert.deepEqual(pageErrors, []);
});

test('authorized Goal details show completion criteria and verification as safe text only', async (t) => {
  const listOnlyCriteria = '<img src=x onerror=window.taskCenterInjected=true> list-only criterion';
  const criteria = ['完了条件を満たす', '<img src=x onerror=window.taskCenterInjected=true> 詳細本文'];
  const verification = ['成果物を開ける', '期待した項目を照合する'];
  const {page, apiRequests, pageErrors} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) {
      await json(route, capabilities());
    } else if (record.method === 'GET' && url.pathname.endsWith('/items')) {
      await json(route, list([item({success_criteria: [listOnlyCriteria], verification: ['list-only verification']})]));
    } else if (record.method === 'GET' && url.pathname.endsWith('/item')) {
      assert.equal(url.searchParams.get('goal_id'), 'goal-demo-1');
      await json(route, {item: item({success_criteria: criteria, verification})});
    } else {
      await json(route, {error_code: 'NOT_FOUND'}, 404);
    }
  });

  await connect(page);
  const card = page.locator('.task-center__item');
  await card.locator('summary').click();
  const details = card.locator('.task-center__item-detail');
  const summaryText = await details.textContent();
  assert.doesNotMatch(summaryText, /list-only criterion|list-only verification|完了条件|確認方法/);
  assert.equal(await page.locator('#taskCenterItems img').count(), 0);

  await details.getByRole('button', {name: 'owner詳細を再取得'}).click();
  await details.locator('dt').filter({hasText: '完了条件'}).waitFor({state: 'visible'});
  const fullDetailText = await details.textContent();
  assert.match(fullDetailText, /完了条件を満たす/);
  assert.match(fullDetailText, /詳細本文/);
  assert.match(fullDetailText, /成果物を開ける/);
  assert.match(fullDetailText, /期待した項目を照合する/);
  assert.equal(await page.locator('#taskCenterItems img').count(), 0, 'detail strings must stay text, not markup');
  assert.equal(await page.evaluate(() => Boolean(window.taskCenterInjected)), false);
  assert.equal(apiRequests.filter((request) => new URL(request.url).pathname.endsWith('/item')).length, 1);
  assert.deepEqual(pageErrors, []);
});

test('legacy authorized detail responses may omit criteria and verification', async (t) => {
  const {page, pageErrors} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) {
      await json(route, capabilities());
    } else if (record.method === 'GET' && url.pathname.endsWith('/items')) {
      await json(route, list([item()]));
    } else if (record.method === 'GET' && url.pathname.endsWith('/item')) {
      await json(route, {item: item()});
    } else {
      await json(route, {error_code: 'NOT_FOUND'}, 404);
    }
  });

  await connect(page);
  const details = page.locator('.task-center__item');
  await details.locator('summary').click();
  await details.locator('.task-center__item-detail').getByRole('button', {name: 'owner詳細を再取得'}).click();
  await page.locator('#taskCenterNotice').getByText(/ownerの詳細を取得しました/).waitFor({state: 'visible'});
  const labels = await details.locator('.task-center__item-detail dt').allTextContents();
  assert.equal(labels.includes('完了条件'), false);
  assert.equal(labels.includes('確認方法'), false);
  assert.equal(await page.locator('#taskCenterError').isVisible(), false);
  assert.deepEqual(pageErrors, []);
});

test('authorized canonical result is shown safely and explicit creator acceptance completes the Goal', async (t) => {
  const pending = resultItem();
  let current = pending;
  let acceptedBody = null;
  const {page, apiRequests, pageErrors} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) await json(route, capabilities());
    else if (record.method === 'GET' && url.pathname.endsWith('/items')) await json(route, list([current]));
    else if (record.method === 'GET' && url.pathname.endsWith('/item')) await json(route, {item: current});
    else if (record.method === 'GET' && url.pathname.endsWith('/result')) {
      assert.equal(url.searchParams.get('goal_id'), pending.goal_id);
      await json(route, resultView(current));
    } else if (record.method === 'POST' && url.pathname.endsWith('/result/accept')) {
      acceptedBody = record.body;
      current = acceptedMutation(pending).item;
      await json(route, acceptedMutation(pending), 201);
    } else {
      await json(route, {error_code: 'NOT_FOUND'}, 404);
    }
  });

  await connect(page);
  const card = page.locator('.task-center__item');
  await card.locator('summary').click();
  const detail = card.locator('.task-center__item-detail');
  await detail.getByRole('button', {name: 'owner詳細を再取得'}).click();
  await detail.getByRole('button', {name: '保存された成果物を確認'}).waitFor({state: 'visible'});
  assert.match(await detail.textContent(), /実行成功・Goal確認待ち/);
  assert.match(await detail.textContent(), /照会結果が正しい対象に対応する/);
  await detail.getByRole('button', {name: '保存された成果物を確認'}).click();
  const resultPanel = detail.locator('.task-center__result-review');
  await resultPanel.getByText('成果物JSONを表示').waitFor({state: 'visible'});
  assert.match(await resultPanel.locator('pre').textContent(), /<img src=x onerror=window\.taskResultInjected=true>/);
  assert.equal(await resultPanel.locator('img').count(), 0);
  assert.equal(await page.evaluate(() => Boolean(window.taskResultInjected)), false);
  assert.equal(await resultPanel.getByRole('button', {name: '成果物を確認してGoalを完了にする'}).isVisible(), true);

  await page.locator('#taskCenterAddDetails > summary').click();
  await page.locator('#taskCenterKind').selectOption('movie.catalog.lookup');
  await page.locator('[data-kind-input-name="kind"]').selectOption('movie');
  await page.locator('[data-kind-input-name="name"]').fill('星を追う子ども');
  await page.locator('[data-kind-input-name="information"]').selectOption('overview');
  if (process.env.TASK_CENTER_SCREENSHOT_DIR) {
    mkdirSync(process.env.TASK_CENTER_SCREENSHOT_DIR, {recursive: true});
    await page.screenshot({path: path.join(process.env.TASK_CENTER_SCREENSHOT_DIR, 'task-center-result-desktop.png'), fullPage: true});
    await page.setViewportSize({width: 390, height: 844});
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, 'narrow result review does not overflow horizontally');
    await page.screenshot({path: path.join(process.env.TASK_CENTER_SCREENSHOT_DIR, 'task-center-result-narrow.png'), fullPage: true});
    await page.setViewportSize({width: 1366, height: 900});
  }

  const acceptanceButton = resultPanel.getByRole('button', {name: '成果物を確認してGoalを完了にする'});
  await acceptanceButton.click();
  await page.locator('#taskCenterMutationMessage').waitFor({state: 'visible'});
  assert.match(await page.locator('#taskCenterMutationMessage').textContent(), /作成者が確認し、Goalを完了/);
  assert.match(await page.locator('#taskCenterMutationReceipt').textContent(), /完了/);
  assert.deepEqual(acceptedBody && Object.keys(acceptedBody).sort(), [
    'expected_assignment_epoch', 'expected_revision', 'goal_id', 'idempotency_key',
  ].sort());
  assert.equal(acceptedBody.goal_id, pending.goal_id);
  assert.equal(acceptedBody.expected_revision, pending.revision);
  assert.equal(acceptedBody.expected_assignment_epoch, pending.assignment_epoch);
  assert.match(acceptedBody.idempotency_key, /^[0-9a-f-]{36}$/i);
  assert.equal('actor_id' in acceptedBody, false);
  assert.equal('user_id' in acceptedBody, false);
  assert.equal(apiRequests.filter((request) => request.method === 'POST' && new URL(request.url).pathname.endsWith('/result/accept')).length, 1);
  assert.deepEqual(pageErrors, []);
});

test('result readback requires exact live references and leaves unreadable/private results unaccepted', async (t) => {
  const pending = resultItem();
  let resultMode = 'mismatch';
  const {page, apiRequests, pageErrors} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) await json(route, capabilities());
    else if (record.method === 'GET' && url.pathname.endsWith('/items')) await json(route, list([pending]));
    else if (record.method === 'GET' && url.pathname.endsWith('/item')) await json(route, {item: pending});
    else if (record.method === 'GET' && url.pathname.endsWith('/result')) {
      if (resultMode === 'private') await json(route, {error_code: 'NOT_FOUND'}, 404);
      else await json(route, resultView(pending, {revision: pending.revision - 1}));
    } else {
      await json(route, {error_code: 'NOT_FOUND'}, 404);
    }
  });

  await connect(page);
  const detail = page.locator('.task-center__item-detail');
  await page.locator('.task-center__item > summary').click();
  await detail.getByRole('button', {name: 'owner詳細を再取得'}).click();
  await detail.getByRole('button', {name: '保存された成果物を確認'}).click();
  await detail.locator('.task-center__result-state.is-error').waitFor({state: 'visible'});
  assert.equal(await detail.getByRole('button', {name: '成果物を確認してGoalを完了にする'}).count(), 0);
  assert.doesNotMatch(await detail.textContent(), /taskResultInjected|RESULT_BODY/);

  resultMode = 'private';
  await detail.getByRole('button', {name: '保存された成果物を確認'}).click();
  await detail.getByText('保存された成果物を確認できません。結果は未確認です。').waitFor({state: 'visible'});
  assert.equal(await detail.getByRole('button', {name: '成果物を確認してGoalを完了にする'}).count(), 0);
  assert.equal(apiRequests.filter((request) => request.method === 'POST' && new URL(request.url).pathname.endsWith('/result/accept')).length, 0);
  assert.equal(apiRequests.filter((request) => new URL(request.url).pathname.endsWith('/result')).length, 2);
  assert.deepEqual(pageErrors, []);
});

test('ambiguous acceptance stays locked and recovers by exact-key GET without a second POST', async (t) => {
  const pending = resultItem();
  const accepted = acceptedMutation(pending);
  let postCount = 0;
  let acceptanceQuery = null;
  const {page, apiRequests, pageErrors} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) await json(route, capabilities());
    else if (record.method === 'GET' && url.pathname.endsWith('/items')) await json(route, list([pending]));
    else if (record.method === 'GET' && url.pathname.endsWith('/item')) await json(route, {item: pending});
    else if (record.method === 'GET' && url.pathname.endsWith('/result')) await json(route, resultView(pending));
    else if (record.method === 'POST' && url.pathname.endsWith('/result/accept')) {
      postCount += 1;
      await route.abort('failed');
    } else if (record.method === 'GET' && url.pathname.endsWith('/result/acceptance')) {
      acceptanceQuery = url.searchParams;
      await json(route, accepted);
    } else {
      await json(route, {error_code: 'NOT_FOUND'}, 404);
    }
  });

  await connect(page);
  const detail = page.locator('.task-center__item-detail');
  await page.locator('.task-center__item > summary').click();
  await detail.getByRole('button', {name: 'owner詳細を再取得'}).click();
  await detail.getByRole('button', {name: '保存された成果物を確認'}).click();
  const accept = detail.locator('.task-center__result-review').getByRole('button', {name: '成果物を確認してGoalを完了にする'});
  await accept.click();
  await page.locator('#taskCenterRecovery').waitFor({state: 'visible'});
  assert.match(await page.locator('#taskCenterMutationMessage').textContent(), /応答が届かず.*未確認/);
  assert.equal(await page.locator('#taskCenterSubmit').isDisabled(), true);
  assert.equal(postCount, 1);

  await page.locator('#taskCenterRecoverButton').click();
  await page.waitForFunction(() => document.querySelector('#taskCenterMutationMessage').textContent.includes('作成者が確認し、Goalを完了'));
  assert.equal(postCount, 1, 'recovery must never repeat the acceptance POST');
  const post = apiRequests.find((request) => request.method === 'POST' && new URL(request.url).pathname.endsWith('/result/accept'));
  assert.ok(post);
  assert.equal(acceptanceQuery.get('goal_id'), pending.goal_id);
  assert.equal(acceptanceQuery.get('idempotency_key'), post.body.idempotency_key);
  assert.equal(acceptanceQuery.has('token'), false);
  assert.deepEqual(pageErrors, []);
});

test('malformed optional Goal detail fields produce a safe response-shape error', async (t) => {
  const {page, pageErrors} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) {
      await json(route, capabilities());
    } else if (record.method === 'GET' && url.pathname.endsWith('/items')) {
      await json(route, list([item()]));
    } else if (record.method === 'GET' && url.pathname.endsWith('/item')) {
      await json(route, {item: item({success_criteria: {private: '<img src=x>'}, verification: ['ok']})});
    } else {
      await json(route, {error_code: 'NOT_FOUND'}, 404);
    }
  });

  await connect(page);
  const card = page.locator('.task-center__item');
  await card.locator('summary').click();
  await card.locator('.task-center__item-detail').getByRole('button', {name: 'owner詳細を再取得'}).click();
  await page.locator('#taskCenterError').getByText(/INVALID_ITEM_DETAIL/).waitFor({state: 'visible'});
  const errorText = await page.locator('#taskCenterError').textContent();
  assert.match(errorText, /HTTP 200.*INVALID_ITEM_DETAIL/);
  assert.doesNotMatch(errorText, /private|<img|src=x/);
  assert.equal(await page.locator('#taskCenterItems img').count(), 0);
  assert.deepEqual(pageErrors, []);
});

test('unsupported recurrence cannot be submitted even if the disabled option is changed programmatically', async (t) => {
  const {page, apiRequests} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) await json(route, capabilities());
    else if (record.method === 'GET' && url.pathname.endsWith('/items')) await json(route, list());
    else await json(route, {error_code: 'NOT_FOUND'}, 404);
  });
  await connect(page);
  await fillRequiredTaskFields(page);
  await page.locator('#taskCenterAddForm details.task-center__timing > summary').click();
  await page.locator('#taskCenterRecurrenceFrequency').evaluate((select) => {
    const daily = select.querySelector('option[value="daily"]');
    daily.disabled = false;
    select.value = 'daily';
    select.dispatchEvent(new Event('change', {bubbles: true}));
  });
  await page.locator('#taskCenterSubmit').click();
  await page.locator('#taskCenterFormError').waitFor({state: 'visible'});
  assert.match(await page.locator('#taskCenterFormError').textContent(), /繰り返しタスクの追加は現在未対応/);
  assert.equal(apiRequests.filter((request) => request.method === 'POST').length, 0);
});

test('ambiguous mutation response is recovered by GET without repeating the POST', async (t) => {
  const {page, apiRequests, pageErrors} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) {
      await json(route, capabilities());
    } else if (record.method === 'GET' && url.pathname.endsWith('/items')) {
      await json(route, list());
    } else if (record.method === 'POST' && url.pathname.endsWith('/requests')) {
      await json(route, {error_code: 'TEMPORARY_FAILURE'}, 500);
    } else if (record.method === 'GET' && url.pathname.endsWith('/request')) {
      await json(route, {status: 'outcome_unknown'});
    } else {
      await json(route, {error_code: 'NOT_FOUND'}, 404);
    }
  });

  assert.equal(apiRequests.length, 0);
  await connect(page);
  await fillRequiredTaskFields(page);
  await page.locator('#taskCenterSubmit').click();
  await page.locator('#taskCenterRecovery').waitFor({state: 'visible'});
  assert.equal(apiRequests.filter((request) => request.method === 'POST' && new URL(request.url).pathname.endsWith('/requests')).length, 1);
  await page.locator('#taskCenterRecoverButton').click();
  await page.locator('#taskCenterRecovery').waitFor({state: 'visible'});
  const requestLookups = apiRequests.filter((request) => request.method === 'GET' && new URL(request.url).pathname.endsWith('/request'));
  assert.equal(requestLookups.length, 1);
  assert.equal(new URL(requestLookups[0].url).searchParams.get('operation'), 'submit');
  assert.equal(new URL(requestLookups[0].url).searchParams.get('idempotency_key'), apiRequests.find((request) => request.method === 'POST').body.idempotency_key);
  assert.equal(apiRequests.filter((request) => request.method === 'POST' && new URL(request.url).pathname.endsWith('/requests')).length, 1);
  assert.equal(await page.locator('#taskCenterSubmit').isDisabled(), true, 'new mutation stays locked until result is confirmed');
  await page.waitForFunction(() => document.querySelector('#taskCenterError').textContent.includes('照会応答だけでは受付結果を確定できません'));
  const unknownWarning = await page.locator('#taskCenterError').textContent();
  assert.match(unknownWarning, /結果を確定できません/);
  await page.locator('[data-task-view="history"]').click();
  await page.locator('#taskCenterListState').getByText(/履歴: 0件を取得済み/).waitFor({state: 'visible'});
  assert.equal(await page.locator('#taskCenterError').textContent(), unknownWarning, 'view reads must preserve the unknown mutation warning');
  assert.equal(await page.locator('#taskCenterRecovery').isVisible(), true, 'the same-key lookup remains available');
  assert.equal(await page.locator('#taskCenterSubmit').isDisabled(), true, 'list recovery must not unlock a new POST');
  assert.equal(apiRequests.filter((request) => request.method === 'POST' && new URL(request.url).pathname.endsWith('/requests')).length, 1);
  assert.deepEqual(pageErrors, []);
});

test('claim capability exposes one explicit claim without a caller-supplied actor', async (t) => {
  const {page, apiRequests, pageErrors} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) {
      await json(route, capabilities({can_claim: true, actor_kind: 'agent'}));
    } else if (record.method === 'GET' && url.pathname.endsWith('/items')) {
      await json(route, list(url.searchParams.get('view') === 'claimable' ? [item({status: 'waiting'})] : []));
    } else if (record.method === 'POST' && url.pathname.endsWith('/claims')) {
      await json(route, {
        item: item({status: 'waiting', assignee: 'authenticated-agent'}),
        replayed: false,
        receipt_ref: 'claim-receipt-1',
      });
    } else {
      await json(route, {error_code: 'NOT_FOUND'}, 404);
    }
  });

  assert.equal(apiRequests.length, 0);
  await connect(page);
  await page.locator('[data-task-view="claimable"]').click();
  const listRequest = apiRequests.filter((request) => new URL(request.url).pathname.endsWith('/items')).at(-1);
  assert.equal(new URL(listRequest.url).searchParams.get('view'), 'claimable');
  assert.equal(await page.locator('[data-task-view="claimable"]').textContent(), '引受可能');
  await page.locator('.task-center__item > summary').click();
  await page.locator('.task-center__claim').waitFor({state: 'visible'});
  assert.equal(await page.locator('.task-center__claim').count(), 1);
  await page.locator('.task-center__claim').click();
  await page.locator('#taskCenterMutationMessage').waitFor({state: 'visible'});
  const claimRequest = apiRequests.find((request) => request.method === 'POST' && new URL(request.url).pathname.endsWith('/claims'));
  assert.ok(claimRequest);
  assert.deepEqual(Object.keys(claimRequest.body).sort(), [
    'expected_policy_revision', 'expected_revision', 'goal_id', 'idempotency_key',
  ].sort());
  assert.equal(claimRequest.body.goal_id, 'goal-demo-1');
  assert.equal(claimRequest.body.expected_revision, 4);
  assert.equal(claimRequest.body.expected_policy_revision, 'task-policy-r7');
  assert.equal('actor_id' in claimRequest.body, false);
  assert.equal('assignee' in claimRequest.body, false);
  assert.deepEqual(pageErrors, []);
});

test('view-unavailable daily response keeps the connection ready and a later view recovers all UI state', async (t) => {
  const {page, apiRequests, pageErrors} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) {
      await json(route, capabilities());
    } else if (record.method === 'GET' && url.pathname.endsWith('/items')) {
      if (url.searchParams.get('view') === 'daily') {
        await json(route, {error: 'task_center_unavailable'}, 503);
      } else {
        await json(route, list([item({goal_id: 'goal-recovered-1'})]));
      }
    } else {
      await json(route, {error: 'NOT_FOUND'}, 404);
    }
  });

  await connect(page);
  await page.locator('[data-task-view="daily"]').click();
  await page.locator('#taskCenterListState').getByText(/HTTP 503/).waitFor({state: 'visible'});
  assert.equal(await page.locator('#taskCenterPageState').textContent(), '認証済み・読み取り接続中');
  assert.equal(await page.locator('#taskCenterMioStatus').textContent(), '読み取り接続中');
  assert.equal(await page.locator('#taskCenterCapabilityState').textContent(), '認証済み・読み取り接続中');
  assert.equal(await page.locator('#taskCenterSummaryState').textContent(), '観測不能');
  assert.equal(await page.locator('#taskCenterListState').evaluate((node) => node.classList.contains('is-error')), true);
  assert.equal(await page.locator('#taskCenterError').isVisible(), false, 'view unavailability belongs to the list state');

  await page.locator('[data-task-view="remaining"]').click();
  await page.locator('.task-center__item').waitFor({state: 'visible'});
  await page.locator('#taskCenterNotice').waitFor({state: 'visible'});
  assert.equal(await page.locator('#taskCenterPageState').textContent(), '認証済み・読み取り接続中');
  assert.equal(await page.locator('#taskCenterMioStatus').textContent(), '読み取り接続中');
  assert.equal(await page.locator('#taskCenterCapabilityState').textContent(), '認証済み・読み取り接続中');
  assert.equal(await page.locator('#taskCenterSummaryState').textContent(), '取得成功');
  assert.equal(await page.locator('#taskCenterListState').evaluate((node) => node.classList.contains('is-recovered')), true);
  assert.match(await page.locator('#taskCenterNotice').textContent(), /残タスク.*回復/);
  assert.match(await page.locator('#taskCenterListState').textContent(), /残タスクの一覧取得が回復.*残タスク: 1件を取得済み/);
  assert.deepEqual(pageErrors, []);
  assert.equal(apiRequests.filter((request) => new URL(request.url).pathname.endsWith('/items')).length, 3);
});

test('a list network failure is shown as unknown and recovers after a successful read', async (t) => {
  const {page, pageErrors} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) {
      await json(route, capabilities());
    } else if (record.method === 'GET' && url.pathname.endsWith('/items')) {
      if (url.searchParams.get('view') === 'history') await route.abort('failed');
      else await json(route, list([item({goal_id: 'goal-network-recovered-1'})]));
    } else {
      await json(route, {error: 'NOT_FOUND'}, 404);
    }
  });

  await connect(page);
  await page.locator('[data-task-view="history"]').click();
  await page.locator('#taskCenterListState').getByText(/接続状態は未確認/).waitFor({state: 'visible'});
  assert.equal(await page.locator('#taskCenterPageState').textContent(), '接続状態を確認できません');
  assert.equal(await page.locator('#taskCenterSummaryState').textContent(), '観測不能');
  assert.match(await page.locator('#taskCenterNotice').textContent(), /接続状態は未確認/);

  await page.locator('[data-task-view="remaining"]').click();
  await page.locator('.task-center__item').waitFor({state: 'visible'});
  await page.locator('#taskCenterNotice').waitFor({state: 'visible'});
  assert.equal(await page.locator('#taskCenterPageState').textContent(), '認証済み・読み取り接続中');
  assert.equal(await page.locator('#taskCenterMioStatus').textContent(), '読み取り接続中');
  assert.equal(await page.locator('#taskCenterCapabilityState').textContent(), '認証済み・読み取り接続中');
  assert.equal(await page.locator('#taskCenterSummaryState').textContent(), '取得成功');
  assert.equal(await page.locator('#taskCenterListState').evaluate((node) => node.classList.contains('is-recovered')), true);
  assert.match(await page.locator('#taskCenterNotice').textContent(), /残タスク.*回復/);
  assert.deepEqual(pageErrors, []);
});

test('a 401 list response requires a fresh operator connection before reads resume', async (t) => {
  const {page, apiRequests, pageErrors} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) {
      await json(route, capabilities());
    } else if (record.method === 'GET' && url.pathname.endsWith('/items')) {
      if (url.searchParams.get('view') === 'daily' && record.headers.authorization === `Bearer ${TEST_TOKEN}`) {
        await json(route, {error: 'unauthorized'}, 401);
      } else if (url.searchParams.get('view') === 'daily') {
        await json(route, dailyList([dailyDefinition()]));
      } else {
        await json(route, list([item({goal_id: 'goal-reauthenticated-1'})]));
      }
    } else {
      await json(route, {error: 'NOT_FOUND'}, 404);
    }
  });

  await connect(page);
  await page.locator('[data-task-view="daily"]').click();
  await page.locator('#taskCenterListState').getByText(/認証期限切れ/).waitFor({state: 'visible'});
  assert.equal(await page.locator('#taskCenterPageState').textContent(), '認証期限切れ・再接続が必要');
  assert.equal(await page.locator('#taskCenterCapabilityState').textContent(), '認証期限切れ・再接続が必要');
  assert.equal(await page.locator('#taskCenterSummaryKinds').textContent(), '—');
  assert.equal(await page.locator('#taskCenterSummaryCount').textContent(), '0');
  assert.equal(await page.locator('#taskCenterListState').evaluate((node) => node.classList.contains('is-error')), true);
  assert.match(await page.locator('#taskCenterNotice').textContent(), /再接続/);

  await page.locator('#taskCenterToken').fill('replacement-operator-token');
  await page.locator('#taskCenterConnectButton').click();
  await page.locator('.task-center__item').waitFor({state: 'visible'});
  assert.equal(await page.locator('.task-center__daily-definition').count(), 1, 'successful reconnect preserves the selected Daily view');
  assert.equal(await page.locator('#taskCenterPageState').textContent(), '認証済み・読み取り接続中');
  assert.equal(await page.locator('#taskCenterMioStatus').textContent(), '読み取り接続中');
  assert.equal(await page.locator('#taskCenterCapabilityState').textContent(), '認証済み・読み取り接続中');
  assert.equal(await page.locator('#taskCenterSummaryState').textContent(), '取得成功');
  assert.equal(apiRequests.at(-1).headers.authorization, 'Bearer replacement-operator-token');
  assert.deepEqual(pageErrors, []);
});

test('a policy revision rejection remains visible after a later successful view read', async (t) => {
  const {page, apiRequests, pageErrors} = await openBrowser(t, async (route, record) => {
    const url = new URL(record.url);
    if (record.method === 'GET' && url.pathname.endsWith('/capabilities')) {
      await json(route, capabilities());
    } else if (record.method === 'GET' && url.pathname.endsWith('/items')) {
      await json(route, list());
    } else if (record.method === 'POST' && url.pathname.endsWith('/requests')) {
      await json(route, {error: 'policy_revision_changed'}, 409);
    } else {
      await json(route, {error: 'NOT_FOUND'}, 404);
    }
  });

  await connect(page);
  await fillRequiredTaskFields(page);
  await page.locator('#taskCenterSubmit').click();
  await page.locator('#taskCenterError').waitFor({state: 'visible'});
  const policyWarning = await page.locator('#taskCenterError').textContent();
  assert.match(policyWarning, /HTTP 409.*policy_revision_changed/);

  await page.locator('[data-task-view="history"]').click();
  await page.locator('#taskCenterListState').getByText(/履歴: 0件を取得済み/).waitFor({state: 'visible'});
  assert.equal(await page.locator('#taskCenterPageState').textContent(), '認証済み・読み取り接続中');
  await page.waitForFunction(() => document.querySelector('#taskCenterSummaryState').textContent === '取得成功');
  assert.equal(await page.locator('#taskCenterSummaryState').textContent(), '取得成功');
  assert.equal(await page.locator('#taskCenterError').textContent(), policyWarning);
  assert.equal(apiRequests.filter((request) => request.method === 'POST' && new URL(request.url).pathname.endsWith('/requests')).length, 1);
  assert.deepEqual(pageErrors, []);
});
