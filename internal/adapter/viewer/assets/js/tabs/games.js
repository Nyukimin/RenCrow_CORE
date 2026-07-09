// Games tab module: read-only RenCrow_GAMES / PicoClaw bridge observer.
function gamesBridgeField(item, key) {
  if (!item || !Object.prototype.hasOwnProperty.call(item, key)) return '';
  return item[key];
}

function gamesBridgeState() {
  const ops = state && state.ops ? state.ops : {};
  return {
    status: ops.gameBridgeStatus || null,
    sessions: Array.isArray(ops.gameBridgeSessions) ? ops.gameBridgeSessions : [],
    events: Array.isArray(ops.gameBridgeEvents) ? ops.gameBridgeEvents : [],
    statusError: String(ops.gameBridgeStatusFetchError || '').trim(),
    sourceError: String(ops.gameBridgeSourceFetchError || '').trim(),
    skipped: Number(ops.gameBridgeSkippedCount || 0),
  };
}

function gamesInfoTile(label, value) {
  return '<div class="games-info-tile"><small>' + esc(label) + '</small><strong>' + esc(value || '-') + '</strong></div>';
}

function gamesBridgeStatusText(data) {
  if (data.statusError) return 'unavailable';
  if (!data.status) return 'checking';
  return data.status.ok === true ? 'ok' : 'unavailable';
}

function renderGamesStatusCard(data) {
  const target = document.getElementById('gamesBridgeStatusCard');
  if (!target) return;
  const status = data || gamesBridgeState();
  const bridge = gamesBridgeStatusText(status);
  const mode = status.status || {};
  const lines = [
    gamesInfoTile('Bridge', bridge),
    gamesInfoTile('Decision', mode.decision_mode || '-'),
    gamesInfoTile('Result', mode.result_mode || '-'),
    gamesInfoTile('Memory', mode.memory_mode || 'candidate_only'),
    gamesInfoTile('Sessions', String(status.sessions.length)),
    gamesInfoTile('Events', String(status.events.length)),
  ].join('');
  const errors = [status.statusError, status.sourceError].filter(Boolean);
  target.innerHTML =
    '<h3>Game Bridge</h3>' +
    '<div class="games-info-grid">' + lines + '</div>' +
    (errors.length ? '<div class="daily-desk-muted games-error">' + esc(errors.join('\n')) + '</div>' : '') +
    (status.skipped > 0 ? '<div class="daily-desk-muted">skipped malformed rows: ' + esc(status.skipped) + '</div>' : '') +
    '<div class="daily-desk-muted">candidate-only: not confirmed</div>';
}

function renderGamesLatestSession(data) {
  const target = document.getElementById('gamesLatestSessionCard');
  if (!target) return;
  const session = (data || gamesBridgeState()).sessions[0] || null;
  if (!session) {
    target.innerHTML = '<h3>Latest Session</h3><div class="daily-desk-muted">session record なし</div>';
    return;
  }
  target.innerHTML =
    '<h3>Latest Session</h3>' +
    '<div class="games-info-grid">' +
      gamesInfoTile('Game', gamesBridgeField(session, 'game_id')) +
      gamesInfoTile('Session', gamesBridgeField(session, 'session_id')) +
      gamesInfoTile('Persona', gamesBridgeField(session, 'persona')) +
      gamesInfoTile('Turn', gamesBridgeField(session, 'latest_turn') || gamesBridgeField(session, 'turn')) +
      gamesInfoTile('Candidates', gamesBridgeField(session, 'candidate_count')) +
      gamesInfoTile('Updated', fdt(gamesBridgeField(session, 'updated_at'))) +
    '</div>' +
    '<div class="daily-desk-muted games-code">' + esc(gamesBridgeField(session, 'latest_event_id') || '-') + '</div>';
}

function renderGamesLatestEvent(data) {
  const target = document.getElementById('gamesLatestEventCard');
  if (!target) return;
  const event = (data || gamesBridgeState()).events[0] || null;
  if (!event) {
    target.innerHTML = '<h3>Latest Event</h3><div class="daily-desk-muted">event record なし</div>';
    return;
  }
  const resultEvents = Array.isArray(event.result_events) ? event.result_events.join(', ') : '';
  target.innerHTML =
    '<h3>Latest Event</h3>' +
    '<div class="games-info-grid">' +
      gamesInfoTile('Intent', gamesBridgeField(event, 'decision_intent')) +
      gamesInfoTile('Executed', Array.isArray(event.executed_actions) ? event.executed_actions.join(', ') : '-') +
      gamesInfoTile('Result', resultEvents || '-') +
      gamesInfoTile('Memory', gamesBridgeField(event, 'memory_state')) +
      gamesInfoTile('Turn', gamesBridgeField(event, 'turn')) +
      gamesInfoTile('Created', fdt(gamesBridgeField(event, 'created_at'))) +
    '</div>' +
    '<div class="daily-desk-muted games-code">' + esc(gamesBridgeField(event, 'event_id') || '-') + '</div>';
}

function renderGamesSessions(data) {
  const target = document.getElementById('gamesSessionsCard');
  if (!target) return;
  const sessions = (data || gamesBridgeState()).sessions;
  const rows = sessions.map((session) => (
    '<tr>' +
      '<td>' + esc(gamesBridgeField(session, 'game_id') || '-') + '</td>' +
      '<td class="code games-code">' + esc(gamesBridgeField(session, 'session_id') || '-') + '</td>' +
      '<td>' + esc(gamesBridgeField(session, 'persona') || '-') + '</td>' +
      '<td>' + esc(gamesBridgeField(session, 'latest_turn') || gamesBridgeField(session, 'turn') || '-') + '</td>' +
      '<td>' + esc(gamesBridgeField(session, 'decision_mode') || '-') + '</td>' +
      '<td>' + esc(gamesBridgeField(session, 'result_mode') || '-') + '</td>' +
      '<td>' + esc(gamesBridgeField(session, 'memory_mode') || '-') + '</td>' +
    '</tr>'
  )).join('');
  target.innerHTML =
    '<h3>Recent Sessions</h3>' +
    '<div class="table-wrap games-table-wrap"><table class="compact-table"><thead><tr><th>game</th><th>session</th><th>persona</th><th>turn</th><th>decision</th><th>result</th><th>memory</th></tr></thead><tbody>' +
    (rows || '<tr><td colspan="7" class="small">session record なし</td></tr>') +
    '</tbody></table></div>';
}

function renderGamesEvents(data) {
  const target = document.getElementById('gamesEventsCard');
  if (!target) return;
  const events = (data || gamesBridgeState()).events;
  const rows = events.map((event) => (
    '<tr>' +
      '<td>' + esc(gamesBridgeField(event, 'turn') || '-') + '</td>' +
      '<td>' + esc(gamesBridgeField(event, 'decision_intent') || '-') + '</td>' +
      '<td>' + esc(Array.isArray(event.executed_actions) ? event.executed_actions.join(', ') : '-') + '</td>' +
      '<td>' + esc(Array.isArray(event.result_events) ? event.result_events.join(', ') : '-') + '</td>' +
      '<td>' + esc(gamesBridgeField(event, 'memory_state') || '-') + '</td>' +
      '<td class="code games-code">' + esc(gamesBridgeField(event, 'event_id') || '-') + '</td>' +
    '</tr>'
  )).join('');
  target.innerHTML =
    '<h3>Recent Events</h3>' +
    '<div class="table-wrap games-table-wrap"><table class="compact-table"><thead><tr><th>turn</th><th>intent</th><th>executed</th><th>events</th><th>memory</th><th>event</th></tr></thead><tbody>' +
    (rows || '<tr><td colspan="6" class="small">event record なし</td></tr>') +
    '</tbody></table></div>';
}

function bindGamesDeskControls() {
  const refresh = document.getElementById('gamesRefreshBtn');
  if (refresh && refresh.dataset.bound !== '1') {
    refresh.dataset.bound = '1';
    refresh.addEventListener('click', () => {
      if (typeof refreshGameBridgeData === 'function') refreshGameBridgeData();
      renderGamesDesk();
    });
  }
}

function renderGamesDesk() {
  bindGamesDeskControls();
  const data = gamesBridgeState();
  renderGamesStatusCard(data);
  renderGamesLatestSession(data);
  renderGamesLatestEvent(data);
  renderGamesSessions(data);
  renderGamesEvents(data);
}
