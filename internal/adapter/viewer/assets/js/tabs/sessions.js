// Sessions tab module: session table rendering.
function renderSessions() {
  const body = document.getElementById('sessionsBody');
  body.innerHTML = '';
  const list = Object.values(state.sessions).sort((a, b) => (b.updatedAt || '').localeCompare(a.updatedAt || ''));

  if (list.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="8" class="small">No session data yet</td>';
    body.appendChild(tr);
    return;
  }

  list.forEach((s) => {
    const agents = Object.keys(s.agents).filter((x) => AGENTS.includes(x) || x === 'user').map((x) => agName(x)).join(', ') || '-';
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td class="code">' + esc(s.id) + '</td>' +
      '<td>' + esc(s.channel || '-') + '</td>' +
      '<td class="code">' + esc(s.chatID || '-') + '</td>' +
      '<td>' + esc(String(s.count)) + '</td>' +
      '<td>' + esc(s.lastRoute || '-') + '</td>' +
      '<td>' + esc(short(s.lastUserMessage || '-', 80)) + '</td>' +
      '<td>' + esc(agents) + '</td>' +
      '<td>' + esc(fdt(s.updatedAt)) + '</td>';
    body.appendChild(tr);
  });
}
