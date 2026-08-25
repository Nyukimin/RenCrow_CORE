import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

function investmentHarness(payload) {
  const elements = new Map([
    'investmentStateBadge',
    'investmentStatusCard',
    'investmentSummaryCards',
    'investmentDependencyCard',
    'investmentPortfolioCard',
    'investmentCapabilityCard',
    'investmentRefreshBtn',
  ].map((id) => [id, {dataset: {}, innerHTML: '', textContent: '', className: '', addEventListener() {}}]));
  const requests = [];
  const context = {
    console,
    state: {investment: {}},
    document: {getElementById(id) { return elements.get(id) || null; }},
    esc(value) { return String(value ?? '').replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;'); },
    fdt(value) { return String(value); },
    fetch(path) {
      requests.push(path);
      return Promise.resolve({ok: true, json: () => Promise.resolve(payload)});
    },
  };
  vm.createContext(context);
  vm.runInContext(fs.readFileSync('internal/adapter/viewer/assets/js/tabs/investment.js', 'utf8'), context);
  return {context, elements, requests};
}

test('Investment Viewer reads and renders the RenCrow_TRADE owner projection', async () => {
  const {context, elements, requests} = investmentHarness({
    bridge_status: 'connected',
    contract_version: 'trade-private/v1',
    service_status: 'ready',
    correlation_id: 'trade-1',
    execution_mode: 'DISABLED',
    learning_mode: 'OFFLINE_AVAILABLE',
    ready: true,
    kill_switch: 'ON',
    dependencies: {broker: 'disabled', ledger: 'ready', market_data: 'unavailable', memory_owner: 'ready'},
    policy_id: 'trade-policy',
    module_policy_revision: 'sha256:module',
    binary_contract_revision: 'trade-binary/v1',
    capabilities: {portfolio_simulation_commit: true, live_order: false},
    portfolio: {status: 'ready', snapshot: {portfolio_id: 'main-sim', mode: 'SIMULATION', nav_jpy: 1000000, cash_jpy: 1000000, positions: []}},
  });

  await context.refreshInvestmentData();

  assert.deepEqual(requests, ['/viewer/trade/status']);
  assert.equal(context.state.investment.ownerRoute, '/viewer/trade/status');
  assert.equal(context.state.investment.ready, true);
  assert.match(elements.get('investmentStatusCard').innerHTML, /RenCrow_TRADE/);
  assert.match(elements.get('investmentDependencyCard').innerHTML, /Ledger/);
  assert.match(elements.get('investmentPortfolioCard').innerHTML, /main-sim/);
  assert.match(elements.get('investmentCapabilityCard').innerHTML, /portfolio_simulation_commit/);
  for (const element of elements.values()) {
    assert.doesNotMatch(element.innerHTML, /investment\/rencrow\.db|DB path/);
  }
});
