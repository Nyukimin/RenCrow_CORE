'use strict';

(() => {
  const API_ROOT = '/api/v1/task-center';
  const PAGE_LIMIT = 50;
  const MAX_LOADED_ITEMS = 200;
  const VIEW_LABELS = {
    remaining: '残タスク',
    claimable: '引受可能',
    daily: '日常タスク',
    history: '履歴',
  };
  const STATUS_LABELS = {
    draft: '下書き', active: '進行中', paused: '一時停止', waiting: '待機中',
    completed: '完了', archived: 'アーカイブ済み',
  };
  const DAILY_AXIS_LABELS = {
    implementation: {
      implemented: '実装済み', unimplemented: '未実装', unsupported: 'owner未対応', unknown: '未確認',
    },
    configured: {
      configured: '設定あり', unconfigured: '未設定', disabled: '設定で無効', unsupported: 'owner未対応', unknown: '未確認',
    },
    effective: {
      enabled: '設定上有効', disabled: '設定上無効', blocked: '設定条件により停止', unavailable: '利用不能',
      unsupported: 'owner未対応', unknown: '未確認',
    },
    observed: {
      available: '観測データあり', unavailable: '観測不能', stale: '古い観測データ', not_observed: '未観測',
      unsupported: 'owner未対応', unknown: '未確認',
    },
  };
  const DAILY_VALUE_STATES = ['known', 'none', 'unknown', 'unsupported'];
  const DAILY_OUTCOME_LABELS = {
    success: '成功記録', failure: '失敗記録', partial: '部分記録', running: '実行中の記録', missed: '未実施記録', unknown: '結果未確認',
  };
  const DAILY_OCCURRENCE_LABELS = {
    scheduled: '予定記録', running: '実行中の記録', succeeded: '成功記録あり', failed: '失敗記録あり',
    partial: '部分記録', missed: '未実施記録', unknown: '状態未確認', unavailable: '観測不能',
  };
  const DAILY_ISSUE_LABELS = {
    source_unavailable: 'sourceを観測できません', invalid_projection: 'source projectionが無効です',
    observation_missing: '観測値がありません', source_reported_partial: 'sourceが部分取得を報告しました',
    kind_filter_unavailable: '種類filterに対応していません',
  };
  const DAILY_TIMING_FIELDS = [
    ['timezone', 'タイムゾーン'], ['scheduled_start_at', '定義上の開始予定'], ['not_before', '開始条件'],
    ['allowed_windows', '許可時間帯'], ['deadline_at', '定義上の期限'], ['recurrence', '繰り返し定義'],
    ['estimated_duration_seconds', '想定所要時間'],
  ];
  const TASK_MEETING_DAILY_KIND_ID = 'meeting.daily_report';
  const RESULT_LABELS = {
    rejected: '受付拒否', not_accepted: '未受付', stored: 'owner受理済み', started: '実行開始済み',
    claimed: '引受済み', completed: '完了', outcome_unknown: '結果未確認',
  };
  const MAX_KIND_INPUT_FIELDS = 24;
  const MAX_KIND_INPUT_OPTIONS = 100;
  const KIND_INPUT_UNSET = '__task_center_no_selection__';
  const $ = (id) => document.getElementById(id);
  const elements = {
    pageState: $('taskCenterPageState'), updatedAt: $('taskCenterUpdatedAt'), mioStatus: $('taskCenterMioStatus'),
    summaryCount: $('taskCenterSummaryCount'), summaryView: $('taskCenterSummaryView'),
    summaryNext: $('taskCenterSummaryNext'), summaryKinds: $('taskCenterSummaryKinds'),
    summaryKindsLabel: $('taskCenterSummaryKindsLabel'), summaryKindsSource: $('taskCenterSummaryKindsSource'),
    summaryState: $('taskCenterSummaryState'), summaryRevision: $('taskCenterSummaryRevision'),
    summaryStateCard: document.querySelector('.task-center__summary-card--state'),
    capabilityState: $('taskCenterCapabilityState'), connectForm: $('taskCenterConnectForm'), token: $('taskCenterToken'),
    connectButton: $('taskCenterConnectButton'), disconnectButton: $('taskCenterDisconnectButton'),
    notice: $('taskCenterNotice'), error: $('taskCenterError'), workspace: $('taskCenterWorkspace'), modeNotice: $('taskCenterModeNotice'),
    viewButtons: Array.from(document.querySelectorAll('[data-task-view]')), refresh: $('taskCenterRefresh'),
    search: $('taskCenterSearch'), searchLabel: $('taskCenterSearchLabel'), listTitle: $('taskCenterListTitle'),
    assignee: $('taskCenterAssignee'), assigneeField: $('taskCenterAssigneeField'),
    dailyKind: $('taskCenterDailyKind'), dailyKindField: $('taskCenterDailyKindField'), listState: $('taskCenterListState'),
    items: $('taskCenterItems'), empty: $('taskCenterEmpty'), more: $('taskCenterMore'),
    addDetails: $('taskCenterAddDetails'), addForm: $('taskCenterAddForm'), formError: $('taskCenterFormError'),
    kind: $('taskCenterKind'), kindSupport: $('taskCenterKindSupport'), kindInputFields: $('taskCenterKindInputFields'),
    assigneeInput: $('taskCenterAssigneeInput'), title: $('taskCenterTitleInput'),
    description: $('taskCenterDescription'), criteria: $('taskCenterCriteria'), verification: $('taskCenterVerification'),
    timezone: $('taskCenterTimezone'), scheduled: $('taskCenterScheduled'), notBefore: $('taskCenterNotBefore'),
    deadline: $('taskCenterDeadline'), windowRows: $('taskCenterWindowRows'), addWindow: $('taskCenterAddWindow'),
    recurrenceFrequency: $('taskCenterRecurrenceFrequency'), recurrenceFields: $('taskCenterRecurrenceFields'),
    recurrenceTimesField: $('taskCenterRecurrenceTimesField'), recurrenceTimes: $('taskCenterRecurrenceTimes'),
    weekdays: $('taskCenterWeekdays'), intervalField: $('taskCenterIntervalField'),
    intervalSeconds: $('taskCenterIntervalSeconds'), duration: $('taskCenterDuration'), submit: $('taskCenterSubmit'),
    mutationPanel: $('taskCenterMutationPanel'), mutationMessage: $('taskCenterMutationMessage'),
    mutationReceipt: $('taskCenterMutationReceipt'), recovery: $('taskCenterRecovery'), recoverButton: $('taskCenterRecoverButton'),
    cancellationForm: $('taskCenterCancellationForm'), cancellationTaskID: $('taskCenterCancellationTaskID'),
    cancellationInspect: $('taskCenterCancellationInspect'), cancellationSubmit: $('taskCenterCancellationSubmit'), cancellationState: $('taskCenterCancellationState'),
  };

  if (Object.values(elements).some((value) => value === null)) return;

  const state = {
    token: '', capabilities: null, items: [], nextCursor: '', partial: false, listPolicyRevision: '',
    view: 'remaining', generation: 0, listController: null, capabilityController: null,
    busy: false, pendingMutation: null, updatedAt: '', listErrorMessage: '', listRecoveryMessage: '', hasListSuccess: false,
    dailyDefinitions: [], dailyAvailability: 'unknown', dailyIssues: [], dailyObservedAt: '', dailyKinds: new Set(),
    detailControllers: new Set(), kindInputDraft: Object.create(null),
    cancellationPreview: null, cancellationBusy: false,
  };

  class TaskCenterAPIError extends Error {
    constructor(status, code) {
      super(`HTTP ${status}`);
      this.status = status;
      this.code = code;
    }
  }

  function cleanText(value, fallback = '未確認') {
    if (typeof value === 'string' && value.trim()) return value.trim();
    if (typeof value === 'number' && Number.isFinite(value)) return String(value);
    if (typeof value === 'boolean') return value ? 'はい' : 'いいえ';
    return fallback;
  }

  function setNotice(message, kind = 'success') {
    elements.notice.textContent = message;
    elements.notice.classList.toggle('task-center__notice--error', kind === 'error');
    elements.notice.hidden = !message;
  }

  function setError(message, target = elements.error) {
    target.textContent = message;
    target.hidden = !message;
    if (target === elements.error && message) elements.notice.hidden = true;
  }

  function updateConnectionState(message, kind = 'warning') {
    elements.pageState.textContent = message;
    elements.mioStatus.textContent = kind === 'ready' ? '読み取り接続中' : kind === 'error' ? '確認が必要' : '認証待ち';
    const dot = document.querySelector('.task-center__state-dot');
    if (dot) {
      dot.classList.toggle('is-ready', kind === 'ready');
      dot.classList.toggle('is-error', kind === 'error');
    }
    elements.capabilityState.textContent = message;
    elements.capabilityState.classList.toggle('is-ready', kind === 'ready');
    elements.capabilityState.classList.toggle('is-error', kind === 'error');
  }

  function isAuthenticationRejected(error) {
    return error instanceof TaskCenterAPIError && error.status === 401;
  }

  function updateSummary() {
    const isDaily = state.view === 'daily';
    const count = isDaily ? state.dailyDefinitions.length : state.items.length;
    const kindCount = isDaily
      ? new Set(state.dailyDefinitions.map((definition) => definition.kind_id)).size
      : state.capabilities ? state.capabilities.kinds.length : 0;
    const dailyUnavailable = isDaily && state.dailyAvailability === 'unavailable';
    elements.summaryCount.textContent = state.token ? String(count) : '—';
    elements.summaryView.textContent = state.token ? `${currentViewLabel()} · ${isDaily ? '定義' : '取得'}を表示` : '認証後に表示';
    elements.summaryNext.textContent = !state.token ? '—' : state.nextCursor ? 'あり' : 'なし';
    elements.summaryKinds.textContent = state.capabilities ? String(kindCount) : '—';
    elements.summaryKindsLabel.textContent = isDaily ? '取得中の定義種類' : '登録された種類';
    elements.summaryKindsSource.textContent = isDaily ? '取得した定義から集計' : 'capabilitiesから取得';
    elements.summaryState.textContent = !state.token ? '未確認'
      : state.capabilityController || state.listController ? '取得中'
        : !state.capabilities || state.listErrorMessage || dailyUnavailable ? '観測不能'
          : state.partial ? '部分取得' : state.updatedAt ? '取得成功' : '未確認';
    elements.summaryRevision.textContent = isDaily
      ? `source状態 ${dailyAvailabilityLabel(state.dailyAvailability)}${state.dailyObservedAt ? ` · ${formatTime(state.dailyObservedAt)}` : ''}`
      : state.capabilities
        ? `policy ${cleanText(state.listPolicyRevision || state.capabilities.policy_revision, 'revision未確認')}`
        : 'policy revision 未確認';
    elements.summaryStateCard.classList.toggle('is-ready', Boolean(state.updatedAt && !state.partial && !dailyUnavailable));
    elements.summaryStateCard.classList.toggle('is-partial', Boolean(state.partial));
    elements.summaryStateCard.classList.toggle('is-error', !state.token || !state.capabilities || Boolean(state.listErrorMessage) || dailyUnavailable);
    elements.updatedAt.textContent = state.updatedAt ? `更新 ${formatTime(state.updatedAt)}` : '未取得';
  }

  function currentViewLabel() {
    if (state.view === 'daily') return '日常タスク';
    if (state.view === 'claimable' && (!state.capabilities || !state.capabilities.can_claim)) return '引受待ち';
    return VIEW_LABELS[state.view];
  }

  function dailyAvailabilityLabel(value) {
    return ({available: 'source利用可能', partial: '部分取得', unavailable: '観測不能'})[value] || '未確認';
  }

  function currentItemCount() {
    return state.view === 'daily' ? state.dailyDefinitions.length : state.items.length;
  }

  function updateBusyControls() {
    const locked = state.busy || Boolean(state.pendingMutation);
    elements.connectButton.disabled = locked;
    elements.token.disabled = locked;
    const selectedKind = state.capabilities && state.capabilities.kinds.find((kind) => kind.kind_id === elements.kind.value);
    elements.submit.disabled = !state.token || !state.capabilities || !state.capabilities.managed_available ||
      !state.capabilities.can_submit || !kindCanSubmit(selectedKind) || locked;
    elements.recoverButton.disabled = state.busy || !state.pendingMutation;
    elements.refresh.disabled = state.busy;
    elements.more.disabled = state.busy || currentItemCount() >= MAX_LOADED_ITEMS;
    elements.disconnectButton.disabled = locked;
    for (const button of elements.items.querySelectorAll('[data-task-result-action]')) button.disabled = locked || Boolean(state.listController);
  }

  function apiURL(path, query = {}) {
    const url = new URL(API_ROOT + path, window.location.origin);
    for (const [key, value] of Object.entries(query)) {
      if (value !== undefined && value !== null && value !== '') url.searchParams.set(key, String(value));
    }
    return url.pathname + url.search;
  }

  async function apiJSON(path, options = {}) {
    const headers = new Headers({
      Authorization: `Bearer ${state.token}`,
      'X-RenCrow-Client': 'RenCrow_Viewer',
      'X-RenCrow-Interaction-Profile': 'task-center',
      Accept: 'application/json',
    });
    if (options.body !== undefined) headers.set('Content-Type', 'application/json');
    const response = await fetch(path, {
      method: options.method || 'GET',
      headers,
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      signal: options.signal,
      cache: 'no-store',
      referrerPolicy: 'no-referrer',
    });
    let payload = null;
    try { payload = await response.json(); } catch (_) { /* response shape is checked below */ }
    if (!response.ok) {
      const rawCode = payload && (payload.error_code || payload.code || payload.error);
      const code = typeof rawCode === 'string' && /^[A-Z0-9_.-]{1,96}$/i.test(rawCode) ? rawCode : '';
      throw new TaskCenterAPIError(response.status, code);
    }
    if (!payload || typeof payload !== 'object' || Array.isArray(payload)) {
      throw new TaskCenterAPIError(response.status, 'INVALID_RESPONSE');
    }
    return payload;
  }

  function safeID(value) {
    return typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9._:-]{0,159}$/.test(value);
  }

  function schemaOptionList(raw) {
    if (!Array.isArray(raw) || raw.length < 1 || raw.length > MAX_KIND_INPUT_OPTIONS) return null;
    const seen = new Set();
    const options = [];
    for (const option of raw) {
      if (!isObject(option) || typeof option.value !== 'string' || option.value.length > 128 ||
          /[\u0000-\u001f\u007f]/.test(option.value) || typeof option.label !== 'string' ||
          !option.label.trim() || option.label.length > 160 || /[\u0000-\u001f\u007f]/.test(option.label) ||
          option.value === KIND_INPUT_UNSET || seen.has(option.value)) return null;
      seen.add(option.value);
      options.push({value: option.value, label: option.label});
    }
    return options;
  }

  function validateKindInputSchema(raw, kindID) {
    let schemaSize = Infinity;
    try { schemaSize = new TextEncoder().encode(JSON.stringify(raw)).byteLength; } catch (_) { /* invalid schemas fail closed */ }
    if (!isObject(raw) || raw.schema_id !== kindID || raw.version !== 1 || !Array.isArray(raw.fields) ||
        raw.fields.length > MAX_KIND_INPUT_FIELDS || schemaSize > 16384) {
      return {state: 'invalid', schema: null};
    }
    const names = new Set();
    const fields = [];
    for (const field of raw.fields) {
      if (!isObject(field) || typeof field.name !== 'string' || !/^[a-z][a-z0-9_]{0,63}$/.test(field.name) ||
          names.has(field.name) || typeof field.label !== 'string' || !field.label.trim() || field.label.length > 160 ||
          /[\u0000-\u001f\u007f]/.test(field.label) || typeof field.required !== 'boolean' ||
          field.help !== undefined && (typeof field.help !== 'string' || field.help.length > 320 || /[\u0000-\u001f\u007f]/.test(field.help))) {
        return {state: 'invalid', schema: null};
      }
      names.add(field.name);
      const normalized = {name: field.name, label: field.label, help: field.help || '', type: field.type, required: field.required};
      if (field.type === 'enum') {
        normalized.options = schemaOptionList(field.options);
        if (!normalized.options || field.required && normalized.options.some((option) => option.value === '')) {
          return {state: 'invalid', schema: null};
        }
        if (field.default !== undefined && (typeof field.default !== 'string' || !normalized.options.some((option) => option.value === field.default))) {
          return {state: 'invalid', schema: null};
        }
      } else if (field.type === 'string') {
        if (field.default !== undefined && (typeof field.default !== 'string' || field.default.length > 4096 || /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/.test(field.default))) {
          return {state: 'invalid', schema: null};
        }
        normalized.default = typeof field.default === 'string' ? field.default : '';
      } else if (field.type === 'integer') {
        if (!Number.isSafeInteger(field.minimum) || !Number.isSafeInteger(field.maximum) || field.minimum > field.maximum ||
            field.default !== undefined && (!Number.isSafeInteger(field.default) || field.default < field.minimum || field.default > field.maximum)) {
          return {state: 'invalid', schema: null};
        }
        normalized.minimum = field.minimum;
        normalized.maximum = field.maximum;
        if (field.default !== undefined) normalized.default = field.default;
      } else if (field.type === 'enum_by_kind') {
        if (!isObject(field.options_by_kind)) return {state: 'invalid', schema: null};
        const categories = Object.keys(field.options_by_kind);
        if (!categories.length || categories.length > MAX_KIND_INPUT_OPTIONS || categories.some((category) =>
          !/^[a-z][a-z0-9_-]{0,63}$/.test(category) || !schemaOptionList(field.options_by_kind[category]))) {
          return {state: 'invalid', schema: null};
        }
        normalized.options_by_kind = Object.create(null);
        for (const category of categories) normalized.options_by_kind[category] = schemaOptionList(field.options_by_kind[category]);
        if (field.default !== undefined && (typeof field.default !== 'string' || categories.some((category) =>
          !normalized.options_by_kind[category].some((option) => option.value === field.default)))) {
          return {state: 'invalid', schema: null};
        }
        if (field.default !== undefined) normalized.default = field.default;
      } else {
        return {state: 'unsupported', schema: null};
      }
      fields.push(normalized);
    }
    for (const field of fields.filter((candidate) => candidate.type === 'enum_by_kind')) {
      const categories = Object.keys(field.options_by_kind);
      const controllers = fields.filter((candidate) => candidate.type === 'enum' &&
        categories.every((category) => candidate.options.some((option) => option.value === category)));
      if (controllers.length !== 1) return {state: 'invalid', schema: null};
      field.depends_on = controllers[0].name;
    }
    return {state: 'ready', schema: {schema_id: raw.schema_id, version: raw.version, fields}};
  }

  function validateCapabilities(payload) {
    const value = payload.capabilities && typeof payload.capabilities === 'object' ? payload.capabilities : payload;
    if (!Array.isArray(value.kinds) || value.kinds.length > 200 || !Array.isArray(value.agents) || value.agents.length > 200 ||
        typeof value.policy_revision !== 'string' || value.policy_revision.length > 256 ||
        typeof value.can_claim !== 'boolean' || typeof value.managed_available !== 'boolean' ||
        typeof value.daily_available !== 'boolean' || typeof value.can_submit !== 'boolean' ||
        value.managed_reason_code !== undefined && (typeof value.managed_reason_code !== 'string' || value.managed_reason_code.length > 96 || value.managed_reason_code && !/^[A-Za-z0-9_.-]+$/.test(value.managed_reason_code)) ||
        value.daily_reason_code !== undefined && (typeof value.daily_reason_code !== 'string' || value.daily_reason_code.length > 96 || value.daily_reason_code && !/^[A-Za-z0-9_.-]+$/.test(value.daily_reason_code)) ||
        !value.managed_available && (value.can_submit || value.can_claim)) {
      throw new TaskCenterAPIError(200, 'INVALID_CAPABILITIES');
    }
    const kinds = [];
    const kindIDs = new Set();
    for (const kind of value.kinds) {
      if (!kind || typeof kind !== 'object' || !safeID(kind.kind_id) || typeof kind.label !== 'string' ||
          kind.label.length > 240 || !Array.isArray(kind.allowed_agents) || kind.allowed_agents.length > 200 || kindIDs.has(kind.kind_id) ||
          !kind.allowed_agents.every(safeID) ||
          kind.executor_available !== undefined && typeof kind.executor_available !== 'boolean' ||
          kind.requires_typed_input !== undefined && typeof kind.requires_typed_input !== 'boolean' ||
          kind.unavailable_reason !== undefined && (typeof kind.unavailable_reason !== 'string' ||
            kind.unavailable_reason.length > 96 || kind.unavailable_reason && !/^[A-Za-z0-9_.-]+$/.test(kind.unavailable_reason))) {
        throw new TaskCenterAPIError(200, 'INVALID_CAPABILITIES');
      }
      kindIDs.add(kind.kind_id);
      const schemaResult = Object.prototype.hasOwnProperty.call(kind, 'input_schema')
        ? validateKindInputSchema(kind.input_schema, kind.kind_id)
        : {state: 'missing', schema: null};
      kinds.push({
        kind_id: kind.kind_id,
        label: kind.label,
        allowed_agents: [...kind.allowed_agents],
        input_schema: schemaResult.schema,
        input_schema_state: schemaResult.state,
        requires_typed_input: kind.requires_typed_input === true,
        executor_available: kind.executor_available === true,
        executor_state_known: typeof kind.executor_available === 'boolean',
        unavailable_reason: typeof kind.unavailable_reason === 'string' ? kind.unavailable_reason : '',
      });
    }
    if (!value.agents.every(safeID) || new Set(value.agents).size !== value.agents.length) {
      throw new TaskCenterAPIError(200, 'INVALID_CAPABILITIES');
    }
    return {
      kinds,
      agents: [...value.agents],
      policy_revision: value.policy_revision,
      owner_ref: typeof value.owner_ref === 'string' ? value.owner_ref : '',
      revision: typeof value.revision === 'string' || typeof value.revision === 'number' ? value.revision : '',
      can_claim: value.can_claim,
      actor_kind: typeof value.actor_kind === 'string' ? value.actor_kind : '',
      managed_available: value.managed_available,
      daily_available: value.daily_available,
      can_submit: value.can_submit,
      managed_reason_code: typeof value.managed_reason_code === 'string' ? value.managed_reason_code : '',
      daily_reason_code: typeof value.daily_reason_code === 'string' ? value.daily_reason_code : '',
    };
  }

  function validateItem(item) {
    if (!item || typeof item !== 'object' || Array.isArray(item) || !safeID(item.goal_id) ||
        typeof item.title !== 'string' || (item.description !== undefined && typeof item.description !== 'string') || typeof item.kind_id !== 'string' ||
        typeof item.status !== 'string' || !Number.isSafeInteger(item.revision) || item.revision <= 0 ||
        !Number.isSafeInteger(item.assignment_epoch) || item.assignment_epoch < 0 ||
        !Array.isArray(item.allowed_agents) || !item.allowed_agents.every((id) => typeof id === 'string')) {
      throw new TaskCenterAPIError(200, 'INVALID_ITEM');
    }
    const validated = {...item, description: typeof item.description === 'string' ? item.description : ''};
    for (const field of ['result_artifact_id', 'result_sha256', 'execution_verification']) {
      if (Object.prototype.hasOwnProperty.call(item, field) && typeof item[field] !== 'string') {
        throw new TaskCenterAPIError(200, 'INVALID_ITEM_RESULT');
      }
    }
    if (Object.prototype.hasOwnProperty.call(item, 'can_accept_result') && typeof item.can_accept_result !== 'boolean') {
      throw new TaskCenterAPIError(200, 'INVALID_ITEM_RESULT');
    }
    for (const field of ['success_criteria', 'verification']) {
      if (!Object.prototype.hasOwnProperty.call(item, field)) continue;
      if (!Array.isArray(item[field]) || !item[field].every((line) => typeof line === 'string')) {
        throw new TaskCenterAPIError(200, 'INVALID_ITEM_DETAIL');
      }
      validated[field] = [...item[field]];
    }
    return validated;
  }

  function validateResultView(payload, item) {
    if (!isObject(payload) || !safeID(payload.goal_id) || !Number.isSafeInteger(payload.revision) ||
        !Number.isSafeInteger(payload.assignment_epoch) || payload.assignment_epoch < 0 ||
        !safeID(payload.result_artifact_id) || typeof payload.result_sha256 !== 'string' || !/^[a-f0-9]{64}$/i.test(payload.result_sha256) ||
        typeof payload.execution_verification !== 'string' || payload.execution_verification.length > 96 ||
        !Object.prototype.hasOwnProperty.call(payload, 'result') || payload.goal_id !== item.goal_id ||
        payload.revision !== item.revision || payload.assignment_epoch !== item.assignment_epoch ||
        payload.result_artifact_id !== item.result_artifact_id || payload.result_sha256 !== item.result_sha256 ||
        payload.execution_verification !== item.execution_verification) {
      throw new TaskCenterAPIError(200, 'INVALID_RESULT_VIEW');
    }
    let encoded;
    try { encoded = JSON.stringify(payload.result); } catch (_) { throw new TaskCenterAPIError(200, 'INVALID_RESULT_VIEW'); }
    if (typeof encoded !== 'string' || new TextEncoder().encode(encoded).byteLength > 48 * 1024) {
      throw new TaskCenterAPIError(200, 'INVALID_RESULT_VIEW');
    }
    return {...payload, result_text: encoded};
  }

  function acceptedResultMatches(result, pending) {
    const item = result && result.item;
    return Boolean(item && item.goal_id === pending.goalID && item.revision === pending.expectedRevision + 1 &&
      item.assignment_epoch === pending.expectedAssignmentEpoch && item.result_artifact_id === pending.resultArtifactID &&
      item.result_sha256 === pending.resultSHA256 && item.execution_verification === 'accepted' && item.status === 'completed');
  }

  function validateList(payload) {
    if (!Array.isArray(payload.items) || payload.items.length > PAGE_LIMIT || typeof payload.partial !== 'boolean' ||
        !(typeof payload.next_cursor === 'string' || payload.next_cursor === null || payload.next_cursor === undefined) ||
        typeof payload.policy_revision !== 'string') {
      throw new TaskCenterAPIError(200, 'INVALID_LIST');
    }
    const items = payload.items.map((candidate) => {
      const item = validateItem(candidate);
      const summary = {...item};
      delete summary.success_criteria;
      delete summary.verification;
      return summary;
    });
    return {...payload, items, next_cursor: payload.next_cursor || ''};
  }

  function isObject(value) {
    return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
  }

  function validTimestamp(value) {
    return typeof value === 'string' && value.length <= 64 && !Number.isNaN(new Date(value).getTime());
  }

  function validDailyTimestamp(value) {
    return typeof value === 'string' && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value) && validTimestamp(value);
  }

  function validDailyCode(value) {
    return typeof value === 'string' && value.length <= 64 && /^[a-z0-9][a-z0-9._-]*$/.test(value);
  }

  function dailyCanonicalID(value) {
    return typeof value === 'string' && value.length <= 512 && value === value.trim() && value === value.toLowerCase() && /^[a-z0-9._-]+$/.test(value);
  }

  function dailySafeReference(value) {
    return typeof value === 'string' && value.length <= 512 && value === value.trim() && !value.startsWith('/') &&
      !value.includes('://') && !value.includes('..') && !/[\\?#]/.test(value) && /^[A-Za-z0-9._:/-]+$/.test(value);
  }

  function dailySafeDisplayReference(value) {
    return dailySafeReference(value) && !/\b(?:bearer|password|passwd|secret|token|credential|api[_ -]?key)\b/i.test(value) ? value : '';
  }

  function dailySafeDisplayID(value) {
    return dailyCanonicalID(value) && !/\b(?:bearer|password|passwd|secret|token|credential|api[_ -]?key)\b/i.test(value) ? value : '';
  }

  function validateDailyValue(value, validateValue) {
    if (!isObject(value) || !DAILY_VALUE_STATES.includes(value.state) || !dailySafeReference(value.source_ref) || !('value' in value) ||
        value.reason_code !== undefined && !validDailyCode(value.reason_code) ||
        value.state === 'known' && (value.value === null || !validateValue(value.value)) ||
        value.state !== 'known' && value.value !== null) {
      throw new TaskCenterAPIError(200, 'INVALID_DAILY_VALUE');
    }
    return value;
  }

  function validateDailyAxis(axis, axisName) {
    const allowed = Object.keys(DAILY_AXIS_LABELS[axisName] || {});
    if (!isObject(axis) || !allowed.includes(axis.state) || !dailySafeReference(axis.source_ref) ||
        axis.reason_code !== undefined && !validDailyCode(axis.reason_code)) {
      throw new TaskCenterAPIError(200, 'INVALID_DAILY_AXIS');
    }
    return axis;
  }

  function safeSymbolicReference(value) {
    return dailySafeDisplayReference(value);
  }

  function safeFilename(value) {
    if (typeof value !== 'string' || !value || value.length > 240 || value === '.' || value === '..' || value.includes('..') ||
        value.includes('/') || value.includes('\\') || value.includes('@') || value.includes('://') || value.startsWith('~/') ||
        /\b(?:bearer|password|passwd|secret|token|credential|api[_ -]?key)\b/i.test(value) || /[\u0000-\u001f]/.test(value)) return '';
    return value;
  }

  function safeOwnerReference(value) {
    return dailySafeDisplayReference(value);
  }

  function validateDailyResourceRef(ref) {
    if (!isObject(ref) || !dailySafeReference(ref.owner_ref) || !dailyCanonicalID(ref.resource_type)) {
      throw new TaskCenterAPIError(200, 'INVALID_DAILY_REFERENCE');
    }
    for (const key of ['source_id', 'reference', 'native_id', 'data_scope', 'revision', 'display_name', 'filename', 'link_ref']) {
      if (ref[key] !== undefined && (typeof ref[key] !== 'string' || ref[key].length > 1024)) {
        throw new TaskCenterAPIError(200, 'INVALID_DAILY_REFERENCE');
      }
    }
    if (ref.source_id !== undefined && ref.source_id !== '' && !dailyCanonicalID(ref.source_id) ||
        ref.native_id !== undefined && ref.native_id !== '' && !dailySafeReference(ref.native_id) ||
        ref.data_scope !== undefined && ref.data_scope !== '' && !['public', 'user', 'internal'].includes(ref.data_scope)) {
      throw new TaskCenterAPIError(200, 'INVALID_DAILY_REFERENCE');
    }
    return {
      source_id: typeof ref.source_id === 'string' ? ref.source_id : '',
      owner_ref: ref.owner_ref,
      resource_type: ref.resource_type,
      reference: safeSymbolicReference(ref.reference),
      native_id: typeof ref.native_id === 'string' ? ref.native_id : '',
      data_scope: typeof ref.data_scope === 'string' ? ref.data_scope : '',
      revision: typeof ref.revision === 'string' ? ref.revision : '',
      display_name: typeof ref.display_name === 'string' ? ref.display_name : '',
      filename: safeFilename(ref.filename),
      link_ref: safeSymbolicReference(ref.link_ref),
    };
  }

  function validateDailyRefs(refs) {
    if (!Array.isArray(refs) || refs.length > 500) throw new TaskCenterAPIError(200, 'INVALID_DAILY_REFERENCES');
    return refs.map(validateDailyResourceRef);
  }

  function validateDailyTimeWindow(window) {
    return isObject(window) && typeof window.start === 'string' && /^([01]\d|2[0-3]):[0-5]\d$/.test(window.start) &&
      typeof window.end === 'string' && /^([01]\d|2[0-3]):[0-5]\d$/.test(window.end) && window.start !== window.end;
  }

  function validateDailyRecurrence(value) {
    if (!isObject(value) || !['none', 'daily', 'weekly', 'interval'].includes(value.frequency) ||
        !Array.isArray(value.times) || value.times.length > 100 || !value.times.every((time) => typeof time === 'string' && /^([01]\d|2[0-3]):[0-5]\d$/.test(time)) ||
        new Set(value.times).size !== value.times.length || !Array.isArray(value.weekdays) || value.weekdays.length > 7 ||
        !value.weekdays.every((day) => Number.isInteger(day) && day >= 0 && day <= 6) || new Set(value.weekdays).size !== value.weekdays.length ||
        !(value.interval_seconds === null || Number.isSafeInteger(value.interval_seconds) && value.interval_seconds > 0)) return false;
    if (value.frequency === 'none') return value.times.length === 0 && value.weekdays.length === 0 && value.interval_seconds === null;
    if (value.frequency === 'daily') return value.times.length > 0 && value.weekdays.length === 0 && value.interval_seconds === null;
    if (value.frequency === 'weekly') return value.times.length > 0 && value.weekdays.length > 0 && value.interval_seconds === null;
    return value.times.length === 0 && value.weekdays.length === 0 && value.interval_seconds !== null;
  }

  function validateDailyIssue(issue) {
    if (!isObject(issue) || !dailyCanonicalID(issue.source_id) ||
        issue.owner_ref !== undefined && issue.owner_ref !== '' && !dailySafeReference(issue.owner_ref) ||
        !validDailyCode(issue.code) || !issue.code) {
      throw new TaskCenterAPIError(200, 'INVALID_DAILY_ISSUE');
    }
    return issue;
  }

  function validateDailyIssues(issues) {
    if (!Array.isArray(issues) || issues.length > 500) throw new TaskCenterAPIError(200, 'INVALID_DAILY_ISSUES');
    return issues.map(validateDailyIssue);
  }

  function validateDailyEvidence(evidence) {
    if (!isObject(evidence) || !DAILY_VALUE_STATES.includes(evidence.state) ||
        !Array.isArray(evidence.refs) || evidence.refs.length > 500) {
      throw new TaskCenterAPIError(200, 'INVALID_DAILY_EVIDENCE');
    }
    validateDailyValue(evidence.last_attempt_at, validDailyTimestamp);
    validateDailyValue(evidence.last_success_at, validDailyTimestamp);
    validateDailyValue(evidence.last_result, (value) => ['success', 'failure', 'partial', 'running', 'missed', 'unknown'].includes(value));
    return {...evidence, refs: validateDailyRefs(evidence.refs)};
  }

  function validateDailyOccurrence(occurrence) {
    const states = ['scheduled', 'running', 'succeeded', 'failed', 'partial', 'missed', 'unknown', 'unavailable'];
    if (!isObject(occurrence) || !dailySafeReference(occurrence.occurrence_id) || !states.includes(occurrence.state)) {
      throw new TaskCenterAPIError(200, 'INVALID_DAILY_OCCURRENCE');
    }
    if (Object.hasOwn(occurrence, 'scheduled_at')) {
      validateDailyValue(occurrence.scheduled_at, validDailyTimestamp);
    }
    validateDailyValue(occurrence.next_run_at, validDailyTimestamp);
    validateDailyValue(occurrence.last_attempt_at, validDailyTimestamp);
    validateDailyValue(occurrence.last_success_at, validDailyTimestamp);
    validateDailyValue(occurrence.result, (value) => ['success', 'failure', 'partial', 'running', 'missed', 'unknown'].includes(value));
    const validated = {...occurrence, native_refs: validateDailyRefs(occurrence.native_refs)};
    if (Object.hasOwn(occurrence, 'meeting_progress')) validated.meeting_progress = validateDailyMeetingProgress(occurrence.meeting_progress);
    return validated;
  }

  function validateDailyProgressCount(progress) {
    if (!isObject(progress)) throw new TaskCenterAPIError(200, 'INVALID_DAILY_MEETING_PROGRESS');
    validateDailyValue(progress.current, (value) => Number.isSafeInteger(value) && value >= 0);
    validateDailyValue(progress.total, (value) => Number.isSafeInteger(value) && value >= 0);
    if (progress.current.state === 'known' && progress.total.state === 'known' && progress.current.value > progress.total.value) {
      throw new TaskCenterAPIError(200, 'INVALID_DAILY_MEETING_PROGRESS');
    }
    return progress;
  }

  function validateDailyMeetingProgress(progress) {
    if (!isObject(progress)) throw new TaskCenterAPIError(200, 'INVALID_DAILY_MEETING_PROGRESS');
    const attendance = validateDailyProgressCount(progress.attendance);
    const deliveredReports = validateDailyProgressCount(progress.delivered_reports);
    const answeredConcerns = validateDailyProgressCount(progress.answered_concerns);
    validateDailyValue(progress.unresolved_concerns, (value) => Number.isSafeInteger(value) && value >= 0);
    if (progress.unresolved_concerns.state === 'known' && answeredConcerns.total.state === 'known' &&
        progress.unresolved_concerns.value > answeredConcerns.total.value) {
      throw new TaskCenterAPIError(200, 'INVALID_DAILY_MEETING_PROGRESS');
    }
    let minutesValue = null;
    validateDailyValue(progress.minutes, (value) => {
      minutesValue = validateDailyResourceRef(value);
      return true;
    });
    return {
      ...progress,
      attendance,
      delivered_reports: deliveredReports,
      answered_concerns: answeredConcerns,
      minutes: {...progress.minutes, value: minutesValue},
    };
  }

  function validateDailyDefinition(definition) {
    if (!isObject(definition) || !dailyCanonicalID(definition.source_id) || !dailyCanonicalID(definition.definition_id) || !dailyCanonicalID(definition.kind_id) ||
        typeof definition.label !== 'string' || definition.label.length > 512 ||
        !dailySafeReference(definition.owner_ref) || !dailyCanonicalID(definition.resource_type) ||
        !['public', 'user', 'internal'].includes(definition.data_scope) ||
        typeof definition.native_refs_truncated !== 'boolean' || typeof definition.occurrences_truncated !== 'boolean' ||
        !Array.isArray(definition.occurrences) || definition.occurrences.length > 200) {
      throw new TaskCenterAPIError(200, 'INVALID_DAILY_DEFINITION');
    }
    validateDailyAxis(definition.implementation, 'implementation');
    validateDailyAxis(definition.configured, 'configured');
    validateDailyAxis(definition.effective, 'effective');
    validateDailyAxis(definition.observed, 'observed');
    validateDailyValue(definition.source_observed_at, validDailyTimestamp);
    validateDailyValue(definition.next_run_at, validDailyTimestamp);
    validateDailyValue(definition.target_count, (value) => Number.isSafeInteger(value) && value >= 0);
    validateDailyValue(definition.native_ref_count, (value) => Number.isSafeInteger(value) && value >= 0);
    validateDailyValue(definition.occurrence_count, (value) => Number.isSafeInteger(value) && value >= 0);
    const timing = definition.timing;
    if (!isObject(timing)) throw new TaskCenterAPIError(200, 'INVALID_DAILY_TIMING');
    validateDailyValue(timing.timezone, (value) => typeof value === 'string' && value.length <= 128 && validTimezone(value));
    validateDailyValue(timing.scheduled_start_at, validDailyTimestamp);
    validateDailyValue(timing.not_before, validDailyTimestamp);
    validateDailyValue(timing.allowed_windows, (value) => Array.isArray(value) && value.length <= 100 && value.every(validateDailyTimeWindow));
    validateDailyValue(timing.deadline_at, validDailyTimestamp);
    validateDailyValue(timing.recurrence, validateDailyRecurrence);
    validateDailyValue(timing.estimated_duration_seconds, (value) => Number.isSafeInteger(value) && value >= 0);
    if (!Array.isArray(definition.source_refs) || definition.source_refs.length > 500 ||
        !Array.isArray(definition.native_refs) || definition.native_refs.length > 500) {
      throw new TaskCenterAPIError(200, 'INVALID_DAILY_DEFINITION');
    }
    const dailyIssues = validateDailyIssues(definition.issues);
    const occurrences = definition.occurrences.map(validateDailyOccurrence);
    return {
      ...definition,
      source_refs: validateDailyRefs(definition.source_refs),
      native_refs: validateDailyRefs(definition.native_refs),
      latest_evidence: validateDailyEvidence(definition.latest_evidence),
      occurrences,
      issues: dailyIssues,
    };
  }

  function validateDailyList(payload) {
    if (!Array.isArray(payload.definitions) || payload.definitions.length > 100 ||
        !(typeof payload.next_cursor === 'string' || payload.next_cursor === null || payload.next_cursor === undefined) ||
        typeof payload.partial !== 'boolean' || !['available', 'partial', 'unavailable'].includes(payload.availability) ||
        !Array.isArray(payload.issues) || !validDailyTimestamp(payload.observed_at)) {
      throw new TaskCenterAPIError(200, 'INVALID_DAILY_LIST');
    }
    return {
      ...payload,
      definitions: payload.definitions.map(validateDailyDefinition),
      next_cursor: payload.next_cursor || '',
      issues: validateDailyIssues(payload.issues),
      partial: payload.partial || payload.availability === 'partial',
    };
  }

  function formatTime(value) {
    if (typeof value !== 'string' || !value) return '未確認';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return value;
    return new Intl.DateTimeFormat('ja-JP', {timeZone: 'Asia/Tokyo', dateStyle: 'medium', timeStyle: 'short'}).format(date);
  }

  function dailyValueStateLabel(value) {
    return ({known: 'known（値あり）', none: 'none（明示的な指定なし）', unknown: 'unknown（未確認）', unsupported: 'unsupported（owner未対応）'})[value] || '未確認';
  }

  function displayDailyValue(value, formatter = (entry) => cleanText(entry)) {
    let display = dailyValueStateLabel(value.state);
    if (value.state === 'known') display = `${formatter(value.value)}（known）`;
    const source = safeOwnerReference(value.source_ref);
    if (source) display += `\nsource: ${source}`;
    if (value.reason_code) display += `\n理由コード: ${value.reason_code}`;
    return display;
  }

  function displayDailyAxis(name, axis) {
    const state = DAILY_AXIS_LABELS[name][axis.state] || '未確認';
    const source = safeOwnerReference(axis.source_ref);
    return `${state}（${axis.state}）${source ? ` · source: ${source}` : ''}${axis.reason_code ? ` · 理由コード: ${axis.reason_code}` : ''}`;
  }

  function formatDailyWindowList(windows) {
    if (!windows.length) return '0件（knownの空配列）';
    return windows.map((window) => `${window.start}–${window.end}`).join('、');
  }

  function formatDailyRecurrence(recurrence) {
    const frequencies = {none: '繰り返しなし', daily: '毎日', weekly: '毎週', interval: '一定間隔'};
    const frequency = frequencies[recurrence.frequency] || '未確認の頻度';
    const times = recurrence.times.length ? `時刻 ${recurrence.times.join('、')}` : '時刻 0件';
    const weekdays = recurrence.weekdays.length
      ? `曜日 ${recurrence.weekdays.map((day) => ['日', '月', '火', '水', '木', '金', '土'][day]).join('・')}`
      : '曜日 0件';
    const interval = recurrence.interval_seconds === null ? '間隔 未指定' : `間隔 ${recurrence.interval_seconds}秒`;
    return `${frequency} · ${times} · ${weekdays} · ${interval}`;
  }

  function displayDailyTimingValue(field, wrapper) {
    switch (field) {
      case 'timezone': return displayDailyValue(wrapper, (value) => validTimezone(value) ? value : 'timezone値を確認できません');
      case 'scheduled_start_at':
      case 'not_before':
      case 'deadline_at': return displayDailyValue(wrapper, formatTime);
      case 'allowed_windows': return displayDailyValue(wrapper, formatDailyWindowList);
      case 'recurrence': return displayDailyValue(wrapper, formatDailyRecurrence);
      case 'estimated_duration_seconds': return displayDailyValue(wrapper, (value) => `${value}秒`);
      default: return dailyValueStateLabel(wrapper.state);
    }
  }

  function displayDailyOutcome(value) {
    return DAILY_OUTCOME_LABELS[value] || `未確認の結果（${value}）`;
  }

  function displayDailyOccurrenceState(value) {
    return DAILY_OCCURRENCE_LABELS[value] || '状態未確認';
  }

  function displayDailyProgressCount(progress) {
    if (progress.current.state === 'known' && progress.total.state === 'known') {
      return `${progress.current.value} / ${progress.total.value}（known）\ncurrent: ${safeOwnerReference(progress.current.source_ref)} · total: ${safeOwnerReference(progress.total.source_ref)}`;
    }
    return `${displayDailyValue(progress.current)} / ${displayDailyValue(progress.total)}`;
  }

  function displayDailyMeetingMinutes(value) {
    if (value.state !== 'known' || !isObject(value.value)) return displayDailyValue(value);
    const reference = safeOwnerReference(value.value.reference) || 'owner参照未確認';
    const nativeID = safeOwnerReference(value.value.native_id) || 'native ID未確認';
    const source = safeOwnerReference(value.source_ref);
    return `保存済み・参照のみ\n${reference} · ${nativeID}${source ? `\nsource: ${source}` : ''}`;
  }

  function renderDailyMeetingProgress(target, progress) {
    const section = document.createElement('section');
    section.className = 'task-center__meeting-progress';
    const heading = document.createElement('h4');
    heading.textContent = '会議の進捗・議事記録';
    section.append(heading);
    if (!progress) {
      const missing = document.createElement('p');
      missing.textContent = 'この応答では進捗値が未報告です。0件とは判断していません。';
      section.append(missing);
      target.append(section);
      return;
    }
    const grid = document.createElement('dl');
    grid.className = 'task-center__detail-grid';
    const rows = [
      ['出席確認（参加者）', displayDailyProgressCount(progress.attendance)],
      ['報告の送達確認（参加者）', displayDailyProgressCount(progress.delivered_reports)],
      ['回答済み懸念／報告済み懸念', displayDailyProgressCount(progress.answered_concerns)],
      ['未解決の報告済み懸念', displayDailyValue(progress.unresolved_concerns, (value) => `${value}件`)],
      ['議事録', displayDailyMeetingMinutes(progress.minutes)],
    ];
    for (const [label, value] of rows) grid.append(createDetail(label, value));
    const note = document.createElement('p');
    note.textContent = '懸念数はこのcheckpointまでに報告された件数です。未報告者からの懸念を含む最終件数ではありません。議事録は参照情報のみで、本文はこの画面から開きません。';
    section.append(grid, note);
    target.append(section);
  }

  function safeDisplayName(value) {
    if (typeof value !== 'string' || !value || value.length > 256 || value.includes('@') ||
        value.includes('/') || value.includes('\\') || /^(?:[a-z]+:)?\/\//i.test(value) || /[\u0000-\u001f]/.test(value) ||
        /\b(?:bearer|password|passwd|secret|token|credential|api[_ -]?key)\b/i.test(value)) return '';
    return value;
  }

  function dailyDataScopeLabel(value) {
    return ({public: 'public', user: 'user scope', internal: 'internal', private: 'private scope'})[value] || 'scope未確認';
  }

  function validTimezone(value) {
    if (typeof value !== 'string' || value.length > 128) return false;
    try { new Intl.DateTimeFormat('en', {timeZone: value}); return true; } catch (_) { return false; }
  }

  function formatDailyReference(ref) {
    const parts = [];
    const displayName = safeDisplayName(ref.display_name);
    const ownerRef = safeOwnerReference(ref.owner_ref);
    const linkRef = safeSymbolicReference(ref.link_ref);
    const nativeID = dailySafeDisplayReference(ref.native_id);
    const sourceID = dailySafeDisplayID(ref.source_id);
    const filename = safeFilename(ref.filename);
    if (displayName) parts.push(displayName);
    if (filename) parts.push(filename);
    const resourceType = dailySafeDisplayID(ref.resource_type);
    if (resourceType) parts.push(`resource: ${resourceType}`);
    if (ownerRef) parts.push(`owner: ${ownerRef}`);
    if (sourceID) parts.push(`source: ${sourceID}`);
    if (nativeID) parts.push(`native: ${nativeID}`);
    if (ref.data_scope) parts.push(`scope: ${dailyDataScopeLabel(ref.data_scope)}`);
    if (dailySafeDisplayID(ref.revision)) parts.push(`revision: ${ref.revision}`);
    if (linkRef) parts.push(`参照: ${linkRef}`);
    return parts.length ? parts.join(' · ') : '安全に表示できる参照情報はありません';
  }

  function dailyIssueLabel(issue) {
    const ownerRef = safeOwnerReference(issue.owner_ref);
    return `${DAILY_ISSUE_LABELS[issue.code] || 'sourceに関する確認事項'}（${issue.code}）${ownerRef ? ` · ${ownerRef}` : ''}`;
  }

  function statusLabel(value) {
    if (typeof value !== 'string' || !value.trim()) return '状態未確認';
    return STATUS_LABELS[value] || `状態未確認（${value}）`;
  }

  function resultLabel(value) {
    if (typeof value !== 'string' || !value.trim()) return '結果未確認';
    return RESULT_LABELS[value] || `結果未確認（${value}）`;
  }

  function assigneeLabel(value) {
    if (value === 'unassigned') return '未割当';
    return cleanText(value, '未割当または未確認');
  }

  function statusClass(value) {
    if (value === 'completed') return 'task-center__item-status--success';
    if (value === 'paused' || value === 'waiting') return 'task-center__item-status--warning';
    return '';
  }

  function fieldStateLabel(stateValue, value, fieldPresent) {
    const stateLabels = {
      specified: '指定済み', configured: '指定済み', known: 'known', unspecified: '指定なし', not_applicable: '適用なし',
      unconfirmed: '未確認', unknown: '未確認', unavailable: '観測不能', unsupported: 'owner未対応',
    };
    if (typeof stateValue === 'string' && stateLabels[stateValue]) {
      if (stateValue === 'known') {
        if (!fieldPresent) return '未確認';
        if (value === null || value === undefined || value === '' || Array.isArray(value) && value.length === 0) return '指定なし';
        return '指定済み';
      }
      if (['specified', 'configured'].includes(stateValue) && (value === null || value === undefined || value === '')) return '値未確認';
      return stateLabels[stateValue];
    }
    if (!fieldPresent) return '未確認';
    if (value !== null && value !== undefined && value !== '' && !(Array.isArray(value) && value.length === 0)) return '指定済み';
    return '未確認';
  }

  function timingLines(item) {
    if (!item.timing || typeof item.timing !== 'object' || Array.isArray(item.timing)) return ['共通時間項目: 未確認'];
    const timing = item.timing;
    const states = timing.state && typeof timing.state === 'object' ? timing.state :
      timing.states && typeof timing.states === 'object' ? timing.states :
      item.timing_state && typeof item.timing_state === 'object' ? item.timing_state : {};
    const overallTimingStatus = typeof item.timing_status === 'string' ? item.timing_status : '';
    const defaultFieldState = ['known', 'unconfirmed', 'unknown', 'unavailable', 'unsupported'].includes(overallTimingStatus)
      ? overallTimingStatus
      : undefined;
    const fields = [
      ['実行予定', 'scheduled_start_at'], ['開始可能時刻', 'not_before'],
      ['実行可能時間帯', 'allowed_windows'], ['期限', 'deadline_at'],
      ['繰り返し', 'recurrence'], ['想定所要時間', 'estimated_duration_seconds'],
    ];
    const lines = [`timezone: ${cleanText(timing.timezone, '未確認')}`];
    for (const [label, key] of fields) {
      const present = Object.prototype.hasOwnProperty.call(timing, key);
      const value = timing[key];
      const fieldState = fieldStateLabel(states[key] || defaultFieldState, value, present);
      if (fieldState !== '指定済み') {
        lines.push(`${label}: ${fieldState}`);
      } else if (key === 'allowed_windows' && Array.isArray(value)) {
        lines.push(`${label}: ${value.map((window) => `${cleanText(window.start)}–${cleanText(window.end)}`).join('、')}`);
      } else if (key === 'recurrence' && value && typeof value === 'object') {
        const times = Array.isArray(value.times) ? value.times.join(', ') : '';
        const days = Array.isArray(value.weekdays) ? value.weekdays.join(', ') : '';
        lines.push(`${label}: ${cleanText(value.frequency)}${times ? ` · ${times}` : ''}${days ? ` · 曜日 ${days}` : ''}`);
      } else if (key.endsWith('_at')) {
        lines.push(`${label}: ${formatTime(value)}`);
      } else {
        lines.push(`${label}: ${cleanText(value)}`);
      }
    }
    const source = timing.source || timing.source_ref || item.timing_source;
    if (overallTimingStatus) lines.push(`時間項目の確認状態: ${cleanText(overallTimingStatus)}`);
    if (source) lines.push(`時間項目のsource: ${cleanText(source)}`);
    return lines;
  }

  function createDetail(label, value) {
    const wrapper = document.createElement('div');
    wrapper.className = 'task-center__detail';
    const term = document.createElement('dt');
    term.textContent = label;
    const description = document.createElement('dd');
    description.textContent = value;
    wrapper.append(term, description);
    return wrapper;
  }

  function renderDetails(target, item, includeAcceptanceDetails = false) {
    const grid = document.createElement('dl');
    grid.className = 'task-center__detail-grid';
    const assignee = assigneeLabel(item.assignee);
    const observed = cleanText(item.observed_state);
    const effective = cleanText(item.effective_state);
    const detailRows = [
      ['担当', assignee], ['owner状態', statusLabel(item.status)], ['有効状態', effective], ['観測状態', observed],
      ['状態の理由', cleanText(item.reason_code, '理由の記録なし')],
      ['種類', cleanText(item.kind_id)], ['共通時間項目', timingLines(item).join('\n')],
      ['許可Agent', Array.isArray(item.allowed_agents) ? (item.allowed_agents.length ? item.allowed_agents.join(', ') : '未確認') : '未確認'],
      ['作成', formatTime(item.created_at)], ['更新', formatTime(item.updated_at)],
    ];
    if (includeAcceptanceDetails) {
      for (const [field, label] of [['success_criteria', '完了条件'], ['verification', '確認方法']]) {
        if (!Object.prototype.hasOwnProperty.call(item, field)) continue;
        const lines = item[field];
        const value = lines.length ? lines.map((line) => `・${line}`).join('\n') : '記録された項目はありません';
        detailRows.push([label, value]);
      }
      if (item.execution_verification) {
        detailRows.push(['実行結果の確認状態', executionVerificationLabel(item.execution_verification)]);
      }
      if (item.result_artifact_id) detailRows.push(['成果物参照', item.result_artifact_id]);
      if (item.result_sha256) detailRows.push(['成果物SHA-256', item.result_sha256]);
      detailRows.push(['このViewerで成果物を受け付けられるか', item.can_accept_result === true ? '可能（作成者として確認済み）' : '不可または未確認']);
    }
    if (item.workstream_id) detailRows.push(['Workstream参照', cleanText(item.workstream_id)]);
    if (item.task_id) detailRows.push(['Task参照', cleanText(item.task_id)]);
    if (item.run_id) detailRows.push(['Run参照', cleanText(item.run_id)]);
    for (const [label, value] of detailRows) grid.append(createDetail(label, value));
    target.replaceChildren(grid);
  }

  function executionVerificationLabel(value) {
    return ({verification_pending: '実行成功・Goal確認待ち', accepted: '作成者が成果物を確認済み'})[value] || '実行結果の確認状態は未確認';
  }

  function filteredItems() {
    const query = elements.search.value.trim().toLocaleLowerCase('ja-JP');
    if (!query) return state.items;
    return state.items.filter((item) => [item.title, item.description, item.kind_id, item.status, item.assignee]
      .some((value) => typeof value === 'string' && value.toLocaleLowerCase('ja-JP').includes(query)));
  }

  function filteredDailyDefinitions() {
    const query = elements.search.value.trim().toLocaleLowerCase('ja-JP');
    return state.dailyDefinitions.filter((definition) => {
      if (elements.dailyKind.value && definition.kind_id !== elements.dailyKind.value) return false;
      if (!query) return true;
      const searchable = [definition.label, definition.kind_id, definition.owner_ref, definition.resource_type, definition.data_scope,
        definition.implementation.state, definition.configured.state, definition.effective.state, definition.observed.state,
        ...definition.source_refs.flatMap((ref) => [ref.display_name, ref.filename, ref.owner_ref, ref.resource_type])];
      return searchable.some((value) => typeof value === 'string' && value.toLocaleLowerCase('ja-JP').includes(query));
    });
  }

  function dailyAxisBadge(axisName, axis) {
    const badge = document.createElement('span');
    badge.className = 'task-center__daily-axis';
    badge.dataset.axisState = axis.state;
    badge.textContent = `${({implementation: '実装', configured: '設定', effective: '有効状態', observed: '観測'})[axisName]}: ${DAILY_AXIS_LABELS[axisName][axis.state] || '未確認'}`;
    return badge;
  }

  function shortDailyValue(value, formatter = (entry) => cleanText(entry)) {
    if (value.state === 'known') return formatter(value.value);
    return ({none: '明示的な指定なし', unknown: '未確認', unsupported: 'owner未対応'})[value.state] || '未確認';
  }

  function renderDailyReferences(target, title, refs) {
    const section = document.createElement('section');
    section.className = 'task-center__daily-ref-section';
    const heading = document.createElement('h4');
    heading.textContent = `${title}（${refs.length}件）`;
    section.append(heading);
    if (!refs.length) {
      const empty = document.createElement('p');
      empty.textContent = '参照なし（一覧にない情報は未確認です）';
      section.append(empty);
    } else {
      const list = document.createElement('ul');
      for (const ref of refs) {
        const entry = document.createElement('li');
        entry.textContent = formatDailyReference(ref);
        list.append(entry);
      }
      section.append(list);
    }
    target.append(section);
  }

  function renderDailyDefinitionDetails(target, definition) {
    const grid = document.createElement('dl');
    grid.className = 'task-center__detail-grid';
    const rows = [
      ['定義ID', safeOwnerReference(definition.definition_id) || '安全に表示できるIDがありません'],
      ['種類', cleanText(definition.kind_id)],
      ['owner / resource', `${safeOwnerReference(definition.owner_ref) || 'owner参照未確認'} · ${safeOwnerReference(definition.resource_type) || 'resource未確認'}`],
      ['scope', dailyDataScopeLabel(definition.data_scope)],
      ['実装状態', displayDailyAxis('implementation', definition.implementation)],
      ['設定状態', displayDailyAxis('configured', definition.configured)],
      ['有効状態', displayDailyAxis('effective', definition.effective)],
      ['観測状態', displayDailyAxis('observed', definition.observed)],
      ['source観測時刻', displayDailyValue(definition.source_observed_at, formatTime)],
      ['owner次回予定', displayDailyValue(definition.next_run_at, formatTime)],
      ['対象件数', displayDailyValue(definition.target_count, (value) => `${value}件`)],
      ['native参照件数', `${displayDailyValue(definition.native_ref_count, (value) => `${value}件`)}${definition.native_refs_truncated ? '\n表示できる参照は一部です' : ''}`],
      ['occurrence件数', `${displayDailyValue(definition.occurrence_count, (value) => `${value}件`)}${definition.occurrences_truncated ? '\n表示できるoccurrenceは一部です' : ''}`],
    ];
    for (const [field, label] of DAILY_TIMING_FIELDS) rows.push([label, displayDailyTimingValue(field, definition.timing[field])]);
    rows.push(
      ['履歴状態', dailyValueStateLabel(definition.latest_evidence.state)],
      ['直近の試行', displayDailyValue(definition.latest_evidence.last_attempt_at, formatTime)],
      ['直近の成功', displayDailyValue(definition.latest_evidence.last_success_at, formatTime)],
      ['直近結果', displayDailyValue(definition.latest_evidence.last_result, displayDailyOutcome)],
    );
    for (const [label, value] of rows) grid.append(createDetail(label, value));
    target.append(grid);
    renderDailyReferences(target, '正本・設定・履歴参照', definition.source_refs);
    renderDailyReferences(target, 'native resource参照', definition.native_refs);
    renderDailyReferences(target, '履歴evidence参照', definition.latest_evidence.refs);

    const occurrences = document.createElement('section');
    occurrences.className = 'task-center__daily-occurrences';
    const occurrenceHeading = document.createElement('h4');
    occurrenceHeading.textContent = `実在するnative occurrence（${definition.occurrences.length}件表示）`;
    occurrences.append(occurrenceHeading);
    if (!definition.occurrences.length) {
      const noOccurrences = document.createElement('p');
      noOccurrences.textContent = '実在するoccurrence記録は返されていません。定義や次回予定からoccurrenceを推定しません。';
      occurrences.append(noOccurrences);
    }
    for (const occurrence of definition.occurrences) {
      const occurrenceGrid = document.createElement('dl');
      occurrenceGrid.className = 'task-center__detail-grid task-center__daily-occurrence';
      const occurrenceRows = [
        ['occurrence ID', safeOwnerReference(occurrence.occurrence_id) || '安全に表示できるIDがありません'],
        ['occurrence状態', `${displayDailyOccurrenceState(occurrence.state)}（${occurrence.state}）`],
        ['開催予定時刻', Object.hasOwn(occurrence, 'scheduled_at')
          ? displayDailyValue(occurrence.scheduled_at, formatTime)
          : '未確認（この応答では未報告）'],
        ['owner次回予定', displayDailyValue(occurrence.next_run_at, formatTime)],
        ['直近の試行', displayDailyValue(occurrence.last_attempt_at, formatTime)],
        ['直近の成功', displayDailyValue(occurrence.last_success_at, formatTime)],
        ['occurrence結果', displayDailyValue(occurrence.result, displayDailyOutcome)],
      ];
      for (const [label, value] of occurrenceRows) occurrenceGrid.append(createDetail(label, value));
      occurrences.append(occurrenceGrid);
      renderDailyReferences(occurrences, 'occurrence native参照', occurrence.native_refs);
      if (definition.kind_id === TASK_MEETING_DAILY_KIND_ID) {
        renderDailyMeetingProgress(occurrences, occurrence.meeting_progress);
      }
    }
    target.append(occurrences);

    if (definition.issues.length) {
      const issueSection = document.createElement('section');
      issueSection.className = 'task-center__daily-issues';
      const issueHeading = document.createElement('h4');
      issueHeading.textContent = `source確認事項（${definition.issues.length}件）`;
      const issueList = document.createElement('ul');
      for (const issue of definition.issues) {
        const entry = document.createElement('li');
        entry.textContent = dailyIssueLabel(issue);
        issueList.append(entry);
      }
      issueSection.append(issueHeading, issueList);
      target.append(issueSection);
    }
  }

  function renderDailyItems() {
    elements.items.replaceChildren();
    if (state.dailyIssues.length) {
      const issues = document.createElement('section');
      issues.className = 'task-center__daily-issues task-center__daily-issues-summary';
      const heading = document.createElement('h4');
      heading.textContent = `全体のsource確認事項（${state.dailyIssues.length}件）`;
      const list = document.createElement('ul');
      for (const issue of state.dailyIssues) {
        const entry = document.createElement('li');
        entry.textContent = dailyIssueLabel(issue);
        list.append(entry);
      }
      issues.append(heading, list);
      elements.items.append(issues);
    }
    const visibleDefinitions = filteredDailyDefinitions();
    for (const definition of visibleDefinitions) {
      const card = document.createElement('details');
      card.className = 'task-center__item task-center__daily-definition';
      const summary = document.createElement('summary');
      summary.className = 'task-center__item-summary';
      const main = document.createElement('span');
      main.className = 'task-center__item-main';
      const title = document.createElement('span');
      title.className = 'task-center__item-title';
      title.textContent = safeDisplayName(definition.label) || '定義名を安全に表示できません';
      const axes = document.createElement('span');
      axes.className = 'task-center__daily-axes';
      for (const name of ['implementation', 'configured', 'effective', 'observed']) axes.append(dailyAxisBadge(name, definition[name]));
      const meta = document.createElement('span');
      meta.className = 'task-center__item-meta task-center__daily-meta';
      const next = document.createElement('span');
      next.textContent = `次回予定: ${shortDailyValue(definition.next_run_at, formatTime)}`;
      const result = document.createElement('span');
      result.textContent = `直近結果: ${shortDailyValue(definition.latest_evidence.last_result, displayDailyOutcome)}`;
      meta.append(next, result);
      main.append(title, axes, meta);
      const summaryStatus = document.createElement('span');
      summaryStatus.className = 'task-center__daily-summary-state';
      summaryStatus.textContent = dailyAvailabilityLabel(state.dailyAvailability);
      summary.append(main, summaryStatus);
      const details = document.createElement('div');
      details.className = 'task-center__item-detail task-center__daily-detail';
      renderDailyDefinitionDetails(details, definition);
      card.append(summary, details);
      elements.items.append(card);
    }

    elements.empty.hidden = visibleDefinitions.length > 0 || !state.hasListSuccess || Boolean(state.listErrorMessage) || Boolean(state.listController);
    elements.more.hidden = !state.nextCursor || state.dailyDefinitions.length >= MAX_LOADED_ITEMS;
    const availabilityText = dailyAvailabilityLabel(state.dailyAvailability);
    const currentListState = state.dailyAvailability === 'unavailable'
      ? `日常定義sourceは観測不能です。${state.dailyDefinitions.length}件のowner定義を表示しています。空の成功とは扱いません。`
      : state.partial
        ? `日常定義を部分取得しました。${state.dailyDefinitions.length}件を表示中です。source状態: ${availabilityText}。`
        : `日常定義: ${state.dailyDefinitions.length}件を表示中です。source状態: ${availabilityText}。${state.nextCursor ? '続きがあります。' : '次ページはありません。'}`;
    const listState = state.listErrorMessage || [state.listRecoveryMessage, currentListState].filter(Boolean).join(' ');
    elements.listState.textContent = state.token ? listState : '認証して接続すると、ownerの読み取り結果を表示します。';
    elements.listState.classList.toggle('is-error', Boolean(state.listErrorMessage) || state.dailyAvailability === 'unavailable');
    elements.listState.classList.toggle('is-recovered', Boolean(state.listRecoveryMessage) && !state.listErrorMessage);
    const emptyTitle = elements.empty.querySelector('h3');
    const emptyCopy = elements.empty.querySelector('p');
    if (state.dailyAvailability === 'unavailable') {
      emptyTitle.textContent = '日常タスク定義を観測できません';
      emptyCopy.textContent = 'sourceから定義を取得できない状態です。対象が0件の成功結果とは扱っていません。';
    } else if (state.partial) {
      emptyTitle.textContent = '取得できた定義に表示対象がありません';
      emptyCopy.textContent = '一部sourceのみの結果です。未取得sourceに定義がないとは限りません。';
    } else {
      emptyTitle.textContent = 'このfilterに表示できる定義はありません';
      emptyCopy.textContent = 'sourceの取得状態とfilterを確認してください。定義が存在しないことは、ownerの取得結果で確認してください。';
    }
    elements.empty.classList.toggle('is-unavailable', state.dailyAvailability === 'unavailable');
    updateSummary();
    updateBusyControls();
  }

  function renderItems() {
    if (state.view === 'daily') {
      renderDailyItems();
      return;
    }
    elements.items.replaceChildren();
    const visibleItems = filteredItems();
    for (const item of visibleItems) {
      const card = document.createElement('details');
      card.className = 'task-center__item';
      const summary = document.createElement('summary');
      summary.className = 'task-center__item-summary';
      const main = document.createElement('span');
      main.className = 'task-center__item-main';
      const title = document.createElement('span');
      title.className = 'task-center__item-title';
      title.textContent = cleanText(item.title, '名前未確認');
      const description = document.createElement('span');
      description.className = 'task-center__item-description';
      description.textContent = cleanText(item.description, '内容未確認');
      const meta = document.createElement('span');
      meta.className = 'task-center__item-meta';
      const metaTexts = [cleanText(item.kind_id, '種類未確認'), `担当 ${assigneeLabel(item.assignee)}`, `更新 ${formatTime(item.updated_at)}`];
      if (state.view === 'claimable') metaTexts.push(`許可Agent ${item.allowed_agents.length ? item.allowed_agents.join(', ') : '未確認'}`);
      for (const text of metaTexts) {
        const span = document.createElement('span');
        span.textContent = text;
        meta.append(span);
      }
      main.append(title, description, meta);
      const status = document.createElement('span');
      status.className = `task-center__item-status ${statusClass(item.status)}`.trim();
      status.textContent = statusLabel(item.status);
      summary.append(main, status);
      const detail = document.createElement('div');
      detail.className = 'task-center__item-detail';
      const detailFields = document.createElement('div');
      detailFields.className = 'task-center__item-detail-fields';
      renderDetails(detailFields, item);
      const resultReview = document.createElement('section');
      resultReview.className = 'task-center__result-review';
      resultReview.hidden = true;
      const controls = document.createElement('div');
      controls.className = 'task-center__item-controls';
      const detailButton = document.createElement('button');
      detailButton.type = 'button';
      detailButton.className = 'task-center__button task-center__button--quiet';
      detailButton.textContent = 'owner詳細を再取得';
      detailButton.addEventListener('click', () => { void refreshItemDetail(item, detailFields, resultReview, controls, detailButton); });
      controls.append(detailButton);
      if (state.view === 'claimable' && state.capabilities && state.capabilities.can_claim) {
        const claimButton = document.createElement('button');
        claimButton.type = 'button';
        claimButton.className = 'task-center__button task-center__button--primary task-center__claim';
        claimButton.textContent = 'この仕事を引き受ける';
        claimButton.disabled = state.busy || Boolean(state.pendingMutation);
        claimButton.addEventListener('click', () => { void claimItem(item, claimButton); });
        controls.append(claimButton);
      }
      detail.append(detailFields, resultReview, controls);
      card.append(summary, detail);
      elements.items.append(card);
    }
    elements.empty.hidden = visibleItems.length > 0 || !state.hasListSuccess || Boolean(state.listErrorMessage) || Boolean(state.listController);
    elements.more.hidden = !state.nextCursor || state.items.length >= MAX_LOADED_ITEMS;
    const currentListState = state.partial
      ? `部分取得です。現在の${state.items.length}件のみを表示し、全体件数とは扱いません。`
      : `${currentViewLabel()}: ${state.items.length}件を取得済み${state.nextCursor ? '。続きがあります。' : '。次ページはありません。'}`;
    const listState = state.listErrorMessage || [state.listRecoveryMessage, currentListState].filter(Boolean).join(' ');
    elements.listState.textContent = state.token ? listState : '認証して接続すると、ownerの読み取り結果を表示します。';
    elements.listState.classList.toggle('is-error', Boolean(state.listErrorMessage));
    elements.listState.classList.toggle('is-recovered', Boolean(state.listRecoveryMessage) && !state.listErrorMessage);
    const emptyTitle = elements.empty.querySelector('h3');
    const emptyCopy = elements.empty.querySelector('p');
    if (state.partial) {
      emptyTitle.textContent = '取得できた範囲に項目はありません';
      emptyCopy.textContent = '結果は部分取得です。owner全体に項目がないとは限りません。';
    } else {
      emptyTitle.textContent = 'この範囲に表示できる項目はありません';
      emptyCopy.textContent = '取得成功と部分取得の状態を確認してください。空の結果だけでowner全体に項目がないとは限りません。';
    }
    updateSummary();
    updateBusyControls();
  }

  function renderCapabilities() {
    const capabilities = state.capabilities;
    const claimTab = elements.viewButtons.find((button) => button.dataset.taskView === 'claimable');
    if (claimTab) claimTab.textContent = capabilities && capabilities.can_claim ? '引受可能' : '引受待ち';
    elements.kind.replaceChildren();
    const placeholder = document.createElement('option');
    placeholder.value = '';
    placeholder.textContent = capabilities && capabilities.kinds.length ? '種類を選択' : '追加可能な種類を確認できません';
    elements.kind.append(placeholder);
    if (capabilities) {
      for (const kind of capabilities.kinds) {
        const option = document.createElement('option');
        option.value = kind.kind_id;
        option.textContent = kind.label;
        elements.kind.append(option);
      }
    }
    elements.assignee.replaceChildren();
    const allOption = document.createElement('option');
    allOption.value = 'all';
    allOption.textContent = '認証scope内すべて';
    elements.assignee.append(allOption);
    const unassignedOption = document.createElement('option');
    unassignedOption.value = 'unassigned';
    unassignedOption.textContent = '未割当';
    elements.assignee.append(unassignedOption);
    for (const agent of (state.capabilities ? state.capabilities.agents : [])) {
      const option = document.createElement('option');
      option.value = agent;
      option.textContent = agent;
      elements.assignee.append(option);
    }
    const dailyOption = elements.dailyKind.value;
    elements.dailyKind.replaceChildren();
    const allDailyKinds = document.createElement('option');
    allDailyKinds.value = '';
    allDailyKinds.textContent = 'すべての定義種類';
    elements.dailyKind.append(allDailyKinds);
    const dailyKinds = new Set([...state.dailyKinds, ...state.dailyDefinitions.map((definition) => definition.kind_id)]);
    if (dailyOption) dailyKinds.add(dailyOption);
    for (const kindID of [...dailyKinds].sort()) {
      const option = document.createElement('option');
      option.value = kindID;
      option.textContent = kindID;
      elements.dailyKind.append(option);
    }
    elements.dailyKind.value = [...elements.dailyKind.options].some((option) => option.value === dailyOption) ? dailyOption : '';

    let modeMessage = '';
    if (capabilities && !capabilities.managed_available && capabilities.daily_available) {
      const reason = capabilities.managed_reason_code === 'managed_owner_unavailable'
        ? 'Goal管理ownerが利用できません。'
        : `Goal管理ownerは利用できません（${capabilities.managed_reason_code || '理由未確認'}）。`;
      modeMessage = `読み取り専用の日常タスク定義を表示しています。${reason}残タスク・引受待ち・履歴と追加は無効です。`;
    } else if (capabilities && !capabilities.managed_available) {
      modeMessage = `Goal管理ownerと日常タスクsourceが利用できません（${capabilities.managed_reason_code || capabilities.daily_reason_code || '理由未確認'}）。一覧の空成功とは扱いません。`;
    } else if (capabilities && !capabilities.can_submit) {
      modeMessage = `タスク追加は現在このscopeで利用できません${capabilities.managed_reason_code ? `（${capabilities.managed_reason_code}）` : ''}。一覧の確認は続けられます。`;
    } else if (capabilities && !capabilities.daily_available) {
      modeMessage = `日常タスク定義は利用できません（${capabilities.daily_reason_code || '理由未確認'}）。`;
    }
    elements.modeNotice.textContent = modeMessage;
    elements.modeNotice.hidden = !modeMessage;

    for (const button of elements.viewButtons) {
      const view = button.dataset.taskView;
      const active = view === state.view;
      const managedView = view !== 'daily';
      const unavailable = Boolean(capabilities) && (managedView ? !capabilities.managed_available : !capabilities.daily_available && capabilities.managed_available);
      button.disabled = unavailable;
      button.setAttribute('aria-disabled', unavailable ? 'true' : 'false');
      button.classList.toggle('is-active', active);
      button.setAttribute('aria-selected', active ? 'true' : 'false');
      if (unavailable) button.title = managedView
        ? `managed owner unavailable: ${capabilities.managed_reason_code || 'unknown'}`
        : `daily source unavailable: ${capabilities.daily_reason_code || 'unknown'}`;
      else button.removeAttribute('title');
    }
    elements.assigneeField.hidden = state.view === 'daily';
    elements.assignee.disabled = state.view === 'claimable' || state.view === 'daily';
    elements.dailyKindField.hidden = state.view !== 'daily';
    elements.listTitle.textContent = state.view === 'daily' ? '日常タスク定義を確認' : '仕事を確認';
    elements.searchLabel.textContent = state.view === 'daily' ? '日常タスク定義を検索' : '表示中のページを検索';
    elements.search.placeholder = state.view === 'daily' ? '定義名・種類・owner' : 'タスク名・内容・状態';
    elements.addDetails.hidden = !capabilities || !capabilities.managed_available || !capabilities.can_submit;
    if (capabilities && capabilities.managed_available && !capabilities.can_submit) {
      elements.modeNotice.textContent = `タスク追加は現在このscopeで利用できません${capabilities.managed_reason_code ? `（${capabilities.managed_reason_code}）` : ''}。一覧の確認は続けられます。`;
      elements.modeNotice.hidden = false;
    }
    updateKindAssignees();
    updateSummary();
  }

  function updateKindAssignees(resetInput = false) {
    const kind = state.capabilities && state.capabilities.kinds.find((entry) => entry.kind_id === elements.kind.value);
    const current = elements.assigneeInput.value || 'unassigned';
    elements.assigneeInput.replaceChildren();
    const unassigned = document.createElement('option');
    unassigned.value = 'unassigned';
    unassigned.textContent = '未割当（Agentの引受待ち）';
    elements.assigneeInput.append(unassigned);
    if (kind) {
      for (const agent of kind.allowed_agents) {
        const option = document.createElement('option');
        option.value = agent;
        option.textContent = agent;
        elements.assigneeInput.append(option);
      }
    }
    elements.assigneeInput.value = [...elements.assigneeInput.options].some((option) => option.value === current) ? current : 'unassigned';
    if (!kind) {
      elements.kindSupport.textContent = '登録された種類を選択してください。種類の登録だけでは実行開始を意味しません。';
      renderKindInputFields(null, true);
      updateBusyControls();
      return;
    }
    const executorState = !kind.executor_state_known ? '実行先の登録状態は未確認です。'
      : kind.executor_available ? '実行先ownerが登録されています。追加や引受は実行開始・成功とは別です。'
        : `実行先は未接続のため、この追加は計画登録です。実行開始・成功を意味しません。${kind.unavailable_reason ? ` 理由: ${kind.unavailable_reason}` : ''}`;
    const allowed = kind.allowed_agents.length ? kind.allowed_agents.join('、') : '許可Agentなし';
    const schemaState = kind.input_schema_state === 'ready' ? 'ownerの入力項目を表示します。'
      : kind.input_schema_state === 'missing' && !kind.requires_typed_input ? '入力項目のmetadataはありません。'
        : kind.input_schema_state === 'unsupported' ? '入力項目の形式に未対応のため、この種類は追加できません。'
          : kind.input_schema_state === 'invalid' ? '入力項目のmetadataを検証できないため、この種類は追加できません。'
            : '必須入力項目を確認できないため、この種類は追加できません。';
    elements.kindSupport.textContent = `${executorState} 許可Agent: ${allowed}。${schemaState}`;
    renderKindInputFields(kind, resetInput);
    updateBusyControls();
  }

  function kindCanSubmit(kind) {
    if (!kind) return false;
    if (kind.input_schema_state === 'ready') {
      return Boolean(kind.input_schema && (!kind.requires_typed_input || kind.input_schema.fields.length > 0));
    }
    return kind.input_schema_state === 'missing' && !kind.requires_typed_input;
  }

  function schemaDraftValue(field) {
    if (Object.prototype.hasOwnProperty.call(state.kindInputDraft, field.name)) return String(state.kindInputDraft[field.name]);
    return Object.prototype.hasOwnProperty.call(field, 'default') ? String(field.default) : KIND_INPUT_UNSET;
  }

  function makeSchemaSelect(field, options, initialValue) {
    const select = document.createElement('select');
    select.dataset.kindInputName = field.name;
    const placeholder = document.createElement('option');
    placeholder.value = KIND_INPUT_UNSET;
    placeholder.textContent = field.required ? '選択してください' : '指定しない';
    select.append(placeholder);
    for (const optionValue of options) {
      const option = document.createElement('option');
      option.value = optionValue.value;
      option.textContent = optionValue.label;
      select.append(option);
    }
    if ([...select.options].some((option) => option.value === initialValue)) select.value = initialValue;
    else select.value = KIND_INPUT_UNSET;
    return select;
  }

  function renderKindInputFields(kind, reset) {
    if (reset) state.kindInputDraft = Object.create(null);
    const schema = kind && kind.input_schema_state === 'ready' ? kind.input_schema : null;
    elements.kindInputFields.replaceChildren();
    elements.kindInputFields.hidden = !schema || schema.fields.length === 0;
    if (!schema || !schema.fields.length) return;

    for (const field of schema.fields) {
      const wrapper = document.createElement('label');
      wrapper.className = 'task-center__field';
      const label = document.createElement('span');
      label.textContent = field.label;
      let control;
      const initialValue = schemaDraftValue(field);
      if (field.type === 'enum') {
        control = makeSchemaSelect(field, field.options, initialValue);
      } else if (field.type === 'enum_by_kind') {
        const controller = schema.fields.find((candidate) => candidate.name === field.depends_on);
        const controllerValue = controller ? schemaDraftValue(controller) : KIND_INPUT_UNSET;
        const options = field.options_by_kind[controllerValue] || [];
        control = makeSchemaSelect(field, options, initialValue);
        control.disabled = options.length === 0;
      } else if (field.type === 'integer') {
        control = document.createElement('input');
        control.type = 'number';
        control.step = '1';
        control.min = String(field.minimum);
        control.max = String(field.maximum);
        control.dataset.kindInputName = field.name;
        if (initialValue !== KIND_INPUT_UNSET) control.value = initialValue;
      } else {
        control = document.createElement('input');
        control.type = 'text';
        control.maxLength = 4096;
        control.dataset.kindInputName = field.name;
        if (initialValue !== KIND_INPUT_UNSET) control.value = initialValue;
      }
      control.required = field.required;
      wrapper.append(label, control);
      if (field.help) {
        const help = document.createElement('small');
        help.textContent = field.help;
        wrapper.append(help);
      }
      const remember = () => {
        state.kindInputDraft[field.name] = control.value;
        for (const dependent of schema.fields.filter((candidate) => candidate.type === 'enum_by_kind' && candidate.depends_on === field.name)) {
          delete state.kindInputDraft[dependent.name];
        }
        if (schema.fields.some((candidate) => candidate.type === 'enum_by_kind' && candidate.depends_on === field.name)) {
          renderKindInputFields(kind, false);
        }
        updateBusyControls();
      };
      control.addEventListener(field.type === 'string' ? 'input' : 'change', remember);
      elements.kindInputFields.append(wrapper);
    }
  }

  function collectKindInput(kind) {
    if (!kind) throw new Error('種類を選択してください。');
    if (kind.input_schema_state === 'missing' && !kind.requires_typed_input) return null;
    if (!kindCanSubmit(kind)) throw new Error('ownerが公開する入力項目を確認できないため、この種類は追加できません。');
    const schema = kind.input_schema;
    const values = Object.create(null);
    for (const field of schema.fields) {
      const value = schemaDraftValue(field);
      if (field.type === 'enum' || field.type === 'enum_by_kind') {
        const controller = field.type === 'enum_by_kind' ? schema.fields.find((candidate) => candidate.name === field.depends_on) : null;
        const controllerValue = controller ? schemaDraftValue(controller) : '';
        const options = field.type === 'enum' ? field.options : field.options_by_kind[controllerValue] || [];
        if (value === KIND_INPUT_UNSET) {
          if (field.required) throw new Error(`${field.label}を選択してください。`);
          continue;
        }
        if (!options.some((option) => option.value === value)) throw new Error(`${field.label}の選択値を確認してください。`);
        values[field.name] = value;
      } else if (field.type === 'string') {
        if (field.required && !value.trim()) throw new Error(`${field.label}を入力してください。`);
        if (value !== KIND_INPUT_UNSET && value !== '') values[field.name] = value;
      } else if (field.type === 'integer') {
        if (value === KIND_INPUT_UNSET || value === '') {
          if (field.required) throw new Error(`${field.label}を入力してください。`);
          continue;
        }
        if (!/^-?\d+$/.test(value)) throw new Error(`${field.label}は整数で入力してください。`);
        const number = Number(value);
        if (!Number.isSafeInteger(number) || number < field.minimum || number > field.maximum) {
          throw new Error(`${field.label}は${field.minimum}から${field.maximum}の範囲で入力してください。`);
        }
        values[field.name] = number;
      }
    }
    if (new TextEncoder().encode(JSON.stringify(values)).byteLength > 8192) throw new Error('種類の入力項目が大きすぎます。');
    return values;
  }

  async function loadCapabilities() {
    if (state.capabilityController) state.capabilityController.abort();
    const controller = new AbortController();
    state.capabilityController = controller;
    updateSummary();
    try {
      const payload = await apiJSON(apiURL('/capabilities'), {signal: controller.signal});
      if (controller.signal.aborted || state.capabilityController !== controller) return;
      state.capabilities = validateCapabilities(payload);
      if (!state.capabilities.managed_available) state.view = 'daily';
      else if (state.view === 'daily' && !state.capabilities.daily_available) state.view = 'remaining';
      renderCapabilities();
      elements.workspace.hidden = false;
      elements.disconnectButton.hidden = false;
      updateConnectionState('認証済み・読み取り接続中', 'ready');
    } finally {
      if (state.capabilityController === controller) state.capabilityController = null;
      updateSummary();
    }
  }

  async function loadDaily({append = false} = {}) {
    if (!state.token || !state.capabilities) return;
    const recoveringList = Boolean(state.listErrorMessage);
    for (const detailController of state.detailControllers) detailController.abort();
    state.detailControllers.clear();
    if (state.listController) state.listController.abort();
    const controller = new AbortController();
    state.listController = controller;
    const generation = ++state.generation;
    updateBusyControls();
    const cursor = append ? state.nextCursor : '';
    if (!append) {
      state.items = [];
      state.dailyDefinitions = [];
      state.dailyIssues = [];
      state.dailyNextCursor = '';
      state.nextCursor = '';
      state.partial = false;
      state.listErrorMessage = '';
      state.listRecoveryMessage = '';
      state.hasListSuccess = false;
      state.updatedAt = '';
      state.dailyObservedAt = '';
      state.dailyAvailability = 'unknown';
      elements.empty.hidden = true;
      elements.listState.textContent = '日常タスクsourceへ読み取りを確認中…';
      elements.listState.classList.remove('is-error', 'is-recovered');
    }
    updateSummary();
    try {
      if (!state.capabilities.daily_available) {
        state.dailyAvailability = 'unavailable';
        state.listErrorMessage = '';
        state.listRecoveryMessage = '';
        state.hasListSuccess = false;
        updateConnectionState('認証済み・読み取り接続中', 'ready');
        return;
      }
      const query = {view: 'daily', kind_id: elements.dailyKind.value, limit: PAGE_LIMIT, cursor};
      const payload = validateDailyList(await apiJSON(apiURL('/items', query), {signal: controller.signal}));
      if (controller.signal.aborted || generation !== state.generation) return;
      const combined = append ? state.dailyDefinitions.concat(payload.definitions) : payload.definitions;
      state.dailyDefinitions = combined.slice(0, MAX_LOADED_ITEMS);
      for (const definition of payload.definitions) state.dailyKinds.add(definition.kind_id);
      state.dailyNextCursor = combined.length >= MAX_LOADED_ITEMS ? '' : payload.next_cursor;
      state.nextCursor = state.dailyNextCursor;
      state.dailyAvailability = append && (state.dailyAvailability === 'unavailable' || payload.availability === 'unavailable')
        ? 'unavailable'
        : append && (state.dailyAvailability === 'partial' || payload.availability === 'partial')
          ? 'partial'
          : payload.availability;
      state.dailyIssues = append
        ? [...state.dailyIssues, ...payload.issues].filter((issue, index, issues) =>
          issues.findIndex((candidate) => candidate.source_id === issue.source_id && candidate.owner_ref === issue.owner_ref && candidate.code === issue.code) === index)
        : payload.issues;
      state.dailyObservedAt = payload.observed_at;
      state.partial = payload.partial || payload.availability === 'partial' || append && state.partial;
      state.updatedAt = payload.observed_at;
      state.listErrorMessage = '';
      state.listRecoveryMessage = recoveringList && payload.availability !== 'unavailable'
        ? '日常タスク定義の読み取りが回復しました。' : '';
      state.hasListSuccess = true;
      updateConnectionState('認証済み・読み取り接続中', 'ready');
      if (state.listRecoveryMessage && !state.pendingMutation && !elements.error.textContent) {
        setNotice('日常タスク定義の読み取りが回復しました。', 'success');
      }
      renderCapabilities();
      renderDailyItems();
    } catch (error) {
      if (controller.signal.aborted || generation !== state.generation) return;
      state.listRecoveryMessage = '';
      if (isAuthenticationRejected(error)) {
        state.capabilities = null;
        state.items = [];
        state.dailyDefinitions = [];
        state.dailyIssues = [];
        state.dailyKinds.clear();
        elements.dailyKind.value = '';
        state.dailyNextCursor = '';
        state.nextCursor = '';
        state.dailyObservedAt = '';
        state.dailyAvailability = 'unknown';
        state.partial = false;
        state.hasListSuccess = false;
        state.updatedAt = '';
        state.listErrorMessage = '認証期限切れです。Operator tokenを再入力して再接続してください。';
        updateConnectionState('認証期限切れ・再接続が必要', 'error');
        if (!state.pendingMutation && !elements.error.textContent) {
          setNotice('認証が失効しています。Operator tokenを再入力して再接続してください。', 'error');
        }
        renderCapabilities();
      } else {
        state.listErrorMessage = error instanceof TaskCenterAPIError
          ? `日常タスク定義を取得できません。状態: HTTP ${error.status}${error.code ? ` · ${error.code}` : ''}。空の一覧とは判定していません。`
          : '日常タスク定義を取得できません。接続状態は未確認です。';
        if (append && state.dailyDefinitions.length) {
          state.partial = true;
          state.hasListSuccess = true;
          state.dailyNextCursor = '';
          state.nextCursor = '';
          state.listErrorMessage = `日常定義の次ページを取得できませんでした。取得済み${state.dailyDefinitions.length}件は保持しています。`;
        } else {
          state.dailyDefinitions = [];
          state.dailyIssues = [];
          state.dailyNextCursor = '';
          state.nextCursor = '';
          state.dailyObservedAt = '';
          state.dailyAvailability = 'unknown';
          state.hasListSuccess = false;
          state.updatedAt = '';
        }
        if (error instanceof TaskCenterAPIError) updateConnectionState('認証済み・読み取り接続中', 'ready');
        else updateConnectionState('接続状態を確認できません', 'error');
      }
      updateSummary();
    } finally {
      if (state.listController === controller) state.listController = null;
      if (generation === state.generation) renderDailyItems();
    }
  }

  async function loadList({append = false} = {}) {
    if (!state.token || !state.capabilities) return;
    if (state.view === 'daily') return loadDaily({append});
    const recoveringList = Boolean(state.listErrorMessage);
    for (const detailController of state.detailControllers) detailController.abort();
    state.detailControllers.clear();
    if (state.listController) state.listController.abort();
    const controller = new AbortController();
    state.listController = controller;
    const generation = ++state.generation;
    updateBusyControls();
    const cursor = append ? state.nextCursor : '';
    if (!append) {
      state.items = [];
      state.nextCursor = '';
      state.partial = false;
      state.listErrorMessage = '';
      state.listRecoveryMessage = '';
      state.hasListSuccess = false;
      state.updatedAt = '';
      elements.empty.hidden = true;
      elements.listState.textContent = 'ownerへ読み取りを確認中…';
      elements.listState.classList.remove('is-error', 'is-recovered');
      renderItems();
    }
    updateSummary();
    const isClaimTab = state.view === 'claimable';
    const apiView = isClaimTab && !state.capabilities.can_claim ? 'remaining' : state.view;
    const assignee = isClaimTab ? 'unassigned' : elements.assignee.value || 'all';
    const query = {view: apiView, assignee: assignee === 'all' ? '' : assignee, limit: PAGE_LIMIT, cursor};
    try {
      const payload = validateList(await apiJSON(apiURL('/items', query), {signal: controller.signal}));
      if (controller.signal.aborted || generation !== state.generation) return;
      const combined = append ? state.items.concat(payload.items) : payload.items;
      state.items = combined.slice(0, MAX_LOADED_ITEMS);
      state.nextCursor = combined.length >= MAX_LOADED_ITEMS ? '' : payload.next_cursor;
      state.partial = payload.partial || (append && state.partial);
      state.listPolicyRevision = payload.policy_revision;
      state.updatedAt = new Date().toISOString();
      state.listErrorMessage = '';
      state.listRecoveryMessage = recoveringList ? `${currentViewLabel()}の一覧取得が回復しました。` : '';
      state.hasListSuccess = true;
      elements.empty.hidden = state.items.length > 0;
      updateConnectionState('認証済み・読み取り接続中', 'ready');
      if (recoveringList && !state.pendingMutation && !elements.error.textContent) {
        setNotice(`${currentViewLabel()}の一覧取得が回復しました。`, 'success');
      }
      renderItems();
    } catch (error) {
      if (controller.signal.aborted || generation !== state.generation) return;
      const message = error instanceof TaskCenterAPIError
        ? `一覧を取得できません。状態: HTTP ${error.status}${error.code ? ` · ${error.code}` : ''}。空の一覧とは判定していません。`
        : '一覧を取得できません。接続状態は未確認です。';
      if (isAuthenticationRejected(error)) {
        state.capabilities = null;
        state.items = [];
        state.dailyDefinitions = [];
        state.dailyIssues = [];
        state.dailyKinds.clear();
        elements.dailyKind.value = '';
        state.dailyAvailability = 'unknown';
        state.dailyObservedAt = '';
        state.nextCursor = '';
        state.partial = false;
        state.hasListSuccess = false;
        state.updatedAt = '';
        state.listPolicyRevision = '';
        state.listErrorMessage = '認証期限切れです。Operator tokenを再入力して再接続してください。';
        state.listRecoveryMessage = '';
        renderCapabilities();
        updateConnectionState('認証期限切れ・再接続が必要', 'error');
        if (!state.pendingMutation && !elements.error.textContent) {
          setNotice('認証が失効しています。Operator tokenを再入力して再接続してください。', 'error');
        }
      } else {
        state.listErrorMessage = append && state.items.length
          ? `次ページを取得できませんでした。既に取得した${state.items.length}件を保持しています。`
          : message;
        state.listRecoveryMessage = '';
        if (append) state.partial = true;
        else {
          state.items = [];
          state.nextCursor = '';
          state.hasListSuccess = false;
        }
        if (error instanceof TaskCenterAPIError) updateConnectionState('認証済み・読み取り接続中', 'ready');
        else {
          updateConnectionState('接続状態を確認できません', 'error');
          if (!state.pendingMutation && !elements.error.textContent) {
            setNotice('一覧を取得できませんでした。接続状態は未確認です。', 'error');
          }
        }
        updateSummary();
      }
    } finally {
      if (state.listController === controller) state.listController = null;
      if (generation === state.generation) {
        renderItems();
      }
    }
  }

  async function refreshAll() {
    if (!state.token) return;
    elements.refresh.disabled = true;
    try {
      await loadCapabilities();
      await loadList();
      if (!elements.error.textContent && !state.listErrorMessage) setNotice('capabilitiesと選択中の一覧を更新しました。', 'success');
    } catch (error) {
      if (isAuthenticationRejected(error)) {
        state.capabilities = null;
        state.items = [];
        state.dailyDefinitions = [];
        state.dailyIssues = [];
        state.dailyKinds.clear();
        elements.dailyKind.value = '';
        state.dailyAvailability = 'unknown';
        state.dailyObservedAt = '';
        state.nextCursor = '';
        state.partial = false;
        state.hasListSuccess = false;
        state.updatedAt = '';
        state.listPolicyRevision = '';
        state.listErrorMessage = '認証期限切れです。Operator tokenを再入力して再接続してください。';
        state.listRecoveryMessage = '';
        updateConnectionState('認証期限切れ・再接続が必要', 'error');
        setError('認証が失効しています。Operator tokenを再入力して再接続してください。');
        renderItems();
      } else if (error instanceof TaskCenterAPIError) {
        updateConnectionState('認証済み・読み取り接続中', 'ready');
        setError(`capabilitiesを更新できません。状態: HTTP ${error.status}${error.code ? ` · ${error.code}` : ''}。前回取得した種類とscope表示を保持しています。`);
      } else {
        updateConnectionState('接続状態を確認できません', 'error');
        setError('capabilitiesを更新できません。接続状態は未確認です。前回取得した種類とscope表示を保持しています。');
      }
      elements.workspace.hidden = false;
      renderCapabilities();
      updateSummary();
    } finally {
      elements.refresh.disabled = false;
      updateBusyControls();
    }
  }

  async function connect(token) {
    if (state.pendingMutation) return;
    state.busy = true;
    updateBusyControls();
    for (const detailController of state.detailControllers) detailController.abort();
    state.detailControllers.clear();
    if (state.listController) state.listController.abort();
    state.generation += 1;
    state.listController = null;
    state.token = token;
    state.capabilities = null;
    state.items = [];
    state.dailyDefinitions = [];
    state.dailyIssues = [];
    state.dailyKinds.clear();
    elements.dailyKind.value = '';
    state.dailyAvailability = 'unknown';
    state.dailyObservedAt = '';
    state.nextCursor = '';
    state.partial = false;
    state.updatedAt = '';
    state.listPolicyRevision = '';
    state.listErrorMessage = '';
    state.listRecoveryMessage = '';
    state.hasListSuccess = false;
    state.kindInputDraft = Object.create(null);
    elements.addForm.reset();
    resetWindows();
    syncRecurrenceFields();
    elements.error.hidden = true;
    elements.notice.hidden = true;
    elements.workspace.hidden = true;
    elements.disconnectButton.hidden = true;
    updateConnectionState('認証情報を確認中', 'warning');
    updateSummary();
    try {
      await loadCapabilities();
      await loadList();
      if (!elements.error.textContent && !state.listErrorMessage) setNotice('接続しました。取得結果はownerの読取状態です。', 'success');
    } catch (error) {
      state.capabilities = null;
      state.items = [];
      state.dailyDefinitions = [];
      state.dailyIssues = [];
      state.dailyKinds.clear();
      elements.dailyKind.value = '';
      state.dailyAvailability = 'unknown';
      state.dailyObservedAt = '';
      state.dailyDefinitions = [];
      state.dailyIssues = [];
      state.dailyKinds.clear();
      state.dailyAvailability = 'unknown';
      state.nextCursor = '';
      elements.workspace.hidden = true;
      elements.disconnectButton.hidden = false;
      updateConnectionState(isAuthenticationRejected(error) ? '認証期限切れ・再接続が必要' : '接続状態を確認できません', 'error');
      setError(error instanceof TaskCenterAPIError
        ? `接続できません。状態: HTTP ${error.status}${error.code ? ` · ${error.code}` : ''}。Operator tokenとownerの状態を確認してください。`
        : '接続できません。接続状態は未確認です。');
      updateSummary();
    } finally {
      state.busy = false;
      renderCapabilities();
      updateBusyControls();
    }
  }

  function disconnect() {
    if (state.pendingMutation) return;
    for (const detailController of state.detailControllers) detailController.abort();
    state.detailControllers.clear();
    if (state.listController) state.listController.abort();
    if (state.capabilityController) state.capabilityController.abort();
    state.generation += 1;
    state.token = '';
    state.capabilities = null;
    state.items = [];
    state.dailyDefinitions = [];
    state.dailyIssues = [];
    state.dailyKinds.clear();
    elements.dailyKind.value = '';
    state.dailyAvailability = 'unknown';
    state.dailyObservedAt = '';
    state.nextCursor = '';
    state.partial = false;
    state.listPolicyRevision = '';
    state.updatedAt = '';
    state.listErrorMessage = '';
    state.listRecoveryMessage = '';
    state.hasListSuccess = false;
    state.listController = null;
    state.kindInputDraft = Object.create(null);
    elements.addForm.reset();
    resetWindows();
    syncRecurrenceFields();
    elements.workspace.hidden = true;
    elements.disconnectButton.hidden = true;
    elements.items.replaceChildren();
    elements.token.value = '';
    elements.search.value = '';
    elements.notice.hidden = true;
    elements.error.hidden = true;
    elements.mutationPanel.hidden = true;
    updateConnectionState('認証が必要です', 'warning');
    elements.capabilityState.classList.remove('is-ready', 'is-error');
    elements.listState.textContent = '認証して接続すると、ownerの読み取り結果を表示します。';
    elements.listState.classList.remove('is-error', 'is-recovered');
    renderCapabilities();
    updateSummary();
  }

  function splitLines(value) {
    return value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
  }

  function parseJSTDateTime(value, label) {
    if (!value) return null;
    const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(value);
    if (!match) throw new Error(`${label}の形式を確認してください。`);
    const [, year, month, day, hour, minute] = match;
    const numbers = [year, month, day, hour, minute].map(Number);
    const [y, m, d, h, min] = numbers;
    const check = new Date(Date.UTC(y, m - 1, d, h, min));
    if (check.getUTCFullYear() !== y || check.getUTCMonth() !== m - 1 || check.getUTCDate() !== d || h > 23 || min > 59) {
      throw new Error(`${label}の日付が正しくありません。`);
    }
    return `${value}:00+09:00`;
  }

  function integerOrNull(input, label) {
    if (!input.value.trim()) return null;
    const value = Number(input.value);
    const minimum = label === '想定所要時間' ? 0 : 1;
    if (!Number.isSafeInteger(value) || value < minimum) throw new Error(`${label}は${minimum}以上の整数にしてください。`);
    return value;
  }

  function collectTiming() {
    if (elements.timezone.value !== 'Asia/Tokyo') throw new Error('timezoneはAsia/Tokyoを選択してください。');
    const scheduled = parseJSTDateTime(elements.scheduled.value, '実行予定');
    const notBefore = parseJSTDateTime(elements.notBefore.value, '開始可能時刻');
    const deadline = parseJSTDateTime(elements.deadline.value, '期限');
    const allowedWindows = [];
    for (const row of elements.windowRows.querySelectorAll('[data-window-row]')) {
      const start = row.querySelector('[data-window-start]').value;
      const end = row.querySelector('[data-window-end]').value;
      if (!start && !end) continue;
      if (!start || !end || !/^([01]\d|2[0-3]):[0-5]\d$/.test(start) || !/^([01]\d|2[0-3]):[0-5]\d$/.test(end) || start === end) {
        throw new Error('実行可能時間帯は開始・終了の両方を正しい時刻で入力してください。');
      }
      allowedWindows.push({start, end});
    }
    const frequency = elements.recurrenceFrequency.value;
    if (frequency) {
      throw new Error('繰り返しタスクの追加は現在未対応です。「なし」のみ選択できます。');
    }
    return {
      timezone: 'Asia/Tokyo',
      scheduled_start_at: scheduled,
      not_before: notBefore,
      allowed_windows: allowedWindows,
      deadline_at: deadline,
      recurrence: null,
      estimated_duration_seconds: integerOrNull(elements.duration, '想定所要時間'),
    };
  }

  function createIdempotencyKey() {
    if (window.crypto && typeof window.crypto.randomUUID === 'function') return window.crypto.randomUUID();
    if (window.crypto && typeof window.crypto.getRandomValues === 'function') {
      const bytes = new Uint8Array(16);
      window.crypto.getRandomValues(bytes);
      bytes[6] = (bytes[6] & 0x0f) | 0x40;
      bytes[8] = (bytes[8] & 0x3f) | 0x80;
      const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
      return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
    }
    throw new Error('安全な冪等キーを作成できないため、送信できません。');
  }

  function mutationResult(payload) {
    if (!payload || !payload.item || typeof payload.item !== 'object' ||
        typeof payload.replayed !== 'boolean' || typeof payload.receipt_ref !== 'string' || !payload.receipt_ref.trim()) {
      return null;
    }
    return {item: validateItem(payload.item), replayed: payload.replayed, receipt_ref: payload.receipt_ref};
  }

  function showMutationResult(result, operation) {
    elements.mutationPanel.hidden = false;
    elements.recovery.hidden = true;
    elements.mutationMessage.textContent = operation === 'claim'
      ? '引受がownerで確認されました。仕事の実行開始を意味するものではありません。'
      : operation === 'accept_result'
        ? '成果物の内容を作成者が確認し、Goalを完了として記録しました。'
        : `ownerが受付を確認しました${result.item.status === 'started' ? '。実行開始も記録されています。' : '。実行開始は別の状態です。'}`;
    if (result.replayed) elements.mutationMessage.textContent += ' 同じ冪等キーの受付結果です。';
    elements.mutationReceipt.replaceChildren();
    const entries = [
      ['タスク', cleanText(result.item.title, '名前未確認')],
      ['owner ID', cleanText(result.item.goal_id)],
      ['状態', statusLabel(result.item.status)],
      ['receipt', result.receipt_ref],
      ['Task / Run', [result.item.task_id, result.item.run_id].filter(Boolean).join(' / ') || '未確認'],
    ];
    for (const [label, value] of entries) {
      const term = document.createElement('dt'); term.textContent = label;
      const description = document.createElement('dd'); description.textContent = value;
      elements.mutationReceipt.append(term, description);
    }
  }

  function showMutationUnknown(operation) {
    elements.mutationPanel.hidden = false;
    elements.recovery.hidden = false;
    elements.mutationMessage.textContent = operation === 'claim'
      ? 'claimの応答が届かず、引受結果は未確認です。'
      : operation === 'accept_result'
        ? '成果物の受付応答が届かず、Goalの完了状態は未確認です。'
        : '追加の応答が届かず、受付結果は未確認です。';
    elements.mutationReceipt.replaceChildren();
    elements.mutationReceipt.append(createReceiptRow('状態', '結果未確認・同じ操作の照会が必要'));
  }

  function createReceiptRow(labelText, valueText) {
    const term = document.createElement('dt'); term.textContent = labelText;
    const description = document.createElement('dd'); description.textContent = valueText;
    const wrapper = document.createElement('div');
    wrapper.append(term, description);
    return wrapper;
  }

  async function submitTask(event) {
    event.preventDefault();
    elements.formError.hidden = true;
    if (!state.token || !state.capabilities || !state.capabilities.managed_available || !state.capabilities.can_submit || state.pendingMutation || state.busy) return;
    const kind = state.capabilities.kinds.find((entry) => entry.kind_id === elements.kind.value);
    const title = elements.title.value.trim();
    const description = elements.description.value.trim();
    const criteria = splitLines(elements.criteria.value);
    const verification = splitLines(elements.verification.value);
    if (!kind || !description || !criteria.length || !verification.length) {
      setError('種類・依頼内容・完了条件・確認方法を確認してください。', elements.formError);
      return;
    }
    if (title.length > 240 || description.length > 12000 || criteria.some((line) => line.length > 1000) || verification.some((line) => line.length > 1000)) {
      setError('入力の文字数が上限を超えています。', elements.formError);
      return;
    }
    let timing;
    let idempotencyKey;
    let kindInput;
    try {
      timing = collectTiming();
      kindInput = collectKindInput(kind);
      idempotencyKey = createIdempotencyKey();
    } catch (error) {
      setError(error.message, elements.formError);
      return;
    }
    const request = {
      kind_id: kind.kind_id,
      title,
      description,
      success_criteria: criteria,
      verification,
      assignee: elements.assigneeInput.value || 'unassigned',
      timing,
      idempotency_key: idempotencyKey,
      expected_policy_revision: state.capabilities.policy_revision,
    };
    if (kindInput !== null) request.kind_input = kindInput;
    state.pendingMutation = {operation: 'submit', idempotencyKey, subjectID: ''};
    state.busy = true;
    updateBusyControls();
    let result = null;
    try {
      const payload = await apiJSON(apiURL('/requests'), {method: 'POST', body: request});
      result = mutationResult(payload);
      if (!result) throw new TaskCenterAPIError(200, 'INVALID_MUTATION_RECEIPT');
    } catch (error) {
      if (isKnownMutationRejection(error)) {
        state.pendingMutation = null;
        elements.mutationPanel.hidden = true;
        const code = error.code ? ` · ${error.code}` : '';
        if (error.status === 409) {
          await loadList();
          setError(`ownerが追加を受理しませんでした。HTTP ${error.status}${code}。内容と対応状態を確認してください。`);
        } else {
          setError(`ownerが追加を受理しませんでした。HTTP ${error.status}${code}。内容と対応状態を確認してください。`);
        }
      } else {
        showMutationUnknown('submit');
        setError('追加結果を確定できません。再送せず、同じ冪等キーで受付結果を照会してください。');
      }
      state.busy = false;
      updateBusyControls();
      return;
    }
    state.pendingMutation = null;
    showMutationResult(result, 'submit');
    elements.addForm.reset();
    elements.kind.value = '';
    state.kindInputDraft = Object.create(null);
    elements.timezone.value = 'Asia/Tokyo';
    resetWindows();
    syncRecurrenceFields();
    updateKindAssignees();
    try {
      await refreshAfterMutation();
    } catch (_) {
      setError('ownerの受付結果は確認済みですが、変更後の一覧を再取得できませんでした。');
    } finally {
      state.busy = false;
      updateBusyControls();
    }
  }

  function isKnownMutationRejection(error) {
    return error instanceof TaskCenterAPIError && error.status >= 400 && error.status < 500 && error.status !== 408;
  }

  async function claimItem(item, button) {
    if (!state.capabilities || !state.capabilities.managed_available || !state.capabilities.can_claim || !state.token || state.busy || state.pendingMutation) return;
    let key;
    try { key = createIdempotencyKey(); } catch (error) { setError(error.message); return; }
    state.pendingMutation = {operation: 'claim', idempotencyKey: key, subjectID: item.goal_id};
    state.busy = true;
    button.disabled = true;
    updateBusyControls();
    const body = {
      goal_id: item.goal_id,
      expected_revision: item.revision,
      expected_policy_revision: state.listPolicyRevision || state.capabilities.policy_revision,
      idempotency_key: key,
    };
    let result = null;
    try {
      const payload = await apiJSON(apiURL('/claims'), {method: 'POST', body});
      result = mutationResult(payload);
      if (!result) throw new TaskCenterAPIError(200, 'INVALID_MUTATION_RECEIPT');
    } catch (error) {
      if (isKnownMutationRejection(error)) {
        state.pendingMutation = null;
        elements.mutationPanel.hidden = true;
        await loadList();
        setError(error.status === 409
          ? '別のAgentが先に引き受けました。候補一覧を更新して、他の仕事を確認してください。'
          : `ownerが引受を受理しませんでした。HTTP ${error.status}${error.code ? ` · ${error.code}` : ''}。`);
      } else {
        showMutationUnknown('claim');
        setError('引受結果を確定できません。別のkeyでclaimせず、同じreceiptを照会してください。');
      }
      state.busy = false;
      updateBusyControls();
      return;
    }
    state.pendingMutation = null;
    showMutationResult(result, 'claim');
    try {
      await refreshAfterMutation();
    } catch (_) {
      setError('ownerのclaim結果は確認済みですが、変更後の一覧を再取得できませんでした。');
    } finally {
      state.busy = false;
      updateBusyControls();
    }
  }

  async function refreshAfterMutation() {
    elements.error.hidden = true;
    state.listErrorMessage = '';
    await loadCapabilities();
    await loadList();
    setNotice('ownerの変更結果を確認し、選択中の一覧を再取得しました。', 'success');
  }

  async function recoverMutation() {
    if (!state.pendingMutation || !state.token || state.busy) return;
    const pending = state.pendingMutation;
    state.busy = true;
    elements.recoverButton.disabled = true;
    updateBusyControls();
    const path = pending.operation === 'submit'
      ? apiURL('/request', {operation: 'submit', idempotency_key: pending.idempotencyKey})
      : pending.operation === 'accept_result'
        ? apiURL('/result/acceptance', {goal_id: pending.goalID, idempotency_key: pending.idempotencyKey})
        : apiURL('/claim', {goal_id: pending.subjectID, idempotency_key: pending.idempotencyKey});
    try {
      const payload = await apiJSON(path);
      const result = mutationResult(payload) || mutationResult(payload.result) || mutationResult(payload.request) || mutationResult(payload.claim);
      if (result) {
        if (pending.operation === 'accept_result' && !acceptedResultMatches(result, pending)) {
          showMutationUnknown(pending.operation);
          setError('受付照会のGoal・revision・成果物参照が保留中の操作と一致しません。新しい操作は行わず、結果を未確認のまま保持します。');
          return;
        }
        state.pendingMutation = null;
        showMutationResult(result, pending.operation);
        try {
          await refreshAfterMutation();
        } catch (_) {
          setError('ownerの受付結果は確認済みですが、変更後の一覧を再取得できませんでした。');
        }
        setNotice('同じ冪等キーのowner記録から結果を確認しました。', 'success');
        return;
      }
      if (pending.operation === 'accept_result') {
        showMutationUnknown(pending.operation);
        setError('受付照会に一致するMutationResultがありません。新しい操作は行わず、結果を未確認のまま保持します。');
        return;
      }
      const outcome = payload.status || payload.outcome || (payload.request && payload.request.status) || (payload.claim && payload.claim.status);
      if (['rejected', 'not_accepted'].includes(outcome)) {
        state.pendingMutation = null;
        elements.recovery.hidden = true;
        elements.mutationMessage.textContent = 'ownerの照会で受付されていないことを確認しました。';
        setNotice('結果が未受付と確定しました。フォームを確認してから、新しい操作として追加できます。', 'success');
      } else if (['stored', 'started', 'claimed', 'completed'].includes(outcome)) {
        state.pendingMutation = null;
        elements.recovery.hidden = true;
        elements.mutationMessage.textContent = `ownerの照会結果: ${resultLabel(outcome)}。詳細なreceipt項目は応答にありません。`;
        try {
          await refreshAfterMutation();
        } catch (_) {
          setError('ownerの受付結果は確認済みですが、変更後の一覧を再取得できませんでした。');
        }
      } else {
        showMutationUnknown(pending.operation);
        setError('照会応答だけでは受付結果を確定できません。新しい操作は行わず、必要なowner確認を続けてください。');
      }
    } catch (error) {
      showMutationUnknown(pending.operation);
      setError(error instanceof TaskCenterAPIError
        ? `受付結果を照会できません。HTTP ${error.status}${error.code ? ` · ${error.code}` : ''}。操作結果は未確認のままです。`
        : '受付結果を照会できません。操作結果は未確認のままです。');
    } finally {
      state.busy = false;
      updateBusyControls();
    }
  }

  async function refreshItemDetail(item, target, resultPanel, controls, button) {
    if (!state.token || !item.goal_id) return;
    const controller = new AbortController();
    const generation = state.generation;
    const token = state.token;
    state.detailControllers.add(controller);
    button.disabled = true;
    resultPanel.replaceChildren();
    resultPanel.hidden = true;
    controls.querySelector('[data-task-result-read]')?.remove();
    try {
      const payload = await apiJSON(apiURL('/item', {goal_id: item.goal_id}), {signal: controller.signal});
      if (controller.signal.aborted || generation !== state.generation || token !== state.token) return;
      if (!payload.item) throw new TaskCenterAPIError(200, 'INVALID_ITEM_DETAIL');
      const detail = validateItem(payload.item);
      renderDetails(target, detail, true);
      resultPanel.replaceChildren();
      resultPanel.hidden = true;
      controls.querySelector('[data-task-result-read]')?.remove();
      if (safeID(detail.result_artifact_id) && typeof detail.result_sha256 === 'string' && /^[a-f0-9]{64}$/i.test(detail.result_sha256) &&
          typeof detail.execution_verification === 'string') {
        const resultButton = document.createElement('button');
        resultButton.type = 'button';
        resultButton.className = 'task-center__button task-center__button--quiet';
        resultButton.dataset.taskResultAction = 'read';
        resultButton.dataset.taskResultRead = '';
        resultButton.textContent = '保存された成果物を確認';
        resultButton.disabled = state.busy || Boolean(state.pendingMutation);
        resultButton.addEventListener('click', () => { void readResultView(detail, resultPanel, controls, resultButton); });
        controls.append(resultButton);
      }
      setNotice('ownerの詳細を取得しました。', 'success');
    } catch (error) {
      if (controller.signal.aborted || generation !== state.generation || token !== state.token) return;
      setError(error instanceof TaskCenterAPIError
        ? `詳細を取得できません。HTTP ${error.status}${error.code ? ` · ${error.code}` : ''}。`
        : '詳細を取得できません。状態は未確認です。');
    } finally {
      state.detailControllers.delete(controller);
      if (!controller.signal.aborted) button.disabled = false;
    }
  }

  function addResultReviewText(target, tagName, className, text) {
    const node = document.createElement(tagName);
    if (className) node.className = className;
    node.textContent = text;
    target.append(node);
    return node;
  }

  async function readResultView(item, resultPanel, controls, button) {
    if (!state.token || state.busy || state.pendingMutation) return;
    const controller = new AbortController();
    const generation = state.generation;
    const token = state.token;
    state.detailControllers.add(controller);
    button.disabled = true;
    resultPanel.hidden = false;
    resultPanel.replaceChildren();
    addResultReviewText(resultPanel, 'h3', '', '実行結果の確認');
    addResultReviewText(resultPanel, 'p', 'task-center__result-state', '保存された成果物を読み取っています。');
    try {
      const payload = await apiJSON(apiURL('/result', {goal_id: item.goal_id}), {signal: controller.signal});
      if (controller.signal.aborted || generation !== state.generation || token !== state.token) return;
      const resultView = {...validateResultView(payload, item), viewer_generation: generation};
      resultPanel.replaceChildren();
      addResultReviewText(resultPanel, 'h3', '', '保存された実行結果');
      addResultReviewText(resultPanel, 'p', 'task-center__result-state', executionVerificationLabel(resultView.execution_verification));
      const metadata = document.createElement('dl');
      metadata.className = 'task-center__detail-grid';
      metadata.append(
        createDetail('成果物参照', resultView.result_artifact_id),
        createDetail('成果物SHA-256', resultView.result_sha256),
      );
      const resultDetails = document.createElement('details');
      resultDetails.className = 'task-center__result-body';
      const resultSummary = document.createElement('summary');
      resultSummary.textContent = '成果物JSONを表示';
      const resultBody = document.createElement('pre');
      resultBody.textContent = resultView.result_text;
      resultDetails.append(resultSummary, resultBody);
      resultPanel.append(metadata, resultDetails);

      const canReview = item.can_accept_result === true && item.execution_verification === 'verification_pending' &&
        Array.isArray(item.success_criteria) && item.success_criteria.length > 0 &&
        Array.isArray(item.verification) && item.verification.length > 0 &&
        resultView.execution_verification === 'verification_pending';
      if (canReview) {
        addResultReviewText(resultPanel, 'p', 'task-center__result-state', '完了条件と確認方法を上の詳細で読み、成果物を確認した後にGoalを完了へ進められます。');
        const acceptButton = document.createElement('button');
        acceptButton.type = 'button';
        acceptButton.className = 'task-center__button task-center__button--primary';
        acceptButton.dataset.taskResultAction = 'accept';
        acceptButton.textContent = '成果物を確認してGoalを完了にする';
        acceptButton.addEventListener('click', () => { void acceptResult(item, resultView, acceptButton); });
        resultPanel.append(acceptButton);
      } else if (resultView.execution_verification === 'verification_pending') {
        addResultReviewText(resultPanel, 'p', 'task-center__result-state', '成果物は読み取りました。Goalを完了にする権限または確認条件をこのViewerで確認できません。');
      }
    } catch (error) {
      if (controller.signal.aborted || generation !== state.generation || token !== state.token) return;
      resultPanel.replaceChildren();
      addResultReviewText(resultPanel, 'h3', '', '実行結果の確認');
      const message = error instanceof TaskCenterAPIError && error.status === 401
        ? '認証期限切れです。再接続が必要です。'
        : '保存された成果物を確認できません。結果は未確認です。';
      addResultReviewText(resultPanel, 'p', 'task-center__result-state is-error', message);
      if (error instanceof TaskCenterAPIError && error.status === 401) {
        updateConnectionState('認証期限切れ・再接続が必要', 'error');
      }
    } finally {
      state.detailControllers.delete(controller);
      if (!controller.signal.aborted) button.disabled = false;
      updateBusyControls();
    }
  }

  async function acceptResult(item, resultView, button) {
    if (!state.token || state.busy || state.pendingMutation || item.can_accept_result !== true ||
        resultView.viewer_generation !== state.generation ||
        item.execution_verification !== 'verification_pending' || resultView.goal_id !== item.goal_id ||
        resultView.revision !== item.revision || resultView.assignment_epoch !== item.assignment_epoch ||
        resultView.result_artifact_id !== item.result_artifact_id || resultView.result_sha256 !== item.result_sha256 ||
        resultView.execution_verification !== 'verification_pending') return;
    let idempotencyKey;
    try { idempotencyKey = createIdempotencyKey(); } catch (error) { setError(error.message); return; }
    const body = {
      goal_id: item.goal_id,
      expected_revision: item.revision,
      expected_assignment_epoch: item.assignment_epoch,
      idempotency_key: idempotencyKey,
    };
    const pending = {
      operation: 'accept_result', idempotencyKey, goalID: item.goal_id,
      expectedRevision: item.revision, expectedAssignmentEpoch: item.assignment_epoch,
      resultArtifactID: item.result_artifact_id, resultSHA256: item.result_sha256, body,
    };
    state.pendingMutation = pending;
    state.busy = true;
    button.disabled = true;
    updateBusyControls();
    try {
      const payload = await apiJSON(apiURL('/result/accept'), {method: 'POST', body: pending.body});
      const result = mutationResult(payload);
      if (!result || !acceptedResultMatches(result, pending)) throw new TaskCenterAPIError(200, 'INVALID_ACCEPTANCE_RECEIPT');
      state.pendingMutation = null;
      showMutationResult(result, 'accept_result');
      try {
        await refreshAfterMutation();
      } catch (_) {
        setError('作成者の確認とGoal完了は記録されましたが、変更後の一覧を再取得できませんでした。');
      }
    } catch (error) {
      if (isKnownMutationRejection(error)) {
        state.pendingMutation = null;
        elements.mutationPanel.hidden = true;
        button.hidden = true;
        setError(`ownerが成果物の確認を受理しませんでした。HTTP ${error.status}${error.code ? ` · ${error.code}` : ''}。詳細を再取得してください。`);
        try { await loadList(); } catch (_) { /* the rejected mutation remains definitive */ }
      } else {
        showMutationUnknown('accept_result');
        setError('成果物の確認結果を確定できません。再送せず、同じ冪等キーでreceiptを照会してください。');
      }
    } finally {
      state.busy = false;
      updateBusyControls();
    }
  }

  function resetWindows() {
    elements.windowRows.replaceChildren();
    elements.windowRows.append(createWindowRow(true));
  }

  function createWindowRow(first = false) {
    const row = document.createElement('div');
    row.className = 'task-center__window-row';
    row.setAttribute('data-window-row', '');
    const startLabel = document.createElement('label'); startLabel.textContent = '開始';
    const start = document.createElement('input'); start.type = 'time'; start.setAttribute('data-window-start', ''); start.setAttribute('aria-label', '実行可能時間帯の開始');
    startLabel.append(start);
    const endLabel = document.createElement('label'); endLabel.textContent = '終了';
    const end = document.createElement('input'); end.type = 'time'; end.setAttribute('data-window-end', ''); end.setAttribute('aria-label', '実行可能時間帯の終了');
    endLabel.append(end);
    const remove = document.createElement('button'); remove.type = 'button'; remove.className = 'task-center__remove-window';
    remove.setAttribute('data-remove-window', ''); remove.textContent = '削除'; remove.disabled = first;
    remove.setAttribute('aria-label', 'この時間帯を削除');
    remove.addEventListener('click', () => { row.remove(); updateWindowButtons(); });
    row.append(startLabel, endLabel, remove);
    return row;
  }

  function updateWindowButtons() {
    const rows = Array.from(elements.windowRows.querySelectorAll('[data-window-row]'));
    if (!rows.length) { resetWindows(); return; }
    rows.forEach((row) => { row.querySelector('[data-remove-window]').disabled = rows.length === 1; });
  }

  function syncRecurrenceFields() {
    const frequency = elements.recurrenceFrequency.value;
    elements.recurrenceFields.hidden = !frequency;
    elements.recurrenceTimesField.hidden = frequency === 'interval';
    elements.weekdays.hidden = frequency !== 'weekly';
    elements.intervalField.hidden = frequency !== 'interval';
  }

  async function inspectUserTaskCancellation() {
    const taskID = elements.cancellationTaskID.value.trim();
    state.cancellationPreview = null;
    if (!taskID || !state.token) return;
    state.cancellationBusy = true;
    elements.cancellationInspect.disabled = elements.cancellationSubmit.disabled = true;
    elements.cancellationState.textContent = 'ownerへTaskの現在状態を確認中…';
    try {
      const result = await apiJSON(apiURL('/task-cancellation', {task_id: taskID}));
      if (elements.cancellationTaskID.value.trim() !== taskID) return;
      state.cancellationPreview = result;
      elements.cancellationSubmit.disabled = result.cancellable !== true;
      elements.cancellationState.textContent = result.cancellable === true
        ? `取消可能です。対象 ${result.task_id} · 状態 ${result.status} · Runなしをownerが確認しました。`
        : result.reason === 'already_cancelled'
          ? `Task ${result.task_id} は取消済みです。owner状態を再読しました。`
          : `取消不可です。対象 ${result.task_id} · 状態 ${result.status} · ${cleanText(result.reason, '理由未確認')}`;
    } catch (error) {
      elements.cancellationState.textContent = `取消可否を取得できませんでした（${cleanText(error && error.code, 'owner error')}）。状態は未確認です。`;
    } finally {
      state.cancellationBusy = false;
      elements.cancellationInspect.disabled = !state.token;
    }
  }

  async function submitUserTaskCancellation(event) {
    event.preventDefault();
    const taskID = elements.cancellationTaskID.value.trim();
    if (!state.cancellationPreview || state.cancellationPreview.cancellable !== true || state.cancellationPreview.task_id !== taskID) {
      elements.cancellationState.textContent = '取消前に、入力したTask IDの現在状態を確認してください。'; return;
    }
    if (!window.confirm(`Task ${taskID} を取消します。対象Task IDを確認してください。`)) return;
    state.cancellationBusy = true;
    elements.cancellationSubmit.disabled = elements.cancellationInspect.disabled = true;
    try {
      const result = await apiJSON(apiURL('/task-cancellation'), {method: 'POST', body: {task_id: taskID}});
      elements.cancellationState.textContent = result.cancelled ? `Task ${taskID} の取消をownerが記録しました。状態を再読しています…` : 'Taskは取消されませんでした。owner状態を再読しています…';
    } catch (error) {
      elements.cancellationState.textContent = `取消結果を確認できませんでした（${cleanText(error && error.code, 'owner error')}）。`;
    } finally {
      state.cancellationBusy = false;
      await inspectUserTaskCancellation();
    }
  }

  elements.cancellationInspect.addEventListener('click', () => { void inspectUserTaskCancellation(); });
  elements.cancellationForm.addEventListener('submit', (event) => { void submitUserTaskCancellation(event); });
  elements.cancellationTaskID.addEventListener('input', () => {
    state.cancellationPreview = null;
    elements.cancellationSubmit.disabled = true;
    elements.cancellationState.textContent = 'Task IDが変更されました。取消前にowner状態を再確認してください。';
  });

  elements.connectForm.addEventListener('submit', (event) => {
    event.preventDefault();
    const token = elements.token.value.trim();
    elements.token.value = '';
    if (!token) { setError('Operator tokenを入力してください。'); return; }
    void connect(token);
  });
  elements.disconnectButton.addEventListener('click', disconnect);
  elements.refresh.addEventListener('click', () => { void refreshAll(); });
  elements.more.addEventListener('click', () => { void loadList({append: true}); });
  elements.search.addEventListener('input', renderItems);
  elements.assignee.addEventListener('change', () => { if (state.view !== 'claimable') void loadList(); });
  elements.viewButtons.forEach((button) => button.addEventListener('click', () => {
    if (button.disabled) return;
    state.view = button.dataset.taskView;
    renderCapabilities();
    if (state.view === 'claimable') elements.assignee.value = 'unassigned';
    else if (elements.assignee.value === 'unassigned') elements.assignee.value = 'all';
    void loadList();
  }));
  elements.dailyKind.addEventListener('change', () => { if (state.view === 'daily') void loadList(); });
  elements.kind.addEventListener('change', () => updateKindAssignees(true));
  elements.addWindow.addEventListener('click', () => elements.windowRows.append(createWindowRow()));
  elements.windowRows.addEventListener('click', (event) => {
    const remove = event.target.closest('[data-remove-window]');
    if (remove && !remove.disabled) { remove.closest('[data-window-row]').remove(); updateWindowButtons(); }
  });
  elements.recurrenceFrequency.addEventListener('change', syncRecurrenceFields);
  elements.addForm.addEventListener('submit', (event) => { void submitTask(event); });
  elements.recoverButton.addEventListener('click', () => { void recoverMutation(); });

  // 接続ボタンが押されるまでAPIを呼ばず、初期状態は観測不能として表示する。
  renderCapabilities();
  renderItems();
  updateConnectionState('認証が必要です', 'warning');
  updateSummary();
})();
