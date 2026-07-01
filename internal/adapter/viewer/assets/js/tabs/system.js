// System tab module: filtered system event rendering.
function renderSystem() {
  const body = document.getElementById('systemBody');
  if (!body) return;
  body.innerHTML = '';
  const list = state.logs.filter(matchesSystemFilters).slice().reverse();
  if (list.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="8" class="small">No system events</td>';
    body.appendChild(tr);
    return;
  }
  list.forEach((ev) => {
    const raw = String(ev.content || '');
    const linePayload = JSON.stringify({
      timestamp: ev.timestamp || '',
      type: ev.type || '',
      from: ev.from || '',
      to: ev.to || '',
      route: ev.route || '',
      job_id: ev.job_id || '',
      content: raw,
    }, null, 2);
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td>' + esc(ftime(ev.timestamp)) + '</td>' +
      '<td>' + esc(ev.type || '-') + '</td>' +
      '<td>' + esc(agName(ev.from || '-')) + '</td>' +
      '<td>' + esc(agName(ev.to || '-')) + '</td>' +
      '<td>' + esc(ev.route || '-') + '</td>' +
      '<td class="code">' + esc(ev.job_id || '-') + '</td>' +
      '<td><div class="sys-content" data-raw="' + esc(raw) + '">' + esc(raw || '-') + '</div></td>' +
      '<td><div class="sys-actions">' +
        '<button class="ctl-btn" onclick="copyTextPayload(this, ' + escAttr(JSON.stringify(raw)) + ')">Text</button>' +
        '<button class="ctl-btn" onclick="copyTextPayload(this, ' + escAttr(JSON.stringify(linePayload)) + ')">Row</button>' +
      '</div></td>';
    body.appendChild(tr);
  });
}
