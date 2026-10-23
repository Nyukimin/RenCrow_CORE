'use strict';

(function initCharImageTab() {
  const characterSelect = document.getElementById('charImageCharacter');
  const characterCount = document.getElementById('charImageCount');
  const characterName = document.getElementById('charImageName');
  const characterNameEn = document.getElementById('charImageNameEn');
  const characterStatus = document.getElementById('charImageCharacterStatus');
  const tagline = document.getElementById('charImageTagline');
  const characterCore = document.getElementById('charImageCharacterCore');
  const render2D = document.getElementById('charImageRender2D');
  const render3D = document.getElementById('charImageRender3D');
  const sourceNotes = document.getElementById('charImageSourceNotes');
  const sceneVariant = document.getElementById('charImageSceneVariant');
  const useDefaultScene = document.getElementById('charImageUseDefaultScene');
  const modeButtons = Array.from(document.querySelectorAll('[data-charimage-mode]'));
  const status = document.getElementById('charImageStatus');
  const error = document.getElementById('charImageError');
  const characterRetry = document.getElementById('charImageRetry');
  const optionsStatus = document.getElementById('charImageOptionsStatus');
  const optionsError = document.getElementById('charImageOptionsError');
  const optionsRetry = document.getElementById('charImageOptionsRetry');
  const parameters = document.getElementById('charImageParameters');
  const seedInput = document.getElementById('charImageSeed');
  const widthInput = document.getElementById('charImageWidth');
  const heightInput = document.getElementById('charImageHeight');
  const stepsInput = document.getElementById('charImageSteps');
  const cfgScaleInput = document.getElementById('charImageCfgScale');
  const loraLabel = document.getElementById('charImageLoraLabel');
  const loraInput = document.getElementById('charImageLoraStrength');
  const faceLabel = document.getElementById('charImageFaceDetailerLabel');
  const faceInput = document.getElementById('charImageFaceDetailer');
  const generateButton = document.getElementById('charImageGenerate');
  const previewSection = document.getElementById('charImagePreviewSection');
  const previewStatus = document.getElementById('charImagePreviewStatus');
  const promptPreview = document.getElementById('charImagePromptPreview');
  const promptParts = document.getElementById('charImagePromptParts');
  const previewCore = document.getElementById('charImagePreviewCore');
  const previewProfile = document.getElementById('charImagePreviewProfile');
  const previewScene = document.getElementById('charImagePreviewScene');
  const resultEmpty = document.getElementById('charImageResultEmpty');
  const resultFigure = document.getElementById('charImageResultFigure');
  const resultImage = document.getElementById('charImageResult');
  const resultPrompt = document.getElementById('charImageResultPrompt');
  const resultMeta = document.getElementById('charImageResultMeta');
  const generationError = document.getElementById('charImageGenerationError');
  const historyList = document.getElementById('charImageHistoryList');
  const historyEmpty = document.getElementById('charImageHistoryEmpty');
  const historyError = document.getElementById('charImageHistoryError');
  const historyRefresh = document.getElementById('charImageHistoryRefresh');

  const MOSS_MODEL_ID = 'moss-image-sft-8-steps-dmd';
  const CHARACTER_COUNT = 22;
  const PREVIEW_DELAY_MS = 320;
  const HISTORY_LIMIT = 8;
  const state = {
    characters: [],
    characterByID: new Map(),
    selectedMode: '2d',
    model: null,
    optionsReady: false,
    charactersReady: false,
    charactersPromise: null,
    optionsPromise: null,
    generationBusy: false,
    previewTimer: null,
    previewController: null,
    previewRevision: 0,
    historyBusy: false,
  };

  if (!characterSelect || !characterCount || !characterName || !characterNameEn || !characterStatus || !tagline ||
      !characterCore || !render2D || !render3D || !sourceNotes || !sceneVariant || !useDefaultScene || !modeButtons.length ||
      !status || !error || !characterRetry || !optionsStatus || !optionsError || !optionsRetry || !parameters || !seedInput || !widthInput ||
      !heightInput || !stepsInput || !cfgScaleInput || !loraLabel || !loraInput || !faceLabel || !faceInput || !generateButton ||
      !previewSection || !previewStatus || !promptPreview || !promptParts || !previewCore || !previewProfile || !previewScene ||
      !resultEmpty || !resultFigure || !resultImage || !resultPrompt || !resultMeta || !generationError || !historyList ||
      !historyEmpty || !historyError || !historyRefresh) return;

  function setStatus(message, kind) {
    status.textContent = message;
    status.classList.remove('is-live', 'is-ready', 'is-error', 'is-warning');
    if (kind) status.classList.add(`is-${kind}`);
  }

  function setError(target, message) {
    target.textContent = message;
    target.hidden = !message;
  }

  function getErrorMessage(payload, response, fallback) {
    if (payload && typeof payload.message === 'string' && payload.message.trim()) return payload.message;
    if (payload && typeof payload.error === 'string' && payload.error.trim()) return payload.error;
    return response ? `${fallback} (HTTP ${response.status})` : fallback;
  }

  async function readJSONResponse(response, fallback) {
    const payload = await response.json().catch(() => null);
    if (!response.ok || !payload || payload.ok !== true) {
      throw new Error(getErrorMessage(payload, response, fallback));
    }
    return payload;
  }

  function safeSameOriginURL(rawURL) {
    if (typeof rawURL !== 'string' || !rawURL.trim()) return '';
    try {
      const url = new URL(rawURL, window.location.origin);
      if (url.origin !== window.location.origin || url.protocol !== window.location.protocol || url.username || url.password) return '';
      return url.href;
    } catch (_) {
      return '';
    }
  }

  function validateCharacters(payload) {
    if (!payload || payload.ok !== true || payload.model !== MOSS_MODEL_ID || !Array.isArray(payload.characters) ||
        payload.characters.length > 256) {
      throw new Error('Character catalog response is invalid.');
    }
    const seen = new Set();
    for (const character of payload.characters) {
      if (!character || typeof character.id !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(character.id) ||
          seen.has(character.id) || typeof character.name_ja !== 'string' || typeof character.name_en !== 'string' ||
          typeof character.status !== 'string' || typeof character.tagline_ja !== 'string' ||
          typeof character.source_notes_ja !== 'string' || !character.fixed ||
          typeof character.fixed.character_core_en !== 'string' || typeof character.fixed.render_2d_en !== 'string' ||
          typeof character.fixed.render_3d_en !== 'string' || typeof character.default_scene_en !== 'string') {
        throw new Error('Character catalog contains an invalid character record.');
      }
      seen.add(character.id);
    }
    return payload.characters;
  }

  function renderCharacterList(characters) {
    state.characters = characters;
    state.characterByID = new Map(characters.map((character) => [character.id, character]));
    const placeholder = document.createElement('option');
    placeholder.value = '';
    placeholder.textContent = characters.length ? 'キャラクターを選択' : '利用可能なキャラクターがありません';
    const options = characters.map((character) => {
      const option = document.createElement('option');
      option.value = character.id;
      option.textContent = `${character.name_ja} / ${character.name_en}`;
      return option;
    });
    characterSelect.replaceChildren(placeholder, ...options);
    characterCount.textContent = `${characters.length}/${CHARACTER_COUNT}`;
    characterSelect.disabled = characters.length === 0;
    sceneVariant.disabled = characters.length === 0;
    useDefaultScene.disabled = characters.length === 0;
    modeButtons.forEach((button) => { button.disabled = characters.length === 0; });
    if (characters.length) {
      characterSelect.value = characters[0].id;
      renderSelectedCharacter(characters[0]);
    } else {
      characterName.textContent = '利用可能なキャラクターがありません';
      characterNameEn.textContent = '';
      tagline.textContent = '';
    }
  }

  function renderSelectedCharacter(character) {
    if (!character) return;
    characterName.textContent = character.name_ja;
    characterNameEn.textContent = character.name_en;
    characterStatus.textContent = character.status;
    tagline.textContent = character.tagline_ja;
    characterCore.textContent = character.fixed.character_core_en;
    render2D.textContent = character.fixed.render_2d_en;
    render3D.textContent = character.fixed.render_3d_en;
    sourceNotes.textContent = character.source_notes_ja;
    sourceNotes.hidden = !character.source_notes_ja;
    sceneVariant.value = character.default_scene_en;
    updateGenerateButton();
  }

  async function loadCharacters() {
    if (state.charactersReady) return;
    if (state.charactersPromise) return state.charactersPromise;
    state.charactersPromise = (async () => {
      setStatus('キャラクター一覧を読み込み中', 'live');
      setError(error, '');
      characterRetry.hidden = true;
      characterSelect.disabled = true;
      try {
        const response = await fetch('/viewer/charimage/characters', {
          method: 'GET',
          credentials: 'same-origin',
          cache: 'no-store',
          headers: {'Accept': 'application/json'},
        });
        const payload = await response.json().catch(() => null);
        if (!response.ok) throw new Error(getErrorMessage(payload, response, 'Character catalog could not be loaded.'));
        const characters = validateCharacters(payload);
        renderCharacterList(characters);
        state.charactersReady = true;
        schedulePreview(true);
        if (characters.length !== CHARACTER_COUNT) {
          const message = `API returned ${characters.length} of ${CHARACTER_COUNT} expected characters.`;
          setError(error, message);
          characterRetry.hidden = false;
          setStatus('キャラクター一覧が不完全です', 'warning');
        } else {
          setStatus('ready', 'ready');
        }
        updateGenerateButton();
      } catch (requestError) {
        const message = requestError && requestError.message ? requestError.message : 'Character catalog could not be loaded.';
        setError(error, message);
        setStatus('キャラクター一覧を利用できません', 'error');
        characterSelect.disabled = true;
        characterRetry.hidden = false;
        throw requestError;
      } finally {
        state.charactersPromise = null;
      }
    })();
    return state.charactersPromise;
  }

  function readIntegerRange(range, label) {
    if (!range || !Number.isSafeInteger(range.min) || !Number.isSafeInteger(range.max) || range.min > range.max) {
      throw new Error(`${label} range is invalid.`);
    }
    return range;
  }

  function readSteppedRange(range, label) {
    if (!range || !Number.isFinite(range.min) || !Number.isFinite(range.max) || !Number.isFinite(range.step) ||
        range.min > range.max || range.step <= 0) {
      throw new Error(`${label} range is invalid.`);
    }
    return range;
  }

  function isSteppedValue(value, range) {
    const count = (value - range.min) / range.step;
    return Number.isFinite(count) && Math.abs(count - Math.round(count)) <= 1e-8;
  }

  function validateMossModel(model) {
    if (!model || model.id !== MOSS_MODEL_ID || typeof model.engine !== 'string' || !model.engine.trim()) {
      throw new Error('MOSS model options are invalid.');
    }
    const {defaults, limits, features} = model;
    if (!defaults || !limits || !features || typeof features.face_detailer !== 'boolean' || typeof features.lora_strength !== 'boolean') {
      throw new Error('MOSS model options are incomplete.');
    }
    const width = readIntegerRange(limits.width, 'Width');
    const height = readIntegerRange(limits.height, 'Height');
    const steps = readIntegerRange(limits.steps, 'Steps');
    const cfgScale = readSteppedRange(limits.cfg_scale, 'CFG scale');
    if (!Number.isSafeInteger(width.multiple_of) || width.multiple_of <= 0 ||
        !Number.isSafeInteger(height.multiple_of) || height.multiple_of <= 0 ||
        !Number.isSafeInteger(defaults.width) || defaults.width < width.min || defaults.width > width.max || defaults.width % width.multiple_of !== 0 ||
        !Number.isSafeInteger(defaults.height) || defaults.height < height.min || defaults.height > height.max || defaults.height % height.multiple_of !== 0 ||
        !Number.isSafeInteger(defaults.steps) || defaults.steps < steps.min || defaults.steps > steps.max ||
        !Number.isFinite(defaults.cfg_scale) || defaults.cfg_scale < cfgScale.min || defaults.cfg_scale > cfgScale.max ||
        !isSteppedValue(defaults.cfg_scale, cfgScale)) {
      throw new Error('MOSS model defaults are outside the advertised ranges.');
    }
    if (features.face_detailer && typeof defaults.face_detailer !== 'boolean') {
      throw new Error('MOSS FaceDetailer default is invalid.');
    }
    if (features.lora_strength) {
      const lora = readSteppedRange(limits.lora_strength, 'LoRA strength');
      if (!Number.isFinite(defaults.lora_strength) || defaults.lora_strength < lora.min || defaults.lora_strength > lora.max ||
          !isSteppedValue(defaults.lora_strength, lora)) {
        throw new Error('MOSS LoRA strength default is invalid.');
      }
    }
    return model;
  }

  function setNumberField(input, value, range, step) {
    input.min = String(range.min);
    input.max = String(range.max);
    input.step = String(step);
    input.value = String(value);
  }

  function configureMossParameters(model) {
    const {defaults, limits, features} = model;
    setNumberField(widthInput, defaults.width, limits.width, limits.width.multiple_of);
    setNumberField(heightInput, defaults.height, limits.height, limits.height.multiple_of);
    setNumberField(stepsInput, defaults.steps, limits.steps, 1);
    setNumberField(cfgScaleInput, defaults.cfg_scale, limits.cfg_scale, limits.cfg_scale.step);
    loraLabel.hidden = !features.lora_strength;
    loraInput.hidden = !features.lora_strength;
    if (features.lora_strength) {
      setNumberField(loraInput, defaults.lora_strength, limits.lora_strength, limits.lora_strength.step);
    }
    faceLabel.hidden = !features.face_detailer;
    if (features.face_detailer) faceInput.checked = defaults.face_detailer;
    parameters.disabled = false;
  }

  async function loadMossOptions() {
    if (state.optionsReady) return;
    if (state.optionsPromise) return state.optionsPromise;
    state.optionsPromise = (async () => {
      state.optionsReady = false;
      state.model = null;
      parameters.disabled = true;
      optionsRetry.hidden = true;
      setError(optionsError, '');
      optionsStatus.textContent = 'MOSS model settings loading…';
      updateGenerateButton();
      try {
        const response = await fetch('/viewer/image/options', {
          method: 'GET',
          credentials: 'same-origin',
          cache: 'no-store',
          headers: {'Accept': 'application/json'},
        });
        const payload = await response.json().catch(() => null);
        if (!response.ok || !payload || payload.ok !== true || !Array.isArray(payload.models)) {
          throw new Error(getErrorMessage(payload, response, 'Image model settings could not be loaded.'));
        }
        const mossModel = payload.models.find((model) => model && model.id === MOSS_MODEL_ID);
        if (!mossModel) throw new Error(`The ${MOSS_MODEL_ID} profile is unavailable in /viewer/image/options.`);
        state.model = validateMossModel(mossModel);
        configureMossParameters(state.model);
        state.optionsReady = true;
        optionsStatus.textContent = `${MOSS_MODEL_ID} · ${state.model.engine}`;
        optionsRetry.hidden = true;
        updateGenerateButton();
      } catch (requestError) {
        state.optionsReady = false;
        state.model = null;
        parameters.disabled = true;
        const message = requestError && requestError.message ? requestError.message : 'MOSS model settings could not be loaded.';
        optionsStatus.textContent = 'MOSS model settings unavailable.';
        setError(optionsError, message);
        optionsRetry.hidden = false;
        updateGenerateButton();
        throw requestError;
      } finally {
        state.optionsPromise = null;
      }
    })();
    return state.optionsPromise;
  }

  function selectedCharacter() {
    return state.characterByID.get(characterSelect.value) || null;
  }

  function currentScene() {
    return sceneVariant.value;
  }

  function setMode(mode) {
    if (mode !== '2d' && mode !== '3d') return;
    state.selectedMode = mode;
    modeButtons.forEach((button) => {
      const selected = button.dataset.charimageMode === mode;
      button.classList.toggle('active', selected);
      button.setAttribute('aria-pressed', selected ? 'true' : 'false');
    });
    schedulePreview();
  }

  function currentCompositionRequest() {
    const character = selectedCharacter();
    if (!character) throw new Error('キャラクターを選択してください。');
    return {
      character_id: character.id,
      render_mode: state.selectedMode,
      scene_variant_text: currentScene(),
    };
  }

  function schedulePreview(immediate) {
    window.clearTimeout(state.previewTimer);
    state.previewRevision += 1;
    if (state.previewController) {
      state.previewController.abort();
      state.previewController = null;
    }
    promptParts.hidden = true;
    if (!state.charactersReady || !selectedCharacter()) {
      previewSection.setAttribute('aria-busy', 'false');
      previewStatus.textContent = 'キャラクターを読み込むとサーバー合成を表示します。';
      promptPreview.textContent = 'キャラクターを読み込み中です。';
      return;
    }
    previewSection.setAttribute('aria-busy', 'true');
    previewStatus.textContent = immediate ? 'サーバーで合成中…' : '入力を待ってサーバーで合成します…';
    promptPreview.textContent = '選択内容をサーバーで合成しています…';
    const revision = state.previewRevision;
    state.previewTimer = window.setTimeout(() => composePreview(revision), immediate ? 0 : PREVIEW_DELAY_MS);
  }

  async function composePreview(revision) {
    state.previewTimer = null;
    const controller = new AbortController();
    state.previewController = controller;
    try {
      const response = await fetch('/viewer/charimage/compose', {
        method: 'POST',
        credentials: 'same-origin',
        headers: {'Accept': 'application/json', 'Content-Type': 'application/json'},
        body: JSON.stringify(currentCompositionRequest()),
        signal: controller.signal,
      });
      const payload = await response.json().catch(() => null);
      if (!response.ok || !payload || payload.ok !== true || typeof payload.final_prompt !== 'string') {
        throw new Error(getErrorMessage(payload, response, 'Prompt preview could not be composed.'));
      }
      if (revision !== state.previewRevision) return;
      promptPreview.textContent = payload.final_prompt;
      previewCore.textContent = typeof payload.character_core_prompt === 'string' ? payload.character_core_prompt : '';
      previewProfile.textContent = typeof payload.render_profile_prompt === 'string' ? payload.render_profile_prompt : '';
      previewScene.textContent = typeof payload.scene_variant_prompt === 'string' ? payload.scene_variant_prompt : '';
      promptParts.hidden = false;
      previewStatus.textContent = 'サーバー合成済み';
      previewSection.setAttribute('aria-busy', 'false');
    } catch (requestError) {
      if (requestError && requestError.name === 'AbortError') return;
      if (revision !== state.previewRevision) return;
      previewStatus.textContent = 'プロンプトを合成できませんでした';
      promptPreview.textContent = requestError && requestError.message ? requestError.message : 'Prompt preview could not be composed.';
      previewSection.setAttribute('aria-busy', 'false');
      promptParts.hidden = true;
    } finally {
      if (state.previewController === controller) state.previewController = null;
    }
  }

  function readNumber(input, label, range, integer, multipleOf) {
    const raw = input.value.trim();
    const value = Number(raw);
    if (!raw || !Number.isFinite(value)) throw new Error(`${label}を数値で入力してください。`);
    if (integer && !Number.isSafeInteger(value)) throw new Error(`${label}は整数で入力してください。`);
    if (value < range.min || value > range.max) throw new Error(`${label}は${range.min}から${range.max}の範囲で入力してください。`);
    if (multipleOf && value % multipleOf !== 0) throw new Error(`${label}は${multipleOf}の倍数で入力してください。`);
    if (!integer && !isSteppedValue(value, range)) throw new Error(`${label}は${range.step}刻みで入力してください。`);
    return value;
  }

  function buildGenerationRequest() {
    if (!state.optionsReady || !state.model) throw new Error('MOSS model settings are unavailable.');
    const scene = currentCompositionRequest();
    const {defaults, limits, features} = state.model;
    const rawSeed = seedInput.value.trim();
    const seed = Number(rawSeed);
    if (!rawSeed || !Number.isSafeInteger(seed) || seed < -1) throw new Error('Seedは-1または0以上の整数で入力してください。');
    const request = {
      ...scene,
      seed,
      width: readNumber(widthInput, 'Width', limits.width, true, limits.width.multiple_of),
      height: readNumber(heightInput, 'Height', limits.height, true, limits.height.multiple_of),
      steps: readNumber(stepsInput, 'Steps', limits.steps, true),
      cfg_scale: readNumber(cfgScaleInput, 'CFG scale', limits.cfg_scale, false),
    };
    if (features.face_detailer) request.face_detailer = faceInput.checked;
    else if (typeof defaults.face_detailer === 'boolean') request.face_detailer = defaults.face_detailer;
    if (features.lora_strength) request.lora_strength = readNumber(loraInput, 'LoRA strength', limits.lora_strength, false);
    else if (Number.isFinite(defaults.lora_strength)) request.lora_strength = defaults.lora_strength;
    return request;
  }

  function displayGeneratedImage(payload) {
    const image = payload && payload.image;
    const imageURL = image && safeSameOriginURL(image.url);
    if (!imageURL) return false;
    if (typeof image.content_type !== 'string' || !image.content_type.toLowerCase().startsWith('image/')) return false;
    resultImage.onerror = () => {
      resultImage.removeAttribute('src');
      resultFigure.hidden = true;
      resultEmpty.hidden = false;
      setError(generationError, '結果画像を読み込めませんでした。履歴から状態を確認してください。');
    };
    resultImage.src = imageURL;
    resultFigure.hidden = false;
    resultEmpty.hidden = true;
    resultPrompt.textContent = typeof payload.prompt === 'string' ? payload.prompt : promptPreview.textContent;
    resultMeta.textContent = typeof payload.id === 'string' ? `ID ${payload.id}` : '生成結果';
    return true;
  }

  function setGenerationBusy(busy) {
    state.generationBusy = busy;
    characterSelect.disabled = busy || !state.charactersReady || !state.characters.length;
    sceneVariant.disabled = busy || !state.charactersReady;
    useDefaultScene.disabled = busy || !state.charactersReady;
    modeButtons.forEach((button) => { button.disabled = busy || !state.charactersReady; });
    characterRetry.disabled = busy;
    parameters.disabled = busy || !state.optionsReady;
    optionsRetry.disabled = busy;
    seedInput.disabled = busy;
    generateButton.disabled = busy || !state.charactersReady || !state.optionsReady || !selectedCharacter();
    generateButton.textContent = busy ? 'Generating…' : 'Generate';
    historyRefresh.disabled = busy || state.historyBusy;
  }

  async function refreshHistory() {
    if (state.historyBusy) return;
    state.historyBusy = true;
    historyRefresh.disabled = true;
    historyEmpty.hidden = false;
    historyEmpty.textContent = '履歴を読み込んでいます。';
    setError(historyError, '');
    try {
      const response = await fetch('/viewer/charimage/history', {
        method: 'GET',
        credentials: 'same-origin',
        cache: 'no-store',
        headers: {'Accept': 'application/json'},
      });
      const payload = await readJSONResponse(response, 'Generation history could not be loaded.');
      if (!Array.isArray(payload.items) || payload.items.length > 1000) throw new Error('Generation history response is invalid.');
      renderHistory(payload.items.slice(0, HISTORY_LIMIT));
    } catch (requestError) {
      historyList.replaceChildren();
      historyEmpty.hidden = true;
      const message = requestError && requestError.message ? requestError.message : 'Generation history could not be loaded.';
      setError(historyError, message);
    } finally {
      state.historyBusy = false;
      historyRefresh.disabled = state.generationBusy;
    }
  }

  function renderHistory(items) {
    historyList.replaceChildren();
    historyEmpty.hidden = items.length > 0;
    historyEmpty.textContent = '生成履歴はありません。';
    for (const item of items) {
      if (!item || typeof item.id !== 'string' || typeof item.character_id !== 'string' ||
          !['2d', '3d'].includes(item.render_mode) || typeof item.scene_variant_text !== 'string' ||
          typeof item.created_at !== 'string' || typeof item.image_url !== 'string') {
        throw new Error('Generation history contains an invalid item.');
      }
      const card = document.createElement('article');
      card.className = 'charimage-history-item';
      const thumbnail = document.createElement('div');
      thumbnail.className = 'charimage-history-thumbnail';
      const imageURL = safeSameOriginURL(item.image_url);
      if (imageURL) {
        const image = document.createElement('img');
        image.loading = 'lazy';
        image.alt = '';
        image.src = imageURL;
        image.addEventListener('error', () => image.removeAttribute('src'), {once: true});
        thumbnail.append(image);
      } else {
        const missing = document.createElement('span');
        missing.textContent = 'No preview';
        thumbnail.append(missing);
      }
      const summary = document.createElement('div');
      summary.className = 'charimage-history-summary';
      const title = document.createElement('strong');
      const character = state.characterByID.get(item.character_id);
      title.textContent = character ? character.name_ja : item.character_id;
      const meta = document.createElement('span');
      const created = typeof item.created_at === 'string' ? new Date(item.created_at) : null;
      const createdText = created && !Number.isNaN(created.getTime()) ? created.toLocaleString() : String(item.created_at || '');
      meta.textContent = `${item.render_mode.toUpperCase()} · ${createdText}`;
      summary.append(title, meta);
      const edit = document.createElement('button');
      edit.type = 'button';
      edit.className = 'ctl-btn charimage-history-edit';
      edit.textContent = 'Re-edit';
      edit.disabled = !state.characterByID.has(item.character_id);
      edit.addEventListener('click', () => reeditHistoryItem(item));
      card.append(thumbnail, summary, edit);
      historyList.append(card);
    }
  }

  function reeditHistoryItem(item) {
    if (state.generationBusy) return;
    if (!state.characterByID.has(item.character_id)) {
      setError(error, 'この履歴のキャラクターは現在のcatalogにありません。');
      return;
    }
    characterSelect.value = item.character_id;
    renderSelectedCharacter(state.characterByID.get(item.character_id));
    sceneVariant.value = item.scene_variant_text;
    setMode(item.render_mode);
    setError(error, '');
    schedulePreview(true);
    characterSelect.focus();
  }

  async function generateImage() {
    if (state.generationBusy) return;
    setError(generationError, '');
    let request;
    try {
      request = buildGenerationRequest();
    } catch (requestError) {
      setError(generationError, requestError && requestError.message ? requestError.message : '入力を確認してください。');
      return;
    }
    window.clearTimeout(state.previewTimer);
    if (state.previewController) {
      state.previewController.abort();
      state.previewController = null;
    }
    setGenerationBusy(true);
    setStatus('画像を生成中', 'live');
    resultImage.removeAttribute('src');
    resultFigure.hidden = true;
    resultEmpty.hidden = false;
    resultMeta.textContent = '';
    resultPrompt.textContent = '';
    try {
      const response = await fetch('/viewer/charimage/generate', {
        method: 'POST',
        credentials: 'same-origin',
        headers: {'Accept': 'application/json', 'Content-Type': 'application/json'},
        body: JSON.stringify(request),
      });
      const payload = await response.json().catch(() => null);
      const imageDisplayed = displayGeneratedImage(payload);
      if (!response.ok || !payload || payload.ok !== true) {
        const errorCode = payload && typeof payload.error_code === 'string' ? payload.error_code : '';
        if (errorCode === 'CHARIMAGE_HISTORY_SAVE_FAILED') {
          setError(generationError, imageDisplayed
            ? '画像は表示していますが、生成履歴の保存に失敗しました。履歴を確認してください。自動再送は行いません。'
            : '生成後の履歴保存に失敗しました。結果画像を表示できないため、履歴を確認してください。自動再送は行いません。');
          setStatus('履歴保存に失敗', 'warning');
        } else if (errorCode === 'CHARIMAGE_GENERATION_RESULT_AMBIGUOUS') {
          setError(generationError, imageDisplayed
            ? '画像データを表示していますが、画像生成結果を確認できませんでした。履歴を確認してください。自動再送は行いません。'
            : '画像生成結果を確認できませんでした。履歴を確認してください。自動再送は行いません。');
          setStatus('生成結果を未確認', 'warning');
        } else {
          setError(generationError, imageDisplayed
            ? '画像データは返されましたが、生成結果の状態を確認できません。履歴を確認してください。自動再送は行いません。'
            : '画像生成結果を確認できませんでした。履歴を確認してください。自動再送は行いません。');
          setStatus('生成結果を未確認', 'warning');
        }
        await refreshHistory();
        return;
      }
      if (!imageDisplayed) {
        setError(generationError, '生成応答に同一originの画像URLがありません。履歴を確認してください。自動再生成は行いません。');
        setStatus('画像URLを利用できません', 'error');
        await refreshHistory();
        return;
      }
      setStatus('生成完了', 'ready');
      await refreshHistory();
    } catch (requestError) {
      setError(generationError, '生成の応答を確認できませんでした。重複生成を避けるため自動再送は行いません。履歴を更新して状態を確認してください。');
      setStatus('応答未確認', 'warning');
      await refreshHistory();
    } finally {
      setGenerationBusy(false);
    }
  }

  async function refreshCharImageTab() {
    const results = await Promise.allSettled([loadCharacters(), loadMossOptions()]);
    if (results.some((result) => result.status === 'rejected')) {
      if (!state.charactersReady) characterSelect.disabled = true;
      updateGenerateButton();
    }
    await refreshHistory();
  }

  characterSelect.addEventListener('change', () => {
    renderSelectedCharacter(selectedCharacter());
    setError(error, '');
    schedulePreview();
  });
  sceneVariant.addEventListener('input', () => schedulePreview());
  useDefaultScene.addEventListener('click', () => {
    const character = selectedCharacter();
    if (!character) return;
    sceneVariant.value = character.default_scene_en;
    sceneVariant.focus();
    schedulePreview();
  });
  modeButtons.forEach((button) => button.addEventListener('click', () => setMode(button.dataset.charimageMode)));
  characterRetry.addEventListener('click', () => {
    state.charactersReady = false;
    loadCharacters().catch(() => {});
  });
  optionsRetry.addEventListener('click', () => loadMossOptions().catch(() => {}));
  historyRefresh.addEventListener('click', () => refreshHistory());
  generateButton.addEventListener('click', () => generateImage());
  window.refreshCharImageTab = refreshCharImageTab;
  updateGenerateButton();

  function updateGenerateButton() {
    generateButton.disabled = state.generationBusy || !state.charactersReady || !state.optionsReady || !selectedCharacter();
  }
})();
