import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';

const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');

test('viewer runtime-config loads voice chat fields into vdsState', () => {
  assert.match(js, /cfg\.voice_chat_stream_url/);
  assert.match(js, /cfg\.voice_chat_enabled/);
  assert.match(js, /cfg\.voice_input_mode/);
  assert.match(js, /vdsState\.voiceChatURL/);
  assert.match(js, /vdsState\.voiceChatEnabled/);
  assert.match(js, /vdsState\.voiceInputMode/);
});

test('viewer defaults voice input mode to stt_primary', () => {
  assert.match(js, /function normalizeVoiceInputMode\(/);
  assert.match(js, /return 'stt_primary'/);
});

test('viewer vds_sub opens voice-chat websocket with session.start control', () => {
  assert.match(js, /function sendVDSSessionStart\(\)/);
  assert.match(js, /type:\s*'session\.start'/);
  assert.match(js, /format:\s*'pcm16le'/);
  assert.match(js, /voice_input_mode:\s*'vds_sub'/);
  assert.match(js, /viewer_session_id:\s*'viewer'/);
  assert.match(js, /prompt:\s*VDS_DEFAULT_PROMPT/);
  assert.match(js, /vdsState\.ws\.send\(JSON\.stringify\(control\)\)/);
  assert.match(js, /function sendVDSSessionCommit\(\)/);
  assert.match(js, /type:\s*'session\.commit'/);
});

test('viewer vds_sub prompt asks for conversation, not audio summary', () => {
  assert.match(js, /const VDS_DEFAULT_PROMPT = 'あなたはMioです。/);
  assert.match(js, /"user_text":"ユーザー発話の復元文"/);
  assert.match(js, /"reply":"Mioとしての返事"/);
  assert.match(js, /user_textには返事や要約を入れず/);
  assert.match(js, /replyには文字起こし説明や音声ファイル要求を書かず/);
  assert.doesNotMatch(js, /聞こえた音声内容を日本語で2文以内に短く確認してください/);
});

test('viewer vds_sub sends binary pcm through voice-chat websocket', () => {
  assert.match(js, /function sendVDSAudioChunk\(pcm16\)/);
  assert.match(js, /vdsState\.ws\.send\(chunk\.buffer\)/);
});

test('viewer vds_sub uses a lower VAD threshold than STT text capture', () => {
  assert.match(js, /const STT_VAD_START_LEVEL = 12/);
  assert.match(js, /const STT_VAD_END_LEVEL = 8/);
  assert.match(js, /const VDS_SILENCE_END_MS = 500/);
  assert.match(js, /const VDS_VAD_START_LEVEL = 4/);
  assert.match(js, /const VDS_VAD_END_LEVEL = 3/);
  const vadStart = js.indexOf('function handleVDSVADFrame');
  assert.ok(vadStart >= 0, 'handleVDSVADFrame not found');
  const vadEnd = js.indexOf('function stopVDSUtteranceBySilence', vadStart);
  assert.ok(vadEnd > vadStart, 'stopVDSUtteranceBySilence block not found');
  const vadSource = js.slice(vadStart, vadEnd);
  assert.match(vadSource, /VDS_VAD_END_LEVEL/);
  assert.match(vadSource, /VDS_VAD_START_LEVEL/);
  assert.match(vadSource, /VDS_SILENCE_END_MS/);
});

test('viewer vds_sub does not call sendViewerMessage on llm.final success path', () => {
  const handleStart = js.indexOf('function handleVDSFinalMessage(msg)');
  assert.ok(handleStart >= 0, 'handleVDSFinalMessage not found');
  const handleEnd = js.indexOf('function toggleVDS()', handleStart);
  assert.ok(handleEnd > handleStart, 'toggleVDS block not found');
  const handleSource = js.slice(handleStart, handleEnd);
  assert.doesNotMatch(handleSource, /sendViewerMessage\(/);
  assert.doesNotMatch(handleSource, /handleSTTFinalText\(/);
  assert.doesNotMatch(handleSource, /\bsend\(\)/);
});

test('viewer vds_sub enters cooldown after llm.final without stopping browser mic', () => {
  const handleStart = js.indexOf('function handleVDSFinalMessage(msg)');
  assert.ok(handleStart >= 0, 'handleVDSFinalMessage not found');
  const handleEnd = js.indexOf('function renderVDSDeltaResponse(reason)', handleStart);
  assert.ok(handleEnd > handleStart, 'renderVDSDeltaResponse block not found');
  const handleSource = js.slice(handleStart, handleEnd);
  assert.match(handleSource, /enterVDSCooldown\('llm\.final'\)/);
  assert.doesNotMatch(handleSource, /completeVDSUtteranceStop\('llm\.final'\)/);
  assert.doesNotMatch(handleSource, /abortVDSImmediately\('llm\.final'\)/);
});

test('viewer vds_sub records llm.delta in debug trace without local chat bubble', () => {
  assert.match(js, /function renderVDSDeltaResponse\(reason\)/);
  assert.match(js, /vdsState\.llmDeltaText \+= String\(msg\.text \|\| ''\)/);
  assert.doesNotMatch(js, /updateVDSCaption\('partial', vdsState\.llmDeltaText\)/);
  assert.match(js, /renderVDSDeltaResponse\('stream'\)/);
  assert.match(js, /scheduleVDSDeltaIdleFinalize\(\)/);
  const renderStart = js.indexOf('function renderVDSDeltaResponse(reason)');
  assert.ok(renderStart >= 0, 'renderVDSDeltaResponse not found');
  const renderEnd = js.indexOf('function finalizeVDSDeltaResponse(reason)', renderStart);
  assert.ok(renderEnd > renderStart, 'finalizeVDSDeltaResponse block not found');
  const renderSource = js.slice(renderStart, renderEnd);
  assert.match(renderSource, /pushDebugTrace\('vds'/);
  assert.doesNotMatch(renderSource, /vds-local-response/);
  assert.doesNotMatch(renderSource, /chat\.appendChild/);
});

test('viewer vds_sub renders transcript events, not Mio response, in voice caption area', () => {
  assert.match(js, /function clearVDSCaption\(\)/);
  assert.match(js, /function updateVDSCaption\(type, text\)/);
  assert.match(js, /function renderVDSFinalTranscriptToChat\(text, msg\)/);
  assert.match(js, /sttState\.partialCaptionText = captionText/);
  assert.match(js, /sttState\.finalCaptionText = captionText/);
  assert.match(js, /msg\.type === 'transcript\.delta' \|\| msg\.type === 'transcript\.partial'/);
  assert.match(js, /updateVDSCaption\('partial', msg\.text\)/);
  assert.match(js, /msg\.type === 'transcript\.final' && msg\.text/);
  assert.match(js, /updateVDSCaption\('final', msg\.text\)/);
  assert.match(js, /renderVDSFinalTranscriptToChat\(msg\.text, msg\)/);
  assert.match(js, /type:\s*'message\.received'/);
  assert.match(js, /from:\s*'user'/);
  assert.doesNotMatch(js, /updateVDSCaption\('final', finalText\)/);
});

test('viewer timeline scroll keeps chat at bottom instead of resetting main upward', () => {
  const start = js.indexOf('function scrollToBottom(force)');
  assert.ok(start >= 0, 'scrollToBottom not found');
  const end = js.indexOf('if (latestBtn)', start);
  assert.ok(end > start, 'latest button block not found after scrollToBottom');
  const source = js.slice(start, end);
  assert.match(source, /chat\.scrollTop = chat\.scrollHeight/);
  assert.match(source, /mainEl\.scrollTop = mainEl\.scrollHeight/);
  assert.doesNotMatch(source, /mainEl\.scrollTop = 0/);
});

test('viewer vds_sub final timeout can finalize received delta', () => {
  assert.match(js, /function finalizeVDSDeltaResponse\(reason\)/);
  assert.match(js, /if \(finalizeVDSDeltaResponse\('timeout'\)\) return/);
  assert.match(js, /renderVDSDeltaResponse\('delta_idle'\)/);
  assert.match(js, /const VDS_DELTA_IDLE_FINALIZE_MS = 2500/);
  assert.match(js, /enterVDSCooldown\('local_delta'\)/);
  assert.match(js, /detail:\s*'local_delta:' \+ String\(reason \|\| 'delta'\)/);
});

test('viewer vds_sub enters cooldown on timeout and error paths', () => {
  assert.match(js, /enterVDSCooldown\('timeout'\)/);
  assert.match(js, /enterVDSCooldown\('error'\)/);
  const cooldownStart = js.indexOf('function enterVDSCooldown(reason)');
  assert.ok(cooldownStart >= 0, 'enterVDSCooldown not found');
  const cooldownEnd = js.indexOf('function scheduleVDSFinalWaitTimeout()', cooldownStart);
  assert.ok(cooldownEnd > cooldownStart, 'scheduleVDSFinalWaitTimeout block not found');
  const cooldownSource = js.slice(cooldownStart, cooldownEnd);
  assert.match(cooldownSource, /completeVDSUtteranceStop\(reason\)/);
  assert.match(cooldownSource, /vdsState\.cooldownUntilMS = Date\.now\(\) \+ VDS_COOLDOWN_MS/);
  assert.doesNotMatch(cooldownSource, /vdsState\.isRecording = false/);
});

test('viewer vds_sub suppresses new sessions during cooldown', () => {
  assert.match(js, /function isVDSInCooldown\(now\)/);
  const vadStart = js.indexOf('function handleVDSVADFrame');
  assert.ok(vadStart >= 0, 'handleVDSVADFrame not found');
  const vadEnd = js.indexOf('function stopVDSUtteranceBySilence', vadStart);
  assert.ok(vadEnd > vadStart, 'stopVDSUtteranceBySilence block not found');
  const vadSource = js.slice(vadStart, vadEnd);
  assert.match(vadSource, /if \(isVDSInCooldown\(now\)\)/);
  assert.match(vadSource, /return/);
  const beginStart = js.indexOf('function beginVDSUtterance(reason)');
  assert.ok(beginStart >= 0, 'beginVDSUtterance not found');
  const beginEnd = js.indexOf('function handleVDSVADFrame', beginStart);
  assert.ok(beginEnd > beginStart, 'handleVDSVADFrame block not found');
  const beginSource = js.slice(beginStart, beginEnd);
  assert.match(beginSource, /if \(isVDSInCooldown\(Date\.now\(\)\)\) return false/);
});

test('viewer vds_sub has min and max utterance guards', () => {
  assert.match(js, /const VDS_MIN_SPEECH_MS = 250/);
  assert.match(js, /const VDS_MAX_UTTERANCE_MS = 30000/);
  assert.match(js, /discardVDSUtterance\('too_short:' \+ String\(speechMS\) \+ 'ms'\)/);
  assert.match(js, /commitVDSUtterance\('max_duration'\)/);
});

test('viewer vds_sub allows barge-in by interrupting assistant output on speech start', () => {
  const beginStart = js.indexOf('function beginVDSUtterance(reason)');
  assert.ok(beginStart >= 0, 'beginVDSUtterance not found');
  const beginEnd = js.indexOf('function handleVDSVADFrame', beginStart);
  assert.ok(beginEnd > beginStart, 'handleVDSVADFrame block not found');
  const beginSource = js.slice(beginStart, beginEnd);
  assert.match(beginSource, /interruptChatOutputForUserInput\('vds_voice_start'\)/);
  assert.match(beginSource, /interruptIdleChatForUserInput\('vds_voice_start'\)/);
  assert.match(beginSource, /connectVDSWebSocket\(\)/);
});

test('viewer vds_sub does not abort while waiting for final', () => {
  const stopStart = js.indexOf('function stopVDS()');
  assert.ok(stopStart >= 0, 'stopVDS not found');
  const stopEnd = js.indexOf('function abortVDSImmediately', stopStart);
  assert.ok(stopEnd > stopStart, 'abortVDSImmediately block not found');
  const stopSource = js.slice(stopStart, stopEnd);
  assert.match(stopSource, /if \(vdsState\.isStopping\) return/);
});

test('viewer mic button routes through voice input mode dispatcher', () => {
  assert.match(js, /function toggleVoiceInput\(\)/);
  assert.match(js, /if \(isVDSSubMode\(\)\)/);
  const clickStart = js.indexOf('function handleMicButtonClick()');
  const clickEnd = js.indexOf('if (micBtn) micBtn.addEventListener', clickStart);
  assert.ok(clickStart >= 0 && clickEnd > clickStart, 'handleMicButtonClick block not found');
  const clickSource = js.slice(clickStart, clickEnd);
  assert.match(clickSource, /interruptIdleChatForUserInput\('stt_button'\)/);
  assert.match(clickSource, /toggleVoiceInput\(\)/);
  assert.match(js, /if \(micBtn\) micBtn\.addEventListener\('click', handleMicButtonClick\)/);
});

test('viewer stt_primary keeps STT start control unchanged', () => {
  assert.match(js, /function sendSTTStartControl\(\)/);
  assert.match(js, /type:\s*'start'/);
  assert.doesNotMatch(js, /sendSTTStartControl\([\s\S]*session\.start/);
});
