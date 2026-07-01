import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';

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

test('viewer voice chat sends final text only in normal timeline chat without stopping capture on idle view', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  assert.match(js, /let activeViewerTab = 'home'/);
  const allowedStart = js.indexOf('function isVoiceChatAllowed()');
  const allowedEnd = js.indexOf('function normalizeVoiceInputMode', allowedStart);
  assert.ok(allowedStart >= 0 && allowedEnd > allowedStart, 'isVoiceChatAllowed block not found');
  const allowedSource = js.slice(allowedStart, allowedEnd);
  assert.match(allowedSource, /if \(isLabInputSurfaceActive\(\)\) return true/);
  assert.match(allowedSource, /return activeViewerTab === 'timeline' && !document\.body\.classList\.contains\('live-mode'\)/);
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
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
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
  assert.match(js, /function finalizeSTTLocalDraft\(reason\)/);
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
  const timeoutEnd = js.indexOf('function finalizeSTTLocalDraft(reason)', timeoutStart);
  assert.ok(timeoutStart >= 0 && timeoutEnd > timeoutStart, 'final wait timeout block not found');
  const timeoutSource = js.slice(timeoutStart, timeoutEnd);
  assert.match(timeoutSource, /finalizeSTTLocalDraft\('timeout'\)/);

  const finalStart = js.indexOf("} else if (msg.type === 'final') {");
  const finalEnd = js.indexOf("} else if (msg.type === 'reply_reset')", finalStart);
  assert.ok(finalStart >= 0 && finalEnd > finalStart, 'final message block not found');
  const finalSource = js.slice(finalStart, finalEnd);
	assert.match(finalSource, /const finalInputText = formatSTTFinalInputText\(sttState\.lastRecognitionText, msg\)/);
	assert.match(finalSource, /handleSTTFinalText\(finalInputText\)/);
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
  assert.match(js, /rememberInterruptedChatOutput\(\{\s*sessionId: centralTTSSpeech\.sessionId,\s*responseId: centralTTSSpeech\.responseId,/);
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
	assert.match(finalSource, /handleSTTFinalText\(finalInputText\)/);

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

test('viewer marks provisional STT final before sending it to chat', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  assert.match(js, /function formatSTTFinalInputText\(text, msg\)/);
  assert.match(js, /msg && msg\.stt_fallback_required === true/);
  assert.match(js, /\[音声入力: 暫定認識 \/ 要確認\]/);
  assert.match(js, /const finalInputText = formatSTTFinalInputText\(sttState\.lastRecognitionText, msg\)/);
  assert.match(js, /handleSTTFinalText\(finalInputText\)/);
});

test('viewer STT message handling is not blocked by debug panel rendering', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
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

test('viewer STT autotest uses runtime STT base URL for provider inference', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/viewer.js', 'utf8');
  assert.match(js, /sttBaseURL:\s*''/);
  assert.match(js, /cfg\.stt_base_url/);
  assert.match(js, /function buildSTTProviderURLForAutoTest\(\)/);
  assert.match(js, /base \+ '\/v1\/audio\/transcriptions'/);
  assert.match(js, /provider_url: providerURL/);
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
  assert.match(js, /await runSTTAutoTest\(\{ provider_rounds: 1, ws_rounds: 0 \}\)/);
  assert.match(js, /sttTestRecordState\.lastTranscript = transcript/);
  assert.match(js, /interruptChatOutputForUserInput\('stt_test_record'\)/);
  assert.match(js, /interruptIdleChatForUserInput\('stt_test_record'\)/);
  assert.match(js, /テスト録音中は通常マイクを使えません/);
  assert.doesNotMatch(js.slice(js.indexOf('function startSTTTestRecording()'), js.indexOf('async function stopSTTTestRecordingAndSave()')), /connectSTTWebSocket/);
  assert.doesNotMatch(js.slice(js.indexOf('function startSTTTestRecording()'), js.indexOf('async function stopSTTTestRecordingAndSave()')), /handleSTTVADFrame/);
});
