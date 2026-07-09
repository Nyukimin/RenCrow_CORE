import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

class FakeElement {
  constructor(id = '') {
    this.id = id;
    this.innerHTML = '';
    this.textContent = '';
    this.style = {};
    this.title = '';
    this.children = [];
    this.dataset = {};
    this.classList = {add() {}, remove() {}, toggle() {}};
  }
  addEventListener() {}
  querySelector(selector) {
    if (selector !== 'span') return null;
    if (!this.children.length) this.children.push(new FakeElement('span'));
    return this.children[0];
  }
  appendChild(child) {
    this.children.push(child);
    this.innerHTML += child.innerHTML || child.textContent || '';
    return child;
  }
  querySelectorAll() {
    return [];
  }
}

function sourceBetween(source, startNeedle, endNeedle) {
  const start = source.indexOf(startNeedle);
  const end = source.indexOf(endNeedle, start);
  assert.ok(start >= 0, `start not found: ${startNeedle}`);
  assert.ok(end > start, `end not found: ${endNeedle}`);
  return source.slice(start, end);
}

test('viewer exposes memory inspector and news pack UI hooks', () => {
  const html = fs.readFileSync('internal/adapter/viewer/viewer.html', 'utf8');
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const memoryJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/memory.js', 'utf8');
  const newsPackJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/news-pack.js', 'utf8');
  const rolesJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/roles.js', 'utf8');
  const timelineJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/timeline.js', 'utf8');
  const idleChatJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/idlechat.js', 'utf8');
  const css = fs.readFileSync('internal/adapter/viewer/assets/css/viewer.css', 'utf8');
  const opsCss = fs.readFileSync('internal/adapter/viewer/assets/css/tabs/ops.css', 'utf8');
  const viewer = html + '\n' + js + '\n' + opsJs + '\n' + memoryJs + '\n' + newsPackJs + '\n' + rolesJs + '\n' + timelineJs + '\n' + idleChatJs;
  assert.match(html, /data-tab="memory"/);
  assert.match(html, /id="panel-memory"/);
  assert.match(html, /class="theme-modern"/);
  assert.match(html, /class="theme-switcher"/);
  assert.match(html, /data-theme="classic"/);
  assert.match(html, /data-theme="modern"/);
  assert.match(html, /data-theme="compact"/);
  assert.match(html, /id="mobilePanelSelect"/);
  assert.match(html, /id="mobilePanelPrev"/);
  assert.match(html, /id="mobilePanelNext"/);
  assert.match(html, /<option value="idlechat">IdleChat<\/option>/);
  assert.doesNotMatch(html, /data-chat-route="worker">Worker/);
  assert.doesNotMatch(html, /data-chat-route="heavy">Heavy/);
  assert.doesNotMatch(html, /data-chat-route="wild">Wild/);
  assert.match(html, /id="memoryNamespace"/);
  assert.match(html, /id="memorySession"/);
  assert.match(html, /id="memoryLayerBody"/);
  assert.match(html, /id="memoryEventBody"/);
  assert.match(html, /id="recallPackBody"/);
  assert.match(html, /id="userMemoryBody"/);
  assert.match(html, /id="recallPackCount"/);
  assert.match(html, /id="userMemoryCount"/);
  assert.match(html, /id="searchCacheBody"/);
  assert.match(html, /id="webGatherSummaryBody"/);
  assert.match(html, /id="webGatherRecentBody"/);
  assert.match(html, /id="knowledgeMemoryBody"/);
  assert.match(html, /id="knowledgeMemoryDetail"/);
  assert.match(html, /id="knowledgeMemoryTypeFilter"/);
  assert.match(html, /id="knowledgeMemoryReviewFilter"/);
  assert.match(html, /id="knowledgeMemoryFlagFilter"/);
  assert.match(html, /id="knowledgePersonalCount"/);
  assert.match(html, /id="knowledgeSourceCount"/);
  assert.match(html, /id="knowledgeDreamCount"/);
  assert.match(html, /id="memoryPromoteKind"/);
  assert.match(html, /id="memoryPromoteID"/);
  assert.match(html, /id="sourceRegistryBody"/);
  assert.match(html, /id="sourceRegistryRunStatus"/);
  assert.match(html, /id="sourceRegistryYAML"/);
  assert.match(html, /id="sourceRegistryStagingGraphDomain"/);
  assert.match(html, /id="sourceRegistryStagingGraphEntityType"/);
  assert.match(html, /id="sourceRegistryStagingGraphEntityID"/);
  assert.match(html, /id="sourceRegistryStagingGraphRelation"/);
  assert.match(html, /id="sourceRegistryStagingGraphConfidence"/);
  assert.match(html, /id="domainGraphAssertionBody"/);
  assert.match(html, /id="domainGraphAssertionStatus"/);
  assert.match(html, /id="domainGraphRefreshBtn"/);
  assert.match(html, /id="memoryBody"/);
  assert.match(html, /id="newsPackBody"/);
  assert.match(html, /id="llmMemoryCards"/);
  assert.match(html, /id="llmMemorySystemBar"/);
  assert.match(html, /id="llmMemoryProcessLists"/);
  assert.match(html, /id="llmMemoryRoles"/);
  assert.match(html, /id="llmRuntimeConfigCards"/);
  assert.match(html, /id="runtimeReadinessCards"/);
  assert.match(html, /id="llmOpsConfigState"/);
  assert.match(html, /id="toolHarnessBody"/);
  assert.match(html, /id="dciTraceBody"/);
  assert.match(html, /id="sandboxGateLogBody"/);
  assert.match(html, /id="sandboxGateLogResult"/);
  assert.match(html, /id="dciSearchInput"/);
  assert.match(html, /id="dciSearchBtn"/);
  assert.match(html, /id="dciSearchResult"/);
  assert.match(html, /id="sandboxBody"/);
  assert.match(html, /data-tab="news-pack"/);
  assert.match(html, /id="panel-news-pack"/);
  assert.match(html, /id="newsPackDetail"/);
  assert.match(html, /id="newsUsageBody"/);
  assert.match(html, /id="recallTraceBody"/);
  assert.match(html, /<th>Status<\/th>/);
  assert.match(html, /<th>Section<\/th>/);
  assert.match(html, /<th>Tokens<\/th>/);
  assert.match(html, /<th>Warning<\/th>/);
  assert.match(html, /<th>Reason<\/th>/);
  assert.match(memoryJs, /item\.Status/);
  assert.match(memoryJs, /item\.PromptSection/);
  assert.match(memoryJs, /item\.Reason/);
  assert.match(memoryJs, /function refreshMemorySnapshot/);
  assert.match(memoryJs, /function refreshKnowledgeMemoryLedger/);
  assert.match(memoryJs, /function fetchMemoryKnowledgeDetail/);
  assert.match(memoryJs, /function relatedSourceRegistryStagingItems/);
  assert.match(memoryJs, /function relatedKnowledgeMemoryRows/);
  assert.match(html, /Related Staging/);
  assert.match(html, /<th>Knowledge<\/th>/);
  assert.match(newsPackJs, /function refreshNewsPack/);
  assert.match(newsPackJs, /function renderNewsPackPanel/);
  assert.match(newsPackJs, /function newsUsageCount/);
  assert.match(newsPackJs, /function newsRelatedMemoryMatches/);
  assert.match(html, /id="newsRelatedMemoryBody"/);
  assert.match(memoryJs, /function refreshMemoryLayers/);
  assert.match(memoryJs, /function refreshMemoryEvents/);
  assert.match(memoryJs, /function refreshMemoryRecallPack/);
  assert.match(memoryJs, /function refreshUserMemory/);
  assert.match(memoryJs, /function setUserMemoryState/);
  assert.match(memoryJs, /function forgetUserMemory/);
  assert.ok(viewer.includes('/viewer/memory/recall-pack'));
  assert.ok(viewer.includes('/viewer/memory/user'));
  assert.match(memoryJs, /function renderMemoryEvents/);
  assert.match(js, /function applyViewerTheme/);
  assert.match(js, /viewer\.theme/);
  assert.match(js, /Viewer state ownership:/);
  assert.match(js, /Do not use this object as the source of truth for transcript, TTS ACK, utterance consumption, or session progress\./);
  assert.match(js, /Browser playback state only\. Backend owns TTS completion\/ACK truth/);
  assert.match(js, /Diagnostic render trace only\. Never drive transcript, pending TTS, ACK, or session progression from this log\./);
  assert.match(idleChatJs, /Diagnostic write-only trace for tests\/debugging/);
  assert.doesNotMatch(js + '\n' + idleChatJs, /idleLiveRenderedLog\.(some|find|filter|map|forEach|reduce|entries|values)/);
  assert.match(js, /completedResponses \/ responsePlaybackCounts \/ responsePlaybackResults \/ seenAudioResponses form one response-level ACK lifecycle/);
  assert.match(js, /seenUtterances and blockedAckKeys are chunk-level local dedupe guards/);
  assert.match(js, /function clearResponsePlaybackLifecycle\(responseId\)/);
  assert.match(js, /function eventStructuredIDParts\(ev\)/);
  assert.match(js, /structuredIDs\.messageId/);
  assert.match(js, /rencrow\.viewer_tab_client_id/);
  assert.match(js, /sessionStorage\.setItem\(tabKey, id\)/);
  assert.match(js, /function switchAdjacentPanel/);
  assert.match(js, /mobilePanelSelect\.addEventListener\('change'/);
  assert.match(idleChatJs, /function isViewerLiveMode\(\) \{/);
  assert.match(idleChatJs, /function idleLiveRenderTarget\(\) \{/);
  assert.match(idleChatJs, /live mode is the theater view and uses the central chat stream/);
  assert.match(idleChatJs, /if \(isViewerLiveMode\(\) && chat\) return chat/);
  assert.match(idleChatJs, /const target = idleLiveRenderTarget\(\);[\s\S]*target\.appendChild\(el\);/);
  assert.match(idleChatJs, /function consumeIdlePendingMessage\(sessionId, characterId, kind, messageId, turnIndex\)/);
  assert.match(idleChatJs, /expectedKind === 'topic'[\s\S]*isIdleTopicEvent\(item\.ev\)/);
  assert.doesNotMatch(idleChatJs, /expectedKind === 'speech'[\s\S]*!isIdleTopicEvent\(item\.ev\)/);
  assert.doesNotMatch(idleChatJs, /queue\.findIndex\(\(item\) => !item\.consumed && \(!id \|\| item\.from === id\)\)/);
  assert.match(idleChatJs, /function renderIdleTTSChunkError\(chunk, errorCode, reason\)/);
  assert.match(idleChatJs, /function idleTranscriptSnapshotKey\(sessionId, rows\)/);
  assert.doesNotMatch(idleChatJs, /sid \+ ':' \+ String\(rows\.length\)/);
  assert.match(js, /const target = typeof idleLiveRenderTarget === 'function' \? idleLiveRenderTarget\(\) : idleLiveLog;/);
  assert.match(js, /consumeIdlePendingMessage\(sid, id, bubbleKind, messageId, turnIndex\)/);
  assert.match(js, /\^\(今日のお題\|きょうのおだい\)\(です\)\?\[、。:：！？!\?\]\?/);
  assert.match(css, /body\.live-mode header\{display:none\}/);
  assert.match(css, /body\.live-mode \.mobile-panel-switch\{display:none !important\}/);
  assert.match(css, /body\.live-mode \.live-audio-btn\{[\s\S]*z-index:80/);
  assert.match(css, /body\.live-mode \.lipsync-stage\{[\s\S]*background:transparent;border:0;box-shadow:none;backdrop-filter:none;pointer-events:none/);
  assert.match(css, /body\.live-mode \.chat-character-pane,\s*body\.live-mode \.chat-desk-head\{\s*display:none !important;/);
  assert.match(css, /body\.live-mode \.chat-main-pane\{[\s\S]*align-items:flex-start;[\s\S]*justify-content:center;/);
  assert.match(css, /body\.live-mode \.chat-main-pane\{[\s\S]*backdrop-filter:none/);
  assert.match(css, /body\.live-mode \.chat-empty > \*\{display:none !important\}/);
  assert.match(css, /body\.live-mode #chat\{[\s\S]*max-width:min\(1040px, calc\(100vw - 420px\)\)/);
  assert.match(css, /body\.live-mode #chat\.chat-conversation\{[\s\S]*max-height:calc\(100dvh - 156px\);[\s\S]*overflow-y:auto/);
  assert.match(css, /body\.live-mode #chat\.chat-conversation\{[\s\S]*overscroll-behavior:contain/);
  assert.match(css, /body\.live-mode #chat\.chat-conversation\{[\s\S]*-webkit-overflow-scrolling:touch/);
  assert.match(css, /\.audio-btn\{[\s\S]*touch-action:manipulation/);
  assert.match(css, /\.chat-desk-panel\{[\s\S]*height:calc\(100dvh - var\(--header-h\) - var\(--input-h\) - 54px - var\(--safe-bottom\)\)/);
  assert.match(css, /\.chat-desk-shell\{[\s\S]*align-items:start/);
  assert.match(css, /\.chat-desk-shell\{[\s\S]*min-height:0;height:100%/);
  assert.match(css, /\.chat-character-pane\{[\s\S]*--chat-character-w:100%/);
  assert.match(css, /\.chat-character-pane\{[\s\S]*--chat-character-h:clamp\(340px, calc\(100dvh - var\(--header-h\) - var\(--input-h\) - 112px - var\(--safe-bottom\)\), 620px\)/);
  assert.match(css, /\.chat-character-pane\{[\s\S]*grid-template-rows:auto minmax\(0,var\(--chat-character-h\)\)/);
  assert.match(css, /\.chat-character-pane\{[\s\S]*align-self:start/);
  assert.match(css, /\.chat-character-pane\{[\s\S]*height:fit-content/);
  assert.match(css, /\.chat-character-pane\{[\s\S]*max-height:100%/);
  assert.match(css, /\.chat-character-pane\{[\s\S]*position:sticky;top:12px/);
  assert.match(css, /\.chat-character-pane\{[\s\S]*border:0;border-radius:0;background:transparent;box-shadow:none;backdrop-filter:none/);
  assert.match(css, /\.chat-character-portrait\{[\s\S]*justify-self:center;width:var\(--chat-character-w\)/);
  assert.match(css, /\.chat-character-portrait\{[\s\S]*height:var\(--chat-character-h\)/);
  assert.match(css, /\.chat-character-portrait\{[\s\S]*max-height:var\(--chat-character-h\)/);
  assert.match(css, /\.chat-character-portrait\{[\s\S]*border-radius:0;padding:0;overflow:hidden;[\s\S]*background:transparent/);
  assert.match(css, /\.chat-character-portrait #chatLive2DMio\{[\s\S]*height:100% !important;[\s\S]*overflow:hidden !important/);
  assert.match(css, /\.chat-main-pane\{[\s\S]*align-self:stretch;height:100%;min-height:0/);
  assert.match(css, /#chat\.chat-conversation\{[\s\S]*min-height:0;max-height:100%/);
  assert.match(html, /assets\/css\/tabs\/ops\.css/);
  assert.match(html, /assets\/js\/tabs\/ops\.js/);
  assert.match(html, /assets\/js\/tabs\/memory\.js/);
  assert.match(html, /assets\/js\/tabs\/news-pack\.js/);
  assert.match(html, /assets\/js\/tabs\/roles\.js/);
  assert.match(html, /assets\/js\/tabs\/timeline\.js/);
  assert.match(html, /assets\/js\/tabs\/idlechat\.js/);
  assert.match(timelineJs, /function addMsgToTimeline/);
  assert.match(timelineJs, /function applyChatRouteAliasToMessage/);
  assert.match(timelineJs, /function buildViewerSendRequest/);
  assert.match(timelineJs, /function ensureViewerLLMReadyForRequest/);
  assert.match(timelineJs, /function viewerLLMStopRolesBeforeStart/);
  assert.match(timelineJs, /\/viewer\/llm-ops\/health/);
  assert.match(timelineJs, /\/viewer\/llm-ops\/stop/);
  assert.match(timelineJs, /\/viewer\/llm-ops\/start/);
  assert.match(timelineJs, /worker: \{label: 'Worker', baseURL: 'http:\/\/127\.0\.0\.1:8082', model: 'Worker', routePrefix: '\/ops'\}/);
  assert.match(timelineJs, /heavy: \{label: 'Heavy', baseURL: 'http:\/\/127\.0\.0\.1:8083', model: 'Heavy', routePrefix: '\/analyze'\}/);
  assert.match(timelineJs, /wild: \{label: 'Wild', baseURL: 'http:\/\/127\.0\.0\.1:8084', model: 'Wild', routePrefix: '\/wild'\}/);
  assert.match(timelineJs, /function syncChatRouteAliasesFromRuntimeConfig/);
  assert.match(js, /const body = buildViewerSendRequest\(message\)/);
  assert.match(js, /await ensureViewerLLMReadyForRequest\(body\)/);
  assert.doesNotMatch(timelineJs, /function addIdleMsgToTimeline/);
  assert.match(idleChatJs, /function addIdleMsgToTimeline/);
  assert.match(idleChatJs, /function appendIdleLiveMessageEvent/);
  assert.doesNotMatch(idleChatJs, /function addMsgToTimeline/);
  assert.match(js, /function setTTSSpeechText/);
  assert.match(js, /function renderChatTTSSpeechText/);
  assert.match(js, /function renderIdleTTSSpeechText/);
  assert.match(js, /let viewerEventSource = null/);
  assert.match(js, /let viewerEventWatchdogTimer = null/);
  assert.match(js, /function scheduleViewerEventReconnect/);
  assert.match(js, /function ensureViewerEventWatchdog/);
  assert.match(js, /viewerEventSource && viewerEventSource\.readyState !== EventSource\.CLOSED/);
  assert.match(js, /setInterval\(\(\) => \{/);
  assert.match(js, /scheduleViewerEventReconnect\(\);/);
  assert.doesNotMatch(sourceBetween(js, 'es.onerror = () => {', '};\n}'), /es\.close\(\);\s*setTimeout\(connect, 3000\)/);
  assert.match(opsJs, /function renderLlmMemoryStatus/);
  assert.match(opsJs, /Available RAM/);
  assert.match(opsJs, /Swap Used/);
  assert.match(opsJs, /Memory Pressure/);
  assert.match(opsJs, /Compressed/);
  assert.match(opsJs, /File Cache/);
  assert.match(opsJs, /Wired/);
  assert.match(opsJs, /Top Memory Processes/);
  assert.match(opsJs, /Model Processes/);
  assert.match(opsJs, /Available for LLM/);
  assert.match(opsJs, /Used for LLM/);
  assert.match(opsJs, /Safe Available/);
  assert.match(opsJs, /Safety Margin/);
  assert.match(opsJs, /function memoryGiB/);
  assert.match(opsJs, /function renderMemoryProcessList/);
  assert.match(opsJs, /function renderLocalLLMRuntimeConfig/);
  assert.match(opsJs, /function renderToolHarnessEvents/);
  assert.match(opsJs, /function toolHarnessOpsCard/);
  assert.match(opsJs, /provider protocol recovery not verified/);
  assert.match(opsJs, /function renderDCITraces/);
  assert.match(opsJs, /function dciOpsCard/);
  assert.match(opsJs, /VectorDB\/Qdrant E2E not verified/);
  assert.match(opsJs, /function bindDCISearchControls/);
  assert.match(opsJs, /\/viewer\/dci\/search/);
  assert.match(opsJs, /function renderSandboxStatus/);
  assert.match(opsJs, /function renderSandboxGateLogs/);
  assert.match(opsJs, /function sandboxOpsCard/);
  assert.match(opsJs, /sandboxArtifacts/);
  assert.match(opsJs, /sandboxGateLogs/);
  assert.match(opsJs, /function previewSandboxPromotion/);
  assert.match(opsJs, /sandbox promotion diff preview/);
  assert.match(opsJs, /function sandboxDiffRiskFlags/);
  assert.match(opsJs, /risk flags/);
  assert.match(opsJs, /manual review/);
  assert.ok(viewer.includes('/viewer/sandbox/promotions/preview'));
  assert.match(opsJs, /function skillGovernanceOpsCard/);
  assert.match(opsJs, /skillManifests/);
  assert.match(opsJs, /skillExternalPRSubmitRecords/);
  assert.match(opsJs, /function renderSkillEvidenceAudits/);
  assert.match(opsJs, /function renderSuperAgentTerminalAudits/);
  assert.match(opsJs, /function renderSuperAgentResumeAudits/);
  assert.match(opsJs, /function renderAIWorkflowRunEvidence/);
  assert.match(opsJs, /scheduler normal completion not verified/);
  assert.ok(viewer.includes('superAgentTerminalAuditBody'));
  assert.ok(viewer.includes('SuperAgent Terminal Audits'));
  assert.ok(viewer.includes('superAgentResumeAuditBody'));
  assert.ok(viewer.includes('SuperAgent Resume Audits'));
  assert.match(opsJs, /external PR adapter/);
  assert.match(viewer, /skillExternalPRAdapterConfigured/);
  assert.match(opsJs, /coderTranscripts/);
  assert.match(opsJs, /skill_trigger_missed requires review/);
  assert.match(opsJs, /function workstreamOpsCard/);
  assert.match(opsJs, /blocked: no vault apply/);
  assert.match(opsJs, /function latestWorkstreamVaultUpdates/);
  assert.match(opsJs, /function renderWorkstreamVaultReviews/);
  assert.match(opsJs, /function workstreamVaultReviewSummary/);
  assert.match(opsJs, /approved not applied/);
  assert.match(opsJs, /function reviewWorkstreamVaultUpdate/);
  assert.match(opsJs, /function formatWorkstreamVaultPreview/);
  assert.match(opsJs, /preview side-by-side/);
  assert.match(opsJs, /function revenueOpsCard/);
  assert.match(opsJs, /external channel adapter/);
  assert.match(opsJs, /function latestPersonaMetaProfileUpdates/);
  assert.match(opsJs, /function renderPersonaMetaReviews/);
  assert.match(opsJs, /function reviewPersonaMetaUpdate/);
  assert.match(opsJs, /function complexityHotspotOpsCard/);
  assert.match(opsJs, /function renderComplexityReviewArtifacts/);
  assert.match(opsJs, /complexity review artifacts:/);
  assert.match(opsJs, /function superAgentOpsCard/);
  assert.match(opsJs, /function heavyWorkerRuntimeOpsCard/);
  assert.match(opsJs, /function knowledgeMemoryOpsCard/);
  assert.match(opsJs, /blocked: empty ledger/);
  assert.match(opsJs, /blocked: no memory promote verified/);
  assert.match(opsJs, /blocked: no trace candidates/);
  assert.match(opsJs, /blocked: no official API adoption/);
  assert.match(opsJs, /function fetchKnowledgeMemoryDetail/);
  assert.match(opsJs, /Knowledge Memory Detail/);
  assert.match(viewer, /refreshHeavyWorkerRuntimeDiagnostics/);
  assert.match(viewer, /\/viewer\/ai-workflow\/heavy-worker\/runtime-diagnostics/);
  assert.match(opsJs, /workstreamGoals/);
  assert.match(css, /html\{width:100%;max-width:100vw;overflow-x:hidden\}/);
  assert.match(css, /linear-gradient\(135deg,#050713/);
  assert.match(css, /body::after/);
  assert.match(css, /repeating-linear-gradient/);
  assert.match(css, /backdrop-filter:blur/);
  assert.match(css, /\.lipsync-stage\{/);
  assert.doesNotMatch(css, /\\u\{1f4ad\}/);
  assert.match(css, /\.thinking \.mc \.dots\{[^}]*animation:dotPulse/);
  assert.match(js, /function shouldRenderThinking/);
  assert.match(js, /gemma4: \{c:'#34d399', l:'Gemma4'/);
  assert.match(js, /gamma4: \{c:'#34d399', l:'Gemma4'/);
  assert.match(js, /id !== 'mio' && id !== 'chat'/);
  assert.doesNotMatch(js, /id !== 'gemma4'/);
  assert.doesNotMatch(js, /id !== 'gamma4'/);
  assert.match(js, /function renderThinkingDots/);
  assert.match(css, /main\{[^}]*max-width:100vw;[^}]*overflow-x:hidden/);
  assert.match(css, /\.panel\{[^}]*max-width:100%/);
  assert.match(opsCss, /#panel-ops,#panel-ops \*\{min-width:0\}/);
  assert.match(opsCss, /#panel-ops \.debug-table\{[^}]*display:block;[^}]*max-width:100%;[^}]*overflow-x:auto/);
  assert.match(opsCss, /\.llm-ops-raw\{[^}]*max-width:100%;[^}]*white-space:pre-wrap;word-break:break-word/);
  assert.match(opsCss, /#llmOpsPanel \.debug-actions\{display:grid;grid-template-columns:1fr;gap:6px\}/);
  assert.match(opsCss, /\.ops-grid,\.llm-memory-grid,\.llm-memory-process-grid,\.llm-runtime-grid\{grid-template-columns:minmax\(0,1fr\)\}/);
  assert.match(viewer, /id="opsTriageCards"/);
  assert.match(viewer, /id="opsSecondaryCards"/);
  assert.match(viewer, /id="opsDetailsRuntime"/);
  assert.match(viewer, /id="opsDetailsEvidence"/);
  assert.match(viewer, /id="opsDetailsTraces"/);
  assert.match(opsCss, /\.ops-triage-grid/);
  assert.match(opsCss, /\.ops-details/);
  assert.match(opsJs, /function refreshOpsTriageFromState/);
  assert.match(viewer, /\/v1\/chat\/completions/);
  assert.match(viewer, /llm_ops_configured/);
  assert.match(opsJs, /LLM Ops/);
  assert.match(opsJs, /blocked: /);
  assert.match(viewer, /LLM_OPS_TOKEN missing/);
  assert.match(viewer, /memory\.system/);
  assert.match(viewer, /memory\.llm_by_role/);
  assert.match(memoryJs, /function refreshSourceRegistry/);
  assert.match(memoryJs, /function runSourceRegistryEntry/);
  assert.match(memoryJs, /function renderSourceRegistryRunStatus/);
  assert.match(memoryJs, /function refreshSourceRegistryStaging/);
  assert.match(memoryJs, /function validateSourceRegistryStaging/);
  assert.match(memoryJs, /function promoteSourceRegistryStaging/);
  assert.match(memoryJs, /function renderDomainGraphAssertions/);
  assert.match(memoryJs, /function refreshDomainGraphAssertions/);
  assert.ok(viewer.includes('/viewer/domain-graph/assertions'));
  assert.match(html, /id="sourceRegistryStagingBody"/);
  assert.match(memoryJs, /sourceRegistryLastRun/);
  assert.match(memoryJs, /warnings=/);
  assert.match(memoryJs, /function refreshRecallTraces/);
  assert.match(html, /data-tab="roles"/);
  assert.match(html, /id="panel-roles"/);
  assert.match(html, /id="roleSelectorBody"/);
  assert.match(html, /id="roleFilter"/);
  assert.match(js, /const ROLE_TARGETS/);
  assert.match(rolesJs, /function renderRoleSelector/);
  assert.match(rolesJs, /function selectRoleTarget/);
  assert.match(rolesJs, /function applyRoleTargetToMessage/);
  assert.match(html, /Chat/);
  assert.match(html, /Worker/);
  assert.match(html, /Wild/);
  assert.ok(rolesJs.includes("if (viewerChatRecipientForTarget(selectedID)) return trimmed"));
  assert.ok(rolesJs.includes("return '/code1 ' + trimmed"));
  assert.ok(rolesJs.includes("return '/code2 ' + trimmed"));
  assert.ok(rolesJs.includes("return '/code3 ' + trimmed"));
  assert.ok(rolesJs.includes("return '/code4 ' + trimmed"));
  assert.ok(viewer.includes('/viewer/memory/snapshot'));
  assert.ok(viewer.includes('/viewer/memory/layers'));
  assert.ok(viewer.includes('/viewer/memory/events'));
  assert.ok(viewer.includes('/viewer/source-registry'));
  assert.ok(viewer.includes('/viewer/recall/traces'));
  assert.ok(viewer.includes('/viewer/tool-harness/recent'));
  assert.ok(viewer.includes('/viewer/dci/recent'));
  assert.ok(viewer.includes('/viewer/sandbox'));
  assert.ok(viewer.includes('/viewer/skill-governance/recent'));
  assert.ok(viewer.includes('Skill Governance External PR Submit Audits'));
  assert.ok(viewer.includes('skillExternalPRAuditBody'));
  assert.ok(viewer.includes('Skill Evidence Audits'));
  assert.ok(viewer.includes('skillEvidenceAuditBody'));
  assert.ok(viewer.includes('function renderSkillExternalPRAudits'));
  assert.ok(viewer.includes('missed triggers'));
  assert.ok(viewer.includes('coder_transcripts'));
  assert.ok(viewer.includes('/viewer/workstreams'));
  assert.ok(viewer.includes('workstreamGoals'));
  assert.ok(viewer.includes('workstreamArtifacts'));
  assert.ok(viewer.includes('workstreamSteering'));
  assert.ok(viewer.includes('workstreamHeartbeats'));
  assert.ok(viewer.includes('workstreamVaultUpdates'));
  assert.ok(viewer.includes('workstreamVaultReviewResult'));
  assert.ok(viewer.includes('workstreamVaultPreviewResult'));
  assert.ok(viewer.includes('/viewer/workstreams/vault-updates/review'));
  assert.ok(viewer.includes('/viewer/workstreams/vault-updates/preview'));
  assert.ok(viewer.includes('Workstream Vault Review'));
  assert.ok(viewer.includes('function previewWorkstreamVaultUpdate'));
  assert.ok(viewer.includes('/viewer/revenue'));
  assert.ok(viewer.includes('/viewer/revenue/human-decision-gate/review'));
  assert.ok(viewer.includes('revenueProducts'));
  assert.ok(viewer.includes('revenueSummary'));
  assert.ok(viewer.includes('kpi_trend'));
  assert.ok(viewer.includes('product_sales'));
  assert.ok(viewer.includes('customer_voice_types'));
  assert.ok(viewer.includes('revenueChannelDrafts'));
  assert.ok(viewer.includes('revenueExternalSendApplyRecords'));
  assert.ok(viewer.includes('revenueExternalChannelAdapterConfigured'));
  assert.ok(viewer.includes('external_send_apply_records'));
  assert.ok(viewer.includes('external apply audits'));
  assert.ok(viewer.includes('channel_drafts'));
  assert.ok(viewer.includes('channel drafts'));
  assert.ok(viewer.includes('Revenue Channel Drafts'));
  assert.ok(viewer.includes('revenueChannelDraftBody'));
  assert.ok(viewer.includes('function renderRevenueChannelDrafts'));
  assert.ok(viewer.includes('Revenue External Send Apply Audits'));
  assert.ok(viewer.includes('revenueExternalSendAuditBody'));
  assert.ok(viewer.includes('function renderRevenueExternalSendAudits'));
  assert.ok(viewer.includes('external_send_applied'));
  assert.ok(viewer.includes('draft only'));
  assert.ok(viewer.includes('Revenue Drilldown'));
  assert.ok(viewer.includes('revenueDrilldownResult'));
  assert.ok(viewer.includes('function renderRevenueDrilldown'));
  assert.ok(viewer.includes('function revenueDrilldownLines'));
  assert.ok(viewer.includes('KPI trend graph'));
  assert.ok(viewer.includes('Product sales graph'));
  assert.ok(viewer.includes('Customer voice graph'));
  assert.ok(viewer.includes('revenueHumanDecisions'));
  assert.ok(viewer.includes('revenueDecisionReviewResult'));
  assert.ok(viewer.includes('function reviewRevenueHumanDecision'));
  assert.ok(viewer.includes('paid events'));
  assert.ok(viewer.includes('human decisions pending'));
  assert.ok(viewer.includes('Revenue Human Decision Gate'));
  assert.ok(viewer.includes('/viewer/persona-observation'));
  assert.ok(viewer.includes('personaObservationLogs'));
  assert.ok(viewer.includes('personaMetaProfileUpdates'));
  assert.ok(viewer.includes('personaMetaReviewResult'));
  assert.ok(viewer.includes('/viewer/persona-observation/meta-updates/review'));
  assert.ok(viewer.includes('Persona Meta Review'));
  assert.ok(viewer.includes('Persona Observation'));
  assert.ok(viewer.includes('/viewer/browser-trace-api'));
  assert.ok(viewer.includes('/viewer/browser-trace-api/fetcher-proposals'));
  assert.ok(viewer.includes('browserTraceAPICandidates'));
  assert.ok(viewer.includes('browserTraceAPIArtifacts'));
  assert.ok(viewer.includes('Browser Trace API'));
  assert.ok(viewer.includes('fetcher proposals'));
  assert.ok(viewer.includes('/viewer/complexity-hotspots'));
  assert.ok(viewer.includes('complexityHotspots'));
  assert.ok(viewer.includes('complexityReports'));
  assert.ok(viewer.includes('Complexity Hotspots'));
  assert.ok(viewer.includes('complexityReviewArtifactBody'));
  assert.ok(viewer.includes('Complexity Review Artifacts'));
  assert.ok(opsJs.includes('blocked: no patch applied'));
  assert.ok(viewer.includes('Runtime Blocked Route Audits'));
  assert.ok(viewer.includes('runtimeBlockedRouteAuditBody'));
  assert.ok(viewer.includes('runtimeBlockedRoutes'));
  assert.ok(viewer.includes('function renderRuntimeBlockedRouteAudits'));
  assert.ok(viewer.includes('function refreshRuntimeBlockedRouteData'));
  assert.ok(viewer.includes('/viewer/ai-workflow'));
  assert.ok(viewer.includes('aiWorkflowEvents'));
  assert.ok(viewer.includes('aiWorkflowContextBudgetPolicy'));
  assert.ok(viewer.includes('AI Workflow'));
  assert.ok(viewer.includes('aiWorkflowRunEvidenceBody'));
  assert.ok(viewer.includes('AI Workflow Run Evidence'));
  assert.ok(opsJs.includes('context-budget:disabled'));
  assert.ok(viewer.includes('/viewer/superagent'));
  assert.ok(viewer.includes('superAgentRuns'));
  assert.ok(viewer.includes('superAgentRunQueue'));
  assert.ok(viewer.includes('superAgentRuntimeConfig'));
  assert.ok(viewer.includes('SuperAgent Harness'));
  assert.ok(opsJs.includes('scheduler:disabled'));
  assert.ok(opsJs.includes('run queue'));
  assert.ok(viewer.includes('/viewer/knowledge-memory'));
  assert.ok(viewer.includes('/viewer/knowledge-memory/review'));
  assert.ok(viewer.includes('detail_type'));
  assert.ok(viewer.includes('reviewKnowledgeMemoryItem'));
  assert.ok(viewer.includes('Review / Promote Comparison'));
  assert.ok(viewer.includes('Review Result'));
  assert.ok(viewer.includes('knowledgePersonalArchive'));
  assert.ok(viewer.includes('Knowledge Memory'));
  assert.ok(viewer.includes('vault updates'));
  assert.ok(viewer.includes('approval pending'));
  assert.ok(viewer.includes('artifacts'));
  assert.ok(viewer.includes('gate_logs'));
  assert.ok(viewer.includes('/viewer/memory/state'));
  assert.ok(viewer.includes('/viewer/memory/promote'));
  assert.ok(viewer.includes('target_kind'));
  assert.ok(viewer.includes('target_id'));
});

test('viewer renders memory layers fetch errors as visible state', async () => {
  const memoryJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/memory.js', 'utf8');
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new FakeElement(id));
    return elements.get(id);
  };
  const document = {
    getElementById: get,
    createElement() {
      return new FakeElement();
    },
  };
  get('memorySession').value = 'session-1';
  get('memoryNamespace').value = 'kb:e2e';
  get('memoryDomain').value = 'movie';
  const requested = [];
  const source = `
function esc(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function fdt(s) { return String(s || ''); }
const state = {memory: {layers: {l0: [{ID: 'old', Message: 'old memory', Layer: 'L0', CreatedAt: '2026-05-20T09:20:00Z'}]}}};
const memorySession = document.getElementById('memorySession');
const memoryNamespace = document.getElementById('memoryNamespace');
const memoryDomain = document.getElementById('memoryDomain');
` + sourceBetween(memoryJs, 'function renderMemoryLayers', 'function memoryEventNamespaceValue') + `
globalThis.__refresh = refreshMemoryLayers;
globalThis.__state = state;
`;
  const context = vm.createContext({
    document,
    console: {error() {}},
    URLSearchParams,
    fetch(url) {
      requested.push(url);
      return Promise.resolve({
        ok: false,
        status: 500,
        text: () => Promise.resolve('invalid memory layers snapshot: l2 summary missing summary'),
      });
    },
  });
  vm.runInContext(source, context);
  context.__refresh();
  await new Promise((resolve) => setImmediate(resolve));

  assert.match(requested[0], /session_id=session-1/);
  assert.equal(get('memoryL0Count').textContent, '0');
  assert.match(get('memoryLayerBody').innerHTML, /Memory Layers unavailable/);
  assert.match(get('memoryLayerBody').innerHTML, /invalid memory layers snapshot: l2 summary missing summary/);
  assert.equal(context.__state.memory.layers.l0.length, 0);
});

test('viewer renders news pack fetch errors as visible state', async () => {
  const newsPackJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/news-pack.js', 'utf8');
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new FakeElement(id));
    return elements.get(id);
  };
  const document = {
    getElementById: get,
    createElement() {
      return new FakeElement();
    },
  };
  get('newsPackCategory').value = 'tech';
  get('memoryCategory').value = '';
  const requested = [];
  const source = `
function esc(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function fdt(s) { return String(s || ''); }
function renderMemorySnapshot() {}
function refreshRecallTraces() {}
const state = {memory: {
  snapshot: {
    news: [{SourceID: 'stale_news', SummaryDraft: 'stale headline', Category: 'tech'}],
    digests: [{DigestText: 'stale digest', Category: 'tech'}],
  },
  traces: [],
  selectedNewsIndex: 0,
  newsPackFetchError: '',
}};
const newsPackCategory = document.getElementById('newsPackCategory');
const memoryCategory = document.getElementById('memoryCategory');
` + newsPackJs + `
globalThis.__refresh = refreshNewsPack;
globalThis.__state = state;
`;
  const context = vm.createContext({
    document,
    console: {error() {}},
    URLSearchParams,
    fetch(url) {
      requested.push(url);
      return Promise.resolve({
        ok: false,
        status: 500,
        text: () => Promise.resolve('invalid news snapshot: digest missing created_at'),
      });
    },
  });
  vm.runInContext(source, context);
  context.__refresh();
  await new Promise((resolve) => setImmediate(resolve));

  assert.match(requested[0], /category=tech/);
  assert.equal(get('memoryCategory').value, 'tech');
  assert.equal(get('newsPackPanelCount').textContent, '0');
  assert.equal(get('newsDigestPanelCount').textContent, '0');
  assert.equal(get('newsUsageCount').textContent, '0');
  assert.match(get('newsPackPanelBody').innerHTML, /News Pack unavailable: HTTP 500: invalid news snapshot: digest missing created_at/);
  assert.match(get('newsDigestPanelBody').innerHTML, /News digests unavailable: HTTP 500: invalid news snapshot: digest missing created_at/);
  assert.match(get('newsPackDetail').innerHTML, /News Pack unavailable: HTTP 500: invalid news snapshot: digest missing created_at/);
  assert.doesNotMatch(get('newsPackPanelBody').innerHTML, /stale headline/);
  assert.doesNotMatch(get('newsDigestPanelBody').innerHTML, /stale digest/);
  assert.equal(context.__state.memory.snapshot.news.length, 0);
  assert.equal(context.__state.memory.snapshot.digests.length, 0);
});

test('viewer renders complexity review-only blocked state in ops card', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const source = `
function short(s) { return String(s || ''); }
const state = {ops: {
  complexityScans: [{scan_id: 'scan_1', status: 'completed'}],
  complexityHotspots: [{hotspot_id: 'hot_1', scan_id: 'scan_1', hotspot_type: 'nested_loop', risk_level: 'medium'}],
  complexityEvidence: [{evidence_id: 'ev_1', hotspot_id: 'hot_1'}],
  complexityReports: [
    {artifact_id: 'art_report', artifact_type: 'complexity_hotspot_report', status: 'generated'},
    {artifact_id: 'art_diff', artifact_type: 'complexity_concrete_diff_proposal', status: 'pending_review'},
  ],
}};
` + sourceBetween(opsJs, 'function sandboxField', 'function superAgentOpsCard') + `
globalThis.__card = complexityHotspotOpsCard();
`;
  const context = vm.createContext({});
  vm.runInContext(source, context);

  assert.equal(context.__card.title, 'Complexity Hotspots');
  assert.match(context.__card.sub, /reports: 2 pending-review: 1/);
  assert.match(context.__card.sub, /mode: review-only/);
  assert.match(context.__card.sub, /blocked: no patch applied/);
});

test('viewer renders complexity review artifact patch boundary', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function short(s) { return String(s || ''); }
function ftime(s) { return String(s || '-'); }
function stateClass(s) { return String(s || ''); }
function sandboxField(obj, snake, pascal) {
  if (!obj) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, snake)) return obj[snake];
  if (Object.prototype.hasOwnProperty.call(obj, pascal)) return obj[pascal];
  return undefined;
}
const state = {ops: {
  complexityReports: [{
    artifact_id: 'art_diff_1',
    artifact_type: 'complexity_concrete_diff_proposal',
    status: 'pending_review',
    content: 'Patch applied: false\\nHuman approval required: true',
  }],
}};
` + sourceBetween(opsJs, 'function renderComplexityReviewArtifacts', 'function workstreamOpsCard') + `
renderComplexityReviewArtifacts();
globalThis.__complexityArtifacts = document.getElementById('complexityReviewArtifactBody').innerHTML;
globalThis.__complexityArtifactResult = document.getElementById('complexityReviewArtifactResult').textContent;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.match(context.__complexityArtifacts, /art_diff_1/);
  assert.match(context.__complexityArtifacts, /complexity_concrete_diff_proposal/);
  assert.match(context.__complexityArtifacts, /pending_review/);
  assert.match(context.__complexityArtifacts, /not applied/);
  assert.match(context.__complexityArtifacts, /required/);
  assert.match(context.__complexityArtifactResult, /1 total \/ 1 pending-review \/ 0 failed \/ 0 patch applied \/ 1 human approval required/);
  assert.match(context.__complexityArtifactResult, /mode: review-only blocked: no patch applied/);
});

test('viewer renders complexity coder diff failure audit boundary', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function short(s) { return String(s || ''); }
function ftime(s) { return String(s || '-'); }
function stateClass(s) { return String(s || ''); }
function sandboxField(obj, snake, pascal) {
  if (!obj) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, snake)) return obj[snake];
  if (Object.prototype.hasOwnProperty.call(obj, pascal)) return obj[pascal];
  return undefined;
}
const state = {ops: {
  complexityReports: [{
    artifact_id: 'art_fail_1',
    artifact_type: 'complexity_coder_diff_failure',
    status: 'failed',
    content: 'Failure reason: timeout\\nPatch applied: false\\nHuman approval required: true',
  }],
}};
` + sourceBetween(opsJs, 'function renderComplexityReviewArtifacts', 'function workstreamOpsCard') + `
renderComplexityReviewArtifacts();
globalThis.__complexityArtifacts = document.getElementById('complexityReviewArtifactBody').innerHTML;
globalThis.__complexityArtifactResult = document.getElementById('complexityReviewArtifactResult').textContent;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.match(context.__complexityArtifacts, /art_fail_1/);
  assert.match(context.__complexityArtifacts, /complexity_coder_diff_failure/);
  assert.match(context.__complexityArtifacts, /failed/);
  assert.match(context.__complexityArtifacts, /not applied/);
  assert.match(context.__complexityArtifactResult, /1 total \/ 0 pending-review \/ 1 failed \/ 0 patch applied \/ 1 human approval required/);
  assert.match(context.__complexityArtifactResult, /mode: review-only blocked: no patch applied/);
});

test('viewer renders complexity fetch errors as visible patch apply state', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function short(s) { return String(s || ''); }
function ftime(s) { return String(s || '-'); }
function stateClass(s) { return String(s || ''); }
function sandboxField(obj, snake, pascal) {
  if (!obj) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, snake)) return obj[snake];
  if (Object.prototype.hasOwnProperty.call(obj, pascal)) return obj[pascal];
  return undefined;
}
const state = {ops: {
  complexityFetchError: 'HTTP 500: complexity store unavailable',
  complexityScans: [{scan_id: 'stale_scan'}],
  complexityHotspots: [{hotspot_id: 'stale_hotspot'}],
  complexityEvidence: [],
  complexityReports: [{
    artifact_id: 'stale_patch',
    artifact_type: 'complexity_concrete_diff_proposal',
    status: 'pending_review',
    content: 'Patch applied: true\\nHuman approval required: false',
  }],
}};
` + sourceBetween(opsJs, 'function renderComplexityReviewArtifacts', 'function workstreamOpsCard') + `
` + sourceBetween(opsJs, 'function complexityHotspotOpsCard', 'function superAgentOpsCard') + `
globalThis.__card = complexityHotspotOpsCard();
renderComplexityReviewArtifacts();
globalThis.__complexityArtifacts = document.getElementById('complexityReviewArtifactBody').innerHTML;
globalThis.__complexityArtifactResult = document.getElementById('complexityReviewArtifactResult').textContent;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.equal(context.__card.big, 'unavailable');
  assert.match(context.__card.sub, /complexity hotspot status unavailable: HTTP 500: complexity store unavailable/);
  assert.match(context.__card.sub, /blocked: patch apply state unreadable/);
  assert.match(context.__complexityArtifacts, /Complexity review artifacts unavailable: HTTP 500: complexity store unavailable/);
  assert.doesNotMatch(context.__complexityArtifacts, /stale_patch/);
  assert.match(context.__complexityArtifactResult, /complexity review artifacts unavailable: HTTP 500: complexity store unavailable/);
  assert.match(context.__complexityArtifactResult, /blocked: patch apply state unreadable/);
});

test('viewer renders browser trace api fetch errors as visible official adoption state', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const source = `
function short(s) { return String(s || ''); }
function sandboxField(obj, snake, pascal) {
  if (!obj) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, snake)) return obj[snake];
  if (Object.prototype.hasOwnProperty.call(obj, pascal)) return obj[pascal];
  return undefined;
}
const state = {ops: {
  browserTraceAPIFetchError: 'HTTP 500: browser trace store unavailable',
  browserTraceRuns: [{trace_run_id: 'stale_trace'}],
  browserTraceAPICandidates: [{candidate_id: 'stale_candidate', path_template: '/stale'}],
  browserTraceAPISchemas: [{schema_id: 'stale_schema'}],
  browserTraceAPICoverageReports: [],
  browserTraceAPIArtifacts: [{artifact_id: 'stale_fetcher', artifact_type: 'fetcher_proposal'}],
}};
` + sourceBetween(opsJs, 'function browserTraceAPIOpsCard', 'async function requestBrowserTraceAPIFetcherProposal') + `
globalThis.__card = browserTraceAPIOpsCard();
`;
  const context = vm.createContext({});
  vm.runInContext(source, context);

  assert.equal(context.__card.title, 'Browser Trace API');
  assert.equal(context.__card.big, 'unavailable');
  assert.match(context.__card.sub, /browser trace api status unavailable: HTTP 500: browser trace store unavailable/);
  assert.match(context.__card.sub, /blocked: official API adoption state unreadable/);
  assert.match(context.__card.sub, /blocked: fetcher implementation state unreadable/);
  assert.doesNotMatch(context.__card.sub, /stale_trace/);
  assert.doesNotMatch(context.__card.sub, /fetcher proposals: 1/);
});

test('viewer renders knowledge memory fetch errors as visible promote sync state', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const source = `
function sandboxField(obj, snake, pascal) {
  if (!obj) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, snake)) return obj[snake];
  if (Object.prototype.hasOwnProperty.call(obj, pascal)) return obj[pascal];
  return undefined;
}
const state = {ops: {
  knowledgeMemoryFetchError: 'HTTP 500: knowledge memory store unavailable',
  knowledgePersonalArchive: [],
  knowledgeCreativeItems: [],
  knowledgeNewsItems: [{news_id: 'stale_news', title: 'stale promoted news', status: 'promoted'}],
  knowledgeDailyIntakeRules: [{rule_id: 'stale_rule', status: 'enabled'}],
  knowledgeTemporalMarkers: [],
  knowledgeDreamRuns: [],
}};
` + sourceBetween(opsJs, 'function knowledgeMemoryOpsCard', 'function runtimeBlockedRoutesOpsCard') + `
globalThis.__card = knowledgeMemoryOpsCard();
`;
  const context = vm.createContext({});
  vm.runInContext(source, context);

  assert.equal(context.__card.title, 'Knowledge Memory');
  assert.equal(context.__card.big, 'unavailable');
  assert.match(context.__card.sub, /knowledge memory status unavailable: HTTP 500: knowledge memory store unavailable/);
  assert.match(context.__card.sub, /blocked: memory promote state unreadable/);
  assert.match(context.__card.sub, /blocked: source registry sync state unreadable/);
  assert.doesNotMatch(context.__card.sub, /stale promoted news/);
  assert.doesNotMatch(context.__card.sub, /daily intake: 1/);
});

test('viewer renders superagent scheduler blocked state in ops card', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const source = `
function short(s) { return String(s || ''); }
const state = {ops: {
  superAgentRuns: [],
  superAgentSubagentTasks: [],
  superAgentContextPacks: [],
  superAgentMessageChannels: [],
  superAgentTraceEvents: [],
  superAgentRunQueue: [{queue_id: 'rq_1', status: 'queued'}],
  superAgentRuntimeConfig: {
    run_queue_scheduler_enabled: false,
    run_queue_scheduler_interval_sec: 60,
    run_queue_scheduler_claim_limit: 1,
  },
}};
` + sourceBetween(opsJs, 'function sandboxField', 'function heavyWorkerRuntimeOpsCard') + `
globalThis.__card = superAgentOpsCard();
`;
  const context = vm.createContext({});
  vm.runInContext(source, context);

  assert.equal(context.__card.title, 'SuperAgent Harness');
  assert.match(context.__card.sub, /run queue: 1 queued: 1/);
  assert.match(context.__card.sub, /scheduler:disabled/);
  assert.match(context.__card.sub, /blocked: scheduler disabled/);
});

test('viewer renders ai workflow context budget blocked state in ops card', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const source = `
function short(s) { return String(s || ''); }
const state = {ops: {
  aiWorkflowEvents: [{event_id: 'evt_1', event_type: 'command_invoked', status: 'requested'}],
  aiWorkflowProjectMemoryIndexes: [],
  aiWorkflowWorktreeRegistries: [],
  aiWorkflowCommandRegistries: [{command_name: '/tool-harness-check'}],
  aiWorkflowContextUsages: [{event_id: 'ctx_1', agent: 'chat', context_tokens: 100}],
  aiWorkflowContextBudgetPolicy: {
    max_context_tokens: 0,
    warn_at_ratio: 0.8,
    stop_at_ratio: 0.95,
  },
}};
` + sourceBetween(opsJs, 'function sandboxField', 'function heavyWorkerRuntimeOpsCard') + `
globalThis.__card = aiWorkflowOpsCard();
`;
  const context = vm.createContext({});
  vm.runInContext(source, context);

  assert.equal(context.__card.title, 'AI Workflow');
  assert.match(context.__card.sub, /commands: 1/);
  assert.match(context.__card.sub, /context usage: 1/);
  assert.match(context.__card.sub, /context-budget:disabled/);
  assert.match(context.__card.sub, /blocked: context budget disabled/);
});

test('viewer renders workstream review-only blocked state in ops card', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const source = `
function short(s) { return String(s || ''); }
function latestWorkstreamVaultUpdates(items) { return items || []; }
const state = {ops: {
  workstreams: [],
  workstreamGoals: [{goal_id: 'goal_1', workstream_id: 'ws_1', title: 'Review diff', status: 'waiting'}],
  workstreamArtifacts: [{artifact_id: 'art_1', workstream_id: 'ws_1', artifact_type: 'complexity_concrete_diff_review', status: 'pending_review'}],
  workstreamAnnotations: [],
  workstreamSteering: [],
  workstreamHeartbeats: [],
  workstreamVaultUpdates: [],
}};
` + sourceBetween(opsJs, 'function sandboxField', 'function skillGovernanceOpsCard') +
sourceBetween(opsJs, 'function workstreamOpsCard', 'function latestWorkstreamVaultUpdates') + `
globalThis.__card = workstreamOpsCard();
`;
  const context = vm.createContext({});
  vm.runInContext(source, context);

  assert.equal(context.__card.title, 'Workstreams');
  assert.match(context.__card.sub, /waiting goals: 1/);
  assert.match(context.__card.sub, /pending-review: 1/);
  assert.match(context.__card.sub, /mode: review-only/);
  assert.match(context.__card.sub, /blocked: no vault apply/);
});

test('viewer renders workstream vault review applied boundary', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
const state = {ops: {
  workstreamVaultUpdates: [
    {update_id: 'vu_1', file_path: 'vault/a.md', review_status: 'pending', proposed_content: 'draft text', applied: false},
  ],
}};
function escAttr(s) { return String(s || ''); }
function ftime(s) { return String(s || ''); }
function esc(s) { return String(s || ''); }
function short(s) { return String(s || ''); }
function stateClass(s) { return 'state-' + String(s || ''); }
` +
sourceBetween(opsJs, 'function sandboxField', 'function skillGovernanceOpsCard') +
sourceBetween(opsJs, 'function latestWorkstreamVaultUpdates', 'function formatWorkstreamVaultPreview') + `
globalThis.__summary = workstreamVaultReviewSummary();
renderWorkstreamVaultReviews();
globalThis.__body = document.getElementById('workstreamVaultReviewBody').innerHTML;
globalThis.__result = document.getElementById('workstreamVaultReviewResult').textContent;
`;
  const context = vm.createContext({document, encodeURIComponent, JSON});
  vm.runInContext(source, context);

  assert.match(context.__summary, /1 total \/ 1 pending \/ 0 approved \/ 0 rejected \/ 0 applied/);
  assert.match(context.__summary, /blocked: no vault apply/);
  assert.match(context.__body, /vu_1/);
  assert.match(context.__body, /not applied/);
  assert.match(context.__body, /vault\/a\.md/);
  assert.match(context.__result, /workstream vault review:/);
  assert.match(context.__result, /blocked: no vault apply/);
});

test('viewer renders workstream fetch errors as visible vault apply state', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
const state = {ops: {
  workstreamFetchError: 'HTTP 500: workstream store unavailable',
  workstreams: [{name: 'stale ws'}],
  workstreamGoals: [],
  workstreamArtifacts: [],
  workstreamAnnotations: [],
  workstreamSteering: [],
  workstreamHeartbeats: [],
  workstreamVaultUpdates: [
    {update_id: 'stale_vault', file_path: 'vault/stale.md', review_status: 'approved', proposed_content: 'old', applied: true, applied_path: 'vault/stale.md'},
  ],
}};
function escAttr(s) { return String(s || ''); }
function ftime(s) { return String(s || ''); }
function esc(s) { return String(s || ''); }
function short(s) { return String(s || ''); }
function stateClass(s) { return 'state-' + String(s || ''); }
` +
sourceBetween(opsJs, 'function sandboxField', 'function skillGovernanceOpsCard') +
sourceBetween(opsJs, 'function workstreamOpsCard', 'function formatWorkstreamVaultPreview') + `
globalThis.__card = workstreamOpsCard();
renderWorkstreamVaultReviews();
globalThis.__body = document.getElementById('workstreamVaultReviewBody').innerHTML;
globalThis.__result = document.getElementById('workstreamVaultReviewResult').textContent;
`;
  const context = vm.createContext({document, encodeURIComponent, JSON});
  vm.runInContext(source, context);

  assert.equal(context.__card.big, 'unavailable');
  assert.match(context.__card.sub, /workstream status unavailable: HTTP 500: workstream store unavailable/);
  assert.match(context.__card.sub, /blocked: vault apply state unreadable/);
  assert.match(context.__body, /Workstream vault reviews unavailable: HTTP 500: workstream store unavailable/);
  assert.doesNotMatch(context.__body, /stale_vault/);
  assert.match(context.__result, /workstream vault review unavailable: HTTP 500: workstream store unavailable/);
  assert.match(context.__result, /blocked: vault apply state unreadable/);
});

test('viewer renders expanded llm ops memory fields', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function escAttr(s) { return String(s || ''); }
const state = {
  ops: {
    llmStatus: {
      memory: {
        system: {
          total_gib: 64,
          used_gib: 40,
          free_gib: 8,
          available_gib: 16,
          swap_used_gib: 0,
          memory_pressure: 'normal',
          compressed_gib: 0,
          file_cache_gib: 10,
          wired_gib: 7,
          available_for_llm_gib: 11.5,
          used_for_llm_gib: 116.5,
          safe_available_for_llm_gib: 3.5,
          llm_safety_margin_gib: 8,
        },
        llm_by_role: {
          Chat: {pid: 111, rss_mib: 2048},
          Worker: {pid: 222, rss_mib: 4096},
        },
        top_memory_processes: [{name: 'python', pid: 123, rss_mib: 1024}],
        model_processes: [{role: 'Chat', model: 'qwen', pid: 111, rss_mib: 2048}],
      },
      roles: {Chat: {health_ok: true}, Worker: {health_ok: true}},
    },
    localLLM: {},
    llmStatusError: '',
  },
};
` + sourceBetween(js, 'function normState', 'function fmt') +
sourceBetween(js, 'function stateClass', 'function bump') +
sourceBetween(opsJs, 'function llmRoleMemoryState', 'async function refreshLlmOpsStatus') + `
renderLlmMemoryStatus();
globalThis.__cards = document.getElementById('llmMemoryCards').innerHTML;
globalThis.__processes = document.getElementById('llmMemoryProcessLists').innerHTML;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  for (const label of ['Total RAM', 'Used RAM', 'Free RAM', 'Available RAM', 'Swap Used', 'Memory Pressure', 'Compressed', 'File Cache', 'Wired', 'Available for LLM', 'Used for LLM', 'Safe Available', 'Safety Margin']) {
    assert.ok(context.__cards.includes(label), `${label} should render`);
  }
  assert.ok(context.__cards.includes('0.00 GiB'), 'reported zero memory values should render as 0.00 GiB');
  assert.ok(context.__cards.includes('llm-memory-indicator state-running'), 'healthy memory metrics should render OK indicators');
  assert.ok(context.__cards.includes('OK'), 'healthy memory metrics should show OK status text');
  assert.ok(context.__processes.includes('Top Memory Processes'));
  assert.ok(context.__processes.includes('Model Processes'));
  assert.ok(context.__processes.includes('python'));
  assert.ok(context.__processes.includes('qwen'));
});

test('viewer renders knowledge memory ledger inside memory tab', () => {
  const memoryJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/memory.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || '').replace(/[&<>"']/g, (c) => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function fdt(s) { return String(s || '-'); }
const state = {memory: {knowledgeMemory: {
  personal_archive: [{entry_id: 'pa_1', title: 'BIO original', user_id: 'ren', source_ref: 'stg_personal', original_text: 'raw bio', compressed_summary: 'bio digest', protected: true, review_status: 'protected', security_warnings: ['prompt-like text']}],
  creative_knowledge: [{item_id: 'ck_1', title: '映画知識', source_id: 'src_movie', summary: 'movie digest'}],
  news_knowledge: [{item_id: 'news_1', topic: 'tech news', source_url: 'https://example.com/news', meta: {security_warnings: ['warn']}}],
  daily_intake_rules: [{rule_id: 'rule_1', title: 'daily tech', enabled: true}],
  temporal_markers: [{marker_id: 'tm_1', summary: 'one week memory'}],
  dream_runs: [{run_id: 'dream_1', topic: 'dream consolidation', review_status: 'pending'}],
}, sourceRegistryStaging: [
  {id: 'stg_personal', validation_status: 'pending'},
  {id: 'stg_movie', source_id: 'src_movie', validation_status: 'pending'},
], knowledgeMemoryDetail: {detail_type: 'personal_archive', id: 'pa_1', item: {entry_id: 'pa_1', source_ref: 'stg_personal', original_text: 'raw bio', compressed_summary: 'bio digest', protected: true, review_status: 'protected', security_warnings: ['prompt-like text']}}}};
` + sourceBetween(memoryJs, 'function knowledgeMemoryID', 'function refreshMemoryEvents') + `
renderKnowledgeMemoryLedger();
globalThis.__body = document.getElementById('knowledgeMemoryBody').innerHTML;
globalThis.__detail = document.getElementById('knowledgeMemoryDetail').innerHTML;
globalThis.__personal = document.getElementById('knowledgePersonalCount').textContent;
globalThis.__source = document.getElementById('knowledgeSourceCount').textContent;
globalThis.__dream = document.getElementById('knowledgeDreamCount').textContent;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.equal(context.__personal, '1');
  assert.equal(context.__source, '4');
  assert.equal(context.__dream, '1');
  assert.match(context.__body, /personal_archive/);
  assert.match(context.__body, /creative_knowledge/);
  assert.match(context.__body, /daily_intake_rule/);
  assert.match(context.__body, /original/);
  assert.match(context.__body, /protected/);
  assert.match(context.__body, /compressed/);
  assert.match(context.__body, /warning/);
  assert.match(context.__body, /stg_movie/);
  assert.match(context.__body, /Detail/);
  assert.match(context.__detail, /Original \/ Protected/);
  assert.match(context.__detail, /Compressed \/ Summary/);
  assert.match(context.__detail, /Warning \/ Review/);
  assert.match(context.__detail, /Review \/ Promote Comparison/);
  assert.match(context.__detail, /raw bio/);
  assert.match(context.__detail, /bio digest/);
  assert.match(context.__detail, /warnings=1/);
  assert.match(context.__detail, /promote_blockers=warnings, protected_original, related_staging_not_validated/);
  assert.match(context.__detail, /related_validated=0/);
  assert.match(context.__detail, /prompt-like text/);
  assert.match(context.__detail, /Related Source Registry Staging/);
  assert.match(context.__detail, /stg_personal/);
});

test('viewer renders knowledge memory ledger fetch errors as visible state', async () => {
  const memoryJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/memory.js', 'utf8');
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new FakeElement(id));
    return elements.get(id);
  };
  const document = {
    getElementById: get,
    createElement() {
      return new FakeElement();
    },
  };
  const requested = [];
  const source = `
function esc(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function fdt(s) { return String(s || '-'); }
const state = {memory: {
  knowledgeMemory: {
    personal_archive: [{entry_id: 'stale_personal', title: 'stale personal'}],
    creative_knowledge: [{item_id: 'stale_creative', title: 'stale creative'}],
    news_knowledge: [{item_id: 'stale_news', topic: 'stale news'}],
    daily_intake_rules: [{rule_id: 'stale_rule', title: 'stale rule'}],
    temporal_markers: [{marker_id: 'stale_marker', summary: 'stale marker'}],
    dream_runs: [{run_id: 'stale_dream', topic: 'stale dream'}],
  },
  sourceRegistryStaging: [],
  knowledgeMemoryFetchError: '',
  knowledgeMemoryDetail: null,
}};
` + sourceBetween(memoryJs, 'function knowledgeMemoryID', 'function refreshMemoryEvents') + `
globalThis.__refreshKnowledgeMemoryLedger = refreshKnowledgeMemoryLedger;
globalThis.__state = state;
`;
  const context = vm.createContext({
    document,
    console: {error() {}},
    URLSearchParams,
    fetch(url) {
      requested.push(url);
      return Promise.resolve({
        ok: false,
        status: 500,
        text: () => Promise.resolve('invalid knowledge memory ledger'),
      });
    },
  });
  vm.runInContext(source, context);
  context.__refreshKnowledgeMemoryLedger();
  await new Promise((resolve) => setImmediate(resolve));

  assert.match(requested[0], /\/viewer\/knowledge-memory\?limit=20/);
  assert.equal(get('knowledgePersonalCount').textContent, '0');
  assert.equal(get('knowledgeSourceCount').textContent, '0');
  assert.equal(get('knowledgeDreamCount').textContent, '0');
  assert.match(get('knowledgeMemoryBody').innerHTML, /Knowledge memory ledger unavailable: HTTP 500: invalid knowledge memory ledger/);
  assert.match(get('knowledgeMemoryDetail').innerHTML, /HTTP 500: invalid knowledge memory ledger/);
  assert.doesNotMatch(get('knowledgeMemoryBody').innerHTML, /stale_personal/);
  assert.doesNotMatch(get('knowledgeMemoryBody').innerHTML, /stale_creative/);
  assert.doesNotMatch(get('knowledgeMemoryBody').innerHTML, /stale_dream/);
  assert.equal(context.__state.memory.knowledgeMemory.personal_archive.length, 0);
  assert.equal(context.__state.memory.knowledgeMemory.creative_knowledge.length, 0);
  assert.equal(context.__state.memory.knowledgeMemory.news_knowledge.length, 0);
  assert.equal(context.__state.memory.knowledgeMemory.daily_intake_rules.length, 0);
  assert.equal(context.__state.memory.knowledgeMemory.temporal_markers.length, 0);
  assert.equal(context.__state.memory.knowledgeMemory.dream_runs.length, 0);
});

test('viewer renders memory tab knowledge detail fetch errors as visible state', async () => {
  const memoryJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/memory.js', 'utf8');
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new FakeElement(id));
    return elements.get(id);
  };
  const document = {
    getElementById: get,
    createElement() {
      return new FakeElement();
    },
  };
  const requested = [];
  const source = `
function esc(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function fdt(s) { return String(s || '-'); }
const state = {memory: {
  knowledgeMemory: {
    personal_archive: [],
    creative_knowledge: [],
    news_knowledge: [{item_id: 'news_1', topic: 'visible news'}],
    daily_intake_rules: [],
    temporal_markers: [],
    dream_runs: [],
  },
  sourceRegistryStaging: [],
  knowledgeMemoryFetchError: '',
  knowledgeMemoryDetail: {detail_type: 'news_knowledge', id: 'stale_news', item: {topic: 'stale detail'}},
}};
` + sourceBetween(memoryJs, 'function knowledgeMemoryID', 'function refreshMemoryEvents') + `
globalThis.__fetchMemoryKnowledgeDetail = fetchMemoryKnowledgeDetail;
globalThis.__state = state;
renderKnowledgeMemoryDetail();
`;
  const context = vm.createContext({
    document,
    console: {error() {}},
    URLSearchParams,
    fetch(url) {
      requested.push(url);
      return Promise.resolve({
        ok: false,
        status: 500,
        statusText: 'Internal Server Error',
        text: () => Promise.resolve('knowledge detail store unavailable'),
      });
    },
  });
  vm.runInContext(source, context);
  context.__fetchMemoryKnowledgeDetail('news_knowledge', 'news_1');
  await new Promise((resolve) => setImmediate(resolve));

  assert.match(requested[0], /detail_type=news_knowledge/);
  assert.match(requested[0], /id=news_1/);
  assert.match(get('knowledgeMemoryDetail').innerHTML, /HTTP 500: knowledge detail store unavailable/);
  assert.doesNotMatch(get('knowledgeMemoryDetail').innerHTML, /stale_news/);
  assert.doesNotMatch(get('knowledgeMemoryDetail').innerHTML, /stale detail/);
  assert.equal(context.__state.memory.knowledgeMemoryDetail.error, 'HTTP 500: knowledge detail store unavailable');
  assert.equal(context.__state.memory.knowledgeMemoryDetail.detail_type, 'news_knowledge');
  assert.equal(context.__state.memory.knowledgeMemoryDetail.id, 'news_1');
});

test('viewer renders source registry staging related knowledge column', () => {
  const memoryJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/memory.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || '').replace(/[&<>"']/g, (c) => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function fdt(s) { return String(s || '-'); }
const state = {memory: {
  knowledgeMemory: {
    creative_knowledge: [{item_id: 'ck_1', title: '映画知識', source_id: 'src_movie', summary: 'movie digest'}],
    news_knowledge: [{item_id: 'news_1', topic: 'tech news', source_url: 'https://example.com/news'}],
  },
  sourceRegistryStaging: [
    {id: 'stg_movie', source_id: 'src_movie', validation_status: 'pending', kind: 'external_fetch', namespace: 'kb:creative', summary_draft: 'movie source'},
    {id: 'stg_news', source_url: 'https://example.com/news', validation_status: 'pending', kind: 'external_fetch', namespace: 'kb:news', summary_draft: 'news source'},
  ],
}};
` + sourceBetween(memoryJs, 'function knowledgeMemoryID', 'function setSourceRegistryStagingStatus') + `
renderSourceRegistryStaging();
globalThis.__body = document.getElementById('sourceRegistryStagingBody').innerHTML;
`;
  const context = vm.createContext({document, validateSourceRegistryStaging() {}, promoteSourceRegistryStaging() {}});
  vm.runInContext(source, context);

  assert.match(context.__body, /creative_knowledge:ck_1/);
  assert.match(context.__body, /news_knowledge:news_1/);
  assert.match(context.__body, /domain_graph/);
  assert.match(context.__body, />Graph</);
});

test('viewer builds source registry domain graph promotion payload', async () => {
  const memoryJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/memory.js', 'utf8');
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new FakeElement(id));
    return elements.get(id);
  };
  const document = {
    getElementById: get,
    createElement() {
      return new FakeElement();
    },
  };
  get('domainGraphDomain').value = '';
  get('sourceRegistryStagingGraphDomain').value = 'movie';
  get('sourceRegistryStagingGraphEntityType').value = 'work';
  get('sourceRegistryStagingGraphEntityID').value = 'movie:1';
  get('sourceRegistryStagingGraphRelation').value = 'catalog_fact';
  get('sourceRegistryStagingGraphConfidence').value = '0.72';
  const requests = [];
  let graphRefreshCount = 0;
  let snapshotRefreshCount = 0;
  let stagingRefreshCount = 0;
  const source = `
function esc(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function fdt(s) { return String(s || '-'); }
const state = {memory: {
  sourceRegistry: [],
  sourceRegistryStaging: [{id: 'stg_1', validation_status: 'validated', summary_draft: 'candidate'}],
  knowledgeMemory: {},
  sourceRegistryLastRun: null,
}};
function refreshSourceRegistryStaging() { globalThis.__stagingRefreshCount += 1; }
function refreshMemorySnapshot() { globalThis.__snapshotRefreshCount += 1; }
function refreshDomainGraphAssertions() { globalThis.__graphRefreshCount += 1; }
` + sourceBetween(memoryJs, 'function renderSourceRegistry', 'function saveSourceRegistryEntry') + `
refreshSourceRegistryStaging = function() { globalThis.__stagingRefreshCount += 1; };
refreshMemorySnapshot = function() { globalThis.__snapshotRefreshCount += 1; };
refreshDomainGraphAssertions = function() { globalThis.__graphRefreshCount += 1; };
globalThis.__promoteSourceRegistryStaging = promoteSourceRegistryStaging;
globalThis.__state = state;
`;
  const context = vm.createContext({
    document,
    console: {error() {}},
    __graphRefreshCount: graphRefreshCount,
    __snapshotRefreshCount: snapshotRefreshCount,
    __stagingRefreshCount: stagingRefreshCount,
    fetch(url, options = {}) {
      requests.push({url: String(url), body: JSON.parse(String(options.body || '{}'))});
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({target: 'domain_graph', item: {ID: 'dg_1'}}),
      });
    },
  });
  vm.runInContext(source, context);
  context.__promoteSourceRegistryStaging('stg_1', 'domain_graph');
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(requests.length, 1);
  assert.equal(requests[0].url, '/viewer/source-registry?action=promote');
  assert.deepEqual(requests[0].body, {
    id: 'stg_1',
    target: 'domain_graph',
    domain: 'movie',
    entity_type: 'work',
    entity_id: 'movie:1',
    relation_type: 'catalog_fact',
    confidence: 0.72,
  });
  assert.equal(get('sourceRegistryStagingStatusLine').innerHTML, '<span class="badge">promoted=domain_graph</span>');
  assert.equal(context.__stagingRefreshCount, 1);
  assert.equal(context.__snapshotRefreshCount, 1);
  assert.equal(context.__graphRefreshCount, 1);
});

test('viewer uses domain graph filter defaults for source registry graph promotion', async () => {
  const memoryJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/memory.js', 'utf8');
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new FakeElement(id));
    return elements.get(id);
  };
  const document = {
    getElementById: get,
    createElement() {
      return new FakeElement();
    },
  };
  get('domainGraphDomain').value = 'manga';
  get('sourceRegistryStagingGraphConfidence').value = 'not-a-number';
  const requests = [];
  const source = `
function esc(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function fdt(s) { return String(s || '-'); }
const state = {memory: {sourceRegistry: [], sourceRegistryStaging: [], knowledgeMemory: {}}};
function refreshSourceRegistryStaging() {}
function refreshMemorySnapshot() {}
function refreshDomainGraphAssertions() {}
` + sourceBetween(memoryJs, 'function renderSourceRegistry', 'function saveSourceRegistryEntry') + `
refreshSourceRegistryStaging = function() {};
refreshMemorySnapshot = function() {};
refreshDomainGraphAssertions = function() {};
globalThis.__promoteSourceRegistryStaging = promoteSourceRegistryStaging;
`;
  const context = vm.createContext({
    document,
    console: {error() {}},
    fetch(url, options = {}) {
      requests.push({url: String(url), body: JSON.parse(String(options.body || '{}'))});
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({target: 'domain_graph', item: {ID: 'dg_1'}}),
      });
    },
  });
  vm.runInContext(source, context);
  context.__promoteSourceRegistryStaging('stg_2', 'domain_graph');
  await new Promise((resolve) => setImmediate(resolve));

  assert.deepEqual(requests[0].body, {
    id: 'stg_2',
    target: 'domain_graph',
    domain: 'manga',
    entity_type: 'work',
  });
});

test('viewer renders web gather diagnostics from search cache and staging', () => {
  const memoryJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/memory.js', 'utf8');
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new FakeElement(id));
    return elements.get(id);
  };
  const document = {
    getElementById: get,
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || '').replace(/[&<>"']/g, (c) => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function fdt(s) { return String(s || '-'); }
const state = {memory: {
  searchCache: [{Provider: 'searxng', RawQuery: 'RenCrow queue timeout', SourceURLs: ['https://example.com/a', 'https://example.com/b']}],
  sourceRegistry: [{kind: 'web_gather', source_id: 'web:bad', last_status: 'error', last_error: 'request timed out'}],
  sourceRegistryStaging: [{id: 'stg_web', validation_status: 'pending', source_id: 'web:example', summary_draft: 'web gather item', meta: {fetcher: 'web_gather', security_warnings: ['warn']}}],
}};
` + sourceBetween(memoryJs, 'function eventPayloadSummary', 'function sourceRegistryStagingReviewSummary') + `
renderWebGatherDiagnostics();
globalThis.__summary = document.getElementById('webGatherSummaryBody').innerHTML;
globalThis.__recent = document.getElementById('webGatherRecentBody').innerHTML;
`;
  const context = vm.createContext({document, validateSourceRegistryStaging() {}});
  vm.runInContext(source, context);

  assert.match(context.__summary, /RenCrow queue timeout/);
  assert.match(context.__summary, /searxng/);
  assert.match(context.__summary, /blocked \/ timeout \/ extraction_failed/);
  assert.match(context.__summary, /0 \/ 1 \/ 0/);
  assert.match(context.__recent, /web:example/);
  assert.match(context.__recent, /web gather item/);
  assert.match(context.__recent, /Validate/);
});

test('viewer renders source registry fetch errors as visible state', async () => {
  const memoryJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/memory.js', 'utf8');
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new FakeElement(id));
    return elements.get(id);
  };
  const document = {
    getElementById: get,
    createElement() {
      return new FakeElement();
    },
  };
  get('sourceRegistryStagingStatus').value = 'pending';
  const requested = [];
  const source = `
function esc(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function fdt(s) { return String(s || '-'); }
const state = {memory: {
  sourceRegistry: [{source_id: 'stale_source', kind: 'rss', url: 'https://example.com/stale'}],
  sourceRegistryStaging: [{id: 'stale_staging', validation_status: 'pending', summary_draft: 'stale staging'}],
  knowledgeMemory: {},
  sourceRegistryFetchError: '',
  sourceRegistryStagingFetchError: '',
}};
` + sourceBetween(memoryJs, 'function renderSourceRegistry', 'function saveSourceRegistryEntry') + `
globalThis.__refreshSourceRegistry = refreshSourceRegistry;
globalThis.__refreshSourceRegistryStaging = refreshSourceRegistryStaging;
globalThis.__state = state;
`;
  const context = vm.createContext({
    document,
    console: {error() {}},
    URLSearchParams,
    fetch(url) {
      requested.push(url);
      if (String(url).includes('action=staging')) {
        return Promise.resolve({
          ok: false,
          status: 503,
          text: () => Promise.resolve('source registry staging unavailable'),
        });
      }
      return Promise.resolve({
        ok: false,
        status: 503,
        text: () => Promise.resolve('source registry unavailable'),
      });
    },
  });
  vm.runInContext(source, context);
  context.__refreshSourceRegistry();
  context.__refreshSourceRegistryStaging();
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(requested.filter((url) => String(url).includes('/viewer/source-registry')).length, 2);
  assert.match(get('sourceRegistryBody').innerHTML, /Source Registry unavailable: HTTP 503: source registry unavailable/);
  assert.match(get('sourceRegistryStagingBody').innerHTML, /Source Registry staging unavailable: HTTP 503: source registry staging unavailable/);
  assert.match(get('sourceRegistryStagingStatusLine').innerHTML, /staging unavailable: HTTP 503: source registry staging unavailable/);
  assert.doesNotMatch(get('sourceRegistryBody').innerHTML, /stale_source/);
  assert.doesNotMatch(get('sourceRegistryStagingBody').innerHTML, /stale_staging/);
  assert.equal(context.__state.memory.sourceRegistry.length, 0);
  assert.equal(context.__state.memory.sourceRegistryStaging.length, 0);
});

test('viewer renders domain graph assertions without raw text', async () => {
  const memoryJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/memory.js', 'utf8');
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new FakeElement(id));
    return elements.get(id);
  };
  const document = {
    getElementById: get,
    createElement() {
      return new FakeElement();
    },
  };
  get('domainGraphDomain').value = 'movie';
  get('domainGraphEntityType').value = 'work';
  get('domainGraphSourceID').value = 'web:eiga';
  get('domainGraphStatusFilter').value = 'validated';
  const requested = [];
  const source = `
function esc(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function fdt(s) { return String(s || '-'); }
const state = {memory: {
  domainGraphAssertions: [],
  domainGraphAssertionsMeta: {limit: 50, offset: 0, total: 0},
  domainGraphAssertionsFetchError: '',
}};
` + sourceBetween(memoryJs, 'function domainGraphAssertionCounts', 'function renderSourceRegistry') + `
globalThis.__refreshDomainGraphAssertions = refreshDomainGraphAssertions;
globalThis.__state = state;
`;
  const context = vm.createContext({
    document,
    console: {error() {}},
    URLSearchParams,
    fetch(url) {
      requested.push(String(url));
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({
          items: [{
            id: 'dg:movie:evt:hash',
            staging_id: 'kb:movie:evt:hash',
            domain: 'movie',
            entity_type: 'work',
            entity_id: 'movie:1',
            relation_type: 'performed_by',
            source_id: 'web:eiga',
            source_url: 'https://example.com/movie/1',
            raw_hash: 'hash',
            summary: 'visible summary',
            confidence: 0.8,
            validation_status: 'validated',
            evidence: {staging_id: 'kb:movie:evt:hash', raw_text: 'do not render raw web text'},
            created_at: '2026-06-06T10:00:00Z',
            updated_at: '2026-06-06T10:05:00Z',
          }],
          limit: 50,
          offset: 0,
          total: 1,
        }),
      });
    },
  });
  vm.runInContext(source, context);
  context.__refreshDomainGraphAssertions();
  await new Promise((resolve) => setImmediate(resolve));

  assert.match(requested[0], /\/viewer\/domain-graph\/assertions\?limit=50/);
  assert.match(requested[0], /domain=movie/);
  assert.match(requested[0], /entity_type=work/);
  assert.match(requested[0], /source_id=web%3Aeiga/);
  assert.equal(get('domainGraphAssertionCount').textContent, '1');
  assert.equal(get('domainGraphDomainCount').textContent, '1');
  assert.equal(get('domainGraphSourceCount').textContent, '1');
  assert.match(get('domainGraphAssertionStatus').innerHTML, /total=1/);
  assert.match(get('domainGraphAssertionBody').innerHTML, /visible summary/);
  assert.match(get('domainGraphAssertionBody').innerHTML, /source_url=https:\/\/example\.com\/movie\/1/);
  assert.match(get('domainGraphAssertionBody').innerHTML, /raw_text/);
  assert.match(get('domainGraphAssertionBody').innerHTML, /\[redacted\]/);
  assert.doesNotMatch(get('domainGraphAssertionBody').innerHTML, /do not render raw web text/);
});

test('viewer clears stale domain graph assertions on fetch failure', async () => {
  const memoryJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/memory.js', 'utf8');
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new FakeElement(id));
    return elements.get(id);
  };
  const document = {
    getElementById: get,
    createElement() {
      return new FakeElement();
    },
  };
  get('domainGraphStatusFilter').value = '';
  const source = `
function esc(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function fdt(s) { return String(s || '-'); }
const state = {memory: {
  domainGraphAssertions: [{id: 'stale', domain: 'movie', entity_type: 'work', source_id: 'web:stale', raw_hash: 'stale', summary: 'stale assertion'}],
  domainGraphAssertionsMeta: {limit: 50, offset: 0, total: 1},
  domainGraphAssertionsFetchError: '',
}};
` + sourceBetween(memoryJs, 'function domainGraphAssertionCounts', 'function renderSourceRegistry') + `
globalThis.__refreshDomainGraphAssertions = refreshDomainGraphAssertions;
globalThis.__state = state;
`;
  const context = vm.createContext({
    document,
    console: {error() {}},
    URLSearchParams,
    fetch() {
      return Promise.resolve({
        ok: false,
        status: 503,
        statusText: 'Service Unavailable',
        text: () => Promise.resolve('domain graph unavailable'),
      });
    },
  });
  vm.runInContext(source, context);
  context.__refreshDomainGraphAssertions();
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(get('domainGraphAssertionCount').textContent, '0');
  assert.match(get('domainGraphAssertionStatus').innerHTML, /Domain Graph unavailable: HTTP 503: domain graph unavailable/);
  assert.match(get('domainGraphAssertionBody').innerHTML, /Domain Graph unavailable: HTTP 503: domain graph unavailable/);
  assert.doesNotMatch(get('domainGraphAssertionBody').innerHTML, /stale assertion/);
  assert.equal(context.__state.memory.domainGraphAssertions.length, 0);
});

test('viewer renders source registry action errors with response body', async () => {
  const memoryJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/memory.js', 'utf8');
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new FakeElement(id));
    return elements.get(id);
  };
  const document = {
    getElementById: get,
    createElement() {
      return new FakeElement();
    },
  };
  get('sourceRegistryStagingTrust').value = '0.8';
  get('sourceRegistryStagingCategory').value = 'tech';
  get('sourceRegistryStagingGraphDomain').value = 'movie';
  get('sourceRegistryStagingGraphEntityType').value = 'work';
  const requested = [];
  const source = `
function esc(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function fdt(s) { return String(s || '-'); }
const state = {memory: {
  sourceRegistry: [],
  sourceRegistryStaging: [{id: 'stg_1', validation_status: 'pending', summary_draft: 'candidate'}],
  knowledgeMemory: {},
  sourceRegistryLastRun: null,
}};
` + sourceBetween(memoryJs, 'function renderSourceRegistry', 'function saveSourceRegistryEntry') + `
globalThis.__validateSourceRegistryStaging = validateSourceRegistryStaging;
globalThis.__promoteSourceRegistryStaging = promoteSourceRegistryStaging;
globalThis.__runSourceRegistryEntry = runSourceRegistryEntry;
globalThis.__state = state;
`;
  const context = vm.createContext({
    document,
    console: {error() {}},
    fetch(url) {
      requested.push(String(url));
      const raw = String(url);
      if (raw.includes('action=validate')) {
        return Promise.resolve({
          ok: false,
          status: 422,
          statusText: 'Unprocessable Entity',
          text: () => Promise.resolve('validation issues present'),
        });
      }
      if (raw.includes('action=promote')) {
        return Promise.resolve({
          ok: false,
          status: 409,
          statusText: 'Conflict',
          text: () => Promise.resolve('promotion target mismatch'),
        });
      }
      return Promise.resolve({
        ok: false,
        status: 503,
        statusText: 'Service Unavailable',
        text: () => Promise.resolve('source registry runtime unavailable'),
      });
    },
  });
  vm.runInContext(source, context);

  context.__validateSourceRegistryStaging('stg_1');
  await new Promise((resolve) => setImmediate(resolve));
  assert.match(get('sourceRegistryStagingStatusLine').innerHTML, /HTTP 422: validation issues present/);
  assert.doesNotMatch(get('sourceRegistryStagingStatusLine').innerHTML, /source registry staging validation failed/);

  context.__promoteSourceRegistryStaging('stg_1', 'news');
  await new Promise((resolve) => setImmediate(resolve));
  assert.match(get('sourceRegistryStagingStatusLine').innerHTML, /HTTP 409: promotion target mismatch/);
  assert.doesNotMatch(get('sourceRegistryStagingStatusLine').innerHTML, /source registry staging promotion failed/);

  context.__promoteSourceRegistryStaging('stg_1', 'domain_graph');
  await new Promise((resolve) => setImmediate(resolve));
  assert.match(get('sourceRegistryStagingStatusLine').innerHTML, /HTTP 409: promotion target mismatch/);
  assert.doesNotMatch(get('sourceRegistryStagingStatusLine').innerHTML, /source registry staging promotion failed/);

  context.__runSourceRegistryEntry('src_1');
  await new Promise((resolve) => setImmediate(resolve));
  assert.match(get('sourceRegistryRunStatus').innerHTML, /Source Registry run unavailable: HTTP 503: source registry runtime unavailable/);
  assert.equal(context.__state.memory.sourceRegistryLastRun.error, 'HTTP 503: source registry runtime unavailable');
  assert.deepEqual(requested, [
    '/viewer/source-registry?action=validate',
    '/viewer/source-registry?action=promote',
    '/viewer/source-registry?action=promote',
    '/viewer/source-registry?action=run&source_id=src_1',
  ]);
});

test('viewer renders source registry yaml action errors with response body', async () => {
  const memoryJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/memory.js', 'utf8');
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new FakeElement(id));
    return elements.get(id);
  };
  const document = {
    getElementById: get,
    createElement() {
      return new FakeElement();
    },
  };
  get('sourceRegistryID').value = 'src_1';
  get('sourceRegistryURL').value = 'https://example.com/feed.xml';
  get('sourceRegistryKind').value = 'rss';
  get('sourceRegistryTrust').value = '0.9';
  get('sourceRegistryInterval').value = '3600';
  get('sourceRegistryLicense').value = 'manual';
  get('sourceRegistryNamespace').value = 'kb:test';
  get('sourceRegistryEnabled').checked = true;
  get('sourceRegistryYAML').value = 'sources:\n- id: src_1\n';
  const requested = [];
  const source = `
function esc(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function fdt(s) { return String(s || '-'); }
const sourceRegistryYAML = document.getElementById('sourceRegistryYAML');
const state = {memory: {
  sourceRegistry: [],
  sourceRegistryStaging: [],
  knowledgeMemory: {},
  sourceRegistryLastRun: null,
}};
` + sourceBetween(memoryJs, 'function renderSourceRegistry', 'function refreshMemorySnapshot') + `
globalThis.__saveSourceRegistryEntry = saveSourceRegistryEntry;
globalThis.__exportSourceRegistryYAML = exportSourceRegistryYAML;
globalThis.__importSourceRegistryYAML = importSourceRegistryYAML;
`;
  const context = vm.createContext({
    document,
    console: {error() {}},
    fetch(url, options = {}) {
      requested.push({url: String(url), method: String(options.method || 'GET')});
      const raw = String(url);
      if (raw.includes('format=yaml') && String(options.method || 'GET') === 'POST') {
        return Promise.resolve({
          ok: false,
          status: 400,
          statusText: 'Bad Request',
          text: () => Promise.resolve('invalid source registry yaml'),
        });
      }
      if (raw.includes('format=yaml')) {
        return Promise.resolve({
          ok: false,
          status: 502,
          statusText: 'Bad Gateway',
          text: () => Promise.resolve('source registry export unavailable'),
        });
      }
      return Promise.resolve({
        ok: false,
        status: 503,
        statusText: 'Service Unavailable',
        text: () => Promise.resolve('source registry save unavailable'),
      });
    },
  });
  vm.runInContext(source, context);

  context.__saveSourceRegistryEntry();
  await new Promise((resolve) => setImmediate(resolve));
  assert.match(get('sourceRegistryRunStatus').innerHTML, /Source Registry save unavailable: HTTP 503: source registry save unavailable/);
  assert.doesNotMatch(get('sourceRegistryRunStatus').innerHTML, /source registry save failed/);

  context.__exportSourceRegistryYAML();
  await new Promise((resolve) => setImmediate(resolve));
  assert.match(get('sourceRegistryRunStatus').innerHTML, /Source Registry export unavailable: HTTP 502: source registry export unavailable/);
  assert.doesNotMatch(get('sourceRegistryRunStatus').innerHTML, /source registry export failed/);

  context.__importSourceRegistryYAML();
  await new Promise((resolve) => setImmediate(resolve));
  assert.match(get('sourceRegistryRunStatus').innerHTML, /Source Registry import unavailable: HTTP 400: invalid source registry yaml/);
  assert.doesNotMatch(get('sourceRegistryRunStatus').innerHTML, /source registry import failed/);
  assert.deepEqual(requested, [
    {url: '/viewer/source-registry', method: 'POST'},
    {url: '/viewer/source-registry?format=yaml', method: 'GET'},
    {url: '/viewer/source-registry?format=yaml', method: 'POST'},
  ]);
});

test('viewer renders ops logs fetch errors as visible state', async () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new FakeElement(id));
    return elements.get(id);
  };
  const document = {
    getElementById: get,
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function fdt(s) { return String(s || '-'); }
function agName(s) { return String(s || '-'); }
function bindDCISearchControls() {}
function renderKnowledgeMemoryDetailFocus() {}
function runtimeHealthDetailText(report, errorText) { return errorText ? 'blocked: ' + String(errorText) : (report ? '/health' : 'blocked: /health not checked yet'); }
const stubCard = () => ({title: 'stub', big: '-', sub: '-'});
const toolHarnessOpsCard = stubCard;
const dciOpsCard = stubCard;
const sandboxOpsCard = stubCard;
const skillGovernanceOpsCard = stubCard;
const workstreamOpsCard = stubCard;
const revenueOpsCard = stubCard;
const personaObservationOpsCard = stubCard;
const browserTraceAPIOpsCard = stubCard;
const complexityHotspotOpsCard = stubCard;
const aiWorkflowOpsCard = stubCard;
const superAgentOpsCard = stubCard;
const heavyWorkerRuntimeOpsCard = stubCard;
const knowledgeMemoryOpsCard = stubCard;
const runtimeBlockedRoutesOpsCard = stubCard;
const AGENTS = ['mio', 'shiro'];
const state = {
  jobs: {},
  agents: {mio: {state: 'idle', jobID: 'stale_mio_job'}, shiro: {state: 'offline', jobID: 'stale_worker_job'}},
  ops: {
    persistedLogs: [
      {type: 'agent.response', from: 'mio', to: 'user', job_id: 'stale_job', route: 'CHAT', content: 'stale persisted report', timestamp: '2026-05-20T00:00:00Z'},
      {type: 'agent.error', job_id: 'stale_error_job', route: 'CODE', content: 'stale error', timestamp: '2026-05-20T00:00:01Z'},
    ],
    opsLogsFetchError: '',
    lastMioReport: {type: 'agent.response', from: 'mio', to: 'user', job_id: 'stale_job', content: 'stale mio report', timestamp: '2026-05-20T00:00:00Z'},
    latestJobID: 'stale_job',
    latestRoute: 'CHAT',
    latestError: {type: 'agent.error', job_id: 'stale_error_job', content: 'stale error'},
  },
};
` + sourceBetween(opsJs, 'function latestOpsEventBy', 'function toolHarnessField') + `
function renderDeskViews() { globalThis.__deskRendered = true; }
` + sourceBetween(viewerJs, 'function refreshOpsData', 'function refreshToolHarnessData') + `
globalThis.__refreshOpsData = refreshOpsData;
globalThis.__state = state;
`;
  const context = vm.createContext({
    document,
    console: {error() {}},
    fetch() {
      return Promise.resolve({
        ok: false,
        status: 500,
        text: () => Promise.resolve('persisted log store unavailable'),
      });
    },
  });
  vm.runInContext(source, context);
  context.__refreshOpsData();
  await new Promise((resolve) => setImmediate(resolve));

  assert.match(get('opsCards').innerHTML, /Latest Job/);
  assert.match(get('opsCards').innerHTML, /unavailable/);
  assert.match(get('opsCards').innerHTML, /ops logs unavailable: HTTP 500: persisted log store unavailable/);
  assert.match(get('opsTriageCards').innerHTML, /Errors/);
  assert.match(get('opsTriageCards').innerHTML, /unavailable/);
  assert.match(get('opsTriageCards').innerHTML, /ops logs unavailable: HTTP 500: persisted log store unavailable/);
  assert.match(get('opsFocusBody').innerHTML, /Ops logs unavailable: HTTP 500: persisted log store unavailable/);
  assert.match(get('opsFeedBody').innerHTML, /Ops logs unavailable: HTTP 500: persisted log store unavailable/);
  assert.doesNotMatch(get('opsCards').innerHTML, /stale_job|stale mio report|stale error/);
  assert.doesNotMatch(get('opsFeedBody').innerHTML, /stale persisted report|stale_error_job/);
  assert.equal(context.__state.ops.persistedLogs.length, 0);
  assert.equal(context.__state.ops.lastMioReport, null);
  assert.equal(context.__state.ops.latestJobID, '');
  assert.equal(context.__state.ops.latestRoute, '');
  assert.equal(context.__state.ops.latestError, null);
  assert.equal(context.__deskRendered, true);
});

test('viewer marks agents unavailable on viewer status fetch errors', async () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const source = `
const AGENTS = ['mio', 'shiro', 'coder'];
const state = {
  viewerStatusFetchError: '',
  agents: {
    mio: {state: 'running', route: 'CHAT', jobID: 'stale_mio_job', reason: '', preview: 'stale mio'},
    shiro: {state: 'idle', route: 'CODE', jobID: 'stale_worker_job', reason: '', preview: 'stale worker'},
    coder: {state: 'idle', route: 'CODE', jobID: 'stale_coder_job', reason: '', preview: 'stale coder'},
  },
};
function renderOverview() { globalThis.__overviewRendered = true; }
function renderRoleSelector() { globalThis.__rolesRendered = true; }
function renderProgress() { globalThis.__progressRendered = true; }
` + sourceBetween(viewerJs, 'function touchAgent', 'function addOpenTask') + `
` + sourceBetween(viewerJs, 'function refreshViewerStatus', 'function ingestEvent') + `
globalThis.__refreshViewerStatus = refreshViewerStatus;
globalThis.__state = state;
`;
  const context = vm.createContext({
    console: {error() {}},
    fetch() {
      return Promise.resolve({
        ok: false,
        status: 503,
        text: () => Promise.resolve('monitor snapshot unavailable'),
      });
    },
  });
  vm.runInContext(source, context);
  context.__refreshViewerStatus();
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(context.__state.viewerStatusFetchError, 'HTTP 503: monitor snapshot unavailable');
  for (const id of ['mio', 'shiro', 'coder']) {
    assert.equal(context.__state.agents[id].state, 'unavailable');
    assert.equal(context.__state.agents[id].route, '-');
    assert.equal(context.__state.agents[id].jobID, '-');
    assert.match(context.__state.agents[id].reason, /viewer status unavailable: HTTP 503: monitor snapshot unavailable/);
    assert.equal(context.__state.agents[id].lastEvent, 'viewer status fetch failed');
    assert.equal(context.__state.agents[id].preview, 'viewer status unavailable');
  }
  assert.equal(context.__overviewRendered, true);
  assert.equal(context.__rolesRendered, true);
  assert.equal(context.__progressRendered, true);
});

test('viewer renders memory events and recall trace fetch errors as visible state', async () => {
  const memoryJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/memory.js', 'utf8');
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new FakeElement(id));
    return elements.get(id);
  };
  const document = {
    getElementById: get,
    createElement() {
      return new FakeElement();
    },
  };
  get('memoryEventNamespace').value = 'kb:test';
  get('memoryNamespace').value = '';
  const requested = [];
  const recallSource = memoryJs.slice(memoryJs.indexOf('function renderRecallTraces'));
  const source = `
function esc(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function fdt(s) { return String(s || '-'); }
function renderNewsPackPanel() {}
const memoryEventNamespace = document.getElementById('memoryEventNamespace');
const memoryNamespace = document.getElementById('memoryNamespace');
const state = {memory: {
  events: [{EventType: 'stale_event', Namespace: 'kb:test', Source: 'stale'}],
  searchCache: [{Provider: 'stale_provider', RawQuery: 'stale query'}],
  traces: [{ResponseID: 'stale_response', Role: 'mio', Items: [{Kind: 'search_cache', Summary: 'stale recall'}]}],
  memoryEventsFetchError: '',
  recallTraceFetchError: '',
}};
` + sourceBetween(memoryJs, 'function memoryEventNamespaceValue', 'function renderSourceRegistry') + `
` + recallSource + `
globalThis.__refreshMemoryEvents = refreshMemoryEvents;
globalThis.__refreshRecallTraces = refreshRecallTraces;
globalThis.__state = state;
`;
  const context = vm.createContext({
    document,
    console: {error() {}},
    URLSearchParams,
    fetch(url) {
      requested.push(url);
      if (String(url).includes('/viewer/memory/events')) {
        return Promise.resolve({
          ok: false,
          status: 500,
          text: () => Promise.resolve('invalid memory event ledger'),
        });
      }
      return Promise.resolve({
        ok: false,
        status: 500,
        text: () => Promise.resolve('invalid recall trace ledger'),
      });
    },
  });
  vm.runInContext(source, context);
  context.__refreshMemoryEvents();
  context.__refreshRecallTraces();
  await new Promise((resolve) => setImmediate(resolve));

  assert.match(requested[0], /namespace=kb%3Atest/);
  assert.match(get('memoryEventBody').innerHTML, /Memory events unavailable: HTTP 500: invalid memory event ledger/);
  assert.match(get('searchCacheBody').innerHTML, /Search cache unavailable: HTTP 500: invalid memory event ledger/);
  assert.match(get('recallTraceBody').innerHTML, /Recall traces unavailable: HTTP 500: invalid recall trace ledger/);
  assert.equal(get('memoryEventCount').textContent, '0');
  assert.equal(get('searchCacheCount').textContent, '0');
  assert.doesNotMatch(get('memoryEventBody').innerHTML, /stale_event/);
  assert.doesNotMatch(get('searchCacheBody').innerHTML, /stale_provider/);
  assert.doesNotMatch(get('recallTraceBody').innerHTML, /stale_response/);
  assert.equal(context.__state.memory.events.length, 0);
  assert.equal(context.__state.memory.searchCache.length, 0);
  assert.equal(context.__state.memory.traces.length, 0);
});

test('viewer renders recall trace status section tokens and warnings', () => {
  const memoryJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/memory.js', 'utf8');
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new FakeElement(id));
    return elements.get(id);
  };
  const document = {
    getElementById: get,
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function fdt(s) { return String(s || '-'); }
const state = {memory: {
  recallTraceFetchError: '',
  traces: [{
    ResponseID: 'turn-1',
    Role: 'mio',
    CreatedAt: '2026-06-19T00:00:00Z',
    Items: [
      {Layer: 'L3', Kind: 'user_memory', Status: 'injected', PromptSection: '[RecallPack: UserMemory]', TokenCount: 8, Reason: 'candidate passed injection policy', Summary: '短く答える'},
      {Layer: 'L2', Kind: 'user_memory', Status: 'filtered_status', PromptSection: '[RecallPack: UserMemory]', TokenCount: 4, Reason: 'candidate memory is not confirmed or pinned', Summary: '候補記憶'},
      {Layer: 'L3', Kind: 'knowledge', Status: 'budget_dropped', PromptSection: '[RecallPack: Knowledge]', TokenCount: 120, Reason: 'token budget dropped Knowledge DB snippet', Summary: '長い知識'}
    ],
  }],
}};
` + sourceBetween(memoryJs, 'function renderRecallTraces', 'function refreshRecallTraces') + `
globalThis.__renderRecallTraces = renderRecallTraces;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);
  context.__renderRecallTraces();

  const html = get('recallTraceBody').innerHTML;
  assert.match(html, /injected/);
  assert.match(html, /\[RecallPack: UserMemory\]/);
  assert.match(html, /filtered_status/);
  assert.match(html, /unpromoted/);
  assert.match(html, /budget_dropped/);
  assert.match(html, /budget/);
  assert.match(html, /120/);
});

test('viewer renders memory snapshot fetch errors as visible state', async () => {
  const memoryJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/memory.js', 'utf8');
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new FakeElement(id));
    return elements.get(id);
  };
  const document = {
    getElementById: get,
    createElement() {
      return new FakeElement();
    },
  };
  get('memoryNamespace').value = 'kb:test';
  get('memoryCategory').value = 'tech';
  get('memoryDomain').value = 'ai';
  const requested = [];
  const source = `
function esc(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function fdt(s) { return String(s || '-'); }
function renderNewsPackPanel() {}
function refreshMemoryLayers() {}
function refreshMemoryEvents() {}
function refreshKnowledgeMemoryLedger() {}
function refreshSourceRegistry() {}
const memoryNamespace = document.getElementById('memoryNamespace');
const memoryCategory = document.getElementById('memoryCategory');
const memoryDomain = document.getElementById('memoryDomain');
const state = {memory: {
  snapshot: {
    memory: [{ID: 'stale_memory', Message: 'stale memory'}],
    news: [{SourceID: 'stale_news', SummaryDraft: 'stale news'}],
    digests: [{DigestText: 'stale digest'}],
    knowledge: [{id: 'stale_knowledge'}],
  },
  memorySnapshotFetchError: '',
}};
` + sourceBetween(memoryJs, 'function renderMemorySnapshot', 'function postMemoryAction') + `
globalThis.__refreshMemorySnapshot = refreshMemorySnapshot;
globalThis.__state = state;
`;
  const context = vm.createContext({
    document,
    console: {error() {}},
    URLSearchParams,
    fetch(url) {
      requested.push(url);
      return Promise.resolve({
        ok: false,
        status: 500,
        text: () => Promise.resolve('invalid memory snapshot: stale current view'),
      });
    },
  });
  vm.runInContext(source, context);
  context.__refreshMemorySnapshot();
  await new Promise((resolve) => setImmediate(resolve));

  assert.match(requested[0], /namespace=kb%3Atest/);
  assert.match(requested[0], /category=tech/);
  assert.match(requested[0], /domain=ai/);
  assert.equal(get('memoryCount').textContent, '0');
  assert.equal(get('newsPackCount').textContent, '0');
  assert.equal(get('digestCount').textContent, '0');
  assert.equal(get('knowledgeCount').textContent, '0');
  assert.match(get('memoryBody').innerHTML, /Memory snapshot unavailable: HTTP 500: invalid memory snapshot: stale current view/);
  assert.match(get('newsPackBody').innerHTML, /Memory snapshot news unavailable: HTTP 500: invalid memory snapshot: stale current view/);
  assert.match(get('digestBody').innerHTML, /Memory snapshot digests unavailable: HTTP 500: invalid memory snapshot: stale current view/);
  assert.doesNotMatch(get('memoryBody').innerHTML, /stale_memory/);
  assert.doesNotMatch(get('newsPackBody').innerHTML, /stale_news/);
  assert.doesNotMatch(get('digestBody').innerHTML, /stale digest/);
  assert.equal(context.__state.memory.snapshot.memory.length, 0);
  assert.equal(context.__state.memory.snapshot.news.length, 0);
  assert.equal(context.__state.memory.snapshot.digests.length, 0);
  assert.equal(context.__state.memory.snapshot.knowledge.length, 0);
});

test('viewer renders memory action errors with response body', async () => {
  const memoryJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/memory.js', 'utf8');
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new FakeElement(id));
    return elements.get(id);
  };
  const document = {
    getElementById: get,
    createElement() {
      return new FakeElement();
    },
  };
  get('memoryPromoteKind').value = 'kb';
  get('memoryPromoteID').value = 'e2e';
  const requested = [];
  const source = `
function esc(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function fdt(s) { return String(s || '-'); }
function renderNewsPackPanel() {}
const memoryPromoteKind = document.getElementById('memoryPromoteKind');
const memoryPromoteID = document.getElementById('memoryPromoteID');
const state = {memory: {
  snapshot: {
    memory: [{ID: 'mem_1', Message: 'candidate memory', MemoryState: 'candidate', Namespace: 'kb:e2e'}],
    news: [],
    digests: [],
    knowledge: [],
  },
  memorySnapshotFetchError: '',
  memoryActionError: '',
}};
` + sourceBetween(memoryJs, 'function renderMemorySnapshot', 'function renderMemoryLayers') + `
` + sourceBetween(memoryJs, 'function postMemoryAction', 'function renderRecallTraces') + `
globalThis.__setMemoryState = setMemoryState;
globalThis.__promoteMemory = promoteMemory;
globalThis.__state = state;
`;
  const context = vm.createContext({
    document,
    console: {error() {}},
    fetch(url) {
      requested.push(url);
      if (url === '/viewer/memory/state') {
        return Promise.resolve({
          ok: false,
          status: 503,
          text: () => Promise.resolve('memory state store unavailable'),
        });
      }
      if (url === '/viewer/memory/promote') {
        return Promise.resolve({
          ok: false,
          status: 409,
          text: () => Promise.resolve('memory promote target not validated'),
        });
      }
      throw new Error('unexpected url: ' + url);
    },
  });
  vm.runInContext(source, context);

  context.__setMemoryState('mem_1', 'confirmed');
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(requested[0], '/viewer/memory/state');
  assert.match(get('memoryBody').innerHTML, /Memory action unavailable: HTTP 503: memory state store unavailable/);
  assert.doesNotMatch(get('memoryBody').innerHTML, /memory action failed/);

  context.__promoteMemory('mem_1');
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(requested[1], '/viewer/memory/promote');
  assert.match(get('memoryBody').innerHTML, /Memory action unavailable: HTTP 409: memory promote target not validated/);
  assert.doesNotMatch(get('memoryBody').innerHTML, /memory action failed/);
});

test('viewer renders source registry warning run status', () => {
  const memoryJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/memory.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || '').replace(/[&<>"']/g, (c) => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])); }
const state = {memory: {sourceRegistryLastRun: {result: {Staged: 1, Validated: 1, Warnings: 2}}}};
` + sourceBetween(memoryJs, 'function renderSourceRegistryRunStatus', 'function runSourceRegistryEntry') + `
renderSourceRegistryRunStatus();
globalThis.__status = document.getElementById('sourceRegistryRunStatus').innerHTML;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.match(context.__status, /Source Registry run/);
  assert.match(context.__status, /warnings=2/);
  assert.match(context.__status, /badge warn/);
});

test('viewer renders revenue drilldown graph lines from dashboard summary', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function escAttr(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function ftime(s) { return String(s || '-'); }
function stateClass(s) { return String(s || ''); }
function sandboxField(obj, snake, pascal) {
  if (!obj) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, snake)) return obj[snake];
  if (Object.prototype.hasOwnProperty.call(obj, pascal)) return obj[pascal];
  return undefined;
}
const state = {ops: {
  revenueExternalChannelAdapter: 'unconfigured',
  revenueExternalChannelAdapterConfigured: false,
  revenueExternalSendHumanApprovalRequired: true,
  revenueSummary: {
    total_revenue_amount: 3000,
    paid_customer_count: 2,
    pending_decision_count: 1,
    channel_draft_count: 1,
    external_send_apply_count: 1,
    kpi_trend: [
      {date: '2026-05-17', revenue_amount: 1000, post_count: 2, voice_count: 1},
      {date: '2026-05-18', revenue_amount: 3000, post_count: 3, voice_count: 2},
    ],
    product_sales: [{product_id: 'prod_1', product_name: '低単価商品', revenue_amount: 3000, sales_count: 2}],
    customer_voice_types: [{voice_type: 'blocker', count: 3}],
  },
  revenueHumanDecisions: [{decision_id: 'dec_1', decision_type: 'external_publish', approval_status: 'pending', gate_status: 'needs_review'}],
  revenueChannelDrafts: [{draft_id: 'draft_1', approval_status: 'pending'}],
  revenueExternalSendApplyRecords: [{apply_id: 'apply_1', apply_status: 'blocked', send_result: 'not_sent'}],
}};
` + sourceBetween(opsJs, 'function revenueOpsCard', 'async function reviewRevenueHumanDecision') + `
renderRevenueDrilldown();
renderRevenueChannelDrafts();
renderRevenueExternalSendAudits();
globalThis.__drilldown = document.getElementById('revenueDrilldownResult').textContent;
globalThis.__channelDraftResult = document.getElementById('revenueChannelDraftResult').textContent;
globalThis.__externalSendAudits = document.getElementById('revenueExternalSendAuditBody').innerHTML;
globalThis.__externalSendAuditResult = document.getElementById('revenueExternalSendAuditResult').textContent;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.match(context.__drilldown, /Revenue Drilldown/);
  assert.match(context.__drilldown, /KPI trend graph/);
  assert.match(context.__drilldown, /2026-05-18/);
  assert.match(context.__drilldown, /Product sales graph/);
  assert.match(context.__drilldown, /低単価商品/);
  assert.match(context.__drilldown, /Customer voice graph/);
  assert.match(context.__drilldown, /blocker/);
  assert.match(context.__drilldown, /Decision drilldown/);
  assert.match(context.__drilldown, /dec_1/);
  assert.match(context.__channelDraftResult, /1 total \/ 1 pending \/ 1 draft-only \/ 0 external_send_applied/);
  assert.match(context.__channelDraftResult, /mode: draft-only \/ external send requires human approval: yes/);
  assert.match(context.__externalSendAudits, /apply_1/);
  assert.match(context.__externalSendAudits, /blocked/);
  assert.match(context.__externalSendAudits, /not_sent/);
  assert.match(context.__externalSendAudits, /unconfigured/);
  assert.match(context.__externalSendAuditResult, /1 total \/ 1 blocked \/ 0 sent \/ 1 not sent \/ 0 verified/);
  assert.match(context.__externalSendAuditResult, /external channel adapter: unconfigured \/ configured: no \/ human approval: required/);
  assert.match(context.__externalSendAuditResult, /blocked: no external send applied/);
});

test('viewer renders tool harness and dci fetch errors as visible read-only state', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function ftime(s) { return String(s || '-'); }
function stateClass(s) { return String(s || ''); }
const state = {ops: {
  toolHarnessFetchError: 'HTTP 500: tool harness store unavailable',
  toolHarnessEvents: [{event_id: 'stale_tool_event', tool_name: 'stale_tool', validation_status: 'repaired'}],
  dciFetchError: 'HTTP 500: dci trace store unavailable',
  dciTraces: [{event_id: 'stale_dci_trace', user_query: 'stale vector query', final_evidence_count: 9, status: 'completed'}],
}};
` + sourceBetween(opsJs, 'function toolHarnessField', 'function sandboxField') + `
globalThis.__toolCard = toolHarnessOpsCard();
renderToolHarnessEvents();
globalThis.__toolRows = document.getElementById('toolHarnessBody').innerHTML;
globalThis.__dciCard = dciOpsCard();
renderDCITraces();
globalThis.__dciRows = document.getElementById('dciTraceBody').innerHTML;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.equal(context.__toolCard.big, 'unavailable');
  assert.match(context.__toolCard.sub, /tool harness status unavailable: HTTP 500: tool harness store unavailable/);
  assert.match(context.__toolCard.sub, /blocked: provider protocol recovery state unreadable/);
  assert.match(context.__toolRows, /Tool Harness events unavailable: HTTP 500: tool harness store unavailable/);
  assert.doesNotMatch(context.__toolRows, /stale_tool/);
  assert.equal(context.__dciCard.big, 'unavailable');
  assert.match(context.__dciCard.sub, /dci trace status unavailable: HTTP 500: dci trace store unavailable/);
  assert.match(context.__dciCard.sub, /blocked: read-only evidence state unreadable/);
  assert.match(context.__dciCard.sub, /blocked: VectorDB\/Qdrant E2E not verified/);
  assert.match(context.__dciRows, /DCI search traces unavailable: HTTP 500: dci trace store unavailable/);
  assert.doesNotMatch(context.__dciRows, /stale_dci_trace/);
  assert.doesNotMatch(context.__dciRows, /stale vector query/);
});

test('viewer renders dci manual search fetch errors with response body', async () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const listeners = {};
  class EventElement extends FakeElement {
    constructor(id = '') {
      super(id);
      this.value = '';
      this.disabled = false;
    }
    addEventListener(type, fn) {
      listeners[this.id + ':' + type] = fn;
    }
  }
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new EventElement(id));
      return elements.get(id);
    },
  };
  const source = `
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
const state = {ops: {
  dciLastResult: {pack: {query: 'stale query'}, trace: {status: 'completed'}, Pack: {Evidence: [{FilePath: 'stale.go'}]}},
}};
` + sourceBetween(opsJs, 'function renderDCISearchResult', 'function sandboxField') + `
` + sourceBetween(opsJs, 'let dciSearchBound', 'let llmOpsUIBound') + `
globalThis.__getDCIResult = () => state.ops.dciLastResult;
`;
  const context = vm.createContext({
    document,
    fetch: async () => ({
      ok: false,
      status: 503,
      statusText: 'Service Unavailable',
      text: async () => 'dci search store unavailable',
    }),
    JSON,
  });
  vm.runInContext(source, context);
  document.getElementById('dciSearchInput').value = 'ToolRunner context budget';
  context.bindDCISearchControls();
  listeners['dciSearchBtn:click']();
  await new Promise((resolve) => setImmediate(resolve));

  const resultText = document.getElementById('dciSearchResult').textContent;
  assert.match(resultText, /query: ToolRunner context budget/);
  assert.match(resultText, /status: failed/);
  assert.match(resultText, /error: HTTP 503: dci search store unavailable/);
  assert.doesNotMatch(resultText, /stale query/);
  assert.doesNotMatch(resultText, /stale\.go/);
  assert.equal(context.__getDCIResult().trace.error_message, 'HTTP 503: dci search store unavailable');
  assert.equal(document.getElementById('dciSearchBtn').disabled, false);
});

test('viewer renders evidence and verification fetch errors as visible execution state', () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function fdt(s) { return String(s || '-'); }
function stateClass(s) { return String(s || ''); }
function syncEvidenceQuery() {}
function updateEvidenceNav() {}
const eviStatus = null;
const eviErrorKind = null;
const state = {
  evidenceFetchError: 'HTTP 500: evidence store unavailable',
  verificationFetchError: 'HTTP 500: verification store unavailable',
  evidenceSummaryFetchError: 'HTTP 500: evidence summary unavailable',
  verificationSummaryFetchError: 'HTTP 500: verification summary unavailable',
  evidence: [{job_id: 'stale_job', status: 'passed', goal: 'stale execution'}],
  verificationReports: [{job_id: 'stale_verify', status: 'verified', route: 'stale verification'}],
  evidenceSummary: {status: {passed: 9}, error_kind: {none: 9}},
  verificationSummary: {status: {verified: 7}, trigger_level: {high: 7}},
  evidenceOrder: ['stale_job'],
  selectedEvidenceJobID: 'stale_job',
  selectedEvidenceItem: {job_id: 'stale_job'},
  evidenceSortDesc: true,
};
` + sourceBetween(viewerJs, 'function renderEvidence', 'function refreshDerivedViews') + `
renderEvidence();
renderEvidenceSummary();
globalThis.__rows = document.getElementById('evidenceBody').innerHTML;
globalThis.__detail = document.getElementById('evidenceDetail').textContent;
globalThis.__summary = document.getElementById('evidenceSummaryCards').innerHTML;
globalThis.__state = state;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.match(context.__rows, /Evidence \/ verification unavailable: evidence: HTTP 500: evidence store unavailable; verification: HTTP 500: verification store unavailable/);
  assert.doesNotMatch(context.__rows, /stale_job/);
  assert.doesNotMatch(context.__rows, /stale verification/);
  assert.equal(context.__detail, 'No selection');
  assert.equal(context.__state.evidenceOrder.length, 0);
  assert.equal(context.__state.selectedEvidenceJobID, '');
  assert.match(context.__summary, /Evidence Total/);
  assert.match(context.__summary, /unavailable/);
  assert.match(context.__summary, /evidence summary unavailable: evidence summary: HTTP 500: evidence summary unavailable; verification summary: HTTP 500: verification summary unavailable/);
  assert.match(context.__summary, /blocked: execution evidence state unreadable/);
  assert.doesNotMatch(context.__summary, />9</);
  assert.doesNotMatch(context.__summary, />7</);
});

test('viewer renders evidence detail fetch errors with response body', async () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new FakeElement(id));
    return elements.get(id);
  };
  const document = {
    getElementById: get,
    createElement() {
      return new FakeElement();
    },
  };
  const requested = [];
  const source = `
function esc(s) { return String(s || ''); }
function syncEvidenceQuery() {}
function renderEvidence() {}
function updateEvidenceNav() {}
function renderEvidenceDetail() { return 'unexpected evidence detail success'; }
function renderVerificationReportDetail() { return 'unexpected verification detail success'; }
function scrollEvidenceFocus() {}
function showToast() {}
const state = {
  evidence: [{job_id: 'job_fail', status: 'passed'}],
  verificationReports: [],
  evidenceOrder: ['job_fail'],
  selectedEvidenceJobID: '',
  selectedEvidenceItem: {job_id: 'stale_job', status: 'passed'},
  selectedEvidenceFocus: '',
};
` + sourceBetween(viewerJs, 'function openEvidence', 'window.openEvidence = openEvidence;') + `
globalThis.__openEvidence = openEvidence;
globalThis.__state = state;
`;
  const context = vm.createContext({
    document,
    console: {error() {}},
    fetch(url) {
      requested.push(url);
      return Promise.resolve({
        ok: false,
        status: 500,
        text: () => Promise.resolve('execution evidence detail store unavailable'),
      });
    },
  });
  vm.runInContext(source, context);
  context.__openEvidence('job_fail');
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(requested[0], '/viewer/evidence/detail?job_id=job_fail');
  assert.match(get('evidenceDetail').innerHTML, /HTTP 500: execution evidence detail store unavailable/);
  assert.match(get('evidenceDetail').innerHTML, /job_id=job_fail/);
  assert.doesNotMatch(get('evidenceDetail').innerHTML, /evidence detail fetch failed/);
  assert.equal(context.__state.selectedEvidenceItem, null);
});

test('viewer renders evidence copy failures as visible button state', async () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const eviCopy = new FakeElement('eviCopy');
  const eviCopySummary = new FakeElement('eviCopySummary');
  eviCopy.textContent = 'Copy JSON';
  eviCopySummary.textContent = 'Copy summary';
  let copyJSONHandler = null;
  let copySummaryHandler = null;
  eviCopy.addEventListener = (type, handler) => {
    if (type === 'click') copyJSONHandler = handler;
  };
  eviCopySummary.addEventListener = (type, handler) => {
    if (type === 'click') copySummaryHandler = handler;
  };
  const source = `
const state = {
  selectedEvidenceItem: {job_id: 'job_1', status: 'failed', error: 'apply failed'},
  evidenceOrder: ['job_1'],
  selectedEvidenceJobID: 'job_1',
};
const eviCopy = globalThis.__eviCopy;
const eviCopySummary = globalThis.__eviCopySummary;
const eviPrev = null;
const eviNext = null;
const eviPos = null;
function buildEvidenceSummary(item) { return 'job_id=' + String(item.job_id || '-'); }
function showToast(message, type) { globalThis.__toasts.push({message, type}); }
function writeClipboardText() { return Promise.reject(new Error('clipboard denied')); }
` + sourceBetween(viewerJs, 'function updateEvidenceNav', 'function errorKindClass') + `
`;
  const context = vm.createContext({
    __eviCopy: eviCopy,
    __eviCopySummary: eviCopySummary,
    __toasts: [],
    console: {error() {}},
    setTimeout(fn) { fn(); },
  });
  vm.runInContext(source, context);

  assert.equal(typeof copyJSONHandler, 'function');
  assert.equal(typeof copySummaryHandler, 'function');
  assert.doesNotThrow(() => copyJSONHandler());
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(eviCopy.textContent, 'Evidence JSON copy unavailable: clipboard denied');
  assert.equal(eviCopy.title, 'Evidence JSON copy unavailable: clipboard denied');
  assert.equal(context.__toasts.at(-1).type, 'error');

  assert.doesNotThrow(() => copySummaryHandler());
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(eviCopySummary.textContent, 'Evidence summary copy unavailable: clipboard denied');
  assert.equal(eviCopySummary.title, 'Evidence summary copy unavailable: clipboard denied');
  assert.equal(context.__toasts.at(-1).message, 'Evidence summary copy failed');
});

test('viewer renders generic copy payload failures as visible button state', async () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const btn = new FakeElement('genericCopy');
  btn.textContent = 'Copy Chat';
  const source = `
var window = {};
function writeClipboardText() { return Promise.reject(new Error('clipboard denied')); }
function showToast(message, type) { globalThis.__toasts.push({message, type}); }
` + sourceBetween(viewerJs, 'function copyTextPayload', 'window.copyTextPayload = copyTextPayload;') + `
globalThis.__copyTextPayload = copyTextPayload;
`;
  const context = vm.createContext({
    __toasts: [],
    console: {error() {}},
    setTimeout(fn) { fn(); },
  });
  vm.runInContext(source, context);

  context.__copyTextPayload(btn, 'payload');
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(btn.textContent, 'Copy unavailable: clipboard denied');
  assert.equal(btn.title, 'Copy unavailable: clipboard denied');
  assert.equal(context.__toasts.at(-1).type, 'error');
});

test('viewer renders message copy failures as visible button state', async () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const message = new FakeElement('mc');
  message.className = 'mc';
  message.dataset.raw = 'raw message';
  message.textContent = 'rendered message';
  const parent = {
    querySelector(selector) {
      return selector === '.mc' ? message : null;
    },
  };
  const btn = new FakeElement('copyMsg');
  btn.textContent = 'Copy';
  btn.parentElement = parent;
  const source = `
var window = {};
function writeClipboardText() { return Promise.reject(new Error('clipboard denied')); }
function showToast(message, type) { globalThis.__toasts.push({message, type}); }
` + sourceBetween(viewerJs, 'function copyMsg', 'window.copyMsg = copyMsg;') + `
globalThis.__copyMsg = copyMsg;
`;
  const context = vm.createContext({
    __toasts: [],
    console: {error() {}},
    setTimeout(fn) { fn(); },
  });
  vm.runInContext(source, context);

  context.__copyMsg(btn);
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(btn.textContent, 'Copy unavailable: clipboard denied');
  assert.equal(btn.title, 'Copy unavailable: clipboard denied');
  assert.equal(context.__toasts.at(-1).type, 'error');
});

test('viewer renders unsupported attachment files as visible tray state', () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const attachmentTray = new FakeElement('attachmentTray');
  const input = {value: 'C:\\fakepath\\payload.exe'};
  const source = `
const state = {viewerAttachmentError: ''};
const attachmentTray = globalThis.__attachmentTray;
let viewerAttachments = [];
function showToast(message, type) { globalThis.__toasts.push({message, type}); }
` + sourceBetween(viewerJs, 'function addViewerAttachments', 'function formatAttachmentSize') + `
globalThis.__addViewerAttachments = addViewerAttachments;
globalThis.__state = state;
globalThis.__viewerAttachments = viewerAttachments;
`;
  const context = vm.createContext({
    __attachmentTray: attachmentTray,
    __toasts: [],
    document: {
      createElement() {
        return new FakeElement();
      },
    },
  });
  vm.runInContext(source, context);

  context.__addViewerAttachments([{name: 'payload.exe', type: 'application/x-msdownload', size: 123}], input);

  assert.equal(input.value, '');
  assert.equal(context.__viewerAttachments.length, 0);
  assert.match(attachmentTray.innerHTML, /Attachment unavailable: unsupported file type: payload\.exe/);
  assert.equal(context.__state.viewerAttachmentError, 'Attachment unavailable: unsupported file type: payload.exe');
  assert.equal(context.__toasts.at(-1).type, 'error');
});

test('viewer renders debug system fetch errors with response body', async () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new FakeElement(id));
    return elements.get(id);
  };
  const document = {
    getElementById: get,
    createElement() {
      return new FakeElement();
    },
  };
  const requested = [];
  const source = `
function esc(s) { return String(s || ''); }
const state = {debug: {
  gpu: {available: true, note: 'stale gpu ok'},
  audio: {stt_ok: true, tts_live_ok: true, tts_ready_ok: true},
  sttTrace: [],
  thinkTrace: [],
}};
` + sourceBetween(viewerJs, 'function renderDebugPanels', 'function trimTimelineNodes') + `
globalThis.__refreshDebugSystem = refreshDebugSystem;
globalThis.__state = state;
`;
  const context = vm.createContext({
    document,
    console: {error() {}},
    fetch(url) {
      requested.push(url);
      return Promise.resolve({
        ok: false,
        status: 503,
        text: () => Promise.resolve('debug system store unavailable'),
      });
    },
  });
  vm.runInContext(source, context);
  context.__refreshDebugSystem();
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(requested[0], '/viewer/debug/system');
  assert.match(get('debugGpuSummary').innerHTML, /HTTP 503: debug system store unavailable/);
  assert.doesNotMatch(get('debugGpuSummary').innerHTML, /stale gpu ok/);
  assert.doesNotMatch(get('debugGpuSummary').innerHTML, />fetch failed</);
  assert.equal(context.__state.debug.audio, null);
});

test('viewer renders revenue fetch errors as visible external send audit state', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function escAttr(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function ftime(s) { return String(s || '-'); }
function stateClass(s) { return String(s || ''); }
function sandboxField(obj, snake, pascal) {
  if (!obj) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, snake)) return obj[snake];
  if (Object.prototype.hasOwnProperty.call(obj, pascal)) return obj[pascal];
  return undefined;
}
const state = {ops: {
  revenueFetchError: 'HTTP 500: revenue store unavailable',
  revenueExternalSendApplyRecords: [{apply_id: 'stale_apply', apply_status: 'sent', external_send_applied: true}],
  revenueChannelDrafts: [{draft_id: 'stale_draft', external_send_applied: true}],
  revenueSummary: {total_revenue_amount: 999},
}};
` + sourceBetween(opsJs, 'function revenueOpsCard', 'async function reviewRevenueHumanDecision') + `
globalThis.__card = revenueOpsCard();
renderRevenueDrilldown();
renderRevenueChannelDrafts();
renderRevenueExternalSendAudits();
globalThis.__drilldown = document.getElementById('revenueDrilldownResult').textContent;
globalThis.__channelDraftResult = document.getElementById('revenueChannelDraftResult').textContent;
globalThis.__externalSendAudits = document.getElementById('revenueExternalSendAuditBody').innerHTML;
globalThis.__externalSendAuditResult = document.getElementById('revenueExternalSendAuditResult').textContent;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.equal(context.__card.big, 'unavailable');
  assert.match(context.__card.sub, /revenue status unavailable: HTTP 500: revenue store unavailable/);
  assert.match(context.__card.sub, /blocked: external send audit state unreadable/);
  assert.match(context.__drilldown, /Revenue Drilldown unavailable/);
  assert.match(context.__channelDraftResult, /revenue channel drafts unavailable: HTTP 500: revenue store unavailable/);
  assert.match(context.__externalSendAudits, /Revenue external send apply audits unavailable: HTTP 500: revenue store unavailable/);
  assert.doesNotMatch(context.__externalSendAudits, /stale_apply/);
  assert.match(context.__externalSendAuditResult, /revenue external send apply audits unavailable: HTTP 500: revenue store unavailable/);
  assert.match(context.__externalSendAuditResult, /blocked: external send audit state unreadable/);
});

test('viewer renders persona observation fetch errors as visible meta review state', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function escAttr(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function ftime(s) { return String(s || '-'); }
function stateClass(s) { return String(s || ''); }
function sandboxField(obj, snake, pascal) {
  if (!obj) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, snake)) return obj[snake];
  if (Object.prototype.hasOwnProperty.call(obj, pascal)) return obj[pascal];
  return undefined;
}
const state = {ops: {
  personaObservationFetchError: 'HTTP 500: persona observation store unavailable',
  personaObservationLogs: [{observation_id: 'stale_observation', review_status: 'approved'}],
  personaMetaProfileUpdates: [{update_id: 'stale_meta', review_status: 'approved', proposed_content: 'stale approved update'}],
  personaMetaReviewResult: {status: 'approved', update_id: 'stale_meta'},
}};
` + sourceBetween(opsJs, 'function personaObservationOpsCard', 'function browserTraceAPIOpsCard') + `
globalThis.__card = personaObservationOpsCard();
renderPersonaMetaReviews();
globalThis.__personaMetaReviews = document.getElementById('personaMetaReviewBody').innerHTML;
globalThis.__personaMetaReviewResult = document.getElementById('personaMetaReviewResult').textContent;
`;
  const context = vm.createContext({document, encodeURIComponent, decodeURIComponent, JSON});
  vm.runInContext(source, context);

  assert.equal(context.__card.big, 'unavailable');
  assert.match(context.__card.sub, /persona observation status unavailable: HTTP 500: persona observation store unavailable/);
  assert.match(context.__card.sub, /blocked: persona meta review state unreadable/);
  assert.match(context.__card.sub, /blocked: long-term personality update state unreadable/);
  assert.match(context.__personaMetaReviews, /Persona meta reviews unavailable: HTTP 500: persona observation store unavailable/);
  assert.doesNotMatch(context.__personaMetaReviews, /stale_meta/);
  assert.doesNotMatch(context.__personaMetaReviews, /stale approved update/);
  assert.match(context.__personaMetaReviewResult, /persona meta review unavailable: HTTP 500: persona observation store unavailable/);
  assert.match(context.__personaMetaReviewResult, /blocked: persona meta review state unreadable/);
});

test('viewer renders runtime blocked route audit details', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function stateClass(s) { return String(s || ''); }
function sandboxField(obj, snake, pascal) {
  if (!obj) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, snake)) return obj[snake];
  if (Object.prototype.hasOwnProperty.call(obj, pascal)) return obj[pascal];
  return undefined;
}
const state = {ops: {
  knowledgePersonalArchive: [],
  knowledgeCreativeItems: [],
  knowledgeNewsItems: [],
  knowledgeDailyIntakeRules: [],
  knowledgeTemporalMarkers: [],
  knowledgeDreamRuns: [],
  runtimeBlockedRoutes: [
    {label: 'Source Registry staging', path: '/viewer/source-registry?action=staging&limit=3', status: 503, ok: false, body: 'source registry unavailable'},
    {label: 'Memory Layers', path: '/viewer/memory/layers', status: 503, ok: false, body: 'memory layers unavailable'},
    {label: 'Sandbox status', path: '/viewer/sandbox?limit=1&viewer_optional=1', status: 503, ok: false, body: 'sandbox store unavailable'},
    {label: 'LLM Ops status', path: '/viewer/llm-ops/status', status: 502, ok: false, body: 'upstream unreachable'},
  ],
}};
` + sourceBetween(opsJs, 'function knowledgeMemoryOpsCard', 'function renderKnowledgeMemoryDetailFocus') + `
const card = runtimeBlockedRoutesOpsCard();
renderRuntimeBlockedRouteAudits();
globalThis.__card = card.sub;
globalThis.__body = document.getElementById('runtimeBlockedRouteAuditBody').innerHTML;
globalThis.__result = document.getElementById('runtimeBlockedRouteAuditResult').textContent;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.match(context.__card, /503 unavailable: 3/);
  assert.match(context.__card, /blocked: dependency unavailable/);
  assert.match(context.__body, /source registry unavailable/);
  assert.match(context.__body, /memory layers unavailable/);
  assert.match(context.__body, /sandbox store unavailable/);
  assert.match(context.__body, /upstream unreachable/);
  assert.match(context.__result, /4 checked \/ 4 blocked \/ 3 unavailable \/ 0 available/);
  assert.match(context.__result, /blocked: Source Registry staging, Memory Layers, Sandbox, and LLM Ops require their runtime dependencies/);
});

test('viewer renders knowledge memory detail fetch errors as visible state', async () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function sandboxField(obj, snake, pascal) {
  if (!obj) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, snake)) return obj[snake];
  if (Object.prototype.hasOwnProperty.call(obj, pascal)) return obj[pascal];
  return undefined;
}
const state = {ops: {
  knowledgePersonalArchive: [],
  knowledgeCreativeItems: [],
  knowledgeNewsItems: [{item_id: 'news_1', title: 'visible news candidate'}],
  knowledgeDailyIntakeRules: [],
  knowledgeTemporalMarkers: [],
  knowledgeDreamRuns: [],
  knowledgeMemoryDetail: {detail_type: 'news_knowledge', id: 'stale_news', item: {title: 'stale detail'}},
}};
function renderOps() {
  const body = document.getElementById('opsFocusBody');
  body.innerHTML = '';
  renderKnowledgeMemoryDetailFocus(body);
}
` + sourceBetween(opsJs, 'function renderKnowledgeMemoryDetailFocus', 'let dciSearchBound') + `
renderOps();
globalThis.__getKnowledgeMemoryDetail = () => state.ops.knowledgeMemoryDetail;
`;
  const context = vm.createContext({
    document,
    encodeURIComponent,
    fetch: async () => ({
      ok: false,
      status: 500,
      statusText: 'Internal Server Error',
      text: async () => 'knowledge memory detail store unavailable',
    }),
    console: {error() {}},
  });
  vm.runInContext(source, context);
  context.fetchKnowledgeMemoryDetail('news_knowledge', 'news_1');
  await new Promise((resolve) => setImmediate(resolve));

  const body = document.getElementById('opsFocusBody').innerHTML;
  assert.match(body, /HTTP 500: knowledge memory detail store unavailable/);
  assert.match(body, /news_knowledge/);
  assert.match(body, /news_1/);
  assert.doesNotMatch(body, /stale_news/);
  assert.doesNotMatch(body, /stale detail/);
  assert.equal(context.__getKnowledgeMemoryDetail().error, 'HTTP 500: knowledge memory detail store unavailable');
});

test('viewer renders heavy runtime llm-ops unavailable diagnostics', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const source = `
function sandboxField(obj, snake, pascal) {
  if (!obj) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, snake)) return obj[snake];
  if (Object.prototype.hasOwnProperty.call(obj, pascal)) return obj[pascal];
  return undefined;
}
function replaceURLPort(raw, port) { return String(raw || '').replace(/:[0-9]+$/, ':' + String(port)); }
const state = {ops: {
  heavyWorkerRuntimeDiagnostics: {
    role: 'Heavy',
    route: 'ANALYZE',
    route_prefix: '/analyze',
    provider: 'local_openai',
    configured: true,
    base_url: 'http://192.168.1.13:8082',
    model: 'Worker',
    failure_is_error: true,
    llm_ops: {
      configured: true,
      enabled: true,
      live_available: false,
      error: 'llm-ops GET /v1/status: context deadline exceeded',
    },
  },
}};
` + sourceBetween(opsJs, 'function heavyWorkerRuntimeOpsCard', 'function knowledgeMemoryOpsCard') + `
globalThis.__card = heavyWorkerRuntimeOpsCard();
`;
  const context = vm.createContext({});
  vm.runInContext(source, context);

  assert.equal(context.__card.title, 'Heavy Runtime');
  assert.equal(context.__card.big, 'config');
  assert.match(context.__card.sub, /route: ANALYZE \/analyze/);
  assert.match(context.__card.sub, /Worker/);
  assert.match(context.__card.sub, /http:\/\/192\.168\.1\.13:8082/);
  assert.match(context.__card.sub, /llm-ops GET \/v1\/status/);
});

test('viewer renders heavy runtime diagnostics fetch errors as blocked state', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const source = `
function sandboxField(obj, snake, pascal) {
  if (!obj) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, snake)) return obj[snake];
  if (Object.prototype.hasOwnProperty.call(obj, pascal)) return obj[pascal];
  return undefined;
}
function replaceURLPort(raw, port) { return String(raw || '').replace(/:[0-9]+$/, ':' + String(port)); }
const state = {ops: {
  heavyWorkerRuntimeDiagnosticsFetchError: 'HTTP 500: heavy runtime store unavailable',
  heavyWorkerRuntimeDiagnostics: {
    role: 'Heavy',
    route: 'ANALYZE',
    base_url: 'http://stale.example:8082',
    model: 'stale-model',
    llm_ops: {live_available: true},
  },
}};
` + sourceBetween(opsJs, 'function heavyWorkerRuntimeOpsCard', 'function knowledgeMemoryOpsCard') + `
globalThis.__card = heavyWorkerRuntimeOpsCard();
`;
  const context = vm.createContext({});
  vm.runInContext(source, context);

  assert.equal(context.__card.title, 'Heavy Runtime');
  assert.equal(context.__card.big, 'unavailable');
  assert.match(context.__card.sub, /heavy runtime diagnostics unavailable: HTTP 500: heavy runtime store unavailable/);
  assert.match(context.__card.sub, /blocked: RouteANALYZE provider state unreadable/);
  assert.match(context.__card.sub, /blocked: LLM Ops live state unreadable/);
  assert.doesNotMatch(context.__card.sub, /stale-model/);
  assert.doesNotMatch(context.__card.sub, /stale\.example/);
});

test('viewer renders skill external PR blocked audit details', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function ftime(s) { return String(s || '-'); }
function stateClass(s) { return String(s || ''); }
function sandboxField(obj, snake, pascal) {
  if (!obj) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, snake)) return obj[snake];
  if (Object.prototype.hasOwnProperty.call(obj, pascal)) return obj[pascal];
  return undefined;
}
const state = {ops: {
  skillExternalPRAdapter: 'unconfigured',
  skillExternalPRAdapterConfigured: false,
  skillExternalPRHumanApprovalRequired: true,
  skillExternalPRSubmitRecords: [{
    submit_id: 'submit_1',
    contribution_event_id: 'gate_1',
    repo: 'owner/repo',
    target_branch: 'feature/test',
    submit_status: 'blocked',
    failure_reason: 'external PR adapter is not configured',
    pr_adapter: 'unconfigured',
    external_pr_created: false,
    post_submit_verified: false,
  }],
}};
` + sourceBetween(opsJs, 'function renderSkillExternalPRAudits', 'function workstreamOpsCard') + `
renderSkillExternalPRAudits();
globalThis.__skillPRAudits = document.getElementById('skillExternalPRAuditBody').innerHTML;
globalThis.__skillPRAuditResult = document.getElementById('skillExternalPRAuditResult').textContent;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.match(context.__skillPRAudits, /submit_1/);
  assert.match(context.__skillPRAudits, /gate_1/);
  assert.match(context.__skillPRAudits, /owner\/repo/);
  assert.match(context.__skillPRAudits, /blocked/);
  assert.match(context.__skillPRAudits, /unconfigured/);
  assert.match(context.__skillPRAudits, /not created/);
  assert.match(context.__skillPRAuditResult, /1 total \/ 1 blocked \/ 0 created \/ 1 not created \/ 0 verified/);
  assert.match(context.__skillPRAuditResult, /external PR adapter: unconfigured \/ configured: no \/ human approval: required/);
  assert.match(context.__skillPRAuditResult, /blocked: no external PR created/);
});

test('viewer renders skill governance fetch errors as visible PR audit state', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function ftime(s) { return String(s || '-'); }
function stateClass(s) { return String(s || ''); }
function sandboxField(obj, snake, pascal) {
  if (!obj) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, snake)) return obj[snake];
  if (Object.prototype.hasOwnProperty.call(obj, pascal)) return obj[pascal];
  return undefined;
}
const state = {ops: {
  skillGovernanceFetchError: 'HTTP 500: skill governance store unavailable',
  skillExternalPRSubmitRecords: [{submit_id: 'stale_submit', submit_status: 'created', external_pr_created: true}],
  skillTriggerLogs: [{event_id: 'stale_trigger', status: 'triggered'}],
  contributionGateLogs: [],
  coderTranscripts: [],
}};
` + sourceBetween(opsJs, 'function skillGovernanceOpsCard', 'function workstreamOpsCard') + `
globalThis.__card = skillGovernanceOpsCard();
renderSkillExternalPRAudits();
renderSkillEvidenceAudits();
globalThis.__skillPRAudits = document.getElementById('skillExternalPRAuditBody').innerHTML;
globalThis.__skillPRAuditResult = document.getElementById('skillExternalPRAuditResult').textContent;
globalThis.__skillEvidence = document.getElementById('skillEvidenceAuditBody').innerHTML;
globalThis.__skillEvidenceResult = document.getElementById('skillEvidenceAuditResult').textContent;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.equal(context.__card.big, 'unavailable');
  assert.match(context.__card.sub, /skill governance status unavailable: HTTP 500: skill governance store unavailable/);
  assert.match(context.__card.sub, /blocked: external PR audit state unreadable/);
  assert.match(context.__skillPRAudits, /Skill external PR submit audits unavailable: HTTP 500: skill governance store unavailable/);
  assert.doesNotMatch(context.__skillPRAudits, /stale_submit/);
  assert.match(context.__skillPRAuditResult, /skill external PR submit audits unavailable: HTTP 500: skill governance store unavailable/);
  assert.match(context.__skillPRAuditResult, /blocked: external PR audit state unreadable/);
  assert.match(context.__skillEvidence, /Skill evidence audits unavailable: HTTP 500: skill governance store unavailable/);
  assert.doesNotMatch(context.__skillEvidence, /stale_trigger/);
  assert.match(context.__skillEvidenceResult, /blocked: coder evidence state unreadable/);
});

test('viewer renders skill evidence audit transcript boundary', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function short(s) { return String(s || ''); }
function ftime(s) { return String(s || '-'); }
function stateClass(s) { return String(s || ''); }
function sandboxField(obj, snake, pascal) {
  if (!obj) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, snake)) return obj[snake];
  if (Object.prototype.hasOwnProperty.call(obj, pascal)) return obj[pascal];
  return undefined;
}
const state = {ops: {
  skillTriggerLogs: [{event_id: 'evt_trigger_1', skill_id: 'core.skill', status: 'triggered', trigger_reason: 'complexity_hotspot_scan'}],
  contributionGateLogs: [{event_id: 'evt_contrib_1', repo: 'example/repo', gate_status: 'passed', diff_human_approved: true, test_result: 'go test ./...'}],
  coderTranscripts: [],
}};
` + sourceBetween(opsJs, 'function renderSkillEvidenceAudits', 'function renderSuperAgentTerminalAudits') + `
renderSkillEvidenceAudits();
globalThis.__skillEvidence = document.getElementById('skillEvidenceAuditBody').innerHTML;
globalThis.__skillEvidenceResult = document.getElementById('skillEvidenceAuditResult').textContent;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.match(context.__skillEvidence, /evt_trigger_1/);
  assert.match(context.__skillEvidence, /evt_contrib_1/);
  assert.match(context.__skillEvidence, /complexity_hotspot_scan/);
  assert.match(context.__skillEvidence, /go test \.\/\.\.\./);
  assert.match(context.__skillEvidenceResult, /1 triggers \/ 1 triggered \/ 1 contribution gates \/ 1 passed \/ 0 coder transcripts \/ 0 with diff\+transcript evidence/);
  assert.match(context.__skillEvidenceResult, /blocked: coder evidence transcript not observed/);
  assert.match(context.__skillEvidenceResult, /blocked: passed contribution gate is not external PR evidence/);
});

test('viewer counts coder transcript evidence path pairs by job', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function short(s) { return String(s || ''); }
function ftime(s) { return String(s || '-'); }
function stateClass(s) { return String(s || ''); }
function sandboxField(obj, snake, pascal) {
  if (!obj) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, snake)) return obj[snake];
  if (Object.prototype.hasOwnProperty.call(obj, pascal)) return obj[pascal];
  return undefined;
}
const state = {ops: {
  skillTriggerLogs: [],
  contributionGateLogs: [],
  coderTranscripts: [
    {event_id: 'evt_patch', job_id: 'job_coder_1', role: 'coder', segment: 'patch_evidence', evidence_path: 'workspace/logs/skill_governance/coder_evidence/job_coder_1/skill_diff.md'},
    {event_id: 'evt_transcript', job_id: 'job_coder_1', role: 'system', segment: 'transcript_evidence', evidence_path: 'workspace/logs/skill_governance/coder_evidence/job_coder_1/agent_transcript.md'},
  ],
}};
` + sourceBetween(opsJs, 'function renderSkillEvidenceAudits', 'function renderSuperAgentTerminalAudits') + `
renderSkillEvidenceAudits();
globalThis.__skillEvidence = document.getElementById('skillEvidenceAuditBody').innerHTML;
globalThis.__skillEvidenceResult = document.getElementById('skillEvidenceAuditResult').textContent;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.match(context.__skillEvidence, /evt_patch/);
  assert.match(context.__skillEvidence, /skill_diff\.md/);
  assert.match(context.__skillEvidence, /agent_transcript\.md/);
  assert.match(context.__skillEvidenceResult, /2 coder transcripts \/ 1 with diff\+transcript evidence/);
  assert.doesNotMatch(context.__skillEvidenceResult, /blocked: coder evidence transcript not observed/);
});

test('viewer renders superagent terminal failure evidence', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function short(s) { return String(s || ''); }
function ftime(s) { return String(s || '-'); }
function stateClass(s) { return String(s || ''); }
function sandboxField(obj, snake, pascal) {
  if (!obj) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, snake)) return obj[snake];
  if (Object.prototype.hasOwnProperty.call(obj, pascal)) return obj[pascal];
  return undefined;
}
const state = {ops: {
  superAgentRuns: [{
    run_id: 'run_1',
    status: 'failed',
    completed_at: '2026-05-19T21:16:26Z',
    summary: 'local LLM connection refused',
  }],
  superAgentRunQueue: [{
    queue_id: 'rq_1',
    run_id: 'run_1',
    status: 'failed',
    completed_at: '2026-05-19T21:16:26Z',
    reason: 'scheduler processor failed',
  }],
}};
` + sourceBetween(opsJs, 'function renderSuperAgentTerminalAudits', 'function workstreamOpsCard') + `
renderSuperAgentTerminalAudits();
globalThis.__superAgentTerminal = document.getElementById('superAgentTerminalAuditBody').innerHTML;
globalThis.__superAgentTerminalResult = document.getElementById('superAgentTerminalAuditResult').textContent;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.match(context.__superAgentTerminal, /agent_run/);
  assert.match(context.__superAgentTerminal, /run_queue/);
  assert.match(context.__superAgentTerminal, /run_1/);
  assert.match(context.__superAgentTerminal, /rq_1/);
  assert.match(context.__superAgentTerminal, /local LLM connection refused/);
  assert.match(context.__superAgentTerminal, /scheduler processor failed/);
  assert.match(context.__superAgentTerminalResult, /1 terminal runs \/ 1 terminal queue \/ 1 failed runs \/ 1 failed queue \/ missing evidence: 0/);
});

test('viewer renders superagent fetch errors as visible scheduler audit state', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function short(s) { return String(s || ''); }
function ftime(s) { return String(s || '-'); }
function stateClass(s) { return String(s || ''); }
function sandboxField(obj, snake, pascal) {
  if (!obj) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, snake)) return obj[snake];
  if (Object.prototype.hasOwnProperty.call(obj, pascal)) return obj[pascal];
  return undefined;
}
const state = {ops: {
  superAgentFetchError: 'HTTP 500: superagent store unavailable',
  superAgentRuns: [{run_id: 'stale_run', status: 'completed', summary: 'stale success'}],
  superAgentRunQueue: [{queue_id: 'stale_queue', action: 'resume', status: 'completed'}],
  superAgentSubagentTasks: [],
  superAgentContextPacks: [],
  superAgentMessageChannels: [],
  superAgentTraceEvents: [],
  superAgentRuntimeConfig: {},
}};
` + sourceBetween(opsJs, 'function renderSuperAgentTerminalAudits', 'function workstreamOpsCard') + `
` + sourceBetween(opsJs, 'function superAgentOpsCard', 'function aiWorkflowOpsCard') + `
globalThis.__card = superAgentOpsCard();
renderSuperAgentTerminalAudits();
renderSuperAgentResumeAudits();
globalThis.__superAgentTerminal = document.getElementById('superAgentTerminalAuditBody').innerHTML;
globalThis.__superAgentTerminalResult = document.getElementById('superAgentTerminalAuditResult').textContent;
globalThis.__superAgentResume = document.getElementById('superAgentResumeAuditBody').innerHTML;
globalThis.__superAgentResumeResult = document.getElementById('superAgentResumeAuditResult').textContent;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.equal(context.__card.big, 'unavailable');
  assert.match(context.__card.sub, /superagent status unavailable: HTTP 500: superagent store unavailable/);
  assert.match(context.__card.sub, /blocked: scheduler terminal state unreadable/);
  assert.match(context.__card.sub, /blocked: true long-running resume state unreadable/);
  assert.match(context.__superAgentTerminal, /SuperAgent terminal audits unavailable: HTTP 500: superagent store unavailable/);
  assert.doesNotMatch(context.__superAgentTerminal, /stale_run/);
  assert.match(context.__superAgentTerminalResult, /superagent terminal audits unavailable: HTTP 500: superagent store unavailable/);
  assert.match(context.__superAgentTerminalResult, /blocked: scheduler terminal state unreadable/);
  assert.match(context.__superAgentResume, /SuperAgent resume audits unavailable: HTTP 500: superagent store unavailable/);
  assert.doesNotMatch(context.__superAgentResume, /stale_queue/);
  assert.match(context.__superAgentResumeResult, /blocked: true long-running resume state unreadable/);
});

test('viewer renders superagent resume manual ledger boundary', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function short(s) { return String(s || ''); }
function ftime(s) { return String(s || '-'); }
function stateClass(s) { return String(s || ''); }
function sandboxField(obj, snake, pascal) {
  if (!obj) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, snake)) return obj[snake];
  if (Object.prototype.hasOwnProperty.call(obj, pascal)) return obj[pascal];
  return undefined;
}
const state = {ops: {
  superAgentRunQueue: [{
    queue_id: 'rq_resume_1',
    run_id: 'run_resume_1',
    action: 'resume',
    status: 'completed',
    completed_at: '2026-05-19T21:16:26Z',
    reason: 'pause/resume queue reentry ledger E2E completed without scheduler execution',
  }],
  superAgentTraceEvents: [
    {run_id: 'run_resume_1', event_type: 'lead_agent_paused', payload_summary: 'lead_agent_paused runtime_control=none'},
    {run_id: 'run_resume_1', event_type: 'lead_agent_resumed', payload_summary: 'lead_agent_resumed runtime_control=none'},
  ],
}};
` + sourceBetween(opsJs, 'function renderSuperAgentResumeAudits', 'function renderAIWorkflowRunEvidence') + `
renderSuperAgentResumeAudits();
globalThis.__superAgentResume = document.getElementById('superAgentResumeAuditBody').innerHTML;
globalThis.__superAgentResumeResult = document.getElementById('superAgentResumeAuditResult').textContent;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.match(context.__superAgentResume, /rq_resume_1/);
  assert.match(context.__superAgentResume, /run_resume_1/);
  assert.match(context.__superAgentResume, /paused:1 resumed:1/);
  assert.match(context.__superAgentResume, /runtime-control:none/);
  assert.match(context.__superAgentResume, /manual-ledger only/);
  assert.match(context.__superAgentResume, /runtime control not applied/);
  assert.match(context.__superAgentResumeResult, /1 resume queue \/ 1 completed \/ 1 manual-ledger \/ 1 pause-resume trace \/ 0 runtime-control applied/);
  assert.match(context.__superAgentResumeResult, /blocked: true long-running resume not verified/);
});

test('viewer renders ai workflow same-run evidence boundary', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function short(s) { return String(s || ''); }
function stateClass(s) { return String(s || ''); }
function sandboxField(obj, snake, pascal) {
  if (!obj) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, snake)) return obj[snake];
  if (Object.prototype.hasOwnProperty.call(obj, pascal)) return obj[pascal];
  return undefined;
}
const state = {ops: {
  aiWorkflowEvents: [{event_id: 'evt_cmd_1', run_id: 'run_1', workstream_id: 'ws_1', event_type: 'command_invoked'}],
  aiWorkflowContextUsages: [{event_id: 'ctx_1', run_id: 'run_1', workstream_id: 'ws_1', context_tokens: 100}],
  superAgentTraceEvents: [{event_id: 'trace_1', run_id: 'run_1', event_type: 'lead_agent_started'}],
}};
` + sourceBetween(opsJs, 'function renderAIWorkflowRunEvidence', 'function workstreamOpsCard') + `
renderAIWorkflowRunEvidence();
globalThis.__aiWorkflowEvidence = document.getElementById('aiWorkflowRunEvidenceBody').innerHTML;
globalThis.__aiWorkflowEvidenceResult = document.getElementById('aiWorkflowRunEvidenceResult').textContent;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.match(context.__aiWorkflowEvidence, /run_1/);
  assert.match(context.__aiWorkflowEvidence, /ws_1/);
  assert.match(context.__aiWorkflowEvidence, /same-run evidence/);
  assert.match(context.__aiWorkflowEvidenceResult, /1 runs \/ 1 command-context-trace same-run \/ 0 partial/);
  assert.match(context.__aiWorkflowEvidenceResult, /blocked: scheduler normal completion not verified/);
});

test('viewer renders ai workflow fetch errors as visible same-run evidence state', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function short(s) { return String(s || ''); }
function stateClass(s) { return String(s || ''); }
function sandboxField(obj, snake, pascal) {
  if (!obj) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, snake)) return obj[snake];
  if (Object.prototype.hasOwnProperty.call(obj, pascal)) return obj[pascal];
  return undefined;
}
const state = {ops: {
  aiWorkflowFetchError: 'HTTP 500: ai workflow store unavailable',
  aiWorkflowEvents: [{event_id: 'stale_cmd', run_id: 'stale_run', event_type: 'command_invoked'}],
  aiWorkflowContextUsages: [{event_id: 'stale_ctx', run_id: 'stale_run'}],
  aiWorkflowProjectMemoryIndexes: [],
  aiWorkflowWorktreeRegistries: [],
  aiWorkflowCommandRegistries: [],
  aiWorkflowContextBudgetPolicy: {},
  superAgentTraceEvents: [{event_id: 'stale_trace', run_id: 'stale_run'}],
}};
` + sourceBetween(opsJs, 'function renderAIWorkflowRunEvidence', 'function renderComplexityReviewArtifacts') + `
` + sourceBetween(opsJs, 'function aiWorkflowOpsCard', 'function heavyWorkerRuntimeOpsCard') + `
globalThis.__card = aiWorkflowOpsCard();
renderAIWorkflowRunEvidence();
globalThis.__aiWorkflowEvidence = document.getElementById('aiWorkflowRunEvidenceBody').innerHTML;
globalThis.__aiWorkflowEvidenceResult = document.getElementById('aiWorkflowRunEvidenceResult').textContent;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.equal(context.__card.big, 'unavailable');
  assert.match(context.__card.sub, /ai workflow status unavailable: HTTP 500: ai workflow store unavailable/);
  assert.match(context.__card.sub, /blocked: scheduler normal completion state unreadable/);
  assert.match(context.__aiWorkflowEvidence, /AI Workflow run evidence unavailable: HTTP 500: ai workflow store unavailable/);
  assert.doesNotMatch(context.__aiWorkflowEvidence, /stale_run/);
  assert.match(context.__aiWorkflowEvidenceResult, /ai workflow run evidence unavailable: HTTP 500: ai workflow store unavailable/);
  assert.match(context.__aiWorkflowEvidenceResult, /blocked: scheduler normal completion state unreadable/);
});

test('viewer renders sandbox promotion gate log details', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function escAttr(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function ftime(s) { return String(s || '-'); }
function stateClass(s) { return String(s || ''); }
function sandboxField(obj, snake, pascal) {
  if (!obj) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, snake)) return obj[snake];
  if (Object.prototype.hasOwnProperty.call(obj, pascal)) return obj[pascal];
  return undefined;
}
const state = {ops: {
  sandboxGateLogs: [{
    event_id: 'evt_gate_1',
    promotion_id: 'promo_1',
    gate_status: 'needs_review',
    human_approval_status: 'pending',
    reason: 'human approval missing',
  }],
}};
` + sourceBetween(opsJs, 'function renderSandboxGateLogs', 'function formatSandboxPromotionDiffPreview') + `
renderSandboxGateLogs();
globalThis.__sandboxGateLogs = document.getElementById('sandboxGateLogBody').innerHTML;
globalThis.__sandboxGateLogResult = document.getElementById('sandboxGateLogResult').textContent;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.match(context.__sandboxGateLogs, /evt_gate_1/);
  assert.match(context.__sandboxGateLogs, /promo_1/);
  assert.match(context.__sandboxGateLogs, /needs_review/);
  assert.match(context.__sandboxGateLogs, /pending/);
  assert.match(context.__sandboxGateLogs, /human approval missing/);
  assert.match(context.__sandboxGateLogResult, /1 total \/ 1 needs-review \/ 0 applied \/ 0 rollback \/ 0 post-apply evidence/);
  assert.match(context.__sandboxGateLogResult, /formal apply requires human approval/);
  assert.match(context.__sandboxGateLogResult, /blocked: no promotion applied/);
});

test('viewer renders sandbox fetch errors as visible promotion apply state', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function escAttr(s) { return String(s || ''); }
function short(s, n) { const v = String(s || ''); return v.length > n ? v.slice(0, n) + '...' : v; }
function ftime(s) { return String(s || '-'); }
function stateClass(s) { return String(s || ''); }
const state = {ops: {
  sandboxFetchError: 'HTTP 500: sandbox store unavailable',
  sandboxes: [{sandbox_id: 'stale_sandbox'}],
  sandboxArtifacts: [],
  sandboxPromotions: [{promotion_id: 'stale_promotion'}],
  sandboxDecisions: [],
  sandboxGateLogs: [{event_id: 'stale_gate', gate_status: 'promotion_applied'}],
}};
` + sourceBetween(opsJs, 'function sandboxField', 'function skillGovernanceOpsCard') + `
globalThis.__card = sandboxOpsCard();
renderSandboxStatus();
globalThis.__sandboxStatus = document.getElementById('sandboxBody').innerHTML;
globalThis.__sandboxPreviewResult = document.getElementById('sandboxPromotionPreviewResult').textContent;
globalThis.__sandboxGateLogs = document.getElementById('sandboxGateLogBody').innerHTML;
globalThis.__sandboxGateLogResult = document.getElementById('sandboxGateLogResult').textContent;
`;
  const context = vm.createContext({document, encodeURIComponent, JSON});
  vm.runInContext(source, context);

  assert.equal(context.__card.big, 'unavailable');
  assert.match(context.__card.sub, /sandbox status unavailable: HTTP 500: sandbox store unavailable/);
  assert.match(context.__card.sub, /blocked: promotion apply state unreadable/);
  assert.match(context.__sandboxStatus, /Sandbox status unavailable: HTTP 500: sandbox store unavailable/);
  assert.doesNotMatch(context.__sandboxStatus, /stale_promotion/);
  assert.match(context.__sandboxPreviewResult, /sandbox promotion diff preview unavailable: HTTP 500: sandbox store unavailable/);
  assert.match(context.__sandboxGateLogs, /Sandbox gate logs unavailable: HTTP 500: sandbox store unavailable/);
  assert.doesNotMatch(context.__sandboxGateLogs, /stale_gate/);
  assert.match(context.__sandboxGateLogResult, /sandbox promotion gate logs unavailable: HTTP 500: sandbox store unavailable/);
  assert.match(context.__sandboxGateLogResult, /blocked: promotion apply state unreadable/);
});

test('viewer keeps sandbox unavailable in panel diagnostics without console error', async () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const state = {ops: {
    sandboxFetchError: '',
    sandboxes: [{sandbox_id: 'stale_sandbox'}],
    sandboxArtifacts: [{artifact_id: 'stale_artifact'}],
    sandboxPromotions: [{promotion_id: 'stale_promotion'}],
    sandboxDecisions: [{promotion_id: 'stale_decision'}],
    sandboxGateLogs: [{event_id: 'stale_gate'}],
  }};
  let renderOpsCount = 0;
  let renderSandboxStatusCount = 0;
  const consoleErrors = [];
  const source = `
` + sourceBetween(viewerJs, 'function refreshSandboxData', 'function refreshSkillGovernanceData') + `
globalThis.__refreshSandboxData = refreshSandboxData;
`;
  const context = vm.createContext({
    state,
    fetch(url) {
      assert.equal(String(url), '/viewer/sandbox?limit=20&viewer_optional=1');
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({ok: false, status: 503, error: 'sandbox store unavailable'}),
      });
    },
    renderOps() { renderOpsCount += 1; },
    renderSandboxStatus() { renderSandboxStatusCount += 1; },
    console: {error(err) { consoleErrors.push(err); }},
  });

  vm.runInContext(source, context);
  await context.__refreshSandboxData();
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(state.ops.sandboxFetchError, 'HTTP 503: sandbox store unavailable');
  assert.equal(state.ops.sandboxes.length, 0);
  assert.equal(state.ops.sandboxArtifacts.length, 0);
  assert.equal(state.ops.sandboxPromotions.length, 0);
  assert.equal(state.ops.sandboxDecisions.length, 0);
  assert.equal(state.ops.sandboxGateLogs.length, 0);
  assert.equal(renderOpsCount, 1);
  assert.equal(renderSandboxStatusCount, 1);
  assert.deepEqual(consoleErrors, []);
});

test('viewer runtime cards prefer live llm ops status over local config labels', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function escAttr(s) { return String(s || ''); }
function stateClass(s) { return 'state-' + s; }
var state = {
  ops: {
    localLLM: {
      enabled: true,
      provider: 'local_openai',
      chat_base_url: 'http://192.168.1.31:8081',
      worker_base_url: 'http://192.168.1.31:8082',
      heavy_base_url: 'http://192.168.1.31:8082',
      wild_base_url: 'http://192.168.1.31:8081',
      chat_model: 'Chat',
      worker_model: 'Worker',
      heavy_model: 'Worker',
      wild_model: 'Chat',
    },
    llmOpsConfigured: true,
    llmOpsEnabled: true,
    llmOpsBaseURL: 'http://192.168.1.13:8079',
    runtimeReadiness: {
      slack_credentials_present: false,
      slack_webhook_registered: false,
      slack_file_payload_pipeline: false,
      discord_credentials_present: false,
      discord_webhook_registered: false,
      discord_file_payload_pipeline: false,
      telegram_credentials_present: false,
      telegram_webhook_registered: false,
      telegram_file_payload_pipeline: false,
      stt_gateway_env_present: true,
      stt_gateway_config_present: true,
      tts_provider_env_present: false,
      tts_provider_config_present: true,
      distributed_enabled: false,
      distributed_transports_present: false,
      distributed_ssh_configured: false,
      distributed_ssh_connected: false,
      distributed_local_transport: false,
      conversation_enabled: false,
      l1_sqlite_config_present: false,
      memory_layers_available: false,
      memory_layers_status_available: true,
      source_registry_available: false,
      source_registry_status_available: true,
      knowledge_memory_enabled: true,
      knowledge_memory_status_available: true,
      browser_trace_api_enabled: true,
      browser_trace_api_status_available: true,
      browser_trace_api_fetcher_available: true,
      sandbox_enabled: false,
      sandbox_status_available: true,
    },
    runtimeSTTBaseURL: 'http://192.168.1.33:8766',
    runtimeSTTStreamURL: 'wss://fujitsu-ubunts.tailb07d8d.ts.net/stt',
    runtimeTTSBaseURL: 'http://192.168.1.13:7870',
    runtimeHealth: {
      status: 'down',
      checks: [
        {name: 'local_llm_chat', status: 'down', message: 'connection refused'},
        {name: 'local_llm_worker', status: 'down', message: 'connection refused'},
      ],
      timestamp: '2026-05-19T20:45:00Z',
    },
    runtimeHealthError: '',
    runtimeDebugSystem: {
      audio: {
        stt_base_url: 'http://192.168.1.33:8766',
        tts_base_url: 'http://192.168.1.13:7870',
        stt_ok: false,
        tts_live_ok: false,
        tts_ready_ok: false,
        last_error: 'stt:context deadline exceeded; tts_live:context deadline exceeded',
      },
    },
    llmStatus: {
      roles: {
        Chat: {health_ok: true, halted: false},
        Worker: {health_ok: false, halted: true},
        Heavy: {health_ok: true, halted: false},
        Wild: {health_ok: false, halted: true},
      },
      memory: {
        llm_by_role: {
          Chat: {role: 'Chat', model: '/models/gemma', port: 8081, pid: 30289, rss_mib: 707.47},
          Worker: {role: 'Worker', model: '/models/qwen-vl', port: 8082, pid: null, rss_mib: null},
          Heavy: {role: 'Heavy', model: '/models/qwen-heavy', port: 8083, pid: 46923, rss_mib: 49971.38},
          Wild: {role: 'Wild', model: '/models/qwen-wild', port: 8084, pid: null, rss_mib: null},
        },
      },
    },
  },
};
` + sourceBetween(js, 'function normState', 'function fmt') +
sourceBetween(opsJs, 'function renderLocalLLMRuntimeConfig', 'function setLlmOpsStatusPre') + `
renderLocalLLMRuntimeConfig();
renderRuntimeDependencyReadiness();
globalThis.__runtime = document.getElementById('llmRuntimeConfigCards').innerHTML;
globalThis.__readiness = document.getElementById('runtimeReadinessCards').innerHTML;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.ok(context.__runtime.includes('Worker'));
  assert.ok(context.__runtime.includes('Heavy'));
  assert.ok(context.__runtime.includes('halted'), 'Worker should not be shown as running when llm-ops marks it halted');
  assert.ok(context.__runtime.includes('/models/qwen-heavy'), 'Heavy should use live status model, not stale local config label');
  assert.ok(context.__runtime.includes('http://192.168.1.31:8083'), 'Heavy should use live status port');
  assert.ok(context.__readiness.includes('LLM Ops'));
  assert.ok(context.__readiness.includes('Runtime Health'));
  assert.ok(context.__readiness.includes('service:missing'));
  assert.ok(context.__readiness.includes('chat:missing'));
  assert.ok(context.__readiness.includes('worker:missing'));
  assert.ok(context.__readiness.includes('local_llm_chat: connection refused'));
  assert.ok(context.__readiness.includes('configured:present'));
  assert.ok(context.__readiness.includes('proxy:present'));
  assert.ok(context.__readiness.includes('STT'));
  assert.ok(context.__readiness.includes('env:present'));
  assert.ok(context.__readiness.includes('config:present'));
  assert.ok(context.__readiness.includes('health:missing'));
  assert.ok(context.__readiness.includes('live:missing'));
  assert.ok(context.__readiness.includes('ready:missing'));
  assert.ok(context.__readiness.includes('blocked: stt:context deadline exceeded'));
  assert.ok(context.__readiness.includes('blocked: real microphone STT E2E not verified'));
  assert.ok(context.__readiness.includes('blocked: browser audio playback/lip sync E2E not verified'));
  assert.ok(context.__readiness.includes('Distributed'));
  assert.ok(context.__readiness.includes('enabled:missing'));
  assert.ok(context.__readiness.includes('ssh-connected:missing'));
  assert.ok(context.__readiness.includes('blocked: distributed disabled'));
  assert.ok(context.__readiness.includes('credentials:missing'));
  assert.ok(context.__readiness.includes('webhook:missing'));
  assert.ok(context.__readiness.includes('file:missing'));
  assert.ok(context.__readiness.includes('blocked: real external API file event E2E not verified'));
  assert.ok(context.__readiness.includes('blocked: Wild SSH/multi-machine E2E not verified'));
  assert.ok(context.__readiness.includes('Source Registry'));
  assert.ok(context.__readiness.includes('memory-layers:missing'));
  assert.ok(context.__readiness.includes('memory-route:present'));
  assert.ok(context.__readiness.includes('source:missing'));
  assert.ok(context.__readiness.includes('source-route:present'));
  assert.ok(context.__readiness.includes('blocked: conversation L1 disabled'));
  assert.ok(context.__readiness.includes('Knowledge Memory'));
  assert.ok(context.__readiness.includes('/viewer/knowledge-memory'));
  assert.ok(context.__readiness.includes('Browser Trace API'));
  assert.ok(context.__readiness.includes('fetcher:present'));
  assert.ok(context.__readiness.includes('review-only: discover and fetcher proposal require evidence'));
  assert.ok(context.__readiness.includes('Sandbox'));
  assert.ok(context.__readiness.includes('status:present'));
  assert.ok(context.__readiness.includes('blocked: sandbox disabled'));
  assert.ok(context.__readiness.includes('http://192.168.1.13:7870'));
});

test('viewer renders llm ops upstream failure as blocked readiness', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const els = new Map();
  const document = {
    getElementById(id) {
      if (!els.has(id)) els.set(id, new FakeElement());
      return els.get(id);
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function stateClass(s) { return 'state-' + s; }
var state = {ops: {
  llmOpsConfigured: true,
  llmOpsEnabled: true,
  llmOpsBaseURL: 'http://192.168.1.13:8079',
  llmStatus: null,
  llmStatusError: 'HTTP 502: upstream unreachable',
  runtimeReadiness: {},
}};
` + sourceBetween(opsJs, 'function renderRuntimeDependencyReadiness', 'function llmRuntimeRoleRow') + `
renderRuntimeDependencyReadiness();
globalThis.__readiness = document.getElementById('runtimeReadinessCards').innerHTML;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.ok(context.__readiness.includes('LLM Ops'));
  assert.ok(context.__readiness.includes('configured:present'));
  assert.ok(context.__readiness.includes('proxy:present'));
  assert.ok(context.__readiness.includes('live:missing'));
  assert.ok(context.__readiness.includes('blocked: HTTP 502: upstream unreachable'));
});

test('viewer renders runtime config and debug snapshot fetch failures as blocked readiness', () => {
  const opsJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/ops.js', 'utf8');
  const els = new Map();
  const document = {
    getElementById(id) {
      if (!els.has(id)) els.set(id, new FakeElement());
      return els.get(id);
    },
  };
  const source = `
function esc(s) { return String(s || ''); }
function stateClass(s) { return 'state-' + s; }
var state = {ops: {
  runtimeConfigFetchError: 'HTTP 503: runtime config store unavailable',
  runtimeDebugSystemFetchError: 'HTTP 502: debug system unavailable',
  localLLM: {enabled: true, chat_model: 'stale-chat', chat_base_url: 'http://stale:8081'},
  runtimeReadiness: {
    stt_gateway_env_present: true,
    stt_gateway_config_present: true,
    tts_provider_env_present: false,
    tts_provider_config_present: true,
  },
  runtimeSTTBaseURL: 'http://192.168.1.33:8766',
  runtimeTTSBaseURL: 'http://192.168.1.13:7870',
  runtimeDebugSystem: null,
  llmOpsConfigured: false,
  llmOpsEnabled: false,
  llmOpsBaseURL: '',
  llmStatus: null,
  llmStatusError: '',
  runtimeHealth: null,
  runtimeHealthError: '',
}};
` + sourceBetween(opsJs, 'function renderLocalLLMRuntimeConfig', 'function setLlmOpsStatusPre') + `
renderLocalLLMRuntimeConfig();
renderRuntimeDependencyReadiness();
globalThis.__runtime = document.getElementById('llmRuntimeConfigCards').innerHTML;
globalThis.__readiness = document.getElementById('runtimeReadinessCards').innerHTML;
`;
  const context = vm.createContext({document});
  vm.runInContext(source, context);

  assert.ok(context.__runtime.includes('local_llm runtime config unavailable: HTTP 503: runtime config store unavailable'));
  assert.ok(!context.__runtime.includes('stale-chat'), 'stale local LLM config should not be displayed when runtime config fetch failed');
  assert.ok(context.__readiness.includes('Runtime Config'));
  assert.ok(context.__readiness.includes('config:missing'));
  assert.ok(context.__readiness.includes('blocked: HTTP 503: runtime config store unavailable'));
  assert.ok(context.__readiness.includes('STT'));
  assert.ok(context.__readiness.includes('blocked: HTTP 502: debug system unavailable'));
  assert.ok(context.__readiness.includes('TTS'));
  assert.ok(context.__readiness.includes('blocked: browser audio playback/lip sync E2E not verified'));
});

test('viewer renders tts audio unlock failures as visible playback state', async () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const nowPlaying = new FakeElement('ttsNowPlaying');
  const nowPlayingText = new FakeElement('ttsNowPlayingText');
  const source = `
var ttsNowPlayingEl = globalThis.__nowPlaying;
var ttsNowPlayingTextEl = globalThis.__nowPlayingText;
var ttsPlayback = {
  queue: [],
  audio: null,
  playing: false,
  audioEnabled: true,
  unlocked: false,
  blocked: false,
  currentCharacterId: '',
  currentText: '',
  currentDisplayText: '',
  currentSessionId: '',
  currentChunkIndex: -1,
  currentUtteranceId: '',
  currentResponseId: '',
  currentShown: false,
  fallbackActive: false,
  fallbackTimer: null,
  seq: 0,
};
var lipSyncActors = {};
function setLipSyncSpeaking() {}
function clearLipSyncSpeaking() {}
function setCentralTTSSpeechText() {}
function resetCentralTTSSpeechBubble() {}
function updateAudioButton() { globalThis.__buttonState = {blocked: ttsPlayback.blocked, error: ttsPlayback.audioError}; }
function isAutoplayBlockedError(err) { return err && err.name === 'NotAllowedError'; }
function ttsDisplayDelay() { return 1; }
class Audio {
  constructor() { this.dataset = {}; this.readyState = 0; this.currentTime = 0; }
  addEventListener() {}
  pause() {}
  removeAttribute() {}
  load() {}
  play() { const err = new Error('browser blocked autoplay'); err.name = 'NotAllowedError'; return Promise.reject(err); }
}
var HTMLMediaElement = {HAVE_CURRENT_DATA: 2};
` + sourceBetween(viewerJs, 'function setNowPlayingText', 'function isIdleChatSessionId') +
sourceBetween(viewerJs, 'function createChatAudioSync', 'const chatAudioSync') + `
globalThis.__sync = createChatAudioSync();
`;
  const context = vm.createContext({
    __nowPlaying: nowPlaying,
    __nowPlayingText: nowPlayingText,
    setTimeout,
    clearTimeout,
    console: {error() {}, warn() {}, log() {}},
  });
  vm.runInContext(source, context);

  await context.__sync.unlockAudio();

  assert.equal(nowPlayingText.textContent, 'TTS audio unavailable: NotAllowedError: browser blocked autoplay');
  assert.equal(context.__buttonState.blocked, true);
  assert.equal(context.__buttonState.error, 'NotAllowedError: browser blocked autoplay');
});

test('viewer uses 500ms chunk wait when speaker is off', () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const delays = [];
  const source = `
var ttsPlayback = {
  queue: [],
  audio: null,
  playing: false,
  audioEnabled: false,
  unlocked: false,
  blocked: false,
  currentCharacterId: '',
  currentText: '',
  currentDisplayText: '',
  currentSessionId: '',
  currentChunkIndex: -1,
  currentUtteranceId: '',
  currentResponseId: '',
  currentShown: false,
  fallbackActive: false,
  fallbackTimer: null,
  tailTimer: null,
  tailActive: false,
  seq: 0,
};
var state = {idleChat: {chatActive: true}};
var lipSyncActors = {};
function setLipSyncSpeaking() {}
function clearLipSyncSpeaking() {}
function setCentralTTSSpeechText(characterId, text) { globalThis.__shown = {characterId, text}; }
function setNowPlayingText() {}
function clearTTSAudioError() {}
function updateAudioButton() {}
function isIdleChatSessionId(sessionId) { return String(sessionId || '').startsWith('idle-'); }
function describeTTSAudioError(err) { return err ? String(err.message || err) : ''; }
function ttsDisplayDelay() { return 3400; }
` + sourceBetween(viewerJs, 'function ttsTextFallbackDelay', 'function ttsPlaybackTailGap') +
sourceBetween(viewerJs, 'function ttsChunkIdentityKey', 'function ttsBubbleKind') +
sourceBetween(viewerJs, 'function createChatAudioSync', 'const chatAudioSync') + `
globalThis.__sync = createChatAudioSync();
globalThis.__sync.enqueueAudio({
  url: '/viewer/tts/audio?path=test.wav',
  characterId: 'mio',
  sessionId: 'chat-1',
  chunkIndex: 0,
  text: '長い読み上げテキスト',
  displayText: '長い読み上げテキスト',
  responseId: 'response-1',
  utteranceId: 'utterance-1',
});
`;
  const context = vm.createContext({
    setTimeout(fn, delay) {
      delays.push(delay);
      return 1;
    },
    clearTimeout() {},
    fetch() { return Promise.resolve({ok: true}); },
    console: {error() {}, warn() {}, log() {}},
  });
  vm.runInContext(source, context);

  assert.deepEqual(delays, [500]);
  assert.equal(context.__shown.characterId, 'mio');
  assert.equal(context.__shown.text, '長い読み上げテキスト');
});

test('viewer renders stt copy failures as persistent session state', async () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const debugSession = new FakeElement('debugSttSession');
  const sessionState = new FakeElement('sttSessionState');
  const source = `
var window = {location: {href: 'http://127.0.0.1:18790/viewer', protocol: 'http:', host: '127.0.0.1:18790'}};
var sttState = {
  ws: null,
  isRecording: false,
  reconnecting: false,
  captureLog: [],
  captureStartedAt: '',
  captureEndedAt: '',
  captureSessionID: 'stt_session_1',
  captureActionError: '',
  voiceBridgeURL: 'ws://127.0.0.1:18790/stt',
};
var micBtn = null;
var micStateEl = null;
var sttConnStateEl = null;
var sttSessionStateEl = globalThis.__sessionState;
var debugSttSessionEl = globalThis.__debugSession;
var vdsState = {isRecording: false, inputLevel: 0};
function fdt(s) { return s || '-'; }
function ftime(s) { return s || '--:--:--'; }
function isVoiceChatAllowed() { return true; }
function isMobileControlViewport() { return false; }
function getSTTMicrophoneUnavailableReason() { return ''; }
function isSTTTestRecording() { return false; }
function showToast(message, type) { globalThis.__toasts.push({message, type}); }
function writeClipboardText() { return Promise.reject(new Error('clipboard denied')); }
` + sourceBetween(viewerJs, 'function getSTTCaptureSummaryText', 'async function persistSTTLogToServer') +
sourceBetween(viewerJs, 'function updateSTTInputIndicators', 'async function toggleSTT') + `
globalThis.__copyLog = copySTTCaptureLog;
globalThis.__copySession = copySTTSessionID;
`;
  const context = vm.createContext({
    __debugSession: debugSession,
    __sessionState: sessionState,
    __toasts: [],
    console: {error() {}, warn() {}, log() {}},
  });
  vm.runInContext(source, context);

  context.__copyLog();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(debugSession.textContent, 'Session: stt_session_1 / STT log copy unavailable: clipboard denied');
  assert.equal(sessionState.title, 'Session: stt_session_1 / STT log copy unavailable: clipboard denied');

  context.__copySession();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(debugSession.textContent, 'Session: stt_session_1 / STT session copy unavailable: clipboard denied');
  assert.equal(context.__toasts.at(-1).type, 'error');
});

test('viewer renders stt microphone start failures as persistent session state', async () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const debugSession = new FakeElement('debugSttSession');
  const sessionState = new FakeElement('sttSessionState');
  const source = `
var WebSocket = {OPEN: 1, CONNECTING: 0};
var sttState = {
  ws: null,
  isRecording: false,
  reconnecting: false,
  captureActionError: '',
  captureSessionID: '(unknown)',
  captureLog: [],
  capturePCM: [],
  captureStartedAt: '',
  captureEndedAt: '',
  streamReady: false,
  runtimeConfigLoaded: true,
};
var micBtn = null;
var micStateEl = null;
var sttConnStateEl = null;
var sttSessionStateEl = globalThis.__sessionState;
var debugSttSessionEl = globalThis.__debugSession;
var vdsState = {isRecording: false, inputLevel: 0};
var navigator = {mediaDevices: {getUserMedia() { return Promise.reject(new Error('permission denied')); }}};
function isVoiceChatAllowed() { return true; }
function isSTTTestRecording() { return false; }
function ensureVoiceChatForMobileControl() { return true; }
function getSTTMicrophoneUnavailableReason() { return ''; }
function loadViewerRuntimeConfig() { return Promise.resolve(); }
function updateSTTInputIndicators() {
  const sid = String(sttState.captureSessionID || '(unknown)').trim() || '(unknown)';
  const actionError = String(sttState.captureActionError || '').trim();
  const suffix = actionError ? ' / ' + actionError : '';
  sttSessionStateEl.textContent = 'Session: ' + sid + suffix;
  sttSessionStateEl.title = 'Session: ' + sid + suffix;
  debugSttSessionEl.textContent = 'Session: ' + sid + suffix;
}
function showToast(message, type) { globalThis.__toasts.push({message, type}); }
function stopSTT() { globalThis.__stopCalled = true; updateSTTInputIndicators(); }
` + sourceBetween(viewerJs, 'function describeSTTActionError', 'function copySTTCaptureLog') +
sourceBetween(viewerJs, 'async function startSTT()', 'function connectSTTWebSocket') + `
globalThis.__startSTT = startSTT;
`;
  const context = vm.createContext({
    __debugSession: debugSession,
    __sessionState: sessionState,
    __toasts: [],
    __stopCalled: false,
    console: {error() {}, warn() {}, log() {}},
  });
  vm.runInContext(source, context);

  await context.__startSTT();

  assert.equal(context.__stopCalled, true);
  assert.equal(debugSession.textContent, 'Session: (unknown) / STT microphone start unavailable: permission denied');
  assert.equal(sessionState.title, 'Session: (unknown) / STT microphone start unavailable: permission denied');
  assert.equal(context.__toasts.at(-1).type, 'error');
});

test('viewer renders stt artifact persistence failures as persistent session state', async () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const debugSession = new FakeElement('debugSttSession');
  const sessionState = new FakeElement('sttSessionState');
  const source = `
var WebSocket = {OPEN: 1};
var sttState = {
  ws: null,
  audioContext: null,
  audioStream: null,
  scriptNode: null,
  isRecording: true,
  isStopping: false,
  reconnectTimer: null,
  reconnecting: false,
  chunkBuffer: [],
  draftBuffer: [],
  captureActionError: '',
  captureSessionID: 'stt_session_2',
};
var sttSessionStateEl = globalThis.__sessionState;
var debugSttSessionEl = globalThis.__debugSession;
function updateSTTInputIndicators() {
  const sid = String(sttState.captureSessionID || '(unknown)').trim() || '(unknown)';
  const actionError = String(sttState.captureActionError || '').trim();
  const suffix = actionError ? ' / ' + actionError : '';
  sttSessionStateEl.textContent = 'Session: ' + sid + suffix;
  sttSessionStateEl.title = 'Session: ' + sid + suffix;
  debugSttSessionEl.textContent = 'Session: ' + sid + suffix;
}
function showToast(message, type) { globalThis.__toasts.push({message, type}); }
function persistSTTArtifacts() { return Promise.reject(new Error('HTTP 507: tmp log path full')); }
` + sourceBetween(viewerJs, 'function describeSTTActionError', 'function copySTTCaptureLog') +
viewerJs.slice(viewerJs.indexOf('function stopSTT()')) + `
globalThis.__stopSTT = stopSTT;
`;
  const context = vm.createContext({
    __debugSession: debugSession,
    __sessionState: sessionState,
    __toasts: [],
    console: {error() {}, warn() {}, log() {}},
    clearTimeout() {},
  });
  vm.runInContext(source, context);

  context.__stopSTT();
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(debugSession.textContent, 'Session: stt_session_2 / STT artifact persistence unavailable: HTTP 507: tmp log path full');
  assert.equal(sessionState.title, 'Session: stt_session_2 / STT artifact persistence unavailable: HTTP 507: tmp log path full');
  assert.equal(context.__toasts.at(-1).type, 'error');
});

test('viewer renders stt websocket message failures as persistent session state', () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const debugSession = new FakeElement('debugSttSession');
  const sessionState = new FakeElement('sttSessionState');
  const source = `
var wsInstances = [];
class FakeWebSocket {
  static OPEN = 1;
  static CONNECTING = 0;
  constructor(url) {
    this.url = url;
    this.readyState = FakeWebSocket.CONNECTING;
    wsInstances.push(this);
  }
}
var WebSocket = FakeWebSocket;
var sttState = {
  ws: null,
  isRecording: true,
  isStopping: false,
  keepSessionChannel: false,
  reconnecting: false,
  captureActionError: '',
  captureSessionID: 'stt_session_3',
  voiceBridgeURL: 'ws://127.0.0.1:18790/stt',
};
var sttSessionStateEl = globalThis.__sessionState;
var debugSttSessionEl = globalThis.__debugSession;
function updateSTTInputIndicators() {
  const sid = String(sttState.captureSessionID || '(unknown)').trim() || '(unknown)';
  const actionError = String(sttState.captureActionError || '').trim();
  const suffix = actionError ? ' / ' + actionError : '';
  sttSessionStateEl.textContent = 'Session: ' + sid + suffix;
  sttSessionStateEl.title = 'Session: ' + sid + suffix;
  debugSttSessionEl.textContent = 'Session: ' + sid + suffix;
}
function showToast(message, type) { globalThis.__toasts.push({message, type}); }
function ftime() { return '12:00:00'; }
function pushDebugTrace() {}
function short(value) { return String(value || ''); }
function recordSTTCaptureEvent() {}
function renderDebugPanels() {}
function handleSTTFinalText() {}
function clearSTTFinalWaitTimer() {}
function scheduleSTTReconnect() {}
var document = {getElementById() { return null; }};
` + sourceBetween(viewerJs, 'function describeSTTActionError', 'function copySTTCaptureLog') +
sourceBetween(viewerJs, 'function connectSTTWebSocket', 'function resampleToPCM16') + `
globalThis.__connectSTTWebSocket = connectSTTWebSocket;
globalThis.__wsInstances = wsInstances;
`;
  const context = vm.createContext({
    __debugSession: debugSession,
    __sessionState: sessionState,
    __toasts: [],
    console: {error() {}, warn() {}, log() {}},
  });
  vm.runInContext(source, context);

  context.__connectSTTWebSocket();
  const ws = context.__wsInstances[0];
  ws.onmessage({data: JSON.stringify({type: 'error', error: 'provider timeout'})});
  assert.equal(debugSession.textContent, 'Session: stt_session_3 / STT recognition unavailable: provider timeout');
  assert.equal(sessionState.title, 'Session: stt_session_3 / STT recognition unavailable: provider timeout');
  assert.equal(context.__toasts.at(-1).type, 'error');

  ws.onmessage({data: '{'});
  assert.match(debugSession.textContent, /Session: stt_session_3 \/ STT message parse unavailable: /);

  ws.onerror(new Error('socket refused'));
  assert.equal(debugSession.textContent, 'Session: stt_session_3 / STT websocket unavailable: socket refused');
  assert.equal(sessionState.title, 'Session: stt_session_3 / STT websocket unavailable: socket refused');
});

test('viewer sends STT stop before locally finalizing latest partial on timeout', async () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const source = `
var wsInstances = [];
class FakeWebSocket {
  static OPEN = 1;
  static CONNECTING = 0;
  constructor(url) {
	    this.url = url;
	    this.readyState = FakeWebSocket.CONNECTING;
	    this.closed = false;
	    this.sent = [];
	    wsInstances.push(this);
	  }
	  send(payload) { this.sent.push(payload); }
	  close() { this.closed = true; this.readyState = 3; }
	}
var WebSocket = FakeWebSocket;
var sttState = {
  ws: null,
  audioContext: null,
  audioStream: null,
  scriptNode: null,
  isRecording: true,
  isStopping: false,
  keepSessionChannel: false,
  reconnecting: false,
  chunkBuffer: [],
  draftBuffer: [],
  captureActionError: '',
	  captureSessionID: 'stt_session_partial',
	  lastRecognitionText: '',
	  lastRecognitionType: '',
	  finalReceived: false,
	  finalWaitTimer: null,
	  stopControlSent: false,
	  sampleRate: 16000,
	  chunkSamples: 1600,
	  voiceBridgeURL: 'ws://127.0.0.1:18790/stt',
	};
	const STT_FINAL_WAIT_TIMEOUT_MS = 90000;
	const STT_STOP_TAIL_SILENCE_MS = 300;
	function updateSTTInputIndicators() {}
	function updateSTTInputLevel() {}
	function updateSTTCaption() {}
	function showToast(message, type) { globalThis.__toasts.push({message, type}); }
function ftime() { return '12:00:00'; }
function pushDebugTrace(kind, payload) { globalThis.__debug.push({kind, payload}); }
function short(value) { return String(value || ''); }
function recordSTTCaptureEvent(type, payload) { globalThis.__capture.push({type, payload}); }
function renderDebugPanels() {}
function scheduleSTTReconnect() {}
function persistSTTArtifacts() { return Promise.resolve(); }
var activeViewerTab = 'timeline';
var sending = false;
var document = {
  body: {classList: {contains() { return false; }}},
  getElementById(id) {
    if (id === 'inp') return globalThis.__input;
    return null;
  }
};
function isVoiceChatAllowed() {
  return activeViewerTab === 'timeline' && !document.body.classList.contains('live-mode');
}
function autoResize() { globalThis.__autoResizeCalled = true; }
function send() { globalThis.__sendCalled = true; }
	` + sourceBetween(viewerJs, 'function describeSTTActionError', 'function copySTTCaptureLog') +
	sourceBetween(viewerJs, 'function connectSTTWebSocket', 'function resampleToPCM16') +
	sourceBetween(viewerJs, 'function flushSTTAudioChunkBuffer', 'function handleSTTFinalText') +
	sourceBetween(viewerJs, 'function handleSTTFinalText', 'function scheduleSTTReconnect') +
	viewerJs.slice(viewerJs.indexOf('function stopSTT()')) + `
globalThis.__connectSTTWebSocket = connectSTTWebSocket;
globalThis.__stopSTT = stopSTT;
globalThis.__wsInstances = wsInstances;
globalThis.__sttState = sttState;
`;
  const contextGlobals = {
	    __capture: [],
	    __debug: [],
	    __input: {value: '', focus() { this.focused = true; }},
    __autoResizeCalled: false,
    __sendCalled: false,
	    __toasts: [],
	    console: {error() {}, warn() {}, log() {}},
	    clearTimeout() {},
	    setTimeout(fn) {
	      contextGlobals.__scheduledTimeout = fn;
	      return 1;
	    },
	  };
  const context = vm.createContext(contextGlobals);
  vm.runInContext(source, context);

  context.__connectSTTWebSocket();
  const ws = context.__wsInstances[0];
  ws.readyState = 1;
  ws.onmessage({data: JSON.stringify({type: 'partial', text: 'テスト', is_final: false})});
  assert.equal(context.__sttState.lastRecognitionText, 'テスト');
  assert.equal(context.__sttState.lastRecognitionType, 'partial');

	  context.__stopSTT();
	  await new Promise((resolve) => setImmediate(resolve));

	  assert.equal(context.__sendCalled, false);
	  assert.equal(ws.closed, false);
	  assert.ok(ws.sent.some((item) => String(item) === '{"type":"stop"}'));

	  context.__scheduledTimeout();
	  await new Promise((resolve) => setImmediate(resolve));

	  assert.equal(context.__input.value, 'テスト');
	  assert.equal(context.__input.focused, true);
	  assert.equal(context.__autoResizeCalled, true);
	  assert.equal(context.__sendCalled, true);
	  assert.equal(context.__sttState.finalReceived, true);
	  assert.equal(ws.closed, true);
	  const recognitionCapture = context.__capture
	    .filter((item) => item.type === 'partial' || item.type === 'final' || item.type === 'final_fallback')
	    .map((item) => ({type: item.type, payload: item.payload}));
	  assert.deepEqual(recognitionCapture, [
	    {type: 'partial', payload: 'テスト'},
	    {type: 'final', payload: 'テスト'},
	    {type: 'final_fallback', payload: 'timeout'},
	  ]);
	});

test('viewer renders home send failures as visible desk state', async () => {
  const homeJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/home.js', 'utf8');
  const elements = new Map();
  const listeners = {};
  class EventElement extends FakeElement {
    constructor(id = '') {
      super(id);
      this.value = '';
      this.disabled = false;
    }
    addEventListener(type, fn) {
      listeners[this.id + ':' + type] = fn;
    }
  }
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new EventElement(id));
      return elements.get(id);
    },
  };
  const source = `
const state = {
  logs: [],
  jobs: {},
  agents: {},
  evidence: [],
  verificationReports: [],
  homeSendError: '',
};
const AGENTS = ['mio'];
function esc(s) { return String(s || ''); }
function fdt(s) { return String(s || '-'); }
function short(s) { return String(s || ''); }
function agName(s) { return String(s || '-'); }
function switchTab(tab) { globalThis.__switchedTab = tab; }
function showToast(message, type) { globalThis.__toasts.push({message, type}); }
function sendViewerMessage() { return Promise.reject(new Error('HTTP 502: viewer send route unavailable')); }
var localStorage = {getItem() { return null; }, setItem() {}};
` + homeJs + `
globalThis.__state = state;
globalThis.__bindHomeDeskControls = bindHomeDeskControls;
globalThis.__renderHomeDesk = renderHomeDesk;
`;
  const context = vm.createContext({
    document,
    __toasts: [],
    console: {error() {}, warn() {}, log() {}},
  });
  vm.runInContext(source, context);
  document.getElementById('homeInput').value = 'hello';
  document.getElementById('homeTargetAgent').value = 'worker';

  context.__bindHomeDeskControls();
  listeners['homeSendBtn:click']();
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(context.__state.homeSendError, 'Home send unavailable: HTTP 502: viewer send route unavailable');
  assert.match(document.getElementById('homeStatusCard').innerHTML, /Home send unavailable: HTTP 502: viewer send route unavailable/);
  assert.equal(context.__toasts.at(-1).type, 'error');
});

test('viewer renders send failures with response body in timeline', async () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const requested = [];
  const timelineEvents = [];
  const source = `
let sending = false;
let viewerAttachments = [];
const inp = {value: 'hello', disabled: false, focus() {}};
const sendBtn = {disabled: false};
const attachBtn = null;
const cameraBtn = null;
function renderAttachmentTray() {}
function autoResize() {}
function buildViewerSendRequest(message) { return {message}; }
async function ensureViewerLLMReadyForRequest() {}
function addMsgToTimeline(ev) { globalThis.__timelineEvents.push(ev); }
` + sourceBetween(viewerJs, 'function send()', 'function buildViewerStatusSnapshot') + `
globalThis.__send = send;
`;
  const context = vm.createContext({
    console: {error() {}},
    __timelineEvents: timelineEvents,
    fetch(url) {
      requested.push(url);
      return Promise.resolve({
        ok: false,
        status: 502,
        text: () => Promise.resolve('viewer send route unavailable'),
      });
    },
  });
  vm.runInContext(source, context);
  context.__send();
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(requested[0], '/viewer/send');
  assert.equal(timelineEvents.length, 1);
  assert.equal(timelineEvents[0].type, 'agent.response');
  assert.equal(timelineEvents[0].from, 'mio');
  assert.equal(timelineEvents[0].to, 'user');
  assert.match(timelineEvents[0].content, /Viewer send unavailable: HTTP 502: viewer send route unavailable/);
  assert.doesNotMatch(timelineEvents[0].content, /send failed/);
});

test('viewer timeline renders local send failures from tts synced speakers', () => {
  const timelineJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/timeline.js', 'utf8');
  const chatNode = {
    children: [],
    appendChild(child) {
      this.children.push(child);
      return child;
    },
  };
  const source = `
const chat = globalThis.__chat;
function matchesFilters() { return true; }
function ag(agentID) { return {c: '#69f', e: 'M', l: String(agentID || '')}; }
function ftime() { return '09:21'; }
function normalizeViewerDisplayText(value) { return String(value || ''); }
function fmt(value) { return String(value || '').replace(/&/g, '&amp;').replace(/</g, '&lt;'); }
function removeThinking() {}
function addThinkingStart() {}
function addThinking() {}
function isTTSSyncedSpeaker(agentID) { return String(agentID || '').toLowerCase() === 'mio'; }
function trimTimelineNodes() {}
function bump() {}
function isViewerLocalFailureMessage(ev) { return String(ev && ev.content || '').startsWith('Viewer send unavailable:'); }
function isCoordinationTraceEvent() { return false; }
function rememberVoiceDirectTimelineJob() {}
function isVoiceDirectTimelineResponse() { return false; }
function addJobNotificationToTimeline() {}
function addCoordinationTraceToTimeline() {}
` + sourceBetween(timelineJs, 'function addMsgToTimeline', 'bindChatRouteAliasButtons();') + `
globalThis.__addMsgToTimeline = addMsgToTimeline;
`;
  const context = vm.createContext({
    __chat: chatNode,
    document: {
      getElementById() { return {remove() {}}; },
      createElement() {
        return {
          className: '',
          _innerHTML: '',
          querySelector(selector) {
            if (selector === '.mc') return {dataset: {}};
            return null;
          },
          set innerHTML(value) { this._innerHTML = String(value || ''); },
          get innerHTML() { return this._innerHTML; },
        };
      },
    },
  });
  vm.runInContext(source, context);

  context.__addMsgToTimeline({
    type: 'agent.response',
    from: 'mio',
    to: 'user',
    timestamp: '2026-05-20T09:21:00Z',
    content: 'Viewer send unavailable: llm ops health failed: HTTP 503: llm ops health unavailable live',
  });
  context.__addMsgToTimeline({
    type: 'agent.response',
    from: 'mio',
    to: 'user',
    timestamp: '2026-05-20T09:21:01Z',
    content: 'normal tts-synced response',
  });

  assert.equal(chatNode.children.length, 1);
  assert.match(chatNode.children[0].innerHTML, /Viewer send unavailable: llm ops health failed: HTTP 503: llm ops health unavailable live/);
  assert.doesNotMatch(chatNode.children[0].innerHTML, /normal tts-synced response/);
});

test('viewer timeline hides voice direct internal prompt user events', () => {
  const timelineJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/timeline.js', 'utf8');
  const chatNode = {
    children: [],
    appendChild(child) {
      this.children.push(child);
      return child;
    },
  };
  const source = `
const chat = globalThis.__chat;
function matchesFilters() { return true; }
function ag(agentID) { return {c: '#69f', e: 'M', l: String(agentID || '')}; }
function ftime() { return '09:21'; }
function normalizeViewerDisplayText(value) { return String(value || ''); }
function fmt(value) { return String(value || '').replace(/&/g, '&amp;').replace(/</g, '&lt;'); }
function removeThinking() {}
function addThinkingStart() {}
function addThinking() {}
function isTTSSyncedSpeaker() { return true; }
function trimTimelineNodes() {}
function bump() {}
` + sourceBetween(timelineJs, 'const voiceDirectTimelineJobIDs', 'bindChatRouteAliasButtons();') + `
globalThis.__addMsgToTimeline = addMsgToTimeline;
`;
  const context = vm.createContext({
    __chat: chatNode,
    document: {
      getElementById() { return {remove() {}}; },
      createElement() {
        return {
          className: '',
          _innerHTML: '',
          querySelector(selector) {
            if (selector === '.mc') return {dataset: {}};
            return null;
          },
          set innerHTML(value) { this._innerHTML = String(value || ''); },
          get innerHTML() { return this._innerHTML; },
        };
      },
    },
  });
  vm.runInContext(source, context);

  context.__addMsgToTimeline({
    type: 'message.received',
    from: 'user',
    to: 'mio',
    job_id: 'voice-job-1',
    timestamp: '2026-06-15T02:40:00Z',
    content: '[voice_direct] あなたはMioです。入力された音声をユーザーの発話として扱い...',
  });
  context.__addMsgToTimeline({
    type: 'routing.decision',
    from: 'mio',
    to: '',
    job_id: 'voice-job-1',
    timestamp: '2026-06-15T02:40:00Z',
    content: 'confidence 100% evidence=voice_direct:matched:CHAT utterance_id=utt-1',
  });
  context.__addMsgToTimeline({
    type: 'agent.response',
    from: 'mio',
    to: 'user',
    job_id: 'voice-job-1',
    timestamp: '2026-06-15T02:40:01Z',
    content: '承知いたしました。',
  });

  assert.equal(chatNode.children.length, 1);
  assert.match(chatNode.children[0].innerHTML, /承知いたしました。/);
  assert.doesNotMatch(chatNode.children[0].innerHTML, /voice_direct/);
});

test('viewer llm ops readiness failures keep response bodies', async () => {
  const timelineJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/timeline.js', 'utf8');
  const requested = [];
  const source = `
const CHAT_ROUTE_ALIASES = {
  worker: {label: 'Worker', baseURL: 'http://127.0.0.1:8082', model: 'Worker', routePrefix: '/ops'},
};
function refreshLlmOpsStatus() {}
` + sourceBetween(timelineJs, 'function viewerLLMStartSelectionForRequest', 'function addMsgToTimeline') + `
globalThis.__ensure = ensureViewerLLMReadyForRequest;
`;
  const responses = [
    {ok: false, status: 503, body: 'llm ops health unavailable'},
    {ok: true, status: 200, body: '{}'},
    {ok: false, status: 502, body: 'llm ops status unavailable'},
    {ok: true, status: 200, body: '{}'},
    {ok: true, status: 200, body: '{"roles":{"Chat":{"health_ok":true},"Worker":{"health_ok":false}}}'},
    {ok: false, status: 409, body: 'llm ops stop refused'},
    {ok: true, status: 200, body: '{}'},
    {ok: true, status: 200, body: '{"roles":{"Chat":{"health_ok":true},"Worker":{"health_ok":false}}}'},
    {ok: true, status: 200, body: 'stopped'},
    {ok: false, status: 503, body: 'llm ops start unavailable'},
  ];
  const context = vm.createContext({
    fetch(url) {
      requested.push(url);
      const next = responses.shift();
      return Promise.resolve({
        ok: next.ok,
        status: next.status,
        text: () => Promise.resolve(next.body),
        json: () => Promise.resolve(JSON.parse(next.body)),
      });
    },
  });
  vm.runInContext(source, context);

  await assert.rejects(
    () => context.__ensure({model_alias: 'Worker'}),
    /llm ops health failed: HTTP 503: llm ops health unavailable/,
  );
  await assert.rejects(
    () => context.__ensure({model_alias: 'Worker'}),
    /llm ops status failed: HTTP 502: llm ops status unavailable/,
  );
  await assert.rejects(
    () => context.__ensure({model_alias: 'Worker'}),
    /llm ops stop failed: HTTP 409: llm ops stop refused/,
  );
  await assert.rejects(
    () => context.__ensure({model_alias: 'Worker'}),
    /llm ops start failed: HTTP 503: llm ops start unavailable/,
  );
  assert.deepEqual(requested, [
    '/viewer/llm-ops/health',
    '/viewer/llm-ops/health',
    '/viewer/llm-ops/status',
    '/viewer/llm-ops/health',
    '/viewer/llm-ops/status',
    '/viewer/llm-ops/stop',
    '/viewer/llm-ops/health',
    '/viewer/llm-ops/status',
    '/viewer/llm-ops/stop',
    '/viewer/llm-ops/start',
  ]);
});

test('viewer stt artifact persistence errors keep response bodies', async () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const requested = [];
  const source = sourceBetween(viewerJs, 'async function persistSTTLogToServer', 'async function persistSTTArtifacts') + `
globalThis.__persistSTTLogToServer = persistSTTLogToServer;
globalThis.__persistSTTWavToServer = persistSTTWavToServer;
globalThis.__runSTTAutoTest = runSTTAutoTest;
`;
  const responses = [
    {status: 507, body: 'tmp log path full'},
    {status: 413, body: 'wav too large'},
    {status: 502, body: 'stt provider unavailable'},
  ];
  const context = vm.createContext({
    fetch(url) {
      requested.push(url);
      const next = responses.shift();
      return Promise.resolve({
        ok: false,
        status: next.status,
        text: () => Promise.resolve(next.body),
      });
    },
  });
  vm.runInContext(source, context);

  await assert.rejects(
    () => context.__persistSTTLogToServer('log-body'),
    /HTTP 507: tmp log path full/,
  );
  await assert.rejects(
    () => context.__persistSTTWavToServer(new ArrayBuffer(4)),
    /HTTP 413: wav too large/,
  );
  await assert.rejects(
    () => context.__runSTTAutoTest(),
    /HTTP 502: stt provider unavailable/,
  );
  assert.deepEqual(requested, ['/viewer/stt/log', '/viewer/stt/wav', '/viewer/stt/autotest']);
});

test('viewer renders idlechat control failures with response body', async () => {
  const idleChatJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/idlechat.js', 'utf8');
  const requested = [];
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
const idleStartBtn = {disabled: false};
const idleModeNormalBtn = null;
const idleModeForecastBtn = null;
const idleModeStorySimpleBtn = null;
const idleStopBtn = {disabled: false};
const idleStateEl = {textContent: '', className: ''};
const state = {idleChat: {history: [], openIndex: -1}};
function esc(s) { return String(s || ''); }
function fdt(s) { return String(s || '-'); }
function short(s) { return String(s || ''); }
function fmt(s) { return String(s || ''); }
function copyTextPayload() {}
function showToast() {}
` + sourceBetween(idleChatJs, 'function setIdleState', 'async function controlIdle') +
idleChatJs.slice(idleChatJs.indexOf('async function controlIdle')) + `
globalThis.__controlIdle = controlIdle;
`;
  const responses = [
    {
      ok: false,
      status: 409,
      text: () => Promise.resolve('idlechat already stopping'),
    },
    {
      ok: true,
      json: () => Promise.resolve({manual_mode: false, chat_active: false, current_topic: ''}),
    },
  ];
  const context = vm.createContext({
    document,
    console: {error() {}},
    fetch(url) {
      requested.push(url);
      return Promise.resolve(responses.shift());
    },
  });
  vm.runInContext(source, context);
  await context.__controlIdle('/viewer/idlechat/stop');

  assert.deepEqual(requested, ['/viewer/idlechat/stop', '/viewer/idlechat/status']);
  assert.match(document.getElementById('idlechatBody').innerHTML, /IdleChat control unavailable: HTTP 409: idlechat already stopping/);
  assert.doesNotMatch(document.getElementById('idlechatBody').innerHTML, /idlechat control failed/);
});

test('viewer wires chat input and stt button to idlechat immediate stop', () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const routesGo = fs.readFileSync('cmd/picoclaw/routes.go', 'utf8');
  const handlersGo = fs.readFileSync('cmd/picoclaw/runtime_idlechat_handlers.go', 'utf8');
  const idlechatRegistrarGo = fs.readFileSync('internal/features/idlechat/registrar.go', 'utf8');
  assert.match(viewerJs, /function interruptIdleChatForUserInput/);
  assert.match(viewerJs, /fetch\('\/viewer\/idlechat\/stop'/);
  assert.match(viewerJs, /'user_input'[\s\S]*'stt_voice_start'[\s\S]*'vds_voice_start'/);
  assert.match(viewerJs, /inp\.addEventListener\('beforeinput', \(\) => handleChatInputIntent\('user_input'\)\)/);
  assert.match(viewerJs, /inp\.addEventListener\('paste', \(\) => handleChatInputIntent\('paste'\)\)/);
  assert.match(viewerJs, /inp\.addEventListener\('compositionstart', \(\) => handleChatInputIntent\('composition_start'\)\)/);
  assert.match(viewerJs, /function beginSTTUtterance/);
  assert.match(viewerJs, /interruptIdleChatForUserInput\('stt_voice_start'\)/);
  assert.match(viewerJs, /interruptChatOutputForUserInput\('stt_voice_start'\)/);
  assert.match(viewerJs, /function abortSTTImmediately\(reason\)/);
  assert.match(viewerJs, /if \(typeof clearSTTFinalWaitTimer === 'function'\) clearSTTFinalWaitTimer\(\)/);
  assert.match(viewerJs, /if \(chunk\.mode === 'idlechat' && !isIdleChatActiveForTTS\(chunk\.sessionId\)\) return/);
  assert.match(routesGo, /idlechatfeature\.RegisterRoutes/);
  assert.match(idlechatRegistrarGo, /\/viewer\/idlechat\/stop/);
  assert.match(idlechatRegistrarGo, /\/viewer\/idlechat\/interrupt/);
  assert.match(handlersGo, /handleIdleChatStop/);
  assert.match(handlersGo, /handleIdleChatInterrupt/);
});

test('viewer idlechat stop is fire-and-forget before send', () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const sendSource = sourceBetween(viewerJs, 'function send()', 'async function sendViewerMessage');
  assert.ok(
    sendSource.indexOf("interruptIdleChatForUserInput('chat_send')") >= 0,
    'send should request idlechat stop before viewer send',
  );
  assert.ok(
    sendSource.indexOf("interruptIdleChatForUserInput('chat_send')") < sendSource.indexOf('sendViewerMessage(message'),
    'idlechat stop should happen before sendViewerMessage',
  );
  const interruptSource = sourceBetween(viewerJs, 'function interruptIdleChatForUserInput', 'function handleChatInputIntent');
  assert.match(interruptSource, /fetch\('\/viewer\/idlechat\/stop'/);
  assert.match(interruptSource, /keepalive: true/);
  assert.doesNotMatch(interruptSource, /await fetch/);
});

test('viewer interrupts active chat output before accepting new user input', () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  assert.match(viewerJs, /function interruptChatOutputForUserInput\(reason\)/);
  assert.match(viewerJs, /resetChat: resetChatInternal/);
  assert.match(viewerJs, /function resetChatInternal\(reason\)/);
  assert.match(viewerJs, /function isInterruptedChatOutput\(item\)/);
  assert.match(viewerJs, /if \(isInterruptedChatOutput\(chunk\)\) return/);

  const inputIntentSource = sourceBetween(viewerJs, 'function handleChatInputIntent', "inp.addEventListener('beforeinput'");
  assert.ok(
    inputIntentSource.indexOf('interruptChatOutputForUserInput(reason)') >= 0,
    'chat input intent should stop active chat output',
  );
  assert.ok(
    inputIntentSource.indexOf('interruptChatOutputForUserInput(reason)') < inputIntentSource.indexOf('interruptIdleChatForUserInput(reason)'),
    'chat output should be stopped before idlechat interrupt side effects',
  );

  const sendSource = sourceBetween(viewerJs, 'function send()', 'async function sendViewerMessage');
  assert.ok(
    sendSource.indexOf("interruptChatOutputForUserInput('chat_send')") >= 0,
    'send should stop active chat output before viewer send',
  );
  assert.ok(
    sendSource.indexOf("interruptChatOutputForUserInput('chat_send')") < sendSource.indexOf('sendViewerMessage(message'),
    'chat output interrupt should happen before sendViewerMessage',
  );
  const resetSource = sourceBetween(viewerJs, 'function resetChatInternal', 'function resetIdleChatInternal');
  assert.match(resetSource, /state\.queue = state\.queue\.filter/);
  assert.match(resetSource, /rememberInterruptedChatOutput\(item\)/);
  assert.match(resetSource, /resetTTSSpeechBubble\(centralTTSSpeech\)/);
  assert.doesNotMatch(resetSource, /state\.audioEnabled = false/);
});

test('viewer renders idlechat status and logs failures with response bodies', async () => {
  const idleChatJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/idlechat.js', 'utf8');
  const requested = [];
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
    createElement() {
      return new FakeElement();
    },
  };
  const source = `
const idleStartBtn = {disabled: false};
const idleModeNormalBtn = null;
const idleModeForecastBtn = null;
const idleModeStorySimpleBtn = null;
const idleStopBtn = {disabled: false};
const idleStateEl = {textContent: '', className: ''};
const state = {idleChat: {history: [{title: 'stale summary'}], openIndex: -1}};
function esc(s) { return String(s || ''); }
function fdt(s) { return String(s || '-'); }
function short(s) { return String(s || ''); }
function fmt(s) { return String(s || ''); }
function copyTextPayload() {}
function showToast() {}
` + sourceBetween(idleChatJs, 'function setIdleState', 'async function controlIdle') + `
globalThis.__refreshIdleStatus = refreshIdleStatus;
globalThis.__refreshIdleLogs = refreshIdleLogs;
`;
  const responses = [
    {
      ok: false,
      status: 503,
      text: () => Promise.resolve('idlechat status store unavailable'),
    },
    {
      ok: false,
      status: 502,
      text: () => Promise.resolve('idlechat logs unavailable'),
    },
  ];
  const context = vm.createContext({
    document,
    fetch(url) {
      requested.push(url);
      return Promise.resolve(responses.shift());
    },
  });
  vm.runInContext(source, context);
  await context.__refreshIdleStatus();
  await context.__refreshIdleLogs();

  const body = document.getElementById('idlechatBody').innerHTML;
  assert.deepEqual(requested, ['/viewer/idlechat/status', '/viewer/idlechat/logs?limit=20']);
  assert.match(body, /IdleChat status unavailable: HTTP 503: idlechat status store unavailable/);
  assert.match(body, /IdleChat logs unavailable: HTTP 502: idlechat logs unavailable/);
  assert.doesNotMatch(body, /stale summary/);
});

test('viewer live mode renders idlechat topic fetch failures', async () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const requested = [];
  let intervalCallback = null;
  const elements = new Map();
  const document = {
    body: {classList: {add() {}}},
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
  };
  const source = `
const panels = {timeline: true};
function switchTab(tab) { globalThis.__switchedTab = tab; }
` + sourceBetween(viewerJs, 'function initLiveMode', 'function initEvidenceFromQuery') + `
globalThis.__initLiveMode = initLiveMode;
`;
  const context = vm.createContext({
    document,
    window: {location: {href: 'http://127.0.0.1:18790/viewer?mode=live'}},
    URL,
    setInterval(fn) {
      intervalCallback = fn;
      return 1;
    },
    fetch(url) {
      requested.push(url);
      return Promise.resolve({
        ok: false,
        status: 503,
        text: () => Promise.resolve('idlechat status unavailable'),
      });
    },
  });
  vm.runInContext(source, context);
  assert.equal(context.__initLiveMode(), true);
  await intervalCallback();

  assert.equal(context.__switchedTab, 'timeline');
  assert.deepEqual(requested, ['/viewer/idlechat/status']);
  assert.match(document.getElementById('liveTopicText').textContent, /IdleChat status unavailable: HTTP 503: idlechat status unavailable/);
});

test('viewer live mode does not start optional hidden panel refreshes', () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const requested = [];
  const intervals = [];
  const elements = new Map();
  const bodyClasses = new Set();
  const bodyClassList = {
    add(name) { bodyClasses.add(name); },
    remove(name) { bodyClasses.delete(name); },
    contains(name) { return bodyClasses.has(name); },
  };
  const document = {
    body: {classList: bodyClassList},
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
  };
  const optionalNames = [
    'refreshOpsData',
    'refreshToolHarnessData',
    'refreshDCIData',
    'refreshSandboxData',
    'refreshSkillGovernanceData',
    'refreshWorkstreamData',
    'refreshRevenueData',
    'refreshPersonaObservationData',
    'refreshBrowserTraceAPIData',
    'refreshComplexityHotspotData',
    'refreshAIWorkflowData',
    'refreshSuperAgentData',
    'refreshHeavyWorkerRuntimeDiagnostics',
    'refreshKnowledgeMemoryData',
    'refreshRuntimeBlockedRouteData',
    'refreshEvidence',
    'refreshEvidenceSummary',
    'refreshVerification',
    'refreshVerificationSummary',
    'refreshMemorySnapshot',
    'refreshRecallTraces',
  ];
  const optionalStubs = optionalNames.map((name) => `
function ${name}() { fetch('/optional/${name}'); }
`).join('\n');
  const source = `
let derivedDirty = false;
const state = {idleChat: {selectedMode: 'manual', selectedView: 'live'}};
const panels = {timeline: true};
${optionalStubs}
function switchTab(tab) { globalThis.__switchedTab = tab; }
function refreshDerivedViews() {}
function bindHomeDeskControls() {}
function bindDevelopDeskControls() {}
function bindInstructionsDeskControls() {}
function bindReportsDeskControls() {}
function renderIdleChat() {}
function setIdleSelectedMode() {}
function setIdleSelectedView() {}
function refreshIdleStatus() { fetch('/viewer/idlechat/status'); }
function refreshIdleLogs() { fetch('/viewer/idlechat/logs?limit=20'); }
function refreshViewerStatus() { fetch('/viewer/status'); }
function refreshDebugSystem() { fetch('/viewer/debug/system'); }
function registerWebMCPTools() {}
function connect() {}
function refreshLlmOpsStatus() {}
` + sourceBetween(viewerJs, 'function initLiveMode', 'function initEvidenceFromQuery') + `
globalThis.__initLiveMode = initLiveMode;
globalThis.__refreshOptionalPanelData = refreshOptionalPanelData;
globalThis.__setOptionalPanelRefreshIntervals = setOptionalPanelRefreshIntervals;
`;
  const context = vm.createContext({
    document,
    window: {location: {href: 'http://127.0.0.1:18790/viewer?mode=live'}},
    URL,
    setInterval(fn, ms) {
      intervals.push({fn: fn.name || 'anonymous', ms});
      return intervals.length;
    },
    fetch(url) {
      requested.push(String(url));
      return Promise.resolve({ok: true, json: () => Promise.resolve({ok: true})});
    },
    console: {error() {}, info() {}},
  });

  vm.runInContext(source, context);
  assert.equal(context.__initLiveMode(), true);
  context.__refreshOptionalPanelData();
  context.__setOptionalPanelRefreshIntervals();

  assert.equal(context.__switchedTab, 'timeline');
  assert.equal(bodyClassList.contains('live-mode'), true);
  assert.deepEqual(requested, []);
  assert.equal(requested.some((url) => url.startsWith('/optional/')), false);
  assert.equal(intervals.some((item) => optionalNames.includes(item.fn)), false);
});

test('viewer home tab does not start sandbox or verification diagnostics refreshes', () => {
  const viewerJs = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const requested = [];
  const elements = new Map();
  const bodyClasses = new Set();
  const document = {
    body: {classList: {
      add(name) { bodyClasses.add(name); },
      remove(name) { bodyClasses.delete(name); },
      contains(name) { return bodyClasses.has(name); },
    }},
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new FakeElement(id));
      return elements.get(id);
    },
  };
  const source = `
let activeViewerTab = 'home';
function refreshOpsData() { fetch('/optional/ops'); }
function refreshToolHarnessData() { fetch('/optional/tool-harness'); }
function refreshDCIData() { fetch('/optional/dci'); }
function refreshSandboxData() { fetch('/diagnostic/sandbox'); }
function refreshSkillGovernanceData() { fetch('/optional/skill-governance'); }
function refreshWorkstreamData() { fetch('/optional/workstream'); }
function refreshRevenueData() { fetch('/optional/revenue'); }
function refreshPersonaObservationData() { fetch('/optional/persona'); }
function refreshBrowserTraceAPIData() { fetch('/optional/browser-trace'); }
function refreshComplexityHotspotData() { fetch('/optional/complexity'); }
function refreshAIWorkflowData() { fetch('/optional/ai-workflow'); }
function refreshSuperAgentData() { fetch('/optional/superagent'); }
function refreshHeavyWorkerRuntimeDiagnostics() { fetch('/optional/heavy-worker'); }
function refreshKnowledgeMemoryData() { fetch('/optional/knowledge-memory'); }
function refreshRuntimeBlockedRouteData() { fetch('/diagnostic/runtime-blocked-routes'); }
function refreshEvidence() { fetch('/optional/evidence'); }
function refreshEvidenceSummary() { fetch('/optional/evidence-summary'); }
function refreshVerification() { fetch('/diagnostic/verification'); }
function refreshVerificationSummary() { fetch('/diagnostic/verification-summary'); }
function refreshMemorySnapshot() { fetch('/optional/memory-snapshot'); }
function refreshRecallTraces() { fetch('/optional/recall-traces'); }
` + sourceBetween(viewerJs, 'function shouldRefreshOptionalPanels', 'function initEvidenceFromQuery') + `
globalThis.__refreshOptionalPanelData = refreshOptionalPanelData;
`;
  const context = vm.createContext({
    document,
    fetch(url) {
      requested.push(String(url));
      return Promise.resolve({ok: true, json: () => Promise.resolve({ok: true})});
    },
  });

  vm.runInContext(source, context);
  context.__refreshOptionalPanelData();

  assert.equal(requested.includes('/diagnostic/sandbox'), false);
  assert.equal(requested.includes('/diagnostic/runtime-blocked-routes'), false);
  assert.equal(requested.includes('/diagnostic/verification'), false);
  assert.equal(requested.includes('/diagnostic/verification-summary'), false);
  assert.ok(requested.includes('/optional/ops'));
  assert.ok(requested.includes('/optional/evidence'));
});

test('viewer chat send ignores stale route alias and leaves routing to orchestrator', () => {
  const timelineJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/timeline.js', 'utf8');
  const store = new Map();
  const context = vm.createContext({
    document: {querySelectorAll: () => []},
    localStorage: {
      getItem: (key) => store.get(key) || null,
      setItem: (key, value) => store.set(key, String(value)),
      removeItem: (key) => store.delete(key),
    },
    applyRoleTargetToMessage: (message) => message,
  });
  vm.runInContext(timelineJs, context);

  vm.runInContext("localStorage.setItem('chatRouteAlias.selected', 'heavy')", context);
  const heavyReq = JSON.parse(vm.runInContext("JSON.stringify(buildViewerSendRequest('原因を調べて'))", context));
  assert.deepEqual(heavyReq, {message: '原因を調べて', to: 'mio'});
  assert.equal(store.has('chatRouteAlias.selected'), false);

  const explicitReq = JSON.parse(vm.runInContext("JSON.stringify(buildViewerSendRequest('/wild 物語にして'))", context));
  assert.deepEqual(explicitReq, {message: '/wild 物語にして'});
});

test('viewer chat send ignores runtime route aliases and leaves routing to orchestrator', () => {
  const timelineJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/timeline.js', 'utf8');
  const store = new Map();
  const context = vm.createContext({
    document: {querySelectorAll: () => []},
    localStorage: {
      getItem: (key) => store.get(key) || null,
      setItem: (key, value) => store.set(key, String(value)),
      removeItem: (key) => store.delete(key),
    },
    applyRoleTargetToMessage: (message) => message,
  });
  vm.runInContext(timelineJs, context);

  vm.runInContext(`syncChatRouteAliasesFromRuntimeConfig({
    enabled: true,
    worker_base_url: 'http://192.168.1.31:8082',
    worker_model: 'WorkerRuntime',
    heavy_base_url: 'http://192.168.1.31:8083',
    heavy_model: 'HeavyRuntime',
    wild_base_url: 'http://192.168.1.31:8084',
    wild_model: 'WildRuntime'
  })`, context);
  vm.runInContext("localStorage.setItem('chatRouteAlias.selected', 'heavy')", context);
  const heavyReq = JSON.parse(vm.runInContext("JSON.stringify(buildViewerSendRequest('原因を調べて'))", context));
  assert.deepEqual(heavyReq, {message: '原因を調べて', to: 'mio'});
  assert.equal(store.has('chatRouteAlias.selected'), false);
});

test('viewer chat send uses recipient contract instead of Shiro execution route', () => {
  const rolesJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/roles.js', 'utf8');
  const timelineJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/timeline.js', 'utf8');
  const store = new Map([['roleSelector.selectedTarget', 'shiro']]);
  const context = vm.createContext({
    document: {querySelectorAll: () => []},
    localStorage: {
      getItem: (key) => store.get(key) || null,
      setItem: (key, value) => store.set(key, String(value)),
      removeItem: (key) => store.delete(key),
    },
    renderRoleSelector: () => {},
    ROLE_TARGETS: [{id: 'mio'}, {id: 'shiro'}, {id: 'coder1'}],
  });
  vm.runInContext(rolesJs, context);
  vm.runInContext(timelineJs, context);

  const req = JSON.parse(vm.runInContext("JSON.stringify(buildViewerSendRequest('作業手順を相談したい'))", context));
  assert.deepEqual(req, {message: '作業手順を相談したい', to: 'shiro'});
  assert.equal(Object.hasOwn(req, 'model_alias'), false);
  assert.equal(Object.hasOwn(req, 'route_prefix'), false);
  assert.equal(req.message.startsWith('/ops '), false);
});

test('viewer chat legacy coder role target remains explicit route command', () => {
  const rolesJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/roles.js', 'utf8');
  const timelineJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/timeline.js', 'utf8');
  const store = new Map([['roleSelector.selectedTarget', 'coder1']]);
  const context = vm.createContext({
    document: {querySelectorAll: () => []},
    localStorage: {
      getItem: (key) => store.get(key) || null,
      setItem: (key, value) => store.set(key, String(value)),
      removeItem: (key) => store.delete(key),
    },
    renderRoleSelector: () => {},
    ROLE_TARGETS: [{id: 'mio'}, {id: 'shiro'}, {id: 'coder1'}],
  });
  vm.runInContext(rolesJs, context);
  vm.runInContext(timelineJs, context);

  const req = JSON.parse(vm.runInContext("JSON.stringify(buildViewerSendRequest('実装方針を出して'))", context));
  assert.deepEqual(req, {message: '/code1 実装方針を出して'});
  assert.equal(Object.hasOwn(req, 'to'), false);
  assert.equal(Object.hasOwn(req, 'model_alias'), false);
  assert.equal(Object.hasOwn(req, 'route_prefix'), false);
});

test('viewer starts selected llm before sending alias request', async () => {
  const timelineJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/timeline.js', 'utf8');
  const calls = [];
  const context = vm.createContext({
    document: {querySelectorAll: () => []},
    localStorage: {getItem: () => null, setItem: () => {}, removeItem: () => {}},
    fetch: async (url, opts = {}) => {
      calls.push({url, opts});
      if (url === '/viewer/llm-ops/health') {
        return {
          ok: true,
          json: async () => ({status: 'ok', daemon: 'llm-mgmt'}),
          text: async () => '{"status":"ok","daemon":"llm-mgmt"}',
        };
      }
      if (url === '/viewer/llm-ops/status') {
        return {
          ok: true,
          json: async () => ({roles: {Chat: {health_ok: true}, Heavy: {health_ok: false}}}),
        };
      }
      if (url === '/viewer/llm-ops/stop') {
        return {ok: true, text: async () => '{"stopped":["Worker","Wild"],"halted":true}'};
      }
      if (url === '/viewer/llm-ops/start') {
        return {ok: true, text: async () => '{"ok_all":true}'};
      }
      throw new Error('unexpected fetch: ' + url);
    },
    refreshLlmOpsStatus: () => {},
  });
  vm.runInContext(timelineJs, context);

  await vm.runInContext(`ensureViewerLLMReadyForRequest({
    message: '原因を調べて',
    model_alias: 'Heavy',
    base_url: 'http://127.0.0.1:8083',
    model: 'Heavy',
    route_prefix: '/analyze'
  })`, context);

  assert.equal(calls.length, 4);
  assert.equal(calls[0].url, '/viewer/llm-ops/health');
  assert.equal(calls[1].url, '/viewer/llm-ops/status');
  assert.equal(calls[2].url, '/viewer/llm-ops/stop');
  assert.equal(calls[2].opts.method, 'POST');
  assert.deepEqual(JSON.parse(calls[2].opts.body), {roles: ['Worker', 'Wild']});
  assert.equal(calls[3].url, '/viewer/llm-ops/start');
  assert.equal(calls[3].opts.method, 'POST');
  assert.deepEqual(JSON.parse(calls[3].opts.body), {selection: 'Heavy'});
});

test('viewer stops Worker and Heavy before starting Wild', async () => {
  const timelineJs = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/timeline.js', 'utf8');
  const calls = [];
  const context = vm.createContext({
    document: {querySelectorAll: () => []},
    localStorage: {getItem: () => null, setItem: () => {}, removeItem: () => {}},
    fetch: async (url, opts = {}) => {
      calls.push({url, opts});
      if (url === '/viewer/llm-ops/health') {
        return {ok: true, json: async () => ({status: 'ok'}), text: async () => '{"status":"ok"}'};
      }
      if (url === '/viewer/llm-ops/status') {
        return {ok: true, json: async () => ({roles: {Chat: {health_ok: true}, Worker: {health_ok: true}, Heavy: {health_ok: false}, Wild: {health_ok: false}}})};
      }
      if (url === '/viewer/llm-ops/stop') {
        return {ok: true, text: async () => '{"stopped":["Worker","Heavy"],"halted":true}'};
      }
      if (url === '/viewer/llm-ops/start') {
        return {ok: true, text: async () => '{"ok_all":true}'};
      }
      throw new Error('unexpected fetch: ' + url);
    },
    refreshLlmOpsStatus: () => {},
  });
  vm.runInContext(timelineJs, context);

  await vm.runInContext(`ensureViewerLLMReadyForRequest({
    message: '物語にして',
    model_alias: 'Wild',
    base_url: 'http://127.0.0.1:8084',
    model: 'Wild',
    route_prefix: '/wild'
  })`, context);

  assert.equal(calls[0].url, '/viewer/llm-ops/health');
  assert.equal(calls[1].url, '/viewer/llm-ops/status');
  assert.equal(calls[2].url, '/viewer/llm-ops/stop');
  assert.deepEqual(JSON.parse(calls[2].opts.body), {roles: ['Worker', 'Heavy']});
  assert.equal(calls[3].url, '/viewer/llm-ops/start');
  assert.deepEqual(JSON.parse(calls[3].opts.body), {selection: 'Wild'});
});
