'use strict';
const BT = String.fromCharCode(96);
const A = {
  user:   {c:'#94a3b8', l:'れん',  en:'Ren',   e:'\u{1f464}'},
  mio:    {c:'#f472b6', l:'みお',  en:'Mio',   e:'\u{1f338}'},
  shiro:  {c:'#22d3ee', l:'しろ',  en:'Shiro', e:'\u26a1'},
  kuro:   {c:'#64748b', l:'くろ',  en:'Kuro',  e:'K'},
  midori: {c:'#34d399', l:'みどり', en:'Midori', e:'M'},
  worker: {c:'#38bdf8', l:'Worker', en:'Worker', e:'W'},
  coder1: {c:'#818cf8', l:'あお',  en:'AO',    e:'\u{1f535}'},
  coder2: {c:'#fb923c', l:'あか',  en:'Aka',   e:'\u{1f534}'},
  coder3: {c:'#facc15', l:'きん',  en:'Kin',   e:'\u{1f7e1}'},
  coder4: {c:'#a78bfa', l:'ぎん',  en:'Gin',   e:'\u{1f7e3}'},
  gemma4: {c:'#34d399', l:'Gemma4', en:'Gemma4', e:'G4'},
  gamma4: {c:'#34d399', l:'Gemma4', en:'Gemma4', e:'G4'},
  system: {c:'#475569', l:'System', en:'System', e:'\u2699\ufe0f'},
};
const RC = {
  CHAT:'#f472b6', OPS:'#22d3ee', CODE:'#818cf8',
  CODE1:'#818cf8', CODE2:'#fb923c', CODE3:'#facc15', CODE4:'#a78bfa',
  PLAN:'#4ade80', ANALYZE:'#fbbf24', RESEARCH:'#34d399',
  IDLECHAT:'#a78bfa',
};
const AGENTS = ['mio', 'shiro', 'kuro', 'midori', 'coder1', 'coder2', 'coder3', 'coder4'];
const LAB_CHAT_PARTNERS = ['shiro', 'kuro', 'midori'];
const LAB_CHAT_PARTNER_LABELS = {shiro: 'Shiro', kuro: 'Kuro', midori: 'Midori'};
const ROLE_TARGETS = [
  {id:'mio', role:'Chat', alias:'Chat', use:'会話テンポ / ルミナ人格 / 音声UI'},
  {id:'shiro', role:'Worker', alias:'Worker', use:'実務処理 / 要約 / RAG'},
  {id:'kuro', role:'Heavy', alias:'Heavy', use:'深い分析 / 長文推論 / 慎重な判断'},
  {id:'midori', role:'Wild', alias:'Wild', use:'発想展開 / アイデア生成 / 変化球'},
  {id:'coder1', role:'Coder', alias:'Coder1', use:'仕様設計 / 構成整理 / 提案'},
  {id:'coder2', role:'Coder', alias:'Coder2', use:'実装 / 検証 / 差分整理'},
  {id:'coder3', role:'Coder', alias:'Coder3', use:'実装 / 調査 / テスト補助'},
  {id:'coder4', role:'Coder', alias:'Coder4', use:'実装 / レビュー / 仕上げ'},
];
const OFFLINE_MS = 120000;
const MAX_LOGS = 500;
const MAX_TIMELINE_NODES = 400;
const MAX_SEEN_EVENTS = 4000;
const PROGRESS_RECENT_EVENTS = 8;
const PROGRESS_DONE_LIMIT = 10;
const seenEventKeys = new Set();
const seenEventQueue = [];
const seenJobNotificationKeys = new Set();
let jobNotificationPollInFlight = false;
let investmentRefreshTimer = null;

function ag(n) { return A[(n || '').toLowerCase()] || A.system; }
function agName(n) {
  const info = ag(n);
  return info.l || info.en || String(n || '-');
}
function ftime(ts) {
  try { return new Date(ts).toLocaleTimeString('ja-JP', {hour:'2-digit', minute:'2-digit', second:'2-digit', timeZone:'Asia/Tokyo'}); }
  catch (_) { return ''; }
}
function fdt(ts) {
  try { return new Date(ts).toLocaleString('ja-JP', {hour12:false, timeZone:'Asia/Tokyo'}); }
  catch (_) { return ''; }
}
function labDateTimeParts(date) {
  const parts = new Intl.DateTimeFormat('ja-JP-u-ca-gregory', {
    timeZone: 'Asia/Tokyo',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    weekday: 'short',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hourCycle: 'h23',
  }).formatToParts(date || new Date()).reduce((acc, part) => {
    acc[part.type] = part.value;
    return acc;
  }, {});
  return parts;
}
function formatLabDateTime(date) {
  const parts = labDateTimeParts(date);
  const year = Number(parts.year || 0);
  const reiwaYear = Math.max(1, year - 2018);
  return `${parts.year || '0000'}（令和${String(reiwaYear).padStart(2, '0')}）${parts.month || '00'}月${parts.day || '00'}日（${parts.weekday || '-'}）${parts.hour || '00'}:${parts.minute || '00'}:${parts.second || '00'}`;
}
function esc(s) {
  const d = document.createElement('div');
  d.textContent = String(s || '');
  return d.innerHTML;
}
function short(s, n) {
  const v = String(s || '');
  return v.length > n ? v.slice(0, n) + '...' : v;
}
function normState(s) {
  if (s === 'idle' || s === 'thinking' || s === 'running' || s === 'error' || s === 'offline' || s === 'unavailable') return s;
  return 'idle';
}
function fmt(s) {
  let h = esc(s);
  const cbRe = new RegExp(BT+'{3}(\\w*)\\n([\\s\\S]*?)'+BT+'{3}', 'g');
  h = h.replace(cbRe, '<pre><code>$2</code></pre>');
  const icRe = new RegExp(BT+'([^'+BT+' ]+)'+BT, 'g');
  h = h.replace(icRe, '<code>$1</code>');
  h = h.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  h = h.replace(/^## (.+)$/gm, '<h2>$1</h2>');
  h = h.replace(/^### (.+)$/gm, '<h3>$1</h3>');
  return h;
}

function eventKey(ev) {
  const structuredIDs = eventStructuredIDParts(ev);
  return [
    ev.type || '',
    ev.from || '',
    ev.to || '',
    ev.route || '',
    ev.job_id || '',
    ev.session_id || '',
    ev.channel || '',
    ev.chat_id || '',
    structuredIDs.messageId,
    structuredIDs.responseId,
    structuredIDs.utteranceId,
    structuredIDs.turnIndex,
    structuredIDs.chunkIndex,
    ev.timestamp || '',
    ev.content || '',
  ].join('|');
}

function eventStructuredIDParts(ev) {
  const parts = {
    messageId: String((ev && (ev.message_id || ev.messageId)) || ''),
    responseId: String((ev && (ev.response_id || ev.responseId)) || ''),
    utteranceId: String((ev && (ev.utterance_id || ev.utteranceId)) || ''),
    turnIndex: String(ev && (ev.turn_index ?? ev.turnIndex ?? '')),
    chunkIndex: String(ev && (ev.chunk_index ?? ev.chunkIndex ?? '')),
  };
  if (!ev || !ev.content || (parts.messageId && parts.responseId && parts.utteranceId)) return parts;
  try {
    const payload = JSON.parse(ev.content || '{}');
    parts.messageId = parts.messageId || String(payload.message_id || payload.messageId || '');
    parts.responseId = parts.responseId || String(payload.response_id || payload.responseId || '');
    parts.utteranceId = parts.utteranceId || String(payload.utterance_id || payload.utteranceId || '');
    parts.turnIndex = parts.turnIndex || String(payload.turn_index ?? payload.turnIndex ?? '');
    parts.chunkIndex = parts.chunkIndex || String(payload.chunk_index ?? payload.chunkIndex ?? '');
  } catch (_) {}
  return parts;
}

function rememberEventKey(key) {
  seenEventKeys.add(key);
  seenEventQueue.push(key);
  while (seenEventQueue.length > MAX_SEEN_EVENTS) {
    const old = seenEventQueue.shift();
    if (!old) break;
    seenEventKeys.delete(old);
  }
}

// Viewer state ownership:
// - UI ephemeral: DOM/display preferences, selected rows, scroll/tab/open state, transient fetch errors.
// - Backend snapshot copy: runtime/status/log/history data fetched from Viewer endpoints; never authoritative.
// - Dangerous derived state: queues, pending maps, dedupe keys, rendered diagnostics; keep local, bounded, and disposable.
// Do not use this object as the source of truth for transcript, TTS ACK, utterance consumption, or session progress.
const state = {
  logs: [],
  homeSendError: '',
  viewerAttachmentError: '',
  viewerStatusFetchError: '',
  sessions: {},
  jobs: {},
  evidence: [],
  evidenceSummary: {status: {}, error_kind: {}},
  evidenceFetchError: '',
  evidenceSummaryFetchError: '',
  verificationReports: [],
  verificationSummary: {status: {}, trigger_level: {}},
  verificationFetchError: '',
  verificationSummaryFetchError: '',
  evidenceOrder: [],
  selectedEvidenceJobID: '',
  selectedEvidenceItem: null,
  selectedEvidenceFocus: '',
  evidenceSortDesc: true,
  pendingEvidenceJobID: '',
  memory: {
    snapshot: {memory: [], news: [], digests: [], knowledge: []},
    memorySnapshotFetchError: '',
    layers: {l0: [], l1: [], l2: [], l3: []},
    recallPack: {items: []},
    recallPackFetchError: '',
    userMemory: [],
    userMemoryFetchError: '',
    events: [],
    searchCache: [],
    memoryEventsFetchError: '',
    sourceRegistry: [],
    sourceRegistryStaging: [],
    sourceRegistryFetchError: '',
    sourceRegistryStagingFetchError: '',
    domainGraphAssertions: [],
    domainGraphAssertionsMeta: {limit: 50, offset: 0, total: 0},
    domainGraphAssertionsFetchError: '',
    knowledgeMemory: {
      personal_archive: [],
      creative_knowledge: [],
      news_knowledge: [],
      daily_intake_rules: [],
      temporal_markers: [],
      dream_runs: [],
    },
    knowledgeMemoryFetchError: '',
    knowledgeMemoryDetail: null,
    knowledgeMemoryReviewResult: null,
    traces: [],
    recallTraceFetchError: '',
    newsPackFetchError: '',
    selectedNewsIndex: 0,
  },
  investment: {
    available: false,
    loading: false,
    dbPath: '',
    status: '',
    statusMessage: '',
    fetchError: '',
    snapshot: null,
    recentSnapshots: [],
    sourceHealth: [],
    featureRows: [],
    eventRows: [],
    summary: {},
    refreshedAt: '',
  },
  agents: {},
  idleChat: {
    selectedMode: localStorage.getItem('idlechat.selectedMode') || 'manual',
    selectedView: localStorage.getItem('idlechat.selectedView') || 'live',
    mode: '',
    manualMode: false,
    chatActive: false,
    interrupted: false,
    interruptedAt: 0,
    interruptedSessionId: '',
    currentTopic: '',
    forecastStock: null,
    history: [],
    openIndex: -1,
    selectedSummaryIndex: 0,
  },
  openTasks: {},
  progressOpenJobs: {},
  ops: {
    persistedLogs: [],
    opsLogsFetchError: '',
    toolHarnessEvents: [],
    toolHarnessFetchError: '',
    dciTraces: [],
    dciFetchError: '',
    dciLastResult: null,
    gameBridgeStatus: null,
    gameBridgeSessions: [],
    gameBridgeEvents: [],
    gameBridgeStatusFetchError: '',
    gameBridgeSourceFetchError: '',
    gameBridgeSkippedCount: 0,
    sandboxes: [],
    sandboxArtifacts: [],
    sandboxPromotions: [],
    sandboxDecisions: [],
    sandboxGateLogs: [],
    sandboxFetchError: '',
    sandboxPromotionPreviewResult: null,
    skillManifests: [],
    skillTriggerLogs: [],
    skillChangeLogs: [],
    contributionGateLogs: [],
    skillExternalPRSubmitRecords: [],
    skillExternalPRAdapter: '',
    skillExternalPRAdapterConfigured: false,
    skillExternalPRHumanApprovalRequired: true,
    skillGovernanceFetchError: '',
    workstreams: [],
    workstreamGoals: [],
    workstreamArtifacts: [],
    workstreamAnnotations: [],
    workstreamSteering: [],
    workstreamHeartbeats: [],
    workstreamVaultUpdates: [],
    workstreamFetchError: '',
    workstreamVaultPreviewResult: null,
    workstreamVaultReviewResult: null,
    revenueMarketResearch: [],
    revenueSNSPostMetrics: [],
    revenueProducts: [],
    revenueCustomerVoices: [],
    revenueEvents: [],
    revenueHumanDecisions: [],
    revenueDailyRoutineReports: [],
    revenueChannelDrafts: [],
    revenueExternalSendApplyRecords: [],
    revenueExternalChannelAdapter: '',
    revenueExternalChannelAdapterConfigured: false,
    revenueExternalSendHumanApprovalRequired: true,
    revenueSummary: null,
    revenueFetchError: '',
    revenueDecisionReviewResult: null,
    personaDiscomfortLogs: [],
    personaTriggerLogs: [],
    personaCanonicalResponseLogs: [],
    personaObservationLogs: [],
    personaMetaProfileUpdates: [],
    personaMetaReviewResult: null,
    personaInterfaceSessions: [],
    personaObservationFetchError: '',
    browserTraceRuns: [],
    browserTraceAPICandidates: [],
    browserTraceAPISchemas: [],
    browserTraceAPICoverageReports: [],
    browserTraceAPIArtifacts: [],
    browserTraceAPIFetchError: '',
    browserTraceAPIFetcherProposalResult: null,
    complexityScans: [],
    complexityHotspots: [],
    complexityEvidence: [],
    complexityReports: [],
    complexityFetchError: '',
    aiWorkflowEvents: [],
    aiWorkflowProjectMemoryIndexes: [],
    aiWorkflowWorktreeRegistries: [],
    aiWorkflowCommandRegistries: [],
    aiWorkflowContextUsages: [],
    aiWorkflowContextBudgetPolicy: null,
    aiWorkflowFetchError: '',
    superAgentRuns: [],
    superAgentSubagentTasks: [],
    superAgentContextPacks: [],
    superAgentMessageChannels: [],
    superAgentTraceEvents: [],
    superAgentRunQueue: [],
    superAgentRuntimeConfig: null,
    superAgentFetchError: '',
    heavyWorkerRuntimeDiagnostics: null,
    heavyWorkerRuntimeDiagnosticsFetchError: '',
	    knowledgePersonalArchive: [],
	    knowledgeCreativeItems: [],
	    knowledgeNewsItems: [],
	    knowledgeDailyIntakeRules: [],
	    knowledgeTemporalMarkers: [],
	    knowledgeDreamRuns: [],
	    knowledgeMemoryFetchError: '',
	    knowledgeMemoryDetail: null,
	    hobbyGraphOverview: null,
	    hobbyGraphOverviewFetchError: '',
	    runtimeBlockedRoutes: [],
    lastMioReport: null,
    latestJobID: '',
    latestRoute: '',
    latestError: null,
    llmOpsEnabled: false,
    localLLM: null,
    runtimeReadiness: null,
    runtimeConfigFetchError: '',
    runtimeSTTBaseURL: '',
    runtimeSTTStreamURL: '',
    runtimeTTSBaseURL: '',
    runtimeDebugSystemFetchError: '',
    llmStatus: null,
    llmStatusError: '',
    runtimeHealth: null,
    runtimeHealthError: '',
  },
  debug: {
    gpu: null,
    audio: null,
    latencyMetrics: [],
    latencyLatest: {},
    latencySeen: {},
    eventReceiveTimes: {},
    sttTrace: [],
    thinkTrace: [],
  },
};
AGENTS.forEach((id) => {
  state.agents[id] = {
    id,
    state: 'offline',
    reason: '',
    route: '-',
    lastEvent: '-',
    peer: '-',
    jobID: '-',
    sessionID: 'viewer',
    preview: '-',
    updatedAt: '',
  };
  state.openTasks[id] = {};
});

let msgCount = 0;
const mainEl = document.querySelector('main');
const chat = document.getElementById('chat');
const idleLiveLog = document.getElementById('idleLiveLog');
const ctr = document.getElementById('ctr');
const cnt = document.getElementById('cnt');
const latestBtn = document.getElementById('latestBtn');
const toastEl = document.getElementById('toast');
const thinkingBubbles = {};
// Browser playback state only. Backend owns TTS completion/ACK truth; this state only tracks this tab's audio queue,
// current chunk, and local error display until the backend confirms or rejects playback progress.
const ttsPlayback = {
  queue: [],
  audio: null,
  playing: false,
  audioEnabled: false,
  unlocked: false,
  blocked: false,
  audioError: '',
  currentCharacterId: '',
  currentText: '',
  currentDisplayText: '',
  currentSessionId: '',
  currentChunkIndex: -1,
  currentUtteranceId: '',
  currentResponseId: '',
  currentMessageId: '',
  currentTurnIndex: -1,
  currentShown: false,
  fallbackActive: false,
  fallbackTimer: null,
  tailActive: false,
  tailTimer: null,
  blockedFallbackUtteranceId: '',
  seq: 0,
  preloadedAudio: new Map(),
};
const viewerControl = {
  clientId: loadViewerClientID(),
  activeAudioViewerId: '',
  activeInputViewerId: '',
};
const lipSyncMioEl = document.getElementById('lipSyncMio');
const lipSyncShiroEl = document.getElementById('lipSyncShiro');
const chatCharacterMioLayeredEl = document.getElementById('chatCharacterMioLayered');
const idleCharacterMioLayeredEl = document.getElementById('idleCharacterMioLayered');
const lipSyncActors = {
  mio: lipSyncMioEl,
  shiro: lipSyncShiroEl,
};

const ttsNowPlayingEl = document.getElementById('ttsNowPlaying');
const ttsNowPlayingTextEl = document.getElementById('ttsNowPlayingText');
const centralTTSSpeech = {
  el: null,
  textEl: null,
  characterId: '',
  sessionId: '',
  responseId: '',
  bubbleKind: '',
  active: false,
  preRendered: false,
  chunkKeys: new Set(),
};
const idleTTSSpeech = {
  el: null,
  textEl: null,
  characterId: '',
  sessionId: '',
  responseId: '',
  bubbleKind: '',
  active: false,
  preRendered: false,
  chunkKeys: new Set(),
};
const idlePendingMessages = new Map();
let idleLiveTopicKey = '';
let idleLiveActiveSessionId = '';
let idleLiveSnapshotKey = '';
// Diagnostic render trace only. Never drive transcript, pending TTS, ACK, or session progression from this log.
const idleLiveRenderedLog = [];
if (typeof window !== 'undefined') window.__idleLiveRenderedLog = idleLiveRenderedLog;
const IDLE_MESSAGE_FALLBACK_MS = 60000;

function loadViewerClientID() {
  const tabKey = 'rencrow.viewer_tab_client_id';
  const legacyKey = 'rencrow.viewer_client_id';
  try {
    if (typeof sessionStorage !== 'undefined' && sessionStorage) {
      const existingTab = sessionStorage.getItem(tabKey);
      if (existingTab) return existingTab;
    }
  } catch (_) {}
  let id = '';
  try {
    if (crypto && typeof crypto.randomUUID === 'function') {
      id = 'viewer-tab-' + crypto.randomUUID();
    }
  } catch (_) {}
  if (!id) {
    id = 'viewer-tab-' + String(Date.now()) + '-' + Math.random().toString(36).slice(2, 10);
  }
  try {
    if (typeof sessionStorage !== 'undefined' && sessionStorage) {
      sessionStorage.setItem(tabKey, id);
      return id;
    }
  } catch (_) {}
  try {
    const existingLegacy = localStorage.getItem(legacyKey);
    if (existingLegacy) return existingLegacy;
    localStorage.setItem(legacyKey, id);
  } catch (_) {}
  return id;
}

function isThisViewerActiveAudio() {
  const activeID = String(viewerControl.activeAudioViewerId || '').trim();
  return !activeID || activeID === viewerControl.clientId;
}

function isThisViewerActiveInput() {
  const activeID = String(viewerControl.activeInputViewerId || '').trim();
  return !activeID || activeID === viewerControl.clientId;
}

function claimViewerControl(kind, reason) {
  const normalizedKind = String(kind || '').trim();
  if (normalizedKind !== 'audio' && normalizedKind !== 'input') return Promise.resolve(false);
  if (normalizedKind === 'audio') viewerControl.activeAudioViewerId = viewerControl.clientId;
  if (normalizedKind === 'input') viewerControl.activeInputViewerId = viewerControl.clientId;
  return sendViewerControlAction(normalizedKind, 'claim', reason);
}

function heartbeatViewerControl(kind, reason) {
  const normalizedKind = String(kind || '').trim();
  if (normalizedKind !== 'audio' && normalizedKind !== 'input') return Promise.resolve(false);
  return sendViewerControlAction(normalizedKind, 'heartbeat', reason);
}

function releaseViewerControl(kind, reason) {
  const normalizedKind = String(kind || '').trim();
  if (normalizedKind !== 'audio' && normalizedKind !== 'input') return Promise.resolve(false);
  if (normalizedKind === 'audio' && viewerControl.activeAudioViewerId !== viewerControl.clientId) return Promise.resolve(false);
  if (normalizedKind === 'input' && viewerControl.activeInputViewerId !== viewerControl.clientId) return Promise.resolve(false);
  if (normalizedKind === 'audio') viewerControl.activeAudioViewerId = '';
  if (normalizedKind === 'input') viewerControl.activeInputViewerId = '';
  return sendViewerControlAction(normalizedKind, 'release', reason);
}

function sendViewerControlAction(kind, action, reason) {
  if (typeof fetch !== 'function') return Promise.resolve(false);
  return fetch('/viewer/active-control', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({
      viewer_client_id: viewerControl.clientId,
      kind: kind,
      action: action,
      reason: String(reason || '').trim(),
    }),
    keepalive: true,
  }).then((res) => {
    if (!res.ok) throw new Error('HTTP ' + String(res.status));
    return res.json().catch(() => ({}));
  }).then((payload) => {
    if (payload && Object.prototype.hasOwnProperty.call(payload, 'active_audio_viewer_id')) {
      viewerControl.activeAudioViewerId = String(payload.active_audio_viewer_id || '');
    }
    if (payload && Object.prototype.hasOwnProperty.call(payload, 'active_input_viewer_id')) {
      viewerControl.activeInputViewerId = String(payload.active_input_viewer_id || '');
    }
    return true;
  }).catch((err) => {
    console.warn('[ViewerActive] control failed:', err && err.message ? err.message : err);
    return false;
  });
}

function handleViewerActiveControlEvent(ev) {
  if (!ev || ev.type !== 'viewer.active_control') return;
  let payload = {};
  try {
    payload = JSON.parse(ev.content || '{}');
  } catch (_) {
    return;
  }
  if (Object.prototype.hasOwnProperty.call(payload, 'active_audio_viewer_id')) {
    viewerControl.activeAudioViewerId = String(payload.active_audio_viewer_id || '');
  }
  if (Object.prototype.hasOwnProperty.call(payload, 'active_input_viewer_id')) {
    viewerControl.activeInputViewerId = String(payload.active_input_viewer_id || '');
  }
  const kind = String(payload.kind || '').trim();
  const owner = String(payload.viewer_client_id || '').trim();
  if (kind === 'audio' && owner && owner !== viewerControl.clientId) {
    if (typeof clearIdleLivePendingForAudioOwnerTransfer === 'function') {
      clearIdleLivePendingForAudioOwnerTransfer(owner);
    }
    if (ttsPlayback.playing || ttsPlayback.audioEnabled || ttsPlayback.queue.length > 0) {
      chatAudioSync.disableAudio();
    }
  }
  if (kind === 'input' && owner && owner !== viewerControl.clientId) {
    try {
      if (sttState && sttState.isRecording) abortSTTImmediately('active_input_transferred');
      if (vdsState && vdsState.isRecording) abortVDSImmediately('active_input_transferred');
    } catch (_) {}
  }
}

// Character expression state management
const characterStates = {
  mio: {
    state: 'idle',
    expressionURL: '',
    talkOpenURL: '',
    talkClosedURL: '',
    layered: true,
    currentExpression: 'normal',
    parts: {
      base: '',
      eyebrowLeft: '',
      eyebrowRight: '',
      eyeLeft: '',
      eyeRight: '',
      mouth: ''
    }
  },
  shiro: {
    state: 'idle',
    expressionURL: '',
    talkOpenURL: '',
    talkClosedURL: '',
    layered: false
  },
};

async function loadCharacterState(characterId, stateName) {
  const id = String(characterId || '').trim().toLowerCase();
  if (!characterStates[id]) return;

  // Check if character uses layered display
  if (characterStates[id].layered) {
    return loadLayeredCharacterState(id, stateName);
  }

  try {
    const response = await fetch(`/viewer/character/state?character_id=${id}&state=${stateName}`);
    if (!response.ok) {
      console.error(`Failed to load character state for ${id}: ${response.statusText}`);
      return;
    }

    const data = await response.json();
    characterStates[id].state = stateName;
    characterStates[id].expressionURL = data.expression_url;
    characterStates[id].talkOpenURL = data.talk_open_url;
    characterStates[id].talkClosedURL = data.talk_closed_url;

    // Update all character images
    updateCharacterImages(id, data.expression_url, data.talk_open_url);
  } catch (err) {
    console.error(`Error loading character state for ${id}:`, err);
  }
}

async function loadLayeredCharacterState(characterId, expressionName) {
  const id = String(characterId || '').trim().toLowerCase();
  if (!characterStates[id] || !characterStates[id].layered) return;

  try {
    const response = await fetch(`/viewer/character/layered/state?character_id=${id}&expression=${expressionName}`);
    if (!response.ok) {
      console.error(`Failed to load layered character state for ${id}: ${response.statusText}`);
      return;
    }

    const data = await response.json();
    characterStates[id].currentExpression = expressionName;
    characterStates[id].canvasSize = data.canvas_size;
    characterStates[id].parts = {
      base: data.parts.base,
      eyebrowLeft: data.parts.eyebrow_left,
      eyebrowRight: data.parts.eyebrow_right,
      eyeLeft: data.parts.eye_left,
      eyeRight: data.parts.eye_right,
      mouth: data.parts.mouth
    };

    // Update layered character display
    updateLayeredCharacterParts(id);
  } catch (err) {
    console.error(`Error loading layered character state for ${id}:`, err);
  }
}

function updateLayeredCharacterParts(characterId) {
  const id = String(characterId || '').trim().toLowerCase();
  const state = characterStates[id];
  if (!state || !state.layered) return;

  // Try multiple container IDs
  const containerIds = [
    `chatCharacter${id.charAt(0).toUpperCase() + id.slice(1)}Layered`,
    `idleCharacter${id.charAt(0).toUpperCase() + id.slice(1)}Layered`
  ];

  let containers = [];
  for (const containerId of containerIds) {
    const el = document.getElementById(containerId);
    if (el) containers.push(el);
  }

  if (containers.length === 0) return;

  const parts = state.parts;
  const canvasSize = state.canvasSize || { w: 1254, h: 1254 };

  // Helper function to set part with bounds
  function setPartWithBounds(img, partData) {
    if (!img || !partData) return;

    const url = partData.url || partData;
    const bounds = partData.bounds;

    img.src = url;

    if (bounds) {
      // Calculate position and size as percentages of canvas
      const left = (bounds.x / canvasSize.w) * 100;
      const top = (bounds.y / canvasSize.h) * 100;
      const width = (bounds.w / canvasSize.w) * 100;
      const height = (bounds.h / canvasSize.h) * 100;

      img.style.left = `${left}%`;
      img.style.top = `${top}%`;
      img.style.width = `${width}%`;
      img.style.height = `${height}%`;
    }
  }

  // Update all containers
  for (const container of containers) {
    const baseImg = container.querySelector('.character-base');
    const eyebrowLeftImg = container.querySelector('.character-eyebrow-left');
    const eyebrowRightImg = container.querySelector('.character-eyebrow-right');
    const eyeLeftImg = container.querySelector('.character-eye-left');
    const eyeRightImg = container.querySelector('.character-eye-right');
    const mouthImg = container.querySelector('.character-mouth');

    // Base image fills the entire container
    if (baseImg && parts.base) {
      baseImg.src = parts.base.url || parts.base;
      baseImg.style.width = '100%';
      baseImg.style.height = '100%';
      baseImg.style.left = '0';
      baseImg.style.top = '0';
    }

    // Set other parts with their bounds
    setPartWithBounds(eyebrowLeftImg, parts.eyebrowLeft);
    setPartWithBounds(eyebrowRightImg, parts.eyebrowRight);
    setPartWithBounds(eyeLeftImg, parts.eyeLeft);
    setPartWithBounds(eyeRightImg, parts.eyeRight);
    setPartWithBounds(mouthImg, parts.mouth);
  }
}

async function setLayeredMouth(characterId, mouthId) {
  const id = String(characterId || '').trim().toLowerCase();
  const state = characterStates[id];
  if (!state || !state.layered) return;

  try {
    const response = await fetch(`/viewer/character/layered/mouth?character_id=${id}&expression=${state.currentExpression}&mouth_id=${mouthId}`);
    if (!response.ok) {
      console.error(`Failed to set mouth for ${id}: ${response.statusText}`);
      return;
    }

    const data = await response.json();
    state.parts.mouth = data.parts.mouth;

    // Update only the mouth part in all containers
    const containerIds = [
      `chatCharacter${id.charAt(0).toUpperCase() + id.slice(1)}Layered`,
      `idleCharacter${id.charAt(0).toUpperCase() + id.slice(1)}Layered`
    ];

    const canvasSize = state.canvasSize || { w: 1254, h: 1254 };
    const partData = state.parts.mouth;

    for (const containerId of containerIds) {
      const container = document.getElementById(containerId);
      if (!container) continue;

      const mouthImg = container.querySelector('.character-mouth');

      if (mouthImg && partData) {
        const url = partData.url || partData;
        const bounds = partData.bounds;

        mouthImg.src = url;

        if (bounds) {
          const left = (bounds.x / canvasSize.w) * 100;
          const top = (bounds.y / canvasSize.h) * 100;
          const width = (bounds.w / canvasSize.w) * 100;
          const height = (bounds.h / canvasSize.h) * 100;

          mouthImg.style.left = `${left}%`;
          mouthImg.style.top = `${top}%`;
          mouthImg.style.width = `${width}%`;
          mouthImg.style.height = `${height}%`;
        }
      }
    }
  } catch (err) {
    console.error(`Error setting layered mouth for ${id}:`, err);
  }
}

function updateCharacterImages(characterId, expressionURL, talkOpenURL) {
  const id = String(characterId || '').trim().toLowerCase();

  // Update lipsync actor element (for non-layered characters)
  const lipSyncEl = lipSyncActors[id];
  if (lipSyncEl) {
    lipSyncEl.dataset.open = talkOpenURL;
    lipSyncEl.dataset.closed = expressionURL;
    // Update the current image if not currently speaking
    const currentSrc = lipSyncEl.src || '';
    if (!currentSrc.includes('talk_open')) {
      lipSyncEl.src = expressionURL;
    }
  }

  // For layered characters, the updateLayeredCharacterParts function handles updates
  // Non-layered character images (like single-image Shiro) are handled here
  const state = characterStates[id];
  if (state && state.layered) {
    // Layered character - already updated by updateLayeredCharacterParts
    return;
  }

  // Update chat character avatar (non-layered only)
  const chatAvatar = document.querySelector('.chat-character-avatar:not(.character-layered)');
  if (chatAvatar && id === 'mio') {
    chatAvatar.src = expressionURL;
  }

  // Update idle character avatars (non-layered only)
  const idleAvatars = document.querySelectorAll(`.idle-character-avatar.${id}:not(.character-layered)`);
  idleAvatars.forEach(avatar => {
    avatar.src = expressionURL;
  });
}

function setCharacterExpression(characterId, stateName) {
  loadCharacterState(characterId, stateName);
}

// Initialize character expressions after characterStates and loaders exist.
if (chatCharacterMioLayeredEl || idleCharacterMioLayeredEl) {
  loadCharacterState('mio', 'idle');
}
if (lipSyncShiroEl) {
  loadCharacterState('shiro', 'idle');
}

function setLipSyncSpeaking(characterId, speaking) {
  const id = String(characterId || '').trim().toLowerCase();
  const state = characterStates[id];

  // Use layered display for characters that support it
  if (state && state.layered) {
    if (speaking) {
      // Use mouth_06 for open mouth (talk_open uses mouth_06)
      setLayeredMouth(id, 'mouth_06');
    } else {
      // Reload current expression to restore normal mouth
      const currentExpression = state.currentExpression || 'normal';
      loadLayeredCharacterState(id, currentExpression);
    }
    return;
  }

  // Fallback to single-image display
  const el = lipSyncActors[id];
  if (!el) return;
  const openSrc = String(el.dataset.open || '').trim();
  const closedSrc = String(el.dataset.closed || '').trim();
  el.src = speaking ? openSrc : closedSrc;
}

function clearLipSyncSpeaking() {
  setLipSyncSpeaking('mio', false);
  setLipSyncSpeaking('shiro', false);
}

function setNowPlayingText(characterId, text) {
  if (!ttsNowPlayingEl || !ttsNowPlayingTextEl) return;
  const normalizedText = String(text || '').trim();
  ttsNowPlayingEl.classList.remove('mio', 'shiro');
  if (!normalizedText) {
    ttsNowPlayingEl.classList.add('hidden');
    ttsNowPlayingTextEl.textContent = '';
    return;
  }
  const id = String(characterId || '').trim().toLowerCase();
  if (id === 'mio' || id === 'shiro') ttsNowPlayingEl.classList.add(id);
  ttsNowPlayingTextEl.textContent = normalizedText;
  ttsNowPlayingEl.classList.remove('hidden');
}

function describeTTSAudioError(err) {
  if (!err) return 'unknown audio playback error';
  const name = String(err.name || '').trim();
  const message = String(err.message || err).trim();
  return [name, message].filter(Boolean).join(': ') || 'unknown audio playback error';
}

function setTTSAudioError(err) {
  const message = describeTTSAudioError(err);
  ttsPlayback.audioError = message;
  setNowPlayingText('', 'TTS audio unavailable: ' + message);
}

function clearTTSAudioError() {
  ttsPlayback.audioError = '';
}

function isIdleChatSessionId(sessionId) {
  const sid = String(sessionId || '').trim();
  return sid.indexOf('idle-') === 0 || sid.indexOf('forecast-') === 0 || sid.indexOf('story-') === 0 || sid.indexOf('story-simple-') === 0;
}

function setCentralTTSSpeechText(characterId, text, sessionId, chunkIndex, utteranceId, responseId, messageId, turnIndex) {
  const target = isIdleChatSessionId(sessionId) ? 'idle' : 'central';
  setTTSSpeechText(target, characterId, text, sessionId, chunkIndex, utteranceId, responseId, messageId, turnIndex);
}

function setTTSSpeechText(target, characterId, text, sessionId, chunkIndex, utteranceId, responseId, messageId, turnIndex) {
  if (target === 'idle') {
    renderIdleTTSSpeechText(characterId, text, sessionId, chunkIndex, utteranceId, responseId, messageId, turnIndex);
    return;
  }
  renderChatTTSSpeechText(characterId, text, sessionId, chunkIndex, utteranceId, responseId);
}

function renderChatTTSSpeechText(characterId, text, sessionId, chunkIndex, utteranceId, responseId) {
  const normalizedText = String(text || '').trim();
  if (!normalizedText) {
    resetTTSSpeechBubble(centralTTSSpeech);
    return;
  }
  if (!chat) return;

  const id = String(characterId || '').trim().toLowerCase();
  const sid = String(sessionId || '').trim();
  const normalizedChunkIndex = Number.isFinite(chunkIndex) ? chunkIndex : -1;
  const rid = String(responseId || '').trim();
  const speech = centralTTSSpeech;
  const bubbleKind = ttsBubbleKind(speech, normalizedText, sid, normalizedChunkIndex, id);
  const f = ag(id || 'mio');
  const key = ttsChunkIdentityKey(sid, utteranceId, normalizedChunkIndex, speech.chunkKeys.size);
  if (!speech.el || speech.characterId !== id || speech.bubbleKind !== bubbleKind || shouldStartNewTTSBubble(speech, normalizedChunkIndex, key, rid)) {
    if (speech.el) speech.el.classList.remove('tts-current');
    const el = document.createElement('div');
    el.className = 'msg tts-current' + (id === 'shiro' ? ' shiro' : '');
    el.innerHTML =
      '<div class="av" style="background:' + f.c + '18;color:' + f.c + '">' + f.e + '</div>' +
      '<div class="mb"><div class="mh">' +
        '<span class="an" style="color:' + f.c + '">' + f.l + '</span>' +
        '<span class="tm">' + ftime(new Date().toISOString()) + '</span>' +
      '</div><div class="mc"></div></div>';
    speech.el = el;
    speech.textEl = el.querySelector('.mc');
    speech.characterId = id;
    speech.sessionId = sid;
    speech.responseId = rid;
    speech.bubbleKind = bubbleKind;
    speech.active = true;
    speech.preRendered = false;
    speech.chunkKeys = new Set();
    const em = document.getElementById('empty');
    if (em) em.remove();
    chat.appendChild(el);
    trimTimelineNodesFor(chat, MAX_TIMELINE_NODES);
  } else {
    speech.el.classList.add('tts-current');
    speech.el.classList.toggle('shiro', id === 'shiro');
    speech.sessionId = sid;
    if (rid) speech.responseId = rid;
    speech.active = true;
  }
  if (speech.chunkKeys.has(key)) {
    return;
  }
  speech.chunkKeys.add(key);
  if (speech.textEl) {
    const current = String(speech.textEl.textContent || '');
    if (!speech.preRendered) {
      speech.textEl.textContent = appendCentralTTSText(current, normalizedText);
      speech.textEl.dataset.raw = speech.textEl.textContent;
    }
  }
  scrollToBottom();
}

function renderIdleTTSSpeechText(characterId, text, sessionId, chunkIndex, utteranceId, responseId, messageId, turnIndex) {
  const normalizedText = String(text || '').trim();
  if (!normalizedText) {
    resetTTSSpeechBubble(idleTTSSpeech);
    return;
  }
  const target = typeof idleLiveRenderTarget === 'function' ? idleLiveRenderTarget() : idleLiveLog;
  if (!target) return;

  const id = String(characterId || '').trim().toLowerCase();
  const sid = String(sessionId || '').trim();
  const normalizedChunkIndex = Number.isFinite(chunkIndex) ? chunkIndex : -1;
  const rid = String(responseId || '').trim();
  const speech = idleTTSSpeech;
  const bubbleKind = ttsBubbleKind(speech, normalizedText, sid, normalizedChunkIndex, id);
  if (bubbleKind === 'topic' && document.body && document.body.classList.contains('live-mode')) {
    return;
  }
  const f = ag(id || 'mio');
  const key = ttsChunkIdentityKey(sid, utteranceId, normalizedChunkIndex, speech.chunkKeys.size);
  if (!speech.el || speech.characterId !== id || speech.bubbleKind !== bubbleKind || shouldStartNewTTSBubble(speech, normalizedChunkIndex, key, rid)) {
    if (speech.el) speech.el.classList.remove('tts-current');
    const rendered = consumeIdlePendingMessage(sid, id, bubbleKind, messageId, turnIndex);
    if (typeof isIdleSuppressedTTSMessage === 'function' && isIdleSuppressedTTSMessage(messageId)) {
      if (rendered) rendered.consumed = true;
      if (typeof recordIdleLiveDiagnostic === 'function') {
        recordIdleLiveDiagnostic('speech_tts_suppressed', rendered && rendered.ev ? rendered.ev : {
          type: 'idlechat.message',
          session_id: sid,
          message_id: messageId,
          turn_index: turnIndex,
        }, {
          reason: 'summary speech is already represented by idlechat.summary',
          response_id: rid,
          utterance_id: String(utteranceId || '').trim(),
        });
      }
      return;
    }
    const renderedWasPending = !!(rendered && (!rendered.el || (rendered.el.classList && rendered.el.classList.contains('idle-pending-tts'))));
    if (rendered && !renderedWasPending && typeof renderIdlePendingMessageFromEvent === 'function') {
      renderIdlePendingMessageFromEvent(rendered);
    }
    const existing = !rendered && typeof findIdleLiveMessageNode === 'function'
      ? findIdleLiveMessageNode({type: 'idlechat.message', session_id: sid, message_id: messageId, turn_index: turnIndex})
      : null;
    const liveMode = document.body && document.body.classList.contains('live-mode');
    if (!rendered && !existing && liveMode && bubbleKind === 'speech' && !String(messageId || '').trim() && !(Number.isFinite(turnIndex) && turnIndex >= 0)) {
      if (typeof renderIdleTTSChunkError === 'function') {
        renderIdleTTSChunkError({
          characterId: id,
          sessionId: sid,
          responseId: rid,
          utteranceId,
          messageId,
          turnIndex,
        }, 'TTS_IDENTITY_MISSING', 'TTS chunk did not include message_id or turn_index; Viewer refused speaker/first-pending matching.');
      }
      return;
    }
    if (!rendered && !existing && bubbleKind === 'speech' && (String(messageId || '').trim() || (Number.isFinite(turnIndex) && turnIndex >= 0))) {
      if (typeof renderIdleTTSChunkError === 'function') {
        renderIdleTTSChunkError({
          characterId: id,
          sessionId: sid,
          responseId: rid,
          utteranceId,
          messageId,
          turnIndex,
        }, 'TTS_DISPLAY_SOURCE_MISSING', 'TTS chunk had a message identity but no matching idlechat.message display event; Viewer refused to use TTS text as chat body.');
      }
      return;
    }
    const el = rendered && rendered.el ? rendered.el : (existing || document.createElement('div'));
    if ((rendered && rendered.el) || existing) {
      el.classList.remove('idle-pending-tts');
      el.classList.add('tts-current');
      el.classList.add('idle-kind-tts');
      el.classList.add('idle-kind-' + bubbleKind);
      el.classList.toggle('shiro', id === 'shiro');
    } else {
      el.className = 'msg tts-current' + (id === 'shiro' ? ' shiro' : '') + ' idle-live-item idle-kind-tts idle-kind-' + bubbleKind;
      el.innerHTML =
        '<div class="av" style="background:' + f.c + '18;color:' + f.c + '">' + f.e + '</div>' +
        '<div class="mb"><div class="mh">' +
          '<span class="idle-kind">' + (bubbleKind === 'topic' ? 'Topic' : 'Speech') + '</span>' +
          '<span class="an" style="color:' + f.c + '">' + f.l + '</span>' +
          '<span class="tm">' + ftime(new Date().toISOString()) + '</span>' +
        '</div><div class="mc"></div></div>';
    }
    const elMessageID = String(messageId || '').trim();
    if (elMessageID) el.dataset.messageId = elMessageID;
    if (Number.isFinite(turnIndex) && turnIndex >= 0) el.dataset.turnIndex = String(Math.floor(turnIndex));
    speech.el = el;
    speech.textEl = el.querySelector('.mc');
    speech.characterId = id;
    speech.sessionId = sid;
    speech.responseId = rid;
    speech.bubbleKind = bubbleKind;
    speech.active = true;
    speech.preRendered = !!((rendered && rendered.el && !renderedWasPending) || (existing && !renderedWasPending));
    speech.chunkKeys = new Set();
    removeIdleLiveEmpty();
    if (!(rendered && rendered.el) && !existing) target.appendChild(el);
    if (typeof sortIdleLiveMessageNodes === 'function') sortIdleLiveMessageNodes(target);
    trimTimelineNodesFor(target, MAX_TIMELINE_NODES);
  } else {
    speech.el.classList.add('tts-current');
    speech.el.classList.toggle('shiro', id === 'shiro');
    speech.sessionId = sid;
    if (rid) speech.responseId = rid;
    speech.active = true;
  }
  if (speech.chunkKeys.has(key)) {
    return;
  }
  speech.chunkKeys.add(key);
  if (speech.textEl) {
    const current = String(speech.textEl.textContent || '');
    if (!speech.preRendered) {
      speech.textEl.textContent = appendCentralTTSText(current, normalizedText);
      speech.textEl.dataset.raw = speech.textEl.textContent;
    }
  }
  target.scrollTop = target.scrollHeight;
}

function resetCentralTTSSpeechBubble() {
  resetTTSSpeechBubble(centralTTSSpeech);
  resetTTSSpeechBubble(idleTTSSpeech);
}

function resetTTSSpeechBubble(speech) {
  if (speech.el) speech.el.classList.remove('tts-current');
  speech.active = false;
}

function shouldStartNewTTSBubble(speech, chunkIndex, key, responseId) {
  if (!speech.el) return true;
  if (!speech.textEl || !String(speech.textEl.textContent || '').trim()) return false;
  if (speech.chunkKeys.has(key)) return false;
  if (responseId && speech.responseId && responseId !== speech.responseId) return true;
  if (chunkIndex === 0) return true;
  if (!speech.active && chunkIndex < 1) return true;
  return false;
}

function appendCentralTTSText(current, next) {
  const left = String(current || '');
  const right = String(next || '').trim();
  if (!left) return right;
  if (!right) return left;
  if (/\s$/.test(left) || /^\s/.test(next)) return left + right;
  if (/[、]$/.test(left)) return left + right;
  if (/[「『（(［\[]$/.test(left) || /^[、。！？!?）」』）)\]］]/.test(right)) return left + right;
  if (isCJKBoundary(left, right)) return left + right;
  return left + ' ' + right;
}

function isCJKBoundary(left, right) {
  const l = String(left || '').trim().slice(-1);
  const r = String(right || '').trim().slice(0, 1);
  if (!l || !r) return false;
  return /[\u3040-\u30ff\u3400-\u9fff]/.test(l) && /[\u3040-\u30ff\u3400-\u9fff]/.test(r);
}

function ttsChunkIdentityKey(sessionId, utteranceId, chunkIndex, fallbackIndex) {
  const uid = String(utteranceId || '').trim();
  const normalizedChunkIndex = Number.isFinite(chunkIndex) ? chunkIndex : -1;
  if (uid && normalizedChunkIndex >= 0) return uid + ':chunk:' + String(normalizedChunkIndex);
  if (uid) return uid;
  const sid = String(sessionId || '').trim();
  const fallback = Number.isFinite(fallbackIndex) ? fallbackIndex : 0;
  return sid + ':' + String(normalizedChunkIndex >= 0 ? normalizedChunkIndex : fallback);
}

function ttsBubbleKind(speech, text, sessionId, chunkIndex, characterId) {
  const s = String(text || '').trim();
  if (/^(今日のお題|きょうのおだい)(です)?[、。:：！？!?]?/.test(s)) return 'topic';
  if (chunkIndex > 0 && speech.bubbleKind === 'topic' && speech.sessionId === String(sessionId || '').trim() && speech.characterId === String(characterId || '').trim().toLowerCase()) {
    return 'topic';
  }
  return 'speech';
}

let timelineAutoFollow = true;
let timelineUserInteracting = false;
let timelineInteractionTimer = null;
let suppressTimelineScroll = false;
let derivedDirty = false;
let activeViewerTab = 'home';
let sttControlsReady = false;

const tabs = Array.from(document.querySelectorAll('.tab-btn'));
const themeButtons = Array.from(document.querySelectorAll('.theme-btn'));
const mobilePanelSelect = document.getElementById('mobilePanelSelect');
const mobilePanelPrev = document.getElementById('mobilePanelPrev');
const mobilePanelNext = document.getElementById('mobilePanelNext');
const panels = {
  home: document.getElementById('panel-home'),
  develop: document.getElementById('panel-develop'),
  instructions: document.getElementById('panel-instructions'),
  backlog: document.getElementById('panel-backlog'),
  reports: document.getElementById('panel-reports'),
  ops: document.getElementById('panel-ops'),
  games: document.getElementById('panel-games'),
  overview: document.getElementById('panel-overview'),
  roles: document.getElementById('panel-roles'),
  progress: document.getElementById('panel-progress'),
  timeline: document.getElementById('panel-timeline'),
  system: document.getElementById('panel-system'),
  memory: document.getElementById('panel-memory'),
  'movie-db': document.getElementById('panel-movie-db'),
  'news-pack': document.getElementById('panel-news-pack'),
  investment: document.getElementById('panel-investment'),
  idlechat: document.getElementById('panel-idlechat'),
  sessions: document.getElementById('panel-sessions'),
  jobs: document.getElementById('panel-jobs'),
};

function applyViewerTheme(theme) {
  const selected = (theme === 'classic' || theme === 'compact') ? theme : 'modern';
  const body = document.body;
  if (body && body.classList) {
    body.classList.remove('theme-classic', 'theme-modern', 'theme-compact');
    body.classList.add('theme-' + selected);
  }
  themeButtons.forEach((btn) => btn.classList.toggle('active', btn.dataset.theme === selected));
  try { localStorage.setItem('viewer.theme', selected); } catch (_) {}
}

function savedViewerTheme() {
  try { return localStorage.getItem('viewer.theme') || 'modern'; }
  catch (_) { return 'modern'; }
}

applyViewerTheme(savedViewerTheme());
themeButtons.forEach((btn) => btn.addEventListener('click', () => applyViewerTheme(btn.dataset.theme)));

const fltType = document.getElementById('fltType');
const fltAgent = document.getElementById('fltAgent');
const fltRoute = document.getElementById('fltRoute');
const fltJob = document.getElementById('fltJob');
const fltText = document.getElementById('fltText');
const sysPreset = document.getElementById('sysPreset');
const sysType = document.getElementById('sysType');
const sysText = document.getElementById('sysText');
const memorySession = document.getElementById('memorySession');
const memoryNamespace = document.getElementById('memoryNamespace');
const memoryCategory = document.getElementById('memoryCategory');
const memoryDomain = document.getElementById('memoryDomain');
const memoryEventNamespace = document.getElementById('memoryEventNamespace');
const memoryPromoteKind = document.getElementById('memoryPromoteKind');
const memoryPromoteID = document.getElementById('memoryPromoteID');
const memoryRefreshBtn = document.getElementById('memoryRefreshBtn');
const roleFilter = document.getElementById('roleFilter');
const sourceRegistrySaveBtn = document.getElementById('sourceRegistrySaveBtn');
const sourceRegistryExportBtn = document.getElementById('sourceRegistryExportBtn');
const sourceRegistryImportBtn = document.getElementById('sourceRegistryImportBtn');
const sourceRegistryStagingRefreshBtn = document.getElementById('sourceRegistryStagingRefreshBtn');
const sourceRegistryYAML = document.getElementById('sourceRegistryYAML');
const domainGraphRefreshBtn = document.getElementById('domainGraphRefreshBtn');
const domainGraphDomain = document.getElementById('domainGraphDomain');
const domainGraphEntityType = document.getElementById('domainGraphEntityType');
const domainGraphSourceID = document.getElementById('domainGraphSourceID');
const domainGraphStatus = document.getElementById('domainGraphStatusFilter');
const newsPackCategory = document.getElementById('newsPackCategory');
const newsPackRefreshBtn = document.getElementById('newsPackRefreshBtn');
const idleStartBtn = document.getElementById('idleStart');
const idleModeNormalBtn = document.getElementById('idleModeNormal');
const idleModeForecastBtn = document.getElementById('idleModeForecast');
const idleModeStorySimpleBtn = document.getElementById('idleModeStorySimple');
const idleStopBtn = document.getElementById('idleStop');
const idleStateEl = document.getElementById('idleState');
const idleSubtabs = Array.from(document.querySelectorAll('.idle-subtab'));
const idleSubviews = Array.from(document.querySelectorAll('.idle-subview'));
const audioBtn = document.getElementById('audioBtn');
const liveAudioBtn = document.getElementById('liveAudioBtn');
const labAudioBtn = document.getElementById('labAudioBtn');
const eviStatus = document.getElementById('eviStatus');
const eviErrorKind = document.getElementById('eviErrorKind');
const eviPrev = document.getElementById('eviPrev');
const eviNext = document.getElementById('eviNext');
const eviPos = document.getElementById('eviPos');
const eviCopy = document.getElementById('eviCopy');
const eviCopySummary = document.getElementById('eviCopySummary');
const eviSort = document.getElementById('eviSort');

function switchTab(tab) {
  if (!panels[tab]) return;
  activeViewerTab = tab;
  document.body.dataset.viewerTab = tab;
  if (mainEl) mainEl.scrollTop = 0;
  tabs.forEach((b) => b.classList.toggle('active', b.dataset.tab === tab));
  Object.keys(panels).forEach((k) => panels[k].classList.toggle('active', k === tab));
  if (mobilePanelSelect && mobilePanelSelect.value !== tab) mobilePanelSelect.value = tab;
  const activeTab = tabs.find((b) => b.dataset.tab === tab);
  if (activeTab && typeof activeTab.scrollIntoView === 'function') {
    activeTab.scrollIntoView({block: 'nearest', inline: 'center'});
  }
  updateLatestButton();
  if (sttControlsReady) {
    updateSTTInputIndicators();
  }
  if (tab === 'timeline' && timelineAutoFollow) scrollToBottom(true);
  if (tab === 'investment' && typeof refreshInvestmentData === 'function') refreshInvestmentData();
  if (tab === 'backlog' && typeof refreshBacklog === 'function') refreshBacklog();
  if (tab === 'ops') {
    refreshSandboxData();
    refreshRuntimeBlockedRouteData();
    if (typeof refreshToBeOpsData === 'function') refreshToBeOpsData();
  }
  if (tab === 'games' && typeof refreshGameBridgeData === 'function') {
    refreshGameBridgeData();
  }
  if (tab === 'jobs') {
    refreshVerification();
    refreshVerificationSummary();
  }
  renderDeskViews();
}
tabs.forEach((btn) => btn.addEventListener('click', () => switchTab(btn.dataset.tab)));
document.body.dataset.viewerTab = activeViewerTab;

function switchAdjacentPanel(delta) {
  const names = tabs.map((btn) => btn.dataset.tab).filter((name) => panels[name]);
  if (!names.length) return;
  const current = names.includes(activeViewerTab) ? activeViewerTab : names[0];
  const nextIndex = (names.indexOf(current) + delta + names.length) % names.length;
  switchTab(names[nextIndex]);
}

if (mobilePanelSelect) {
  mobilePanelSelect.addEventListener('change', () => switchTab(mobilePanelSelect.value));
  mobilePanelSelect.value = activeViewerTab;
}
if (mobilePanelPrev) mobilePanelPrev.addEventListener('click', () => switchAdjacentPanel(-1));
if (mobilePanelNext) mobilePanelNext.addEventListener('click', () => switchAdjacentPanel(1));

function matchesFilters(ev) {
  if (isSystemEvent(ev)) return false;
  if (fltType.value && ev.type !== fltType.value) return false;
  if (fltAgent.value && ev.from !== fltAgent.value && ev.to !== fltAgent.value) return false;
  if (fltRoute.value && (ev.route || '') !== fltRoute.value) return false;
  if (fltJob.value && !(ev.job_id || '').toLowerCase().includes(fltJob.value.toLowerCase())) return false;
  if (fltText.value && !(ev.content || '').toLowerCase().includes(fltText.value.toLowerCase())) return false;
  return true;
}
function isSystemEvent(ev) {
  if (!ev) return false;
  if (ev.type === 'routing.decision' || ev.type === 'entry.stage' || ev.type === 'tts.audio_chunk') return true;
  if (ev.type === 'agent.delegate' || ev.type === 'agent.acknowledge' || ev.type === 'agent.report' || ev.type === 'worker.request' || ev.type === 'worker.result') return true;
  if (ev.type === 'agent.dispatch' || ev.type === 'mailbox.sent' || ev.type === 'mailbox.waiting' || ev.type === 'mailbox.received' || ev.type === 'mailbox.error' || ev.type === 'agent.error') return true;
  if (ev.type === 'agent.start' || ev.type === 'agent.note' || ev.type === 'agent.response') {
    const to = String(ev.to || '').toLowerCase();
    if (to !== 'user') return true;
  }
  if ((ev.from || '').toLowerCase() === 'system' || (ev.to || '').toLowerCase() === 'system') return true;
  if ((ev.from || '').toLowerCase() === 'tts') return true;
  return false;
}
function resetTimeline() {
  chat.innerHTML = '';
  Object.keys(thinkingBubbles).forEach((k) => delete thinkingBubbles[k]);
  msgCount = 0;
  state.logs.forEach((ev) => addMsgToTimeline(ev));
}
[fltType, fltAgent, fltRoute].forEach((el) => el.addEventListener('change', resetTimeline));
[fltJob, fltText].forEach((el) => el.addEventListener('input', resetTimeline));

function matchesSystemFilters(ev) {
  if (!isSystemEvent(ev)) return false;
  const preset = sysPreset ? sysPreset.value : 'all';
  if (preset === 'no-tts-audio' && ev.type === 'tts.audio_chunk') return false;
  if (preset === 'tts-only' && (ev.from || '').toLowerCase() !== 'tts' && ev.type !== 'tts.audio_chunk') return false;
  if (preset === 'tts-audio-only' && ev.type !== 'tts.audio_chunk') return false;
  if (sysType && sysType.value && ev.type !== sysType.value) return false;
  if (sysText && sysText.value && !(ev.content || '').toLowerCase().includes(sysText.value.toLowerCase())) return false;
  return true;
}


if (sysPreset) sysPreset.addEventListener('change', renderSystem);
if (sysType) sysType.addEventListener('change', renderSystem);
if (sysText) sysText.addEventListener('input', renderSystem);
if (roleFilter) roleFilter.addEventListener('change', renderRoleSelector);
if (memoryRefreshBtn) memoryRefreshBtn.addEventListener('click', refreshMemorySnapshot);
if (memorySession) memorySession.addEventListener('keydown', (e) => { if (e.key === 'Enter') refreshMemorySnapshot(); });
if (memoryNamespace) memoryNamespace.addEventListener('keydown', (e) => { if (e.key === 'Enter') refreshMemorySnapshot(); });
if (memoryCategory) memoryCategory.addEventListener('keydown', (e) => { if (e.key === 'Enter') refreshMemorySnapshot(); });
if (memoryDomain) memoryDomain.addEventListener('keydown', (e) => { if (e.key === 'Enter') refreshMemorySnapshot(); });
if (memoryEventNamespace) memoryEventNamespace.addEventListener('keydown', (e) => { if (e.key === 'Enter') refreshMemoryEvents(); });
if (sourceRegistrySaveBtn) sourceRegistrySaveBtn.addEventListener('click', saveSourceRegistryEntry);
if (sourceRegistryExportBtn) sourceRegistryExportBtn.addEventListener('click', exportSourceRegistryYAML);
if (sourceRegistryImportBtn) sourceRegistryImportBtn.addEventListener('click', importSourceRegistryYAML);
if (sourceRegistryStagingRefreshBtn) sourceRegistryStagingRefreshBtn.addEventListener('click', refreshSourceRegistryStaging);
if (domainGraphRefreshBtn) domainGraphRefreshBtn.addEventListener('click', refreshDomainGraphAssertions);
if (domainGraphDomain) domainGraphDomain.addEventListener('keydown', (e) => { if (e.key === 'Enter') refreshDomainGraphAssertions(); });
if (domainGraphEntityType) domainGraphEntityType.addEventListener('keydown', (e) => { if (e.key === 'Enter') refreshDomainGraphAssertions(); });
if (domainGraphSourceID) domainGraphSourceID.addEventListener('keydown', (e) => { if (e.key === 'Enter') refreshDomainGraphAssertions(); });
if (domainGraphStatus) domainGraphStatus.addEventListener('change', refreshDomainGraphAssertions);
if (newsPackRefreshBtn) newsPackRefreshBtn.addEventListener('click', refreshNewsPack);
if (newsPackCategory) newsPackCategory.addEventListener('keydown', (e) => { if (e.key === 'Enter') refreshNewsPack(); });


idleStartBtn.addEventListener('click', () => {
  const path = state.idleChat.selectedMode === 'forecast'
    ? '/viewer/idlechat/forecast'
    : (state.idleChat.selectedMode === 'story-simple'
      ? '/viewer/idlechat/story-simple'
      : '/viewer/idlechat/start');
  controlIdle(path);
});
if (idleModeNormalBtn) idleModeNormalBtn.addEventListener('click', () => setIdleSelectedMode('manual'));
if (idleModeForecastBtn) idleModeForecastBtn.addEventListener('click', () => setIdleSelectedMode('forecast'));
if (idleModeStorySimpleBtn) idleModeStorySimpleBtn.addEventListener('click', () => setIdleSelectedMode('story-simple'));
idleStopBtn.addEventListener('click', () => controlIdle('/viewer/idlechat/stop'));
idleSubtabs.forEach((btn) => {
  btn.addEventListener('click', () => setIdleSelectedView(btn.dataset.idleView || 'live'));
});

function stateClass(s) { return 'state-' + normState(s); }

function num(v) {
  const n = Number(v);
  return Number.isFinite(n) ? n : 0;
}

function pct(part, total) {
  const p = num(part);
  const t = num(total);
  if (t <= 0) return 0;
  return Math.max(0, Math.min(100, (p / t) * 100));
}

function fmtGiB(value) {
  const n = num(value);
  if (n <= 0) return '-';
  return n.toFixed(n >= 10 ? 1 : 2) + ' GiB';
}

function fmtGiBFromMiB(value) {
  const n = num(value);
  if (n <= 0) return '-';
  return fmtGiB(n / 1024);
}

function fmtBytesAsGiB(bytes) {
  const n = num(bytes);
  if (n <= 0) return '-';
  return fmtGiB(n / 1073741824);
}

function bump() {
  msgCount++;
  cnt.textContent = msgCount;
  ctr.style.display = 'flex';
  scrollToBottom();
}

function pushDebugTrace(kind, payload) {
  const bucket = kind === 'stt' ? state.debug.sttTrace : state.debug.thinkTrace;
  bucket.push(payload);
  if (bucket.length > 40) bucket.shift();
}

function nowLatencyMS() {
  if (typeof performance !== 'undefined' && typeof performance.now === 'function' && performance.timeOrigin) {
    return performance.timeOrigin + performance.now();
  }
  return Date.now();
}

function recordLatencyMetric(kind, point, options = {}) {
  const normalizedKind = String(kind || '').trim().toLowerCase();
  const normalizedPoint = String(point || '').trim();
  if (!normalizedKind || !normalizedPoint) return null;
  const atMS = Number.isFinite(Number(options.atMS)) ? Number(options.atMS) : nowLatencyMS();
  const valueMS = Number.isFinite(Number(options.valueMS)) ? Number(options.valueMS) : NaN;
  const elapsedMS = Number.isFinite(Number(options.elapsedMS)) ? Number(options.elapsedMS) : valueMS;
  const item = {
    kind: normalizedKind,
    point: normalizedPoint,
    time: ftime(new Date(atMS).toISOString()),
    atMS,
    valueMS: Number.isFinite(valueMS) ? valueMS : NaN,
    elapsedMS: Number.isFinite(elapsedMS) ? elapsedMS : NaN,
    detail: String(options.detail || '').trim(),
    route: String(options.route || '').trim(),
    job: String(options.job || '').trim(),
    session: String(options.session || '').trim(),
    source: String(options.source || 'viewer').trim(),
  };
  state.debug.latencyMetrics.push(item);
  if (state.debug.latencyMetrics.length > 120) state.debug.latencyMetrics.shift();
  state.debug.latencyLatest[normalizedKind + ':' + normalizedPoint] = item;
  return item;
}

function recordLatencyMetricOnce(key, kind, point, options = {}) {
  const normalizedKey = String(key || '').trim();
  if (!normalizedKey) return recordLatencyMetric(kind, point, options);
  if (state.debug.latencySeen[normalizedKey]) return null;
  state.debug.latencySeen[normalizedKey] = true;
  return recordLatencyMetric(kind, point, options);
}

function parseLatencyMetricContent(content) {
  try {
    const payload = JSON.parse(String(content || '{}'));
    if (!payload || typeof payload !== 'object') return null;
    return payload;
  } catch (_) {
    return null;
  }
}

function ingestLatencyMetricEvent(ev) {
  const payload = parseLatencyMetricContent(ev && ev.content);
  if (!payload) return;
  recordLatencyMetric(payload.kind, payload.point, {
    atMS: Number(payload.at_unix_ms || 0),
    elapsedMS: Number(payload.elapsed_ms),
    valueMS: Number(payload.since_ms !== undefined ? payload.since_ms : payload.elapsed_ms),
    detail: payload.detail || '',
    route: ev.route || '',
    job: ev.job_id || '',
    session: ev.session_id || '',
    source: 'server',
  });
  const emittedAt = Number(payload.at_unix_ms || 0);
  if (emittedAt > 0) {
    recordLatencyMetric('network', 'sse_metric_receive_lag', {
      valueMS: Math.max(0, nowLatencyMS() - emittedAt),
      detail: String(payload.kind || '-') + '/' + String(payload.point || '-'),
      route: ev.route || '',
      job: ev.job_id || '',
      session: ev.session_id || '',
    });
  }
}

function noteViewerEventLatency(ev, receivedMS) {
  if (!ev || !ev.type) return;
  if (ev.type === 'metrics.latency') {
    ingestLatencyMetricEvent(ev);
    return;
  }
  const job = String(ev.job_id || '').trim();
  const eventType = String(ev.type || '').trim();
  if (eventType === 'message.received') {
    const key = 'sse-message:' + String(ev.seq || ev.timestamp || receivedMS);
    recordLatencyMetricOnce(key, 'network', 'sse_message_received', {
      atMS: receivedMS,
      detail: short(ev.content || '', 80),
      route: ev.route || '',
      job,
      session: ev.session_id || '',
    });
    return;
  }
  if (eventType === 'agent.thinking') {
    const key = 'llm-thinking:' + (job || ev.session_id || 'unknown');
    recordLatencyMetricOnce(key, 'llm', 'agent_thinking_received', {
      atMS: receivedMS,
      detail: short(ev.content || '', 80),
      route: ev.route || '',
      job,
      session: ev.session_id || '',
    });
    return;
  }
  if (eventType === 'agent.response') {
    const key = 'llm-response:' + (job || ev.message_id || receivedMS);
    recordLatencyMetricOnce(key, 'llm', 'agent_response_received', {
      atMS: receivedMS,
      detail: 'len=' + String(String(ev.content || '').length),
      route: ev.route || '',
      job,
      session: ev.session_id || '',
    });
    return;
  }
  if (eventType === 'tts.audio_chunk') {
    const key = 'tts-chunk-received:' + String(ev.seq || ev.content || receivedMS);
    recordLatencyMetricOnce(key, 'tts', 'audio_chunk_received', {
      atMS: receivedMS,
      detail: 'sse audio chunk',
      route: ev.route || '',
      job,
      session: ev.session_id || '',
    });
  }
}

function formatLatencyMS(value) {
  const n = Number(value);
  if (!Number.isFinite(n)) return '-';
  if (n >= 1000) return (n / 1000).toFixed(n >= 10000 ? 1 : 2) + 's';
  return Math.round(n) + 'ms';
}

function latencyKindLabel(kind) {
  const value = String(kind || '').toLowerCase();
  if (value === 'llm') return 'LLM';
  if (value === 'tts') return 'TTS';
  if (value === 'stt') return 'STT';
  if (value === 'network') return 'Network';
  return value || '-';
}

function renderLatencySummary(el) {
  if (!el) return;
  const items = state.debug.latencyMetrics.slice(-24).reverse();
  if (items.length === 0) {
    el.innerHTML = '<div class="debug-empty">まだ速度計測イベントがありません</div>';
    return;
  }
  el.innerHTML = items.map((item) => {
    const value = Number.isFinite(Number(item.valueMS)) ? item.valueMS : item.elapsedMS;
    const detail = item.detail ? ' · ' + esc(item.detail) : '';
    const meta = esc(item.time || '-') + ' · ' + esc(latencyKindLabel(item.kind)) + ' · ' + esc(item.point || '-') + ' · ' + esc(item.source || 'viewer');
    return '<div class="debug-item">' +
      '<div class="debug-meta">' + meta + '</div>' +
      '<div><span class="badge state-running">' + esc(formatLatencyMS(value)) + '</span>' + detail + '</div>' +
    '</div>';
  }).join('');
}

function renderDebugPanels() {
  const gpuEl = document.getElementById('debugGpuSummary');
  const latencyEl = document.getElementById('debugLatencySummary');
  const sttEl = document.getElementById('debugSttTrace');
  const thinkEl = document.getElementById('debugThinkTrace');
  if (!gpuEl || !sttEl || !thinkEl) return;

  const g = state.debug.gpu;
  const a = state.debug.audio;
  if (!g || !g.available) {
    const note = g && g.note ? g.note : 'GPU情報を取得できません';
    gpuEl.innerHTML = '<span class="badge state-offline">unavailable</span> ' + esc(note);
  } else {
    gpuEl.innerHTML =
      '<div class="row"><span>total/used/free</span><span>' + esc(String(g.total_mb || 0)) + ' / ' + esc(String(g.used_mb || 0)) + ' / ' + esc(String(g.free_mb || 0)) + ' MB</span></div>' +
      '<div class="row"><span>LLM</span><span class="badge state-running">' + esc(String(g.llm_used_mb || 0)) + ' MB</span></div>' +
      '<div class="row"><span>STT</span><span class="badge state-thinking">' + esc(String(g.stt_used_mb || 0)) + ' MB</span></div>' +
      '<div class="row"><span>TTS</span><span class="badge state-idle">' + esc(String(g.tts_used_mb || 0)) + ' MB</span></div>' +
      '<div class="row"><span>other</span><span class="badge state-offline">' + esc(String(g.other_used_mb || 0)) + ' MB</span></div>';
  }
  if (a) {
    gpuEl.innerHTML +=
      '<div class="row"><span>STT health</span><span class="badge ' + (a.stt_ok ? 'state-idle' : 'state-error') + '">' + (a.stt_ok ? 'ok' : 'ng') + '</span></div>' +
      '<div class="ops-sub">stt=' + esc(a.stt_base_url || '-') + '\n' + esc(a.stt_health || '-') + '</div>' +
      '<div class="row"><span>TTS live/ready</span><span class="badge ' + ((a.tts_live_ok && a.tts_ready_ok) ? 'state-idle' : 'state-error') + '">' + ((a.tts_live_ok ? 'live:ok' : 'live:ng') + ' / ' + (a.tts_ready_ok ? 'ready:ok' : 'ready:ng')) + '</span></div>' +
      '<div class="ops-sub">tts=' + esc(a.tts_base_url || '-') + '\n/live ' + esc(a.tts_live || '-') + '\n/ready ' + esc(a.tts_ready || '-') + '</div>';
    if (a.last_error) {
      gpuEl.innerHTML += '<div class="ops-sub">error: ' + esc(a.last_error) + '</div>';
    }
  }

  if (typeof renderLatencySummary === 'function') renderLatencySummary(latencyEl);

  const sttList = state.debug.sttTrace.slice().reverse();
  if (sttList.length === 0) {
    sttEl.innerHTML = '<div class="debug-empty">まだSTTイベントがありません</div>';
  } else {
    sttEl.innerHTML = sttList.map((item) => (
      '<div class="debug-item">' +
        '<div class="debug-meta">' + esc(item.time || '-') + ' · ' + esc(item.step || '-') + '</div>' +
        '<div>' + esc(item.text || '-') + '</div>' +
      '</div>'
    )).join('');
  }

  const thinkList = state.debug.thinkTrace.slice().reverse();
  if (thinkList.length === 0) {
    thinkEl.innerHTML = '<div class="debug-empty">まだthinkingイベントがありません</div>';
  } else {
    thinkEl.innerHTML = thinkList.map((item) => (
      '<div class="debug-item">' +
        '<div class="debug-meta">' + esc(item.time || '-') + ' · ' + esc(item.agent || '-') + ' · ' + esc(item.job || '-') + '</div>' +
        '<div>' + esc(item.text || '-') + '</div>' +
      '</div>'
    )).join('');
  }
}

function refreshDebugSystem() {
  fetch('/viewer/debug/system')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'debug system fetch failed'));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.debug.gpu = data && data.gpu ? data.gpu : null;
      state.debug.audio = data && data.audio ? data.audio : null;
      renderDebugPanels();
    })
    .catch((err) => {
      console.error(err);
      state.debug.gpu = {available: false, note: String(err && err.message ? err.message : err)};
      state.debug.audio = null;
      renderDebugPanels();
    });
}

function trimTimelineNodes() {
  trimTimelineNodesFor(chat, MAX_TIMELINE_NODES);
}

function trimTimelineNodesFor(container, maxNodes) {
  if (!container) return;
  while (container.childElementCount > maxNodes) {
    const first = container.firstElementChild;
    if (!first) break;
    first.remove();
  }
}


function isTTSSyncedSpeaker(agentID) {
  const id = String(agentID || '').trim().toLowerCase();
  return id === 'mio' || id === 'shiro';
}

function thinkingDisplayAgentID(ev) {
  if (!ev) return '';
  if (ev.type === 'agent.start' && String(ev.to || '').trim()) return String(ev.to || '').trim().toLowerCase();
  return String(ev.from || '').trim().toLowerCase();
}

function shouldRenderThinking(ev) {
  const id = thinkingDisplayAgentID(ev);
  return id !== '' && id !== 'mio' && id !== 'chat';
}

function matchesThinkingFilters(ev) {
  const id = thinkingDisplayAgentID(ev);
  if (fltType.value && ev.type !== fltType.value) return false;
  if (fltAgent.value && id !== fltAgent.value && ev.from !== fltAgent.value && ev.to !== fltAgent.value) return false;
  if (fltRoute.value && (ev.route || '') !== fltRoute.value) return false;
  if (fltJob.value && !(ev.job_id || '').toLowerCase().includes(fltJob.value.toLowerCase())) return false;
  if (fltText.value && !(ev.content || '').toLowerCase().includes(fltText.value.toLowerCase())) return false;
  return true;
}

function renderThinkingDots(el) {
  if (!el) return;
  el.innerHTML = '<span class="dots" aria-label="Thinking"><span></span><span></span><span></span></span>';
}

function addThinkingStart(ev) {
  if (!matchesThinkingFilters(ev)) return;
  if (!shouldRenderThinking(ev)) return;
  const jid = ev.job_id || '_';
  if (thinkingBubbles[jid]) return;
  const f = ag(thinkingDisplayAgentID(ev));
  const el = document.createElement('div');
  el.className = 'msg thinking';
  const textEl = document.createElement('div');
  textEl.className = 'mc';
  renderThinkingDots(textEl);
  el.innerHTML =
    '<div class="av" style="background:' + f.c + '18;color:' + f.c + '">' + f.e + '</div>' +
    '<div class="mb"><div class="mh">' +
      '<span class="an" style="color:' + f.c + '">' + f.l + '</span>' +
      '<span class="tm">' + ftime(ev.timestamp) + '</span>' +
    '</div></div>';
  el.querySelector('.mb').appendChild(textEl);
  chat.appendChild(el);
  trimTimelineNodes();
  thinkingBubbles[jid] = {el: el, textEl: textEl, raw: '', waiting: true};
  scrollToBottom();
}

function addThinking(ev) {
  if (!matchesThinkingFilters(ev)) return;
  if (!shouldRenderThinking(ev)) return;
  const jid = ev.job_id || '_';
  let b = thinkingBubbles[jid];
  if (!b) {
    const f = ag(thinkingDisplayAgentID(ev));
    const el = document.createElement('div');
    el.className = 'msg thinking';
    const textEl = document.createElement('div');
    textEl.className = 'mc';
    renderThinkingDots(textEl);
    el.innerHTML =
      '<div class="av" style="background:' + f.c + '18;color:' + f.c + '">' + f.e + '</div>' +
      '<div class="mb"><div class="mh">' +
        '<span class="an" style="color:' + f.c + '">' + f.l + '</span>' +
        '<span class="tm">' + ftime(ev.timestamp) + '</span>' +
      '</div></div>';
    el.querySelector('.mb').appendChild(textEl);
    chat.appendChild(el);
    trimTimelineNodes();
    b = {el: el, textEl: textEl, raw: '', waiting: false};
    thinkingBubbles[jid] = b;
  }
  b.raw += normalizeViewerDisplayText(ev.content || '');
  renderThinkingDots(b.textEl);
  scrollToBottom();
}

function removeThinking(jid) {
  const key = jid || '_';
  const b = thinkingBubbles[key];
  if (!b) return;
  b.el.remove();
  delete thinkingBubbles[key];
}

function addSys(ev) {
  const el = document.createElement('div');
  el.className = 'sys';
  const rc = RC[ev.route] || '#94a3b8';
  const displayContent = normalizeViewerDisplayText(ev.content || '');
  el.innerHTML =
    '<div class="sl"></div>' +
    '<div class="sb">⚙️ <span class="rt" style="color:' + rc + '">' +
      esc(ev.route || '?') + '</span> ' + esc(displayContent) + '</div>' +
    '<div class="sl"></div>';
  chat.appendChild(el);
  trimTimelineNodes();
  bump();
}

function isTimelineActive() {
  return !!(panels.timeline && panels.timeline.classList.contains('active'));
}

function isTimelineNearBottom() {
  const target = chat || mainEl;
  return (target.scrollHeight - target.scrollTop - target.clientHeight) <= 120;
}

function updateLatestButton() {
  if (!latestBtn) return;
  latestBtn.classList.toggle('show', isTimelineActive() && !timelineAutoFollow);
}

function noteTimelineInteraction() {
  if (!isTimelineActive()) return;
  timelineUserInteracting = true;
  if (timelineInteractionTimer) clearTimeout(timelineInteractionTimer);
  timelineInteractionTimer = setTimeout(() => {
    timelineUserInteracting = false;
  }, 1200);
}

function setTimelineAutoFollow(enabled) {
  timelineAutoFollow = !!enabled;
  updateLatestButton();
}

mainEl.addEventListener('wheel', noteTimelineInteraction, {passive:true});
mainEl.addEventListener('touchstart', noteTimelineInteraction, {passive:true});
mainEl.addEventListener('touchmove', noteTimelineInteraction, {passive:true});
mainEl.addEventListener('pointerdown', noteTimelineInteraction, {passive:true});
mainEl.addEventListener('scroll', () => {
  if (!isTimelineActive() || suppressTimelineScroll) return;
  const nearBottom = isTimelineNearBottom();
  if (timelineUserInteracting && !nearBottom) {
    setTimelineAutoFollow(false);
    return;
  }
  if (nearBottom) setTimelineAutoFollow(true);
});

function scrollToBottom(force) {
  if (!isTimelineActive()) return;
  if (!force && !timelineAutoFollow) return;
  suppressTimelineScroll = true;
  if (chat) chat.scrollTop = chat.scrollHeight;
  if (mainEl) mainEl.scrollTop = mainEl.scrollHeight;
  requestAnimationFrame(() => {
    if (chat) chat.scrollTop = chat.scrollHeight;
    if (mainEl) mainEl.scrollTop = mainEl.scrollHeight;
    suppressTimelineScroll = false;
    if (isTimelineNearBottom()) setTimelineAutoFollow(true);
  });
}

if (latestBtn) {
  latestBtn.addEventListener('click', () => {
    setTimelineAutoFollow(true);
    scrollToBottom(true);
  });
}

function copyMsg(btn) {
  const mc = btn.parentElement.querySelector('.mc');
  const text = mc.dataset.raw || mc.textContent;
  writeClipboardText(text).then(() => {
    btn.textContent = 'OK';
    btn.title = '';
    btn.classList.add('ok');
    setTimeout(() => { btn.textContent = 'Copy'; btn.classList.remove('ok'); }, 1200);
  }).catch((err) => {
    console.error(err);
    const message = 'Copy unavailable: ' + String(err && err.message ? err.message : err);
    btn.textContent = message;
    btn.title = message;
    showToast('Copy failed', 'error');
  });
}
window.copyMsg = copyMsg;

function escAttr(s) {
  return String(s || '')
    .replace(/&/g, '&amp;')
    .replace(/"/g, '&quot;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
}

function writeClipboardText(text) {
  const value = String(text || '');
  if (navigator.clipboard && window.isSecureContext) {
    return navigator.clipboard.writeText(value);
  }
  return new Promise((resolve, reject) => {
    const ta = document.createElement('textarea');
    ta.value = value;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.top = '-1000px';
    ta.style.left = '-1000px';
    document.body.appendChild(ta);
    ta.focus();
    ta.select();
    let ok = false;
    try {
      ok = document.execCommand('copy');
    } catch (err) {
      document.body.removeChild(ta);
      reject(err);
      return;
    }
    document.body.removeChild(ta);
    if (ok) resolve();
    else reject(new Error('copy command failed'));
  });
}

function copyTextPayload(btn, payload) {
  writeClipboardText(String(payload || '')).then(() => {
    const old = btn.textContent;
    btn.textContent = 'Copied';
    btn.title = '';
    btn.classList.add('ok');
    showToast('Copied to clipboard', 'success');
    setTimeout(() => {
      btn.textContent = old;
      btn.classList.remove('ok');
    }, 1200);
  }).catch((err) => {
    console.error(err);
    const message = 'Copy unavailable: ' + String(err && err.message ? err.message : err);
    btn.textContent = message;
    btn.title = message;
    showToast('Copy failed', 'error');
  });
}
window.copyTextPayload = copyTextPayload;

function upsertSession(ev) {
  const sid = ev.session_id || 'viewer';
  let s = state.sessions[sid];
  if (!s) {
    s = {id: sid, channel: '-', chatID: '-', count: 0, lastRoute: '-', lastUserMessage: '-', agents: {}, updatedAt: ''};
    state.sessions[sid] = s;
  }
  s.count++;
  if (ev.channel) s.channel = ev.channel;
  if (ev.chat_id) s.chatID = ev.chat_id;
  if (ev.route) s.lastRoute = ev.route;
  if (ev.from === 'user' && ev.content) s.lastUserMessage = ev.content;
  if (ev.from) s.agents[ev.from] = true;
  if (ev.to) s.agents[ev.to] = true;
  s.updatedAt = ev.timestamp;
}

function upsertJob(ev) {
  const jid = ev.job_id || '-';
  if (jid === '-') return;
  let j = state.jobs[jid];
  if (!j) {
    j = {id: jid, route: '-', status: 'running', from: '-', to: '-', startedAt: ev.timestamp, updatedAt: ev.timestamp, events: 0, preview: ''};
    state.jobs[jid] = j;
  }
  j.events++;
  j.updatedAt = ev.timestamp;
  if (ev.route) j.route = ev.route;
  if (ev.from) j.from = ev.from;
  if (ev.to) j.to = ev.to;
  if (ev.content) j.preview = ev.content;
  if (ev.type === 'agent.response') {
    const c = (ev.content || '').toLowerCase();
    j.status = (c.includes('error') || c.includes('失敗')) ? 'error' : 'done';
  }
}

function touchAgent(agentID, patch) {
  if (!state.agents[agentID]) return;
  Object.assign(state.agents[agentID], patch);
}

function applyMonitorStatusSnapshot(payload) {
  const status = payload && payload.status ? payload.status : payload;
  if (!status) return;
  state.viewerStatusFetchError = '';
  const items = [];
  if (status.chat) items.push(status.chat);
  if (status.worker) items.push(status.worker);
  if (status.coders && Array.isArray(status.coders.items)) {
    status.coders.items.forEach((item) => items.push(item));
  }
  items.forEach((item) => {
    const id = String(item.id || item.agent_id || '').toLowerCase();
    if (!AGENTS.includes(id)) return;
    touchAgent(id, {
      state: item.state || item.status || state.agents[id].state,
      reason: item.reason || '',
      route: item.route || '-',
      lastEvent: item.last_event || '-',
      preview: item.preview || '-',
      updatedAt: item.updated_at || '',
      jobID: item.job_id || '-',
    });
  });
  renderOverview();
  renderRoleSelector();
  renderProgress();
}

function addOpenTask(owner, ev) {
  if (!AGENTS.includes(owner)) return;
  const jid = ev.job_id || '';
  if (!jid) return;
  state.openTasks[owner][jid] = {
    jobID: jid,
    route: ev.route || '-',
    text: short(ev.content || '-', 80),
    updatedAt: ev.timestamp || new Date().toISOString(),
  };
}

function doneOpenTask(owner, jid) {
  if (!AGENTS.includes(owner) || !jid) return;
  delete state.openTasks[owner][jid];
}

function openTaskSummary(agentID) {
  const m = state.openTasks[agentID] || {};
  const list = Object.values(m).sort((a, b) => (b.updatedAt || '').localeCompare(a.updatedAt || ''));
  if (list.length === 0) return '-';
  if (list.length === 1) return list[0].text || list[0].route || list[0].jobID;
  return short((list[0].text || list[0].route || list[0].jobID) + ' / +' + String(list.length - 1), 90);
}

function updateAgents(ev) {
  const ts = ev.timestamp || new Date().toISOString();
  const route = ev.route || '-';
  const jid = ev.job_id || '-';

  if (ev.type === 'message.received') {
    touchAgent('mio', {state: 'running', route, lastEvent: ev.type, peer: ev.from || '-', preview: short(ev.content, 80), updatedAt: ts, jobID: jid});
    return;
  }
  if (ev.type === 'routing.decision') {
    touchAgent('mio', {state: 'running', reason: '', route, lastEvent: ev.type, peer: '-', preview: short(ev.content, 80), updatedAt: ts, jobID: jid});
    return;
  }

  const from = (ev.from || '').toLowerCase();
  const to = (ev.to || '').toLowerCase();

  if (AGENTS.includes(from)) {
    let s = 'running';
    if (ev.type === 'agent.thinking') s = 'thinking';
    if (ev.type === 'agent.response') {
      const c = (ev.content || '').toLowerCase();
      s = (c.includes('error') || c.includes('失敗')) ? 'error' : 'idle';
    }
    touchAgent(from, {
      state: s,
      reason: '',
      route,
      lastEvent: ev.type,
      peer: to || '-',
      preview: short(ev.content, 80),
      updatedAt: ts,
      jobID: jid,
    });

    // Update character expression based on agent state
    if (from === 'mio' || from === 'shiro') {
      let characterState = 'idle';
      if (ev.type === 'agent.thinking') {
        characterState = 'thinking';
      } else if (ev.type === 'agent.response') {
        const c = (ev.content || '').toLowerCase();
        if (c.includes('error') || c.includes('失敗')) {
          characterState = 'error';
        } else if (c.includes('完了') || c.includes('成功') || c.includes('できました')) {
          characterState = 'happy';
        } else {
          characterState = 'idle';
        }
      }
      setCharacterExpression(from, characterState);
    }
  }

  if (ev.type === 'agent.start' && AGENTS.includes(to)) {
    addOpenTask(to, ev);
    touchAgent(to, {
      state: 'running',
      reason: '',
      route,
      lastEvent: ev.type,
      peer: from || '-',
      preview: short(ev.content, 80),
      updatedAt: ts,
      jobID: jid,
    });
  }
  if (ev.type === 'agent.response' && AGENTS.includes(from)) {
    doneOpenTask(from, jid);
  }
  if (ev.type === 'agent.response' && to === 'mio') {
    touchAgent('mio', {
      state: 'idle',
      reason: '',
      route,
      lastEvent: ev.type,
      peer: from || '-',
      preview: short(ev.content, 80),
      updatedAt: ts,
      jobID: jid,
    });
  }
}


function renderEvidence() {
  const body = document.getElementById('evidenceBody');
  body.innerHTML = '';
  const fetchError = evidenceFetchErrorMessage();
  if (fetchError) {
    state.evidenceOrder = [];
    if (state.selectedEvidenceJobID) {
      state.selectedEvidenceJobID = '';
      state.selectedEvidenceItem = null;
      syncEvidenceQuery('');
    }
    const detail = document.getElementById('evidenceDetail');
    if (detail) detail.textContent = 'No selection';
    updateEvidenceNav();
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="11" class="small">Evidence / verification unavailable: ' + esc(fetchError) + '</td>';
    body.appendChild(tr);
    return;
  }
  const statusFilter = (eviStatus && eviStatus.value) ? eviStatus.value : '';
  const kindFilter = (eviErrorKind && eviErrorKind.value) ? eviErrorKind.value : '';
  const list = combinedEvidenceList().filter((r) => {
    if (statusFilter && String(r.status || '') !== statusFilter) return false;
    if (kindFilter && String(r.error_kind || '') !== kindFilter) return false;
    return true;
  }).sort((a, b) => {
    const ta = Date.parse(a.finished_at || a.created_at || 0) || 0;
    const tb = Date.parse(b.finished_at || b.created_at || 0) || 0;
    return state.evidenceSortDesc ? (tb - ta) : (ta - tb);
  });
  state.evidenceOrder = list.map((r) => String(r.job_id || '')).filter((id) => id !== '');
  if (state.selectedEvidenceJobID && state.evidenceOrder.indexOf(state.selectedEvidenceJobID) < 0) {
    state.selectedEvidenceJobID = '';
    state.selectedEvidenceItem = null;
    syncEvidenceQuery('');
    const detail = document.getElementById('evidenceDetail');
    if (detail) detail.textContent = 'No selection';
  }
  updateEvidenceNav();

  if (list.length === 0) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="11" class="small">No execution evidence yet</td>';
    body.appendChild(tr);
    return;
  }

  list.forEach((r) => {
    const isVerificationReport = r._kind === 'verification_report';
    const st = isVerificationReport ? verificationStatusClass(r.status) : ((r.status === 'failed') ? 'error' : (r.status === 'passed' ? 'idle' : 'running'));
    const ek = String(r.error_kind || '');
    const stepsCount = Array.isArray(r.steps) ? r.steps.length : 0;
    const verifyCount = isVerificationReport ? Number(r.claim_count || 0) : (Array.isArray(r.verification) ? r.verification.length : 0);
    const latestVerify = isVerificationReport ? latestVerificationReportLink(r.job_id || '', r.status) : latestVerificationLink(r.job_id || '', r.verification);
    const tr = document.createElement('tr');
    if ((r.job_id || '') === (state.selectedEvidenceJobID || '')) tr.classList.add('evi-selected');
    tr.innerHTML =
      '<td class="code">' + esc((isVerificationReport ? 'verification_report:' : 'execution_report:') + (r.job_id || '-')) + '</td>' +
      '<td class="code">' + esc(r.job_id || '-') + '</td>' +
      '<td><span class="badge ' + stateClass(st) + '">' + esc(r.status || '-') + '</span></td>' +
      '<td><span class="badge ' + (isVerificationReport ? 'state-thinking' : errorKindClass(ek)) + '">' + esc(ek || r.trigger_level || '-') + '</span></td>' +
      '<td>' + latestVerify + '</td>' +
      '<td><button class="ctl-btn" onclick="openEvidenceWithFocus(\'' + esc(r.job_id || '') + '\', \'steps\', event)">' + esc(String(stepsCount)) + '</button></td>' +
      '<td><button class="ctl-btn" onclick="openEvidenceWithFocus(\'' + esc(r.job_id || '') + '\', \'verification\', event)">' + esc(String(verifyCount)) + '</button></td>' +
      '<td><button class="ctl-btn" onclick="openEvidenceWithFocus(\'' + esc(r.job_id || '') + '\', \'\', event)">' + esc(short(r.goal || r.route || '-', 90)) + '</button></td>' +
      '<td>' + esc(String(r.attempt_count || 0)) + '</td>' +
      '<td>' + esc(String(r.repair_count || 0)) + '</td>' +
      '<td>' + esc(fdt(r.finished_at)) + '</td>' +
      '<td><button class="ctl-btn" onclick="openEvidence(\'' + esc(r.job_id || '') + '\')">View</button></td>';
    tr.style.cursor = 'pointer';
    tr.addEventListener('click', function(evt) {
      const t = evt.target;
      if (t && t.tagName === 'BUTTON') return;
      openEvidence(r.job_id || '');
    });
    body.appendChild(tr);
  });
}

function combinedEvidenceList() {
  const out = Array.isArray(state.evidence) ? state.evidence.slice() : [];
  const seenJobs = new Set(out.map((r) => String(r.job_id || '')).filter((id) => id !== ''));
  (state.verificationReports || []).forEach((r) => {
    const jobID = String(r.job_id || '');
    if (!jobID || seenJobs.has(jobID)) return;
    out.push(Object.assign({_kind: 'verification_report'}, r));
  });
  return out;
}

function evidenceFetchErrorMessage() {
  const parts = [];
  if (state.evidenceFetchError) parts.push('evidence: ' + state.evidenceFetchError);
  if (state.verificationFetchError) parts.push('verification: ' + state.verificationFetchError);
  return parts.join('; ');
}

function evidenceSummaryFetchErrorMessage() {
  const parts = [];
  if (state.evidenceSummaryFetchError) parts.push('evidence summary: ' + state.evidenceSummaryFetchError);
  if (state.verificationSummaryFetchError) parts.push('verification summary: ' + state.verificationSummaryFetchError);
  return parts.join('; ');
}

function renderEvidenceSummary() {
  const root = document.getElementById('evidenceSummaryCards');
  if (!root) return;
  const fetchError = evidenceSummaryFetchErrorMessage();
  if (fetchError) {
    root.innerHTML = '' +
      '<div class="card"><h4>Evidence Total</h4><div style="font-size:22px;font-weight:700">unavailable</div><div class="small">evidence summary unavailable: ' + esc(fetchError) + '</div></div>' +
      '<div class="card"><h4>Verification Reports</h4><div style="font-size:22px;font-weight:700">unavailable</div><div class="small">verification summary unavailable: ' + esc(fetchError) + '</div><div class="small">blocked: execution evidence state unreadable</div></div>';
    return;
  }
  const s = state.evidenceSummary || {};
  const st = s.status || {};
  const ek = s.error_kind || {};
  const vs = (state.verificationSummary || {}).status || {};
  const vl = (state.verificationSummary || {}).trigger_level || {};
  const total = (st.passed || 0) + (st.failed || 0) + (st.other || 0);
  const verifyTotal = (vs.verified || 0) + (vs.weakly_supported || 0) + (vs.unsupported || 0) + (vs.conflict || 0) + (vs.not_checked || 0);
  root.innerHTML = '' +
    '<div class="card"><h4>Evidence Total</h4><div style="font-size:22px;font-weight:700">' + esc(String(total)) + '</div></div>' +
    '<div class="card"><h4>Status</h4>' +
      '<div class="row"><span>passed</span><span class="badge state-idle">' + esc(String(st.passed || 0)) + '</span></div>' +
      '<div class="row"><span>failed</span><span class="badge state-error">' + esc(String(st.failed || 0)) + '</span></div>' +
      '<div class="row"><span>other</span><span class="badge state-offline">' + esc(String(st.other || 0)) + '</span></div>' +
    '</div>' +
    '<div class="card"><h4>Error Kind</h4>' +
      '<div class="row"><span>apply</span><span class="badge state-running">' + esc(String(ek.apply || 0)) + '</span></div>' +
      '<div class="row"><span>verify</span><span class="badge state-error">' + esc(String(ek.verify || 0)) + '</span></div>' +
      '<div class="row"><span>repair</span><span class="badge state-thinking">' + esc(String(ek.repair || 0)) + '</span></div>' +
      '<div class="row"><span>none</span><span class="badge state-offline">' + esc(String(ek.none || 0)) + '</span></div>' +
    '</div>' +
    '<div class="card"><h4>Verification Reports</h4>' +
      '<div class="row"><span>total</span><span class="badge state-running">' + esc(String(verifyTotal)) + '</span></div>' +
      '<div class="row"><span>verified</span><span class="badge state-idle">' + esc(String(vs.verified || 0)) + '</span></div>' +
      '<div class="row"><span>weak</span><span class="badge state-thinking">' + esc(String(vs.weakly_supported || 0)) + '</span></div>' +
      '<div class="row"><span>unsupported</span><span class="badge state-error">' + esc(String(vs.unsupported || 0)) + '</span></div>' +
      '<div class="row"><span>conflict</span><span class="badge state-error">' + esc(String(vs.conflict || 0)) + '</span></div>' +
      '<div class="row"><span>high</span><span class="badge state-error">' + esc(String(vl.high || 0)) + '</span></div>' +
    '</div>';
}




function refreshDerivedViews() {
  renderDeskViews();
  renderOps();
  if (typeof renderToolHarnessEvents === 'function') renderToolHarnessEvents();
  if (typeof renderDCITraces === 'function') renderDCITraces();
  renderDebugPanels();
  renderOverview();
  renderRoleSelector();
  renderProgress();
  renderSystem();
  renderSessions();
  renderJobs();
  renderEvidence();
  renderMemorySnapshot();
  renderMemoryEvents();
  renderRecallTraces();
}

function renderDeskViews() {
  if (typeof renderHomeDesk === 'function') renderHomeDesk();
  if (typeof renderDevelopDesk === 'function') renderDevelopDesk();
  if (typeof renderInstructionsDesk === 'function') renderInstructionsDesk();
  if (typeof renderBacklogDesk === 'function') renderBacklogDesk();
  if (typeof renderReportsDesk === 'function') renderReportsDesk();
  if (typeof renderInvestmentDesk === 'function') renderInvestmentDesk();
  if (typeof renderGamesDesk === 'function') renderGamesDesk();
}

function refreshOpsData() {
  fetch('/viewer/logs?scope=persisted&limit=40')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'ops logs unavailable'));
        });
      }
      return r.json();
    })
    .then((data) => {
      const items = Array.isArray(data.items) ? data.items : [];
      state.ops.opsLogsFetchError = '';
      state.ops.persistedLogs = items;
      state.ops.lastMioReport = items.find((ev) => String(ev.from || '').toLowerCase() === 'mio' && String(ev.to || '').toLowerCase() === 'user') || null;
      state.ops.latestJobID = items[0] ? (items[0].job_id || '') : '';
      state.ops.latestRoute = items[0] ? (items[0].route || '') : '';
      state.ops.latestError = items.find((ev) => {
        const t = String(ev.type || '').toLowerCase();
        return t === 'agent.error' || t === 'mailbox.error' || t === 'worker.classified_failure';
      }) || null;
      renderOps();
      renderDeskViews();
    })
    .catch((err) => {
      state.ops.opsLogsFetchError = String(err && err.message ? err.message : err);
      state.ops.persistedLogs = [];
      state.ops.lastMioReport = null;
      state.ops.latestJobID = '';
      state.ops.latestRoute = '';
      state.ops.latestError = null;
      renderOps();
      renderDeskViews();
      console.error(err);
    });
}

function refreshToolHarnessData() {
  fetch('/viewer/tool-harness/recent?limit=30')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'tool harness unavailable'));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.ops.toolHarnessFetchError = '';
      state.ops.toolHarnessEvents = Array.isArray(data.items) ? data.items : [];
      if (typeof renderToolHarnessEvents === 'function') renderToolHarnessEvents();
      renderOps();
    })
    .catch((err) => {
      state.ops.toolHarnessFetchError = String(err && err.message ? err.message : err);
      state.ops.toolHarnessEvents = [];
      if (typeof renderToolHarnessEvents === 'function') renderToolHarnessEvents();
      renderOps();
      console.error(err);
    });
}

function refreshDCIData() {
  fetch('/viewer/dci/recent?limit=20')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'dci trace unavailable'));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.ops.dciFetchError = '';
      state.ops.dciTraces = Array.isArray(data.items) ? data.items : [];
      if (typeof renderDCITraces === 'function') renderDCITraces();
      renderOps();
    })
    .catch((err) => {
      state.ops.dciFetchError = String(err && err.message ? err.message : err);
      state.ops.dciTraces = [];
      if (typeof renderDCITraces === 'function') renderDCITraces();
      renderOps();
      console.error(err);
    });
}

function fetchGameBridgeJSON(path) {
  return fetch(path, {cache: 'no-store'})
    .then((r) => r.text().then((body) => {
      let data = null;
      if (body) {
        try {
          data = JSON.parse(body);
        } catch (err) {
          throw new Error('HTTP ' + String(r.status) + ': invalid JSON from ' + path);
        }
      }
      if (!r.ok || (data && data.ok === false)) {
        const message = data && (data.message || data.error)
          ? String(data.message || data.error)
          : (body || r.statusText || 'game bridge unavailable');
        throw new Error('HTTP ' + String(r.status) + ': ' + message);
      }
      return data || {};
    }));
}

function refreshGameBridgeData() {
  fetchGameBridgeJSON('/viewer/games/status')
    .then((status) => Promise.allSettled([
      fetchGameBridgeJSON('/viewer/games/sessions?limit=5'),
      fetchGameBridgeJSON('/viewer/games/events?limit=5'),
    ]).then((results) => ({status, results})))
    .then(({status, results}) => {
      const sessionResult = results[0];
      const eventResult = results[1];
      const sourceErrors = [];
      let skippedCount = 0;

      state.ops.gameBridgeStatusFetchError = '';
      state.ops.gameBridgeStatus = status || null;

      if (sessionResult && sessionResult.status === 'fulfilled') {
        const payload = sessionResult.value || {};
        state.ops.gameBridgeSessions = Array.isArray(payload.sessions) ? payload.sessions : [];
        skippedCount += Number(payload.skipped_count || 0);
      } else {
        state.ops.gameBridgeSessions = [];
        sourceErrors.push('sessions: ' + String(sessionResult && sessionResult.reason && sessionResult.reason.message ? sessionResult.reason.message : 'unavailable'));
      }

      if (eventResult && eventResult.status === 'fulfilled') {
        const payload = eventResult.value || {};
        state.ops.gameBridgeEvents = Array.isArray(payload.events) ? payload.events : [];
        skippedCount += Number(payload.skipped_count || 0);
      } else {
        state.ops.gameBridgeEvents = [];
        sourceErrors.push('events: ' + String(eventResult && eventResult.reason && eventResult.reason.message ? eventResult.reason.message : 'unavailable'));
      }

      state.ops.gameBridgeSourceFetchError = sourceErrors.join('\n');
      state.ops.gameBridgeSkippedCount = skippedCount;
      renderOps();
      if (typeof renderGamesDesk === 'function') renderGamesDesk();
    })
    .catch((err) => {
      state.ops.gameBridgeStatusFetchError = String(err && err.message ? err.message : err);
      state.ops.gameBridgeStatus = null;
      state.ops.gameBridgeSessions = [];
      state.ops.gameBridgeEvents = [];
      state.ops.gameBridgeSourceFetchError = '';
      state.ops.gameBridgeSkippedCount = 0;
      renderOps();
      if (typeof renderGamesDesk === 'function') renderGamesDesk();
      console.error(err);
    });
}

function refreshSandboxData() {
  fetch('/viewer/sandbox?limit=20&viewer_optional=1')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((body) => {
          throw new Error('HTTP ' + r.status + (body ? ': ' + body.trim() : ''));
        });
      }
      return r.json();
    })
    .then((data) => {
      if (data && data.ok === false) {
        throw new Error('HTTP ' + String(data.status || 503) + ': ' + (data.error || 'sandbox store unavailable'));
      }
      state.ops.sandboxFetchError = '';
      state.ops.sandboxes = Array.isArray(data.sandboxes) ? data.sandboxes : [];
      state.ops.sandboxArtifacts = Array.isArray(data.artifacts) ? data.artifacts : [];
      state.ops.sandboxPromotions = Array.isArray(data.promotions) ? data.promotions : [];
      state.ops.sandboxDecisions = Array.isArray(data.decisions) ? data.decisions : [];
      state.ops.sandboxGateLogs = Array.isArray(data.gate_logs) ? data.gate_logs : [];
      if (typeof renderSandboxStatus === 'function') renderSandboxStatus();
      renderOps();
    })
    .catch((err) => {
      state.ops.sandboxFetchError = String(err && err.message ? err.message : err);
      state.ops.sandboxes = [];
      state.ops.sandboxArtifacts = [];
      state.ops.sandboxPromotions = [];
      state.ops.sandboxDecisions = [];
      state.ops.sandboxGateLogs = [];
      if (typeof renderSandboxStatus === 'function') renderSandboxStatus();
      renderOps();
    });
}

function refreshSkillGovernanceData() {
  fetch('/viewer/skill-governance/recent?limit=20')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((body) => {
          throw new Error('HTTP ' + r.status + (body ? ': ' + body.trim() : ''));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.ops.skillGovernanceFetchError = '';
      state.ops.skillManifests = Array.isArray(data.manifests) ? data.manifests : [];
      state.ops.skillTriggerLogs = Array.isArray(data.trigger_logs) ? data.trigger_logs : [];
      state.ops.skillChangeLogs = Array.isArray(data.change_logs) ? data.change_logs : [];
      state.ops.contributionGateLogs = Array.isArray(data.contributions) ? data.contributions : [];
      state.ops.skillExternalPRSubmitRecords = Array.isArray(data.external_pr_submit_records) ? data.external_pr_submit_records : [];
      state.ops.skillExternalPRAdapter = data.external_pr_adapter ? String(data.external_pr_adapter) : '';
      state.ops.skillExternalPRAdapterConfigured = Boolean(data.external_pr_adapter_configured);
      state.ops.skillExternalPRHumanApprovalRequired = data.human_approval_required_for_pr !== false;
      state.ops.coderTranscripts = Array.isArray(data.coder_transcripts) ? data.coder_transcripts : [];
      renderOps();
    })
    .catch((err) => {
      state.ops.skillGovernanceFetchError = String(err && err.message ? err.message : err);
      state.ops.skillManifests = [];
      state.ops.skillTriggerLogs = [];
      state.ops.skillChangeLogs = [];
      state.ops.contributionGateLogs = [];
      state.ops.skillExternalPRSubmitRecords = [];
      state.ops.coderTranscripts = [];
      renderOps();
      console.error(err);
    });
}

function refreshWorkstreamData() {
  fetch('/viewer/workstreams?limit=20')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((body) => {
          throw new Error('HTTP ' + r.status + (body ? ': ' + body.trim() : ''));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.ops.workstreamFetchError = '';
      state.ops.workstreams = Array.isArray(data.workstreams) ? data.workstreams : [];
      state.ops.workstreamGoals = Array.isArray(data.goals) ? data.goals : [];
      state.ops.workstreamArtifacts = Array.isArray(data.artifacts) ? data.artifacts : [];
      state.ops.workstreamAnnotations = Array.isArray(data.annotations) ? data.annotations : [];
      state.ops.workstreamSteering = Array.isArray(data.steering) ? data.steering : [];
      state.ops.workstreamHeartbeats = Array.isArray(data.heartbeats) ? data.heartbeats : [];
      state.ops.workstreamVaultUpdates = Array.isArray(data.vault_updates) ? data.vault_updates : [];
      renderOps();
    })
    .catch((err) => {
      state.ops.workstreamFetchError = String(err && err.message ? err.message : err);
      state.ops.workstreams = [];
      state.ops.workstreamGoals = [];
      state.ops.workstreamArtifacts = [];
      state.ops.workstreamAnnotations = [];
      state.ops.workstreamSteering = [];
      state.ops.workstreamHeartbeats = [];
      state.ops.workstreamVaultUpdates = [];
      renderOps();
      console.error(err);
    });
}

function refreshRevenueData() {
  fetch('/viewer/revenue?limit=20')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((body) => {
          throw new Error('HTTP ' + r.status + (body ? ': ' + body.trim() : ''));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.ops.revenueFetchError = '';
      state.ops.revenueMarketResearch = Array.isArray(data.market_research) ? data.market_research : [];
      state.ops.revenueSNSPostMetrics = Array.isArray(data.sns_post_metrics) ? data.sns_post_metrics : [];
      state.ops.revenueProducts = Array.isArray(data.products) ? data.products : [];
      state.ops.revenueCustomerVoices = Array.isArray(data.customer_voices) ? data.customer_voices : [];
      state.ops.revenueEvents = Array.isArray(data.revenue_events) ? data.revenue_events : [];
      state.ops.revenueHumanDecisions = Array.isArray(data.human_decisions) ? data.human_decisions : [];
      state.ops.revenueDailyRoutineReports = Array.isArray(data.daily_routine_reports) ? data.daily_routine_reports : [];
      state.ops.revenueChannelDrafts = Array.isArray(data.channel_drafts) ? data.channel_drafts : [];
      state.ops.revenueExternalSendApplyRecords = Array.isArray(data.external_send_apply_records) ? data.external_send_apply_records : [];
      state.ops.revenueExternalChannelAdapter = String(data.external_channel_adapter || '');
      state.ops.revenueExternalChannelAdapterConfigured = Boolean(data.external_channel_adapter_configured);
      state.ops.revenueExternalSendHumanApprovalRequired = Boolean(data.human_approval_required_for_external_send);
      state.ops.revenueSummary = data && data.summary && typeof data.summary === 'object' ? data.summary : null;
      renderOps();
    })
    .catch((err) => {
      state.ops.revenueFetchError = String(err && err.message ? err.message : err);
      state.ops.revenueMarketResearch = [];
      state.ops.revenueSNSPostMetrics = [];
      state.ops.revenueProducts = [];
      state.ops.revenueCustomerVoices = [];
      state.ops.revenueEvents = [];
      state.ops.revenueHumanDecisions = [];
      state.ops.revenueDailyRoutineReports = [];
      state.ops.revenueChannelDrafts = [];
      state.ops.revenueExternalSendApplyRecords = [];
      state.ops.revenueSummary = null;
      renderOps();
      console.error(err);
    });
}

function refreshPersonaObservationData() {
  fetch('/viewer/persona-observation?limit=20')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'persona observation unavailable'));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.ops.personaObservationFetchError = '';
      state.ops.personaDiscomfortLogs = Array.isArray(data.discomfort_logs) ? data.discomfort_logs : [];
      state.ops.personaTriggerLogs = Array.isArray(data.trigger_logs) ? data.trigger_logs : [];
      state.ops.personaCanonicalResponseLogs = Array.isArray(data.canonical_response_logs) ? data.canonical_response_logs : [];
      state.ops.personaObservationLogs = Array.isArray(data.observation_logs) ? data.observation_logs : [];
      state.ops.personaMetaProfileUpdates = Array.isArray(data.meta_profile_updates) ? data.meta_profile_updates : [];
      state.ops.personaInterfaceSessions = Array.isArray(data.interface_sessions) ? data.interface_sessions : [];
      renderOps();
    })
    .catch((err) => {
      state.ops.personaObservationFetchError = String(err && err.message ? err.message : err);
      state.ops.personaDiscomfortLogs = [];
      state.ops.personaTriggerLogs = [];
      state.ops.personaCanonicalResponseLogs = [];
      state.ops.personaObservationLogs = [];
      state.ops.personaMetaProfileUpdates = [];
      state.ops.personaInterfaceSessions = [];
      renderOps();
      console.error(err);
    });
}

function refreshBrowserTraceAPIData() {
  fetch('/viewer/browser-trace-api?limit=20')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((body) => {
          throw new Error('HTTP ' + r.status + (body ? ': ' + body.trim() : ''));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.ops.browserTraceAPIFetchError = '';
      state.ops.browserTraceRuns = Array.isArray(data.trace_runs) ? data.trace_runs : [];
      state.ops.browserTraceAPICandidates = Array.isArray(data.api_candidates) ? data.api_candidates : [];
      state.ops.browserTraceAPISchemas = Array.isArray(data.api_schemas) ? data.api_schemas : [];
      state.ops.browserTraceAPICoverageReports = Array.isArray(data.coverage_reports) ? data.coverage_reports : [];
      state.ops.browserTraceAPIArtifacts = Array.isArray(data.api_artifacts) ? data.api_artifacts : [];
      renderOps();
    })
    .catch((err) => {
      state.ops.browserTraceAPIFetchError = String(err && err.message ? err.message : err);
      state.ops.browserTraceRuns = [];
      state.ops.browserTraceAPICandidates = [];
      state.ops.browserTraceAPISchemas = [];
      state.ops.browserTraceAPICoverageReports = [];
      state.ops.browserTraceAPIArtifacts = [];
      renderOps();
      console.error(err);
    });
}

function refreshComplexityHotspotData() {
  fetch('/viewer/complexity-hotspots?limit=20')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((body) => {
          throw new Error('HTTP ' + r.status + (body ? ': ' + body.trim() : ''));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.ops.complexityFetchError = '';
      state.ops.complexityScans = Array.isArray(data.scans) ? data.scans : [];
      state.ops.complexityHotspots = Array.isArray(data.hotspots) ? data.hotspots : [];
      state.ops.complexityEvidence = Array.isArray(data.evidence) ? data.evidence : [];
      state.ops.complexityReports = Array.isArray(data.reports) ? data.reports : [];
      renderOps();
    })
    .catch((err) => {
      state.ops.complexityFetchError = String(err && err.message ? err.message : err);
      state.ops.complexityScans = [];
      state.ops.complexityHotspots = [];
      state.ops.complexityEvidence = [];
      state.ops.complexityReports = [];
      renderOps();
      console.error(err);
    });
}

function refreshSuperAgentData() {
  fetch('/viewer/superagent?limit=20')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((body) => {
          throw new Error('HTTP ' + r.status + (body ? ': ' + body.trim() : ''));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.ops.superAgentFetchError = '';
      state.ops.superAgentRuns = Array.isArray(data.agent_runs) ? data.agent_runs : [];
      state.ops.superAgentSubagentTasks = Array.isArray(data.subagent_tasks) ? data.subagent_tasks : [];
      state.ops.superAgentContextPacks = Array.isArray(data.context_packs) ? data.context_packs : [];
      state.ops.superAgentMessageChannels = Array.isArray(data.message_channels) ? data.message_channels : [];
      state.ops.superAgentTraceEvents = Array.isArray(data.trace_events) ? data.trace_events : [];
      state.ops.superAgentRunQueue = Array.isArray(data.run_queue) ? data.run_queue : [];
      state.ops.superAgentRuntimeConfig = data.runtime_config && typeof data.runtime_config === 'object' ? data.runtime_config : null;
      renderOps();
    })
    .catch((err) => {
      state.ops.superAgentFetchError = String(err && err.message ? err.message : err);
      state.ops.superAgentRuns = [];
      state.ops.superAgentSubagentTasks = [];
      state.ops.superAgentContextPacks = [];
      state.ops.superAgentMessageChannels = [];
      state.ops.superAgentTraceEvents = [];
      state.ops.superAgentRunQueue = [];
      state.ops.superAgentRuntimeConfig = null;
      renderOps();
      console.error(err);
    });
}

function refreshAIWorkflowData() {
  fetch('/viewer/ai-workflow?limit=20')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((body) => {
          throw new Error('HTTP ' + r.status + (body ? ': ' + body.trim() : ''));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.ops.aiWorkflowFetchError = '';
      state.ops.aiWorkflowEvents = Array.isArray(data.workflow_events) ? data.workflow_events : [];
      state.ops.aiWorkflowProjectMemoryIndexes = Array.isArray(data.project_memory_indexes) ? data.project_memory_indexes : [];
      state.ops.aiWorkflowWorktreeRegistries = Array.isArray(data.worktree_registries) ? data.worktree_registries : [];
      state.ops.aiWorkflowCommandRegistries = Array.isArray(data.command_registries) ? data.command_registries : [];
      state.ops.aiWorkflowContextUsages = Array.isArray(data.context_usages) ? data.context_usages : [];
      state.ops.aiWorkflowContextBudgetPolicy = data.context_budget_policy && typeof data.context_budget_policy === 'object' ? data.context_budget_policy : null;
      renderOps();
    })
    .catch((err) => {
      state.ops.aiWorkflowFetchError = String(err && err.message ? err.message : err);
      state.ops.aiWorkflowEvents = [];
      state.ops.aiWorkflowProjectMemoryIndexes = [];
      state.ops.aiWorkflowWorktreeRegistries = [];
      state.ops.aiWorkflowCommandRegistries = [];
      state.ops.aiWorkflowContextUsages = [];
      state.ops.aiWorkflowContextBudgetPolicy = null;
      renderOps();
      console.error(err);
    });
}

function refreshHeavyWorkerRuntimeDiagnostics() {
  fetch('/viewer/ai-workflow/heavy-worker/runtime-diagnostics', { cache: 'no-store' })
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'heavy worker runtime diagnostics unavailable'));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.ops.heavyWorkerRuntimeDiagnosticsFetchError = '';
      state.ops.heavyWorkerRuntimeDiagnostics = data || null;
      renderOps();
    })
    .catch((err) => {
      state.ops.heavyWorkerRuntimeDiagnosticsFetchError = String(err && err.message ? err.message : err);
      state.ops.heavyWorkerRuntimeDiagnostics = null;
      renderOps();
      console.error(err);
    });
}

function refreshKnowledgeMemoryData() {
  fetch('/viewer/knowledge-memory?limit=20')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((body) => {
          throw new Error('HTTP ' + r.status + (body ? ': ' + body.trim() : ''));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.ops.knowledgeMemoryFetchError = '';
      state.ops.knowledgePersonalArchive = Array.isArray(data.personal_archive) ? data.personal_archive : [];
      state.ops.knowledgeCreativeItems = Array.isArray(data.creative_knowledge) ? data.creative_knowledge : [];
      state.ops.knowledgeNewsItems = Array.isArray(data.news_knowledge) ? data.news_knowledge : [];
      state.ops.knowledgeDailyIntakeRules = Array.isArray(data.daily_intake_rules) ? data.daily_intake_rules : [];
      state.ops.knowledgeTemporalMarkers = Array.isArray(data.temporal_markers) ? data.temporal_markers : [];
      state.ops.knowledgeDreamRuns = Array.isArray(data.dream_runs) ? data.dream_runs : [];
      renderOps();
    })
    .catch((err) => {
      state.ops.knowledgeMemoryFetchError = String(err && err.message ? err.message : err);
      state.ops.knowledgePersonalArchive = [];
      state.ops.knowledgeCreativeItems = [];
      state.ops.knowledgeNewsItems = [];
      state.ops.knowledgeDailyIntakeRules = [];
	      state.ops.knowledgeTemporalMarkers = [];
	      state.ops.knowledgeDreamRuns = [];
	      state.ops.knowledgeMemoryDetail = null;
	      renderOps();
	      console.error(err);
	    });
}

function refreshHobbyGraphOverviewData() {
  fetch('/viewer/hobby-graph?action=overview&limit=5', {cache: 'no-store'})
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'hobby graph overview unavailable'));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.ops.hobbyGraphOverviewFetchError = '';
      state.ops.hobbyGraphOverview = data && typeof data === 'object' ? data : null;
      renderOps();
    })
    .catch((err) => {
      state.ops.hobbyGraphOverviewFetchError = String(err && err.message ? err.message : err);
      state.ops.hobbyGraphOverview = null;
      renderOps();
      console.error(err);
    });
}

function refreshRuntimeBlockedRouteData() {
  const routes = [
    {label: 'Source Registry staging', path: '/viewer/source-registry?action=staging&limit=3'},
    {label: 'Memory Layers', path: '/viewer/memory/layers'},
    {label: 'Sandbox status', path: '/viewer/sandbox?limit=1&viewer_optional=1'},
    {label: 'LLM Ops status', path: '/viewer/llm-ops/status'},
  ];
  Promise.all(routes.map((route) => {
    return fetch(route.path, {cache: 'no-store'})
      .then((r) => r.text().then((body) => {
        let optional = null;
        try { optional = body ? JSON.parse(body) : null; } catch (_) {}
        if (r.ok && optional && optional.ok === false) {
          return {
            label: route.label,
            path: route.path,
            status: Number(optional.status || httpStatusServiceUnavailable()),
            ok: false,
            body: optional.error || body || '',
          };
        }
        return {
          label: route.label,
          path: route.path,
          status: r.status,
          ok: r.ok,
          body: body || '',
        };
      }))
      .catch((err) => ({
        label: route.label,
        path: route.path,
        status: 0,
        ok: false,
        body: String(err && err.message ? err.message : err),
      }));
  }))
    .then((items) => {
      state.ops.runtimeBlockedRoutes = items;
      renderOps();
    })
    .catch((err) => console.error(err));
}

function httpStatusServiceUnavailable() {
  return 503;
}

function refreshEvidence() {
  fetch('/viewer/evidence/recent?limit=20')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'evidence unavailable'));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.evidenceFetchError = '';
      state.evidence = Array.isArray(data.items) ? data.items : [];
      renderEvidence();
      renderDeskViews();
      if (state.pendingEvidenceJobID) {
        const want = state.pendingEvidenceJobID;
        const found = state.evidence.some((r) => String(r.job_id || '') === want);
        if (found) {
          state.pendingEvidenceJobID = '';
          openEvidence(want);
        } else {
          const detail = document.getElementById('evidenceDetail');
          if (detail) detail.innerHTML = '<span class="badge state-error">not found</span> job_id=' + esc(want);
          state.pendingEvidenceJobID = '';
          if (state.evidenceOrder.length > 0) {
            showToast('job_id not found, switched to newest evidence', 'error');
            openEvidence(state.evidenceOrder[0]);
          }
        }
      }
    })
    .catch((err) => {
      state.evidenceFetchError = String(err && err.message ? err.message : err);
      state.evidence = [];
      state.pendingEvidenceJobID = '';
      state.selectedEvidenceItem = null;
      renderEvidence();
      renderDeskViews();
      console.error(err);
    });
}

function refreshEvidenceSummary() {
  fetch('/viewer/evidence/summary')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'evidence summary unavailable'));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.evidenceSummaryFetchError = '';
      state.evidenceSummary = data.summary || {status: {}, error_kind: {}};
      renderEvidenceSummary();
      renderDeskViews();
    })
    .catch((err) => {
      state.evidenceSummaryFetchError = String(err && err.message ? err.message : err);
      state.evidenceSummary = {status: {}, error_kind: {}};
      renderEvidenceSummary();
      renderDeskViews();
      console.error(err);
    });
}

function refreshVerification() {
  fetch('/viewer/verification/recent?limit=20&viewer_optional=1')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'verification unavailable'));
        });
      }
      return r.json();
    })
    .then((data) => {
      if (data && data.ok === false) {
        throw new Error('HTTP ' + String(data.status || 503) + ': ' + (data.error || 'verification unavailable'));
      }
      state.verificationFetchError = '';
      state.verificationReports = Array.isArray(data.items) ? data.items : [];
      renderEvidence();
      renderDeskViews();
    })
    .catch((err) => {
      state.verificationFetchError = String(err && err.message ? err.message : err);
      state.verificationReports = [];
      renderEvidence();
      renderDeskViews();
    });
}

function refreshVerificationSummary() {
  fetch('/viewer/verification/summary?viewer_optional=1')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'verification summary unavailable'));
        });
      }
      return r.json();
    })
    .then((data) => {
      if (data && data.ok === false) {
        throw new Error('HTTP ' + String(data.status || 503) + ': ' + (data.error || 'verification summary unavailable'));
      }
      state.verificationSummaryFetchError = '';
      state.verificationSummary = data.summary || {status: {}, trigger_level: {}};
      renderEvidenceSummary();
      renderDeskViews();
    })
    .catch((err) => {
      state.verificationSummaryFetchError = String(err && err.message ? err.message : err);
      state.verificationSummary = {status: {}, trigger_level: {}};
      renderEvidenceSummary();
      renderDeskViews();
    });
}

function openEvidence(jobID) {
  if (!jobID) return;
  state.selectedEvidenceJobID = jobID;
  syncEvidenceQuery(jobID);
  renderEvidence();
  const hasVerificationOnly = (state.verificationReports || []).some((r) => String(r.job_id || '') === String(jobID)) &&
    !(state.evidence || []).some((r) => String(r.job_id || '') === String(jobID));
  const detailURL = hasVerificationOnly ? '/viewer/verification/detail?job_id=' : '/viewer/evidence/detail?job_id=';
  fetch(detailURL + encodeURIComponent(jobID))
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          const fallback = r.status === 404 ? 'evidence not found' : 'evidence detail fetch failed';
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || fallback));
        });
      }
      return r.json();
    })
    .then((data) => {
      state.selectedEvidenceItem = data.item || null;
      updateEvidenceNav();
      const el = document.getElementById('evidenceDetail');
      el.innerHTML = hasVerificationOnly ? renderVerificationReportDetail(state.selectedEvidenceItem || {}) : renderEvidenceDetail(state.selectedEvidenceItem || {});
      if (state.selectedEvidenceFocus) {
        scrollEvidenceFocus(state.selectedEvidenceFocus);
        state.selectedEvidenceFocus = '';
      }
    })
    .catch((err) => {
      console.error(err);
      state.selectedEvidenceItem = null;
      updateEvidenceNav();
      const el = document.getElementById('evidenceDetail');
      if (el) {
        const msg = String(err && err.message ? err.message : 'error');
        el.innerHTML = '<span class="badge state-error">' + esc(msg) + '</span> job_id=' + esc(jobID);
      }
      if (state.evidenceOrder.length > 0 && String(state.evidenceOrder[0]) !== String(jobID)) {
        showToast('evidence unavailable, switched to newest evidence', 'error');
        openEvidence(state.evidenceOrder[0]);
      }
    });
}
window.openEvidence = openEvidence;

function openEvidenceWithFocus(jobID, focus, evt) {
  if (evt && typeof evt.stopPropagation === 'function') evt.stopPropagation();
  state.selectedEvidenceFocus = String(focus || '');
  openEvidence(jobID);
}
window.openEvidenceWithFocus = openEvidenceWithFocus;

function updateEvidenceNav() {
  const order = state.evidenceOrder || [];
  const cur = String(state.selectedEvidenceJobID || '');
  const idx = order.indexOf(cur);
  if (eviPos) {
    if (order.length === 0 || idx < 0) eviPos.textContent = '- / -';
    else eviPos.textContent = String(idx + 1) + ' / ' + String(order.length);
  }
  if (eviPrev) eviPrev.disabled = !(idx > 0);
  if (eviNext) eviNext.disabled = !(idx >= 0 && idx < order.length - 1);
  if (eviCopy) eviCopy.disabled = !state.selectedEvidenceItem;
  if (eviCopySummary) eviCopySummary.disabled = !state.selectedEvidenceItem;
}

function openEvidenceAdjacent(delta) {
  const order = state.evidenceOrder || [];
  const cur = String(state.selectedEvidenceJobID || '');
  const idx = order.indexOf(cur);
  if (idx < 0) return;
  const next = idx + delta;
  if (next < 0 || next >= order.length) return;
  openEvidence(order[next]);
}

if (eviPrev) eviPrev.addEventListener('click', () => openEvidenceAdjacent(-1));
if (eviNext) eviNext.addEventListener('click', () => openEvidenceAdjacent(1));

function setEvidenceCopyFailure(btn, label, err) {
  if (!btn) return;
  const message = label + ' unavailable: ' + String(err && err.message ? err.message : err);
  btn.textContent = message;
  btn.title = message;
  showToast(label + ' failed', 'error');
}

if (eviCopy) eviCopy.addEventListener('click', () => {
  if (!state.selectedEvidenceItem) return;
  const text = JSON.stringify(state.selectedEvidenceItem, null, 2);
  writeClipboardText(text).then(() => {
    const old = eviCopy.textContent;
    eviCopy.textContent = 'Copied';
    eviCopy.title = '';
    showToast('Copied evidence JSON', 'success');
    setTimeout(() => { eviCopy.textContent = old; }, 1200);
  }).catch((err) => {
    console.error(err);
    setEvidenceCopyFailure(eviCopy, 'Evidence JSON copy', err);
  });
});
if (eviCopySummary) eviCopySummary.addEventListener('click', () => {
  if (!state.selectedEvidenceItem) return;
  const summary = buildEvidenceSummary(state.selectedEvidenceItem);
  writeClipboardText(summary).then(() => {
    const old = eviCopySummary.textContent;
    eviCopySummary.textContent = 'Copied';
    eviCopySummary.title = '';
    showToast('Copied evidence summary', 'success');
    setTimeout(() => { eviCopySummary.textContent = old; }, 1200);
  }).catch((err) => {
    console.error(err);
    setEvidenceCopyFailure(eviCopySummary, 'Evidence summary copy', err);
  });
});

function errorKindClass(kind) {
  const k = String(kind || '').toLowerCase();
  if (k === 'apply') return 'state-running';
  if (k === 'verify') return 'state-error';
  if (k === 'repair') return 'state-thinking';
  return 'state-offline';
}

function renderEvidenceDetail(item) {
  const steps = Array.isArray(item.steps) ? item.steps : [];
  const verification = Array.isArray(item.verification) ? item.verification : [];
  const statusClass = item.status === 'failed' ? 'state-error' : (item.status === 'passed' ? 'state-idle' : 'state-running');
  const errText = item.error ? esc(item.error) : '-';
  const stepHTML = steps.length > 0 ? steps.map((s, i) => (String(i + 1) + '. ' + esc(s))).join('<br>') : '-';
  const verifyHTML = verification.length > 0 ? verification.map((v, i) => (String(i + 1) + '. ' + renderVerificationLine(v))).join('<br>') : '-';
  return '' +
    '<div class="row"><span>Job ID</span><span class="code">' + esc(item.job_id || '-') + '</span></div>' +
    '<div class="row"><span>Status</span><span class="badge ' + statusClass + '">' + esc(item.status || '-') + '</span></div>' +
    '<div class="row"><span>Error Kind</span><span class="badge ' + errorKindClass(item.error_kind || '') + '">' + esc(item.error_kind || '-') + '</span></div>' +
    '<div class="row"><span>Goal</span><span>' + esc(item.goal || '-') + '</span></div>' +
    '<div class="row"><span>Attempt Count</span><span>' + esc(String(item.attempt_count || 0)) + '</span></div>' +
    '<div class="row"><span>Repair Count</span><span>' + esc(String(item.repair_count || 0)) + '</span></div>' +
    '<div id="evidenceSectionSteps" style="margin-top:8px"><b>Steps</b><div style="margin-top:4px;line-height:1.5">' + stepHTML + '</div></div>' +
    '<div id="evidenceSectionVerification" style="margin-top:8px"><b>Verification</b><div style="margin-top:4px;line-height:1.5">' + verifyHTML + '</div></div>' +
    '<div style="margin-top:8px"><b>Error</b><div style="margin-top:4px;line-height:1.5">' + errText + '</div></div>' +
    '<div style="margin-top:8px" class="small">Finished: ' + esc(fdt(item.finished_at)) + '</div>';
}

function renderVerificationLine(v) {
  const line = String(v || '');
  const lower = line.toLowerCase();
  if (lower.includes('verify:passed')) {
    return '<span class="badge state-idle">passed</span> ' + esc(line);
  }
  if (lower.includes('verify:failed')) {
    return '<span class="badge state-error">failed</span> ' + esc(line);
  }
  if (lower.includes('verify:error') || lower.includes('repair:error')) {
    return '<span class="badge state-error">error</span> ' + esc(line);
  }
  return '<span class="badge state-offline">note</span> ' + esc(line);
}

function renderVerificationReportDetail(item) {
  const claims = Array.isArray(item.claims) ? item.claims : [];
  const evidence = Array.isArray(item.evidence) ? item.evidence : [];
  const questions = Array.isArray(item.questions) ? item.questions : [];
  const status = String(item.status || '-');
  const claimHTML = claims.length > 0 ? claims.map((c, i) => {
    return String(i + 1) + '. <span class="badge ' + stateClass(verificationStatusClass(c.status)) + '">' + esc(c.status || '-') + '</span> ' + esc(c.text || '-') + ' <span class="small">' + esc(c.reason || '') + '</span>';
  }).join('<br>') : '-';
  const evidenceHTML = evidence.length > 0 ? evidence.map((ev, i) => {
    const support = ev.conflicts ? 'conflict' : (ev.supports ? 'support' : 'ref');
    const cls = ev.conflicts ? 'state-error' : (ev.supports ? 'state-idle' : 'state-offline');
    return String(i + 1) + '. <span class="badge ' + cls + '">' + esc(support) + '</span> ' + esc(ev.source_type || '-') + ':' + esc(ev.source_id || '-') + ' <span class="small">' + esc(ev.note || '') + '</span>';
  }).join('<br>') : '-';
  const questionHTML = questions.length > 0 ? questions.map((q, i) => String(i + 1) + '. ' + esc(q.query || '-')).join('<br>') : '-';
  return '' +
    '<div class="row"><span>Job ID</span><span class="code">' + esc(item.job_id || '-') + '</span></div>' +
    '<div class="row"><span>Status</span><span class="badge ' + stateClass(verificationStatusClass(status)) + '">' + esc(status) + '</span></div>' +
    '<div class="row"><span>Trigger</span><span class="badge state-thinking">' + esc(item.trigger_level || '-') + '</span></div>' +
    '<div class="row"><span>Route</span><span>' + esc(item.route || '-') + '</span></div>' +
    '<div class="row"><span>Counts</span><span>' + esc('claims=' + String(item.claim_count || 0) + ' verified=' + String(item.verified_count || 0) + ' weak=' + String(item.weak_count || 0) + ' unsupported=' + String(item.unsupported_count || 0) + ' conflict=' + String(item.conflict_count || 0)) + '</span></div>' +
    '<div id="evidenceSectionVerification" style="margin-top:8px"><b>Claims</b><div style="margin-top:4px;line-height:1.5">' + claimHTML + '</div></div>' +
    '<div style="margin-top:8px"><b>Evidence Refs</b><div style="margin-top:4px;line-height:1.5">' + evidenceHTML + '</div></div>' +
    '<div style="margin-top:8px"><b>Verification Questions</b><div style="margin-top:4px;line-height:1.5">' + questionHTML + '</div></div>' +
    '<div style="margin-top:8px" class="small">Created: ' + esc(fdt(item.created_at)) + '</div>';
}

function verificationStatusClass(status) {
  const s = String(status || '').toLowerCase();
  if (s === 'verified') return 'idle';
  if (s === 'weakly_supported' || s === 'not_checked') return 'thinking';
  if (s === 'unsupported' || s === 'conflict') return 'error';
  return 'offline';
}

function latestVerificationReportLink(jobID, status) {
  const cls = stateClass(verificationStatusClass(status));
  const badge = '<span class="badge ' + cls + '">' + esc(status || '-') + '</span>';
  if (!jobID) return badge;
  return '<button class="ctl-btn" onclick="openEvidenceWithFocus(\'' + esc(jobID) + '\', \'verification\', event)">' + badge + '</button>';
}

function latestVerificationBadge(list) {
  const arr = Array.isArray(list) ? list : [];
  if (arr.length === 0) return '<span class="badge state-offline">-</span>';
  const line = String(arr[arr.length - 1] || '').toLowerCase();
  if (line.includes('verify:passed')) return '<span class="badge state-idle">passed</span>';
  if (line.includes('verify:failed')) return '<span class="badge state-error">failed</span>';
  if (line.includes('verify:error') || line.includes('repair:error')) return '<span class="badge state-error">error</span>';
  return '<span class="badge state-offline">note</span>';
}

function latestVerificationLink(jobID, list) {
  const badge = latestVerificationBadge(list);
  if (!jobID) return badge;
  return '<button class="ctl-btn" onclick="openEvidenceWithFocus(\'' + esc(jobID) + '\', \'verification\', event)">' + badge + '</button>';
}

function latestVerificationLabel(list) {
  const arr = Array.isArray(list) ? list : [];
  if (arr.length === 0) return '-';
  const line = String(arr[arr.length - 1] || '').toLowerCase();
  if (line.includes('verify:passed')) return 'passed';
  if (line.includes('verify:failed')) return 'failed';
  if (line.includes('verify:error') || line.includes('repair:error')) return 'error';
  return 'note';
}

function buildEvidenceSummary(item) {
  const parts = [
    'job_id=' + String(item.job_id || '-'),
    'status=' + String(item.status || '-'),
    'error_kind=' + String(item.error_kind || '-'),
    'latest_verify=' + latestVerificationLabel(item.verification),
    'attempt_count=' + String(item.attempt_count || 0),
    'repair_count=' + String(item.repair_count || 0),
  ];
  if (item.error) parts.push('error=' + String(item.error));
  return parts.join(' | ');
}

function syncEvidenceQuery(jobID) {
  if (!window.history || !window.history.replaceState) return;
  const u = new URL(window.location.href);
  if (jobID) u.searchParams.set('job_id', String(jobID));
  else u.searchParams.delete('job_id');
  window.history.replaceState(null, '', u.toString());
}

function initTabFromQuery() {
  try {
    const u = new URL(window.location.href);
    const tab = (u.searchParams.get('tab') || '').trim().toLowerCase();
    if (tab && panels[tab]) {
      switchTab(tab);
    }
  } catch (_) {}
}

// Live2D モード: Viewer の UI を隠し、疑似 Live2D ステージを全画面表示する。
// 対応キャラクターは assets/live2d/<character>/ 配下にビュワー一式を置いて追加する。
function initLive2DMode(u) {
  const stage = document.getElementById('live2dStage');
  const frame = document.getElementById('live2dStageFrame');
  if (!stage || !frame) return false;
  const character = String(u.searchParams.get('character') || 'marin').trim().toLowerCase();
  if (!/^[a-z0-9_-]+$/.test(character)) return false;
  document.body.classList.add('live2d-mode');
  const params = new URLSearchParams();
  const expression = String(u.searchParams.get('expression') || '').trim();
  if (expression) params.set('expression', expression);
  if (u.searchParams.get('ui') === '0') params.set('ui', '0');
  const query = params.toString();
  frame.src = '/viewer/assets/live2d/' + character + '/index.html' + (query ? '?' + query : '');
  stage.hidden = false;
  return true;
}

function initLiveMode() {
  try {
    const u = new URL(window.location.href);
    const mode = String(u.searchParams.get('mode') || '').trim().toLowerCase();
    if (mode === 'live2d') return initLive2DMode(u);
    if (mode !== 'live' && mode !== 'lab') return false;
    const isLabMode = mode === 'lab';
    document.body.classList.add('live-mode');
    if (isLabMode) {
      document.body.classList.add('lab-mode');
      bindLabModeSwitcher();
      applyLabConversationStatus({mode: 'chat'});
    }
    switchTab('timeline');
    // ライブモードではIdleChat状態をポーリングしてトピックバーを更新
    const refreshLiveStatus = async () => {
      const topicEl = document.getElementById('liveTopicText');
      try {
        const r = await fetch('/viewer/idlechat/status');
        if (!r.ok) {
          const text = await r.text();
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'idlechat status unavailable'));
        }
        const d = await r.json();
        if (topicEl) {
          topicEl.textContent = d.current_topic || '-';
        }
        if (isLabMode) applyLabConversationStatus(d);
      } catch (err) {
        if (topicEl) {
          topicEl.textContent = 'IdleChat status unavailable: ' + String(err && err.message ? err.message : err);
        }
      }
    };
    if (isLabMode) refreshLiveStatus();
    setInterval(refreshLiveStatus, 5000);
    return true;
  } catch (_) { return false; }
}

const LAB_PARTNER_STORAGE_KEY = 'labConversation.selectedPartner';

function isLabChatPartner(actor) {
  return LAB_CHAT_PARTNERS.indexOf(String(actor || '').toLowerCase()) >= 0;
}

function labPartnerLabel(actor) {
  return LAB_CHAT_PARTNER_LABELS[String(actor || '').toLowerCase()] || 'Shiro';
}

function normalizeLabActor(value) {
  if (value === null || value === undefined) return '';
  if (Array.isArray(value)) {
    for (let i = value.length - 1; i >= 0; i -= 1) {
      const actor = normalizeLabActor(value[i]);
      if (actor) return actor;
    }
    return '';
  }
  if (typeof value === 'object') {
    const keys = ['to', 'recipient', 'target', 'persona', 'speaker', 'from', 'name', 'role'];
    for (const key of keys) {
      const actor = normalizeLabActor(value[key]);
      if (actor) return actor;
    }
    return '';
  }
  const text = String(value).trim().toLowerCase();
  if (text.includes('midori') || text.includes('みどり')) return 'midori';
  if (text.includes('shiro') || text.includes('しろ')) return 'shiro';
  if (text.includes('kuro') || text.includes('くろ')) return 'kuro';
  if (text.includes('mio') || text.includes('みお')) return 'mio';
  return '';
}

function deriveLabConversationMode(status) {
  const raw = String(
    status && (status.mode || (status.watchdog && status.watchdog.mode)) || ''
  ).trim().toLowerCase();
  if (status && (status.manual_mode === true || status.chat_active === true)) return 'idle';
  if (raw === 'idle' || raw === 'idlechat') return 'idle';
  if (raw === 'manual' || raw === 'forecast' || raw === 'story' || raw === 'story-simple') return 'idle';
  if (raw === 'chat') return 'chat';
  const sessionID = String(status && (status.active_session_id || (status.watchdog && status.watchdog.session_id)) || '');
  if (sessionID.toLowerCase().startsWith('idle-')) return 'idle';
  if (status && typeof status.current_topic === 'string' && status.current_topic.trim()) return 'idle';
  return 'chat';
}

function getLabSelectedPartner() {
  try {
    const stored = normalizeLabActor(localStorage.getItem(LAB_PARTNER_STORAGE_KEY));
    if (isLabChatPartner(stored)) return stored;
    if (typeof selectedRoleTargetID === 'function') {
      const selected = normalizeLabActor(selectedRoleTargetID());
      if (isLabChatPartner(selected)) return selected;
    }
  } catch (_) {}
  return 'shiro';
}

function syncLabRoleTarget(partner) {
  const actor = normalizeLabActor(partner) || 'shiro';
  try {
    const current = typeof selectedRoleTargetID === 'function'
      ? normalizeLabActor(selectedRoleTargetID())
      : normalizeLabActor(localStorage.getItem('roleSelector.selectedTarget'));
    if (current === actor) return actor;
    if (typeof selectRoleTarget === 'function') {
      selectRoleTarget(actor);
    } else {
      localStorage.setItem('roleSelector.selectedTarget', actor);
    }
  } catch (_) {}
  return actor;
}

function setLabSelectedPartner(partner, syncRoleTarget) {
  const normalized = normalizeLabActor(partner);
  const actor = normalized === 'mio' || isLabChatPartner(normalized) ? normalized : 'shiro';
  try { localStorage.setItem(LAB_PARTNER_STORAGE_KEY, actor); } catch (_) {}
  if (syncRoleTarget !== false) syncLabRoleTarget(actor);
  return actor;
}

function deriveLabConversationPartner(status) {
  const candidates = [
    status && status.to,
    status && status.recipient,
    status && status.target,
    status && status.persona,
    status && status.watchdog && status.watchdog.to,
    status && status.watchdog && status.watchdog.recipient,
    status && status.watchdog && status.watchdog.target,
    status && status.from,
    status && status.speaker,
    status && status.watchdog && status.watchdog.from,
    status && status.watchdog && status.watchdog.speaker,
    status && status.active_transcript,
    status && status.watchdog && status.watchdog.detail,
  ];
  for (const candidate of candidates) {
    const actor = normalizeLabActor(candidate);
    if (actor) return actor;
  }
  return getLabSelectedPartner();
}

function setLabBodyClass(name, enabled) {
  const classList = document && document.body && document.body.classList;
  if (!classList) return;
  if (enabled) {
    if (typeof classList.add === 'function') classList.add(name);
    return;
  }
  if (typeof classList.remove === 'function') {
    classList.remove(name);
  } else if (typeof classList.toggle === 'function') {
    classList.toggle(name, false);
  }
}

function setLabChipState(id, enabled) {
  const el = document.getElementById(id);
  if (!el) return;
  if (typeof el.setAttribute === 'function') el.setAttribute('aria-current', enabled ? 'true' : 'false');
  if (typeof el.setAttribute === 'function') el.setAttribute('aria-pressed', enabled ? 'true' : 'false');
  if (el.classList && typeof el.classList.toggle === 'function') el.classList.toggle('is-active', !!enabled);
}

function setLabPartnerMenuOpen(open) {
  const chip = document.getElementById('labModePartnerChip');
  const menu = document.getElementById('labPartnerOptions');
  const body = document && document.body;
  const isChat = !!(body && body.classList && body.classList.contains('lab-chat-mode'));
  const shouldOpen = !!open && isChat && !!menu && !!chip && !chip.disabled;
  if (menu) menu.hidden = !shouldOpen;
  if (chip && typeof chip.setAttribute === 'function') chip.setAttribute('aria-expanded', shouldOpen ? 'true' : 'false');
}

function syncLabPartnerPicker(partner, isIdle) {
  const actor = normalizeLabActor(partner) || getLabSelectedPartner();
  const selectedPartner = isLabChatPartner(actor) ? actor : getLabSelectedPartner();
  const isActivePartner = !isIdle && isLabChatPartner(actor);
  const chip = document.getElementById('labModePartnerChip');
  if (chip) {
    chip.textContent = labPartnerLabel(selectedPartner);
    chip.disabled = !!isIdle;
    chip.title = '';
    if (typeof chip.setAttribute === 'function') chip.setAttribute('aria-current', isActivePartner ? 'true' : 'false');
    if (typeof chip.setAttribute === 'function') chip.setAttribute('aria-pressed', isActivePartner ? 'true' : 'false');
    if (typeof chip.setAttribute === 'function') chip.setAttribute('aria-disabled', chip.disabled ? 'true' : 'false');
    if (chip.classList && typeof chip.classList.toggle === 'function') chip.classList.toggle('is-active', isActivePartner);
  }
  document.querySelectorAll('[data-lab-partner-option]').forEach((btn) => {
    const option = normalizeLabActor(btn.dataset.labPartnerOption);
    btn.hidden = option === selectedPartner;
    btn.textContent = labPartnerLabel(option);
    btn.disabled = false;
    btn.title = '';
    if (typeof btn.setAttribute === 'function') btn.setAttribute('aria-disabled', 'false');
  });
  if (isIdle || !isActivePartner) setLabPartnerMenuOpen(false);
}

function applyLabConversationStatus(status) {
  const body = document && document.body;
  if (!body) return;
  const conversationMode = deriveLabConversationMode(status || {});
  const isIdle = conversationMode === 'idle';
  const partner = isIdle
    ? getLabSelectedPartner()
    : setLabSelectedPartner(deriveLabConversationPartner(status || {}), true);
  const isMio = partner === 'mio';
  setLabBodyClass('lab-idle-mode', isIdle);
  setLabBodyClass('lab-chat-mode', !isIdle);
  setLabBodyClass('lab-partner-mio', isIdle || isMio);
  setLabBodyClass('lab-partner-shiro', isIdle || !isMio);
  setLabBodyClass('lab-partner-kuro', !isIdle && partner === 'kuro');
  setLabBodyClass('lab-partner-midori', !isIdle && partner === 'midori');
  if (body.dataset) {
    body.dataset.labConversationMode = conversationMode;
    body.dataset.labPartner = isIdle ? 'both' : partner;
    body.dataset.labSelectedPartner = partner;
  }
  setLabChipState('labModeChatChip', !isIdle);
  setLabChipState('labModeIdleChip', isIdle);
  setLabChipState('labModeMioChip', isIdle || partner === 'mio');
  syncLabPartnerPicker(partner, isIdle);
}

function setLabModeSwitcherBusy(enabled) {
  document.querySelectorAll('[data-lab-switch], [data-lab-partner-toggle], [data-lab-partner-option]').forEach((btn) => {
    btn.disabled = !!enabled;
  });
  if (enabled) setLabPartnerMenuOpen(false);
}

async function runLabIdleControl(path) {
  setLabModeSwitcherBusy(true);
  try {
    const res = await fetch(path, {method: 'POST'});
    if (!res.ok) {
      const text = await res.text();
      throw new Error('HTTP ' + String(res.status) + ': ' + (text || res.statusText || 'idlechat control failed'));
    }
    if (state && state.idleChat) state.idleChat.controlError = '';
    if (typeof refreshIdleStatus === 'function') await refreshIdleStatus();
    return true;
  } catch (err) {
    if (state && state.idleChat) {
      state.idleChat.controlError = 'IdleChat control unavailable: ' + String(err && err.message ? err.message : err);
    }
    if (typeof refreshIdleStatus === 'function') await refreshIdleStatus();
    if (typeof renderIdleChat === 'function') renderIdleChat();
    console.error(err);
    return false;
  } finally {
    setLabModeSwitcherBusy(false);
  }
}

function focusLabChatInput() {
  const target = document.getElementById('labInp') || document.getElementById('inp');
  if (target && typeof target.focus === 'function') target.focus();
}

function switchLabConversation(nextMode, partner) {
  const selectedPartner = partner ? setLabSelectedPartner(partner, true) : getLabSelectedPartner();
  if (nextMode === 'idle') {
    runLabIdleControl('/viewer/idlechat/start');
    return;
  }
  applyLabConversationStatus({mode: 'chat', persona: selectedPartner});
  focusLabChatInput();
  runLabIdleControl('/viewer/idlechat/stop');
}

let labModeSwitcherBound = false;
function bindLabModeSwitcher() {
  if (labModeSwitcherBound) return;
  labModeSwitcherBound = true;
  document.querySelectorAll('[data-lab-switch]').forEach((btn) => {
    btn.addEventListener('click', () => {
      const action = String(btn.dataset.labSwitch || '').trim().toLowerCase();
      if (action === 'idle') {
        switchLabConversation('idle');
        return;
      }
      if (action === 'mio') {
        switchLabConversation('chat', action);
        return;
      }
      switchLabConversation('chat');
    });
  });
  const partnerChip = document.querySelector('[data-lab-partner-toggle]');
  if (partnerChip) {
    partnerChip.addEventListener('click', (ev) => {
      ev.preventDefault();
      const body = document && document.body;
      if (!body || !body.classList || !body.classList.contains('lab-chat-mode')) return;
      const menu = document.getElementById('labPartnerOptions');
      setLabPartnerMenuOpen(menu ? menu.hidden : true);
    });
  }
  document.querySelectorAll('[data-lab-partner-option]').forEach((btn) => {
    btn.addEventListener('click', () => {
      const actor = normalizeLabActor(btn.dataset.labPartnerOption);
      if (!isLabChatPartner(actor)) return;
      setLabPartnerMenuOpen(false);
      switchLabConversation('chat', actor);
    });
  });
  document.addEventListener('click', (ev) => {
    const picker = document.getElementById('labPartnerPicker');
    if (picker && typeof picker.contains === 'function' && picker.contains(ev.target)) return;
    setLabPartnerMenuOpen(false);
  });
}

if (typeof window !== 'undefined') {
  window.applyLabConversationStatus = applyLabConversationStatus;
}

function shouldRefreshOptionalPanels() {
  return !(document.body && document.body.classList && document.body.classList.contains('live-mode'));
}

function shouldRefreshOpsPanelDiagnostics() {
  return shouldRefreshOptionalPanels() && activeViewerTab === 'ops';
}

function shouldRefreshEvidencePanelDiagnostics() {
  return shouldRefreshOptionalPanels() && activeViewerTab === 'jobs';
}

function refreshOptionalPanelData() {
  if (!shouldRefreshOptionalPanels()) return;
  if (typeof refreshInvestmentData === 'function') refreshInvestmentData();
  refreshOpsData();
  refreshToolHarnessData();
  refreshDCIData();
  if (typeof refreshGameBridgeData === 'function') refreshGameBridgeData();
  refreshSkillGovernanceData();
  refreshWorkstreamData();
  refreshRevenueData();
  refreshPersonaObservationData();
  refreshBrowserTraceAPIData();
  refreshComplexityHotspotData();
  refreshAIWorkflowData();
  refreshSuperAgentData();
  refreshHeavyWorkerRuntimeDiagnostics();
  refreshKnowledgeMemoryData();
  if (typeof refreshHobbyGraphOverviewData === 'function') refreshHobbyGraphOverviewData();
  refreshEvidence();
  refreshEvidenceSummary();
  refreshMemorySnapshot();
  refreshRecallTraces();
  if (shouldRefreshOpsPanelDiagnostics()) {
    refreshSandboxData();
    refreshRuntimeBlockedRouteData();
    if (typeof refreshToBeOpsData === 'function') refreshToBeOpsData();
  }
  if (shouldRefreshEvidencePanelDiagnostics()) {
    refreshVerification();
    refreshVerificationSummary();
  }
}

function setOptionalPanelRefreshIntervals() {
  if (!shouldRefreshOptionalPanels()) return;
  if (typeof refreshInvestmentData === 'function') setInterval(refreshInvestmentData, 30000);
  setInterval(refreshOpsData, 5000);
  setInterval(refreshToolHarnessData, 5000);
  setInterval(refreshDCIData, 5000);
  setInterval(() => { if (typeof refreshGameBridgeData === 'function') refreshGameBridgeData(); }, 5000);
  setInterval(() => { if (shouldRefreshOpsPanelDiagnostics()) refreshSandboxData(); }, 5000);
  setInterval(refreshSkillGovernanceData, 5000);
  setInterval(refreshWorkstreamData, 5000);
  setInterval(refreshRevenueData, 5000);
  setInterval(refreshPersonaObservationData, 5000);
  setInterval(refreshBrowserTraceAPIData, 5000);
  setInterval(refreshComplexityHotspotData, 5000);
  setInterval(refreshAIWorkflowData, 5000);
  setInterval(refreshSuperAgentData, 5000);
  setInterval(refreshHeavyWorkerRuntimeDiagnostics, 5000);
  setInterval(refreshKnowledgeMemoryData, 5000);
  setInterval(() => { if (typeof refreshHobbyGraphOverviewData === 'function') refreshHobbyGraphOverviewData(); }, 5000);
  setInterval(() => { if (shouldRefreshOpsPanelDiagnostics()) refreshRuntimeBlockedRouteData(); }, 5000);
  setInterval(() => { if (shouldRefreshOpsPanelDiagnostics() && typeof refreshToBeOpsData === 'function') refreshToBeOpsData(); }, 15000);
  setInterval(refreshEvidence, 5000);
  setInterval(refreshEvidenceSummary, 5000);
  setInterval(() => { if (shouldRefreshEvidencePanelDiagnostics()) refreshVerification(); }, 5000);
  setInterval(() => { if (shouldRefreshEvidencePanelDiagnostics()) refreshVerificationSummary(); }, 5000);
  setInterval(refreshMemorySnapshot, 15000);
  setInterval(refreshRecallTraces, 15000);
}

function initEvidenceFromQuery() {
  try {
    const u = new URL(window.location.href);
    const q = (u.searchParams.get('job_id') || '').trim();
    if (q) {
      state.pendingEvidenceJobID = q;
      switchTab('jobs');
    }
  } catch (_) {}
}

function scrollEvidenceFocus(focus) {
  const f = String(focus || '').toLowerCase();
  let id = '';
  if (f === 'verification') id = 'evidenceSectionVerification';
  if (f === 'steps') id = 'evidenceSectionSteps';
  if (!id) return;
  const el = document.getElementById(id);
  if (!el) return;
  el.scrollIntoView({behavior: 'smooth', block: 'center'});
}

let toastTimer = null;
let toastLastMsg = '';
let toastLastKind = '';
function showToast(msg, kind) {
  if (!toastEl) return;
  const text = String(msg || '');
  const k = (kind === 'success' || kind === 'error') ? kind : 'info';
  const same = toastEl.classList.contains('show') && toastLastMsg === text && toastLastKind === k;
  toastEl.textContent = text;
  toastEl.classList.remove('info', 'success', 'error');
  toastEl.classList.add(k);
  if (!same) {
    toastEl.classList.remove('show');
    // Force style flush so quick consecutive different messages animate reliably.
    void toastEl.offsetWidth;
    toastEl.classList.add('show');
  }
  toastLastMsg = text;
  toastLastKind = k;
  if (toastTimer) clearTimeout(toastTimer);
  let ms = 1800;
  if (k === 'success') ms = 1200;
  if (k === 'error') ms = 2600;
  toastTimer = setTimeout(() => {
    toastEl.classList.remove('show');
    toastLastMsg = '';
    toastLastKind = '';
  }, ms);
}

if (toastEl) {
  toastEl.addEventListener('click', () => {
    if (toastTimer) clearTimeout(toastTimer);
    toastEl.classList.remove('show');
    toastLastMsg = '';
    toastLastKind = '';
  });
}
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Escape') return;
  if (!toastEl || !toastEl.classList.contains('show')) return;
  if (toastTimer) clearTimeout(toastTimer);
  toastEl.classList.remove('show');
  toastLastMsg = '';
  toastLastKind = '';
});

if (eviStatus) eviStatus.addEventListener('change', renderEvidence);
if (eviErrorKind) eviErrorKind.addEventListener('change', renderEvidence);
if (eviSort) eviSort.addEventListener('click', () => {
  state.evidenceSortDesc = !state.evidenceSortDesc;
  eviSort.textContent = state.evidenceSortDesc ? 'finished: newest' : 'finished: oldest';
  renderEvidence();
});

function degradeOfflineStates() {
  const now = Date.now();
  AGENTS.forEach((id) => {
    const s = state.agents[id];
    if (!s.updatedAt) return;
    if (s.state === 'unavailable') return;
    const diff = now - Date.parse(s.updatedAt);
    if (diff > OFFLINE_MS && s.state !== 'offline') s.state = 'offline';
  });
  renderOverview();
  renderRoleSelector();
  renderProgress();
}
setInterval(degradeOfflineStates, 5000);

function refreshViewerStatus() {
  fetch('/viewer/status')
    .then((r) => {
      if (!r.ok) {
        return r.text().then((text) => {
          throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'viewer status unavailable'));
        });
      }
      return r.json();
    })
    .then((payload) => applyMonitorStatusSnapshot(payload))
    .catch((err) => {
      const message = String(err && err.message ? err.message : err);
      state.viewerStatusFetchError = message;
      AGENTS.forEach((id) => {
        touchAgent(id, {
          state: 'unavailable',
          reason: 'viewer status unavailable: ' + message,
          route: '-',
          lastEvent: 'viewer status fetch failed',
          peer: '-',
          jobID: '-',
          preview: 'viewer status unavailable',
          updatedAt: new Date().toISOString(),
        });
      });
      renderOverview();
      renderRoleSelector();
      renderProgress();
      console.error(err);
    });
}

function ingestEvent(ev) {
  handleViewerActiveControlEvent(ev);
  if (isStaleIdleChatEvent(ev)) return;
  if (ev && ev.type === 'job.notification') {
    const key = jobNotificationEventKey(ev);
    if (key && seenJobNotificationKeys.has(key)) return;
    if (key) rememberJobNotificationKey(key);
  }
  const key = eventKey(ev);
  if (seenEventKeys.has(key)) return;
  rememberEventKey(key);
  const receivedMS = typeof nowLatencyMS === 'function' ? nowLatencyMS() : Date.now();
  noteViewerEventLatency(ev, receivedMS);

  state.logs.push(ev);
  if (state.logs.length > MAX_LOGS) state.logs.shift();
  upsertSession(ev);
  upsertJob(ev);
  updateAgents(ev);
  addMsgToTimeline(ev);
  addIdleMsgToTimeline(ev);
  addIdleSummaryToTimeline(ev);
  if (ev.type === 'agent.thinking') {
    pushDebugTrace('think', {
      time: ftime(ev.timestamp),
      agent: agName(ev.from || '-'),
      job: ev.job_id || '-',
      text: short(ev.content || '', 240),
    });
  }
  handleTTSAudioEvent(ev);
  if (String(ev.type || '').toLowerCase().startsWith('investment.')) {
    scheduleInvestmentRefresh();
  }
  derivedDirty = true;
  // Update Live2D emotion on messages
  if (typeof updateLive2DOnMessage === 'function') updateLive2DOnMessage(ev);
}

function rememberJobNotificationKey(key) {
  seenJobNotificationKeys.add(key);
  if (seenJobNotificationKeys.size > 300) {
    const first = seenJobNotificationKeys.values().next().value;
    if (first) seenJobNotificationKeys.delete(first);
  }
}

function jobNotificationEventKey(ev) {
  return [
    ev.job_id || '',
    ev.status || ev.category || '',
    ev.level || '',
    ev.timestamp || '',
    ev.content || '',
  ].join('|');
}

function jobNotificationKey(n) {
  return [
    n.job_id || '',
    n.status || '',
    n.level || '',
    n.created_at || '',
    n.summary || '',
  ].join('|');
}

function normalizeJobNotificationAssignee(n) {
  const raw = String((n && n.assignee) || '').trim().toLowerCase();
  if (!raw || raw === 'worker' || raw === 'heavy') return 'shiro';
  if (raw === 'ao') return 'coder1';
  if (raw === 'aka') return 'coder2';
  if (raw === 'kin') return 'coder3';
  if (raw === 'gin') return 'coder4';
  return raw;
}

function formatJobNotificationContent(n) {
  const title = String((n && n.title) || 'job').trim();
  const status = String((n && n.status) || '').trim();
  const summary = String((n && n.summary) || '').trim();
  const nextActions = Array.isArray(n && n.next_actions) ? n.next_actions.filter(Boolean) : [];
  let content = title;
  if (status) content += '\nstatus: ' + status;
  if (summary) content += '\n' + summary;
  if (nextActions.length) content += '\nnext: ' + nextActions.join(' / ');
  return content;
}

function jobNotificationToEvent(n) {
  const status = String((n && n.status) || '').trim();
  return {
    type: 'job.notification',
    from: normalizeJobNotificationAssignee(n),
    to: 'mio',
    content: formatJobNotificationContent(n),
    route: String((n && n.route) || '').trim(),
    job_id: String((n && n.job_id) || '').trim(),
    timestamp: String((n && n.created_at) || new Date().toISOString()),
    category: status,
    status,
    level: String((n && n.level) || '').trim(),
  };
}

function ingestJobNotification(n) {
  const key = jobNotificationKey(n);
  if (!key.trim() || seenJobNotificationKeys.has(key)) return;
  rememberJobNotificationKey(key);
  ingestEvent(jobNotificationToEvent(n));
}

async function refreshJobNotifications() {
  if (jobNotificationPollInFlight) return;
  jobNotificationPollInFlight = true;
  try {
    const res = await fetch('/viewer/job-notifications?limit=20', {cache: 'no-store'});
    if (!res.ok) return;
    const data = await res.json();
    const items = Array.isArray(data.items) ? data.items.slice().reverse() : [];
    items.forEach(ingestJobNotification);
  } catch (_) {
  } finally {
    jobNotificationPollInFlight = false;
  }
}

function scheduleInvestmentRefresh() {
  if (typeof refreshInvestmentData !== 'function') return;
  if (investmentRefreshTimer) return;
  investmentRefreshTimer = setTimeout(() => {
    investmentRefreshTimer = null;
    refreshInvestmentData();
  }, 500);
}

function isStaleIdleChatEvent(ev) {
	if (!ev) return false;
	if (!state.idleChat.interrupted) return false;
	const type = String(ev.type || '').trim();
	if (type === 'idlechat.message' || type === 'idlechat.summary' || type === 'idlechat.topic') {
		return isStoppedIdleChatSession(String(ev.session_id || ev.chat_id || '').trim());
	}
	if (type === 'tts.audio_chunk') {
		try {
			const payload = JSON.parse(ev.content || '{}');
			return isStoppedIdleChatSession(String(payload.session_id || '').trim());
		} catch (_) {
			return false;
		}
	}
	return false;
}

function handleTTSAudioEvent(ev) {
  chatAudioSync.handleEvent(ev);
}

function isStoppedIdleChatSession(sessionId) {
	const sid = String(sessionId || '').trim();
	if (!state || !state.idleChat || !state.idleChat.interrupted || state.idleChat.chatActive) return false;
	const interruptedSessionId = String(state.idleChat.interruptedSessionId || '').trim();
	if (!sid || !interruptedSessionId) return false;
	return sid === interruptedSessionId;
}

function isIdleChatActiveForTTS(sessionId) {
	if (state && state.idleChat && state.idleChat.chatActive) return true;
	const sid = String(sessionId || '').trim();
	if (!isIdleChatSessionId(sid) || isStoppedIdleChatSession(sid)) return false;
	return true;
}

function resolveTTSPlaybackURL(audioURL, audioPath) {
  const url = String(audioURL || '').trim();
  if (url) {
    try {
      const parsed = new URL(url, window.location.href);
      if (parsed.protocol === 'http:' || parsed.protocol === 'https:') {
        return '/viewer/tts/audio?url=' + encodeURIComponent(parsed.href);
      }
    } catch (_) {}
  }
  if (url) return url;
  if (audioPath) return '/viewer/tts/audio?path=' + encodeURIComponent(audioPath);
  return '';
}

function createChatAudioSync() {
  const state = ttsPlayback;
  // Lifecycle ownership:
  // - completedSessions is only an IdleChat session-level start gate for buffered audio.
  // - responseLifecycle is the response-level source of truth for the three independent TTS checkpoints:
  //   synthesis completed, browser WAV fetch completed, and playback ACK completed.
  // - completedResponses / responsePlaybackCounts / responsePlaybackResults / seenAudioResponses form one response-level ACK lifecycle.
  // - seenUtterances and blockedAckKeys are chunk-level local dedupe guards for this tab only.
  const completedSessions = new Set();
  const completedResponses = new Set();
  const acknowledgedResponses = new Set();
  const responsePlaybackCounts = new Map();
  const responsePlaybackResults = new Map();
  const seenAudioResponses = new Set();
  const seenUtterances = new Set();
  const blockedAckKeys = new Set();
  const interruptedChatSessions = new Set();
  const interruptedChatResponses = new Set();

  const module = {
    state,
    audio: {
      ensure: ensureAudioInternal,
      enqueue: enqueueAudioChunkInternal,
      playNext: playNextInternal,
      disable: disableAudioInternal,
    unlock: unlockAudioInternal,
    },
    text: {
      show: showChunkTextInternal,
      clear: clearTextInternal,
      fallback: showFallbackChunkInternal,
    },
    lipSync: {
      start: startLipSyncInternal,
      stop: stopLipSyncInternal,
      clear: clearLipSyncSpeaking,
    },
    handleEvent,
    enqueueAudio: enqueueAudioChunkInternal,
    enqueueDisplayFallback: enqueueDisplayFallbackInternal,
    markAudioStarted,
    markSessionCompleted,
    resetCurrent: resetCurrentInternal,
    resetChat: resetChatInternal,
    resetIdleChat: resetIdleChatInternal,
    startTextFallback: startTextFallbackInternal,
    playNext: playNextInternal,
    disableAudio: disableAudioInternal,
    unlockAudio: unlockAudioInternal,
    ensureAudio: ensureAudioInternal,
    showFallbackChunk: showFallbackChunkInternal,
  };
  return module;

  function normalizeEvent(ev) {
    if (ev && ev.type === 'tts.session_completed') {
      try {
        const payload = JSON.parse(ev.content || '{}');
        return {
          eventType: 'session_completed',
          sessionId: String(payload.session_id || ev.session_id || '').trim(),
          responseId: String(payload.response_id || '').trim(),
          utteranceId: String(payload.utterance_id || '').trim(),
          messageId: String(payload.message_id || '').trim(),
          turnIndex: Number.isFinite(Number(payload.turn_index)) ? Math.floor(Number(payload.turn_index)) : -1,
          characterId: String(payload.character_id || payload.speaker || '').trim().toLowerCase(),
        };
      } catch (_) {
        return {
          eventType: 'session_completed',
          sessionId: String(ev.session_id || '').trim(),
          responseId: '',
        };
      }
    }
    if (!ev || ev.type !== 'tts.audio_chunk') return null;
    let payload = null;
    try {
      payload = JSON.parse(ev.content || '{}');
    } catch (_) {
      return null;
    }
    let url = String(payload.audio_url || '').trim();
    const audioPath = String(payload.audio_path || '').trim();
    const sessionId = String(payload.session_id || '').trim();
    const track = String(payload.track || payload.track_id || 'default').trim() || 'default';
    const chunkIndexRaw = Number(payload.chunk_index);
    const chunkIndex = Number.isFinite(chunkIndexRaw) ? Math.floor(chunkIndexRaw) : -1;
    const turnIndexRaw = Number(payload.turn_index);
    const turnIndex = Number.isFinite(turnIndexRaw) ? Math.floor(turnIndexRaw) : -1;
    const characterId = String(payload.character_id || payload.speaker || '').trim().toLowerCase();
    const text = String(payload.speech_text || payload.text || '').trim();
    const displayText = String(payload.display_text || payload.viewer_text || payload.text || '').trim();
    const responseId = String(payload.response_id || '').trim();
    const messageId = String(payload.message_id || '').trim();
    const utteranceId = String(payload.utterance_id || '').trim() || (sessionId + ':' + String(chunkIndex));
    const errorCode = String(payload.error_code || '').trim();
    const error = String(payload.error || '').trim();
    const mode = isIdleChatSessionId(sessionId) ? 'idlechat' : 'chat';
    url = resolveTTSPlaybackURL(url, audioPath);

    // Rewrite known remote URL patterns to browser-fetchable paths.
    const cacheMatch = url.match(/^http:\/\/192\.168\.1\.36:(8765|8766)\/cache-(?:a|b)\/([^/?#]+\.wav)$/i);
    if (cacheMatch) {
      url = `http://192.168.1.36:${cacheMatch[1]}/audio/${cacheMatch[2]}`;
    }
    return {
      eventType: 'chunk',
      url,
      audioPath,
      characterId,
      sessionId,
      track,
      chunkIndex,
      text,
      displayText,
      responseId,
      utteranceId,
      messageId,
      turnIndex,
      errorCode,
      error,
      displayOnly: !url,
      mode,
    };
  }

  function handleEvent(ev) {
    const chunk = normalizeEvent(ev);
    if (!chunk) return;
		if (chunk.mode === 'idlechat' && !isIdleChatActiveForTTS(chunk.sessionId)) return;
    if (chunk.mode === 'idlechat' && !ttsPlayback.audioEnabled) return;
    if (chunk.mode === 'idlechat') {
      const activeIdleSession = String(typeof idleLiveActiveSessionId !== 'undefined' ? idleLiveActiveSessionId || '' : '').trim();
      if (activeIdleSession && chunk.sessionId && chunk.sessionId !== activeIdleSession) return;
    } else if (isInterruptedChatOutput(chunk)) {
      return;
    }
    if (chunk.eventType === 'session_completed') {
      if (!isThisViewerActiveAudio()) return;
      markSessionCompleted(chunk);
      return;
    }
    if (!isThisViewerActiveAudio()) return;
    if (!chunk.url) {
      if (chunk.displayText) enqueueDisplayFallbackInternal(chunk);
      return;
    }
    if (chunk.mode === 'idlechat' && !String(viewerControl.activeAudioViewerId || '').trim() && ttsPlayback.audioEnabled) {
      claimViewerControl('audio', 'idlechat_tts_chunk');
    }
    enqueueAudioChunkInternal(chunk);
  }

  function normalizeChunk(chunk) {
    const normalized = {
      url: String((chunk && chunk.url) || ''),
      characterId: String((chunk && chunk.characterId) || '').trim().toLowerCase(),
      sessionId: String((chunk && chunk.sessionId) || '').trim(),
      track: String((chunk && chunk.track) || 'default').trim() || 'default',
      chunkIndex: Number.isFinite(chunk && chunk.chunkIndex) ? chunk.chunkIndex : -1,
      text: String((chunk && chunk.text) || ''),
      displayText: String((chunk && (chunk.displayText || chunk.text)) || ''),
      responseId: String((chunk && chunk.responseId) || ''),
      utteranceId: String((chunk && chunk.utteranceId) || ''),
      messageId: String((chunk && chunk.messageId) || ''),
      turnIndex: Number.isFinite(chunk && chunk.turnIndex) ? chunk.turnIndex : -1,
      errorCode: String((chunk && chunk.errorCode) || ''),
      error: String((chunk && chunk.error) || ''),
      displayOnly: Boolean(chunk && chunk.displayOnly),
      mode: String((chunk && chunk.mode) || '').trim(),
    };
    if (!normalized.mode) normalized.mode = isIdleChatSessionId(normalized.sessionId) ? 'idlechat' : 'chat';
    if (!normalized.utteranceId) {
      normalized.utteranceId = normalized.sessionId + ':' + String(normalized.chunkIndex >= 0 ? normalized.chunkIndex : state.seq + 1);
    }
    return normalized;
  }

  function enqueueAudioChunkInternal(chunk) {
    enqueueChunkInternal(Object.assign(normalizeChunk(chunk), {displayOnly: false}));
    playNextInternal();
  }

  function enqueueDisplayFallbackInternal(chunk) {
    enqueueChunkInternal(Object.assign(normalizeChunk(chunk), {url: '', displayOnly: true}));
    startTextFallbackInternal();
  }

  function enqueueChunkInternal(chunk) {
    if (isInterruptedChatOutput(chunk)) return;
    const chunkKey = ttsChunkIdentityKey(chunk.sessionId, chunk.utteranceId, chunk.chunkIndex, state.seq + 1);
    if (chunkKey && seenUtterances.has(chunkKey)) return;
    if (chunkKey) seenUtterances.add(chunkKey);
    chunk.seq = ++state.seq;
    if (typeof recordLatencyMetric === 'function') {
      recordLatencyMetric('tts', 'audio_queue_enqueue', {
        detail: 'chunk=' + String(chunk.chunkIndex),
        job: chunk.responseId,
        session: chunk.sessionId,
      });
    }
    incrementResponsePlaybackCount(chunk.responseId);
    state.queue.push(chunk);
    sortQueue();
    preloadQueuedAudioInternal();
  }

  function sortQueue() {
    state.queue.sort((a, b) => {
      const aKey = `${a.sessionId}|${a.responseId}|${a.track}`;
      const bKey = `${b.sessionId}|${b.responseId}|${b.track}`;
      if (aKey === bKey && a.chunkIndex >= 0 && b.chunkIndex >= 0 && a.chunkIndex !== b.chunkIndex) {
        return a.chunkIndex - b.chunkIndex;
      }
      return a.seq - b.seq;
    });
  }

  function markSessionCompleted(sessionOrChunk, responseId) {
    const chunk = sessionOrChunk && typeof sessionOrChunk === 'object'
      ? normalizeChunk(sessionOrChunk)
      : {sessionId: String(sessionOrChunk || '').trim(), responseId: String(responseId || '').trim()};
    const sid = String(chunk.sessionId || '').trim();
    if (sid) completedSessions.add(sid);
    const rid = String(chunk.responseId || '').trim();
    if (rid) {
      completedResponses.add(rid);
      if (seenAudioResponses.has(rid)) {
        maybeAcknowledgeResponsePlayback(chunk, 'completed_after_playback');
      }
    }
    playNextInternal();
  }

  function incrementResponsePlaybackCount(responseId) {
    const rid = String(responseId || '').trim();
    if (!rid) return;
    seenAudioResponses.add(rid);
    responsePlaybackCounts.set(rid, (responsePlaybackCounts.get(rid) || 0) + 1);
  }

  function decrementResponsePlaybackCount(responseId) {
    const rid = String(responseId || '').trim();
    if (!rid) return;
    const nextCount = Math.max(0, (responsePlaybackCounts.get(rid) || 0) - 1);
    if (nextCount === 0) {
      responsePlaybackCounts.delete(rid);
      return;
    }
    responsePlaybackCounts.set(rid, nextCount);
  }

  function recordResponsePlaybackResult(item, status, err) {
    const responseId = String((item && item.responseId) || '').trim();
    if (!responseId) return;
    const normalizedStatus = String(status || '').trim();
    if (!normalizedStatus || normalizedStatus === 'ended' || normalizedStatus === 'completed_after_playback') return;
    if (responsePlaybackResults.has(responseId)) return;
    responsePlaybackResults.set(responseId, {item, status: normalizedStatus, err: err || null});
  }

  function maybeAcknowledgeResponsePlayback(item, status, err) {
    const responseId = String((item && item.responseId) || '').trim();
    if (!responseId) return;
    if (!completedResponses.has(responseId)) return;
    if ((responsePlaybackCounts.get(responseId) || 0) > 0) return;
    if (acknowledgedResponses.has(responseId)) return;
    acknowledgedResponses.add(responseId);
    const recorded = responsePlaybackResults.get(responseId);
    if (recorded) {
      postTTSPlaybackAck(recorded.item || item, recorded.status || status, recorded.err || err);
      clearResponsePlaybackLifecycle(responseId);
      return;
    }
    postTTSPlaybackAck(item, status, err);
    clearResponsePlaybackLifecycle(responseId);
  }

  function clearResponsePlaybackLifecycle(responseId) {
    const rid = String(responseId || '').trim();
    if (!rid) return;
    completedResponses.delete(rid);
    responsePlaybackCounts.delete(rid);
    responsePlaybackResults.delete(rid);
    seenAudioResponses.delete(rid);
  }

  function postTTSPlaybackAck(item, status, err) {
    const normalizedStatus = normalizeTTSPlaybackAckStatus(status);
    const errorCode = ttsPlaybackAckErrorCode(item, normalizedStatus, err);
    const payload = {
      response_id: String((item && item.responseId) || '').trim(),
      session_id: String((item && item.sessionId) || '').trim(),
      utterance_id: String((item && item.utteranceId) || '').trim(),
      message_id: String((item && item.messageId) || '').trim(),
      turn_index: Number.isFinite(item && item.turnIndex) ? Math.floor(item.turnIndex) : -1,
      viewer_client_id: viewerControl.clientId,
      status: normalizedStatus,
      error_code: errorCode,
      error: err ? describeTTSAudioError(err) : '',
    };
    if (!payload.response_id) return;
    if (!isThisViewerActiveAudio()) return;
    if (typeof fetch !== 'function') return;
    fetch('/viewer/tts/playback-ack', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(payload),
      keepalive: true,
    }).catch((ackErr) => {
      console.warn('tts playback ack failed', ackErr);
    });
  }

  function normalizeTTSPlaybackAckStatus(status) {
    const value = String(status || 'ended').trim();
    if (value === 'fallback') return 'error';
    return value || 'ended';
  }

  function ttsPlaybackAckErrorCode(item, status, err) {
    const normalizedStatus = String(status || '').trim();
    if (normalizedStatus !== 'error') return '';
    if (item && item.errorCode) return String(item.errorCode).trim();
    const text = describeTTSAudioError(err).toLowerCase();
    if (text.indexOf('missing idlechat audio url') >= 0) return 'TTS_AUDIO_MISSING';
    if (text.indexOf('idlechat audio disabled') >= 0) return 'TTS_AUDIO_DISABLED';
    if (text.indexOf('blocked autoplay') >= 0 || text.indexOf('notallowed') >= 0 || text.indexOf('did not interact') >= 0) return 'TTS_AUDIO_BLOCKED';
    if (isIdleChatPlaybackItem(item)) return 'TTS_AUDIO_PLAYBACK_ERROR';
    return 'TTS_PLAYBACK_ERROR';
  }

  function canStartChunk(chunk) {
    if (!chunk) return false;
    if (chunk.mode !== 'idlechat') return true;
    if (chunk.chunkIndex === 0) return true;
    const buffered = state.queue.filter((item) => item && item.mode === 'idlechat' && item.sessionId === chunk.sessionId && !item.displayOnly).length;
    return buffered >= 2 || completedSessions.has(chunk.sessionId);
  }

  function preloadQueuedAudioInternal() {
    if (!state.audioEnabled || typeof Audio !== 'function') return;
    const wanted = new Set();
    state.queue.forEach((item) => {
      if (!item || item.displayOnly || !item.url) return;
      wanted.add(String(item.url));
    });
    for (const key of Array.from(state.preloadedAudio.keys())) {
      if (wanted.has(key)) continue;
      const audio = state.preloadedAudio.get(key);
      try { if (audio) audio.removeAttribute('src'); } catch (_) {}
      try { if (audio) audio.load(); } catch (_) {}
      state.preloadedAudio.delete(key);
    }
    for (const url of wanted) {
      if (state.preloadedAudio.has(url)) continue;
      try {
        const audio = new Audio();
        audio.preload = 'auto';
        if (typeof prepareMobileInlineAudio === 'function') prepareMobileInlineAudio(audio);
        audio.src = url;
        audio.load();
        state.preloadedAudio.set(url, audio);
      } catch (_) {
        // Preload is an optimization only. Playback still uses the primary audio element.
      }
    }
  }

  function clearPreloadedAudioInternal() {
    if (!state.preloadedAudio || typeof state.preloadedAudio.forEach !== 'function') return;
    state.preloadedAudio.forEach((audio) => {
      try { if (audio) audio.removeAttribute('src'); } catch (_) {}
      try { if (audio) audio.load(); } catch (_) {}
    });
    state.preloadedAudio.clear();
  }

  function resetCurrentInternal() {
    const clearingSessionId = state.currentSessionId;
    if (state.tailTimer) clearTimeout(state.tailTimer);
    state.tailTimer = null;
    state.tailActive = false;
    stopLipSyncInternal(state.currentCharacterId);
    state.playing = false;
    state.currentCharacterId = '';
    state.currentText = '';
    state.currentDisplayText = '';
    state.currentSessionId = '';
    state.currentChunkIndex = -1;
    state.currentUtteranceId = '';
    state.currentResponseId = '';
    state.currentMessageId = '';
    state.currentTurnIndex = -1;
    state.currentShown = false;
    state.blockedFallbackUtteranceId = '';
    setNowPlayingText('', '');
    clearTextInternal(clearingSessionId);
  }

  function rememberInterruptedChatOutput(item) {
    if (!item || isIdleChatPlaybackItem(item)) return;
    const sid = String(item.sessionId || '').trim();
    const rid = String(item.responseId || '').trim();
    if (sid) interruptedChatSessions.add(sid);
    if (rid) {
      interruptedChatResponses.add(rid);
      clearResponsePlaybackLifecycle(rid);
      acknowledgedResponses.add(rid);
    }
  }

  function isInterruptedChatOutput(item) {
    if (!item || isIdleChatPlaybackItem(item)) return false;
    const sid = String(item.sessionId || '').trim();
    const rid = String(item.responseId || '').trim();
    return Boolean((sid && interruptedChatSessions.has(sid)) || (rid && interruptedChatResponses.has(rid)));
  }

  function resetChatInternal(reason) {
    const current = currentAudioItemInternal();
    const currentIsChat = state.currentSessionId && !isIdleChatSessionId(state.currentSessionId);
    if (currentIsChat) rememberInterruptedChatOutput(current);
    if (centralTTSSpeech && (centralTTSSpeech.sessionId || centralTTSSpeech.responseId)) {
      rememberInterruptedChatOutput({
        sessionId: centralTTSSpeech.sessionId,
        responseId: centralTTSSpeech.responseId,
      });
    }
    state.queue = state.queue.filter((item) => {
      if (isIdleChatPlaybackItem(item)) return true;
      rememberInterruptedChatOutput(item);
      return false;
    });
    if (state.fallbackTimer) clearTimeout(state.fallbackTimer);
    state.fallbackTimer = null;
    state.fallbackActive = false;
    if (state.tailTimer) clearTimeout(state.tailTimer);
    state.tailTimer = null;
    state.tailActive = false;
    if (currentIsChat && state.audio) {
      try { state.audio.pause(); } catch (_) {}
      try { state.audio.removeAttribute('src'); } catch (_) {}
      try { state.audio.load(); } catch (_) {}
      resetCurrentInternal();
    }
    clearPreloadedAudioInternal();
    clearLipSyncSpeaking();
    setNowPlayingText('', '');
    resetTTSSpeechBubble(centralTTSSpeech);
    state.blockedFallbackUtteranceId = '';
    clearTTSAudioError();
    updateAudioButton();
  }

  function resetIdleChatInternal() {
    state.queue = state.queue.filter((item) => item && item.mode !== 'idlechat');
    if (state.fallbackTimer) clearTimeout(state.fallbackTimer);
    state.fallbackTimer = null;
    state.fallbackActive = false;
    if (state.tailTimer) clearTimeout(state.tailTimer);
    state.tailTimer = null;
    state.tailActive = false;
    if (state.currentSessionId && isIdleChatSessionId(state.currentSessionId)) {
      if (state.audio) {
        try { state.audio.pause(); } catch (_) {}
        try { state.audio.removeAttribute('src'); } catch (_) {}
        try { state.audio.load(); } catch (_) {}
      }
      resetCurrentInternal();
    }
    clearPreloadedAudioInternal();
    clearLipSyncSpeaking();
    setNowPlayingText('', '');
    clearTextInternal();
  }

  function disableAudioInternal() {
    if (state.playing && !state.currentShown && state.currentDisplayText) {
      showChunkTextInternal({
        characterId: state.currentCharacterId,
        displayText: state.currentDisplayText,
        text: state.currentText,
        sessionId: state.currentSessionId,
        chunkIndex: state.currentChunkIndex,
        utteranceId: state.currentUtteranceId,
        responseId: state.currentResponseId,
        messageId: state.currentMessageId,
        turnIndex: state.currentTurnIndex,
      });
    }
    if (state.audio) {
      try { state.audio.pause(); } catch (_) {}
      try { state.audio.removeAttribute('src'); } catch (_) {}
      try { state.audio.load(); } catch (_) {}
    }
    clearPreloadedAudioInternal();
    state.audioEnabled = false;
    state.unlocked = false;
    state.blocked = false;
    state.blockedFallbackUtteranceId = '';
    clearTTSAudioError();
    resetCurrentInternal();
    releaseViewerControl('audio', 'audio_disabled');
    updateAudioButton();
    startTextFallbackInternal();
  }

  async function unlockAudioInternal(options = {}) {
    state.audioEnabled = true;
    if (options.preferQueued && state.blocked && state.queue.length > 0) {
      state.blocked = false;
      state.unlocked = true;
      state.blockedFallbackUtteranceId = '';
      clearTTSAudioError();
      updateAudioButton();
      claimViewerControl('audio', 'speaker_unlock').then(() => {
        playNextInternal();
      });
      return;
    }
    const audio = ensureAudioInternal();
    try {
      audio.pause();
      audio.muted = false;
      audio.src = 'data:audio/wav;base64,UklGRigAAABXQVZFZm10IBAAAAABAAEAQB8AAEAfAAABAAgAZGF0YQQAAAAA';
      await audio.play();
      audio.pause();
      audio.currentTime = 0;
      audio.removeAttribute('src');
      audio.load();
      audio.muted = false;
      state.unlocked = true;
      state.blocked = false;
      state.blockedFallbackUtteranceId = '';
      clearTTSAudioError();
      updateAudioButton();
      claimViewerControl('audio', 'speaker_unlock').then(() => {
        playNextInternal();
      });
    } catch (err) {
      state.unlocked = false;
      state.blocked = true;
      setTTSAudioError(err);
      updateAudioButton();
      startTextFallbackInternal();
      console.error('tts audio unlock failed', err);
    }
  }

  function ensureAudioInternal() {
    if (!state.audio) {
      state.audio = new Audio();
      state.audio.preload = 'auto';
      if (typeof prepareMobileInlineAudio === 'function') prepareMobileInlineAudio(state.audio);
      if (typeof attachPlaybackAudioElement === 'function') attachPlaybackAudioElement(state.audio);
      state.audio.addEventListener('playing', markAudioStarted);
      state.audio.addEventListener('timeupdate', markAudioStarted);
      state.audio.addEventListener('ended', function() {
        completeCurrentAudioPlaybackInternal();
      });
      state.audio.addEventListener('error', function() {
        handleCurrentAudioFailureInternal(new Error('audio element error'));
      });
    }
    return state.audio;
  }

  function markAudioStarted() {
    if (!state.playing) return;
    if (state.currentShown) return;
    const audio = state.audio;
    if (audio && audio.currentTime <= 0 && audio.readyState < HTMLMediaElement.HAVE_CURRENT_DATA) return;
    state.currentShown = true;
    if (typeof recordLatencyMetric === 'function') {
      recordLatencyMetric('tts', 'audio_play_start', {
        detail: 'chunk=' + String(state.currentChunkIndex),
        job: state.currentResponseId,
        session: state.currentSessionId,
      });
    }
    startLipSyncInternal(state.currentCharacterId);
    setNowPlayingText(state.currentCharacterId, state.currentText);
    showChunkTextInternal({
      characterId: state.currentCharacterId,
      displayText: state.currentDisplayText,
      text: state.currentText,
      sessionId: state.currentSessionId,
      chunkIndex: state.currentChunkIndex,
      utteranceId: state.currentUtteranceId,
      responseId: state.currentResponseId,
      messageId: state.currentMessageId,
      turnIndex: state.currentTurnIndex,
    });
  }

  function showFallbackChunkInternal(item) {
    if (!item) return;
    showChunkTextInternal(item);
  }

  function showChunkTextInternal(item) {
    setCentralTTSSpeechText(
      String((item && item.characterId) || ''),
      String((item && (item.displayText || item.text)) || ''),
      String((item && item.sessionId) || ''),
      Number.isFinite(item && item.chunkIndex) ? item.chunkIndex : -1,
      String((item && item.utteranceId) || ''),
      String((item && item.responseId) || ''),
      String((item && item.messageId) || ''),
      Number.isFinite(item && item.turnIndex) ? item.turnIndex : -1
    );
  }

  function clearTextInternal(sessionId) {
    const sid = String(sessionId || '').trim();
    if (!sid) {
      resetCentralTTSSpeechBubble();
      return;
    }
    setTTSSpeechText(isIdleChatSessionId(sid) ? 'idle' : 'central', '', '', sid);
  }

  function startLipSyncInternal(characterId) {
    setLipSyncSpeaking(characterId, true);
    // Set speaking expression when starting to speak
    const id = String(characterId || '').trim().toLowerCase();
    if (id === 'mio' || id === 'shiro') {
      setCharacterExpression(id, 'speaking');
    }
  }

  function stopLipSyncInternal(characterId) {
    setLipSyncSpeaking(characterId, false);
    // Return to idle expression when finished speaking
    const id = String(characterId || '').trim().toLowerCase();
    if (id === 'mio' || id === 'shiro') {
      setCharacterExpression(id, 'idle');
    }
  }

  function startTextFallbackInternal() {
    if (state.playing || state.fallbackActive) return;
    if (state.queue.length === 0) {
      clearTextInternal();
      return;
    }
    const head = state.queue[0];
    if (state.blocked && head && !head.displayOnly) {
      showBlockedAudioTextFallbackInternal(head);
      return;
    }
    if (state.audioEnabled && !state.blocked && !(head && head.displayOnly)) return;
    const next = state.queue.shift();
    const idleChatFallback = isIdleChatPlaybackItem(next);
    const fallbackErr = idleChatFallback ? new Error(next && next.error ? next.error : (next && next.displayOnly ? 'missing idlechat audio url' : 'idlechat audio disabled')) : null;
    state.fallbackActive = true;
    if (idleChatFallback) {
      renderIdlePlaybackErrorInternal(next, next && next.errorCode ? next.errorCode : (next && next.displayOnly ? 'TTS_AUDIO_MISSING' : 'TTS_AUDIO_DISABLED'), next && next.error ? next.error : (next && next.displayOnly ? 'TTS audio chunk did not include a playable audio URL.' : 'IdleChat audio playback was disabled before this chunk could be spoken.'));
      recordResponsePlaybackResult(next, 'error', fallbackErr);
    } else {
      showFallbackChunkInternal(next);
    }
    if (state.fallbackTimer) clearTimeout(state.fallbackTimer);
    state.fallbackTimer = setTimeout(function() {
      state.fallbackTimer = null;
      state.fallbackActive = false;
      decrementResponsePlaybackCount(next.responseId);
      maybeAcknowledgeResponsePlayback(next, 'error', fallbackErr || new Error('tts audio fallback is not a successful playback path'));
      const nextHead = state.queue[0];
      if (state.blocked || (nextHead && nextHead.displayOnly)) {
        startTextFallbackInternal();
        return;
      }
      playNextInternal();
    }, ttsTextFallbackDelay(next));
  }

  function showBlockedAudioTextFallbackInternal(item) {
    if (!item) return;
    const key = String(item.utteranceId || item.sessionId + ':' + String(item.chunkIndex) || item.seq || '');
    if (key && state.blockedFallbackUtteranceId === key) return;
    state.blockedFallbackUtteranceId = key;
    if (isIdleChatPlaybackItem(item)) {
      renderIdlePlaybackErrorInternal(item, 'TTS_AUDIO_BLOCKED', 'Browser blocked IdleChat audio playback; Viewer did not render fallback speech text.');
    } else {
      showFallbackChunkInternal(item);
    }
    scheduleBlockedAudioAckInternal(item, new Error('browser blocked autoplay'));
  }

  function scheduleBlockedAudioAckInternal(item, err) {
    if (!item || !item.responseId) return;
    const key = ttsChunkIdentityKey(item.sessionId, item.utteranceId, item.chunkIndex, item.seq);
    if (key && blockedAckKeys.has(key)) return;
    if (key) blockedAckKeys.add(key);
    recordResponsePlaybackResult(item, 'error', err);
    setTimeout(function() {
      decrementResponsePlaybackCount(item.responseId);
      maybeAcknowledgeResponsePlayback(item, 'error', err);
    }, ttsDisplayDelay(item));
  }

  function currentAudioItemInternal(fallback) {
    return {
      characterId: state.currentCharacterId || String((fallback && fallback.characterId) || ''),
      displayText: state.currentDisplayText || String((fallback && fallback.displayText) || ''),
      text: state.currentText || String((fallback && fallback.text) || ''),
      sessionId: state.currentSessionId || String((fallback && fallback.sessionId) || ''),
      chunkIndex: state.currentChunkIndex >= 0 ? state.currentChunkIndex : (Number.isFinite(fallback && fallback.chunkIndex) ? fallback.chunkIndex : -1),
      utteranceId: state.currentUtteranceId || String((fallback && fallback.utteranceId) || ''),
      responseId: state.currentResponseId || String((fallback && fallback.responseId) || ''),
      messageId: state.currentMessageId || String((fallback && fallback.messageId) || ''),
      turnIndex: state.currentTurnIndex >= 0 ? state.currentTurnIndex : (Number.isFinite(fallback && fallback.turnIndex) ? fallback.turnIndex : -1),
    };
  }

  function handleCurrentAudioFailureInternal(err, fallback) {
    const item = currentAudioItemInternal(fallback);
    stopLipSyncInternal(item.characterId);
    if (!state.currentShown) {
      if (isIdleChatPlaybackItem(item)) {
        renderIdlePlaybackErrorInternal(item, 'TTS_AUDIO_PLAYBACK_ERROR', 'IdleChat audio element failed before playback could be confirmed.');
      } else {
        showFallbackChunkInternal(item);
      }
    }
    if (err) setTTSAudioError(err);
    recordResponsePlaybackResult(item, 'error', err);
    updateAudioButton();
    if (state.fallbackTimer) clearTimeout(state.fallbackTimer);
    state.fallbackActive = true;
    state.fallbackTimer = setTimeout(function() {
      state.fallbackTimer = null;
      state.fallbackActive = false;
      decrementResponsePlaybackCount(item.responseId);
      maybeAcknowledgeResponsePlayback(item, 'error', err);
      resetCurrentInternal();
      playNextInternal();
    }, ttsDisplayDelay(item));
  }

  function isIdleChatPlaybackItem(item) {
    return !!item && (item.mode === 'idlechat' || isIdleChatSessionId(item.sessionId));
  }

  function renderIdlePlaybackErrorInternal(item, errorCode, reason) {
    if (!isIdleChatPlaybackItem(item)) return;
    if (typeof renderIdleTTSChunkError !== 'function') return;
    renderIdleTTSChunkError(item, errorCode, reason);
  }

  function completeCurrentAudioPlaybackInternal() {
    const finished = currentAudioItemInternal();
    stopLipSyncInternal(finished.characterId);
    state.playing = false;
    state.currentCharacterId = '';
    state.currentText = '';
    state.currentDisplayText = '';
    state.currentSessionId = '';
    state.currentChunkIndex = -1;
    state.currentUtteranceId = '';
    state.currentResponseId = '';
    state.currentShown = false;
    state.blockedFallbackUtteranceId = '';
    setNowPlayingText('', '');
    clearTextInternal(finished.sessionId);
    const delay = ttsPlaybackTailGap(finished, state.queue[0]);
    decrementResponsePlaybackCount(finished.responseId);
    maybeAcknowledgeResponsePlayback(finished, 'ended');
    if (state.tailTimer) clearTimeout(state.tailTimer);
    state.tailActive = true;
    state.tailTimer = setTimeout(function() {
      state.tailTimer = null;
      state.tailActive = false;
      playNextInternal();
    }, delay);
  }

  function playNextInternal() {
    if (state.playing) return;
    if (state.fallbackActive) return;
    if (state.tailActive) return;
    if (!state.audioEnabled) {
      startTextFallbackInternal();
      return;
    }
    if (state.queue.length > 0 && state.queue[0].displayOnly) {
      startTextFallbackInternal();
      return;
    }
    if (state.blocked) {
      showBlockedAudioTextFallbackInternal(state.queue[0]);
      return;
    }
    if (state.queue.length === 0) {
      module.lipSync.clear();
      setNowPlayingText('', '');
      clearTextInternal();
      return;
    }
    const next = state.queue[0];
    if (!canStartChunk(next)) return;
    state.queue.shift();
    const audio = ensureAudioInternal();
    state.playing = true;
    state.currentCharacterId = String((next && next.characterId) || '');
    state.currentText = String((next && next.text) || '');
    state.currentDisplayText = String((next && next.displayText) || state.currentText || '');
    state.currentSessionId = String((next && next.sessionId) || '');
    state.currentChunkIndex = Number.isFinite(next && next.chunkIndex) ? next.chunkIndex : -1;
    state.currentUtteranceId = String((next && next.utteranceId) || '');
    state.currentResponseId = String((next && next.responseId) || '');
    state.currentMessageId = String((next && next.messageId) || '');
    state.currentTurnIndex = Number.isFinite(next && next.turnIndex) ? next.turnIndex : -1;
    state.currentShown = false;
    audio.dataset.characterId = state.currentCharacterId;
    if (next && next.url && state.preloadedAudio) {
      state.preloadedAudio.delete(String(next.url));
    }
    audio.src = String((next && next.url) || '');
    preloadQueuedAudioInternal();
    audio.play().then(function() {
      markAudioStarted();
      state.audioEnabled = true;
      state.unlocked = true;
      state.blocked = false;
      state.blockedFallbackUtteranceId = '';
      clearTTSAudioError();
      updateAudioButton();
    }).catch(function(err) {
      if (isAutoplayBlockedError(err)) {
        state.blocked = true;
        state.unlocked = false;
        state.queue.unshift(next);
        resetCurrentInternal();
        setTTSAudioError(err);
        console.error('tts audio play failed', err);
        updateAudioButton();
        showBlockedAudioTextFallbackInternal(state.queue[0]);
      } else {
        state.blocked = false;
        console.error('tts audio play failed', err);
        handleCurrentAudioFailureInternal(err, next);
      }
    });
  }
}

const chatAudioSync = createChatAudioSync();

function updateLabAudioButton(status) {
  if (!labAudioBtn) return;
  const controlState = ttsPlayback.audioEnabled ? 'TTS_ON' : 'TTS_OFF';
  labAudioBtn.classList.remove('ready', 'blocked', 'off');
  if (status) labAudioBtn.classList.add(status);
  labAudioBtn.dataset.state = controlState;
  labAudioBtn.dataset.controlState = controlState;
  labAudioBtn.setAttribute('aria-pressed', ttsPlayback.audioEnabled ? 'true' : 'false');
  if (!ttsPlayback.audioEnabled) {
    labAudioBtn.title = 'TTS_OFF';
    labAudioBtn.setAttribute('aria-label', 'TTS_OFF');
  } else if (ttsPlayback.blocked) {
    labAudioBtn.title = 'TTS_ON / ブラウザ再許可待ち';
    labAudioBtn.setAttribute('aria-label', 'TTS_ON');
  } else if (ttsPlayback.unlocked) {
    labAudioBtn.title = 'TTS_ON';
    labAudioBtn.setAttribute('aria-label', 'TTS_ON');
  } else {
    labAudioBtn.title = 'TTS_ON / クリックで音声を有効化';
    labAudioBtn.setAttribute('aria-label', 'TTS_ON');
  }
}

function updateAudioButton() {
  const status = !ttsPlayback.audioEnabled ? 'off' : (ttsPlayback.blocked ? 'blocked' : (ttsPlayback.unlocked ? 'ready' : ''));
  [audioBtn, liveAudioBtn].forEach(function(btn) {
    if (!btn) return;
    btn.classList.remove('ready', 'blocked', 'off');
    if (status) btn.classList.add(status);
    btn.textContent = ttsPlayback.audioEnabled ? '🔊' : '🔇';
    if (!ttsPlayback.audioEnabled) {
      btn.title = '音声はOFFです。タップしてON';
      btn.setAttribute('aria-label', '音声はOFFです。タップしてON');
    } else if (ttsPlayback.blocked) {
      const detail = ttsPlayback.audioError ? ' / ' + ttsPlayback.audioError : '';
      btn.title = '音声がブロックされています。タップして再許可' + detail;
      btn.setAttribute('aria-label', '音声がブロックされています。タップして再許可' + detail);
    } else if (ttsPlayback.unlocked) {
      btn.title = '音声は有効です';
      btn.setAttribute('aria-label', '音声は有効です');
    } else {
      btn.title = '音声を有効化';
      btn.setAttribute('aria-label', '音声を有効化');
    }
  });
  updateLabAudioButton(status);
}

function bindTTSAudioButton(btn) {
  if (btn) {
    btn.addEventListener('click', toggleTTSAudio);
  }
}

function resetCurrentTTSAudioState() {
  chatAudioSync.resetCurrent();
}

function disableTTSAudio() {
  chatAudioSync.disableAudio();
}

async function unlockTTSAudio(options = {}) {
  await chatAudioSync.unlockAudio(options);
}

async function toggleTTSAudio() {
  if (ttsPlayback.audioEnabled && ttsPlayback.unlocked && !ttsPlayback.blocked) {
    disableTTSAudio();
    return;
  }
  await unlockTTSAudio({preferQueued: isMobileControlViewport()});
}

function ensureTTSAudio() {
  return chatAudioSync.ensureAudio();
}

function bindMobileTTSAudioAutounlock() {
  if (!document || !document.addEventListener) return;
  let attempted = false;
  document.addEventListener('pointerdown', function() {
    if (attempted) return;
    if (!isMobileControlViewport()) return;
    if (!ttsPlayback.audioEnabled || ttsPlayback.unlocked) return;
    attempted = true;
    unlockTTSAudio().catch(function(err) {
      attempted = false;
      console.warn('mobile tts audio unlock skipped:', err);
    });
  }, {passive: true});
}

function bindViewerActiveControlLifecycle() {
  if (typeof setInterval === 'function') {
    setInterval(function() {
      if (viewerControl.activeAudioViewerId === viewerControl.clientId && (ttsPlayback.audioEnabled || ttsPlayback.playing || ttsPlayback.queue.length > 0)) {
        heartbeatViewerControl('audio', 'audio_heartbeat');
      }
    }, 15000);
  }
  const releaseAudioOwner = function() {
    if (viewerControl.activeAudioViewerId === viewerControl.clientId) {
      releaseViewerControl('audio', 'viewer_unload');
    }
  };
  if (typeof window !== 'undefined' && window && typeof window.addEventListener === 'function') {
    window.addEventListener('pagehide', releaseAudioOwner);
    window.addEventListener('beforeunload', releaseAudioOwner);
  }
}

function isAutoplayBlockedError(err) {
  if (!err) return false;
  const name = String(err.name || '').trim();
  const msg = String(err.message || '').toLowerCase();
  if (name === 'NotAllowedError') return true;
  return msg.includes('user didn\'t interact') || msg.includes('notallowederror');
}

function isMobileControlViewport() {
  if (typeof window === 'undefined') return false;
  if (window.matchMedia && window.matchMedia('(max-width: 640px)').matches) return true;
  return Boolean(navigator.maxTouchPoints && window.innerWidth <= 900);
}

function prepareMobileInlineAudio(audio) {
  if (!audio) return;
  audio.playsInline = true;
  audio.setAttribute('playsinline', '');
  audio.setAttribute('webkit-playsinline', '');
}

function attachPlaybackAudioElement(audio) {
  if (!audio || audio.dataset.rencrowPlaybackAttached === '1') return;
  audio.dataset.rencrowPlaybackAttached = '1';
  audio.setAttribute('aria-hidden', 'true');
  audio.style.position = 'fixed';
  audio.style.width = '1px';
  audio.style.height = '1px';
  audio.style.opacity = '0';
  audio.style.pointerEvents = 'none';
  audio.style.left = '-10000px';
  audio.style.bottom = '0';
  try {
    document.body.appendChild(audio);
  } catch (_) {}
}

function ensureVoiceChatForMobileControl() {
  if (isVoiceChatAllowed()) return true;
  if (!isMobileControlViewport()) return false;
  switchTab('timeline');
  return isVoiceChatAllowed();
}

function markTTSAudioStarted() {
  chatAudioSync.markAudioStarted();
}

function ttsDisplayDelay(item) {
  const text = String((item && (item.displayText || item.text)) || '');
  const len = Array.from(text).length;
  const punctuationPause = /[。！？!?]$/.test(text.trim()) ? 280 : 0;
  return Math.max(900, Math.min(3400, 520 + (len * 85) + punctuationPause));
}

function ttsTextFallbackDelay(item) {
  if (!ttsPlayback.audioEnabled) return 500;
  return ttsDisplayDelay(item);
}

function ttsPlaybackTailGap(finished, next) {
  if (!next) return 180;
  const finishedSpeaker = String((finished && finished.characterId) || '');
  const nextSpeaker = String((next && next.characterId) || '');
  if (finishedSpeaker && nextSpeaker && finishedSpeaker !== nextSpeaker) return 420;
  const finishedSession = String((finished && finished.sessionId) || '');
  const nextSession = String((next && next.sessionId) || '');
  if (finishedSession && nextSession && finishedSession !== nextSession) return 260;
  const text = String((finished && (finished.displayText || finished.text)) || '').trim();
  if (/[。！？!?]$/.test(text)) return 240;
  if (/[、,]$/.test(text)) return 160;
  return 180;
}

function showTTSFallbackChunk(item) {
  chatAudioSync.showFallbackChunk(item);
}

function startTTSTextFallback() {
  chatAudioSync.startTextFallback();
}

function enqueueTTSAudio(url, characterId, sessionId, track, chunkIndex, text, displayText, responseId, utteranceId) {
  chatAudioSync.enqueueAudio({
    url: url,
    characterId: characterId || '',
    sessionId: sessionId || '',
    track: track || 'default',
    chunkIndex: Number.isFinite(chunkIndex) ? chunkIndex : -1,
    text: text || '',
    displayText: displayText || text || '',
    responseId: responseId || '',
    utteranceId: utteranceId || '',
  });
}

function enqueueTTSDisplayFallback(characterId, sessionId, track, chunkIndex, text, displayText, responseId, utteranceId) {
  chatAudioSync.enqueueDisplayFallback({
    url: '',
    characterId: characterId || '',
    sessionId: sessionId || '',
    track: track || 'default',
    chunkIndex: Number.isFinite(chunkIndex) ? chunkIndex : -1,
    text: text || '',
    displayText: displayText || text || '',
    responseId: responseId || '',
    utteranceId: utteranceId || '',
  });
}

function playNextTTSAudio() {
  chatAudioSync.playNext();
}

const inp = document.getElementById('inp');
const labInp = document.getElementById('labInp');
const sendBtn = document.getElementById('sendBtn');
const attachBtn = document.getElementById('attachBtn');
const screenBtn = document.getElementById('screenBtn');
const cameraBtn = document.getElementById('cameraBtn');
const repairBtn = document.getElementById('repairBtn');
const labAttachBtn = document.getElementById('labAttachBtn');
const labScreenBtn = document.getElementById('labScreenBtn');
const labCameraBtn = document.getElementById('labCameraBtn');
const labDateTimePanel = document.getElementById('labDateTimePanel');
const attachInput = document.getElementById('attachInput');
const cameraInput = document.getElementById('cameraInput');
const attachmentTray = document.getElementById('attachmentTray');
const cameraCaptureModal = document.getElementById('cameraCaptureModal');
const cameraCaptureVideo = document.getElementById('cameraCaptureVideo');
const cameraCaptureStatus = document.getElementById('cameraCaptureStatus');
const cameraCaptureCloseBtn = document.getElementById('cameraCaptureCloseBtn');
const cameraPhotoBtn = document.getElementById('cameraPhotoBtn');
const cameraFrameStartBtn = document.getElementById('cameraFrameStartBtn');
const cameraFrameStopBtn = document.getElementById('cameraFrameStopBtn');
const labCameraLivePreview = document.getElementById('labCameraLivePreview');
const labCameraLiveVideo = document.getElementById('labCameraLiveVideo');

function syncLabDateTimePanelLayout() {
  if (!labDateTimePanel) return;
  const indicator = document.getElementById('labModeIndicator');
  const topic = document.getElementById('liveTopicBar');
  if (!indicator || !topic) return;
  const indicatorRect = indicator.getBoundingClientRect();
  const topicRect = topic.getBoundingClientRect();
  if (indicatorRect.width <= 0 || topicRect.width <= 0) return;
  const gap = 8;
  const height = Math.max(24, Math.round(indicatorRect.top - topicRect.top - gap));
  labDateTimePanel.style.left = `${Math.round(indicatorRect.left)}px`;
  labDateTimePanel.style.width = `${Math.round(indicatorRect.width)}px`;
  labDateTimePanel.style.top = `${Math.round(topicRect.top)}px`;
  labDateTimePanel.style.height = `${height}px`;
}

function refreshLabDateTimePanel() {
  if (!labDateTimePanel) return;
  const now = new Date();
  labDateTimePanel.textContent = formatLabDateTime(now);
  labDateTimePanel.setAttribute('datetime', now.toISOString());
  syncLabDateTimePanelLayout();
}

refreshLabDateTimePanel();
if (labDateTimePanel && typeof window !== 'undefined') {
  window.setInterval(refreshLabDateTimePanel, 1000);
  window.addEventListener('resize', syncLabDateTimePanelLayout);
  if (typeof window.requestAnimationFrame === 'function') {
    window.requestAnimationFrame(syncLabDateTimePanelLayout);
  }
}

bindTTSAudioButton(audioBtn);
bindTTSAudioButton(liveAudioBtn);
bindTTSAudioButton(labAudioBtn);
bindMobileTTSAudioAutounlock();
bindViewerActiveControlLifecycle();
updateAudioButton();
let sending = false;
let viewerAttachments = [];
let suppressInputInterrupt = false;
let lastIdleStopAt = 0;
const cameraCapture = {
  stream: null,
  sourceType: '',
  frameTimer: null,
  frameCapturing: false,
  frameCaptureBusy: false,
  frameIndex: 0,
  opening: false,
};
// Current camera input emits browser-side JPEG still frames for Gemma4.
// Future transport can switch this session to one-second video chunks when
// RenCrow_LLM and the backend accept input_video/video_url without schema errors.
const CAMERA_FRAME_INTERVAL_MS = 1000;
const CAMERA_FRAME_MAX_COUNT = 60;
const CAMERA_FRAME_MAX_DIMENSION = 1280;
const CAMERA_FRAME_JPEG_QUALITY = 0.86;
function autoResize() {
  inp.style.height = 'auto';
  inp.style.height = Math.min(inp.scrollHeight, 120) + 'px';
}
function autoResizeLabInput() {
  if (!labInp) return;
  labInp.style.height = 'auto';
  labInp.style.height = Math.min(labInp.scrollHeight, 56) + 'px';
}
function syncLabInputToMain() {
  if (!labInp || !inp) return;
  if (inp.value !== labInp.value) inp.value = labInp.value;
  autoResize();
  autoResizeLabInput();
}
function syncMainInputToLab() {
  if (!labInp || !inp) return;
  if (labInp.value !== inp.value) labInp.value = inp.value;
  autoResizeLabInput();
}
function interruptIdleChatForUserInput(reason) {
  const normalizedReason = String(reason || 'user_input').trim() || 'user_input';
  const now = Date.now();
  const mustStopNow = [
    'user_input',
    'paste',
    'composition_start',
    'chat_send',
    'stt_button',
    'stt_test_record',
    'stt_voice_start',
    'stt_voice_resume',
    'vds_voice_start',
  ].includes(normalizedReason);
  const shouldNotifyServer = mustStopNow || now - lastIdleStopAt > 500;
  lastIdleStopAt = now;
  state.idleChat.mode = '';
  state.idleChat.manualMode = false;
  state.idleChat.chatActive = false;
  state.idleChat.currentTopic = '';
  state.idleChat.interrupted = true;
  state.idleChat.interruptedAt = Date.now();
  state.idleChat.interruptedSessionId = String(typeof idleLiveActiveSessionId !== 'undefined' ? idleLiveActiveSessionId || '' : '').trim();
  if (typeof renderIdleChat === 'function') renderIdleChat();
  if (typeof chatAudioSync !== 'undefined' && chatAudioSync && typeof chatAudioSync.resetIdleChat === 'function') {
    chatAudioSync.resetIdleChat();
  }
  if (!shouldNotifyServer) return;
  fetch('/viewer/idlechat/stop', {
    method: 'POST',
    keepalive: true,
  }).catch((err) => {
    state.idleChat.controlError = 'IdleChat stop unavailable: ' + String(err && err.message ? err.message : err);
    if (typeof renderIdleChat === 'function') renderIdleChat();
    console.warn('[IdleChat] stop failed:', err);
  });
}
function interruptChatOutputForUserInput(reason) {
  if (typeof chatAudioSync !== 'undefined' && chatAudioSync && typeof chatAudioSync.resetChat === 'function') {
    chatAudioSync.resetChat(reason || 'user_input');
  }
  Object.keys(thinkingBubbles).forEach((jid) => removeThinking(jid));
}
function handleChatInputIntent(reason) {
  if (suppressInputInterrupt) return;
  interruptChatOutputForUserInput(reason);
  interruptIdleChatForUserInput(reason);
  if (sttState && sttState.isRecording) abortSTTImmediately('chat_input');
  if (vdsState && vdsState.isRecording) abortVDSImmediately('chat_input');
}
inp.addEventListener('beforeinput', () => handleChatInputIntent('user_input'));
inp.addEventListener('input', () => {
  handleChatInputIntent('user_input');
  autoResize();
  syncMainInputToLab();
});
inp.addEventListener('paste', () => handleChatInputIntent('paste'));
inp.addEventListener('compositionstart', () => handleChatInputIntent('composition_start'));
inp.addEventListener('keydown', (e) => {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault();
    send();
  }
});
sendBtn.addEventListener('click', send);
if (repairBtn) repairBtn.addEventListener('click', requestRepairFromChat);
if (typeof labInp !== 'undefined' && labInp) {
  labInp.addEventListener('beforeinput', () => handleChatInputIntent('user_input'));
  labInp.addEventListener('input', () => {
    syncLabInputToMain();
    handleChatInputIntent('user_input');
  });
  labInp.addEventListener('paste', () => handleChatInputIntent('paste'));
  labInp.addEventListener('compositionstart', () => handleChatInputIntent('composition_start'));
  labInp.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      syncLabInputToMain();
      send();
    }
  });
}
if (attachBtn && attachInput) attachBtn.addEventListener('click', () => attachInput.click());
if (cameraBtn) cameraBtn.addEventListener('click', toggleCameraCapture);
if (labAttachBtn && attachInput) labAttachBtn.addEventListener('click', () => attachInput.click());
if (screenBtn) screenBtn.addEventListener('click', toggleDisplayCapture);
if (labScreenBtn) labScreenBtn.addEventListener('click', toggleDisplayCapture);
if (labCameraBtn) labCameraBtn.addEventListener('click', toggleCameraCapture);
if (attachInput) attachInput.addEventListener('change', () => addViewerAttachments(attachInput.files, attachInput));
if (cameraInput) cameraInput.addEventListener('change', () => addViewerAttachments(cameraInput.files, cameraInput));
if (cameraCaptureCloseBtn) cameraCaptureCloseBtn.addEventListener('click', () => closeCameraCapture());
if (cameraPhotoBtn) cameraPhotoBtn.addEventListener('click', captureCameraPhoto);
if (cameraFrameStartBtn) cameraFrameStartBtn.addEventListener('click', startCameraFrameCapture);
if (cameraFrameStopBtn) cameraFrameStopBtn.addEventListener('click', stopCameraFrameCapture);

function setCameraCaptureStatus(text, kind) {
  if (!cameraCaptureStatus) return;
  cameraCaptureStatus.textContent = String(text || '').trim() || 'CAMERA_OFF';
  cameraCaptureStatus.classList.remove('recording', 'streaming', 'error');
  if (kind) cameraCaptureStatus.classList.add(kind);
}

function setCameraCaptureButtons() {
  const active = !!cameraCapture.stream;
  const frameCapturing = !!cameraCapture.frameCapturing;
  const sourceType = String(cameraCapture.sourceType || '').trim();
  [cameraBtn, labCameraBtn].forEach((btn) => {
    if (!btn) return;
    const cameraActive = active && sourceType === 'camera';
    btn.classList.toggle('ready', cameraActive);
    btn.classList.toggle('blocked', false);
    btn.classList.toggle('camera-on', cameraActive);
    btn.dataset.state = cameraActive ? 'CAMERA_ON' : 'CAMERA_OFF';
    btn.dataset.controlState = cameraActive ? 'CAMERA_ON' : 'CAMERA_OFF';
    btn.setAttribute('aria-pressed', cameraActive ? 'true' : 'false');
    btn.title = cameraActive ? 'CAMERA_ON' : 'CAMERA_OFF';
    btn.setAttribute('aria-label', btn.title);
  });
  [screenBtn, labScreenBtn].forEach((btn) => {
    if (!btn) return;
    const displayActive = active && sourceType === 'display';
    btn.classList.toggle('ready', displayActive);
    btn.classList.toggle('blocked', false);
    btn.classList.toggle('screen-on', displayActive);
    btn.dataset.state = displayActive ? 'DISPLAY_ON' : 'DISPLAY_OFF';
    btn.dataset.controlState = displayActive ? 'DISPLAY_ON' : 'DISPLAY_OFF';
    btn.setAttribute('aria-pressed', displayActive ? 'true' : 'false');
    btn.title = displayActive ? 'DISPLAY_ON' : 'DISPLAY_OFF';
    btn.setAttribute('aria-label', btn.title);
  });
  if (cameraPhotoBtn) cameraPhotoBtn.disabled = !active || frameCapturing;
  if (cameraFrameStartBtn) cameraFrameStartBtn.disabled = !active || frameCapturing;
  if (cameraFrameStopBtn) cameraFrameStopBtn.disabled = !frameCapturing;
  setLabCameraLivePreviewStream(cameraCapture.stream);
}

function setLabCameraLivePreviewStream(stream) {
  if (!labCameraLivePreview || !labCameraLiveVideo) return;
  const active = !!stream;
  labCameraLivePreview.classList.toggle('is-visible', active);
  if (!active) {
    try { labCameraLiveVideo.pause(); } catch (_) {}
    labCameraLiveVideo.srcObject = null;
    return;
  }
  if (labCameraLiveVideo.srcObject !== stream) labCameraLiveVideo.srcObject = stream;
  labCameraLiveVideo.muted = true;
  labCameraLiveVideo.playsInline = true;
  labCameraLiveVideo.setAttribute('playsinline', '');
  labCameraLiveVideo.play().catch(() => {});
}

function useLabCameraCompactPreview() {
  return !!(document.body && document.body.classList.contains('lab-mode') && document.body.classList.contains('live-mode'));
}

function activeCameraFrameVideo() {
  if (cameraCaptureVideo && cameraCaptureVideo.videoWidth > 0 && cameraCaptureVideo.videoHeight > 0) return cameraCaptureVideo;
  if (labCameraLiveVideo && labCameraLiveVideo.videoWidth > 0 && labCameraLiveVideo.videoHeight > 0) return labCameraLiveVideo;
  return cameraCaptureVideo || labCameraLiveVideo;
}

function getCameraCaptureConstraints() {
  return {
    video: {
      facingMode: { ideal: 'environment' },
      width: { ideal: 1280 },
      height: { ideal: 720 },
    },
    audio: {
      echoCancellation: true,
      noiseSuppression: true,
      autoGainControl: true,
    },
  };
}

function getDisplayCaptureConstraints() {
  return {
    video: true,
    audio: {
      echoCancellation: false,
      noiseSuppression: false,
      autoGainControl: false,
    },
  };
}

function toggleCameraCapture() {
  if (cameraCapture.stream && cameraCapture.sourceType === 'camera') {
    stopCameraCaptureStream();
    if (cameraCaptureModal) cameraCaptureModal.classList.add('hidden');
    return;
  }
  openCameraCapture();
}

function toggleDisplayCapture() {
  if (cameraCapture.stream && cameraCapture.sourceType === 'display') {
    stopCameraCaptureStream();
    if (cameraCaptureModal) cameraCaptureModal.classList.add('hidden');
    return;
  }
  openDisplayCapture();
}

function bindCaptureTrackLifecycle(stream) {
  if (!stream) return;
  stream.getTracks().forEach((track) => {
    track.addEventListener('ended', () => {
      if (!cameraCapture.stream) return;
      if (track.kind === 'video') {
        stopCameraCaptureStream();
        if (cameraCaptureModal) cameraCaptureModal.classList.add('hidden');
        return;
      }
      const liveTracks = cameraCapture.stream.getTracks().filter((candidate) => candidate.readyState === 'live');
      if (liveTracks.length === 0) {
        stopCameraCaptureStream();
        if (cameraCaptureModal) cameraCaptureModal.classList.add('hidden');
      } else {
        setCameraCaptureButtons();
      }
    });
  });
}

async function openCameraCapture() {
  if (cameraCapture.opening) return;
  cameraCapture.opening = true;
  try {
    if (!cameraCaptureModal || !cameraCaptureVideo) {
      if (cameraInput) cameraInput.click();
      return;
    }
    if (cameraCapture.stream) {
      cameraCaptureModal.classList.toggle('hidden', useLabCameraCompactPreview());
      if (!cameraCaptureVideo.srcObject) cameraCaptureVideo.srcObject = cameraCapture.stream;
      await cameraCaptureVideo.play().catch(() => {});
      setCameraCaptureButtons();
      return;
    }
    if (typeof navigator === 'undefined' || !navigator.mediaDevices || typeof navigator.mediaDevices.getUserMedia !== 'function') {
      setCameraCaptureStatus('CAMERA_UNAVAILABLE', 'error');
      showToast('カメラAPIが利用できません', 'error');
      if (cameraInput) cameraInput.click();
      return;
    }
    stopCameraCaptureStream();
    let stream;
    try {
      stream = await navigator.mediaDevices.getUserMedia(getCameraCaptureConstraints());
    } catch (err) {
      stream = await navigator.mediaDevices.getUserMedia({ video: true, audio: true });
    }
    cameraCapture.stream = stream;
    cameraCapture.sourceType = 'camera';
    bindCaptureTrackLifecycle(stream);
    cameraCaptureVideo.srcObject = stream;
    cameraCaptureVideo.muted = true;
    cameraCaptureVideo.playsInline = true;
    cameraCaptureVideo.setAttribute('playsinline', '');
    cameraCaptureModal.classList.toggle('hidden', useLabCameraCompactPreview());
    await cameraCaptureVideo.play().catch(() => {});
    const videoTracks = stream.getVideoTracks().length;
    const audioTracks = stream.getAudioTracks().length;
    setCameraCaptureStatus(`CAMERA_ON / MIC_${audioTracks > 0 ? 'ON' : 'OFF'} / still_frame=ready / video=${videoTracks} audio=${audioTracks}`);
    setCameraCaptureButtons();
  } catch (err) {
    const message = String(err && err.message ? err.message : err);
    setCameraCaptureStatus('CAMERA_ERROR / ' + message, 'error');
    showToast('カメラ/マイク入力を開始できません', 'error');
  } finally {
    cameraCapture.opening = false;
    setCameraCaptureButtons();
  }
}

async function openDisplayCapture() {
  if (cameraCapture.opening) return;
  cameraCapture.opening = true;
  try {
    if (!cameraCaptureModal || !cameraCaptureVideo) {
      showToast('画面/タブ入力UIが見つかりません', 'error');
      return;
    }
    if (typeof navigator === 'undefined' || !navigator.mediaDevices || typeof navigator.mediaDevices.getDisplayMedia !== 'function') {
      setCameraCaptureStatus('DISPLAY_UNAVAILABLE', 'error');
      showToast('画面/タブ入力APIが利用できません', 'error');
      return;
    }
    stopCameraCaptureStream();
    const stream = await navigator.mediaDevices.getDisplayMedia(getDisplayCaptureConstraints());
    cameraCapture.stream = stream;
    cameraCapture.sourceType = 'display';
    bindCaptureTrackLifecycle(stream);
    cameraCaptureVideo.srcObject = stream;
    cameraCaptureVideo.muted = true;
    cameraCaptureVideo.playsInline = true;
    cameraCaptureVideo.setAttribute('playsinline', '');
    cameraCaptureModal.classList.toggle('hidden', useLabCameraCompactPreview());
    await cameraCaptureVideo.play().catch(() => {});
    const videoTracks = stream.getVideoTracks().length;
    const audioTracks = stream.getAudioTracks().length;
    setCameraCaptureStatus(`DISPLAY_ON / AUDIO_${audioTracks > 0 ? 'ON' : 'OFF'} / still_frame=ready / video=${videoTracks} audio=${audioTracks}`);
    setCameraCaptureButtons();
  } catch (err) {
    const name = String(err && err.name ? err.name : '').trim();
    if (name === 'NotAllowedError' || name === 'AbortError') {
      setCameraCaptureStatus('DISPLAY_OFF');
    } else {
      setCameraCaptureStatus('DISPLAY_ERROR / ' + String(err && err.message ? err.message : err), 'error');
      showToast('画面/タブ入力を開始できません', 'error');
    }
  } finally {
    cameraCapture.opening = false;
    setCameraCaptureButtons();
  }
}

function stopCameraCaptureStream() {
  stopCameraFrameCapture();
  if (cameraCapture.stream) {
    cameraCapture.stream.getTracks().forEach((track) => {
      try { track.stop(); } catch (_) {}
    });
  }
  cameraCapture.stream = null;
  cameraCapture.sourceType = '';
  if (cameraCaptureVideo) cameraCaptureVideo.srcObject = null;
  setCameraCaptureStatus('CAMERA_OFF');
  setCameraCaptureButtons();
}

function closeCameraCapture() {
  stopCameraCaptureStream();
  if (cameraCaptureModal) cameraCaptureModal.classList.add('hidden');
}

function startCameraFrameCapture() {
  if (!activeCameraFrameVideo() || !cameraCapture.stream) {
    showToast('カメラ入力が開始されていません', 'error');
    return;
  }
  if (cameraCapture.frameCapturing) return;
  cameraCapture.frameCapturing = true;
  cameraCapture.frameCaptureBusy = false;
  cameraCapture.frameIndex = 0;
  setCameraCaptureStatus(`FRAME_CAPTURE / 1FPS / 0/${CAMERA_FRAME_MAX_COUNT}`, 'streaming');
  setCameraCaptureButtons();
  captureCameraFrameTick();
  cameraCapture.frameTimer = window.setInterval(captureCameraFrameTick, CAMERA_FRAME_INTERVAL_MS);
}

function stopCameraFrameCapture() {
  if (cameraCapture.frameTimer) {
    window.clearInterval(cameraCapture.frameTimer);
    cameraCapture.frameTimer = null;
  }
  if (cameraCapture.frameCapturing) {
    const count = cameraCapture.frameIndex;
    cameraCapture.frameCapturing = false;
    cameraCapture.frameCaptureBusy = false;
    setCameraCaptureStatus(`FRAMES_ATTACHED / ${count} still image(s) / CAMERA_ON`);
    setCameraCaptureButtons();
  }
}

async function captureCameraFrameTick() {
  if (!cameraCapture.frameCapturing || cameraCapture.frameCaptureBusy) return;
  if (cameraCapture.frameIndex >= CAMERA_FRAME_MAX_COUNT) {
    stopCameraFrameCapture();
    return;
  }
  cameraCapture.frameCaptureBusy = true;
  try {
    const nextIndex = cameraCapture.frameIndex + 1;
    const file = await createCameraFrameFile(`camera_frame_${String(nextIndex).padStart(3, '0')}_${Date.now()}.jpg`);
    addViewerAttachments([file]);
    cameraCapture.frameIndex = nextIndex;
    setCameraCaptureStatus(`FRAME_CAPTURE / 1FPS / ${cameraCapture.frameIndex}/${CAMERA_FRAME_MAX_COUNT}`, 'streaming');
    if (cameraCapture.frameIndex >= CAMERA_FRAME_MAX_COUNT) stopCameraFrameCapture();
  } catch (err) {
    setCameraCaptureStatus('FRAME_CAPTURE_ERROR / ' + String(err && err.message ? err.message : err), 'error');
    showToast('静止画フレームを作成できません', 'error');
    stopCameraFrameCapture();
  } finally {
    cameraCapture.frameCaptureBusy = false;
  }
}

function createCameraFrameFile(filename) {
  return new Promise((resolve, reject) => {
    const blob = captureCameraFrameBlobSync();
    if (!blob) {
      reject(new Error('capture failed'));
      return;
    }
    resolve(new File([blob], filename, { type: 'image/jpeg' }));
  });
}

function captureCameraFrameBlobSync() {
  const video = activeCameraFrameVideo();
  if (!video || !cameraCapture.stream) return null;
  const width = video.videoWidth || 1280;
  const height = video.videoHeight || 720;
  if (width <= 0 || height <= 0) return null;
  const scale = Math.min(1, CAMERA_FRAME_MAX_DIMENSION / Math.max(width, height));
  const outWidth = Math.max(1, Math.round(width * scale));
  const outHeight = Math.max(1, Math.round(height * scale));
  const canvas = document.createElement('canvas');
  canvas.width = outWidth;
  canvas.height = outHeight;
  const ctx = canvas.getContext('2d');
  if (!ctx) return null;
  ctx.drawImage(video, 0, 0, outWidth, outHeight);
  const dataURL = canvas.toDataURL('image/jpeg', CAMERA_FRAME_JPEG_QUALITY);
  const parts = String(dataURL || '').split(',');
  if (parts.length < 2) return null;
  const binary = atob(parts[1]);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
  return new Blob([bytes], { type: 'image/jpeg' });
}

async function captureCameraPhoto() {
  if (!activeCameraFrameVideo() || !cameraCapture.stream) return;
  try {
    const file = await createCameraFrameFile(`camera_${Date.now()}.jpg`);
    addViewerAttachments([file]);
    setCameraCaptureStatus('PHOTO_ATTACHED / CAMERA_ON');
    showToast('カメラ画像を添付しました', 'success');
  } catch (err) {
    setCameraCaptureStatus('PHOTO_CAPTURE_ERROR / ' + String(err && err.message ? err.message : err), 'error');
    showToast('カメラ画像を作成できません', 'error');
  }
}

function addViewerAttachments(files, input) {
  state.viewerAttachmentError = '';
  Array.from(files || []).forEach((file) => {
    if (!viewerAttachmentAccepted(file)) {
      const name = String(file && file.name ? file.name : 'attachment');
      state.viewerAttachmentError = 'Attachment unavailable: unsupported file type: ' + name;
      showToast('未対応の添付形式です', 'error');
      return;
    }
    state.viewerAttachmentError = '';
    viewerAttachments.push(file);
  });
  if (input) input.value = '';
  renderAttachmentTray();
}

function viewerAttachmentAccepted(file) {
  const type = String(file && file.type || '').toLowerCase();
  const name = String(file && file.name || '').toLowerCase();
  return type.startsWith('image/') || type.startsWith('video/') || type.startsWith('audio/') || type === 'application/pdf' || type.startsWith('text/') ||
    /\.(txt|md|json|csv|yaml|yml|mp4|mov|webm|m4v|mp3|wav|m4a|ogg)$/.test(name);
}

function renderAttachmentTray() {
  if (!attachmentTray) return;
  attachmentTray.innerHTML = '';
  attachmentTray.classList.toggle('has-items', viewerAttachments.length > 0 || Boolean(state.viewerAttachmentError));
  if (state.viewerAttachmentError) {
    const err = document.createElement('span');
    err.className = 'attachment-chip attachment-error';
    err.textContent = state.viewerAttachmentError;
    attachmentTray.appendChild(err);
  }
  viewerAttachments.forEach((file, index) => {
    const chip = document.createElement('span');
    chip.className = 'attachment-chip';
    const name = document.createElement('span');
    name.className = 'name';
    name.textContent = file.name || 'attachment';
    const size = document.createElement('span');
    size.className = 'size';
    size.textContent = formatAttachmentSize(file.size || 0);
    const remove = document.createElement('button');
    remove.className = 'attachment-remove';
    remove.type = 'button';
    remove.title = '添付を外す';
    remove.textContent = '×';
    remove.addEventListener('click', () => {
      viewerAttachments.splice(index, 1);
      renderAttachmentTray();
    });
    chip.append(name, size, remove);
    attachmentTray.appendChild(chip);
  });
}

function formatAttachmentSize(bytes) {
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1024 * 1024) return Math.round(bytes / 1024) + ' KiB';
  return Math.round(bytes / (1024 * 1024)) + ' MiB';
}

function send() {
  const text = inp.value.trim();
  const message = text;
  const attachments = viewerAttachments.slice();
  if ((!text && attachments.length === 0) || sending) return;
  if (typeof interruptChatOutputForUserInput === 'function') interruptChatOutputForUserInput('chat_send');
  if (typeof interruptIdleChatForUserInput === 'function') interruptIdleChatForUserInput('chat_send');
  sending = true;
  sendBtn.disabled = true;
  inp.disabled = true;
  if (typeof labInp !== 'undefined' && labInp) labInp.disabled = true;
  if (typeof attachBtn !== 'undefined' && attachBtn) attachBtn.disabled = true;
  if (typeof screenBtn !== 'undefined' && screenBtn) screenBtn.disabled = true;
  if (typeof cameraBtn !== 'undefined' && cameraBtn) cameraBtn.disabled = true;
  if (typeof labAttachBtn !== 'undefined' && labAttachBtn) labAttachBtn.disabled = true;
  if (typeof labScreenBtn !== 'undefined' && labScreenBtn) labScreenBtn.disabled = true;
  if (typeof labCameraBtn !== 'undefined' && labCameraBtn) labCameraBtn.disabled = true;

  const sendPromise = attachments.length > 0 ? sendViewerMessage(message, attachments) : sendViewerMessage(message);
  sendPromise
  .then(() => {
    inp.value = '';
    viewerAttachments = [];
    renderAttachmentTray();
    autoResize();
    if (typeof syncMainInputToLab === 'function') syncMainInputToLab();
  })
  .catch((err) => {
    const message = 'Viewer send unavailable: ' + String(err && err.message ? err.message : err);
    addMsgToTimeline({
      type: 'agent.response',
      from: 'mio',
      to: 'user',
      timestamp: new Date().toISOString(),
      content: message,
    });
    console.error(err);
  })
  .finally(() => {
    sending = false;
    sendBtn.disabled = false;
    inp.disabled = false;
    if (typeof labInp !== 'undefined' && labInp) labInp.disabled = false;
    if (typeof attachBtn !== 'undefined' && attachBtn) attachBtn.disabled = false;
    if (typeof screenBtn !== 'undefined' && screenBtn) screenBtn.disabled = false;
    if (typeof cameraBtn !== 'undefined' && cameraBtn) cameraBtn.disabled = false;
    if (typeof labAttachBtn !== 'undefined' && labAttachBtn) labAttachBtn.disabled = false;
    if (typeof labScreenBtn !== 'undefined' && labScreenBtn) labScreenBtn.disabled = false;
    if (typeof labCameraBtn !== 'undefined' && labCameraBtn) labCameraBtn.disabled = false;
    const isLabMode = typeof document !== 'undefined' && document.body && document.body.classList.contains('lab-mode');
    const focusTarget = typeof labInp !== 'undefined' && isLabMode && labInp ? labInp : inp;
    focusTarget.focus();
  });
}

async function sendViewerMessage(message, attachments = []) {
  const body = buildViewerSendRequest(message);
  if (!body.message && (!attachments || attachments.length === 0)) throw new Error('message or attachment is required');
  await ensureViewerLLMReadyForRequest(body);
  let request;
  if (attachments && attachments.length > 0) {
    const form = new FormData();
    Object.entries(body).forEach(([key, value]) => form.append(key, value || ''));
    attachments.forEach((file) => form.append('attachments[]', file, file.name));
    request = {method: 'POST', body: form};
  } else {
    request = {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(body),
    };
  }
  const sendStartedAt = typeof nowLatencyMS === 'function' ? nowLatencyMS() : Date.now();
  if (typeof recordLatencyMetric === 'function') {
    recordLatencyMetric('network', 'viewer_send_start', {
      atMS: sendStartedAt,
      detail: short(body.message || '', 80),
      session: body.session_id || 'viewer',
    });
  }
  const r = await fetch('/viewer/send', request);
  if (typeof recordLatencyMetric === 'function') {
    const sendFinishedAt = typeof nowLatencyMS === 'function' ? nowLatencyMS() : Date.now();
    recordLatencyMetric('network', 'viewer_send_response', {
      atMS: sendFinishedAt,
      valueMS: sendFinishedAt - sendStartedAt,
      detail: 'HTTP ' + String(r.status),
      session: body.session_id || 'viewer',
    });
  }
  if (!r.ok) {
    const text = await r.text();
    throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'send failed'));
  }
  return {ok: true};
}

async function requestRepairFromChat() {
  if (!repairBtn) return;
  const instruction = String(inp && inp.value ? inp.value : '').trim();
  repairBtn.disabled = true;
  try {
    const payload = await sendViewerRepairRun({
      reason: 'user-directed-repair',
      instruction: instruction || 'Mioが正常に応答できない可能性があります。直近ログを見て、Chat経路を診断し修復してください。',
      recent: 100,
      target_route: 'CHAT',
      target_agent: 'mio',
    });
    showToast('修復ジョブを受け付けました: ' + String(payload.job_id || ''), 'success');
    ingestJobNotification({
      type: 'repair',
      level: 'interrupt',
      job_id: String(payload.job_id || ''),
      title: '修復ジョブを受け付けました',
      assignee: 'shiro',
      route: 'OPS',
      status: 'requested',
      summary: String(payload.summary || '直近ログを見て修復します'),
      next_actions: ['ログ確認', '原因診断', '修復案作成'],
      interrupt: true,
      created_at: new Date().toISOString(),
    });
  } catch (err) {
    showToast('修復要求に失敗しました: ' + String(err && err.message ? err.message : err), 'error');
    console.error(err);
  } finally {
    repairBtn.disabled = false;
    if (inp) inp.focus();
  }
}

async function sendViewerRepairRun(payload) {
  const r = await fetch('/viewer/repair/run', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(payload || {}),
  });
  if (!r.ok) {
    const text = await r.text();
    throw new Error('HTTP ' + String(r.status) + ': ' + (text || r.statusText || 'repair failed'));
  }
  return r.json();
}

function buildViewerStatusSnapshot() {
  const items = {};
  AGENTS.forEach((id) => {
    const current = state.agents[id] || {};
    items[id] = {
      state: current.state || 'offline',
      reason: current.reason || '',
      route: current.route || '-',
      updated_at: current.updatedAt || '',
      preview: current.preview || '',
    };
  });
  const jobs = Object.values(state.jobs || {});
  return {
    generated_at: new Date().toISOString(),
    timeline_event_count: state.logs.length,
    job_count: jobs.length,
    running_job_count: jobs.filter((j) => String(j.status || '') !== 'done').length,
    agents: items,
  };
}

function registerWebMCPTools() {
  const modelContext = navigator && navigator.modelContext;
  if (!modelContext || typeof modelContext.registerTool !== 'function') {
    console.info('[WebMCP] navigator.modelContext is unavailable on this browser/page');
    return;
  }

  const toolSignalController = new AbortController();
  window.addEventListener('pagehide', () => toolSignalController.abort(), {once: true});

  try {
    modelContext.registerTool({
      name: 'viewer.get_status',
      title: 'Get Viewer Status',
      description: 'Return RenCrow viewer status including agent states and running jobs.',
      inputSchema: {
        type: 'object',
        properties: {},
        additionalProperties: false,
      },
      annotations: {
        readOnlyHint: true,
      },
      execute: async function() {
        return buildViewerStatusSnapshot();
      },
    }, {signal: toolSignalController.signal});

    modelContext.registerTool({
      name: 'viewer.send_message',
      title: 'Send Viewer Message',
      description: 'Send a user message to RenCrow via viewer send endpoint.',
      inputSchema: {
        type: 'object',
        properties: {
          message: {
            type: 'string',
            description: 'Message body to send to RenCrow.',
            minLength: 1,
          },
        },
        required: ['message'],
        additionalProperties: false,
      },
      execute: async function(input) {
        const msg = input && typeof input.message === 'string' ? input.message : '';
        await sendViewerMessage(msg);
        return {
          ok: true,
          message_preview: short(msg, 120),
        };
      },
    }, {signal: toolSignalController.signal});

    console.info('[WebMCP] tools registered: viewer.get_status, viewer.send_message');
  } catch (err) {
    console.error('[WebMCP] tool registration failed', err);
  }
}

let viewerEventSource = null;
let viewerEventReconnectTimer = null;
let viewerEventWatchdogTimer = null;

function scheduleViewerEventReconnect() {
  if (viewerEventReconnectTimer || typeof setTimeout !== 'function') return;
  viewerEventReconnectTimer = setTimeout(() => {
    viewerEventReconnectTimer = null;
    if (viewerEventSource && viewerEventSource.readyState === EventSource.OPEN) return;
    if (viewerEventSource) {
      try { viewerEventSource.close(); } catch (_) {}
      viewerEventSource = null;
    }
    connect();
  }, 3000);
}

function ensureViewerEventWatchdog() {
  if (viewerEventWatchdogTimer || typeof setInterval !== 'function') return;
  viewerEventWatchdogTimer = setInterval(() => {
    if (!viewerEventSource || viewerEventSource.readyState === EventSource.CLOSED) {
      viewerEventSource = null;
      scheduleViewerEventReconnect();
    }
  }, 5000);
}

function connect() {
  if (viewerEventSource && viewerEventSource.readyState !== EventSource.CLOSED) return;
  ensureViewerEventWatchdog();
  const es = new EventSource('/viewer/events');
  viewerEventSource = es;
  const dot = document.getElementById('dot');
  const stxt = document.getElementById('stxt');
  es.onopen = () => {
    if (viewerEventReconnectTimer) {
      clearTimeout(viewerEventReconnectTimer);
      viewerEventReconnectTimer = null;
    }
    dot.className = 'dot';
    stxt.textContent = 'Connected';
  };
  es.onmessage = (e) => {
    try { ingestEvent(JSON.parse(e.data)); }
    catch (err) { console.error(err); }
  };
  es.onerror = () => {
    dot.className = 'dot off';
    stxt.textContent = 'Reconnecting...';
    if (es.readyState === EventSource.CLOSED && viewerEventSource === es) viewerEventSource = null;
    scheduleViewerEventReconnect();
  };
}

refreshDerivedViews();
if (typeof bindHomeDeskControls === 'function') bindHomeDeskControls();
if (typeof bindDevelopDeskControls === 'function') bindDevelopDeskControls();
if (typeof bindInstructionsDeskControls === 'function') bindInstructionsDeskControls();
if (typeof bindReportsDeskControls === 'function') bindReportsDeskControls();
if (typeof bindInvestmentDeskControls === 'function') bindInvestmentDeskControls();
renderIdleChat();
setIdleSelectedMode(state.idleChat.selectedMode);
setIdleSelectedView(state.idleChat.selectedView);
refreshIdleStatus();
refreshIdleLogs();
if (!initLiveMode()) {
  initTabFromQuery();
}
initEvidenceFromQuery();
refreshOptionalPanelData();
refreshJobNotifications();
refreshViewerStatus();
setInterval(() => {
  if (!derivedDirty) return;
  refreshDerivedViews();
  derivedDirty = false;
}, 500);
setInterval(refreshViewerStatus, 5000);
setInterval(refreshJobNotifications, 3000);
setInterval(refreshIdleStatus, 3000);
setInterval(refreshIdleLogs, 5000);
setOptionalPanelRefreshIntervals();
setInterval(refreshDebugSystem, 5000);
setInterval(() => {
  const panel = document.getElementById('llmOpsPanel');
  if (panel && state.ops.llmOpsEnabled) refreshLlmOpsStatus();
}, 5000);
refreshDebugSystem();
registerWebMCPTools();
connect();

// === STT (Speech-to-Text) realtime PCM16 streaming ===
const sttState = {
  ws: null,
  audioContext: null,
  audioStream: null,
  scriptNode: null,
  isRecording: false,
  isStarting: false,
  isStopping: false,
  keepSessionChannel: false,
  vadSpeechActive: false,
  vadSilenceStartedAt: 0,
  vadLastVoiceAt: 0,
  pendingSpeechRestart: false,
  pendingSpeechRestartInterrupted: false,
  streamReady: false,
  sampleRate: 16000,
  inputSampleRate: 48000,
  chunkBuffer: [],
  chunkSamples: 1600,
  draftBuffer: [],        // Recent window for draft (1 second)
  draftTimer: null,
  finalWaitTimer: null,
  reconnectTimer: null,
  reconnecting: false,
  stopControlSent: false,
  finalReceived: false,
  captureLog: [],
  capturePCM: [],
  captureStartedAt: '',
  captureEndedAt: '',
  captureSessionID: '(unknown)',
  captureEventID: '',
  captureActionError: '',
  latencySpeechStartMS: 0,
  latencyStopMS: 0,
  latencyFinalMS: 0,
  sentAudioSamples: 0,
  sentAudioBytes: 0,
  sentAudioFrames: 0,
  lastLoggedAudioSecond: 0,
  lastRecognitionText: '',
  lastRecognitionType: '',
  partialCaptionText: '',
  finalCaptionText: '',
  errorCaptionText: '',
  inputLevel: 0,
  voiceBridgeURL: `${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}/stt`,
  sttBaseURL: '',
  runtimeConfigLoaded: false
};

const vdsState = {
  voiceInputMode: 'stt_primary',
  voiceChatURL: `${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}/voice-chat`,
  voiceChatEnabled: false,
  ws: null,
  audioContext: null,
  audioStream: null,
  scriptNode: null,
  isRecording: false,
  isStopping: false,
  vadSpeechActive: false,
  vadSilenceStartedAt: 0,
  vadLastVoiceAt: 0,
  utteranceStartedAtMS: 0,
  cooldownUntilMS: 0,
  cooldownTimer: null,
  streamReady: false,
  utteranceID: '',
  sessionID: '',
  chunkBuffer: [],
  chunkSamples: 8000,
  sentAudioBytes: 0,
  sentAudioSamples: 0,
  sentAudioFrames: 0,
  sampleRate: 16000,
  inputSampleRate: 48000,
  inputLevel: 0,
  captureLog: [],
  llmDeltaText: '',
  llmFinalText: '',
  errorText: '',
  lastDeltaRenderMS: 0,
  latencySpeechStartMS: 0,
  latencyCommitMS: 0,
  latencyFirstDeltaMS: 0,
  latencyFinalMS: 0,
  finalWaitTimer: null,
  runtimeConfigLoaded: false,
};

const micBtn = document.getElementById('micBtn');
const labMicBtn = document.getElementById('labMicBtn');
const micStateEl = document.getElementById('micState');
const sttConnStateEl = document.getElementById('sttConnState');
const sttSessionStateEl = document.getElementById('sttSessionState');
const sttCaptionEl = document.getElementById('sttCaption');
const sttCaptionLabelEl = document.getElementById('sttCaptionLabel');
const sttCaptionTextEl = document.getElementById('sttCaptionText');
const debugSttSessionEl = document.getElementById('debugSttSession');
const sttCaptureCopyBtn = document.getElementById('sttCaptureCopyBtn');
const sttCaptureDownloadBtn = document.getElementById('sttCaptureDownloadBtn');
const sttCaptureClearBtn = document.getElementById('sttCaptureClearBtn');
const sttSessionCopyBtn = document.getElementById('sttSessionCopyBtn');
const STT_FINAL_WAIT_TIMEOUT_MS = 90000;
const STT_SILENCE_END_MS = 700;
const STT_STOP_TAIL_SILENCE_MS = 300;
const STT_VAD_START_LEVEL = 12;
const STT_VAD_END_LEVEL = 8;
const VDS_SILENCE_END_MS = 500;
const VDS_COOLDOWN_MS = 900;
const VDS_MIN_SPEECH_MS = 250;
const VDS_MAX_UTTERANCE_MS = 30000;
const VDS_VAD_START_LEVEL = 4;
const VDS_VAD_END_LEVEL = 3;

const sttTestRecordState = {
  isRecording: false,
  capturePCM: [],
  startedAt: '',
  lastSavedAt: '',
  lastSavedSamples: 0,
  lastSavedRawSamples: 0,
  lastTranscript: '',
  inputLevel: 0,
  lastError: '',
  audioStream: null,
  audioContext: null,
  scriptNode: null,
  sampleRate: 16000,
  inputSampleRate: 48000,
};

const sttTestRecordStartBtn = document.getElementById('sttTestRecordStartBtn');
const sttTestRecordStopBtn = document.getElementById('sttTestRecordStopBtn');
const sttTestRecordStatusEl = document.getElementById('sttTestRecordStatus');
const sttTestRecordTranscriptEl = document.getElementById('sttTestRecordTranscript');

function extractSTTAutoTestTranscript(result) {
  const inference = Array.isArray(result && result.inference) ? result.inference : [];
  for (const item of inference) {
    const text = String(item && item.text ? item.text : '').trim();
    if (item && item.ok && text) return text;
  }
  const ws = Array.isArray(result && result.ws) ? result.ws : [];
  for (const item of ws) {
    const text = String(item && item.final ? item.final : '').trim();
    if (item && item.ok && text) return text;
  }
  return '';
}

function isSTTTestRecording() {
  return !!sttTestRecordState.isRecording;
}

function updateSTTTestRecordUI() {
  const microphoneUnavailable = getSTTMicrophoneUnavailableReason();
  if (sttTestRecordStartBtn) {
    sttTestRecordStartBtn.disabled = sttTestRecordState.isRecording || !!microphoneUnavailable || !!sttState.isRecording || !!vdsState.isRecording;
  }
  if (sttTestRecordStopBtn) {
    sttTestRecordStopBtn.disabled = !sttTestRecordState.isRecording;
  }
  if (sttTestRecordStatusEl) {
    if (sttTestRecordState.isRecording) {
      const sec = sttTestRecordState.capturePCM.length / (sttTestRecordState.sampleRate || 16000);
      sttTestRecordStatusEl.textContent = `録音中 / 入力 ${Math.round(sttTestRecordState.inputLevel)}% / ${sec.toFixed(1)}s`;
    } else if (sttTestRecordState.lastError) {
      sttTestRecordStatusEl.textContent = 'エラー: ' + sttTestRecordState.lastError;
    } else if (sttTestRecordState.lastSavedAt) {
      const sec = sttTestRecordState.lastSavedSamples / (sttTestRecordState.sampleRate || 16000);
      const rawSec = sttTestRecordState.lastSavedRawSamples / (sttTestRecordState.sampleRate || 16000);
      sttTestRecordStatusEl.textContent = `保存済み raw ${rawSec.toFixed(1)}s / trimmed ${sec.toFixed(1)}s`;
    } else {
      sttTestRecordStatusEl.textContent = '待機中';
    }
  }
  if (sttTestRecordTranscriptEl) {
    const transcript = String(sttTestRecordState.lastTranscript || '').trim();
    sttTestRecordTranscriptEl.textContent = transcript ? ('確定文: ' + transcript) : '確定文: -';
  }
}

function releaseSTTTestRecordAudio() {
  if (sttTestRecordState.scriptNode) {
    sttTestRecordState.scriptNode.disconnect();
    sttTestRecordState.scriptNode = null;
  }
  if (sttTestRecordState.audioContext) {
    sttTestRecordState.audioContext.close();
    sttTestRecordState.audioContext = null;
  }
  if (sttTestRecordState.audioStream) {
    sttTestRecordState.audioStream.getTracks().forEach((track) => track.stop());
    sttTestRecordState.audioStream = null;
  }
}

async function startSTTTestRecording() {
  if (sttTestRecordState.isRecording) return;
  if (sttState.isRecording) {
    showToast('通常マイク使用中のためテスト録音を開始できません', 'error');
    return;
  }
  const microphoneUnavailable = getSTTMicrophoneUnavailableReason();
  if (microphoneUnavailable) {
    sttTestRecordState.lastError = microphoneUnavailable;
    updateSTTTestRecordUI();
    showToast('マイク利用不可', 'error');
    return;
  }
  try {
    interruptChatOutputForUserInput('stt_test_record');
    interruptIdleChatForUserInput('stt_test_record');
    if (typeof claimViewerControl === 'function') {
      await claimViewerControl('input', 'stt_test_record_start');
    }
    sttTestRecordState.lastError = '';
    sttTestRecordState.lastSavedAt = '';
    sttTestRecordState.lastSavedSamples = 0;
    sttTestRecordState.lastSavedRawSamples = 0;
    sttTestRecordState.lastTranscript = '';
    sttTestRecordState.capturePCM = [];
    sttTestRecordState.startedAt = new Date().toISOString();
    sttTestRecordState.inputLevel = 0;
    sttTestRecordState.audioStream = await navigator.mediaDevices.getUserMedia({
      audio: {
        noiseSuppression: true,
        echoCancellation: true,
        autoGainControl: true,
      },
    });
    sttTestRecordState.audioContext = new (window.AudioContext || window.webkitAudioContext)();
    sttTestRecordState.inputSampleRate = Math.round(sttTestRecordState.audioContext.sampleRate || 48000);
    sttTestRecordState.sampleRate = 16000;
    sttTestRecordState.isRecording = true;
    const source = sttTestRecordState.audioContext.createMediaStreamSource(sttTestRecordState.audioStream);
    sttTestRecordState.scriptNode = sttTestRecordState.audioContext.createScriptProcessor(4096, 1, 1);
    sttTestRecordState.scriptNode.onaudioprocess = (e) => {
      if (!sttTestRecordState.isRecording) return;
      const pcm = e.inputBuffer.getChannelData(0);
      const pcm16 = resampleToPCM16(pcm, sttTestRecordState.inputSampleRate || 48000, 16000);
      sttTestRecordState.inputLevel = calculateSTTInputLevel(pcm16);
      sttTestRecordState.capturePCM.push(...pcm16);
      updateSTTTestRecordUI();
    };
    source.connect(sttTestRecordState.scriptNode);
    sttTestRecordState.scriptNode.connect(sttTestRecordState.audioContext.destination);
    updateSTTTestRecordUI();
    updateSTTInputIndicators();
    showToast('テスト録音を開始しました', 'success');
  } catch (err) {
    sttTestRecordState.lastError = describeSTTActionError('STT test record start unavailable', err);
    releaseSTTTestRecordAudio();
    sttTestRecordState.isRecording = false;
    updateSTTTestRecordUI();
    updateSTTInputIndicators();
    console.error('[STT Test Record] Failed:', err);
    showToast('テスト録音開始失敗', 'error');
  }
}

async function stopSTTTestRecordingAndSave() {
  if (!sttTestRecordState.isRecording) return;
  sttTestRecordState.isRecording = false;
  sttTestRecordState.inputLevel = 0;
  releaseSTTTestRecordAudio();
  updateSTTTestRecordUI();
  updateSTTInputIndicators();
  try {
    const rawPCM = new Int16Array(sttTestRecordState.capturePCM);
    if (!rawPCM.length) {
      throw new Error('録音データが空です');
    }
    const rawWav = pcm16ToWav(rawPCM, sttTestRecordState.sampleRate || 16000);
    await persistSTTRawWavToServer(rawWav);
    sttTestRecordState.lastSavedRawSamples = rawPCM.length;
    const trimFn = typeof trimSTTPcmSilence === 'function' ? trimSTTPcmSilence : null;
    const trimmed = trimFn
      ? trimFn(rawPCM, { minLevel: STT_VAD_END_LEVEL, minVoiceMs: 300, frameSamples: 160, sampleRate: sttTestRecordState.sampleRate, edgeOnly: true })
      : rawPCM;
    if (!trimmed || trimmed.length === 0) {
      throw new Error('録音データが空です（無音のみ）');
    }
    const wav = pcm16ToWav(trimmed, sttTestRecordState.sampleRate || 16000);
    await persistSTTWavToServer(wav);
    sttTestRecordState.lastSavedAt = new Date().toISOString();
    sttTestRecordState.lastSavedSamples = trimmed.length;
    sttTestRecordState.lastError = '';
    sttTestRecordState.capturePCM = [];
    if (sttTestRecordStatusEl) sttTestRecordStatusEl.textContent = 'STT確認中...';
    updateSTTTestRecordUI();
    if (!sttState.runtimeConfigLoaded) {
      await loadViewerRuntimeConfig();
    }
    const autoTest = await runSTTAutoTest({ provider_rounds: 1, ws_rounds: 0 });
    const transcript = extractSTTAutoTestTranscript(autoTest);
    if (!transcript) {
      throw new Error('STT確定文を取得できませんでした');
    }
    sttTestRecordState.lastTranscript = transcript;
    updateSTTTestRecordUI();
    showToast('テスト録音とSTT確認が完了しました', 'success');
  } catch (err) {
    sttTestRecordState.lastError = describeSTTActionError('STT test record save unavailable', err);
    updateSTTTestRecordUI();
    console.error('[STT Test Record] Save failed:', err);
    showToast('テスト録音保存失敗', 'error');
  }
}

if (sttTestRecordStartBtn) {
  sttTestRecordStartBtn.addEventListener('click', () => {
    startSTTTestRecording();
  });
}
if (sttTestRecordStopBtn) {
  sttTestRecordStopBtn.addEventListener('click', () => {
    stopSTTTestRecordingAndSave();
  });
}
updateSTTTestRecordUI();
function handleMicButtonClick() {
  interruptIdleChatForUserInput('stt_button');
  toggleVoiceInput();
}
if (micBtn) micBtn.addEventListener('click', handleMicButtonClick);
if (labMicBtn) labMicBtn.addEventListener('click', handleMicButtonClick);
if (sttCaptureCopyBtn) {
  sttCaptureCopyBtn.addEventListener('click', copySTTCaptureLog);
}
if (sttCaptureDownloadBtn) {
  sttCaptureDownloadBtn.addEventListener('click', downloadSTTCaptureLog);
}
if (sttCaptureClearBtn) {
  sttCaptureClearBtn.addEventListener('click', clearSTTCaptureLog);
}
if (sttSessionCopyBtn) {
  sttSessionCopyBtn.addEventListener('click', copySTTSessionID);
}
sttControlsReady = true;
updateSTTInputIndicators();
loadViewerRuntimeConfig();

function isLabInputSurfaceActive() {
  return document.body.classList.contains('lab-mode') && document.body.classList.contains('live-mode');
}

function isVoiceChatAllowed() {
  if (isLabInputSurfaceActive()) return true;
  return activeViewerTab === 'timeline' && !document.body.classList.contains('live-mode');
}

function normalizeVoiceInputMode(raw) {
  const mode = String(raw || '').trim();
  if (mode === 'vds_sub' || mode === 'parallel_caption') return mode;
  return 'stt_primary';
}

function isVDSSubMode() {
  return normalizeVoiceInputMode(vdsState.voiceInputMode) === 'vds_sub';
}

async function loadViewerRuntimeConfig() {
  try {
    const res = await fetch('/viewer/runtime-config', { cache: 'no-store' });
    if (!res.ok) {
      const text = await res.text();
      syncLLMOpsPanel(null, 'HTTP ' + String(res.status) + ': ' + (text || res.statusText || 'runtime config unavailable'));
      return;
    }
    const cfg = await res.json();
    if (cfg && cfg.stt_stream_url) {
      sttState.voiceBridgeURL = String(cfg.stt_stream_url).trim() || sttState.voiceBridgeURL;
    }
    if (cfg && cfg.stt_base_url) {
      sttState.sttBaseURL = String(cfg.stt_base_url).trim();
    }
    if (cfg && cfg.voice_chat_stream_url) {
      vdsState.voiceChatURL = String(cfg.voice_chat_stream_url).trim() || vdsState.voiceChatURL;
    }
    if (cfg && typeof cfg.voice_chat_enabled === 'boolean') {
      vdsState.voiceChatEnabled = cfg.voice_chat_enabled;
    }
    if (cfg && cfg.voice_input_mode) {
      vdsState.voiceInputMode = normalizeVoiceInputMode(cfg.voice_input_mode);
    }
    sttState.runtimeConfigLoaded = true;
    vdsState.runtimeConfigLoaded = true;
    updateSTTInputIndicators();
    syncLLMOpsPanel(cfg, '');
    loadViewerDebugSystemSnapshot();
  } catch (err) {
    const message = String(err && err.message ? err.message : err);
    console.warn('[STT] runtime config unavailable:', err);
    syncLLMOpsPanel(null, message);
  }
}

async function loadViewerDebugSystemSnapshot() {
  if (typeof syncRuntimeDebugSystem !== 'function') return;
  try {
    const res = await fetch('/viewer/debug/system', { cache: 'no-store' });
    if (!res.ok) {
      const text = await res.text();
      syncRuntimeDebugSystem(null, 'HTTP ' + String(res.status) + ': ' + (text || res.statusText || 'debug system unavailable'));
      return;
    }
    syncRuntimeDebugSystem(await res.json(), '');
  } catch (err) {
    const message = String(err && err.message ? err.message : err);
    console.warn('[Runtime] debug system snapshot unavailable:', err);
    syncRuntimeDebugSystem(null, message);
  }
}

function recordSTTCaptureEvent(type, payload) {
  if (type !== 'speech_start' && type !== 'start' && type !== 'stop' && type !== 'draft' && type !== 'partial' && type !== 'final' && type !== 'final_fallback' && type !== 'final_ignored' && type !== 'progress' && type !== 'audio_sent' && type !== 'ready' && type !== 'closed' && type !== 'error' && type !== 'ws_open' && type !== 'ws_error' && type !== 'ws_close') return;
  const rawPayload = String(payload || '').trim();
  if (type === 'speech_start' || type === 'ready' || type === 'closed' || type === 'ws_open' || type === 'ws_close') {
    payload = '-';
  } else {
    if (!rawPayload) return;
    payload = rawPayload;
  }
  const nowISO = new Date().toISOString();
  if (!sttState.captureStartedAt) {
    sttState.captureStartedAt = nowISO;
  }
  sttState.captureEndedAt = nowISO;
  sttState.captureLog.push({
    time: ftime(nowISO),
    type,
    payload: String(payload || '-'),
  });
  if (sttState.captureLog.length > 200) {
    sttState.captureLog.shift();
  }
}

function renderSTTDebugPanelsSafely() {
  try {
    renderDebugPanels();
  } catch (err) {
    console.warn('[STT] Debug panel render skipped:', err && err.message ? err.message : err);
  }
}

function getSTTCaptureSummaryText() {
  const finals = sttState.captureLog
    .filter((item) => (item.type === 'final' || item.type === 'partial' || item.type === 'draft') && item.payload && item.payload !== '-')
    .map((item) => item.payload.trim().split(' / ')[0].trim())
    .filter(Boolean);
  return finals.length > 0 ? finals.slice(-3).join(' / ') : '-';
}

function buildSTTCaptureLogText() {
  const startedAt = sttState.captureStartedAt ? fdt(sttState.captureStartedAt) : '-';
  const endedAt = sttState.captureEndedAt ? fdt(sttState.captureEndedAt) : '-';
  const meta = [
    '# Client STT Log',
    'client_url: ' + window.location.href,
    'ws_url: ' + sttState.voiceBridgeURL,
    'test_time: ' + startedAt + ' ~ ' + endedAt,
    'session_id: ' + (sttState.captureSessionID || '(unknown)'),
    'event_id: ' + (sttState.captureEventID || '(unknown)'),
    'sent_audio: ' + formatSTTSentAudioSummary(),
    'spoken_text: ' + getSTTCaptureSummaryText(),
    '',
  ];
  const body = sttState.captureLog.slice().reverse().map((item) => {
    return `${item.time || '--:--:--'} · ${item.type || '-'}\n${item.payload || '-'}`;
  });
  if (body.length === 0) {
    body.push('NO_STT_EVENTS');
  }
  return meta.concat(body).join('\n');
}

function formatSTTSentAudioSummary() {
  const sampleRate = Number(sttState.sampleRate || 16000) || 16000;
  const seconds = sampleRate > 0 ? sttState.sentAudioSamples / sampleRate : 0;
  return `${seconds.toFixed(3)}s / ${sttState.sentAudioBytes} bytes / ${sttState.sentAudioFrames} frames`;
}

function formatSTTServerEventPayload(msg, fallbackText) {
  const parts = [];
  const text = String(fallbackText || '').trim();
  if (text) parts.push(text);
  if (msg && msg.seq !== undefined && msg.seq !== null) parts.push('seq=' + String(msg.seq));
  if (msg && msg.start_ms !== undefined && msg.end_ms !== undefined) {
    parts.push('range=' + String(msg.start_ms) + '-' + String(msg.end_ms) + 'ms');
  }
  if (msg && msg.duration !== undefined && msg.duration !== null) {
    parts.push('duration=' + String(msg.duration) + 's');
  }
  if (msg && msg.reason) parts.push('reason=' + String(msg.reason));
  return parts.join(' / ');
}

function describeSTTActionError(prefix, err) {
  return prefix + ': ' + String(err && err.message ? err.message : err);
}

function getSTTMicrophoneUnavailableReason() {
  if (typeof window !== 'undefined' && window.isSecureContext === false) {
    return 'HTTPSまたはlocalhostでViewerを開いてください';
  }
  if (typeof navigator === 'undefined' || !navigator.mediaDevices || typeof navigator.mediaDevices.getUserMedia !== 'function') {
    return 'ブラウザのマイクAPIが利用できません';
  }
  return '';
}

function copySTTCaptureLog() {
  const text = buildSTTCaptureLogText();
  writeClipboardText(text).then(() => {
    sttState.captureActionError = '';
    updateSTTInputIndicators();
    showToast('STTログをコピーしました', 'success');
  }).catch((err) => {
    sttState.captureActionError = describeSTTActionError('STT log copy unavailable', err);
    updateSTTInputIndicators();
    console.error('[STT] copy failed:', err);
    showToast('STTログコピー失敗', 'error');
  });
}

function downloadSTTCaptureLog() {
  const text = buildSTTCaptureLogText();
  const blob = new Blob([text], { type: 'text/plain;charset=utf-8' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = 'client_stt_log.txt';
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
  showToast('client_stt_log.txt を保存しました', 'success');
}

function clearSTTCaptureLog() {
  sttState.captureLog = [];
  sttState.capturePCM = [];
  sttState.captureStartedAt = '';
  sttState.captureEndedAt = '';
  sttState.latencySpeechStartMS = 0;
  sttState.latencyStopMS = 0;
  sttState.latencyFinalMS = 0;
  sttState.partialCaptionText = '';
  sttState.finalCaptionText = '';
  sttState.errorCaptionText = '';
  updateSTTCaption();
  updateSTTInputIndicators();
  showToast('STTログをクリアしました', 'success');
}

function copySTTSessionID() {
  const sid = String(sttState.captureSessionID || '(unknown)').trim() || '(unknown)';
  if (sid === '(unknown)') {
    showToast('SessionID未受信です', 'error');
    return;
  }
  writeClipboardText(sid).then(() => {
    sttState.captureActionError = '';
    updateSTTInputIndicators();
    showToast('SessionIDをコピーしました', 'success');
  }).catch((err) => {
    sttState.captureActionError = describeSTTActionError('STT session copy unavailable', err);
    updateSTTInputIndicators();
    console.error('[STT] session_id copy failed:', err);
    showToast('SessionIDコピー失敗', 'error');
  });
}

async function persistSTTLogToServer(logText) {
  const res = await fetch('/viewer/stt/log', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({content: logText}),
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error('HTTP ' + String(res.status) + ': ' + (text || res.statusText || 'stt log save failed'));
  }
}

async function persistSTTWavToServer(wavBuffer) {
  const res = await fetch('/viewer/stt/wav', {
    method: 'POST',
    headers: {'Content-Type': 'audio/wav'},
    body: wavBuffer,
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error('HTTP ' + String(res.status) + ': ' + (text || res.statusText || 'stt wav save failed'));
  }
}

async function persistSTTRawWavToServer(wavBuffer) {
  const res = await fetch('/viewer/stt/wav/raw', {
    method: 'POST',
    headers: {'Content-Type': 'audio/wav'},
    body: wavBuffer,
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error('HTTP ' + String(res.status) + ': ' + (text || res.statusText || 'stt raw wav save failed'));
  }
}

async function runSTTAutoTest(options) {
  const opts = options || {};
  const providerURL = buildSTTProviderURLForAutoTest();
  const res = await fetch('/viewer/stt/autotest', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({
      provider_url: providerURL,
      provider_rounds: Number.isFinite(opts.provider_rounds) ? opts.provider_rounds : 1,
      ws_rounds: Number.isFinite(opts.ws_rounds) ? opts.ws_rounds : 1,
      ws_wait: Number.isFinite(opts.ws_wait) ? opts.ws_wait : 8,
    }),
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error('HTTP ' + String(res.status) + ': ' + (text || res.statusText || 'stt autotest failed'));
  }
  return res.json();
}

function buildSTTProviderURLForAutoTest() {
  const state = typeof sttState !== 'undefined' ? sttState : {};
  const base = String(state.sttBaseURL || '').trim().replace(/\/+$/, '');
  return base ? base + '/v1/audio/transcriptions' : '';
}

async function persistSTTArtifacts() {
  const logText = buildSTTCaptureLogText();
  await persistSTTLogToServer(logText);
  if (sttState.capturePCM.length > 0) {
    const wav = pcm16ToWav(new Int16Array(sttState.capturePCM));
    await persistSTTWavToServer(wav);
    await runSTTAutoTest();
  }
}

function applyMicButtonState(btn, microphoneUnavailable, voiceAllowed, mobileControlAllowed) {
  if (!btn) return;
  const sttOn = !!(sttState.isRecording || sttState.isStarting);
  const actionError = String(sttState.captureActionError || '').trim();
  const controlState = sttOn ? 'STT_ON' : 'STT_OFF';
  const isLabMic = btn === labMicBtn || btn.classList.contains('lab-mic-toggle');
  btn.classList.toggle('ready', sttOn);
  btn.classList.toggle('has-level', sttState.isRecording && sttState.inputLevel > 0);
  btn.classList.toggle('off', !sttOn);
  btn.classList.toggle('mic-unavailable', !!(microphoneUnavailable || actionError) && !sttOn);
  btn.style.setProperty('--mic-level-pct', `${Math.round(Math.max(0, Math.min(100, sttState.inputLevel)))}%`);
  btn.disabled = (!mobileControlAllowed || !!microphoneUnavailable) && !sttOn;
  btn.setAttribute('aria-pressed', sttOn ? 'true' : 'false');
  btn.dataset.state = controlState;
  btn.dataset.controlState = controlState;
  const defaultTitle = microphoneUnavailable
    ? '音声入力不可: ' + microphoneUnavailable
    : voiceAllowed
    ? (sttOn ? `音声入力中（入力レベル ${Math.round(sttState.inputLevel)}%・クリックで停止）` : '音声入力')
    : (mobileControlAllowed ? 'Chatに切り替えて音声入力' : '音声入力は通常チャットでのみ有効です');
  if (isLabMic) {
    btn.title = sttOn ? `STT_ON（LLM直結・入力レベル ${Math.round(sttState.inputLevel)}%）` : (microphoneUnavailable || actionError ? 'STT_OFF / ' + (microphoneUnavailable || actionError) : 'STT_OFF');
    btn.setAttribute('aria-label', sttOn ? 'STT_ON' : 'STT_OFF');
  } else {
    btn.title = defaultTitle;
    btn.setAttribute('aria-label', sttOn ? 'STT_ON' : 'STT_OFF');
  }
}

function updateSTTInputIndicators() {
  const voiceAllowed = isVoiceChatAllowed();
  const mobileControlAllowed = voiceAllowed || isMobileControlViewport();
  const microphoneUnavailable = getSTTMicrophoneUnavailableReason();
  const voiceRecording = !!sttState.isRecording || !!vdsState.isRecording;
  const voiceInputLevel = vdsState.isRecording ? vdsState.inputLevel : sttState.inputLevel;
  [micBtn, (typeof labMicBtn !== 'undefined' ? labMicBtn : null)].forEach((btn) => {
    if (!btn) return;
    btn.classList.toggle('ready', voiceRecording);
    btn.classList.toggle('has-level', voiceRecording && voiceInputLevel > 0);
    btn.classList.toggle('mic-unavailable', !!microphoneUnavailable && !sttState.isRecording);
    btn.style.setProperty('--mic-level-pct', `${Math.round(Math.max(0, Math.min(100, voiceInputLevel)))}%`);
    btn.disabled = (!!microphoneUnavailable && !sttState.isRecording) || isSTTTestRecording();
    if (vdsState.isRecording) btn.disabled = false;
    btn.setAttribute('aria-pressed', voiceRecording ? 'true' : 'false');
    btn.dataset.state = voiceRecording ? 'on' : (microphoneUnavailable ? 'unavailable' : 'off');
    btn.title = isSTTTestRecording()
      ? 'テスト録音中は通常マイクを使えません'
      : microphoneUnavailable
      ? '音声入力不可: ' + microphoneUnavailable
      : voiceAllowed
      ? (voiceRecording ? `音声入力監視中（入力レベル ${Math.round(voiceInputLevel)}%・無音で自動確定）` : '音声入力')
      : (mobileControlAllowed ? 'Chatに切り替えて音声入力' : '音声入力は通常チャットでのみ有効です');
  });
  if (micStateEl) {
    micStateEl.textContent = voiceRecording ? 'Mic: on' : (microphoneUnavailable ? 'Mic: unavailable' : 'Mic: off');
    micStateEl.className = 'stt-state' + (voiceRecording ? ' mic-on' : (microphoneUnavailable ? ' mic-unavailable' : ''));
  }
  if (sttConnStateEl) {
    let text = 'STT: off';
    let cls = 'stt-state conn-off';
    const ws = sttState.ws;
    if (sttState.isRecording) {
      if (ws && ws.readyState === WebSocket.OPEN) {
        text = sttState.vadSpeechActive || sttState.isStopping ? 'STT: connected' : 'STT: listening';
        cls = 'stt-state conn-on';
      } else if (sttState.reconnecting || (ws && ws.readyState === WebSocket.CONNECTING)) {
        text = 'STT: reconnecting';
        cls = 'stt-state conn-reconnect';
      } else {
        text = 'STT: waiting';
        cls = 'stt-state conn-off';
      }
    } else if (ws && ws.readyState === WebSocket.OPEN) {
      text = 'STT: standby';
      cls = 'stt-state conn-on';
    } else if (sttState.reconnecting || (ws && ws.readyState === WebSocket.CONNECTING)) {
      text = 'STT: connecting';
      cls = 'stt-state conn-reconnect';
    }
    sttConnStateEl.textContent = text;
    sttConnStateEl.className = cls;
  }
  if (sttSessionStateEl) {
    const sid = String(sttState.captureSessionID || '(unknown)').trim() || '(unknown)';
    const actionError = String(sttState.captureActionError || '').trim();
    const suffix = actionError ? ' / ' + actionError : '';
    sttSessionStateEl.textContent = 'Session: ' + sid + suffix;
    sttSessionStateEl.title = 'Session: ' + sid + suffix;
    if (debugSttSessionEl) {
      debugSttSessionEl.textContent = 'Session: ' + sid + suffix;
    }
  }
}

function updateSTTCaption() {
  if (!sttCaptionEl) return;
  const finalText = String(sttState.finalCaptionText || '').trim();
  const partialText = String(sttState.partialCaptionText || '').trim();
  const errorText = String(sttState.errorCaptionText || '').trim();
  const setCaption = (label, text, cls) => {
    if (sttCaptionLabelEl) sttCaptionLabelEl.textContent = label || '暫定文字列';
    if (sttCaptionTextEl) sttCaptionTextEl.textContent = text || '-';
    sttCaptionEl.title = [label, text].filter(Boolean).join(': ');
    sttCaptionEl.className = cls || 'stt-caption';
  };
  if (errorText) {
    setCaption('STT error', errorText, 'stt-caption has-text error');
    return;
  }
  if (finalText) {
    setCaption('確定文字列', finalText, 'stt-caption has-text final');
    return;
  }
  if (partialText) {
    setCaption('暫定文字列', partialText, 'stt-caption has-text draft');
    return;
  }
  setCaption('暫定文字列', '', 'stt-caption');
}

function setSTTCaptionError(text) {
  sttState.errorCaptionText = String(text || 'unknown error').trim() || 'unknown error';
  sttState.partialCaptionText = '';
  sttState.finalCaptionText = '';
  updateSTTCaption();
}

function getSTTExternalAudioStream() {
  if (!cameraCapture.stream || cameraCapture.sourceType !== 'display') return null;
  const audioTracks = cameraCapture.stream.getAudioTracks().filter((track) => track.readyState === 'live');
  if (audioTracks.length === 0 || typeof MediaStream !== 'function') return null;
  return new MediaStream(audioTracks.map((track) => track.clone()));
}

if (typeof ensureVoiceChatForMobileControl !== 'function') {
  var ensureVoiceChatForMobileControl = function() { return true; };
}

const VDS_FINAL_WAIT_TIMEOUT_MS = 120000;
const VDS_READY_WAIT_TIMEOUT_MS = 5000;
const VDS_DELTA_IDLE_FINALIZE_MS = 2500;
const VDS_DELTA_RENDER_INTERVAL_MS = 250;
const VDS_DEFAULT_PROMPT = 'あなたはMioです。入力音声からユーザーが実際に話した内容を日本語で短く復元し、その発話への返事を自然に日本語で1〜2文作ってください。必ずJSONだけを返してください。形式: {"user_text":"ユーザー発話の復元文","reply":"Mioとしての返事"}。user_textには返事や要約を入れず、音声内のユーザー発話だけを書いてください。replyには文字起こし説明や音声ファイル要求を書かず、相手への返事だけを書いてください。聞き取れない場合はuser_textを空文字にし、replyで短く聞き返してください。';

function buildVDSWebSocketURL() {
  const base = String(vdsState.voiceChatURL || '');
  const viewerClientID = (typeof viewerControl !== 'undefined' && viewerControl && viewerControl.clientId)
    ? String(viewerControl.clientId || '')
    : '';
  if (!viewerClientID) return base;
  const sep = base.includes('?') ? '&' : '?';
  return base + sep + 'viewer_client_id=' + encodeURIComponent(viewerClientID);
}

function extractVDSMessageText(msg) {
  if (!msg) return '';
  if (msg.text) return String(msg.text);
  if (msg.message) return String(msg.message);
  if (typeof msg.error === 'string') return msg.error;
  if (msg.error && msg.error.message) return String(msg.error.message);
  if (msg.error_code) return String(msg.error_code);
  return '';
}

function updateVDSInputLevel(level) {
  vdsState.inputLevel = Math.max(0, Math.min(100, Number(level) || 0));
  updateSTTInputIndicators();
}

function recordVDSAudioSent(samples) {
  const count = Math.max(0, Number(samples) || 0);
  if (count <= 0) return;
  vdsState.sentAudioSamples += count;
  vdsState.sentAudioBytes += count * 2;
  vdsState.sentAudioFrames += 1;
}

function sendVDSSessionStart() {
  if (!vdsState.ws || vdsState.ws.readyState !== WebSocket.OPEN) return false;
  vdsState.utteranceID = (typeof crypto !== 'undefined' && crypto.randomUUID)
    ? crypto.randomUUID()
    : ('utt-' + String(Date.now()));
  const sampleRate = Number(vdsState.sampleRate || 16000) || 16000;
  const control = {
    type: 'session.start',
    utterance_id: vdsState.utteranceID,
    sample_rate: sampleRate,
    channels: 1,
    format: 'pcm16le',
    voice_input_mode: 'vds_sub',
    viewer_session_id: 'viewer',
    channel: 'viewer',
    chat_id: 'default',
    prompt: VDS_DEFAULT_PROMPT,
  };
  vdsState.ws.send(JSON.stringify(control));
  return true;
}

function sendVDSSessionCommit() {
  if (!vdsState.ws || vdsState.ws.readyState !== WebSocket.OPEN) return false;
  const control = {
    type: 'session.commit',
    utterance_id: vdsState.utteranceID,
  };
  vdsState.ws.send(JSON.stringify(control));
  vdsState.latencyCommitMS = typeof nowLatencyMS === 'function' ? nowLatencyMS() : Date.now();
  return true;
}

function sendVDSAudioChunk(pcm16) {
  if (!vdsState.isRecording || !vdsState.vadSpeechActive || vdsState.isStopping) return;
  vdsState.chunkBuffer.push(...pcm16);
  if (!vdsState.ws || vdsState.ws.readyState !== WebSocket.OPEN || !vdsState.streamReady) return;
  while (vdsState.chunkBuffer.length >= vdsState.chunkSamples) {
    const chunk = new Int16Array(vdsState.chunkBuffer.slice(0, vdsState.chunkSamples));
    vdsState.chunkBuffer = vdsState.chunkBuffer.slice(vdsState.chunkSamples);
    vdsState.ws.send(chunk.buffer);
    recordVDSAudioSent(chunk.length);
  }
}

function flushVDSAudioChunkBuffer() {
  if (!vdsState.ws || vdsState.ws.readyState !== WebSocket.OPEN || vdsState.chunkBuffer.length === 0) return false;
  const chunk = new Int16Array(vdsState.chunkBuffer);
  vdsState.chunkBuffer = [];
  vdsState.ws.send(chunk.buffer);
  recordVDSAudioSent(chunk.length);
  return true;
}

function sendVDSTailSilence() {
  if (!vdsState.ws || vdsState.ws.readyState !== WebSocket.OPEN) return false;
  const sampleRate = Number(vdsState.sampleRate || 16000) || 16000;
  const totalSamples = Math.max(0, Math.round(sampleRate * STT_STOP_TAIL_SILENCE_MS / 1000));
  if (totalSamples <= 0) return false;
  const chunkSamples = Math.max(1, Number(vdsState.chunkSamples || 1600) || 1600);
  for (let offset = 0; offset < totalSamples; offset += chunkSamples) {
    const size = Math.min(chunkSamples, totalSamples - offset);
    vdsState.ws.send(new Int16Array(size).buffer);
  }
  return true;
}

function clearVDSFinalWaitTimer() {
  if (!vdsState.finalWaitTimer) return;
  clearTimeout(vdsState.finalWaitTimer);
  vdsState.finalWaitTimer = null;
}

function clearVDSDeltaIdleTimer() {
  if (!vdsState.deltaIdleTimer) return;
  clearTimeout(vdsState.deltaIdleTimer);
  vdsState.deltaIdleTimer = null;
}

function clearVDSCooldownTimer() {
  if (!vdsState.cooldownTimer) return;
  clearTimeout(vdsState.cooldownTimer);
  vdsState.cooldownTimer = null;
}

function isVDSInCooldown(now) {
  const cooldownUntil = Number(vdsState.cooldownUntilMS || 0) || 0;
  return cooldownUntil > 0 && (Number(now || Date.now()) || Date.now()) < cooldownUntil;
}

function enterVDSCooldown(reason) {
  completeVDSUtteranceStop(reason);
  vdsState.cooldownUntilMS = Date.now() + VDS_COOLDOWN_MS;
  clearVDSCooldownTimer();
  vdsState.cooldownTimer = setTimeout(() => {
    vdsState.cooldownTimer = null;
    vdsState.cooldownUntilMS = 0;
    updateSTTInputIndicators();
  }, VDS_COOLDOWN_MS);
  pushDebugTrace('vds', {
    time: ftime(new Date().toISOString()),
    step: 'cooldown',
    text: String(reason || 'done') + ' ' + String(VDS_COOLDOWN_MS) + 'ms',
  });
  renderDebugPanels();
  updateSTTInputIndicators();
}

function scheduleVDSFinalWaitTimeout() {
  clearVDSFinalWaitTimer();
  vdsState.finalWaitTimer = setTimeout(() => {
    vdsState.finalWaitTimer = null;
    if (!vdsState.isStopping) return;
    if (finalizeVDSDeltaResponse('timeout')) return;
    vdsState.errorText = 'timed out waiting for llm.final';
    showToast('Voice Direct 応答タイムアウト', 'error');
    enterVDSCooldown('timeout');
  }, VDS_FINAL_WAIT_TIMEOUT_MS);
}

function scheduleVDSDeltaIdleFinalize() {
  clearVDSDeltaIdleTimer();
  if (!vdsState.isStopping) return;
  vdsState.deltaIdleTimer = setTimeout(() => {
    vdsState.deltaIdleTimer = null;
    if (!vdsState.isStopping) return;
    renderVDSDeltaResponse('delta_idle');
  }, VDS_DELTA_IDLE_FINALIZE_MS);
}

function resetVDSUtteranceState() {
  vdsState.streamReady = false;
  vdsState.utteranceID = '';
  vdsState.sessionID = '';
  vdsState.chunkBuffer = [];
  vdsState.sentAudioBytes = 0;
  vdsState.sentAudioSamples = 0;
  vdsState.sentAudioFrames = 0;
  vdsState.llmDeltaText = '';
  vdsState.llmFinalText = '';
  vdsState.errorText = '';
  vdsState.responseEl = null;
  vdsState.responseTextEl = null;
  vdsState.responseRaw = '';
  vdsState.responseFinalized = false;
  vdsState.lastDeltaRenderMS = 0;
  clearVDSDeltaIdleTimer();
  vdsState.utteranceStartedAtMS = 0;
  vdsState.latencySpeechStartMS = 0;
  vdsState.latencyCommitMS = 0;
  vdsState.latencyFirstDeltaMS = 0;
  vdsState.latencyFinalMS = 0;
  clearVDSCaption();
  clearVDSFinalWaitTimer();
}

function clearVDSCaption() {
  if (!sttState) return;
  sttState.partialCaptionText = '';
  sttState.finalCaptionText = '';
  sttState.errorCaptionText = '';
  if (typeof updateSTTCaption === 'function') updateSTTCaption();
}

function updateVDSCaption(type, text) {
  if (!sttState) return;
  const captionText = String(text || '').trim();
  if (!captionText) return;
  sttState.errorCaptionText = '';
  if (type === 'final') {
    sttState.finalCaptionText = captionText;
    sttState.partialCaptionText = '';
  } else {
    sttState.partialCaptionText = captionText;
    sttState.finalCaptionText = '';
  }
  if (typeof updateSTTCaption === 'function') updateSTTCaption();
}

function renderVDSFinalTranscriptToChat(text, msg) {
  const finalText = String(text || '').trim();
  if (!finalText) return;
  if (typeof addMsgToTimeline !== 'function') return;
  addMsgToTimeline({
    type: 'message.received',
    from: 'user',
    to: 'mio',
    route: 'CHAT',
    job_id: String((msg && msg.utterance_id) || vdsState.utteranceID || '').trim(),
    session_id: String((msg && msg.session_id) || vdsState.sessionID || 'viewer').trim(),
    channel: 'viewer',
    chat_id: 'default',
    timestamp: new Date().toISOString(),
    content: finalText,
  });
}

function connectVDSWebSocket() {
  if (vdsState.isStopping) return;
  if (!vdsState.isRecording) return;
  if (vdsState.ws && vdsState.ws.readyState === WebSocket.OPEN) return;
  if (vdsState.ws && vdsState.ws.readyState === WebSocket.CONNECTING) return;
  vdsState.ws = new WebSocket(buildVDSWebSocketURL());
  vdsState.ws.binaryType = 'arraybuffer';
  vdsState.ws.onopen = () => {
    sendVDSSessionStart();
    console.log('[VDS] Connected - waiting for session.ready');
    updateSTTInputIndicators();
  };
  vdsState.ws.onmessage = (event) => {
    try {
      const msg = JSON.parse(event.data);
      const eventText = extractVDSMessageText(msg);
      pushDebugTrace('vds', {
        time: ftime(new Date().toISOString()),
        step: msg.type || 'message',
        text: short(eventText, 240),
      });
      if (msg.type === 'session.ready') {
        vdsState.streamReady = true;
        if (msg.session_id) vdsState.sessionID = String(msg.session_id).trim();
        if (vdsState.latencySpeechStartMS && typeof recordLatencyMetric === 'function') {
          const readyMS = typeof nowLatencyMS === 'function' ? nowLatencyMS() : Date.now();
          recordLatencyMetric('vds', 'session_ready', {
            valueMS: readyMS - vdsState.latencySpeechStartMS,
            session: vdsState.sessionID || '',
          });
        }
        flushVDSAudioChunkBuffer();
        updateSTTInputIndicators();
      } else if (msg.type === 'session.progress') {
        console.log('[VDS] Progress:', msg);
      } else if ((msg.type === 'transcript.delta' || msg.type === 'transcript.partial') && msg.text) {
        updateVDSCaption('partial', msg.text);
      } else if (msg.type === 'transcript.final' && msg.text) {
        updateVDSCaption('final', msg.text);
        renderVDSFinalTranscriptToChat(msg.text, msg);
      } else if (msg.type === 'llm.delta' && msg.text) {
        vdsState.llmDeltaText += String(msg.text || '');
        const renderAt = typeof nowLatencyMS === 'function' ? nowLatencyMS() : Date.now();
        if (!vdsState.lastDeltaRenderMS || renderAt - vdsState.lastDeltaRenderMS >= VDS_DELTA_RENDER_INTERVAL_MS) {
          vdsState.lastDeltaRenderMS = renderAt;
          renderVDSDeltaResponse('stream');
        }
        scheduleVDSDeltaIdleFinalize();
        if (!vdsState.latencyFirstDeltaMS) {
          vdsState.latencyFirstDeltaMS = typeof nowLatencyMS === 'function' ? nowLatencyMS() : Date.now();
          if (typeof recordLatencyMetric === 'function') {
            recordLatencyMetric('vds', 'first_delta', {
              atMS: vdsState.latencyFirstDeltaMS,
              valueMS: vdsState.latencyCommitMS ? vdsState.latencyFirstDeltaMS - vdsState.latencyCommitMS : NaN,
              session: vdsState.sessionID || '',
            });
          }
        }
      } else if (msg.type === 'llm.final') {
        handleVDSFinalMessage(msg);
      }
      if (msg.type === 'error') {
        vdsState.errorText = extractVDSMessageText(msg) || 'unknown error';
        showToast('Voice Direct エラー', 'error');
        console.error('[VDS] Error:', msg);
        enterVDSCooldown('error');
      }
    } catch (err) {
      console.warn('[VDS] Non-JSON message ignored:', err);
    }
  };
  vdsState.ws.onerror = (err) => {
    console.error('[VDS] WebSocket error:', err);
    vdsState.errorText = 'websocket error';
    updateSTTInputIndicators();
  };
  vdsState.ws.onclose = () => {
    vdsState.streamReady = false;
    updateSTTInputIndicators();
  };
}

function handleVDSFinalMessage(msg) {
  const finalText = String((msg && msg.text) || '').trim();
  vdsState.llmFinalText = finalText;
  vdsState.latencyFinalMS = typeof nowLatencyMS === 'function' ? nowLatencyMS() : Date.now();
  clearVDSFinalWaitTimer();
  clearVDSDeltaIdleTimer();
  if (finalText) {
    vdsState.llmDeltaText = finalText;
    renderVDSDeltaResponse('final');
  }
  if (typeof recordLatencyMetric === 'function') {
    recordLatencyMetric('vds', 'final_received', {
      atMS: vdsState.latencyFinalMS,
      valueMS: vdsState.latencyCommitMS ? vdsState.latencyFinalMS - vdsState.latencyCommitMS : NaN,
      detail: 'len=' + String(finalText.length),
      session: vdsState.sessionID || '',
    });
  }
  console.log('[VDS] LLM final received (SSE displays chat response):', finalText);
  enterVDSCooldown('llm.final');
}

function renderVDSDeltaResponse(reason) {
  const text = String(vdsState.llmDeltaText || '').trim();
  if (!text || vdsState.responseFinalized) return false;
  vdsState.responseRaw = text;
  pushDebugTrace('vds', {
    time: ftime(new Date().toISOString()),
    step: reason === 'final' ? 'local.final' : 'local.delta',
    text: short(text, 240),
  });
  renderDebugPanels();
  return true;
}

function finalizeVDSDeltaResponse(reason) {
  if (!renderVDSDeltaResponse('final')) return false;
  vdsState.responseFinalized = true;
  vdsState.llmFinalText = String(vdsState.llmDeltaText || '').trim();
  vdsState.latencyFinalMS = typeof nowLatencyMS === 'function' ? nowLatencyMS() : Date.now();
  if (typeof recordLatencyMetric === 'function') {
    recordLatencyMetric('vds', 'final_received', {
      atMS: vdsState.latencyFinalMS,
      valueMS: vdsState.latencyCommitMS ? vdsState.latencyFinalMS - vdsState.latencyCommitMS : NaN,
      detail: 'local_delta:' + String(reason || 'delta'),
      session: vdsState.sessionID || '',
    });
  }
  showToast('Voice Direct 応答を表示しました', 'success');
  enterVDSCooldown('local_delta');
  return true;
}

async function toggleVDS() {
  if (isSTTTestRecording()) {
    showToast('テスト録音中は通常マイクを使えません', 'error');
    return;
  }
  if (vdsState.isRecording) {
    stopVDS();
  } else {
    if (typeof ensureVoiceChatForMobileControl === 'function' && !ensureVoiceChatForMobileControl()) {
      showToast('音声入力は通常チャットでのみ有効です', 'error');
      return;
    }
    await startVDS();
  }
}

async function startVDS() {
  if (isSTTTestRecording()) {
    showToast('テスト録音中は通常マイクを使えません', 'error');
    return;
  }
  if (!ensureVoiceChatForMobileControl()) {
    showToast('音声入力は通常チャットでのみ有効です', 'error');
    return;
  }
  const microphoneUnavailable = getSTTMicrophoneUnavailableReason();
  if (microphoneUnavailable) {
    showToast('マイク利用不可', 'error');
    return;
  }
  if (!vdsState.runtimeConfigLoaded) {
    await loadViewerRuntimeConfig();
  }
  if (!vdsState.voiceChatEnabled) {
    showToast('Voice Direct は無効です', 'error');
    return;
  }
  try {
    if (typeof claimViewerControl === 'function') {
      await claimViewerControl('input', 'vds_start');
    }
    resetVDSUtteranceState();
    vdsState.isStopping = false;
    vdsState.vadSpeechActive = false;
    vdsState.vadSilenceStartedAt = 0;
    vdsState.vadLastVoiceAt = 0;
    vdsState.cooldownUntilMS = 0;
    clearVDSCooldownTimer();
    updateVDSInputLevel(0);
    vdsState.audioStream = await navigator.mediaDevices.getUserMedia({
      audio: {
        noiseSuppression: true,
        echoCancellation: true,
        autoGainControl: true,
      },
    });
    vdsState.audioContext = new (window.AudioContext || window.webkitAudioContext)();
    vdsState.inputSampleRate = Math.round(vdsState.audioContext.sampleRate || 48000);
    vdsState.sampleRate = 16000;
    const source = vdsState.audioContext.createMediaStreamSource(vdsState.audioStream);
    vdsState.scriptNode = vdsState.audioContext.createScriptProcessor(4096, 1, 1);
    source.connect(vdsState.scriptNode);
    vdsState.scriptNode.connect(vdsState.audioContext.destination);
    vdsState.scriptNode.onaudioprocess = (e) => {
      if (!vdsState.isRecording) return;
      const pcm = e.inputBuffer.getChannelData(0);
      const pcm16 = resampleToPCM16(pcm, vdsState.inputSampleRate || 48000, 16000);
      const level = calculateSTTInputLevel(pcm16);
      updateVDSInputLevel(level);
      handleVDSVADFrame(pcm16, level);
      if (vdsState.vadSpeechActive && !vdsState.isStopping) {
        sendVDSAudioChunk(pcm16);
      }
    };
    vdsState.isRecording = true;
    updateSTTInputIndicators();
  } catch (err) {
    console.error('[VDS] Failed:', err);
    showToast('マイクアクセス拒否', 'error');
    stopVDS();
  }
}

function beginVDSUtterance(reason) {
  if (!vdsState.isRecording || vdsState.isStopping || vdsState.vadSpeechActive) return false;
  if (isVDSInCooldown(Date.now())) return false;
  resetVDSUtteranceState();
  vdsState.vadSpeechActive = true;
  vdsState.vadSilenceStartedAt = 0;
  vdsState.vadLastVoiceAt = Date.now();
  vdsState.utteranceStartedAtMS = vdsState.vadLastVoiceAt;
  vdsState.latencySpeechStartMS = typeof nowLatencyMS === 'function' ? nowLatencyMS() : Date.now();
  interruptChatOutputForUserInput('vds_voice_start');
  interruptIdleChatForUserInput('vds_voice_start');
  if (typeof recordLatencyMetric === 'function') {
    recordLatencyMetric('vds', 'speech_start', {
      atMS: vdsState.latencySpeechStartMS,
      detail: String(reason || 'vad_voice'),
      session: vdsState.sessionID || '',
    });
  }
  connectVDSWebSocket();
  updateSTTInputIndicators();
  return true;
}

function handleVDSVADFrame(pcm16, level) {
  if (!vdsState.isRecording) return;
  const now = Date.now();
  const inputLevel = Math.max(0, Number(level) || 0);
  if (isVDSInCooldown(now)) {
    vdsState.vadSpeechActive = false;
    vdsState.vadSilenceStartedAt = 0;
    return;
  }
  if (vdsState.vadSpeechActive && vdsState.utteranceStartedAtMS && now - vdsState.utteranceStartedAtMS >= VDS_MAX_UTTERANCE_MS) {
    commitVDSUtterance('max_duration');
    return;
  }
  const isVoice = inputLevel >= (vdsState.vadSpeechActive ? VDS_VAD_END_LEVEL : VDS_VAD_START_LEVEL);
  if (isVoice) {
    vdsState.vadLastVoiceAt = now;
    vdsState.vadSilenceStartedAt = 0;
    if (vdsState.isStopping) return;
    beginVDSUtterance('vad_voice');
    return;
  }
  if (!vdsState.vadSpeechActive || vdsState.isStopping) return;
  if (!vdsState.vadSilenceStartedAt) {
    vdsState.vadSilenceStartedAt = now;
    return;
  }
  if (now - vdsState.vadSilenceStartedAt >= VDS_SILENCE_END_MS) {
    stopVDSUtteranceBySilence();
  }
}

function stopVDSUtteranceBySilence() {
  if (vdsState.isStopping || !vdsState.vadSpeechActive) return false;
  const speechMS = vdsState.utteranceStartedAtMS ? Date.now() - vdsState.utteranceStartedAtMS : 0;
  if (speechMS > 0 && speechMS < VDS_MIN_SPEECH_MS) {
    discardVDSUtterance('too_short:' + String(speechMS) + 'ms');
    return false;
  }
  return commitVDSUtterance('silence');
}

function discardVDSUtterance(reason) {
  const discardReason = String(reason || 'discard').trim() || 'discard';
  completeVDSUtteranceStop(discardReason);
  vdsState.utteranceStartedAtMS = 0;
  vdsState.cooldownUntilMS = Date.now() + VDS_COOLDOWN_MS;
  clearVDSCooldownTimer();
  vdsState.cooldownTimer = setTimeout(() => {
    vdsState.cooldownTimer = null;
    vdsState.cooldownUntilMS = 0;
    updateSTTInputIndicators();
  }, VDS_COOLDOWN_MS);
  pushDebugTrace('vds', {
    time: ftime(new Date().toISOString()),
    step: 'discard',
    text: discardReason,
  });
  renderDebugPanels();
  updateSTTInputIndicators();
}

function commitVDSUtterance(reason) {
  if (vdsState.isStopping) return false;
  if (!vdsState.vadSpeechActive && !vdsState.utteranceID && !vdsState.sentAudioBytes) return false;
  vdsState.isStopping = true;
  vdsState.vadSpeechActive = false;
  console.log('[VDS] Committing utterance:', String(reason || 'commit'));
  flushVDSAudioChunkBuffer();
  sendVDSTailSilence();
  sendVDSSessionCommit();
  scheduleVDSFinalWaitTimeout();
  updateSTTInputIndicators();
  return true;
}

function completeVDSUtteranceStop(reason) {
  clearVDSFinalWaitTimer();
  clearVDSDeltaIdleTimer();
  vdsState.isStopping = false;
  vdsState.vadSpeechActive = false;
  vdsState.vadSilenceStartedAt = 0;
  vdsState.utteranceStartedAtMS = 0;
  vdsState.streamReady = false;
  vdsState.chunkBuffer = [];
  if (vdsState.ws && (vdsState.ws.readyState === WebSocket.OPEN || vdsState.ws.readyState === WebSocket.CONNECTING)) {
    try { vdsState.ws.close(); } catch (_) {}
  }
  vdsState.ws = null;
  updateSTTInputIndicators();
  console.log('[VDS] Utterance complete:', String(reason || 'done'));
}

function stopVDS() {
  if (vdsState.isStopping) return;
  if (commitVDSUtterance('mic_off')) {
    return;
  }
  abortVDSImmediately('mic_off');
}

function abortVDSImmediately(reason) {
  const abortReason = String(reason || 'immediate_abort').trim() || 'immediate_abort';
  vdsState.isRecording = false;
  vdsState.isStopping = false;
  vdsState.vadSpeechActive = false;
  vdsState.vadSilenceStartedAt = 0;
  vdsState.vadLastVoiceAt = 0;
  vdsState.utteranceStartedAtMS = 0;
  vdsState.cooldownUntilMS = 0;
  clearVDSCooldownTimer();
  clearVDSFinalWaitTimer();
  clearVDSDeltaIdleTimer();
  if (vdsState.scriptNode) {
    try { vdsState.scriptNode.disconnect(); } catch (_) {}
    vdsState.scriptNode = null;
  }
  if (vdsState.audioContext) {
    try { vdsState.audioContext.close(); } catch (_) {}
    vdsState.audioContext = null;
  }
  if (vdsState.audioStream) {
    vdsState.audioStream.getTracks().forEach((t) => {
      try { t.stop(); } catch (_) {}
    });
    vdsState.audioStream = null;
  }
  if (vdsState.ws && (vdsState.ws.readyState === WebSocket.OPEN || vdsState.ws.readyState === WebSocket.CONNECTING)) {
    try { vdsState.ws.close(); } catch (_) {}
  }
  vdsState.ws = null;
  vdsState.streamReady = false;
  vdsState.chunkBuffer = [];
  updateVDSInputLevel(0);
  updateSTTInputIndicators();
  console.log('[VDS] Aborted:', abortReason);
}

async function toggleVoiceInput() {
  if (isVDSSubMode()) {
    await toggleVDS();
    return;
  }
  await toggleSTT();
}

async function toggleSTT() {
  if (isSTTTestRecording()) {
    showToast('テスト録音中は通常マイクを使えません', 'error');
    return;
  }
  if (sttState.isRecording || sttState.isStarting) {
    stopSTT();
  } else {
    if (typeof ensureVoiceChatForMobileControl === 'function' && !ensureVoiceChatForMobileControl()) {
      showToast('音声入力は通常チャットでのみ有効です', 'error');
      return;
    }
    await startSTT();
  }
}

async function startSTT() {
  if (isSTTTestRecording()) {
    showToast('テスト録音中は通常マイクを使えません', 'error');
    return;
  }
  if (typeof ensureVoiceChatForMobileControl !== 'function' && typeof globalThis !== 'undefined') {
    globalThis.ensureVoiceChatForMobileControl = function() { return true; };
  }
  if (!ensureVoiceChatForMobileControl()) {
    showToast('音声入力は通常チャットでのみ有効です', 'error');
    return;
  }
  const externalAudioStream = typeof getSTTExternalAudioStream === 'function' ? getSTTExternalAudioStream() : null;
  const microphoneUnavailable = externalAudioStream ? '' : getSTTMicrophoneUnavailableReason();
  if (microphoneUnavailable) {
    sttState.isStarting = false;
    sttState.captureActionError = describeSTTActionError('STT microphone start unavailable', microphoneUnavailable);
    if (typeof setSTTCaptionError === 'function') setSTTCaptionError(sttState.captureActionError);
    updateSTTInputIndicators();
    showToast('マイク利用不可', 'error');
    return;
  }
  try {
    sttState.isStarting = true;
    sttState.captureActionError = '';
    updateSTTInputIndicators();
    if (typeof claimViewerControl === 'function') {
      await claimViewerControl('input', 'stt_start');
    }
    sttState.isStopping = false;
    sttState.captureLog = [];
    sttState.capturePCM = [];
    sttState.captureStartedAt = '';
    sttState.captureEndedAt = '';
    sttState.captureEventID = '';
    sttState.sentAudioSamples = 0;
    sttState.sentAudioBytes = 0;
    sttState.sentAudioFrames = 0;
    sttState.lastLoggedAudioSecond = 0;
    sttState.lastRecognitionText = '';
    sttState.lastRecognitionType = '';
    sttState.partialCaptionText = '';
    sttState.finalCaptionText = '';
    sttState.errorCaptionText = '';
    sttState.stopControlSent = false;
    sttState.finalReceived = false;
    sttState.vadSpeechActive = false;
    sttState.vadSilenceStartedAt = 0;
    sttState.vadLastVoiceAt = 0;
    sttState.pendingSpeechRestart = false;
    sttState.pendingSpeechRestartInterrupted = false;
    if (typeof clearSTTFinalWaitTimer === 'function') clearSTTFinalWaitTimer();
    if (typeof updateSTTCaption === 'function') updateSTTCaption();
    if (typeof updateSTTInputLevel === 'function') updateSTTInputLevel(0);
    sttState.streamReady = false;
    if (!sttState.runtimeConfigLoaded) {
      await loadViewerRuntimeConfig();
    }
    sttState.audioStream = externalAudioStream || await navigator.mediaDevices.getUserMedia({
      audio: {
        noiseSuppression: true,
        echoCancellation: true,
        autoGainControl: true
      }
    });
    recordSTTCaptureEvent('start', externalAudioStream ? 'source=display_audio' : 'source=microphone');
    sttState.audioContext = new (window.AudioContext || window.webkitAudioContext)();
    sttState.inputSampleRate = Math.round(sttState.audioContext.sampleRate || 48000);
    sttState.sampleRate = 16000;
    const source = sttState.audioContext.createMediaStreamSource(sttState.audioStream);

    // ScriptProcessor is enough here because we only need mono PCM16 chunks for STT.
    sttState.scriptNode = sttState.audioContext.createScriptProcessor(4096, 1, 1);
    source.connect(sttState.scriptNode);
    sttState.scriptNode.connect(sttState.audioContext.destination);

    sttState.scriptNode.onaudioprocess = (e) => {
      if (!sttState.isRecording) return;
      const pcm = e.inputBuffer.getChannelData(0);
      const pcm16 = resampleToPCM16(pcm, sttState.inputSampleRate || 48000, 16000);
      const level = calculateSTTInputLevel(pcm16);
      updateSTTInputLevel(level);
      handleSTTVADFrame(pcm16, level);
      if (sttState.vadSpeechActive && !sttState.isStopping) {
        sttState.draftBuffer.push(...pcm16);
        sttState.capturePCM.push(...pcm16);
        sendSTTAudioChunk(pcm16);
      } else if (sttState.isStopping) {
        sttState.capturePCM.push(...pcm16);
      }
      const maxDraftSamples = sttState.sampleRate;
      if (sttState.draftBuffer.length > maxDraftSamples) {
        sttState.draftBuffer = sttState.draftBuffer.slice(-maxDraftSamples);
      }
    };

    sttState.isRecording = true;
    sttState.isStarting = false;
    updateSTTInputIndicators();
  } catch (err) {
    sttState.isStarting = false;
    sttState.isRecording = false;
    sttState.captureActionError = describeSTTActionError('STT microphone start unavailable', err);
    updateSTTInputIndicators();
    console.error('[STT] Failed:', err);
    showToast('マイクアクセス拒否', 'error');
    stopSTT();
  }
}

function connectSTTWebSocket() {
  if (sttState.isStopping) return;
  if (!sttState.keepSessionChannel && !sttState.isRecording) return;
  if (sttState.ws && sttState.ws.readyState === WebSocket.OPEN) return;
  if (sttState.ws && sttState.ws.readyState === WebSocket.CONNECTING) return;
  if (typeof formatSTTServerEventPayload !== 'function' && typeof globalThis !== 'undefined') {
    globalThis.formatSTTServerEventPayload = function(_msg, fallbackText) { return fallbackText; };
  }
  sttState.ws = new WebSocket(buildSTTWebSocketURL());
  sttState.ws.binaryType = 'arraybuffer';
  sttState.ws.onopen = () => {
    sttState.reconnecting = false;
    recordSTTCaptureEvent('ws_open', '');
    if (sttState.latencySpeechStartMS && typeof recordLatencyMetric === 'function') {
      const sttWsOpenMS = typeof nowLatencyMS === 'function' ? nowLatencyMS() : Date.now();
      recordLatencyMetric('network', 'stt_ws_open', {
        valueMS: sttWsOpenMS - sttState.latencySpeechStartMS,
        session: sttState.captureSessionID || '',
      });
    }
    sendSTTStartControl();
    flushSTTAudioChunkBuffer();
    console.log('[STT] Connected - streaming PCM16 16kHz chunks');
    updateSTTInputIndicators();
  };
  sttState.ws.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data);
        const inp = document.getElementById('inp');
        if (msg.type) {
          const eventText = extractSTTMessageText(msg);
          pushDebugTrace('stt', {
            time: ftime(new Date().toISOString()),
            step: msg.type,
            text: short(eventText, 240),
          });
          if (msg.event_id && !sttState.captureEventID) {
            sttState.captureEventID = String(msg.event_id).trim();
          }
          if (msg.type === 'session_info' && msg.session_id) {
            sttState.captureSessionID = String(msg.session_id).trim() || '(unknown)';
            updateSTTInputIndicators();
          } else if (msg.session_id && sttState.captureSessionID === '(unknown)') {
            sttState.captureSessionID = String(msg.session_id).trim() || '(unknown)';
            updateSTTInputIndicators();
          } else if (msg.type === 'ready') {
            sttState.streamReady = true;
            if (msg.sample_rate) sttState.sampleRate = Number(msg.sample_rate) || sttState.sampleRate;
            if (sttState.latencySpeechStartMS && typeof recordLatencyMetric === 'function') {
              const sttReadyMS = typeof nowLatencyMS === 'function' ? nowLatencyMS() : Date.now();
              recordLatencyMetric('stt', 'stream_ready', {
                valueMS: sttReadyMS - sttState.latencySpeechStartMS,
                session: sttState.captureSessionID || '',
              });
            }
            updateSTTInputIndicators();
          } else if (msg.type === 'transcribing') {
            recordSTTCaptureEvent('progress', 'transcribing');
          } else if (msg.type === 'progress') {
            recordSTTCaptureEvent('progress', `${msg.duration || 0}s / ${msg.bytes || 0} bytes`);
          }
          if (msg.type !== 'progress') {
            recordSTTCaptureEvent(msg.type, formatSTTServerEventPayload(msg, eventText));
          }
          if (typeof renderSTTDebugPanelsSafely === 'function') renderSTTDebugPanelsSafely();
        }
        if ((msg.type === 'draft' || msg.type === 'partial') && extractSTTMessageText(msg)) {
          const draftText = extractSTTMessageText(msg);
          sttState.lastRecognitionText = String(draftText || '').trim();
          sttState.lastRecognitionType = msg.type;
          sttState.partialCaptionText = sttState.lastRecognitionText;
          sttState.finalCaptionText = '';
          updateSTTCaption();
          console.log('[STT] Draft:', draftText);
	        } else if (msg.type === 'final') {
	          if (!isThisViewerActiveInput()) {
	            recordSTTCaptureEvent('final_ignored', 'inactive input viewer');
	            updateSTTInputIndicators();
	            return;
	          }
	          sttState.lastRecognitionText = String(msg.text || '').trim();
          sttState.lastRecognitionType = 'final';
          sttState.finalReceived = true;
          sttState.latencyFinalMS = typeof nowLatencyMS === 'function' ? nowLatencyMS() : Date.now();
          if (typeof recordLatencyMetric === 'function') {
            recordLatencyMetric('stt', 'final_received', {
              atMS: sttState.latencyFinalMS,
              valueMS: sttState.latencySpeechStartMS ? sttState.latencyFinalMS - sttState.latencySpeechStartMS : NaN,
              detail: 'len=' + String(sttState.lastRecognitionText.length),
              session: sttState.captureSessionID || '',
            });
          }
          if (sttState.latencyStopMS && typeof recordLatencyMetric === 'function') {
            recordLatencyMetric('stt', 'stop_to_final', {
              atMS: sttState.latencyFinalMS,
              valueMS: sttState.latencyFinalMS - sttState.latencyStopMS,
              session: sttState.captureSessionID || '',
            });
          }
          clearSTTFinalWaitTimer();
          sttState.finalCaptionText = sttState.lastRecognitionText;
          sttState.partialCaptionText = '';
          sttState.errorCaptionText = '';
          updateSTTCaption();
          console.log('[STT] Final:', msg.text);
          const finalInputText = formatSTTFinalInputText(sttState.lastRecognitionText, msg);
          handleSTTFinalText(finalInputText);
          // Clear buffer for next utterance (server-side VAD detected end)
          sttState.draftBuffer = [];
          if (sttState.isStopping && sttState.ws && sttState.ws.readyState === WebSocket.OPEN) {
            sttState.ws.close();
          }
        } else if (msg.type === 'reply_reset') {
          console.log('[STT] LLM reply starting...');
        } else if (msg.type === 'reply_delta' && msg.text) {
          console.log('[STT] LLM reply:', msg.text);
        } else if (msg.type === 'closed') {
          if (sttState.isStopping && sttState.ws && sttState.ws.readyState === WebSocket.OPEN) {
            sttState.ws.close();
          }
        } else if (msg.type === 'empty') {
          console.log('[STT] Empty result');
        } else if (msg.type === 'error') {
          const sttErrorText = extractSTTMessageText(msg) || 'unknown error';
          if (sttState.finalReceived) {
            recordSTTCaptureEvent('error', 'ignored after final: ' + sttErrorText);
            console.warn('[STT] Error ignored after final:', msg.error || msg.message);
            updateSTTInputIndicators();
            return;
          }
          sttState.finalReceived = true;
          if (typeof recordLatencyMetric === 'function') {
            const sttErrorMS = typeof nowLatencyMS === 'function' ? nowLatencyMS() : Date.now();
            recordLatencyMetric('stt', 'error_received', {
              valueMS: sttState.latencySpeechStartMS ? sttErrorMS - sttState.latencySpeechStartMS : NaN,
              detail: sttErrorText,
              session: sttState.captureSessionID || '',
            });
          }
          clearSTTFinalWaitTimer();
          sttState.captureActionError = describeSTTActionError('STT recognition unavailable', sttErrorText);
          if (typeof setSTTCaptionError === 'function') setSTTCaptionError(sttErrorText);
          updateSTTInputIndicators();
          console.error('[STT] Error:', msg.error || msg.message);
          showToast('認識エラー', 'error');
          if (sttState.isStopping && sttState.ws && sttState.ws.readyState === WebSocket.OPEN) {
            sttState.ws.close();
          }
        }
      } catch (err) {
        sttState.captureActionError = describeSTTActionError('STT message parse unavailable', err);
        if (typeof setSTTCaptionError === 'function') setSTTCaptionError(sttState.captureActionError);
        updateSTTInputIndicators();
        console.error('[STT] Parse error:', err);
      }
  };
  sttState.ws.onerror = (event) => {
    recordSTTCaptureEvent('ws_error', event && event.message ? event.message : 'connection error');
    sttState.captureActionError = describeSTTActionError(
      'STT websocket unavailable',
      event && event.message ? event : 'connection error',
    );
    if (typeof setSTTCaptionError === 'function') setSTTCaptionError(sttState.captureActionError);
    updateSTTInputIndicators();
    if (!sttState.isStopping && sttState.keepSessionChannel) scheduleSTTReconnect();
  };
  sttState.ws.onclose = () => {
    recordSTTCaptureEvent('ws_close', '');
    sttState.streamReady = false;
    sttState.ws = null;
    updateSTTInputIndicators();
    if (sttState.isStopping) {
      completeSTTStop();
      return;
    }
    if (!sttState.isStopping && sttState.keepSessionChannel) scheduleSTTReconnect();
  };
}

function buildSTTWebSocketURL() {
  const base = String(sttState.voiceBridgeURL || '');
  const viewerClientID = (typeof viewerControl !== 'undefined' && viewerControl && viewerControl.clientId)
    ? String(viewerControl.clientId || '')
    : '';
  if (!viewerClientID) return base;
  const sep = base.includes('?') ? '&' : '?';
  return base + sep + 'viewer_client_id=' + encodeURIComponent(viewerClientID);
}

function extractSTTMessageText(msg) {
  if (!msg) return '';
  if (msg.text) return String(msg.text);
  if (msg.message) return String(msg.message);
  if (typeof msg.error === 'string') return msg.error;
  if (msg.error && msg.error.message) return String(msg.error.message);
  if (msg.error_code) return String(msg.error_code);
  return '';
}

function resampleToPCM16(input, fromRate, toRate) {
  const sourceRate = Math.max(1, Number(fromRate) || 48000);
  const targetRate = Math.max(1, Number(toRate) || 16000);
  const outputLength = Math.max(1, Math.floor(input.length * targetRate / sourceRate));
  const output = new Int16Array(outputLength);
  const ratio = sourceRate / targetRate;
  for (let i = 0; i < outputLength; i++) {
    const pos = i * ratio;
    const left = Math.floor(pos);
    const right = Math.min(input.length - 1, left + 1);
    const frac = pos - left;
    const sample = (input[left] || 0) * (1 - frac) + (input[right] || 0) * frac;
    output[i] = Math.max(-32768, Math.min(32767, Math.round(sample * 32767)));
  }
  return output;
}

function calculateSTTInputLevel(pcm16) {
  if (!pcm16 || pcm16.length === 0) return 0;
  let sumSquares = 0;
  for (let i = 0; i < pcm16.length; i++) {
    const sample = Number(pcm16[i]) || 0;
    sumSquares += sample * sample;
  }
  const rms = Math.sqrt(sumSquares / pcm16.length);
  return Math.max(0, Math.min(100, Math.round((rms / 2400) * 100)));
}

function updateSTTInputLevel(level) {
  sttState.inputLevel = Math.max(0, Math.min(100, Number(level) || 0));
  [micBtn, (typeof labMicBtn !== 'undefined' ? labMicBtn : null)].forEach((btn) => {
    if (!btn) return;
    btn.style.setProperty('--mic-level-pct', `${Math.round(sttState.inputLevel)}%`);
    btn.classList.toggle('has-level', sttState.isRecording && sttState.inputLevel > 0);
  });
}

function resetSTTUtteranceState() {
  sttState.captureLog = [];
  sttState.capturePCM = [];
  sttState.captureStartedAt = '';
  sttState.captureEndedAt = '';
  sttState.captureEventID = '';
  sttState.captureActionError = '';
  sttState.latencySpeechStartMS = 0;
  sttState.latencyStopMS = 0;
  sttState.latencyFinalMS = 0;
  sttState.sentAudioSamples = 0;
  sttState.sentAudioBytes = 0;
  sttState.sentAudioFrames = 0;
  sttState.lastLoggedAudioSecond = 0;
  sttState.lastRecognitionText = '';
  sttState.lastRecognitionType = '';
  sttState.partialCaptionText = '';
  sttState.finalCaptionText = '';
  sttState.errorCaptionText = '';
  sttState.stopControlSent = false;
  sttState.finalReceived = false;
  sttState.chunkBuffer = [];
  sttState.draftBuffer = [];
  if (typeof clearSTTFinalWaitTimer === 'function') clearSTTFinalWaitTimer();
  if (typeof updateSTTCaption === 'function') updateSTTCaption();
}

function beginSTTUtterance(reason) {
  if (!sttState.isRecording || sttState.isStopping || sttState.vadSpeechActive) return false;
  resetSTTUtteranceState();
  sttState.vadSpeechActive = true;
  sttState.vadSilenceStartedAt = 0;
  sttState.vadLastVoiceAt = Date.now();
  sttState.pendingSpeechRestart = false;
  sttState.pendingSpeechRestartInterrupted = false;
  sttState.latencySpeechStartMS = typeof nowLatencyMS === 'function' ? nowLatencyMS() : Date.now();
  sttState.latencyStopMS = 0;
  sttState.latencyFinalMS = 0;
  interruptChatOutputForUserInput('stt_voice_start');
  interruptIdleChatForUserInput('stt_voice_start');
  if (typeof recordLatencyMetric === 'function') {
    recordLatencyMetric('stt', 'speech_start', {
      atMS: sttState.latencySpeechStartMS,
      detail: String(reason || 'vad_voice'),
      session: sttState.captureSessionID || '',
    });
  }
  recordSTTCaptureEvent('speech_start', String(reason || 'vad_voice'));
  connectSTTWebSocket();
  updateSTTInputIndicators();
  return true;
}

function handleSTTVADFrame(pcm16, level) {
  if (!sttState.isRecording) return;
  const now = Date.now();
  const inputLevel = Math.max(0, Number(level) || 0);
  const isVoice = inputLevel >= (sttState.vadSpeechActive ? STT_VAD_END_LEVEL : STT_VAD_START_LEVEL);
  if (isVoice) {
    sttState.vadLastVoiceAt = now;
    sttState.vadSilenceStartedAt = 0;
    if (sttState.isStopping) {
      sttState.pendingSpeechRestart = true;
      if (!sttState.pendingSpeechRestartInterrupted) {
        sttState.pendingSpeechRestartInterrupted = true;
        interruptChatOutputForUserInput('stt_voice_resume');
        interruptIdleChatForUserInput('stt_voice_resume');
        recordSTTCaptureEvent('speech_start', 'pending_restart');
      }
      return;
    }
    beginSTTUtterance('vad_voice');
    return;
  }
  if (!sttState.vadSpeechActive || sttState.isStopping) return;
  if (!sttState.vadSilenceStartedAt) {
    sttState.vadSilenceStartedAt = now;
    return;
  }
  if (now - sttState.vadSilenceStartedAt >= STT_SILENCE_END_MS) {
    stopSTTUtteranceBySilence();
  }
}

function sendSTTAudioChunk(pcm16) {
  if (!sttState.isRecording || !sttState.vadSpeechActive) return;
  sttState.chunkBuffer.push(...pcm16);
  if (!sttState.ws || sttState.ws.readyState !== WebSocket.OPEN) return;
  while (sttState.chunkBuffer.length >= sttState.chunkSamples) {
    const chunk = new Int16Array(sttState.chunkBuffer.slice(0, sttState.chunkSamples));
    sttState.chunkBuffer = sttState.chunkBuffer.slice(sttState.chunkSamples);
    sttState.ws.send(chunk.buffer);
    recordSTTAudioSent(chunk.length);
  }
}

function flushSTTAudioChunkBuffer() {
  if (!sttState.ws || sttState.ws.readyState !== WebSocket.OPEN || sttState.chunkBuffer.length === 0) return false;
  const chunk = new Int16Array(sttState.chunkBuffer);
  sttState.chunkBuffer = [];
  sttState.ws.send(chunk.buffer);
  recordSTTAudioSent(chunk.length);
  return true;
}

function recordSTTAudioSent(samples) {
  const count = Math.max(0, Number(samples) || 0);
  if (count <= 0) return;
  sttState.sentAudioSamples += count;
  sttState.sentAudioBytes += count * 2;
  sttState.sentAudioFrames += 1;
  const sampleRate = Number(sttState.sampleRate || 16000) || 16000;
  const seconds = sampleRate > 0 ? sttState.sentAudioSamples / sampleRate : 0;
  const wholeSecond = Math.floor(seconds);
  if (wholeSecond > sttState.lastLoggedAudioSecond) {
    sttState.lastLoggedAudioSecond = wholeSecond;
    recordSTTCaptureEvent('audio_sent', `${seconds.toFixed(3)}s / ${sttState.sentAudioBytes} bytes / frame ${sttState.sentAudioFrames}`);
  }
}

function sendSTTStopTailSilence() {
  if (!sttState.ws || sttState.ws.readyState !== WebSocket.OPEN) return false;
  const sampleRate = Number(sttState.sampleRate || 16000) || 16000;
  const totalSamples = Math.max(0, Math.round(sampleRate * STT_STOP_TAIL_SILENCE_MS / 1000));
  if (totalSamples <= 0) return false;
  const chunkSamples = Math.max(1, Number(sttState.chunkSamples || 1600) || 1600);
  for (let offset = 0; offset < totalSamples; offset += chunkSamples) {
    const size = Math.min(chunkSamples, totalSamples - offset);
    sttState.ws.send(new Int16Array(size).buffer);
  }
  recordSTTCaptureEvent('progress', `stop tail silence ${STT_STOP_TAIL_SILENCE_MS}ms`);
  return true;
}

function sendSTTStartControl() {
  if (!sttState.ws || sttState.ws.readyState !== WebSocket.OPEN) return false;
  const sampleRate = Number(sttState.sampleRate || 16000) || 16000;
  const control = {
    type: 'start',
    sample_rate: sampleRate,
    channels: 1,
    format: 'pcm_s16le',
  };
  sttState.ws.send(JSON.stringify(control));
  recordSTTCaptureEvent('start', `${sampleRate}Hz pcm_s16le mono`);
  return true;
}

function sendSTTStopControl(reason) {
  if (!sttState.ws || sttState.ws.readyState !== WebSocket.OPEN) return false;
  if (sttState.stopControlSent) return true;
  sttState.ws.send(JSON.stringify({ type: 'stop' }));
  sttState.stopControlSent = true;
  sttState.latencyStopMS = typeof nowLatencyMS === 'function' ? nowLatencyMS() : Date.now();
  const reasonText = String(reason || 'requested').trim() || 'requested';
  if (typeof recordLatencyMetric === 'function') {
    recordLatencyMetric('stt', 'stop_sent', {
      atMS: sttState.latencyStopMS,
      valueMS: sttState.latencySpeechStartMS ? sttState.latencyStopMS - sttState.latencySpeechStartMS : NaN,
      detail: reasonText,
      session: sttState.captureSessionID || '',
    });
  }
  recordSTTCaptureEvent('stop', reasonText);
  return true;
}

function clearSTTFinalWaitTimer() {
  if (!sttState.finalWaitTimer) return;
  clearTimeout(sttState.finalWaitTimer);
  sttState.finalWaitTimer = null;
}

function scheduleSTTFinalWaitTimeout() {
  clearSTTFinalWaitTimer();
  sttState.finalWaitTimer = setTimeout(() => {
    sttState.finalWaitTimer = null;
    if (!sttState.isStopping) return;
    if (finalizeSTTLocalDraft('timeout')) {
      updateSTTInputIndicators();
      return;
    }
    sttState.captureActionError = describeSTTActionError('STT final unavailable', 'timed out waiting for final');
    if (typeof setSTTCaptionError === 'function') setSTTCaptionError(sttState.captureActionError);
    recordSTTCaptureEvent('error', 'timed out waiting for final');
    updateSTTInputIndicators();
    if (sttState.ws && (sttState.ws.readyState === WebSocket.OPEN || sttState.ws.readyState === WebSocket.CONNECTING)) {
      sttState.ws.close();
      return;
    }
    completeSTTStop();
  }, STT_FINAL_WAIT_TIMEOUT_MS);
}

function finalizeSTTLocalDraft(reason) {
  if (sttState.finalReceived) return false;
  const finalText = String(sttState.lastRecognitionText || '').trim();
  if (!finalText || sttState.lastRecognitionType === 'final') return false;
  sttState.finalReceived = true;
  sttState.lastRecognitionType = 'final';
  sttState.latencyFinalMS = typeof nowLatencyMS === 'function' ? nowLatencyMS() : Date.now();
  if (typeof recordLatencyMetric === 'function') {
    recordLatencyMetric('stt', 'final_received', {
      atMS: sttState.latencyFinalMS,
      valueMS: sttState.latencySpeechStartMS ? sttState.latencyFinalMS - sttState.latencySpeechStartMS : NaN,
      detail: 'local_draft:' + String(reason || 'local_draft'),
      session: sttState.captureSessionID || '',
    });
  }
  if (sttState.latencyStopMS && typeof recordLatencyMetric === 'function') {
    recordLatencyMetric('stt', 'stop_to_final', {
      atMS: sttState.latencyFinalMS,
      valueMS: sttState.latencyFinalMS - sttState.latencyStopMS,
      detail: 'local_draft',
      session: sttState.captureSessionID || '',
    });
  }
  sttState.finalCaptionText = finalText;
  sttState.partialCaptionText = '';
  sttState.errorCaptionText = '';
  if (typeof updateSTTCaption === 'function') updateSTTCaption();
  recordSTTCaptureEvent('final', finalText);
  recordSTTCaptureEvent('final_fallback', String(reason || 'local_draft'));
  handleSTTFinalText(finalText);
  return true;
}

function formatSTTFinalInputText(text, msg) {
  const finalText = String(text || '').trim();
  if (!finalText) return '';
  if (msg && msg.stt_fallback_required === true) {
    return '[音声入力: 暫定認識 / 要確認]\n' + finalText;
  }
  return finalText;
}

function handleSTTFinalText(text) {
  const finalText = String(text || '').trim();
  if (!finalText) return;
  if (!isVoiceChatAllowed()) {
    console.warn('[STT] Final ignored outside normal chat:', finalText);
    return;
  }
  const inp = document.getElementById('inp');
  if (inp) {
    suppressInputInterrupt = true;
    inp.value = finalText;
    autoResize();
    if (typeof syncMainInputToLab === 'function') syncMainInputToLab();
    const isLabMode = typeof document !== 'undefined' && document.body && document.body.classList.contains('lab-mode');
    const focusTarget = typeof labInp !== 'undefined' && isLabMode && labInp ? labInp : inp;
    focusTarget.focus();
    if (typeof setTimeout === 'function') {
      setTimeout(() => { suppressInputInterrupt = false; }, 0);
    } else {
      suppressInputInterrupt = false;
    }
  }
  if (!sending) {
    send();
  }
  if (sttState.ws && sttState.ws.readyState === WebSocket.OPEN && !sttState.isRecording) {
    sttState.ws.close();
  }
}

function scheduleSTTReconnect() {
  if (sttState.reconnecting) return;
  sttState.reconnecting = true;
  updateSTTInputIndicators();
  if (sttState.reconnectTimer) clearTimeout(sttState.reconnectTimer);
  sttState.reconnectTimer = setTimeout(() => {
    sttState.reconnectTimer = null;
    if (sttState.isStopping || !sttState.keepSessionChannel) {
      sttState.reconnecting = false;
      updateSTTInputIndicators();
      return;
    }
    connectSTTWebSocket();
  }, 700);
}

function pcm16ToWav(pcmBuffer, sampleRate = sttState.sampleRate || 48000) {
  const numChannels = 1;
  const bitsPerSample = 16;
  const byteRate = sampleRate * numChannels * bitsPerSample / 8;
  const blockAlign = numChannels * bitsPerSample / 8;
  const dataSize = pcmBuffer.length * 2;
  const header = new ArrayBuffer(44);
  const view = new DataView(header);
  // RIFF
  view.setUint32(0, 0x52494646, false); // "RIFF"
  view.setUint32(4, 36 + dataSize, true);
  view.setUint32(8, 0x57415645, false); // "WAVE"
  // fmt
  view.setUint32(12, 0x666d7420, false); // "fmt "
  view.setUint32(16, 16, true);
  view.setUint16(20, 1, true); // PCM
  view.setUint16(22, numChannels, true);
  view.setUint32(24, sampleRate, true);
  view.setUint32(28, byteRate, true);
  view.setUint16(32, blockAlign, true);
  view.setUint16(34, bitsPerSample, true);
  // data
  view.setUint32(36, 0x64617461, false); // "data"
  view.setUint32(40, dataSize, true);

  const wavBuffer = new ArrayBuffer(44 + dataSize);
  new Uint8Array(wavBuffer).set(new Uint8Array(header), 0);
  new Int16Array(wavBuffer, 44).set(pcmBuffer, 0);
  return wavBuffer;
}

function sendDraft() {
  // Deprecated: realtime STT now streams PCM16 binary chunks directly.
}

function stopSTT() {
  if (sttState.isStopping) return;
  sttState.isStopping = true;
  console.log('[STT] Stopping');
  sttState.isRecording = false;
  sttState.vadSpeechActive = false;
  sttState.vadSilenceStartedAt = 0;
  sttState.pendingSpeechRestart = false;
  sttState.pendingSpeechRestartInterrupted = false;
  sttState.isStarting = false;
  if (typeof updateSTTInputLevel === 'function') updateSTTInputLevel(0);

  if (sttState.draftTimer) sttState.draftTimer();
  if (sttState.reconnectTimer) {
    clearTimeout(sttState.reconnectTimer);
    sttState.reconnectTimer = null;
  }
  sttState.reconnecting = false;

  if (sttState.scriptNode) {
    sttState.scriptNode.disconnect();
    sttState.scriptNode = null;
  }
  if (sttState.audioContext) {
    sttState.audioContext.close();
    sttState.audioContext = null;
  }
  if (sttState.audioStream) {
    sttState.audioStream.getTracks().forEach(t => t.stop());
    sttState.audioStream = null;
  }
  if (sttState.finalReceived) {
    if (typeof clearSTTFinalWaitTimer === 'function') clearSTTFinalWaitTimer();
    sttState.chunkBuffer = [];
    if (sttState.ws && sttState.ws.readyState === WebSocket.OPEN) {
      sttState.ws.close();
      updateSTTInputIndicators();
      return;
    }
  }
  if (sttState.ws && sttState.ws.readyState === WebSocket.OPEN) {
    if (typeof flushSTTAudioChunkBuffer === 'function') flushSTTAudioChunkBuffer();
    if (typeof sendSTTStopTailSilence === 'function') sendSTTStopTailSilence();
    if (typeof sendSTTStopControl === 'function') sendSTTStopControl();
    if (typeof scheduleSTTFinalWaitTimeout === 'function') scheduleSTTFinalWaitTimeout();
    updateSTTInputIndicators();
    return;
  }
  sttState.chunkBuffer = [];
  if (sttState.ws && sttState.ws.readyState === WebSocket.CONNECTING) {
    sttState.ws.close();
    if (typeof scheduleSTTFinalWaitTimeout === 'function') scheduleSTTFinalWaitTimeout();
    updateSTTInputIndicators();
    return;
  }

  completeSTTStop();
}

function stopSTTUtteranceBySilence() {
  if (sttState.isStopping || !sttState.vadSpeechActive) return false;
  sttState.isStopping = true;
  sttState.vadSpeechActive = false;
  const silenceMs = sttState.vadSilenceStartedAt ? Math.max(0, Date.now() - sttState.vadSilenceStartedAt) : STT_SILENCE_END_MS;
  console.log('[STT] Silence detected - finalizing utterance');
  if (typeof flushSTTAudioChunkBuffer === 'function') flushSTTAudioChunkBuffer();
  if (typeof sendSTTStopTailSilence === 'function') sendSTTStopTailSilence();
  if (typeof sendSTTStopControl === 'function') sendSTTStopControl('silence ' + String(Math.round(silenceMs)) + 'ms');
  if (typeof scheduleSTTFinalWaitTimeout === 'function') scheduleSTTFinalWaitTimeout();
  updateSTTInputIndicators();
  return true;
}

function abortSTTImmediately(reason) {
  const abortReason = String(reason || 'immediate_abort').trim() || 'immediate_abort';
  sttState.isRecording = false;
  sttState.isStarting = false;
  sttState.isStopping = false;
  sttState.vadSpeechActive = false;
  sttState.vadSilenceStartedAt = 0;
  sttState.vadLastVoiceAt = 0;
  sttState.pendingSpeechRestart = false;
  sttState.pendingSpeechRestartInterrupted = false;
  sttState.stopControlSent = false;
  if (typeof clearSTTFinalWaitTimer === 'function') clearSTTFinalWaitTimer();
  if (sttState.reconnectTimer) {
    clearTimeout(sttState.reconnectTimer);
    sttState.reconnectTimer = null;
  }
  sttState.reconnecting = false;
  if (sttState.scriptNode) {
    try { sttState.scriptNode.disconnect(); } catch (_) {}
    sttState.scriptNode = null;
  }
  if (sttState.audioContext) {
    try { sttState.audioContext.close(); } catch (_) {}
    sttState.audioContext = null;
  }
  if (sttState.audioStream) {
    sttState.audioStream.getTracks().forEach((t) => {
      try { t.stop(); } catch (_) {}
    });
    sttState.audioStream = null;
  }
  if (sttState.ws && (sttState.ws.readyState === WebSocket.OPEN || sttState.ws.readyState === WebSocket.CONNECTING)) {
    try { sttState.ws.close(); } catch (_) {}
  }
  sttState.chunkBuffer = [];
  sttState.draftBuffer = [];
  sttState.partialCaptionText = '';
  sttState.finalCaptionText = '';
  sttState.errorCaptionText = '';
  if (typeof updateSTTInputLevel === 'function') updateSTTInputLevel(0);
  if (typeof updateSTTCaption === 'function') updateSTTCaption();
  updateSTTInputIndicators();
  recordSTTCaptureEvent('stop', 'aborted: ' + abortReason);
}

function completeSTTStop() {
  if (!sttState.isStopping) return;
  if (typeof clearSTTFinalWaitTimer === 'function') clearSTTFinalWaitTimer();
  sttState.chunkBuffer = [];
  sttState.draftBuffer = [];
  sttState.stopControlSent = false;
  sttState.isStarting = false;
  sttState.isStopping = false;
  sttState.vadSpeechActive = false;
  sttState.vadSilenceStartedAt = 0;
  updateSTTInputIndicators();
  persistSTTArtifacts().then(() => {
    showToast('STTログ/WAVを tmp に保存しました', 'success');
  }).catch((err) => {
    sttState.captureActionError = describeSTTActionError('STT artifact persistence unavailable', err);
    updateSTTInputIndicators();
    console.error('[STT] persist failed:', err);
    showToast('STT保存または自動テストに失敗', 'error');
  });
  console.log('[STT] Stopped');
}

// ============================================================================
// Live2D Emotion Control
// ============================================================================

const live2dEmotionMapping = {
  'normal':   { motion: '', expression: 'f01' },
  'happy':    { motion: 'tapBody', expression: 'f02' },
  'sad':      { motion: '', expression: 'f03' },
  'angry':    { motion: 'shake', expression: 'f04' },
  'surprise': { motion: 'pinchIn', expression: 'f05' },
  'think':    { motion: '', expression: 'f06' },
  'speaking': { motion: 'tapBody', expression: 'f02' }
};

function setLive2DEmotion(characterId, emotion) {
  const frameId = characterId === 'mio' ? 
    (document.getElementById('chatLive2DMio') || document.getElementById('idleLive2DMio')) :
    document.getElementById('idleLive2DShiro');
  
  if (!frameId) {
    console.warn('[Live2D] Frame not found for:', characterId);
    return;
  }

  const state = live2dEmotionMapping[emotion] || live2dEmotionMapping['normal'];
  
  console.log('[Live2D] Setting emotion:', characterId, emotion, state);
  
  if (frameId.contentWindow) {
    frameId.contentWindow.postMessage({
      type: 'emotion',
      emotion: emotion,
      state: state
    }, '*');
  }
}

// Auto-detect emotion from message content
function detectMessageEmotion(content) {
  const text = String(content || '').toLowerCase();
  
  // Angry
  if (/怒|腹|イライラ|angry|mad/i.test(text)) return 'angry';
  
  // Surprise  
  if (/驚|まさか|すごい|surprise|wow|amazing/i.test(text)) return 'surprise';
  
  // Sad
  if (/悲しい|残念|申し訳|すみません|ごめん|sad|sorry/i.test(text)) return 'sad';
  
  // Happy
  if (/嬉しい|楽しい|ありがとう|感謝|素晴らしい|最高|happy|glad|thank/i.test(text)) return 'happy';
  
  // Think
  if (/考え|検討|確認|調べ|分析|think|consider|check/i.test(text) || /[？?]/.test(text)) return 'think';
  
  return 'normal';
}

// Update Live2D on new messages
function updateLive2DOnMessage(ev) {
  if (!ev || !ev.content) return;
  
  const from = String(ev.from || '').toLowerCase();
  const content = String(ev.content || '');
  
  // Detect emotion
  const emotion = detectMessageEmotion(content);
  
  // Update character
  if (from === 'mio') {
    setLive2DEmotion('mio', emotion);
  } else if (from === 'shiro') {
    setLive2DEmotion('shiro', emotion);
  }
}

console.log('[Live2D] Emotion control initialized');
