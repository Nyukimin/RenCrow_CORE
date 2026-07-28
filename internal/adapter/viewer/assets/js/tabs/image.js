'use strict';

(function initImageTab() {
  const prompt = document.getElementById('imagePrompt');
  const negativePrompt = document.getElementById('imageNegativePrompt');
  const seed = document.getElementById('imageSeed');
  const generateButton = document.getElementById('imageGenerateBtn');
  const status = document.getElementById('imageStatus');
  const empty = document.getElementById('imageResultEmpty');
  const figure = document.getElementById('imageResultFigure');
  const image = document.getElementById('imageResult');
  const resultPrompt = document.getElementById('imageResultPrompt');
  const resultMeta = document.getElementById('imageResultMeta');
  const error = document.getElementById('imageError');
  if (!prompt || !generateButton || !status || !image) return;

  function setBusy(busy) {
    generateButton.disabled = busy;
    prompt.disabled = busy;
    if (negativePrompt) negativePrompt.disabled = busy;
    if (seed) seed.disabled = busy;
    generateButton.textContent = busy ? 'Generating...' : 'Generate';
    if (busy) status.textContent = 'generating';
    status.classList.toggle('is-live', busy);
  }

  function showError(message) {
    error.textContent = message;
    error.hidden = false;
    status.textContent = 'failed';
    status.classList.remove('is-live');
  }

  async function generate() {
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
        status.textContent = 'complete';
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

  generateButton.addEventListener('click', generate);
  prompt.addEventListener('keydown', function onPromptKeydown(event) {
    if ((event.ctrlKey || event.metaKey) && event.key === 'Enter') {
      event.preventDefault();
      generate();
    }
  });
})();
