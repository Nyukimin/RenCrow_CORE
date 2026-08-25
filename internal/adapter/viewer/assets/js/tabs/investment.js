// Investment tab projection. RenCrow_TRADE is the sole owner of this state.

function investmentStateView() {
  return state.investment || {};
}

function investmentStatusClass(status) {
  const value = String(status || '').toLowerCase();
  if (value === 'ok' || value === 'ready' || value === 'connected' || value === 'success') return 'running';
  if (value === 'disabled' || value === 'unconfigured' || value === 'unknown') return 'idle';
  if (value === 'warn' || value === 'partial') return 'thinking';
  if (value === 'unavailable' || value === 'fail' || value === 'blocked') return 'error';
  return 'idle';
}

function investmentValueText(value, fallback) {
  if (value == null || value === '') return fallback || '-';
  return String(value);
}

function investmentRow(label, value, className) {
  const cls = className ? ' class="' + className + '"' : '';
  return '<div class="desk-row"><span>' + esc(label) + '</span><span' + cls + '>' + esc(investmentValueText(value, '-')) + '</span></div>';
}

function investmentSummaryCards(data) {
  const dependencies = data.dependencies || {};
  return [
    {title: 'Bridge', big: data.bridgeStatus || 'unavailable', sub: data.ownerRoute || '/viewer/trade/status'},
    {title: 'Service', big: data.serviceStatus || 'unavailable', sub: data.contractVersion || 'contract unavailable'},
    {title: 'Learning', big: data.learningMode || '-', sub: 'RenCrow_TRADE owner'},
    {title: 'Execution', big: data.executionMode || '-', sub: 'broker=' + investmentValueText(dependencies.broker, '-')},
    {title: 'Kill switch', big: data.killSwitch || '-', sub: data.ready ? 'trade owner ready' : 'owner not ready'},
  ];
}

function renderInvestmentSummaryCards(target, data) {
  if (!target) return;
  target.innerHTML = investmentSummaryCards(data).map((item) => (
    '<div class="daily-desk-card">' +
      '<h3>' + esc(item.title) + '</h3>' +
      '<div class="ops-big">' + esc(item.big) + '</div>' +
      '<div class="ops-sub">' + esc(item.sub) + '</div>' +
    '</div>'
  )).join('');
}

function renderInvestmentDesk() {
  const data = investmentStateView();
  const badge = document.getElementById('investmentStateBadge');
  const statusCard = document.getElementById('investmentStatusCard');
  const summaryCards = document.getElementById('investmentSummaryCards');
  const dependencyCard = document.getElementById('investmentDependencyCard');
  const portfolioCard = document.getElementById('investmentPortfolioCard');
  const capabilityCard = document.getElementById('investmentCapabilityCard');
  if (!badge || !statusCard || !summaryCards || !dependencyCard || !portfolioCard || !capabilityCard) return;

  const status = data.ready ? 'ready' : (data.bridgeStatus || 'unavailable');
  badge.className = 'desk-status-pill ' + investmentStatusClass(status);
  badge.textContent = data.loading ? 'loading' : status;

  statusCard.innerHTML =
    '<h3>RenCrow_TRADE owner projection</h3>' +
    investmentRow('Owner', 'RenCrow_TRADE') +
    investmentRow('Owner route', data.ownerRoute || '/viewer/trade/status', 'desk-code') +
    investmentRow('Bridge', data.bridgeStatus) +
    investmentRow('Service', data.serviceStatus) +
    investmentRow('Correlation', data.correlationID, 'desk-code') +
    investmentRow('Refreshed', data.refreshedAt ? fdt(data.refreshedAt) : '-') +
    (data.statusMessage ? investmentRow('Message', data.statusMessage) : '');

  renderInvestmentSummaryCards(summaryCards, data);

  const dependencies = data.dependencies || {};
  dependencyCard.innerHTML =
    '<h3>Dependencies</h3>' +
    investmentRow('Broker', dependencies.broker) +
    investmentRow('Ledger', dependencies.ledger) +
    investmentRow('Market data', dependencies.market_data) +
    investmentRow('Memory owner', dependencies.memory_owner);

  const portfolio = data.portfolio || {};
  const snapshot = portfolio.snapshot || {};
  portfolioCard.innerHTML =
    '<h3>Portfolio</h3>' +
    investmentRow('Status', portfolio.status) +
    investmentRow('Portfolio ID', snapshot.portfolio_id, 'desk-code') +
    investmentRow('Mode', snapshot.mode) +
    investmentRow('NAV JPY', snapshot.nav_jpy == null ? '-' : snapshot.nav_jpy) +
    investmentRow('Cash JPY', snapshot.cash_jpy == null ? '-' : snapshot.cash_jpy) +
    investmentRow('Positions', Array.isArray(snapshot.positions) ? snapshot.positions.length : 0);

  const capabilities = data.capabilities || {};
  const capabilityNames = Object.keys(capabilities).sort();
  capabilityCard.innerHTML = '<h3>Capabilities</h3>' + (
    capabilityNames.length ? capabilityNames.map((name) => (
      investmentRow(name, capabilities[name] ? 'allowed' : 'blocked', 'desk-pill ' + (capabilities[name] ? 'running' : 'error'))
    )).join('') : investmentRow('Status', 'unavailable')
  ) + investmentRow('Policy', data.policyID, 'desk-code') +
    investmentRow('Module policy', data.modulePolicyRevision, 'desk-code') +
    investmentRow('Binary contract', data.binaryPolicyRevision, 'desk-code');
}

function refreshInvestmentData() {
  const data = investmentStateView();
  data.loading = true;
  data.fetchError = '';
  return fetch('/viewer/trade/status')
    .then((response) => {
      if (!response.ok) {
        return response.text().then((text) => {
          throw new Error('HTTP ' + String(response.status) + ': ' + (text || response.statusText || 'TRADE status unavailable'));
        });
      }
      return response.json();
    })
    .then((payload) => {
      state.investment = Object.assign({}, data, {
        available: Boolean(payload.ready),
        status: payload.ready ? 'ready' : String(payload.bridge_status || 'unavailable'),
        statusMessage: String(payload.reason_code || ''),
        ownerRoute: '/viewer/trade/status',
        bridgeStatus: String(payload.bridge_status || 'unavailable'),
        contractVersion: String(payload.contract_version || ''),
        serviceStatus: String(payload.service_status || ''),
        correlationID: String(payload.correlation_id || ''),
        executionMode: String(payload.execution_mode || ''),
        learningMode: String(payload.learning_mode || ''),
        ready: Boolean(payload.ready),
        killSwitch: String(payload.kill_switch || ''),
        dependencies: payload.dependencies || {},
        policyID: String(payload.policy_id || ''),
        modulePolicyRevision: String(payload.module_policy_revision || ''),
        binaryPolicyRevision: String(payload.binary_policy_revision || ''),
        capabilities: payload.capabilities || {},
        portfolio: payload.portfolio || {},
        refreshedAt: new Date().toISOString(),
        loading: false,
      });
      renderInvestmentDesk();
    })
    .catch((error) => {
      state.investment = Object.assign({}, data, {
        available: false,
        ready: false,
        status: 'unavailable',
        bridgeStatus: 'unavailable',
        statusMessage: String(error && error.message ? error.message : error),
        fetchError: String(error && error.message ? error.message : error),
        loading: false,
      });
      renderInvestmentDesk();
      console.error(error);
    });
}

function bindInvestmentDeskControls() {
  const button = document.getElementById('investmentRefreshBtn');
  if (!button || button.dataset.bound === '1') return;
  button.dataset.bound = '1';
  button.addEventListener('click', refreshInvestmentData);
}
