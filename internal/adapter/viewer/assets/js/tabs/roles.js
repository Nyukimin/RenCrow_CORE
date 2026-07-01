// Roles tab module: role target selection and command routing helpers.
function selectedRoleTargetID() {
  return localStorage.getItem('roleSelector.selectedTarget') || '';
}

function selectRoleTarget(id) {
  localStorage.setItem('roleSelector.selectedTarget', String(id || ''));
  renderRoleSelector();
}

const VIEWER_CHAT_RECIPIENT_TARGETS = ['mio', 'shiro', 'kuro', 'midori'];

function viewerChatRecipientForTarget(id) {
  const normalized = String(id || '').trim().toLowerCase();
  if (!normalized) return 'mio';
  return VIEWER_CHAT_RECIPIENT_TARGETS.includes(normalized) ? normalized : '';
}

function selectedViewerChatRecipient() {
  return viewerChatRecipientForTarget(selectedRoleTargetID());
}

function applyRoleTargetToMessage(message) {
  const trimmed = String(message || '').trim();
  if (!trimmed) return '';
  if (/^\/(ops|wild|code|code1|code2|code3|code4)(\s|$)/.test(trimmed)) return trimmed;
  const selectedID = selectedRoleTargetID();
  if (viewerChatRecipientForTarget(selectedID)) return trimmed;
  const selected = ROLE_TARGETS.find((target) => target.id === selectedID);
  if (!selected || selected.id === 'mio') return trimmed;
  if (selected.id === 'coder1') return '/code1 ' + trimmed;
  if (selected.id === 'coder2') return '/code2 ' + trimmed;
  if (selected.id === 'coder3') return '/code3 ' + trimmed;
  if (selected.id === 'coder4') return '/code4 ' + trimmed;
  return trimmed;
}

function roleTargetSummary(target) {
  const info = ag(target.id);
  const agent = state.agents[target.id] || {};
  return info.e + ' ' + info.l + ' / ' + target.role + ' / ' + (agent.state || 'offline');
}

function renderRoleSelector() {
  const cards = document.getElementById('roleSelectorCards');
  const body = document.getElementById('roleSelectorBody');
  const detail = document.getElementById('roleSelectorDetail');
  if (!cards || !body || !detail) return;
  const filter = roleFilter ? String(roleFilter.value || '') : '';
  const selectedID = selectedRoleTargetID();
  const targets = ROLE_TARGETS.filter((target) => !filter || target.role === filter);
  cards.innerHTML = '';
  body.innerHTML = '';

  ROLE_TARGETS.forEach((target) => {
    const info = ag(target.id);
    const agent = state.agents[target.id] || {};
    const card = document.createElement('div');
    card.className = 'card' + (selectedID === target.id ? ' evi-selected' : '');
    card.innerHTML =
      '<h4>' + info.e + ' ' + info.l + '</h4>' +
      '<div class="row"><span>Role</span><span>' + esc(target.role) + '</span></div>' +
      '<div class="row"><span>Alias</span><span>' + esc(target.alias) + '</span></div>' +
      '<div class="row"><span>State</span><span class="badge ' + stateClass(agent.state || 'offline') + '">' + esc(agent.state || 'offline') + '</span></div>' +
      '<div class="ops-sub">' + esc(target.use) + '</div>';
    card.addEventListener('click', () => selectRoleTarget(target.id));
    cards.appendChild(card);
  });

  if (targets.length === 0) {
    body.innerHTML = '<tr><td colspan="7" class="small">No role targets</td></tr>';
  } else {
    targets.forEach((target) => {
      const info = ag(target.id);
      const agent = state.agents[target.id] || {};
      const tr = document.createElement('tr');
      if (selectedID === target.id) tr.classList.add('evi-selected');
      tr.innerHTML =
        '<td>' + info.e + ' ' + esc(info.l) + '</td>' +
        '<td>' + esc(target.role) + '</td>' +
        '<td>' + esc(target.alias) + '</td>' +
        '<td>' + esc(target.use) + '</td>' +
        '<td><span class="badge ' + stateClass(agent.state || 'offline') + '">' + esc(agent.state || 'offline') + '</span></td>' +
        '<td>' + esc(agent.route || '-') + '</td>' +
        '<td><button class="ctl-btn" onclick="selectRoleTarget(&quot;' + esc(target.id) + '&quot;)">Select</button></td>';
      tr.addEventListener('click', (evt) => {
        if (evt.target && evt.target.tagName === 'BUTTON') return;
        selectRoleTarget(target.id);
      });
      body.appendChild(tr);
    });
  }

  const selected = ROLE_TARGETS.find((target) => target.id === selectedID);
  if (!selected) {
    detail.innerHTML = '<h4>Selected Target</h4><div class="small">No role selected</div>';
    return;
  }
  const agent = state.agents[selected.id] || {};
  detail.innerHTML =
    '<h4>' + esc(roleTargetSummary(selected)) + '</h4>' +
    '<div class="row"><span>Model Alias</span><span>' + esc(selected.alias) + '</span></div>' +
    '<div class="row"><span>Route</span><span>' + esc(agent.route || '-') + '</span></div>' +
    '<div class="row"><span>Job</span><span class="code">' + esc(agent.jobID || '-') + '</span></div>' +
    '<div class="ops-sub">' + esc(selected.use) + '</div>';
}
