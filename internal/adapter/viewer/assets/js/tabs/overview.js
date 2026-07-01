// Overview tab module: agent overview rendering.
function renderOverview() {
  const cards = document.getElementById('agentCards');
  const body = document.getElementById('overviewBody');
  cards.innerHTML = '';
  body.innerHTML = '';

  AGENTS.forEach((id) => {
    const s = state.agents[id];
    const info = ag(id);
    const card = document.createElement('div');
    card.className = 'card';
    card.innerHTML =
      '<h4>' + info.e + ' ' + info.l + '</h4>' +
      '<div class="row"><span>State</span><span class="badge ' + stateClass(s.state) + '">' + esc(s.state) + '</span></div>' +
      '<div class="row"><span>Reason</span><span>' + esc(s.reason || '-') + '</span></div>' +
      '<div class="row"><span>Route</span><span>' + esc(s.route || '-') + '</span></div>' +
      '<div class="row"><span>Open</span><span>' + esc(String(Object.keys(state.openTasks[id] || {}).length)) + '</span></div>' +
      '<div class="row"><span>Job</span><span class="code">' + esc(s.jobID || '-') + '</span></div>' +
      '<div class="row"><span>Updated</span><span>' + esc(ftime(s.updatedAt)) + '</span></div>';
    cards.appendChild(card);

    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td>' + info.e + ' ' + info.l + '</td>' +
      '<td><span class="badge ' + stateClass(s.state) + '">' + esc(s.state) + '</span></td>' +
      '<td>' + esc(s.reason || '-') + '</td>' +
      '<td>' + esc(s.route || '-') + '</td>' +
      '<td>' + esc(s.lastEvent || '-') + '</td>' +
      '<td>' + esc(agName(s.peer || '-')) + '</td>' +
      '<td class="code">' + esc(s.jobID || '-') + '</td>' +
      '<td>' + esc(ftime(s.updatedAt)) + '</td>' +
      '<td>' + esc((s.preview || '-') + ' | open: ' + openTaskSummary(id)) + '</td>';
    body.appendChild(tr);
  });
}
