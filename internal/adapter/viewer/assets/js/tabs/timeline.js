// Chat Timeline tab module: normal chat message rendering.
function isExplicitRouteMessage(message) {
  return /^\/(ops|wild|heavy|code|code1|code2|code3|code4|plan|analyze|research|chat)(\s|$)/.test(String(message || '').trim());
}

function buildViewerSendRequest(message) {
  const trimmed = String(message || '').trim();
  const audioOutput = typeof ttsPlayback !== 'undefined' && ttsPlayback && ttsPlayback.audioEnabled ? 'requested' : 'disabled';
  const request = {message: trimmed, audio_output: audioOutput};
  const viewerClientID = typeof viewerControl !== 'undefined' && viewerControl ? String(viewerControl.clientId || '').trim() : '';
  if (viewerClientID) request.viewer_client_id = viewerClientID;
  if (!trimmed) return request;
  const recipient = typeof selectedViewerChatRecipient === 'function' ? selectedViewerChatRecipient() : 'mio';
  if (/^\/chat(\s|$)/.test(trimmed) && recipient) {
    request.to = recipient;
    return request;
  }
  if (isExplicitRouteMessage(trimmed)) return request;

  if (recipient) {
    request.to = recipient;
    return request;
  }
  request.message = applyRoleTargetToMessage(trimmed);
  return request;
}

const voiceDirectTimelineJobIDs = new Set();

function rememberVoiceDirectTimelineJob(ev) {
  const jobID = String(ev && ev.job_id || '').trim();
  if (!jobID) return;
  const content = String(ev && ev.content || '');
  if (!content.includes('voice_direct')) return;
  voiceDirectTimelineJobIDs.add(jobID);
  if (voiceDirectTimelineJobIDs.size > 80) {
    const first = voiceDirectTimelineJobIDs.values().next().value;
    if (first) voiceDirectTimelineJobIDs.delete(first);
  }
}

function isVoiceDirectTimelineResponse(ev) {
  const jobID = String(ev && ev.job_id || '').trim();
  return !!(jobID && voiceDirectTimelineJobIDs.has(jobID));
}

function renderTrustedGeneratedImages(message) {
  const source = String(message || '');
  const imagePattern = /!\[generated image\]\(\/viewer\/image\/result\?id=(img_[0-9A-Za-z_-]+)\)/g;
  let output = '';
  let cursor = 0;
  let match;
  while ((match = imagePattern.exec(source)) !== null) {
    output += fmt(source.slice(cursor, match.index));
    const imagePath = '/viewer/image/result?id=' + encodeURIComponent(match[1]);
    output += '<figure class="generated-chat-image">' +
      '<a href="' + imagePath + '" target="_blank" rel="noopener noreferrer">' +
      '<img src="' + imagePath + '" alt="MidoriがRenCrow_Imageで生成した画像" loading="lazy">' +
      '</a></figure>';
    cursor = match.index + match[0].length;
  }
  output += fmt(source.slice(cursor));
  return output;
}

function addMsgToTimeline(ev) {
  if (ev.type === 'job.notification') { addJobNotificationToTimeline(ev); return; }
  if (ev.type === 'task.notification') { addTaskNotificationToTimeline(ev); return; }
  if (ev.type === 'agent.response') removeThinking(ev.job_id);
  if (ev.type === 'agent.thinking') { addThinking(ev); return; }
  if (ev.type === 'agent.start') { addThinkingStart(ev); return; }
  if (isCoordinationTraceEvent(ev)) { addCoordinationTraceToTimeline(ev); return; }
  if (ev.type === 'routing.decision') rememberVoiceDirectTimelineJob(ev);

  if (!matchesFilters(ev)) return;
  if (ev.type === 'idlechat.summary') return;
  if (ev.type === 'idlechat.message') return;
  const isUserFacingAgentResponse = ev.type === 'agent.response' && (ev.to || '').toLowerCase() === 'user';
  if (ev.type !== 'message.received' && ev.type !== 'idlechat.message' && (ev.from || '').toLowerCase() !== 'mio' && !isUserFacingAgentResponse) return;

  const em = document.getElementById('empty');
  if (em) em.remove();

  if (ev.type === 'routing.decision') return;
  if (ev.type === 'agent.response' && (ev.to || '').toLowerCase() !== 'user') return;
  if (ev.type === 'agent.response' && isTTSSyncedSpeaker(ev.from) && !isViewerLocalFailureMessage(ev) && !isVoiceDirectTimelineResponse(ev)) return;
  if (ev.type === 'idlechat.message' && isTTSSyncedSpeaker(ev.from)) return;
  if (ev.type === 'agent.note' && (ev.to || '').toLowerCase() !== 'user') return;
  if (ev.type === 'message.received' && (ev.from || '').toLowerCase() !== 'user') return;
  if (ev.type === 'message.received' && String(ev.content || '').trim().startsWith('[voice_direct]')) return;

  const f = ag(ev.from);
  const t = ev.to ? ag(ev.to) : null;
  const dir = t && ev.to ? '<span class="dir">→ ' + t.e + ' ' + t.l + '</span>' : '';
  const displayContent = normalizeViewerDisplayText(ev.content);
  const from = String(ev.from || '').toLowerCase();
  const roleClass = from === 'user' ? ' user' : ' assistant';
  const el = document.createElement('div');
  el.className = 'msg' + roleClass;
  el.innerHTML =
    '<div class="av" style="background:' + f.c + '18;color:' + f.c + '">' + f.e + '</div>' +
    '<div class="mb"><div class="mh">' +
      '<span class="an" style="color:' + f.c + '">' + f.l + '</span>' + dir +
      '<span class="tm">' + ftime(ev.timestamp) + '</span>' +
    '</div><button class="cp" onclick="copyMsg(this)">Copy</button>' +
    '<div class="mc">' + renderTrustedGeneratedImages(displayContent) + '</div></div>';
  el.querySelector('.mc').dataset.raw = ev.content || '';
  chat.appendChild(el);
  trimTimelineNodes();
  bump();
}

function isCoordinationTraceEvent(ev) {
  const type = String(ev && ev.type ? ev.type : '');
  return type === 'agent.delegate' || type === 'agent.acknowledge' || type === 'agent.report' || type === 'worker.request' || type === 'worker.result';
}

function addCoordinationTraceToTimeline(ev) {
  if (!matchesCoordinationTraceFilters(ev)) return;
  const em = document.getElementById('empty');
  if (em) em.remove();
  const f = ag(ev.from);
  const t = ev.to ? ag(ev.to) : null;
  const dir = t && ev.to ? '<span class="dir">→ ' + t.e + ' ' + t.l + '</span>' : '';
  const meta = [ev.type || '', ev.route || '', ev.job_id || ''].filter(Boolean).join(' / ');
  const el = document.createElement('div');
  el.className = 'msg assistant coordination-trace';
  el.innerHTML =
    '<div class="av" style="background:' + f.c + '18;color:' + f.c + '">' + f.e + '</div>' +
    '<div class="mb"><div class="mh">' +
      '<span class="an" style="color:' + f.c + '">' + f.l + '</span>' + dir +
      '<span class="tm">' + ftime(ev.timestamp) + '</span>' +
    '</div><button class="cp" onclick="copyMsg(this)">Copy</button>' +
    '<div class="coord-meta">' + esc(meta || 'internal trace') + '</div>' +
    '<div class="mc">' + fmt(normalizeViewerDisplayText(ev.content || '')) + '</div></div>';
  el.querySelector('.mc').dataset.raw = ev.content || '';
  chat.appendChild(el);
  trimTimelineNodes();
  bump();
}

function matchesCoordinationTraceFilters(ev) {
  if (fltType.value && ev.type !== fltType.value) return false;
  if (fltAgent.value && ev.from !== fltAgent.value && ev.to !== fltAgent.value) return false;
  if (fltRoute.value && (ev.route || '') !== fltRoute.value) return false;
  if (fltJob.value && !(ev.job_id || '').toLowerCase().includes(fltJob.value.toLowerCase())) return false;
  if (fltText.value && !(ev.content || '').toLowerCase().includes(fltText.value.toLowerCase())) return false;
  return true;
}

function addJobNotificationToTimeline(ev) {
  const em = document.getElementById('empty');
  if (em) em.remove();
  const fromName = String(ev.from || '').trim() || 'shiro';
  const f = ag(fromName);
  const route = String(ev.route || '').trim();
  const status = String(ev.status || ev.category || '').trim();
  const jobID = String(ev.job_id || '').trim();
  const meta = [route, status, jobID].filter(Boolean).join(' / ');
  const el = document.createElement('div');
  el.className = 'msg assistant job-interrupt';
  el.innerHTML =
    '<div class="av" style="background:' + f.c + '18;color:' + f.c + '">' + f.e + '</div>' +
    '<div class="mb"><div class="mh">' +
      '<span class="an" style="color:' + f.c + '">' + f.l + '</span>' +
      '<span class="dir">割り込み報告</span>' +
      '<span class="tm">' + ftime(ev.timestamp) + '</span>' +
    '</div><button class="cp" onclick="copyMsg(this)">Copy</button>' +
    '<div class="coord-meta">' + esc(meta || 'job.notification') + '</div>' +
    '<div class="mc">' + fmt(normalizeViewerDisplayText(ev.content || '')) + '</div></div>';
  el.querySelector('.mc').dataset.raw = ev.content || '';
  chat.appendChild(el);
  trimTimelineNodes();
  bump();
}

function addTaskNotificationToTimeline(ev) {
  const em = document.getElementById('empty');
  if (em) em.remove();
  const fromName = String(ev.from || '').trim() || 'shiro';
  const f = ag(fromName);
  const route = String(ev.route || '').trim();
  const status = String(ev.status || ev.category || '').trim();
  const taskID = String(ev.task_id || '').trim();
  const meta = [route, status, taskID].filter(Boolean).join(' / ');
  const el = document.createElement('div');
  el.className = 'msg assistant task-interrupt';
  el.innerHTML =
    '<div class="av" style="background:' + f.c + '18;color:' + f.c + '">' + f.e + '</div>' +
    '<div class="mb"><div class="mh">' +
      '<span class="an" style="color:' + f.c + '">' + f.l + '</span>' +
      '<span class="dir">割り込み報告</span>' +
      '<span class="tm">' + ftime(ev.timestamp) + '</span>' +
    '</div><button class="cp" onclick="copyMsg(this)">Copy</button>' +
    '<div class="coord-meta">' + esc(meta || 'task.notification') + '</div>' +
    '<div class="mc">' + fmt(normalizeViewerDisplayText(ev.content || '')) + '</div></div>';
  el.querySelector('.mc').dataset.raw = ev.content || '';
  chat.appendChild(el);
  trimTimelineNodes();
  bump();
}

function isViewerLocalFailureMessage(ev) {
  return String(ev && ev.content ? ev.content : '').startsWith('Viewer send unavailable:');
}
