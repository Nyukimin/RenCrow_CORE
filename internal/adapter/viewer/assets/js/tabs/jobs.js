// Jobs tab module: job table rendering.
function renderJobs() {
  const body = document.getElementById('jobsBody');
  body.innerHTML = '';
  const list = Object.values(state.jobs).sort((a, b) => (b.updatedAt || '').localeCompare(a.updatedAt || ''));

  if (list.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="8" class="small">No job data yet</td>';
    body.appendChild(tr);
    return;
  }

  list.forEach((j) => {
    const st = j.status === 'error' ? 'error' : (j.status === 'done' ? 'idle' : 'running');
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td class="code">' + esc(j.id) + '</td>' +
      '<td>' + esc(j.route || '-') + '</td>' +
      '<td><span class="badge ' + stateClass(st) + '">' + esc(j.status) + '</span></td>' +
      '<td>' + esc(agName(j.from || '-') + ' -> ' + agName(j.to || '-')) + '</td>' +
      '<td>' + esc(fdt(j.startedAt)) + '</td>' +
      '<td>' + esc(fdt(j.updatedAt)) + '</td>' +
      '<td>' + esc(String(j.events || 0)) + '</td>' +
      '<td>' + esc(short(j.preview || '-', 90)) + '</td>';
    body.appendChild(tr);
  });
}
