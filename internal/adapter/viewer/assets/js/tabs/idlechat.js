// IdleChat tab module: mode controls, subviews, history, and summary review.
const idleLiveBootedAtMs = Date.now();
const idleSuppressedTTSMessageIds = new Set();

function removeIdleLiveEmpty() {
  const target = idleLiveRenderTarget();
  if (!target) return;
  if (target === chat) {
    const empty = document.getElementById('empty');
    if (empty) empty.remove();
    return;
  }
  const empty = target.querySelector('.idle-live-empty');
  if (empty) empty.remove();
}

function isViewerLiveMode() {
  return !!(document.body && document.body.classList.contains('live-mode'));
}

function idleLiveRenderTarget() {
  // Rendering target only. This is not transcript truth, ACK truth, or session truth.
  // live mode is the theater view and uses the central chat stream; the IdleChat tab log is
  // for normal mode only and must not be used as the live-mode observation selector.
  if (isViewerLiveMode() && chat) return chat;
  return idleLiveLog;
}

function clearIdleLiveTimelineForTopic(ev) {
	const target = idleLiveRenderTarget();
	if (!target || !isIdleTopicEvent(ev)) return;
	const key = idleTopicKey(ev);
	if (!key || key === idleLiveTopicKey) return;
	idleLiveTopicKey = key;
	idleLiveActiveSessionId = String((ev && (ev.session_id || ev.chat_id)) || '').trim();
	idlePendingMessages.clear();
	resetTTSSpeechBubble(idleTTSSpeech);
	if (typeof target.replaceChildren === 'function') target.replaceChildren();
  else {
    target.innerHTML = '';
    if (Array.isArray(target.children)) target.children.length = 0;
  }
}

function idleTopicKey(ev) {
  const sid = String((ev && (ev.session_id || ev.chat_id)) || '').trim();
  const content = normalizeViewerDisplayText((ev && ev.content) || '').trim();
  return sid + '|' + content;
}

function recordIdleLiveRendered(kind, ev, text) {
  // Diagnostic write-only trace for tests/debugging. Runtime decisions must use live events,
  // backend status/log endpoints, or explicit playback state, never this rendered history.
  idleLiveRenderedLog.push({
    kind,
    from: String((ev && ev.from) || ''),
    to: String((ev && ev.to) || ''),
    session_id: String((ev && (ev.session_id || ev.chat_id)) || ''),
    message_id: String((ev && (ev.message_id || ev.messageId)) || ''),
    turn_index: ev && (ev.turn_index ?? ev.turnIndex ?? ''),
    content: String(text || ''),
    timestamp: String((ev && ev.timestamp) || new Date().toISOString()),
  });
  while (idleLiveRenderedLog.length > 200) idleLiveRenderedLog.shift();
}

function recordIdleLiveIdentityError(reason, ev, detail) {
  const payload = {
    reason: String(reason || 'identity_mismatch'),
    detail: String(detail || ''),
  };
  recordIdleLiveRendered('identity_error', ev, JSON.stringify(payload));
  try {
    console.warn('[IdleChat] conversation id mismatch:', payload, ev);
  } catch (_) {}
}

function recordIdleLiveDiagnostic(kind, ev, payload) {
  recordIdleLiveRendered(kind, ev, JSON.stringify(payload || {}));
}

function idleEsc(s) {
  if (typeof esc === 'function') return esc(s);
  return String(s || '').replace(/[&<>"']/g, (ch) => ({
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#39;',
  })[ch]);
}

function idlePendingQueue(sessionId) {
  const sid = String(sessionId || '').trim() || 'idlechat';
  if (!idlePendingMessages.has(sid)) idlePendingMessages.set(sid, []);
  return idlePendingMessages.get(sid);
}

function queueIdleMessageForTTS(ev) {
  if (!ev || (ev.type !== 'idlechat.message' && ev.type !== 'idlechat.topic')) return;
  if (isIdleLiveHistoricalEvent(ev)) return;
  const sid = String(ev.session_id || ev.chat_id || '').trim() || 'idlechat';
  if (idleLiveIdentityConflict(ev)) return;
  const liveMode = isViewerLiveMode();
  if (liveMode && typeof isThisViewerActiveAudio === 'function' && !isThisViewerActiveAudio()) {
    recordIdleLiveDiagnostic('pending_skipped', ev, {
      error_code: 'NON_ACTIVE_AUDIO_VIEWER_PENDING_SKIPPED',
      reason: 'Viewer is not the active audio owner; it did not arm an IdleChat TTS pending timeout.',
      session_id: sid,
      message_id: String(ev.message_id || '').trim(),
      turn_index: idleTurnIndex(ev),
      active_audio_viewer_id: String((typeof viewerControl !== 'undefined' && viewerControl && viewerControl.activeAudioViewerId) || '').trim(),
      viewer_client_id: String((typeof viewerControl !== 'undefined' && viewerControl && viewerControl.clientId) || '').trim(),
    });
    return;
  }
  if (isIdleTTSAudioDisabled()) {
    if (!isIdleSummarySpeechEvent(ev)) {
      appendIdleLiveMessageEvent(ev, {pending: false});
    }
    return;
  }
  const existing = findIdleLiveMessageNode(ev);
  if (existing && !existing.classList.contains('idle-pending-tts')) return;
  const queue = idlePendingQueue(sid);
  const messageId = String(ev.message_id || '').trim();
  const turnIndex = idleTurnIndex(ev);
  const suppressDisplay = isIdleSummarySpeechEvent(ev);
  if (suppressDisplay && messageId) idleSuppressedTTSMessageIds.add(messageId);
  if (messageId && queue.some((item) => !item.consumed && item.messageId === messageId)) return;
  if (!messageId && turnIndex >= 0 && queue.some((item) => !item.consumed && item.turnIndex === turnIndex)) return;
  const el = suppressDisplay ? null : (liveMode ? null : (existing || appendIdleLiveMessageEvent(ev, {pending: true})));
  const item = {
    ev,
    el,
    from: String(ev.from || '').trim().toLowerCase(),
    messageId,
    turnIndex,
    suppressDisplay,
    consumed: false,
    timer: null,
  };
  item.timer = suppressDisplay ? null : setTimeout(() => {
    if (item.consumed) return;
    if (isIdleTTSAudioDisabled()) {
      renderIdlePendingMessageFromEvent(item);
      item.consumed = true;
      pruneIdlePendingQueue(sid);
      return;
    }
    renderIdlePendingTTSError(item, 'TTS_CHUNK_TIMEOUT', 'TTS chunk was not rendered before the pending display timeout.');
    item.consumed = true;
    pruneIdlePendingQueue(sid);
  }, IDLE_MESSAGE_FALLBACK_MS);
  queue.push(item);
}

function isIdleSummarySpeechEvent(ev) {
  if (!ev || ev.type !== 'idlechat.message') return false;
  if (String(ev.to || '').trim().toLowerCase() !== 'user') return false;
  return normalizeViewerDisplayText(ev.content).trim().startsWith('今回のまとめです。');
}

function isIdleSuppressedTTSMessage(messageId) {
  const id = String(messageId || '').trim();
  return !!id && idleSuppressedTTSMessageIds.has(id);
}

function isIdleTTSAudioDisabled() {
  return typeof ttsPlayback !== 'undefined' && ttsPlayback && ttsPlayback.audioEnabled === false;
}

function clearIdleLivePendingForAudioOwnerTransfer(ownerId) {
  if (!isViewerLiveMode() || !idlePendingMessages || idlePendingMessages.size === 0) return;
  idlePendingMessages.forEach((queue, sid) => {
    (queue || []).forEach((item) => {
      if (!item || item.consumed) return;
      item.consumed = true;
      if (item.timer) clearTimeout(item.timer);
      recordIdleLiveDiagnostic('pending_skipped', item.ev, {
        error_code: 'NON_ACTIVE_AUDIO_VIEWER_PENDING_SKIPPED',
        reason: 'Active audio owner changed before this Viewer rendered the pending IdleChat TTS chunk.',
        session_id: String(sid || '').trim(),
        message_id: String(item.messageId || '').trim(),
        turn_index: Number.isFinite(item.turnIndex) ? item.turnIndex : -1,
        active_audio_viewer_id: String(ownerId || '').trim(),
        viewer_client_id: String((typeof viewerControl !== 'undefined' && viewerControl && viewerControl.clientId) || '').trim(),
      });
    });
  });
  idlePendingMessages.clear();
}

function isIdleLiveHistoricalEvent(ev) {
  if (!isViewerLiveMode()) return false;
  const raw = String((ev && ev.timestamp) || '').trim();
  if (!raw) return false;
  const eventMs = Date.parse(raw);
  if (!Number.isFinite(eventMs)) return false;
  return eventMs < idleLiveBootedAtMs - 2000;
}

function consumeIdlePendingMessage(sessionId, characterId, kind, messageId, turnIndex) {
  const sid = String(sessionId || '').trim() || 'idlechat';
  const queue = idlePendingMessages.get(sid);
  if (!queue || queue.length === 0) return;
  const expectedKind = String(kind || '').trim().toLowerCase();
  const expectedMessageId = String(messageId || '').trim();
  const expectedTurnIndex = Number.isFinite(turnIndex) ? Math.floor(turnIndex) : -1;
  let idx = -1;
  if (expectedMessageId) {
    idx = queue.findIndex((item) => !item.consumed && item.messageId === expectedMessageId);
  }
  if (idx < 0 && expectedTurnIndex >= 0) {
    idx = queue.findIndex((item) => !item.consumed && item.turnIndex === expectedTurnIndex);
  }
  if (idx < 0 && expectedKind === 'topic') {
    idx = queue.findIndex((item) => !item.consumed && isIdleTopicEvent(item.ev));
  }
  if (idx < 0) return;
  const item = queue[idx];
  item.consumed = true;
  if (item.timer) clearTimeout(item.timer);
  queue.splice(idx, 1);
  if (queue.length === 0) idlePendingMessages.delete(sid);
  return item;
}

function pruneIdlePendingQueue(sessionId) {
  const sid = String(sessionId || '').trim() || 'idlechat';
  const queue = idlePendingMessages.get(sid);
  if (!queue) return;
  const kept = queue.filter((item) => !item.consumed);
  if (kept.length === 0) idlePendingMessages.delete(sid);
  else idlePendingMessages.set(sid, kept);
}

function addIdleMsgToTimeline(ev) {
		if (!idleLiveRenderTarget() || !ev || (ev.type !== 'idlechat.message' && ev.type !== 'idlechat.topic')) return;
		clearIdleLiveTimelineForTopic(ev);
		if (isIdleTopicEvent(ev) && document.body && document.body.classList.contains('live-mode')) return;
	const sid = String(ev.session_id || ev.chat_id || '').trim();
	if (!isIdleTopicEvent(ev) && idleLiveActiveSessionId && sid && sid !== idleLiveActiveSessionId) return;
	queueIdleMessageForTTS(ev);
}

function hydrateIdleLiveTranscript(sessionId, transcript) {
	const target = idleLiveRenderTarget();
	if (!target || !(document.body && document.body.classList.contains('live-mode'))) return;
	const sid = String(sessionId || '').trim();
	if (!sid) return;
	const rows = Array.isArray(transcript) ? transcript : [];
	const key = idleTranscriptSnapshotKey(sid, rows);
	if (key === idleLiveSnapshotKey) return;
	const sessionChanged = idleLiveActiveSessionId && idleLiveActiveSessionId !== sid;
	idleLiveSnapshotKey = key;
	if (sessionChanged) {
		idlePendingMessages.clear();
		resetTTSSpeechBubble(idleTTSSpeech);
		if (typeof target.replaceChildren === 'function') target.replaceChildren();
		else {
			target.innerHTML = '';
			if (Array.isArray(target.children)) target.children.length = 0;
		}
	}
	idleLiveActiveSessionId = sid;
	rows.forEach((row) => {
		if (!row || (row.type !== 'idlechat.message' && row.type !== 'idlechat.topic')) return;
		queueIdleMessageForTTS(row);
	});
	}

function idleTranscriptSnapshotKey(sessionId, rows) {
  const sid = String(sessionId || '').trim();
  const parts = (Array.isArray(rows) ? rows : []).map((row, index) => {
    const r = row || {};
    return [
      index,
      r.type || '',
      r.session_id || r.chat_id || '',
      r.message_id || r.messageId || '',
      r.response_id || r.responseId || '',
      r.utterance_id || r.utteranceId || '',
      r.turn_index ?? r.turnIndex ?? '',
      r.timestamp || '',
      r.content || '',
    ].map((v) => encodeURIComponent(String(v ?? ''))).join(',');
  }).join(';');
  return sid + ':' + String(parts.length) + ':' + parts;
}

function appendIdleLiveMessageEvent(ev, options = {}) {
  const target = idleLiveRenderTarget();
  if (!target || !ev || (ev.type !== 'idlechat.message' && ev.type !== 'idlechat.topic')) return null;
  if (idleLiveIdentityConflict(ev)) return null;
  removeIdleLiveEmpty();
  const existing = findIdleLiveMessageNode(ev);
  if (existing) {
    const pending = Boolean(options && options.pending);
    if (!pending) {
      const mc = existing.querySelector && existing.querySelector('.mc');
      const displayContent = normalizeViewerDisplayText(ev.content);
      if (mc && displayContent) {
        mc.innerHTML = fmt(displayContent) + idleRawResponseBlock(ev, displayContent);
        mc.dataset.raw = ev.content || '';
      }
      existing.classList.remove('idle-pending-tts');
    }
    sortIdleLiveMessageNodes(target);
    return existing;
  }

  const f = ag(ev.from);
  const t = ev.to ? ag(ev.to) : null;
  const dir = t && ev.to ? '<span class="dir">→ ' + t.e + ' ' + t.l + '</span>' : '';
  const displayContent = normalizeViewerDisplayText(ev.content);
  const pending = Boolean(options && options.pending);
  const rawBlock = pending ? '' : idleRawResponseBlock(ev, displayContent);
  const kind = isIdleTopicEvent(ev) ? 'topic' : 'speech';
  const el = document.createElement('div');
  el.className = 'msg idle-live-item idle-kind-' + kind + (pending ? ' idle-pending-tts' : '');
  const turnIndex = idleTurnIndex(ev);
  if (turnIndex >= 0) el.dataset.turnIndex = String(turnIndex);
  const messageID = String(ev.message_id || '').trim();
  if (messageID) el.dataset.messageId = messageID;
  el.innerHTML =
    '<div class="av" style="background:' + f.c + '18;color:' + f.c + '">' + f.e + '</div>' +
    '<div class="mb"><div class="mh">' +
      '<span class="idle-kind">' + (kind === 'topic' ? 'Topic' : 'Speech') + '</span>' +
      '<span class="an" style="color:' + f.c + '">' + f.l + '</span>' + dir +
      '<span class="tm">' + ftime(ev.timestamp) + '</span>' +
    '</div><button class="cp" onclick="copyMsg(this)">Copy</button>' +
    '<div class="mc">' + (pending ? '' : fmt(displayContent) + rawBlock) + '</div></div>';
  el.querySelector('.mc').dataset.raw = ev.content || '';
	  target.appendChild(el);
	  sortIdleLiveMessageNodes(target);
	  validateIdleLiveNodeSequence(target, String(ev.session_id || ev.chat_id || '').trim());
	  recordIdleLiveRendered(kind, ev, pending ? '' : displayContent);
  trimTimelineNodesFor(target, MAX_TIMELINE_NODES);
  target.scrollTop = target.scrollHeight;
  // Update Live2D emotion
  if (typeof updateLive2DOnMessage === 'function') updateLive2DOnMessage(ev);
  return el;
}

function idleTurnIndex(ev) {
  const raw = ev && (ev.turn_index ?? ev.turnIndex);
  const n = Number(raw);
  return Number.isFinite(n) ? Math.floor(n) : -1;
}

function findIdleLiveMessageNode(ev) {
  const target = idleLiveRenderTarget();
  if (!target || !ev) return null;
  const messageID = String(ev.message_id || ev.messageId || '').trim();
  const turnIndex = idleTurnIndex(ev);
  const nodes = Array.from(target.children || []);
	  if (messageID) {
	    const byMessage = nodes.find((node) => node && node.dataset && node.dataset.messageId === messageID);
	    if (byMessage) return byMessage;
	    return null;
	  }
  if (turnIndex >= 0) {
    const sid = String(ev.session_id || ev.chat_id || '').trim();
    return nodes.find((node) => {
      if (!node || !node.dataset || String(node.dataset.turnIndex || '') !== String(turnIndex)) return false;
      if (!sid) return true;
      const nodeMessage = String(node.dataset.messageId || '');
      return !nodeMessage || nodeMessage.indexOf(sid + ':') === 0;
    }) || null;
  }
	  return null;
	}

function idleLiveIdentityConflict(ev) {
  const target = idleLiveRenderTarget();
  if (!target || !ev) return false;
  const messageID = String(ev.message_id || ev.messageId || '').trim();
  const turnIndex = idleTurnIndex(ev);
  const sid = String(ev.session_id || ev.chat_id || '').trim();
  const nodes = Array.from(target.children || []);
  if (messageID) {
    const byMessage = nodes.find((node) => node && node.dataset && node.dataset.messageId === messageID);
    if (byMessage) {
      const existingTurn = Number(byMessage.dataset && byMessage.dataset.turnIndex);
      if (turnIndex >= 0 && Number.isFinite(existingTurn) && existingTurn !== turnIndex) {
        recordIdleLiveIdentityError('same_message_different_turn', ev, 'existing=' + String(existingTurn) + ' incoming=' + String(turnIndex));
        return true;
      }
    }
  }
  if (turnIndex >= 1) {
    const byTurn = nodes.find((node) => {
      if (!node || !node.dataset || String(node.dataset.turnIndex || '') !== String(turnIndex)) return false;
      const nodeMessage = String(node.dataset.messageId || '');
      if (sid && nodeMessage && nodeMessage.indexOf(sid + ':') !== 0) return false;
      return true;
    });
    if (byTurn) {
      const existingMessage = String(byTurn.dataset && byTurn.dataset.messageId || '').trim();
      if (messageID && existingMessage && existingMessage !== messageID) {
        recordIdleLiveIdentityError('same_turn_different_message', ev, 'existing=' + existingMessage + ' incoming=' + messageID);
        return true;
      }
    }
  }
  return false;
}

function validateIdleLiveNodeSequence(target, sessionId) {
  if (!target || !target.children) return true;
  const sid = String(sessionId || '').trim();
  const seenMessages = new Set();
  const seenTurns = new Set();
  let ok = true;
  Array.from(target.children || []).forEach((node) => {
    if (!node || !node.dataset) return;
    const messageID = String(node.dataset.messageId || '').trim();
    const turnIndex = Number(node.dataset.turnIndex);
    if (messageID) {
      if (seenMessages.has(messageID)) {
        recordIdleLiveIdentityError('duplicate_message_dom', {session_id: sid, message_id: messageID, turn_index: turnIndex}, '');
        ok = false;
      }
      seenMessages.add(messageID);
    }
    if (Number.isFinite(turnIndex) && turnIndex >= 1) {
      if (seenTurns.has(turnIndex)) {
        recordIdleLiveIdentityError('duplicate_turn_dom', {session_id: sid, message_id: messageID, turn_index: turnIndex}, '');
        ok = false;
      }
      seenTurns.add(turnIndex);
    }
  });
  return ok;
}

function sortIdleLiveMessageNodes(target) {
  if (!target || !target.children || target.children.length < 2) return;
  const nodes = Array.from(target.children || []);
  const sortable = nodes.filter((node) => node && node.dataset && Number.isFinite(Number(node.dataset.turnIndex)));
  if (sortable.length < 2) return;
  const sorted = nodes.slice().sort((a, b) => {
    const at = Number(a.dataset && a.dataset.turnIndex);
    const bt = Number(b.dataset && b.dataset.turnIndex);
    if (!Number.isFinite(at) && !Number.isFinite(bt)) return 0;
    if (!Number.isFinite(at)) return 1;
    if (!Number.isFinite(bt)) return -1;
    return at - bt;
  });
  if (sorted.every((node, idx) => node === nodes[idx])) return;
  if (typeof target.replaceChildren === 'function') target.replaceChildren(...sorted);
  else if (Array.isArray(target.children)) target.children = sorted;
}

function idlePendingTTSErrorHTML(ev, errorCode, reason) {
  const meta = [
    ['error_code', errorCode || 'TTS_CHUNK_TIMEOUT'],
    ['session_id', ev && (ev.session_id || ev.chat_id) || ''],
    ['response_id', ev && (ev.response_id || ev.responseId) || ''],
    ['utterance_id', ev && (ev.utterance_id || ev.utteranceId) || ''],
    ['message_id', ev && (ev.message_id || ev.messageId) || ''],
    ['turn_index', ev && (ev.turn_index ?? ev.turnIndex ?? '')],
    ['from', ev && ev.from || ''],
    ['to', ev && ev.to || ''],
    ['elapsed_ms', String(IDLE_MESSAGE_FALLBACK_MS)],
  ];
  return '<div class="idle-tts-error-box">' +
    '<div><span class="badge state-error">' + idleEsc(errorCode || 'TTS_CHUNK_TIMEOUT') + '</span></div>' +
    '<div class="idle-tts-error-reason">' + idleEsc(reason || 'TTS chunk timeout') + '</div>' +
    '<div class="idle-tts-error-meta">' +
      meta.map(([k, v]) => '<span><b>' + idleEsc(k) + '</b>=' + idleEsc(String(v || '-')) + '</span>').join('') +
    '</div>' +
  '</div>';
}

function renderIdlePendingTTSError(item, errorCode, reason) {
  const ev = item && item.ev;
  let el = item && item.el;
  if (!ev) return;
  if (!el) {
    el = appendIdleLiveMessageEvent(ev, {pending: true});
    if (item) item.el = el;
  }
  if (!el) return;
  const mc = el.querySelector && el.querySelector('.mc');
  if (!mc) return;
  mc.innerHTML = idlePendingTTSErrorHTML(ev, errorCode, reason);
  mc.dataset.raw = '';
  el.classList.remove('idle-pending-tts');
  el.classList.add('idle-tts-error');
  el.classList.add('idle-display-error');
  recordIdleLiveRendered(isIdleTopicEvent(ev) ? 'topic_tts_error' : 'speech_tts_error', ev, JSON.stringify({
    error_code: errorCode || 'TTS_CHUNK_TIMEOUT',
    reason: reason || 'TTS chunk timeout',
    session_id: ev.session_id || ev.chat_id || '',
    response_id: ev.response_id || ev.responseId || '',
    utterance_id: ev.utterance_id || ev.utteranceId || '',
    message_id: ev.message_id || ev.messageId || '',
    turn_index: ev.turn_index ?? ev.turnIndex ?? '',
    from: ev.from || '',
    to: ev.to || '',
    elapsed_ms: IDLE_MESSAGE_FALLBACK_MS,
  }));
}

function renderIdleTTSChunkError(chunk, errorCode, reason) {
  const sid = String((chunk && chunk.sessionId) || '').trim();
  const messageId = String((chunk && chunk.messageId) || '').trim();
  const turnIndex = Number.isFinite(chunk && chunk.turnIndex) ? Math.floor(chunk.turnIndex) : -1;
  const ev = {
    type: 'idlechat.message',
    from: String((chunk && chunk.characterId) || '').trim().toLowerCase(),
    to: '',
    content: '',
    session_id: sid,
    response_id: String((chunk && chunk.responseId) || '').trim(),
    utterance_id: String((chunk && chunk.utteranceId) || '').trim(),
    message_id: messageId,
    turn_index: turnIndex >= 0 ? turnIndex : '',
    timestamp: new Date().toISOString(),
  };
  const el = appendIdleLiveMessageEvent(ev, {pending: true});
  if (!el) return;
  const item = {ev, el};
  renderIdlePendingTTSError(item, errorCode || 'TTS_IDENTITY_MISSING', reason || 'TTS chunk did not include a stable message identity.');
  recordIdleLiveRendered((errorCode || '') === 'TTS_IDENTITY_MISSING' ? 'tts_identity_error' : 'tts_playback_error', ev, JSON.stringify({
    error_code: errorCode || 'TTS_IDENTITY_MISSING',
    reason: reason || 'TTS chunk did not include a stable message identity.',
    session_id: sid,
    response_id: ev.response_id,
    utterance_id: ev.utterance_id,
    message_id: messageId,
    turn_index: ev.turn_index,
  }));
}

function renderIdlePendingMessageFromEvent(item) {
	const ev = item && item.ev;
	let el = item && item.el;
	if (!ev) return false;
	if (!el) {
		el = appendIdleLiveMessageEvent(ev, {pending: false});
		if (item) item.el = el;
	}
	if (!el) return false;
	const mc = el.querySelector && el.querySelector('.mc');
	if (!mc) return false;
	const displayContent = normalizeViewerDisplayText(ev.content);
	if (!displayContent) return false;
	mc.innerHTML = fmt(displayContent) + idleRawResponseBlock(ev, displayContent);
	mc.dataset.raw = ev.content || '';
	el.classList.remove('idle-pending-tts');
	recordIdleLiveRendered(isIdleTopicEvent(ev) ? 'topic_tts' : 'speech_tts', ev, displayContent);
	return true;
}

function idleRawResponseBlock(ev, displayContent) {
  if (!ev || isIdleTopicEvent(ev)) return '';
  if (!shouldShowIdleRawResponse()) return '';
  const raw = String(ev.raw_content || ev.rawContent || '').trim();
  if (!raw) return '';
  return '<div class="idle-raw-response">' +
    '<div class="idle-raw-label">編集前（テストモード）</div>' +
    '<div class="idle-raw-text">' + fmt(raw) + '</div>' +
  '</div>';
}

function shouldShowIdleRawResponse() {
  try {
    if (typeof window !== 'undefined' && window.__REN_CROW_SHOW_IDLE_RAW_RESPONSE) return true;
    if (typeof window !== 'undefined' && window.location && window.location.search) {
      return /(?:^|[?&])idle_raw=1(?:&|$)/.test(String(window.location.search || ''));
    }
  } catch (_) {}
  return false;
}

function addIdleSummaryToTimeline(ev) {
  if (!idleLiveLog || !ev || ev.type !== 'idlechat.summary') return;
  removeIdleLiveEmpty();

  const f = ag(ev.from || 'shiro');
  const displayContent = normalizeViewerDisplayText(ev.content);
  const el = document.createElement('div');
  el.className = 'msg idle-live-item idle-kind-summary';
  el.innerHTML =
    '<div class="av" style="background:' + f.c + '18;color:' + f.c + '">' + f.e + '</div>' +
    '<div class="mb"><div class="mh">' +
      '<span class="idle-kind">Summary</span>' +
      '<span class="an" style="color:' + f.c + '">' + f.l + '</span>' +
      '<span class="tm">' + ftime(ev.timestamp) + '</span>' +
    '</div><button class="cp" onclick="copyMsg(this)">Copy</button>' +
    '<div class="mc">' + fmt(displayContent) + '</div></div>';
  el.querySelector('.mc').dataset.raw = ev.content || '';
  idleLiveLog.appendChild(el);
  recordIdleLiveRendered('summary', ev, displayContent);
  trimTimelineNodesFor(idleLiveLog, MAX_TIMELINE_NODES);
  idleLiveLog.scrollTop = idleLiveLog.scrollHeight;
}

function isIdleTopicEvent(ev) {
  if (String((ev && ev.type) || '').trim() === 'idlechat.topic') return true;
  const content = String((ev && ev.content) || '').trim();
  return String((ev && ev.from) || '').toLowerCase() === 'user' &&
    String((ev && ev.to) || '').toLowerCase() === 'mio' &&
    (/^今日のお題/.test(content) || /^お題は[、,:：]/.test(content));
}

function setIdleState(mode, manual, active) {
  const currentMode = String(mode || '');
  if (currentMode === 'forecast') {
    idleStateEl.textContent = 'Forecast: ' + (active ? 'talking' : 'ready');
    idleStateEl.className = active ? 'idle-on' : 'idle-off';
    return;
  }
  if (currentMode === 'story') {
    idleStateEl.textContent = 'Story: ' + (active ? 'talking' : 'ready');
    idleStateEl.className = active ? 'idle-on' : 'idle-off';
    return;
  }
  if (currentMode === 'story-simple') {
    idleStateEl.textContent = 'Story(簡易): ' + (active ? 'talking' : 'ready');
    idleStateEl.className = active ? 'idle-on' : 'idle-off';
    return;
  }
  if (active) {
    idleStateEl.textContent = 'IdleChat: talking';
    idleStateEl.className = 'idle-on';
    return;
  }
  if (manual) {
    idleStateEl.textContent = 'IdleChat: on';
    idleStateEl.className = 'idle-on';
    return;
  }
  idleStateEl.textContent = 'IdleChat: off';
  idleStateEl.className = 'idle-off';
}

function setIdleSelectedMode(mode) {
  const next = mode === 'forecast'
    ? 'forecast'
    : (mode === 'story-simple' ? 'story-simple' : 'manual');
  state.idleChat.selectedMode = next;
  localStorage.setItem('idlechat.selectedMode', next);
  if (idleModeNormalBtn) idleModeNormalBtn.classList.toggle('is-selected', next === 'manual');
  if (idleModeForecastBtn) idleModeForecastBtn.classList.toggle('is-selected', next === 'forecast');
  if (idleModeStorySimpleBtn) idleModeStorySimpleBtn.classList.toggle('is-selected', next === 'story-simple');
}

function setBadge(el, enabled) {
  el.textContent = enabled ? 'on' : 'off';
  el.className = 'badge ' + (enabled ? 'state-running' : 'state-offline');
}

function stripIdleTopicCategory(text) {
  return String(text || '').replace(/^今日のお題(?:（[^）]+）)*[:：]\s*/, '今日のお題：').trim();
}

function normalizeViewerDisplayText(text) {
  return stripIdleTopicCategory(text);
}

function setIdleSelectedView(view) {
  const next = (view === 'summary' || view === 'history') ? view : 'live';
  state.idleChat.selectedView = next;
  localStorage.setItem('idlechat.selectedView', next);
  idleSubtabs.forEach((btn) => {
    const active = btn.dataset.idleView === next;
    btn.classList.toggle('active', active);
    btn.setAttribute('aria-selected', active ? 'true' : 'false');
    btn.tabIndex = active ? 0 : -1;
  });
  idleSubviews.forEach((viewEl) => {
    const expectedID = 'idleView' + next.charAt(0).toUpperCase() + next.slice(1);
    viewEl.classList.toggle('active', viewEl.id === expectedID);
  });
}

function renderIdleChat() {
  const manualEl = document.getElementById('idleManual');
  const activeEl = document.getElementById('idleActive');
  const topicEl = document.getElementById('idleTopicNow');
  const body = document.getElementById('idlechatBody');
  if (!manualEl || !activeEl || !topicEl || !body) return;

  setBadge(manualEl, state.idleChat.manualMode);
  setBadge(activeEl, state.idleChat.chatActive);
  topicEl.textContent = stripIdleTopicCategory(state.idleChat.currentTopic) || '-';

  body.innerHTML = '';
  const rows = state.idleChat.history || [];
  renderIdleSummaryReview(rows);
  const idleErrors = [
    state.idleChat.statusError,
    state.idleChat.logsError,
    state.idleChat.controlError,
  ].map((err) => String(err || '').trim()).filter(Boolean);
  idleErrors.forEach((err) => {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="7" class="small">' + esc(err) + '</td>';
    body.appendChild(tr);
  });
  if (rows.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="7" class="small">No idleChat summaries yet</td>';
    body.appendChild(tr);
    return;
  }

  if (state.idleChat.openIndex >= rows.length) state.idleChat.openIndex = -1;

  rows.forEach((r, rowIndex) => {
    const tr = document.createElement('tr');
    const isOpen = state.idleChat.openIndex === rowIndex;
    const strategy = r.strategy || r.category || '-';
    const isForecast = strategy === 'forecast';
    if (isForecast) tr.style.borderLeft = '3px solid rgba(59,130,246,.5)';
    tr.innerHTML =
      '<td><button class="ctl-btn idle-open idle-title-btn" data-idx="' + esc(String(rowIndex)) + '"><span class="idle-arrow ' + (isOpen ? 'open' : '') + '">&#9654;</span><span>' + esc(stripIdleTopicCategory(r.title || '-') || '-') + '</span></button></td>' +
      '<td>' + esc(stripIdleTopicCategory(r.topic || '-') || '-') + '</td>' +
      '<td>' + esc(String(r.turns || 0)) + '</td>' +
      '<td>' + esc(r.loop_restarted ? 'yes' : 'no') + '</td>' +
      '<td>' + esc(fdt(r.started_at)) + '</td>' +
      '<td>' + esc(fdt(r.ended_at)) + '</td>' +
      '<td>' + esc(short(r.summary || '-', 200)) + '</td>';
    body.appendChild(tr);

    if (isOpen) {
      const exp = document.createElement('tr');
      exp.className = 'idle-expand';
      const transcript = Array.isArray(r.transcript) ? r.transcript : [];
      if (transcript.length === 0) {
        exp.innerHTML = '<td colspan="7"><div class="idle-actions"><button class="ctl-btn idle-copy-chat" data-idx="' + esc(String(rowIndex)) + '">Copy Chat</button></div><div class="idle-empty">Transcript not available</div></td>';
      } else {
        const items = transcript.map((line) => {
          const idx = String(line || '').indexOf(':');
          let speaker = 'agent';
          let content = String(line || '');
          if (idx > 0) {
            speaker = content.slice(0, idx).trim();
            content = content.slice(idx + 1).trim();
          }
          const info = ag(speaker);
          return '<div class="idle-bubble"><div class="idle-meta" style="color:' + info.c + '">' + info.e + ' ' + info.l + '</div><div>' + esc(content || '-') + '</div></div>';
        }).join('');
        exp.innerHTML = '<td colspan="7"><div class="idle-actions"><button class="ctl-btn idle-copy-chat" data-idx="' + esc(String(rowIndex)) + '">Copy Chat</button></div><div class="idle-transcript">' + items + '</div></td>';
      }
      body.appendChild(exp);
    }
  });

  body.querySelectorAll('.idle-open').forEach((btn) => {
    btn.addEventListener('click', () => {
      const next = Number(btn.dataset.idx || '-1');
      state.idleChat.openIndex = (state.idleChat.openIndex === next) ? -1 : next;
      renderIdleChat();
    });
  });
  body.querySelectorAll('.idle-copy-chat').forEach((btn) => {
    btn.addEventListener('click', () => {
      const idx = Number(btn.dataset.idx || '-1');
      const row = rows[idx];
      if (!row) {
        showToast('Copy failed', 'error');
        return;
      }
      copyTextPayload(btn, formatIdleChatTranscript(row));
    });
  });
}

function renderIdleSummaryReview(rows) {
  const listEl = document.getElementById('idleSummaryList');
  const detailEl = document.getElementById('idleSummaryDetail');
  if (!listEl || !detailEl) return;

  listEl.innerHTML = '';
  detailEl.innerHTML = '';
  if (!Array.isArray(rows) || rows.length === 0) {
    state.idleChat.selectedSummaryIndex = 0;
    listEl.innerHTML = '<div class="idle-empty">No summaries yet</div>';
    detailEl.innerHTML = '<div class="idle-empty">Select a summary</div>';
    return;
  }

  if (state.idleChat.selectedSummaryIndex < 0 || state.idleChat.selectedSummaryIndex >= rows.length) {
    state.idleChat.selectedSummaryIndex = 0;
  }
  const selectedIndex = state.idleChat.selectedSummaryIndex;
  rows.forEach((r, idx) => {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'idle-review-item' + (idx === selectedIndex ? ' active' : '');
    btn.dataset.idx = String(idx);
    btn.innerHTML =
      '<div class="idle-review-title">' + esc(stripIdleTopicCategory(r.title || r.topic || '-') || '-') + '</div>' +
      '<div class="idle-review-meta">' + esc(fdt(r.ended_at || r.started_at)) + ' · turns ' + esc(String(r.turns || 0)) + '</div>';
    btn.addEventListener('click', () => {
      state.idleChat.selectedSummaryIndex = idx;
      renderIdleSummaryReview(rows);
    });
    listEl.appendChild(btn);
  });

  const row = rows[selectedIndex] || {};
  const sections = [
    {title: 'Summary', text: row.summary || '-'},
    {title: 'Quality Review', text: row.quality_review || '-'},
    {title: 'Prompt Guidance', text: row.prompt_guidance || '-'},
    {title: 'Transcript', text: formatIdleTranscriptOnly(row)},
  ];
  detailEl.innerHTML =
    '<div class="idle-actions" style="padding:0;justify-content:flex-start">' +
      '<button class="ctl-btn" id="idleSummaryCopy">Copy Review</button>' +
    '</div>' +
    '<h4>' + esc(stripIdleTopicCategory(row.title || '-') || '-') + '</h4>' +
    '<div class="idle-review-kv">' +
      '<span>Topic: ' + esc(stripIdleTopicCategory(row.topic || '-') || '-') + '</span>' +
      '<span>Strategy: ' + esc(String(row.strategy || row.category || '-')) + '</span>' +
      '<span>Turns: ' + esc(String(row.turns || 0)) + '</span>' +
      '<span>Ended: ' + esc(fdt(row.ended_at)) + '</span>' +
      (row.loop_restarted ? '<span>Loop Restart: yes' + (row.loop_reason ? ' / ' + esc(row.loop_reason) : '') + '</span>' : '') +
    '</div>' +
    sections.map((s) => (
      '<div class="idle-review-section">' +
        '<h5>' + esc(s.title) + '</h5>' +
        '<div class="idle-review-text">' + fmt(s.text || '-') + '</div>' +
      '</div>'
    )).join('');

  const copyBtn = document.getElementById('idleSummaryCopy');
  if (copyBtn) {
    copyBtn.addEventListener('click', () => copyTextPayload(copyBtn, formatIdleChatTranscript(row)));
  }
}

function formatIdleTranscriptOnly(row) {
  const transcript = Array.isArray(row && row.transcript) ? row.transcript : [];
  if (transcript.length === 0) return '(not available)';
  return transcript.map((line) => String(line || '')).join('\n');
}

function formatIdleChatTranscript(row) {
  const lines = [];
  lines.push('Title: ' + String(row && row.title || '-'));
  lines.push('Topic: ' + String(row && row.topic || '-'));
  lines.push('Strategy: ' + String(row && (row.strategy || row.category) || '-'));
  lines.push('Turns: ' + String(row && row.turns || 0));
  lines.push('Started: ' + String(row && row.started_at || ''));
  lines.push('Ended: ' + String(row && row.ended_at || ''));
  lines.push('');
  lines.push('Summary:');
  lines.push(String(row && row.summary || '-'));
  if (row && row.quality_review) {
    lines.push('');
    lines.push('Quality Review:');
    lines.push(String(row.quality_review || '-'));
  }
  if (row && row.prompt_guidance) {
    lines.push('');
    lines.push('Prompt Guidance:');
    lines.push(String(row.prompt_guidance || '-'));
  }
  lines.push('');
  lines.push('Transcript:');
  const transcript = Array.isArray(row && row.transcript) ? row.transcript : [];
  if (transcript.length === 0) {
    lines.push('(not available)');
  } else {
    transcript.forEach((line) => lines.push(String(line || '')));
  }
  return lines.join('\n');
}

async function refreshIdleStatus() {
  try {
    const r = await fetch('/viewer/idlechat/status');
    if (!r.ok) {
      const text = await r.text();
      idleStartBtn.disabled = true;
      if (idleModeNormalBtn) idleModeNormalBtn.disabled = true;
      if (idleModeForecastBtn) idleModeForecastBtn.disabled = true;
      if (idleModeStorySimpleBtn) idleModeStorySimpleBtn.disabled = true;
      idleStopBtn.disabled = true;
      setIdleState('', false, false);
      state.idleChat.mode = '';
      state.idleChat.manualMode = false;
      state.idleChat.chatActive = false;
      state.idleChat.currentTopic = '';
      state.idleChat.history = [];
      state.idleChat.statusError = 'IdleChat status unavailable: HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'idlechat status unavailable');
      renderIdleChat();
      return;
    }
    const d = await r.json();
    state.idleChat.statusError = '';
    setIdleState(d.mode || '', !!d.manual_mode, !!d.chat_active);
    idleStartBtn.disabled = !!d.manual_mode || !!d.chat_active;
    if (idleModeNormalBtn) idleModeNormalBtn.disabled = !!d.chat_active;
    if (idleModeForecastBtn) idleModeForecastBtn.disabled = !!d.chat_active;
    if (idleModeStorySimpleBtn) idleModeStorySimpleBtn.disabled = !!d.chat_active;
    idleStopBtn.disabled = !d.manual_mode && !d.chat_active;
    state.idleChat.mode = d.mode || '';
    state.idleChat.manualMode = !!d.manual_mode;
    state.idleChat.chatActive = !!d.chat_active;
    if (state.idleChat.chatActive) {
      state.idleChat.interrupted = false;
      state.idleChat.interruptedSessionId = '';
    }
    state.idleChat.currentTopic = d.current_topic || '';
    hydrateIdleLiveTranscript(d.active_session_id || '', d.active_transcript || []);
    const applyLabStatus = typeof window !== 'undefined' && typeof window.applyLabConversationStatus === 'function'
      ? window.applyLabConversationStatus
      : (typeof applyLabConversationStatus === 'function' ? applyLabConversationStatus : null);
    if (applyLabStatus) applyLabStatus(d);
    renderIdleChat();
  } catch (_) {
    idleStartBtn.disabled = true;
    if (idleModeNormalBtn) idleModeNormalBtn.disabled = true;
    if (idleModeForecastBtn) idleModeForecastBtn.disabled = true;
    if (idleModeStorySimpleBtn) idleModeStorySimpleBtn.disabled = true;
    idleStopBtn.disabled = true;
    setIdleState('', false, false);
    state.idleChat.mode = '';
    state.idleChat.manualMode = false;
    state.idleChat.chatActive = false;
    state.idleChat.currentTopic = '';
    state.idleChat.history = [];
    state.idleChat.statusError = 'IdleChat status unavailable: ' + String(_ && _.message ? _.message : _);
    renderIdleChat();
  }
}

async function refreshIdleLogs() {
  try {
    const r = await fetch('/viewer/idlechat/logs?limit=20');
    if (!r.ok) {
      const text = await r.text();
      state.idleChat.history = [];
      state.idleChat.logsError = 'IdleChat logs unavailable: HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'idlechat logs unavailable');
      renderIdleChat();
      return;
    }
    const d = await r.json();
    state.idleChat.logsError = '';
    state.idleChat.mode = d.mode || '';
    state.idleChat.manualMode = !!d.manual_mode;
    state.idleChat.chatActive = !!d.chat_active;
    if (state.idleChat.chatActive) {
      state.idleChat.interrupted = false;
      state.idleChat.interruptedSessionId = '';
    }
    state.idleChat.currentTopic = d.current_topic || '';
    state.idleChat.history = Array.isArray(d.history) ? d.history : [];
    hydrateIdleLiveTranscript(d.active_session_id || '', d.active_transcript || []);
    const applyLabStatus = typeof window !== 'undefined' && typeof window.applyLabConversationStatus === 'function'
      ? window.applyLabConversationStatus
      : (typeof applyLabConversationStatus === 'function' ? applyLabConversationStatus : null);
    if (applyLabStatus) applyLabStatus(d);
    renderIdleChat();
  } catch (err) {
    state.idleChat.history = [];
    state.idleChat.logsError = 'IdleChat logs unavailable: ' + String(err && err.message ? err.message : err);
    renderIdleChat();
  }
}

async function controlIdle(path) {
  const btns = [idleStartBtn, idleModeNormalBtn, idleModeForecastBtn, idleModeStorySimpleBtn, idleStopBtn].filter(Boolean);
  btns.forEach((b) => { b.disabled = true; });
  try {
    const r = await fetch(path, {method: 'POST'});
    if (!r.ok) {
      const text = await r.text();
      throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'idlechat control failed'));
    }
    state.idleChat.controlError = '';
  } catch (err) {
    state.idleChat.controlError = 'IdleChat control unavailable: ' + String(err && err.message ? err.message : err);
    console.error(err);
  } finally {
    await refreshIdleStatus();
  }
}
