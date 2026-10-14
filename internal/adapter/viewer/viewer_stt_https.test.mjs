import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

test('viewer STT websocket can be configured for Mac streaming endpoint', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  assert.match(js, /fetch\('\/viewer\/runtime-config'/);
  assert.match(js, /cfg\.stt_stream_url/);
  assert.match(js, /voiceBridgeURL:\s*`\$\{window\.location\.protocol === 'https:' \? 'wss:' : 'ws:'\}\/\/\$\{window\.location\.host\}\/stt`/);
});

test('viewer microphone input is the STT production entrypoint', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  assert.match(js, /navigator\.mediaDevices\.getUserMedia\(\{/);
  assert.match(js, /sampleRate:\s*16000/);
  assert.match(js, /chunkSamples:\s*1600/);
  assert.match(js, /resampleToPCM16\(pcm, sttState\.inputSampleRate \|\| 48000, 16000\)/);
  assert.match(js, /sttState\.ws\.send\(chunk\.buffer\)/);
  assert.match(js, /sttState\.ws\.close\(\)/);
  assert.match(js, /sendViewerMessage\(message\)/);
});

test('viewer records latency metrics for STT, LLM, TTS, and network paths', () => {
  const html = fs.readFileSync('internal/adapter/viewer/viewer.html', 'utf8');
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  assert.match(html, /id="debugLatencySummary"/);
  assert.match(js, /function recordLatencyMetric\(/);
  assert.match(js, /function ingestLatencyMetricEvent\(/);
  assert.match(js, /recordLatencyMetric\('stt', 'speech_start'/);
  assert.match(js, /recordLatencyMetric\('stt', 'final_received'/);
  assert.match(js, /recordLatencyMetric\('network', 'viewer_send_start'/);
  assert.match(js, /recordLatencyMetric\('network', 'viewer_send_response'/);
  assert.match(js, /agent_thinking_received/);
  assert.match(js, /audio_chunk_received/);
  assert.match(js, /recordLatencyMetric\('tts', 'audio_play_start'/);
  assert.match(js, /ev\.type === 'metrics\.latency'/);
});

test('viewer sends STT start control before streaming audio chunks', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  assert.match(js, /function sendSTTStartControl\(\)/);
  assert.match(js, /type:\s*'start'/);
  assert.match(js, /sample_rate:\s*sampleRate/);
  assert.match(js, /channels:\s*1/);
  assert.match(js, /format:\s*'pcm_s16le'/);
  assert.match(js, /sttState\.ws\.send\(JSON\.stringify\(control\)\)/);

  const onopenStart = js.indexOf('sttState.ws.onopen = () => {');
  const onopenEnd = js.indexOf('sttState.ws.onmessage =', onopenStart);
  assert.ok(onopenStart >= 0 && onopenEnd > onopenStart, 'STT onopen block not found');
  const onopenSource = js.slice(onopenStart, onopenEnd);
  assert.match(onopenSource, /sendSTTStartControl\(\);/);

  const startControl = js.indexOf('function sendSTTStartControl()');
  const sendChunk = js.indexOf('function sendSTTAudioChunk(pcm16)');
  assert.ok(sendChunk >= 0 && startControl > sendChunk, 'start control helper should be near chunk sender');
});

test('viewer voice chat sends final text only in timeline chat without stopping capture on idle view', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  assert.match(js, /let activeViewerTab = 'home'/);
  const allowedStart = js.indexOf('function isVoiceChatAllowed()');
  const allowedEnd = js.indexOf('function normalizeVoiceInputMode', allowedStart);
  assert.ok(allowedStart >= 0 && allowedEnd > allowedStart, 'isVoiceChatAllowed block not found');
  const allowedSource = js.slice(allowedStart, allowedEnd);
  assert.match(allowedSource, /return activeViewerTab === 'timeline'/);
  assert.match(js, /function ensureVoiceChatForMobileControl\(\) \{/);
  assert.match(js, /switchTab\('timeline'\);/);
  const switchTabStart = js.indexOf('function switchTab(tab) {');
  const switchTabEnd = js.indexOf('function switchAdjacentPanel', switchTabStart);
  assert.ok(switchTabStart >= 0 && switchTabEnd > switchTabStart, 'switchTab block not found');
  const switchTabSource = js.slice(switchTabStart, switchTabEnd);
  assert.doesNotMatch(switchTabSource, /stopSTT\(\)/);
  assert.match(js, /btn\.disabled = \(!!microphoneUnavailable && !sttState\.isRecording\) \|\| isSTTTestRecording\(\)/);
  assert.match(js, /if \(!ensureVoiceChatForMobileControl\(\)\) \{\s*showToast\('音声入力は通常チャットでのみ有効です', 'error'\);/);
  assert.match(js, /if \(!isVoiceChatAllowed\(\)\) \{\s*console\.warn\('\[STT\] Final ignored outside normal chat:', finalText\);/);
});

test('viewer marks microphone unavailable on insecure origins before getUserMedia', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const css = fs.readFileSync('internal/adapter/viewer/assets/css/viewer.css', 'utf8');
  assert.match(js, /function getSTTMicrophoneUnavailableReason\(\)/);
  assert.match(js, /window\.isSecureContext === false/);
  assert.match(js, /HTTPSまたはlocalhostでViewerを開いてください/);
  assert.match(js, /typeof navigator === 'undefined' \|\| !navigator\.mediaDevices \|\| typeof navigator\.mediaDevices\.getUserMedia !== 'function'/);
  assert.match(js, /btn\.disabled = \(!!microphoneUnavailable && !sttState\.isRecording\) \|\| isSTTTestRecording\(\)/);
  assert.match(js, /Mic: unavailable/);
  assert.match(js, /describeSTTActionError\('STT microphone start unavailable', microphoneUnavailable\)/);
  assert.match(css, /\.stt-state\.mic-unavailable/);
});

test('viewer treats Mac STT partial events as recognition drafts without chat fallback', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  assert.match(js, /type !== 'draft' && type !== 'partial' && type !== 'final'/);
  assert.match(js, /\(msg\.type === 'draft' \|\| msg\.type === 'partial'\) && extractSTTMessageText\(msg\)/);
  assert.match(js, /const draftText = extractSTTMessageText\(msg\)/);
  assert.match(js, /sttState\.lastRecognitionText = String\(draftText \|\| ''\)\.trim\(\);/);
  assert.doesNotMatch(js, /handleSTTFinalText\(pendingText\)/);
  assert.doesNotMatch(js, /recordSTTCaptureEvent\('final', pendingText\)/);
});

test('viewer renders partial and final STT captions outside the chat input', () => {
  const html = fs.readFileSync('internal/adapter/viewer/viewer.html', 'utf8');
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const css = fs.readFileSync('internal/adapter/viewer/assets/css/viewer.css', 'utf8');
  assert.match(html, /id="sttCaption"/);
  assert.match(html, /class="input-text-stack"/);
  assert.match(html, /class="input-side"/);
  assert.match(html, /class="input-compose"/);
  assert.match(html, /id="sttCaptionLabel"/);
  assert.match(html, /id="sttCaptionText"/);
  assert.match(html, /暫定文字列/);
  assert.match(js, /const sttCaptionEl = document\.getElementById\('sttCaption'\)/);
  assert.match(js, /const sttCaptionLabelEl = document\.getElementById\('sttCaptionLabel'\)/);
  assert.match(js, /const sttCaptionTextEl = document\.getElementById\('sttCaptionText'\)/);
  assert.match(js, /function updateSTTCaption\(\)/);
  assert.match(js, /setCaption\('確定文字列', finalText, 'stt-caption has-text final'\)/);
  assert.match(js, /setCaption\('暫定文字列', partialText, 'stt-caption has-text draft'\)/);
  assert.match(js, /sttState\.partialCaptionText = sttState\.lastRecognitionText/);
  assert.match(js, /sttState\.finalCaptionText = sttState\.lastRecognitionText/);
  assert.match(css, /\.input-text-stack/);
  assert.match(css, /\.stt-caption\.draft/);
  assert.match(css, /\.stt-caption\.final/);
});

test('viewer renders STT errors in the caption area without keeping stale partial text', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8').replace(/\r\n/g, '\n');
  const css = fs.readFileSync('internal/adapter/viewer/assets/css/viewer.css', 'utf8');
  assert.match(js, /errorCaptionText:\s*''/);
  assert.match(js, /setCaption\('STT error', errorText, 'stt-caption has-text error'\)/);
  assert.match(js, /function setSTTCaptionError\(text\)/);
  assert.match(js, /setSTTCaptionError\(sttErrorText\)/);
  assert.match(js, /setSTTCaptionError\(sttState\.captureActionError\)/);
  assert.match(js, /sttState\.partialCaptionText = '';/);
  assert.match(js, /sttState\.finalCaptionText = '';/);
  assert.match(css, /\.stt-caption\.error/);

  const parseStart = js.indexOf("sttState.captureActionError = describeSTTActionError('STT message parse unavailable'");
  const wsStart = js.indexOf("sttState.captureActionError = describeSTTActionError(\n      'STT websocket unavailable'");
  const timeoutStart = js.indexOf("sttState.captureActionError = describeSTTActionError('STT final unavailable'");
  assert.ok(parseStart >= 0, 'parse error path not found');
  assert.ok(wsStart >= 0, 'websocket error path not found');
  assert.ok(timeoutStart >= 0, 'final timeout path not found');
  assert.match(js.slice(parseStart, parseStart + 220), /setSTTCaptionError\(sttState\.captureActionError\)/);
  assert.match(js.slice(wsStart, wsStart + 260), /setSTTCaptionError\(sttState\.captureActionError\)/);
  assert.match(js.slice(timeoutStart, timeoutStart + 260), /setSTTCaptionError\(sttState\.captureActionError\)/);

  const serverErrorStart = js.indexOf("} else if (msg.type === 'error') {");
  const serverErrorEnd = js.indexOf('      } catch (err) {', serverErrorStart);
  assert.ok(serverErrorStart >= 0 && serverErrorEnd > serverErrorStart, 'server error path not found');
  const serverErrorSource = js.slice(serverErrorStart, serverErrorEnd);
  assert.doesNotMatch(serverErrorSource, /handleSTTFinalText/);
});

test('viewer treats STT error as terminal response during final wait', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const errorStart = js.indexOf("} else if (msg.type === 'error') {");
  const errorEnd = js.indexOf('      } catch (err) {', errorStart);
  assert.ok(errorStart >= 0 && errorEnd > errorStart, 'server error path not found');
  const errorSource = js.slice(errorStart, errorEnd);
  assert.match(errorSource, /sttState\.finalReceived = true;/);
  assert.match(errorSource, /clearSTTFinalWaitTimer\(\);/);
  assert.match(errorSource, /setSTTCaptionError\(sttErrorText\)/);
  assert.match(errorSource, /showToast\('認識エラー', 'error'\)/);
  assert.match(errorSource, /if \(sttState\.isStopping && sttState\.ws && sttState\.ws\.readyState === WebSocket\.OPEN\) \{\s*sttState\.ws\.close\(\);/);
  assert.doesNotMatch(errorSource, /handleSTTFinalText/);
});

test('viewer sends STT stop control and waits for final or error before closing', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  assert.match(js, /function sendSTTStopControl\(reason\)/);
  assert.match(js, /const STT_SILENCE_END_MS = 700/);
  assert.match(js, /const STT_STOP_TAIL_SILENCE_MS = 300/);
  assert.match(js, /function handleSTTVADFrame\(pcm16, level\)/);
  assert.match(js, /function stopSTTUtteranceBySilence\(\)/);
  assert.match(js, /function sendSTTStopTailSilence\(\)/);
  assert.match(js, /stop tail silence/);
  assert.match(js, /sttState\.ws\.send\(JSON\.stringify\(\{ type: 'stop' \}\)\)/);
  assert.match(js, /recordSTTCaptureEvent\('stop', reasonText\)/);
  assert.match(js, /function scheduleSTTFinalWaitTimeout\(\)/);
  assert.match(js, /const STT_FINAL_WAIT_TIMEOUT_MS = 90000/);
  assert.match(js, /}, STT_FINAL_WAIT_TIMEOUT_MS\)/);
  assert.match(js, /timed out waiting for final/);
  assert.doesNotMatch(js, /function finalizeSTTLocalDraft\(reason\)/);
  assert.match(js, /function completeSTTStop\(\)/);

  const stopStart = js.indexOf('function stopSTT()');
  const stopEnd = js.indexOf('function completeSTTStop()', stopStart);
  assert.ok(stopStart >= 0 && stopEnd > stopStart, 'stopSTT block not found');
  const stopSource = js.slice(stopStart, stopEnd);
  assert.match(js, /stopSTTUtteranceBySilence\(\)/);
  assert.match(js, /flushSTTAudioChunkBuffer\(\);/);
  assert.match(js, /sendSTTStopTailSilence\(\);/);
  assert.match(js, /sendSTTStopControl\('silence '/);
  assert.match(js, /scheduleSTTFinalWaitTimeout\(\);/);
  assert.doesNotMatch(stopSource, /handleSTTFinalText/);
  assert.doesNotMatch(stopSource, /finalizeSTTLocalDraft/);

  const timeoutStart = js.indexOf('function scheduleSTTFinalWaitTimeout()');
  const timeoutEnd = js.indexOf('function handleSTTFinalText(text)', timeoutStart);
  assert.ok(timeoutStart >= 0 && timeoutEnd > timeoutStart, 'final wait timeout block not found');
  const timeoutSource = js.slice(timeoutStart, timeoutEnd);
  assert.doesNotMatch(timeoutSource, /finalizeSTTLocalDraft/);
  assert.match(timeoutSource, /timed out waiting for final/);

  const finalStart = js.indexOf("} else if (msg.type === 'final') {");
  const finalEnd = js.indexOf("} else if (msg.type === 'reply_reset')", finalStart);
  assert.ok(finalStart >= 0 && finalEnd > finalStart, 'final message block not found');
  const finalSource = js.slice(finalStart, finalEnd);
	assert.match(finalSource, /handleSTTFinalText\(sttState\.lastRecognitionText\)/);
  assert.match(finalSource, /sttState\.ws\.close\(\)/);
});

test('viewer uses VAD silence under one second instead of button endpoint stop', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  assert.match(js, /vadSpeechActive:\s*false/);
  assert.match(js, /vadSilenceStartedAt:\s*0/);
  assert.match(js, /const STT_VAD_START_LEVEL = 12/);
  assert.match(js, /const STT_VAD_END_LEVEL = 8/);
  assert.match(js, /const STT_SILENCE_END_MS = 700/);
  assert.match(js, /now - sttState\.vadSilenceStartedAt >= STT_SILENCE_END_MS/);
  assert.match(js, /stopSTTUtteranceBySilence\(\)/);
  const clickStart = js.indexOf("micBtn.addEventListener('click'");
  const clickEnd = js.indexOf('});', clickStart);
  assert.ok(clickStart >= 0 && clickEnd > clickStart, 'mic click handler not found');
  const clickSource = js.slice(clickStart, clickEnd);
  assert.doesNotMatch(clickSource, /if \(sttState\.isRecording\) \{\s*stopSTT\(\);/);
});

test('viewer interrupts LLM output when VAD detects a new utterance', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  assert.match(js, /resetChat: resetChatInternal/);
  assert.match(js, /function resetChatInternal\(reason\)/);
  assert.match(js, /rememberInterruptedChatOutput\(\{\s*sessionId: centralTTSSpeech\.sessionId,\s*publicPlaybackRef: centralTTSSpeech\.publicPlaybackRef,/);
  const beginStart = js.indexOf('function beginSTTUtterance');
  const beginEnd = js.indexOf('function handleSTTVADFrame', beginStart);
  assert.ok(beginStart >= 0 && beginEnd > beginStart, 'beginSTTUtterance block not found');
  const beginSource = js.slice(beginStart, beginEnd);
  assert.match(beginSource, /interruptChatOutputForUserInput\('stt_voice_start'\)/);
  assert.match(beginSource, /interruptIdleChatForUserInput\('stt_voice_start'\)/);
  const vadStart = js.indexOf('function handleSTTVADFrame');
  const vadEnd = js.indexOf('function sendSTTAudioChunk', vadStart);
  assert.ok(vadStart >= 0 && vadEnd > vadStart, 'handleSTTVADFrame block not found');
  const vadSource = js.slice(vadStart, vadEnd);
  assert.match(vadSource, /sttState\.pendingSpeechRestart = true/);
  assert.match(vadSource, /interruptChatOutputForUserInput\('stt_voice_resume'\)/);
  assert.match(vadSource, /interruptIdleChatForUserInput\('stt_voice_resume'\)/);
});

test('viewer logs sent audio and STT event timing for stream correlation', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  assert.match(js, /captureEventID:\s*''/);
  assert.match(js, /sentAudioSamples:\s*0/);
  assert.match(js, /sentAudioBytes:\s*0/);
  assert.match(js, /sentAudioFrames:\s*0/);
  assert.match(js, /'event_id: ' \+ \(sttState\.captureEventID \|\| '\(unknown\)'\)/);
  assert.match(js, /'sent_audio: ' \+ formatSTTSentAudioSummary\(\)/);
  assert.ok(js.includes("item.payload.trim().split(' / ')[0].trim()"));
  assert.match(js, /function formatSTTServerEventPayload\(msg, fallbackText\)/);
  assert.match(js, /range=' \+ String\(msg\.start_ms\) \+ '-' \+ String\(msg\.end_ms\) \+ 'ms'/);
  assert.match(js, /duration=' \+ String\(msg\.duration\) \+ 's'/);
  assert.match(js, /recordSTTCaptureEvent\('audio_sent'/);
  assert.match(js, /recordSTTAudioSent\(chunk\.length\)/);
  assert.match(js, /sttState\.captureEventID = String\(msg\.event_id\)\.trim\(\);/);
  assert.match(js, /if \(msg\.type !== 'progress'\) \{\s*recordSTTCaptureEvent\(msg\.type, formatSTTServerEventPayload\(msg, eventText\)\);/);
});

test('viewer preserves received STT final when later stop or error arrives', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  assert.match(js, /finalReceived:\s*false/);
  assert.match(js, /sttState\.finalReceived = false;/);

  const finalStart = js.indexOf("} else if (msg.type === 'final') {");
  const finalEnd = js.indexOf("} else if (msg.type === 'reply_reset')", finalStart);
  assert.ok(finalStart >= 0 && finalEnd > finalStart, 'final message block not found');
  const finalSource = js.slice(finalStart, finalEnd);
  assert.match(finalSource, /sttState\.finalReceived = true;/);
  assert.match(finalSource, /clearSTTFinalWaitTimer\(\);/);
  assert.match(finalSource, /handleSTTFinalText\(sttState\.lastRecognitionText\)/);

  const errorStart = js.indexOf("} else if (msg.type === 'error') {");
  const errorEnd = js.indexOf('        }', errorStart);
  assert.ok(errorStart >= 0 && errorEnd > errorStart, 'server error path not found');
  const errorSource = js.slice(errorStart, errorEnd);
  assert.match(errorSource, /if \(sttState\.finalReceived\)/);
  assert.doesNotMatch(errorSource, /sttState\.finalCaptionText = ''/);

  const stopStart = js.indexOf('function stopSTT()');
  const stopEnd = js.indexOf('function completeSTTStop()', stopStart);
  assert.ok(stopStart >= 0 && stopEnd > stopStart, 'stopSTT block not found');
  const stopSource = js.slice(stopStart, stopEnd);
  const finalReceivedBranch = stopSource.indexOf('if (sttState.finalReceived)');
  const openStopBranch = stopSource.indexOf('sendSTTStopControl();', finalReceivedBranch);
  assert.ok(finalReceivedBranch >= 0, 'finalReceived stop branch not found');
  assert.ok(openStopBranch > finalReceivedBranch, 'normal open stop branch not found after finalReceived branch');
  const finalReceivedSource = stopSource.slice(finalReceivedBranch, openStopBranch);
  assert.match(finalReceivedSource, /sttState\.ws\.close\(\);/);
  assert.doesNotMatch(finalReceivedSource, /sendSTTStopControl\(\);/);
});

test('viewer sends only RenCrow_STT final text to chat', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  assert.match(js, /handleSTTFinalText\(sttState\.lastRecognitionText\)/);
});

test('viewer STT message handling is not blocked by debug panel rendering', () => {  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  assert.match(js, /function renderSTTDebugPanelsSafely\(\)/);
  assert.match(js, /try \{\s*renderDebugPanels\(\);/);
  assert.match(js, /console\.warn\('\[STT\] Debug panel render skipped:'/);

  const msgStart = js.indexOf('sttState.ws.onmessage = (event) => {');
  const partialStart = js.indexOf("if ((msg.type === 'draft' || msg.type === 'partial') && extractSTTMessageText(msg))", msgStart);
  assert.ok(msgStart >= 0 && partialStart > msgStart, 'STT message handler not found');
  const preActionSource = js.slice(msgStart, partialStart);
  assert.match(preActionSource, /renderSTTDebugPanelsSafely\(\);/);
  assert.doesNotMatch(preActionSource, /renderDebugPanels\(\);/);
});

test('viewer STT autotest uses the same-origin RenCrow_STT websocket route', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  assert.match(js, /function buildSTTWebSocketURLForAutoTest\(\)/);
  assert.match(js, /location\.host \+ '\/stt'/);
  assert.match(js, /ws_url: wsURL/);
});

test('viewer renders live microphone input level on the mic button', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const css = fs.readFileSync('internal/adapter/viewer/assets/css/viewer.css', 'utf8');
  assert.match(js, /inputLevel:\s*0/);
  assert.match(js, /function calculateSTTInputLevel\(pcm16\)/);
  assert.match(js, /const level = calculateSTTInputLevel\(pcm16\)/);
  assert.match(js, /updateSTTInputLevel\(level\)/);
  assert.match(js, /handleSTTVADFrame\(pcm16, level\)/);
  assert.match(js, /btn\.style\.setProperty\('--mic-level-pct'/);
  assert.match(js, /updateSTTInputLevel\(0\);/);
  assert.match(css, /#micBtn\.has-level/);
  assert.match(css, /var\(--mic-level-pct\)/);
});

test('viewer exposes Ops test recording mode that trims silence and saves Latest WAV', () => {
  const html = fs.readFileSync('internal/adapter/viewer/viewer.html', 'utf8');
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  assert.match(html, /stt_test_record_utils\.js/);
  assert.match(html, /id="sttTestRecordStartBtn"/);
  assert.match(html, /id="sttTestRecordStopBtn"/);
  assert.match(html, /id="sttTestRecordStatus"/);
  assert.match(html, /id="sttTestRecordTranscript"/);
  assert.match(html, /STT テスト録音/);
  assert.match(js, /const sttTestRecordState = \{/);
  assert.match(js, /function extractSTTAutoTestTranscript\(result\)/);
  assert.match(js, /function isSTTTestRecording\(\)/);
  assert.match(js, /function startSTTTestRecording\(\)/);
  assert.match(js, /function stopSTTTestRecordingAndSave\(\)/);
  assert.match(js, /edgeOnly: true/);
  assert.match(js, /await persistSTTRawWavToServer\(rawWav\)/);
  assert.match(js, /\/viewer\/stt\/wav\/raw/);
  assert.match(js, /await persistSTTWavToServer\(wav\)/);
  assert.match(js, /await runSTTAutoTest\(\{ ws_rounds: 1 \}\)/);
  assert.match(js, /sttTestRecordState\.lastTranscript = transcript/);
  assert.match(js, /interruptChatOutputForUserInput\('stt_test_record'\)/);
  assert.match(js, /interruptIdleChatForUserInput\('stt_test_record'\)/);
  assert.match(js, /テスト録音中は通常マイクを使えません/);
  assert.doesNotMatch(js.slice(js.indexOf('function startSTTTestRecording()'), js.indexOf('async function stopSTTTestRecordingAndSave()')), /connectSTTWebSocket/);
  assert.doesNotMatch(js.slice(js.indexOf('function startSTTTestRecording()'), js.indexOf('async function stopSTTTestRecordingAndSave()')), /handleSTTVADFrame/);
});

// --- utterance-level recognition: preroll and utterance_id ---------------------------------

function sliceFunctions(js, from, to) {
  const start = js.indexOf(`function ${from}(`);
  const end = js.indexOf(`function ${to}(`, start);
  assert.ok(start >= 0 && end > start, `${from}..${to} not found`);
  return js.slice(start, end);
}

function loadSTTUtils() {
  const sandbox = {};
  vm.createContext(sandbox);
  vm.runInContext(fs.readFileSync('internal/adapter/viewer/assets/js/stt_utterance_utils.js', 'utf8'), sandbox);
  return sandbox;
}

test('viewer loads the utterance utils before viewer.js', () => {
  const html = fs.readFileSync('internal/adapter/viewer/viewer.html', 'utf8');
  assert.match(html, /stt_utterance_utils\.js/);
  assert.ok(html.indexOf('stt_utterance_utils.js') < html.indexOf('/viewer/assets/js/viewer.js?v='), 'utils must load before viewer.js');
});

test('viewer keeps a 300 ms preroll that excludes the VAD trigger frame and is prepended on utterance start', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  assert.match(js, /const STT_PREROLL_MS = 300;/);
  assert.match(js, /prerollBuffer:\s*null/);
  assert.match(js, /createSTTPrerollBuffer\(\{\s*sampleRate:\s*16000,\s*preRollMs:\s*STT_PREROLL_MS\s*\}\)/);

  const procStart = js.indexOf('sttState.scriptNode.onaudioprocess = (e) => {');
  const procEnd = js.indexOf('sttState.isRecording = true;', procStart);
  assert.ok(procStart >= 0 && procEnd > procStart, 'onaudioprocess block not found');
  const proc = js.slice(procStart, procEnd);
  const vadAt = proc.indexOf('handleSTTVADFrame(pcm16, level)');
  const prerollAt = proc.indexOf('sttState.prerollBuffer.push(pcm16)');
  assert.ok(vadAt >= 0 && prerollAt > vadAt, 'the VAD must see the frame before it enters the preroll, so the trigger frame is not sent twice');
  assert.match(proc, /\} else \{[^}]*?if \(sttState\.prerollBuffer\) sttState\.prerollBuffer\.push\(pcm16\);/, 'only frames outside an active utterance feed the preroll');
});

test('beginSTTUtterance prepends the preroll after the utterance state is reset and assigns an utterance id', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const source = sliceFunctions(js, 'resetSTTUtteranceState', 'handleSTTVADFrame');
  const utils = loadSTTUtils();
  const preroll = utils.createSTTPrerollBuffer({ sampleRate: 16000, preRollMs: 300 });
  const earlier = Int16Array.from({ length: 6400 }, (_, i) => i); // 400 ms, 100 ms older than the limit
  preroll.push(earlier);

  const sttState = {
    isRecording: true, isStopping: false, vadSpeechActive: false, vadSilenceStartedAt: 0, vadLastVoiceAt: 0,
    pendingSpeechRestart: false, pendingSpeechRestartInterrupted: false,
    chunkBuffer: [], capturePCM: [], draftBuffer: [], sentAudioSamples: 0, captureSessionID: '(unknown)',
    prerollBuffer: preroll, utteranceID: '', utteranceCounter: 0,
  };
  const events = [];
  const context = {
    sttState,
    createSTTUtteranceID: utils.createSTTUtteranceID,
    interruptChatOutputForUserInput() {}, interruptIdleChatForUserInput() {},
    recordLatencyMetric() {}, recordSTTCaptureEvent(kind, detail) { events.push([kind, detail]); },
    connectSTTWebSocket() {}, updateSTTInputIndicators() {}, nowLatencyMS: () => 1,
    Date, console,
  };
  vm.createContext(context);
  vm.runInContext(`${source}\nthis.beginSTTUtterance = beginSTTUtterance;`, context);

  assert.equal(context.beginSTTUtterance('vad_voice'), true);
  assert.equal(sttState.vadSpeechActive, true);
  assert.equal(sttState.chunkBuffer.length, 4800, 'the utterance starts with the last 300 ms, not the whole backlog');
  assert.equal(sttState.chunkBuffer[0], 6400 - 4800);
  assert.equal(sttState.chunkBuffer[4799], 6399);
  assert.equal(sttState.capturePCM.length, 4800, 'the saved debug WAV must contain the same audio that was sent');
  assert.equal(preroll.length, 0, 'taking the preroll empties it');
  assert.match(sttState.utteranceID, /^[A-Za-z0-9_-]{1,64}$/);
  const first = sttState.utteranceID;

  sttState.vadSpeechActive = false;
  context.beginSTTUtterance('vad_voice');
  assert.notEqual(sttState.utteranceID, first, 'each utterance gets its own id');
});

test('viewer start control announces the utterance id while stop stays id-less for the active utterance', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const source = sliceFunctions(js, 'sendSTTStartControl', 'sendSTTStopControl');
  const sent = [];
  const context = {
    sttState: { ws: { readyState: 1, send(data) { sent.push(JSON.parse(data)); } }, sampleRate: 16000, utteranceID: 'v1-1' },
    WebSocket: { OPEN: 1 }, recordSTTCaptureEvent() {},
  };
  vm.createContext(context);
  vm.runInContext(`${source}\nthis.sendSTTStartControl = sendSTTStartControl;`, context);
  assert.equal(context.sendSTTStartControl(), true);
  assert.deepEqual(sent[0], { type: 'start', utterance_id: 'v1-1', sample_rate: 16000, channels: 1, format: 'pcm_s16le' });

  context.sttState.utteranceID = '';
  context.sendSTTStartControl();
  assert.equal('utterance_id' in sent[1], false, 'no id is sent when none was assigned (CORE assigns one)');

  assert.match(js, /sttState\.ws\.send\(JSON\.stringify\(\{ type: 'stop' \}\)\)/, 'stop applies to the active utterance and needs no id');
});

// The Viewer ends its utterance when the server does (stt_utterance_utils.js
// decides; stt_utterance_utils.test.mjs tests the decision). These tests pin
// where viewer.js applies it, because the two paths that used to swallow the
// event (final_ignored, and the finalReceived early return of error) would
// still stop the Viewer for 90 seconds if the decision came after them.

function sttMessageHandlerSource(js) {
  const start = js.indexOf('sttState.ws.onmessage = (event) => {');
  const end = js.indexOf('sttState.ws.onerror =', start);
  assert.ok(start >= 0 && end > start, 'STT onmessage block not found');
  return js.slice(start, end);
}

test('V7: the decision to end the utterance is made before final_ignored and before the error early return', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const handler = sttMessageHandlerSource(js);

  const finalStart = handler.indexOf("} else if (msg.type === 'final') {");
  const finalEnd = handler.indexOf("} else if (msg.type === 'reply_reset')", finalStart);
  assert.ok(finalStart >= 0 && finalEnd > finalStart, 'final branch not found');
  const finalBranch = handler.slice(finalStart, finalEnd);
  const finalIgnoredAt = finalBranch.indexOf("recordSTTCaptureEvent('final_ignored'");
  assert.ok(finalIgnoredAt >= 0, 'final_ignored path not found');
  const finalDecisionAt = finalBranch.indexOf('shouldEndSTTUtteranceOnServerEvent(');
  assert.ok(finalDecisionAt >= 0 && finalDecisionAt < finalIgnoredAt, 'the final decision must be taken before the inactive-viewer return');
  const ignoredBlock = finalBranch.slice(finalIgnoredAt, finalBranch.indexOf('return;', finalIgnoredAt));
  assert.match(ignoredBlock, /endSTTUtteranceFromServer\(/, 'a final the inactive Viewer ignores must still end its utterance');

  const errorStart = handler.indexOf("} else if (msg.type === 'error') {");
  assert.ok(errorStart >= 0, 'error branch not found');
  const errorBranch = handler.slice(errorStart);
  const errorDecisionAt = errorBranch.indexOf('shouldEndSTTUtteranceOnServerEvent(');
  const earlyReturnAt = errorBranch.indexOf('if (sttState.finalReceived)');
  assert.ok(errorDecisionAt >= 0 && earlyReturnAt >= 0 && errorDecisionAt < earlyReturnAt, 'the error decision must be taken before the finalReceived early return');
  const afterFinalBlock = errorBranch.slice(earlyReturnAt, errorBranch.indexOf('return;', earlyReturnAt));
  assert.match(afterFinalBlock, /endSTTUtteranceFromServer\(/, 'an error after a final must still end the utterance');
});

test('the final text goes to chat before the utterance is closed, and the close does not depend on the chat send', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const handler = sttMessageHandlerSource(js);
  const finalStart = handler.indexOf("} else if (msg.type === 'final') {");
  const finalEnd = handler.indexOf("} else if (msg.type === 'reply_reset')", finalStart);
  const finalBranch = handler.slice(finalStart, finalEnd);
  const sendAt = finalBranch.indexOf('handleSTTFinalText(sttState.lastRecognitionText)');
  const endAt = finalBranch.lastIndexOf('endSTTUtteranceFromServer(');
  assert.ok(sendAt >= 0 && endAt > sendAt, 'handleSTTFinalText must come first');
  const between = finalBranch.slice(sendAt, endAt);
  assert.match(between, /finally\s*\{/, 'the close must run even if handleSTTFinalText throws');
});

test('a connection that closes while the Viewer is speaking ends the utterance instead of reconnecting', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const start = js.indexOf('sttState.ws.onclose = () => {');
  const end = js.indexOf('function buildSTTWebSocketURL()', start);
  assert.ok(start >= 0 && end > start, 'STT onclose block not found');
  const onclose = js.slice(start, end);
  const nulledAt = onclose.indexOf('sttState.ws = null;');
  const decisionAt = onclose.indexOf("endSTTUtteranceFromServer(sttState, { type: 'close' }");
  const reconnectAt = onclose.indexOf('scheduleSTTReconnect()');
  assert.ok(nulledAt >= 0 && decisionAt > nulledAt, 'ws must be null before the decision, so that it completes the stop directly');
  assert.ok(reconnectAt > decisionAt, 'the decision comes before the reconnect branch');
});

test('viewer.html changes the version of the scripts that changed, so a cached Viewer cannot keep the old decision', () => {
  const html = fs.readFileSync('internal/adapter/viewer/viewer.html', 'utf8');
  assert.match(html, /stt_utterance_utils\.js\?v=20261015-stt-relay-align35"/);
  assert.match(html, /viewer\.js\?v=[^"]*stt-relay-align35"/);
});

// The decision in stt_utterance_utils.js is tested on its own; this drives the
// real onmessage and onclose of viewer.js with the utils loaded as the page
// loads them, to pin what the handlers do with it.
function runSTTHandlers(stateOverrides, extra) {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const log = [];
  const source = `
var WebSocket = class { static OPEN = 1; static CONNECTING = 0; constructor() { globalThis.__ws = this; this.readyState = 1; }
  send() {} close() { __log.push('ws.close'); this.readyState = 2; } };
var sttState = Object.assign({
  ws: null, isRecording: true, isStopping: false, keepSessionChannel: false, vadSpeechActive: true,
  reconnecting: false, finalReceived: false, captureActionError: '', captureSessionID: 's1', voiceBridgeURL: 'ws://x/stt',
  draftBuffer: [], chunkBuffer: [], lastRecognitionText: '', partialCaptionText: '', finalCaptionText: '', errorCaptionText: '',
}, globalThis.__stateOverrides);
function updateSTTInputIndicators() {}
function showToast() {}
function ftime() { return ''; }
function pushDebugTrace() {}
function short(v) { return String(v || ''); }
function recordSTTCaptureEvent() {}
function updateSTTCaption() {}
function setSTTCaptionError() {}
function clearSTTFinalWaitTimer() { globalThis.__waitCleared = (globalThis.__waitCleared || 0) + 1; }
function scheduleSTTReconnect() { __log.push('reconnect'); }
function completeSTTStop() { __log.push('completeSTTStop'); }
function handleSTTFinalText(text) { if (!String(text || '').trim()) return; __log.push('chat:' + text); if (globalThis.__chatThrows) throw new Error('chat failed'); }
function isThisViewerActiveInput() { return globalThis.__active !== false; }
function describeSTTActionError(a, b) { return a + ': ' + b; }
var document = { getElementById() { return null; } };
` + js.slice(js.indexOf('const sttServerEndActions = {'), js.indexOf('function connectSTTWebSocket')) +
    js.slice(js.indexOf('function connectSTTWebSocket'), js.indexOf('function resampleToPCM16')) + `
globalThis.__connect = connectSTTWebSocket;
globalThis.__state = sttState;
`;
  const context = vm.createContext({
    __log: log, __stateOverrides: stateOverrides, console: { error() {}, warn() {}, log() {} },
    __active: extra && extra.active, __chatThrows: extra && extra.chatThrows,
  });
  vm.runInContext(fs.readFileSync('internal/adapter/viewer/assets/js/stt_utterance_utils.js', 'utf8'), context);
  vm.runInContext(source, context);
  context.__connect();
  return { context, log, ws: context.__ws, state: context.__state, waitCleared: () => context.__waitCleared || 0 };
}

test('the real handler: a final that closes the utterance sends the text to chat first, then closes the socket', () => {
  const { ws, state, log } = runSTTHandlers({});
  ws.onmessage({ data: JSON.stringify({ type: 'final', text: 'こんにちは', end_reason: 'silence' }) });
  assert.deepEqual(log, ['chat:こんにちは', 'ws.close']);
  assert.equal(state.isStopping, true);
  assert.equal(state.vadSpeechActive, false);
  ws.onclose();
  assert.deepEqual(log, ['chat:こんにちは', 'ws.close', 'completeSTTStop'], 'closing the socket reaches completeSTTStop through onclose');
});

test('the real handler: V7, an inactive Viewer sends nothing to chat but still ends the utterance', () => {
  const { ws, state, log } = runSTTHandlers({}, { active: false });
  ws.onmessage({ data: JSON.stringify({ type: 'final', text: 'ignored', end_reason: 'silence' }) });
  assert.deepEqual(log, ['ws.close']);
  assert.equal(state.isStopping, true);
  assert.equal(state.vadSpeechActive, false);
});

test('the real handler: V7, an error that arrives after a final still ends the utterance', () => {
  const { ws, state, log } = runSTTHandlers({ finalReceived: true });
  ws.onmessage({ data: JSON.stringify({ type: 'error', code: 'stt_failed', error: 'x' }) });
  assert.deepEqual(log, ['ws.close']);
  assert.equal(state.isStopping, true);
});

test('the real handler: a long utterance that only reached the maximum length is not ended', () => {
  const { ws, state, log } = runSTTHandlers({});
  ws.onmessage({ data: JSON.stringify({ type: 'final', text: 'part', end_reason: 'max_length' }) });
  assert.deepEqual(log, ['chat:part']);
  assert.equal(state.isStopping, false);
  assert.equal(state.vadSpeechActive, true);
});

test('the real handler: the utterance ends even when sending the text to chat throws', () => {
  const { ws, state, log } = runSTTHandlers({}, { chatThrows: true });
  ws.onmessage({ data: JSON.stringify({ type: 'final', text: 'x', end_reason: 'silence' }) });
  assert.deepEqual(log, ['chat:x', 'ws.close']);
  assert.equal(state.isStopping, true);
});

test('the real handler: a final while the Viewer is already stopping behaves as before and closes once', () => {
  const { ws, state, log } = runSTTHandlers({});
  state.isStopping = true; // the Viewer sent stop and is waiting for the final
  state.vadSpeechActive = false;
  ws.onmessage({ data: JSON.stringify({ type: 'final', text: 'a', end_reason: 'stop' }) });
  assert.deepEqual(log, ['chat:a', 'ws.close']);
});

test('the real handler: V4, a close nobody asked for while speaking completes the stop and does not reconnect', () => {
  const { ws, state, log } = runSTTHandlers({ keepSessionChannel: true });
  ws.onclose();
  assert.deepEqual(log, ['completeSTTStop']);
  assert.equal(state.isStopping, true);
  assert.equal(state.vadSpeechActive, false);
});

test('the real handler: a close while the Viewer is idle still reconnects when the channel is kept', () => {
  const { ws, log } = runSTTHandlers({ keepSessionChannel: true, vadSpeechActive: false });
  ws.onclose();
  assert.deepEqual(log, ['reconnect']);
});

// Silence-first (contract 3.5, 9.3): the Viewer's stop reached STT after STT had
// closed the utterance, so STT answers it with a final that has no utterance_id
// and empty_reason no_active_utterance. It is a second final the Viewer asked no
// recognition for: only an answer to the stop.
const NO_ACTIVE_FINAL = { type: 'final', text: '', empty_reason: 'no_active_utterance', end_reason: 'stop', audio_ms: 0, target_called: false };

test('the real handler: the answer to a stop with nothing in progress ends the wait for the final, with nothing sent to chat', () => {
  // The Viewer sent stop and waits; this is the only thing STT has to say.
  const { ws, state, log, waitCleared } = runSTTHandlers({});
  state.isStopping = true;
  state.vadSpeechActive = false;
  ws.onmessage({ data: JSON.stringify(NO_ACTIVE_FINAL) });
  assert.deepEqual(log, ['ws.close'], 'no chat send for an empty text; the socket closes so that the stop completes');
  assert.equal(state.finalReceived, true);
  assert.equal(waitCleared() >= 1, true, 'the 90 s wait for the final is cancelled');
  assert.equal(state.errorCaptionText, '', 'it is not an error');
  assert.equal(state.captureActionError, '');
  ws.onclose();
  assert.deepEqual(log, ['ws.close', 'completeSTTStop']);
});

test('the real handler: the answer to a stop that comes after the final of the utterance leaves the recognized text alone', () => {
  // Browsers drop a message that arrives after close() (Chromium and Firefox do),
  // but the handler does not rely on it: the final went first (it is ahead in
  // STT's order), the Viewer closed the socket, and the answer to the same stop
  // follows. It must not erase what was recognized.
  const { ws, state, log } = runSTTHandlers({});
  state.isStopping = true;
  state.vadSpeechActive = false;
  ws.onmessage({ data: JSON.stringify({ type: 'final', utterance_id: 'v1-1', text: 'こんにちは', end_reason: 'silence', target_called: true }) });
  ws.onmessage({ data: JSON.stringify(NO_ACTIVE_FINAL) });
  assert.deepEqual(log, ['chat:こんにちは', 'ws.close'], 'the text goes to chat once and the socket is closed once');
  assert.equal(state.lastRecognitionText, 'こんにちは');
  assert.equal(state.finalCaptionText, 'こんにちは');
  assert.equal(state.errorCaptionText, '');
});

test('the real handler: the answer to a stop that comes after the final still closes a socket the Viewer had left open', () => {
  // The final was a part of a long speech (max_length): the Viewer kept the
  // socket. Then it sent stop, and STT had nothing announced in progress to
  // close (the rest was too short to be announced). This is the case that
  // reaches a Viewer whose socket is still open, in every browser.
  const { ws, state, log } = runSTTHandlers({});
  ws.onmessage({ data: JSON.stringify({ type: 'final', utterance_id: 'v1-1', text: 'part', end_reason: 'max_length', target_called: true }) });
  state.isStopping = true; // the Viewer's own silence: it sent stop
  state.vadSpeechActive = false;
  ws.onmessage({ data: JSON.stringify(NO_ACTIVE_FINAL) });
  assert.deepEqual(log, ['chat:part', 'ws.close'], 'otherwise the Viewer would wait 90 seconds for a final that STT will not send');
  assert.equal(state.lastRecognitionText, 'part');
  assert.equal(state.finalCaptionText, 'part');
});

test('the real handler: V7, the answer to a stop reaches a Viewer that is not the active input without a chat send', () => {
  const { ws, state, log } = runSTTHandlers({}, { active: false });
  state.isStopping = true;
  state.vadSpeechActive = false;
  ws.onmessage({ data: JSON.stringify(NO_ACTIVE_FINAL) });
  assert.deepEqual(log, [], 'an inactive Viewer ignores the final; it was not speaking, so there is nothing to end');
});

test('the answer to a stop with nothing in progress is checked after the V7 decision and before the recognized text is replaced', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  const handler = sttMessageHandlerSource(js);
  const finalStart = handler.indexOf("} else if (msg.type === 'final') {");
  const finalEnd = handler.indexOf("} else if (msg.type === 'reply_reset')", finalStart);
  const finalBranch = handler.slice(finalStart, finalEnd);
  const decisionAt = finalBranch.indexOf('shouldEndSTTUtteranceOnServerEvent(');
  const ignoredAt = finalBranch.indexOf("recordSTTCaptureEvent('final_ignored'");
  const guardAt = finalBranch.indexOf('sttState.finalReceived && isSTTStopAnswerWithoutUtterance(msg)');
  const replaceAt = finalBranch.indexOf('sttState.lastRecognitionText = String(msg.text');
  assert.ok(decisionAt >= 0 && ignoredAt > decisionAt, 'the V7 decision comes first');
  assert.ok(guardAt > ignoredAt, 'the guard sits after the inactive-Viewer return, which already ignores the text');
  assert.ok(replaceAt > guardAt, 'the guard must come before the recognized text is replaced');
  const guardBlock = finalBranch.slice(guardAt, replaceAt);
  assert.ok(guardAt >= 0, 'it applies only after a final was received');
  assert.match(guardBlock, /ws\.close\(\)/, 'it still closes the socket of a Viewer that waits for the stop');
});
