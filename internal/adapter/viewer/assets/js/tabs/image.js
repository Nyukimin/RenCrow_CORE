'use strict';

(function initImageTab() {
  const prompt = document.getElementById('imagePrompt');
  const negativePrompt = document.getElementById('imageNegativePrompt');
  const seed = document.getElementById('imageSeed');
  const outputKind = document.getElementById('imageOutputKind');
  const imageOnlyOptions = document.getElementById('imageOnlyOptions');
  const videoOptions = document.getElementById('videoOptions');
  const videoMode = document.getElementById('videoMode');
  const videoInputImageRow = document.getElementById('videoInputImageRow');
  const videoInputImage = document.getElementById('videoInputImage');
  const videoSeed = document.getElementById('videoSeed');
  const generateButton = document.getElementById('imageGenerateBtn');
  const status = document.getElementById('imageStatus');
  const empty = document.getElementById('imageResultEmpty');
  const figure = document.getElementById('imageResultFigure');
  const image = document.getElementById('imageResult');
  const resultPrompt = document.getElementById('imageResultPrompt');
  const resultMeta = document.getElementById('imageResultMeta');
  const resultKicker = document.getElementById('imageResultKicker');
  const resultMain = document.querySelector('.image-main');
  const error = document.getElementById('imageError');
  const videoPanel = document.getElementById('videoResultPanel');
  const videoStatus = document.getElementById('videoJobStatus');
  const videoProgress = document.getElementById('videoProgress');
  const video = document.getElementById('videoResult');
  const download = document.getElementById('videoDownload');
  const videoAuthPanel = document.getElementById('videoAuthPanel');
  const videoLoginForm = document.getElementById('videoLoginForm');
  const videoCredential = document.getElementById('videoCredential');
  const videoLoginButton = document.getElementById('videoLoginButton');
  const videoLogoutButton = document.getElementById('videoLogoutButton');
  const videoAuthStatus = document.getElementById('videoAuthStatus');
  if (!prompt || !generateButton || !status || !image || !outputKind) return;

  const savedVideoJobKey = 'rencrow.image.video.lastJobId';
  const savedVideoPendingKey = 'rencrow.image.video.pendingRequest';
  const videoJobPattern = /^vid_[A-Za-z0-9_-]{1,128}$/;
  const maxVideoImageBytes = 20 * 1024 * 1024;
  let currentVideoID = '';
  let pollTimer = 0;
  let videoSubmitBusy = false;
  let videoAuthenticated = false;
  let videoAuthRevision = 0;

  function setStatus(value, isLive) {
    status.textContent = value;
    status.classList.toggle('is-live', Boolean(isLive));
  }

  function showError(message) {
    error.textContent = message;
    error.hidden = false;
    setStatus('failed', false);
  }

  function setBusy(busy, label) {
    generateButton.disabled = busy;
    prompt.disabled = busy;
    if (negativePrompt) negativePrompt.disabled = busy;
    if (seed) seed.disabled = busy;
    if (outputKind) outputKind.disabled = busy;
    if (videoMode) videoMode.disabled = busy;
    if (videoInputImage) videoInputImage.disabled = busy;
    if (videoSeed) videoSeed.disabled = busy;
    generateButton.textContent = label || (busy ? 'Generating...' : outputKind.value === 'video' ? 'Generate video' : 'Generate image');
    if (busy) setStatus('generating', true);
  }

  function setOutputKind() {
    const isVideo = outputKind.value === 'video';
    imageOnlyOptions.hidden = isVideo;
    videoOptions.hidden = !isVideo;
    videoInputImageRow.hidden = !isVideo || videoMode.value !== 'i2v';
    generateButton.textContent = isVideo ? 'Generate video' : 'Generate image';
    resultKicker.textContent = isVideo ? 'Generated video' : 'Generated image';
    if (resultMain) resultMain.classList.toggle('is-video-output', isVideo);
    videoPanel.hidden = !isVideo;
    if (videoAuthPanel) videoAuthPanel.hidden = !isVideo;
  }

  function renderVideoAuth() {
    if (videoLoginForm) videoLoginForm.hidden = videoAuthenticated;
    if (videoLoginButton) videoLoginButton.disabled = videoAuthenticated;
    if (videoLogoutButton) videoLogoutButton.hidden = !videoAuthenticated;
  }

  async function checkVideoSession() {
    const revision = videoAuthRevision;
    let authenticated = false;
    let message = '認証状態を確認できません。COREのServe経由で接続してください。';
    try {
      const response = await fetch('/viewer/image/video/session', {
        method: 'GET',
        cache: 'no-store',
        credentials: 'same-origin',
        headers: {'Accept': 'application/json'},
      });
      const payload = await response.json().catch(() => ({}));
      authenticated = response.ok && payload.authenticated === true;
      message = authenticated
        ? '動画を利用できます。'
        : response.status === 401
          ? '動画を使うには認証してください。'
          : '認証状態を確認できません。COREのServe経由で接続してください。';
    } catch (_) {
      authenticated = false;
    }
    if (revision !== videoAuthRevision) return videoAuthenticated;
    videoAuthenticated = authenticated;
    if (videoAuthStatus) videoAuthStatus.textContent = message;
    renderVideoAuth();
    return videoAuthenticated;
  }

  function hasSavedVideoState() {
    try {
      return Boolean(window.localStorage.getItem(savedVideoJobKey) || loadPendingVideoRequest());
    } catch (_) {
      return false;
    }
  }

  async function signInVideo(event) {
    event.preventDefault();
    if (!videoCredential || !videoCredential.value) {
      if (videoAuthStatus) videoAuthStatus.textContent = 'Browser credentialを入力してください。';
      return;
    }
    videoAuthRevision += 1;
    const credential = videoCredential.value;
    if (videoLoginButton) videoLoginButton.disabled = true;
    if (videoAuthStatus) videoAuthStatus.textContent = '認証しています…';
    try {
      const response = await fetch('/viewer/image/video/session', {
        method: 'POST',
        cache: 'no-store',
        credentials: 'same-origin',
        headers: {'Accept': 'application/json', 'Content-Type': 'application/json'},
        body: JSON.stringify({credential}),
      });
      const payload = await response.json().catch(() => ({}));
      videoAuthenticated = response.ok && payload.authenticated === true;
      if (!videoAuthenticated) {
        if (videoAuthStatus) videoAuthStatus.textContent = response.status === 401
          ? '認証情報が正しくありません。'
          : '認証できませんでした。COREのServe経由で接続してください。';
        return;
      }
      if (videoAuthStatus) videoAuthStatus.textContent = '動画を利用できます。';
      renderVideoAuth();
      resumeSavedVideoState();
    } catch (_) {
      videoAuthenticated = false;
      if (videoAuthStatus) videoAuthStatus.textContent = '認証できませんでした。接続を確認して再試行してください。';
    } finally {
      videoAuthRevision += 1;
      // Never keep the browser credential in a field or browser storage.
      videoCredential.value = '';
      renderVideoAuth();
    }
  }

  async function signOutVideo() {
    videoAuthRevision += 1;
    let signedOut = false;
    try {
      const response = await fetch('/viewer/image/video/session', {
        method: 'DELETE',
        cache: 'no-store',
        credentials: 'same-origin',
        headers: {'Accept': 'application/json'},
      });
      signedOut = response.ok;
    } catch (_) {
      signedOut = false;
    } finally {
      videoAuthRevision += 1;
      window.clearTimeout(pollTimer);
      currentVideoID = '';
      videoAuthenticated = false;
      resetVideoPlayer();
      if (videoAuthStatus) videoAuthStatus.textContent = signedOut
        ? 'サインアウトしました。認証すると保存した動画ジョブを再開できます。'
        : 'サインアウトできませんでした。接続を確認してください。';
      renderVideoAuth();
    }
  }

  function makeIdempotencyKey() {
    if (!window.crypto || !window.crypto.getRandomValues) {
      throw new Error('安全なリクエストIDを作成できません。HTTPSで開き直してください。');
    }
    const bytes = new Uint8Array(16);
    window.crypto.getRandomValues(bytes);
    return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
  }

  async function sha256Hex(value) {
    if (!window.crypto || !window.crypto.subtle || !window.crypto.subtle.digest) {
      throw new Error('動画リクエストの再試行保護にはHTTPSとSHA-256が必要です。');
    }
    const digest = await window.crypto.subtle.digest('SHA-256', value);
    return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, '0')).join('');
  }

  async function videoRequestFingerprint(mode, promptText, seedValue, file) {
    const imageSha256 = file ? await sha256Hex(await file.arrayBuffer()) : '';
    const normalizedRequest = JSON.stringify({
      version: 1,
      mode,
      prompt: promptText,
      seed: seedValue,
      image_sha256: imageSha256,
    });
    return sha256Hex(new TextEncoder().encode(normalizedRequest));
  }

  function loadPendingVideoRequest() {
    try {
      const serialized = window.localStorage.getItem(savedVideoPendingKey);
      if (!serialized) return null;
      const pending = JSON.parse(serialized);
      if (!pending || pending.version !== 1 ||
          !/^[0-9a-f]{32}$/.test(pending.key || '') ||
          !/^[0-9a-f]{64}$/.test(pending.fingerprint || '') ||
          !['t2v', 'i2v'].includes(pending.mode) ||
          !Number.isSafeInteger(pending.seed) || pending.seed < 0 || pending.seed > 9007199254740991) {
        window.localStorage.removeItem(savedVideoPendingKey);
        return null;
      }
      return pending;
    } catch (_) {
      return null;
    }
  }

  function savePendingVideoRequest(pending) {
    window.localStorage.setItem(savedVideoPendingKey, JSON.stringify(pending));
  }

  function clearPendingVideoRequest(key) {
    try {
      const pending = loadPendingVideoRequest();
      if (!key || (pending && pending.key === key)) {
        window.localStorage.removeItem(savedVideoPendingKey);
      }
    } catch (_) {
      // Keep the pending key if storage is unavailable.
    }
  }

  function saveVideoJob(id) {
    try {
      window.localStorage.setItem(savedVideoJobKey, id);
      return true;
    } catch (_) {
      // The pending idempotency key remains available for a safe retry after reload.
      return false;
    }
  }

  function clearSavedVideoJob(id) {
    try {
      if (!id || window.localStorage.getItem(savedVideoJobKey) === id) {
        window.localStorage.removeItem(savedVideoJobKey);
      }
    } catch (_) {
      // Storage can be disabled by browser policy.
    }
  }

  function resetVideoPlayer() {
    video.pause();
    video.removeAttribute('src');
    video.load();
    video.hidden = true;
    download.hidden = true;
    download.removeAttribute('href');
  }

  function schedulePoll(id, delay) {
    window.clearTimeout(pollTimer);
    pollTimer = window.setTimeout(() => pollVideoJob(id), delay);
  }

  async function pollVideoJob(id) {
    if (!id || id !== currentVideoID) return;
    try {
      const response = await fetch(`/viewer/image/video/generations/${encodeURIComponent(id)}`, {
        method: 'GET',
        cache: 'no-store',
        credentials: 'same-origin',
        headers: {'Accept': 'application/json'},
      });
      const payload = await response.json().catch(() => ({}));
      if (id !== currentVideoID) return;
      if (response.status === 404) {
        if (!await checkVideoSession()) {
          currentVideoID = '';
          window.clearTimeout(pollTimer);
          videoStatus.textContent = 'ログインすると保存した動画ジョブを再開できます。';
          setStatus('authentication required', false);
          return;
        }
        currentVideoID = '';
        clearSavedVideoJob(id);
        resetVideoPlayer();
        videoStatus.textContent = 'この動画ジョブは見つからないか、現在の利用者から参照できません。';
        setStatus('not found', false);
        return;
      }
      if (!response.ok || payload.id !== id || !['queued', 'running', 'succeeded', 'failed'].includes(payload.status)) {
        throw new Error(payload.message || `動画状態を取得できません。HTTP ${response.status}`);
      }
      error.hidden = true;
      videoProgress.hidden = payload.status !== 'queued' && payload.status !== 'running';
      if (Number.isInteger(payload.progress) && payload.progress >= 0 && payload.progress <= 100) {
        videoProgress.value = payload.progress;
        videoStatus.textContent = `${payload.status} · ${payload.progress}%`;
      } else {
        videoProgress.removeAttribute('value');
        videoStatus.textContent = payload.status === 'queued'
          ? 'queued · 生成開始を待っています'
          : payload.status === 'running'
            ? 'running · 進捗率はまだ報告されていません'
            : payload.status;
      }
      setStatus(payload.status, payload.status === 'queued' || payload.status === 'running');

      if (payload.status === 'succeeded') {
        const expectedURL = `/viewer/image/video/${id}.mp4`;
        if (!payload.video || payload.video.url !== expectedURL || payload.video.content_type !== 'video/mp4') {
          throw new Error('動画サービスから無効な再生情報が返りました。');
        }
        currentVideoID = '';
        videoProgress.hidden = true;
        videoStatus.textContent = 'succeeded';
        video.onerror = function onVideoError() {
          checkVideoSession().then((authenticated) => {
            if (!authenticated) {
              showError('ログインすると保存した動画を再生できます。');
              return;
            }
            showError('生成したMP4を再生できませんでした。');
          });
        };
        video.src = expectedURL;
        video.hidden = false;
        download.href = expectedURL;
        download.hidden = false;
        return;
      }
      if (payload.status === 'failed') {
        currentVideoID = '';
        videoProgress.hidden = true;
        showError(payload.error && payload.error.message ? payload.error.message : '動画を生成できませんでした。');
        videoStatus.textContent = 'failed';
        return;
      }
      schedulePoll(id, 2000);
    } catch (requestError) {
      if (id !== currentVideoID) return;
      videoStatus.textContent = requestError && requestError.message ? requestError.message : '状態取得に失敗しました。再試行します。';
      setStatus('reconnecting', true);
      schedulePoll(id, 5000);
    }
  }

  function resumeSavedVideoJob() {
    let id = '';
    try {
      id = window.localStorage.getItem(savedVideoJobKey) || '';
    } catch (_) {
      return;
    }
    if (!videoJobPattern.test(id)) {
      clearSavedVideoJob(id);
      return;
    }
    currentVideoID = id;
    outputKind.value = 'video';
    setOutputKind();
    resetVideoPlayer();
    videoStatus.textContent = '保存した動画ジョブを確認しています…';
    videoPanel.hidden = false;
    pollVideoJob(id);
  }

  function resumePendingVideoRequest() {
    const pending = loadPendingVideoRequest();
    if (!pending) return false;
    outputKind.value = 'video';
    videoMode.value = pending.mode;
    videoSeed.value = String(pending.seed);
    setOutputKind();
    resetVideoPlayer();
    videoStatus.textContent = pending.mode === 'i2v'
      ? '前回の送信結果が不明です。同じプロンプトとSeedを入力し、同じ開始画像を選び直して安全に再試行できます。'
      : '前回の送信結果が不明です。同じプロンプトとSeedで再試行できます。';
    videoPanel.hidden = false;
    setStatus('submission uncertain', true);
    return true;
  }

  function resumeSavedVideoState() {
    if (!videoAuthenticated) return false;
    if (!resumePendingVideoRequest()) resumeSavedVideoJob();
    return true;
  }

  function startVideoGeneration() {
    if (videoSubmitBusy) return;
    videoSubmitBusy = true;
    generateVideo()
      .catch(() => showError('動画生成を開始できませんでした。'))
      .finally(() => { videoSubmitBusy = false; });
  }

  async function generateImage() {
    const promptText = prompt.value.trim();
    if (!promptText) {
      showError('プロンプトを入力してください。');
      prompt.focus();
      return;
    }
    error.hidden = true;
    setBusy(true);
    const seedValue = seed ? Number(seed.value) : -1;
    try {
      const response = await fetch('/viewer/image/generate', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({
          prompt: promptText,
          negative_prompt: negativePrompt ? negativePrompt.value.trim() : '',
          seed: Number.isSafeInteger(seedValue) ? seedValue : -1,
        }),
      });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok || !payload.ok || !payload.image || !payload.image.url) {
        throw new Error(payload.message || `画像生成に失敗しました。HTTP ${response.status}`);
      }
      image.onload = function onImageLoad() {
        setBusy(false);
        setStatus('complete', false);
      };
      image.onerror = function onImageError() {
        setBusy(false);
        showError('生成結果の画像を取得できませんでした。');
      };
      image.src = payload.image.url + '&v=' + encodeURIComponent(payload.id || Date.now());
      resultPrompt.textContent = payload.prompt || promptText;
      resultMeta.textContent = `${payload.profile || 'RenCrow_Image'} / ${payload.image.width || '-'}×${payload.image.height || '-'}`;
      empty.hidden = true;
      figure.hidden = false;
    } catch (requestError) {
      setBusy(false);
      showError(requestError && requestError.message ? requestError.message : '画像生成に失敗しました。');
    }
  }

  async function generateVideo() {
    if (!await checkVideoSession()) {
      if (videoAuthPanel) videoAuthPanel.hidden = false;
      if (videoCredential) videoCredential.focus();
      videoStatus.textContent = '認証すると動画を生成できます。';
      return;
    }
    const promptText = prompt.value.trim();
    if (!promptText) {
      showError('プロンプトを入力してください。');
      prompt.focus();
      return;
    }
    const seedValue = Number(videoSeed.value);
    if (videoSeed.value.trim() === '' || !Number.isSafeInteger(seedValue) || seedValue < 0 || seedValue > 9007199254740991) {
      showError('動画Seedは0以上の整数で入力してください。');
      videoSeed.focus();
      return;
    }
    const mode = videoMode.value;
    if (!['t2v', 'i2v'].includes(mode)) {
      showError('動画モードを選び直してください。');
      videoMode.focus();
      return;
    }
    const file = mode === 'i2v' ? videoInputImage.files[0] : null;
    if (mode === 'i2v' && !file) {
      showError('Image to videoでは開始画像を選択してください。');
      videoInputImage.focus();
      return;
    }
    if (file && (file.size > maxVideoImageBytes || !['image/png', 'image/jpeg'].includes(file.type) && !/\.(png|jpe?g)$/i.test(file.name))) {
      showError('開始画像は20 MiB以下のPNGまたはJPEGを選択してください。');
      videoInputImage.focus();
      return;
    }

    error.hidden = true;
    setBusy(true, 'Preparing...');
    videoStatus.textContent = '動画リクエストを準備しています…';
    videoPanel.hidden = false;
    resetVideoPlayer();
    let requestSubmitted = false;
    try {
      const fingerprint = await videoRequestFingerprint(mode, promptText, seedValue, file);
      const previous = loadPendingVideoRequest();
      const pending = previous && previous.fingerprint === fingerprint && previous.mode === mode
        ? previous
        : {version: 1, key: makeIdempotencyKey(), fingerprint, mode, seed: seedValue};
      if (pending !== previous) {
        // Persist before POST so a lost 202 can be retried with the same key.
        savePendingVideoRequest(pending);
      }

      const form = new FormData();
      form.append('mode', mode);
      form.append('prompt', promptText);
      form.append('seed', String(seedValue));
      if (file) form.append('image', file, file.name);

      window.clearTimeout(pollTimer);
      currentVideoID = '';
      clearSavedVideoJob();
      setBusy(true, 'Submitting...');
      videoStatus.textContent = '動画ジョブを送信しています…';
      requestSubmitted = true;
      const response = await fetch('/viewer/image/video/generations', {
        method: 'POST',
        credentials: 'same-origin',
        headers: {
          'Accept': 'application/json',
          'Idempotency-Key': pending.key,
        },
        body: form,
      });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok || !payload.id || !videoJobPattern.test(payload.id) || !['queued', 'running', 'succeeded', 'failed'].includes(payload.status)) {
        throw new Error(payload.message || `動画生成を開始できません。HTTP ${response.status}`);
      }
      currentVideoID = payload.id;
      const jobStored = saveVideoJob(payload.id);
      if (jobStored) clearPendingVideoRequest(pending.key);
      videoStatus.textContent = payload.status === 'queued' ? 'queued · 生成開始を待っています' : payload.status;
      if (!jobStored) {
        videoStatus.textContent = '受付済みです。保存領域を確認してから状態を取得します。';
      }
      schedulePoll(payload.id, 1000);
    } catch (requestError) {
      showError(requestError && requestError.message ? requestError.message : '動画生成に失敗しました。');
      videoStatus.textContent = requestSubmitted
        ? '応答を確認できませんでした。同じリクエストを再送すると同じジョブを確認できます。'
        : '送信前に停止しました。入力を確認してから再試行してください。';
    } finally {
      setBusy(false);
    }
  }

  generateButton.addEventListener('click', () => {
    if (outputKind.value === 'video') startVideoGeneration();
    else generateImage();
  });
  if (videoLoginForm) videoLoginForm.addEventListener('submit', signInVideo);
  if (videoLogoutButton) videoLogoutButton.addEventListener('click', signOutVideo);
  outputKind.addEventListener('change', setOutputKind);
  videoMode.addEventListener('change', setOutputKind);
  prompt.addEventListener('keydown', function onPromptKeydown(event) {
    if ((event.ctrlKey || event.metaKey) && event.key === 'Enter') {
      event.preventDefault();
      if (outputKind.value === 'video') startVideoGeneration();
      else generateImage();
    }
  });

  setOutputKind();
  checkVideoSession().then((authenticated) => {
    if (authenticated) {
      resumeSavedVideoState();
      return;
    }
    if (hasSavedVideoState()) {
      outputKind.value = 'video';
      setOutputKind();
      videoPanel.hidden = false;
      videoStatus.textContent = '認証すると保存した動画の状態を再開します。';
    }
  });
})();
