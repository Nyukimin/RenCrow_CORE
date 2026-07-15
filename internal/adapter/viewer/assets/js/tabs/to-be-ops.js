(function(root) {
  'use strict';

  const ALLOWED_STATUSES = new Set(['ok', 'warning', 'blocked', 'unavailable']);

  function arrayValue(value) {
    return Array.isArray(value) ? value : [];
  }

  function objectValue(value) {
    return value && typeof value === 'object' && !Array.isArray(value) ? value : {};
  }

  function field(value, ...names) {
    const source = objectValue(value);
    for (const name of names) {
      if (Object.prototype.hasOwnProperty.call(source, name) && source[name] !== undefined && source[name] !== null) {
        return source[name];
      }
    }
    return undefined;
  }

  function textValue(value, fallback) {
    const text = String(value === undefined || value === null ? '' : value).trim();
    return text || fallback || '';
  }

  function countValue(value, fallback) {
    const number = Number(value);
    if (Number.isInteger(number) && number >= 0) return number;
    return Number.isInteger(fallback) && fallback >= 0 ? fallback : 0;
  }

  function boolLabel(value, trueLabel, falseLabel) {
    return value === true ? trueLabel : falseLabel;
  }

  function normalizeToBeOpsStatus(value, fallback) {
    const normalized = textValue(value, fallback || 'warning').toLowerCase();
    return ALLOWED_STATUSES.has(normalized) ? normalized : (fallback || 'warning');
  }

  function statusFromSources(sources, hasError) {
    if (hasError) return 'unavailable';
    const statuses = sources.map((item) => normalizeToBeOpsStatus(field(item, 'status', 'Status'), 'ok'));
    if (statuses.length && statuses.every((status) => status === 'unavailable')) return 'unavailable';
    if (statuses.some((status) => status === 'blocked')) return 'blocked';
    if (statuses.some((status) => status !== 'ok')) return 'warning';
    return 'ok';
  }

  function metric(label, value) {
    return {label, value: String(value)};
  }

  function safeAdvisorDetails(advisor) {
    const runs = arrayValue(field(advisor, 'recent_runs', 'recentRuns')).slice(0, 3).map((run) => {
      return [
        'run ' + textValue(field(run, 'run_id', 'RunID'), '-'),
        'advisor ' + textValue(field(run, 'advisor_id', 'AdvisorID'), '-'),
        textValue(field(run, 'status', 'Status'), 'unknown'),
      ].join(' · ');
    });
    const decisions = arrayValue(field(advisor, 'policy_decisions', 'policyDecisions')).slice(0, 2).map((decision) => {
      return [
        'policy ' + textValue(field(decision, 'decision_id', 'DecisionID'), '-'),
        'agent ' + textValue(field(decision, 'agent_id', 'AgentID'), '-'),
        textValue(field(decision, 'decision', 'Decision'), 'unknown'),
      ].join(' · ');
    });
    return runs.concat(decisions);
  }

  function safeEconomicDetails(opportunities, tasks, reflections) {
    const details = [];
    for (const item of arrayValue(field(opportunities, 'opportunities', 'Opportunities')).slice(0, 2)) {
      details.push('opportunity ' + textValue(field(item, 'opportunity_id', 'OpportunityID'), '-') + ' · ' + textValue(field(item, 'approval_state', 'ApprovalState'), 'draft'));
    }
    for (const item of arrayValue(field(tasks, 'economic_tasks', 'EconomicTasks')).slice(0, 2)) {
      details.push('task ' + textValue(field(item, 'task_id', 'TaskID'), '-') + ' · ' + textValue(field(item, 'task_kind', 'TaskKind'), 'unknown') + ' · ' + textValue(field(item, 'status', 'Status'), 'unknown'));
    }
    for (const item of arrayValue(field(reflections, 'economic_reflections', 'EconomicReflections')).slice(0, 1)) {
      details.push('reflection ' + textValue(field(item, 'reflection_id', 'ReflectionID'), '-') + ' · ' + textValue(field(item, 'outcome', 'Outcome'), 'recorded'));
    }
    return details;
  }

  function pendingEconomicTasks(tasks) {
    return arrayValue(field(tasks, 'economic_tasks', 'EconomicTasks')).filter((item) => {
      const approvalMode = textValue(field(item, 'approval_mode', 'ApprovalMode')).toLowerCase();
      const status = textValue(field(item, 'status', 'Status')).toLowerCase();
      return approvalMode === 'human_required' && status !== 'completed' && status !== 'rejected';
    });
  }

  function pendingHumanDecisions(revenue) {
    return arrayValue(field(revenue, 'human_decisions', 'HumanDecisions')).filter((item) => {
      const approval = textValue(field(item, 'approval_status', 'ApprovalStatus')).toLowerCase();
      const gate = textValue(field(item, 'gate_status', 'GateStatus')).toLowerCase();
      return approval === 'pending' || gate === 'needs_review';
    });
  }

  function safeApprovalDetails(tasks, revenue) {
    const details = pendingEconomicTasks(tasks).slice(0, 3).map((item) => {
      return 'task ' + textValue(field(item, 'task_id', 'TaskID'), '-') +
        ' · target ' + textValue(field(item, 'opportunity_id', 'OpportunityID'), '-') +
        ' · ' + textValue(field(item, 'task_kind', 'TaskKind'), 'unknown') + ' · human_required';
    });
    for (const item of pendingHumanDecisions(revenue).slice(0, 2)) {
      details.push('decision ' + textValue(field(item, 'decision_id', 'DecisionID'), '-') +
        ' · target ' + textValue(field(item, 'subject_id', 'SubjectID'), '-') +
        ' · ' + textValue(field(item, 'decision_type', 'DecisionType'), 'unknown') +
        ' · ' + textValue(field(item, 'approval_status', 'ApprovalStatus'), 'pending'));
    }
    return details;
  }

  function traceSummary(traces) {
    const responses = arrayValue(field(traces, 'items', 'Items'));
    const relationItems = [];
    for (const trace of responses) {
      for (const item of arrayValue(field(trace, 'items', 'Items'))) {
        if (textValue(field(item, 'kind', 'Kind')).toLowerCase() !== 'knowledge_relation') continue;
        relationItems.push({trace, item});
      }
    }
    return {responses, relationItems};
  }

  function safeTraceDetails(summary) {
    return summary.relationItems.slice(0, 5).map(({trace, item}) => {
      return [
        'response ' + textValue(field(trace, 'response_id', 'ResponseID'), '-'),
        'role ' + textValue(field(trace, 'role', 'Role'), '-'),
        textValue(field(item, 'kind', 'Kind'), 'knowledge_relation'),
        'source ' + textValue(field(item, 'source_id', 'SourceID'), '-'),
        textValue(field(item, 'status', 'Status'), 'unknown'),
      ].join(' · ');
    });
  }

  function buildToBeOpsViewModel(input) {
    const data = objectValue(input);
    const errors = objectValue(data.errors);
    const advisor = objectValue(data.advisor);
    const advisorSummary = objectValue(field(advisor, 'summary', 'Summary'));
    let advisorStatus = statusFromSources([advisor], Boolean(errors.advisor));
    if (field(advisor, 'enabled', 'Enabled') === false && advisorStatus === 'ok') advisorStatus = 'warning';

    const knowledge = objectValue(data.knowledge);
    const knowledgeSummary = objectValue(field(knowledge, 'summary', 'Summary'));
    let knowledgeStatus = statusFromSources([knowledge], Boolean(errors.knowledge));
    if (field(knowledge, 'enabled', 'Enabled') === false && knowledgeStatus === 'blocked') knowledgeStatus = 'warning';
    if (field(knowledge, 'enabled', 'Enabled') === false && knowledgeStatus === 'ok') knowledgeStatus = 'warning';
    const knowledgeWarnings = arrayValue(field(knowledge, 'warnings', 'Warnings'));
    const knowledgeDetails = [];
    if (field(knowledge, 'enabled', 'Enabled') === false) knowledgeDetails.push('Feature disabled or not configured.');
    if (knowledgeWarnings.length > 0) knowledgeDetails.push(String(knowledgeWarnings.length) + ' relation warning(s) reported.');

    const opportunities = objectValue(data.opportunities);
    const tasks = objectValue(data.tasks);
    const reflections = objectValue(data.reflections);
    const revenue = objectValue(data.revenue);
    const economic = objectValue(field(revenue, 'economic_objective', 'EconomicObjective'));
    const opportunityItems = arrayValue(field(opportunities, 'opportunities', 'Opportunities'));
    const taskItems = arrayValue(field(tasks, 'economic_tasks', 'EconomicTasks'));
    const reflectionItems = arrayValue(field(reflections, 'economic_reflections', 'EconomicReflections'));
    let economicStatus = statusFromSources(
      [opportunities, tasks, reflections],
      Boolean(errors.opportunities || errors.tasks || errors.reflections || errors.revenue),
    );
    if (field(economic, 'enabled', 'Enabled') === false && economicStatus !== 'unavailable') economicStatus = 'warning';
    const pendingTasks = pendingEconomicTasks(tasks);
    const pendingDecisions = pendingHumanDecisions(revenue);
    let approvalStatus = economicStatus;
    if (pendingTasks.length + pendingDecisions.length > 0 && approvalStatus === 'ok') approvalStatus = 'warning';

    const traces = objectValue(data.traces);
    const recentTrace = traceSummary(traces);
    const traceStatus = statusFromSources([traces], Boolean(errors.traces));
    const injectedCount = recentTrace.relationItems.filter(({item}) => textValue(field(item, 'status', 'Status')).toLowerCase() === 'injected').length;

    return [
      {
        key: 'advisor-agent', title: 'Advisor / Agent', status: advisorStatus,
        metrics: [
          metric('Advisors', countValue(field(advisorSummary, 'advisor_count', 'AdvisorCount'))),
          metric('Agent profiles', countValue(field(advisorSummary, 'profile_count', 'ProfileCount'))),
          metric('Recent runs', countValue(field(advisorSummary, 'recent_run_count', 'RecentRunCount'))),
          metric('Failed runs', countValue(field(advisorSummary, 'failed_run_count', 'FailedRunCount'))),
          metric('Score snapshots', countValue(field(advisorSummary, 'score_snapshot_count', 'ScoreSnapshotCount'))),
        ],
        detailsLabel: 'Recent Advisor evidence', details: safeAdvisorDetails(advisor), emptyDetail: 'No recent Advisor evidence.',
      },
      {
        key: 'knowledge-relation', title: 'Knowledge Relation', status: knowledgeStatus,
        metrics: [
          metric('Enabled', boolLabel(field(knowledge, 'enabled', 'Enabled') === true, 'yes', 'no')),
          metric('Entities', countValue(field(knowledgeSummary, 'entity_count', 'EntityCount'))),
          metric('Item links', countValue(field(knowledgeSummary, 'item_entity_count', 'ItemEntityCount'))),
          metric('Relations', countValue(field(knowledgeSummary, 'relation_count', 'RelationCount'))),
          metric('Max hop', countValue(field(knowledgeSummary, 'max_hop', 'MaxHop'), 2)),
        ],
        detailsLabel: 'Relation availability', details: knowledgeDetails,
        emptyDetail: 'No relation warnings. Last build: ' + textValue(field(knowledgeSummary, 'last_build_status', 'LastBuildStatus'), 'not reported') + '.',
      },
      {
        key: 'economic-objective', title: 'Economic Objective', status: economicStatus,
        metrics: [
          metric('Enabled', boolLabel(field(economic, 'enabled', 'Enabled') === true, 'yes', 'no')),
          metric('Opportunities', countValue(field(opportunities, 'opportunity_count', 'OpportunityCount'), opportunityItems.length)),
          metric('Tasks', countValue(field(tasks, 'task_count', 'TaskCount'), taskItems.length)),
          metric('Reflections', countValue(field(reflections, 'reflection_count', 'ReflectionCount'), reflectionItems.length)),
          metric('Draft only', boolLabel(field(economic, 'draft_only', 'DraftOnly') !== false, 'yes', 'no')),
        ],
        detailsLabel: 'Draft economic records', details: safeEconomicDetails(opportunities, tasks, reflections), emptyDetail: 'No draft economic records.',
      },
      {
        key: 'approval-queue', title: 'Approval Queue', status: approvalStatus,
        metrics: [
          metric('Pending total', pendingTasks.length + pendingDecisions.length),
          metric('Economic tasks', pendingTasks.length),
          metric('Human decisions', pendingDecisions.length),
          metric('Approval mode', 'human'),
          metric('External action', 'blocked'),
        ],
        detailsLabel: 'Pending approval IDs', details: safeApprovalDetails(tasks, revenue), emptyDetail: 'No pending approvals.',
      },
      {
        key: 'recent-trace', title: 'Recent Trace', status: traceStatus,
        metrics: [
          metric('Responses', recentTrace.responses.length),
          metric('Relation items', recentTrace.relationItems.length),
          metric('Injected', injectedCount),
          metric('Not injected', Math.max(0, recentTrace.relationItems.length - injectedCount)),
          metric('View', 'safe IDs'),
        ],
        detailsLabel: 'Knowledge relation trace IDs', details: safeTraceDetails(recentTrace), emptyDetail: traceStatus === 'unavailable' ? 'Recall trace unavailable.' : 'No recent Knowledge Relation trace.',
      },
    ];
  }

  function escapeToBeOpsHTML(value) {
    return String(value === undefined || value === null ? '' : value)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }

  function renderToBeOpsHTML(model) {
    return arrayValue(model).map((block) => {
      const status = normalizeToBeOpsStatus(block.status, 'warning');
      const metrics = arrayValue(block.metrics).slice(0, 5).map((item) => {
        return '<div class="ops-to-be-metric"><dt>' + escapeToBeOpsHTML(item.label) + '</dt><dd>' + escapeToBeOpsHTML(item.value) + '</dd></div>';
      }).join('');
      const details = arrayValue(block.details).slice(0, 5);
      const detailHTML = details.length
        ? '<ul>' + details.map((item) => '<li>' + escapeToBeOpsHTML(item) + '</li>').join('') + '</ul>'
        : '<p>' + escapeToBeOpsHTML(block.emptyDetail || 'No details.') + '</p>';
      return '<article class="ops-to-be-block status-' + status + '" data-to-be-block="' + escapeToBeOpsHTML(block.key) + '">' +
        '<header class="ops-to-be-head"><h3>' + escapeToBeOpsHTML(block.title) + '</h3><span class="ops-to-be-status">' + escapeToBeOpsHTML(status) + '</span></header>' +
        '<dl class="ops-to-be-metrics">' + metrics + '</dl>' +
        '<details class="ops-to-be-details"><summary>' + escapeToBeOpsHTML(block.detailsLabel || 'Details') + '</summary><div class="ops-to-be-detail-body">' + detailHTML + '</div></details>' +
        '</article>';
    }).join('');
  }

  async function fetchToBeOpsJSON(path, options) {
    const settings = objectValue(options);
    const fetchImpl = typeof settings.fetchImpl === 'function' ? settings.fetchImpl : fetch;
    const configuredTimeout = Number(settings.timeoutMS);
    const timeoutMS = Number.isFinite(configuredTimeout) && configuredTimeout > 0 ? configuredTimeout : 5000;
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), timeoutMS);
    try {
      const response = await fetchImpl(path, {headers: {'Accept': 'application/json'}, signal: controller.signal});
      if (!response.ok) throw new Error('request unavailable');
      return response.json();
    } finally {
      clearTimeout(timeout);
    }
  }

  let refreshSequence = 0;

  async function refreshToBeOpsData() {
    if (typeof document === 'undefined') return [];
    const target = document.getElementById('toBeOpsSummary');
    if (!target) return [];
    const sequence = ++refreshSequence;
    target.setAttribute('aria-busy', 'true');
    const endpoints = {
      advisor: '/viewer/advisors?limit=5',
      knowledge: '/viewer/knowledge-relations/summary?limit=5',
      opportunities: '/viewer/revenue/opportunities?limit=5',
      tasks: '/viewer/revenue/economic-tasks?limit=5',
      reflections: '/viewer/revenue/economic-reflections?limit=5',
      revenue: '/viewer/revenue?limit=5',
      traces: '/viewer/recall/traces?limit=5',
    };
    const keys = Object.keys(endpoints);
    const settled = await Promise.allSettled(keys.map((key) => fetchToBeOpsJSON(endpoints[key])));
    if (sequence !== refreshSequence) return [];
    const data = {errors: {}};
    settled.forEach((result, index) => {
      const key = keys[index];
      if (result.status === 'fulfilled') data[key] = result.value;
      else data.errors[key] = true;
    });
    const model = buildToBeOpsViewModel(data);
    target.innerHTML = renderToBeOpsHTML(model);
    target.setAttribute('aria-busy', 'false');
    return model;
  }

  const api = {buildToBeOpsViewModel, renderToBeOpsHTML, refreshToBeOpsData, normalizeToBeOpsStatus, fetchToBeOpsJSON};
  if (root) root.refreshToBeOpsData = refreshToBeOpsData;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof window !== 'undefined' ? window : globalThis);
