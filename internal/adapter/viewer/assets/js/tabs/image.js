'use strict';

(function initImageTab() {
  const form = document.getElementById('imageGenerationForm');
  const modeInputs = form ? Array.from(form.querySelectorAll('input[name="imageMode"]')) : [];
  const promptLabel = document.getElementById('imagePromptLabel');
  const prompt = document.getElementById('imagePrompt');
  const negativePrompt = document.getElementById('imageNegativePrompt');
  const textToImageControls = document.getElementById('imageTextToImageControls');
  const seed = document.getElementById('imageSeed');
  const editOptionsStatus = document.getElementById('imageEditOptionsStatus');
  const editInputs = document.getElementById('imageEditInputs');
  const personImageInput = document.getElementById('imagePersonImage');
  const referenceImageInput = document.getElementById('imageReferenceImage');
  const referenceImageLabel = document.getElementById('imageReferenceImageLabel');
  const dynamicOptions = document.getElementById('imageDynamicOptions');
  const optionsStatus = document.getElementById('imageOptionsStatus');
  const optionsError = document.getElementById('imageOptionsError');
  const optionsRetry = document.getElementById('imageOptionsRetry');
  const modelSelect = document.getElementById('imageModel');
  const widthInput = document.getElementById('imageWidth');
  const heightInput = document.getElementById('imageHeight');
  const stepsInput = document.getElementById('imageSteps');
  const cfgScaleInput = document.getElementById('imageCfgScale');
  const loraStrengthField = document.getElementById('imageLoraStrengthField');
  const loraStrengthInput = document.getElementById('imageLoraStrength');
  const faceDetailerField = document.getElementById('imageFaceDetailerField');
  const faceDetailerInput = document.getElementById('imageFaceDetailer');
  const generateButton = document.getElementById('imageGenerateBtn');
  const status = document.getElementById('imageStatus');
  const empty = document.getElementById('imageResultEmpty');
  const figure = document.getElementById('imageResultFigure');
  const image = document.getElementById('imageResult');
  const resultPrompt = document.getElementById('imageResultPrompt');
  const resultMeta = document.getElementById('imageResultMeta');
  const error = document.getElementById('imageError');
  const modeOptions = new Map(modeInputs.map((modeInput) => [modeInput.value, modeInput]));
  if (!form || modeInputs.length !== 3 || !modeOptions.has('t2i') || !modeOptions.has('pose') || !modeOptions.has('clothing') ||
      !promptLabel || !prompt || !textToImageControls || !seed || !editOptionsStatus ||
      !editInputs || !personImageInput || !referenceImageInput || !referenceImageLabel || !dynamicOptions ||
      !modelSelect || !widthInput || !heightInput || !stepsInput || !cfgScaleInput || !loraStrengthField ||
      !loraStrengthInput || !faceDetailerField || !faceDetailerInput || !generateButton || !status || !image ||
      !optionsStatus || !optionsError || !optionsRetry || !error) return;

  const maxImageUploadBytes = 20 * 1024 * 1024;
  const maxImageEditPromptBytes = 8 * 1024;
  const profileIDPattern = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
  const allowedEditOperations = new Set(['pose', 'clothing']);
  let modelOptions = [];
  let selectedModel = null;
  let defaultModel = '';
  let optionsReady = false;
  let editOptionsReady = false;
  let editOperations = new Set();
  let generationBusy = false;

  function activeMode() {
    const selectedMode = modeInputs.find((modeInput) => modeInput.checked);
    return selectedMode ? selectedMode.value : '';
  }

  function modeReady() {
    const operation = activeMode();
    if (operation === 't2i') return optionsReady;
    return editOptionsReady && editOperations.has(operation);
  }

  function setStatus(value, isLive) {
    status.textContent = value;
    status.classList.toggle('is-live', Boolean(isLive));
  }

  function showError(message) {
    error.textContent = message;
    error.hidden = false;
    setStatus('failed', false);
  }

  function updateModeUI() {
    const operation = activeMode();
    const isEdit = allowedEditOperations.has(operation);
    const isTextToImage = operation === 't2i';
    promptLabel.textContent = isEdit ? 'Edit prompt' : 'Prompt';
    prompt.placeholder = isEdit ? '変更したい内容を入力' : '描きたい画像を入力';
    textToImageControls.hidden = !isTextToImage;
    editInputs.hidden = !isEdit;
    dynamicOptions.hidden = !isTextToImage || !optionsReady || modelOptions.length === 0;
    dynamicOptions.disabled = generationBusy || !isTextToImage || !optionsReady || modelOptions.length === 0;
    personImageInput.disabled = generationBusy || !isEdit;
    referenceImageInput.disabled = generationBusy || !isEdit;
    referenceImageLabel.textContent = operation === 'pose' ? 'ポーズ参照画像' : '着せたい服の画像';
    for (const [operation, option] of modeOptions) {
      option.disabled = generationBusy || (operation !== 't2i' && (!editOptionsReady || !editOperations.has(operation)));
    }
    generateButton.disabled = generationBusy || !modeReady();
    generateButton.textContent = generationBusy ? 'Generating...' : (isEdit ? 'Edit image' : 'Generate image');
  }

  function setBusy(busy) {
    generationBusy = busy;
    prompt.disabled = busy;
    if (negativePrompt) negativePrompt.disabled = busy;
    seed.disabled = busy;
    modelSelect.disabled = busy || modelOptions.length === 0;
    widthInput.disabled = busy;
    heightInput.disabled = busy;
    stepsInput.disabled = busy;
    cfgScaleInput.disabled = busy;
    loraStrengthInput.disabled = busy;
    faceDetailerInput.disabled = busy;
    updateModeUI();
    if (busy) setStatus('generating', true);
  }

  function isStepValue(value, min, step) {
    const steps = (value - min) / step;
    return Number.isFinite(steps) && Math.abs(steps - Math.round(steps)) <= 1e-8;
  }

  function readIntegerRange(range, name) {
    if (!range || !Number.isSafeInteger(range.min) || !Number.isSafeInteger(range.max) || range.min > range.max) {
      throw new Error(`${name}の範囲が正しくありません。`);
    }
    return range;
  }

  function readSteppedRange(range, name) {
    if (!range || !Number.isFinite(range.min) || !Number.isFinite(range.max) || !Number.isFinite(range.step) ||
        range.min > range.max || range.step <= 0) {
      throw new Error(`${name}の範囲が正しくありません。`);
    }
    return range;
  }

  function isIntegerDefault(value, range, multipleOf) {
    return Number.isSafeInteger(value) && value >= range.min && value <= range.max && value % multipleOf === 0;
  }

  function isSteppedDefault(value, range) {
    return Number.isFinite(value) && value >= range.min && value <= range.max && isStepValue(value, range.min, range.step);
  }

  function validateModel(model) {
    if (!model || typeof model.id !== 'string' || !profileIDPattern.test(model.id) ||
        typeof model.engine !== 'string' || !model.engine.trim()) {
      throw new Error('モデル名またはengineが正しくありません。');
    }
    const defaults = model.defaults;
    const limits = model.limits;
    const features = model.features;
    if (!defaults || !limits || !features || typeof features.face_detailer !== 'boolean' || typeof features.lora_strength !== 'boolean') {
      throw new Error(`モデル「${model.id}」の設定が不足しています。`);
    }

    const width = readIntegerRange(limits.width, 'Width');
    const height = readIntegerRange(limits.height, 'Height');
    const steps = readIntegerRange(limits.steps, 'Steps');
    if (!Number.isSafeInteger(limits.width.multiple_of) || limits.width.multiple_of <= 0 ||
        !Number.isSafeInteger(limits.height.multiple_of) || limits.height.multiple_of <= 0) {
      throw new Error(`モデル「${model.id}」の画像サイズ刻みが正しくありません。`);
    }
    const cfgScale = readSteppedRange(limits.cfg_scale, 'CFG scale');
    if (!isIntegerDefault(defaults.width, width, limits.width.multiple_of) ||
        !isIntegerDefault(defaults.height, height, limits.height.multiple_of) ||
        !Number.isSafeInteger(defaults.steps) || defaults.steps < steps.min || defaults.steps > steps.max ||
        !isSteppedDefault(defaults.cfg_scale, cfgScale)) {
      throw new Error(`モデル「${model.id}」の既定値が範囲外です。`);
    }

    if (features.face_detailer && typeof defaults.face_detailer !== 'boolean') {
      throw new Error(`モデル「${model.id}」のFaceDetailer既定値が正しくありません。`);
    }
    if (features.lora_strength) {
      const loraStrength = readSteppedRange(limits.lora_strength, 'LoRA strength');
      if (!isSteppedDefault(defaults.lora_strength, loraStrength)) {
        throw new Error(`モデル「${model.id}」のLoRA strength既定値が範囲外です。`);
      }
    }
    return model;
  }

  function validateOptions(payload) {
    if (!payload || payload.ok !== true || typeof payload.default_model !== 'string' ||
        !profileIDPattern.test(payload.default_model) || !Array.isArray(payload.models) || payload.models.length > 256) {
      throw new Error('画像モデル設定が正しくありません。');
    }
    const seenIDs = new Set();
    const models = payload.models.map((model) => {
      validateModel(model);
      if (seenIDs.has(model.id)) throw new Error(`モデルID「${model.id}」が重複しています。`);
      seenIDs.add(model.id);
      return model;
    });
    if (models.length > 0 && !models.some((model) => model.id === payload.default_model)) {
      throw new Error('既定の画像モデルがモデル一覧にありません。');
    }
    return {models, defaultModel: payload.default_model};
  }

  function validateEditOptions(payload) {
    if (!payload || payload.ok !== true || !Array.isArray(payload.operations)) {
      throw new Error('画像編集設定が正しくありません。');
    }
    const operations = new Set();
    for (const operation of payload.operations) {
      if (!allowedEditOperations.has(operation) || operations.has(operation)) {
        throw new Error('画像編集設定が正しくありません。');
      }
      operations.add(operation);
    }
    return operations;
  }

  function setNumberInput(input, value, range, step) {
    input.min = String(range.min);
    input.max = String(range.max);
    input.step = String(step);
    input.value = String(value);
  }

  function selectModel(modelID, useDefaults) {
    selectedModel = modelOptions.find((model) => model.id === modelID) || null;
    if (!selectedModel) return;

    const {defaults, limits, features} = selectedModel;
    if (useDefaults) {
      setNumberInput(widthInput, defaults.width, limits.width, limits.width.multiple_of);
      setNumberInput(heightInput, defaults.height, limits.height, limits.height.multiple_of);
      setNumberInput(stepsInput, defaults.steps, limits.steps, 1);
      setNumberInput(cfgScaleInput, defaults.cfg_scale, limits.cfg_scale, limits.cfg_scale.step);
    }

    loraStrengthField.hidden = !features.lora_strength;
    loraStrengthInput.hidden = !features.lora_strength;
    if (features.lora_strength && useDefaults) {
      setNumberInput(loraStrengthInput, defaults.lora_strength, limits.lora_strength, limits.lora_strength.step);
    }
    faceDetailerField.hidden = !features.face_detailer;
    if (features.face_detailer && useDefaults) faceDetailerInput.checked = defaults.face_detailer;
  }

  function readNumberInput(input, label, range, integer, multipleOf) {
    const rawValue = input.value.trim();
    const value = Number(rawValue);
    if (!rawValue || !Number.isFinite(value)) throw new Error(`${label}を数値で入力してください。`);
    if (integer && !Number.isSafeInteger(value)) throw new Error(`${label}は整数で入力してください。`);
    if (value < range.min || value > range.max) throw new Error(`${label}は${range.min}から${range.max}の範囲で入力してください。`);
    if (multipleOf && value % multipleOf !== 0) throw new Error(`${label}は${multipleOf}の倍数で入力してください。`);
    if (!integer && !isStepValue(value, range.min, range.step)) throw new Error(`${label}は${range.step}刻みで入力してください。`);
    return value;
  }

  function readSeed() {
    const seedText = seed.value.trim();
    const seedValue = Number(seedText);
    if (!seedText || !Number.isSafeInteger(seedValue) || seedValue < -1) {
      throw new Error('Seedは-1または0以上の整数で入力してください。');
    }
    return seedValue;
  }

  function buildRequestBody(promptText) {
    if (!optionsReady) throw new Error('画像モデル設定を読み込んでください。');
    const requestBody = {
      prompt: promptText,
      negative_prompt: negativePrompt ? negativePrompt.value.trim() : '',
      seed: readSeed(),
    };
    if (modelOptions.length === 0) return requestBody;
    if (!selectedModel) throw new Error('画像モデル設定を選択してください。');

    const {limits, features} = selectedModel;
    requestBody.model = selectedModel.id;
    requestBody.width = readNumberInput(widthInput, 'Width', limits.width, true, limits.width.multiple_of);
    requestBody.height = readNumberInput(heightInput, 'Height', limits.height, true, limits.height.multiple_of);
    requestBody.steps = readNumberInput(stepsInput, 'Steps', limits.steps, true);
    requestBody.cfg_scale = readNumberInput(cfgScaleInput, 'CFG scale', limits.cfg_scale, false);
    if (features.face_detailer) requestBody.face_detailer = faceDetailerInput.checked;
    if (features.lora_strength) {
      requestBody.lora_strength = readNumberInput(loraStrengthInput, 'LoRA strength', limits.lora_strength, false);
    }
    return requestBody;
  }

  function uploadContentType(file, label) {
    if (!file || file.size <= 0) throw new Error(`${label}を選択してください。`);
    if (file.size > maxImageUploadBytes) throw new Error(`${label}は20 MiB以下のPNGまたはJPEGを選択してください。`);
    const declaredType = String(file.type || '').toLowerCase();
    if (declaredType === 'image/png' || declaredType === 'image/jpeg') return declaredType;
    if (declaredType) throw new Error(`${label}はPNGまたはJPEGを選択してください。`);
    const name = String(file.name || '').toLowerCase();
    if (name.endsWith('.png')) return 'image/png';
    if (name.endsWith('.jpg') || name.endsWith('.jpeg')) return 'image/jpeg';
    throw new Error(`${label}はPNGまたはJPEGを選択してください。`);
  }

  function appendImageFile(formData, field, file, label) {
    const contentType = uploadContentType(file, label);
    const upload = file.type === contentType ? file : new File([file], file.name, {type: contentType});
    formData.append(field, upload, file.name);
  }

  function buildEditRequest(operation, promptText) {
    if (!editOptionsReady || !editOperations.has(operation)) throw new Error('PoseまたはClothingの編集設定が利用できません。');
    const promptBytes = new TextEncoder().encode(promptText).length;
    if (promptBytes > maxImageEditPromptBytes) throw new Error('編集promptは8 KiB以内で入力してください。');
    const formData = new FormData();
    formData.append('operation', operation);
    appendImageFile(formData, 'person', personImageInput.files[0], '人物画像');
    appendImageFile(formData, 'reference', referenceImageInput.files[0], operation === 'pose' ? 'ポーズ参照画像' : '着せたい服の画像');
    formData.append('prompt', promptText);
    formData.append('seed', String(readSeed()));
    return formData;
  }

  function showOptionsError(message) {
    optionsReady = false;
    modelOptions = [];
    selectedModel = null;
    defaultModel = '';
    dynamicOptions.hidden = true;
    dynamicOptions.disabled = true;
    optionsStatus.textContent = '画像モデル設定を利用できません。';
    optionsError.textContent = message;
    optionsError.hidden = false;
    optionsRetry.hidden = false;
    optionsRetry.disabled = false;
    updateModeUI();
    setStatus(activeMode() === 't2i' ? 'options unavailable' : 'ready', false);
  }

  async function loadOptions() {
    optionsReady = false;
    modelOptions = [];
    selectedModel = null;
    defaultModel = '';
    dynamicOptions.hidden = true;
    dynamicOptions.disabled = true;
    optionsError.hidden = true;
    optionsRetry.hidden = true;
    optionsRetry.disabled = true;
    optionsStatus.textContent = '画像モデル設定を読み込み中です。';
    if (activeMode() === 't2i') setStatus('loading models', true);
    updateModeUI();

    try {
      const response = await fetch('/viewer/image/options', {
        method: 'GET',
        credentials: 'same-origin',
        headers: {'Accept': 'application/json'},
      });
      const payload = await response.json().catch(() => null);
      if (!response.ok) {
        const message = payload && typeof payload.message === 'string' ? payload.message : `HTTP ${response.status}`;
        throw new Error(message);
      }
      const validatedOptions = validateOptions(payload);
      modelOptions = validatedOptions.models;
      defaultModel = validatedOptions.defaultModel;
      modelSelect.replaceChildren(...modelOptions.map((model) => {
        const option = document.createElement('option');
        option.value = model.id;
        option.textContent = `${model.id} (${model.engine})`;
        return option;
      }));
      optionsReady = true;
      if (modelOptions.length > 0) {
        modelSelect.value = defaultModel;
        selectModel(defaultModel, true);
        optionsStatus.textContent = `${modelOptions.length}個の画像モデルを利用できます。`;
      } else {
        optionsStatus.textContent = `RenCrow_Imageの既定profile「${defaultModel}」を使用します。Model固有のparameterは指定できません。`;
      }
      optionsError.hidden = true;
      optionsRetry.hidden = true;
      updateModeUI();
      if (activeMode() === 't2i') setStatus('ready', false);
    } catch (optionsRequestError) {
      const message = optionsRequestError && optionsRequestError.message
        ? optionsRequestError.message
        : '画像モデル設定を取得できませんでした。';
      showOptionsError(`画像モデル設定を読み込めませんでした: ${message}`);
    }
  }

  async function loadEditOptions() {
    editOptionsReady = false;
    editOperations = new Set();
    editOptionsStatus.textContent = 'ポーズ参照と服画像で着せ替えの利用可否を確認しています。';
    updateModeUI();

    try {
      const response = await fetch('/viewer/image/edit/options', {
        method: 'GET',
        credentials: 'same-origin',
        headers: {'Accept': 'application/json'},
      });
      const payload = await response.json().catch(() => null);
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      editOperations = validateEditOptions(payload);
      editOptionsReady = true;
      const availableModes = Array.from(editOperations, (operation) => operation === 'pose' ? 'ポーズ参照' : '服画像で着せ替え');
      editOptionsStatus.textContent = editOperations.size > 0
        ? `${availableModes.join(' / ')}を利用できます。`
        : '画像編集workflowが設定されていません。';
      updateModeUI();
    } catch (_) {
      editOperations = new Set();
      editOptionsReady = false;
      editOptionsStatus.textContent = '画像編集workflowが設定されていないため、ポーズ参照と服画像で着せ替えは利用できません。';
      updateModeUI();
    }
  }

  function displayResult(payload, promptText, operation) {
    if (!payload || !payload.ok || !payload.image || !payload.image.url) {
      throw new Error('画像生成に失敗しました。結果を受け取れませんでした。');
    }
    const resultURL = new URL(payload.image.url, window.location.href);
    if (resultURL.origin !== window.location.origin) throw new Error('同一originの画像結果だけを表示できます。');
    resultURL.searchParams.set('v', payload.id || String(Date.now()));
    image.onload = function onImageLoad() {
      setBusy(false);
      setStatus('complete', false);
    };
    image.onerror = function onImageError() {
      setBusy(false);
      showError('生成結果の画像を取得できませんでした。');
    };
    image.src = resultURL.href;
    resultPrompt.textContent = payload.prompt || promptText;
    resultMeta.textContent = operation
      ? `Image edit / ${operation}`
      : `${payload.profile || defaultModel || 'RenCrow_Image'} / ${payload.image.width || '-'}×${payload.image.height || '-'}`;
    empty.hidden = true;
    figure.hidden = false;
  }

  async function generateImage() {
    if (generationBusy) return;
    const operation = activeMode();
    const promptText = prompt.value.trim();
    if (!promptText) {
      showError('プロンプトを入力してください。');
      prompt.focus();
      return;
    }
    let requestBody;
    try {
      if (!modeReady()) throw new Error('選択した画像モードが利用できません。設定を確認してください。');
      requestBody = operation === 't2i' ? buildRequestBody(promptText) : buildEditRequest(operation, promptText);
    } catch (requestError) {
      showError(requestError && requestError.message ? requestError.message : '画像生成設定を確認してください。');
      return;
    }

    error.hidden = true;
    setBusy(true);
    try {
      const isEdit = operation !== 't2i';
      const response = await fetch(isEdit ? '/viewer/image/edit' : '/viewer/image/generate', {
        method: 'POST',
        credentials: 'same-origin',
        headers: {'Accept': 'application/json', ...(isEdit ? {} : {'Content-Type': 'application/json'})},
        body: isEdit ? requestBody : JSON.stringify(requestBody),
      });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok || !payload.ok || !payload.image || !payload.image.url) {
        throw new Error(payload.message || `画像生成に失敗しました。HTTP ${response.status}`);
      }
      displayResult(payload, promptText, isEdit ? operation : '');
    } catch (requestError) {
      setBusy(false);
      showError(requestError && requestError.message ? requestError.message : '画像生成に失敗しました。');
    }
  }

  form.addEventListener('submit', (event) => {
    event.preventDefault();
    generateImage();
  });
  modeInputs.forEach((modeInput) => modeInput.addEventListener('change', () => {
    if (!modeInput.checked) return;
    error.hidden = true;
    updateModeUI();
    setStatus(modeReady() ? 'ready' : 'options unavailable', false);
  }));
  modelSelect.addEventListener('change', () => {
    selectModel(modelSelect.value, true);
    updateModeUI();
  });
  optionsRetry.addEventListener('click', loadOptions);
  prompt.addEventListener('keydown', function onPromptKeydown(event) {
    if ((event.ctrlKey || event.metaKey) && event.key === 'Enter') {
      event.preventDefault();
      form.requestSubmit();
    }
  });
  loadOptions();
  loadEditOptions();
})();

(function initMovieTab() {
  const form = document.getElementById('movieGenerationForm');
  const promptRow = document.getElementById('moviePromptRow');
  const prompt = document.getElementById('moviePrompt');
  const taskInputs = Array.from(document.querySelectorAll('input[name="movieTask"]'));
  const mode = document.getElementById('movieMode');
  const v2vModeOption = mode && mode.querySelector('option[value="v2v"]');
  const aspectRatio = document.getElementById('movieAspectRatio');
  const aspectRatioRow = document.getElementById('movieAspectRatioRow');
  const optionsStatus = document.getElementById('movieOptionsStatus');
  const optionsError = document.getElementById('movieOptionsError');
  const optionsRetry = document.getElementById('movieOptionsRetry');
  const movieModel = document.getElementById('movieModel');
  const movieMegapixels = document.getElementById('movieMegapixels');
  const movieDurationSeconds = document.getElementById('movieDurationSeconds');
  const movieDurationLabel = document.getElementById('movieDurationLabel');
  const movieSteps = document.getElementById('movieSteps');
  const movieResolutionLabel = document.getElementById('movieResolutionLabel');
  const movieResolutionValue = document.getElementById('movieResolutionValue');
  const movieStepsLabel = document.getElementById('movieStepsLabel');
  const movieStepsValue = document.getElementById('movieStepsValue');
  const movieMaxVideoBytesLabel = document.getElementById('movieMaxVideoBytesLabel');
  const movieMaxVideoBytes = document.getElementById('movieMaxVideoBytes');
  const movieMaxAudioBytesLabel = document.getElementById('movieMaxAudioBytesLabel');
  const movieMaxAudioBytes = document.getElementById('movieMaxAudioBytes');
  const inputVideoRow = document.getElementById('movieInputVideoRow');
  const inputVideo = document.getElementById('movieInputVideo');
  const inputVideoLimit = document.getElementById('movieInputVideoLimit');
  const inputImageRow = document.getElementById('movieInputImageRow');
  const inputImage = document.getElementById('movieInputImage');
  const inputAudioRow = document.getElementById('movieInputAudioRow');
  const inputAudio = document.getElementById('movieInputAudio');
  const inputAudioLabel = document.getElementById('movieInputAudioLabel');
  const inputAudioLimit = document.getElementById('movieInputAudioLimit');
  const seedRow = document.getElementById('movieSeedRow');
  const seed = document.getElementById('movieSeed');
  const generateButton = document.getElementById('movieGenerateBtn');
  const status = document.getElementById('movieStatus');
  const empty = document.getElementById('videoResultEmpty');
  const error = document.getElementById('movieError');
  const videoPanel = document.getElementById('videoResultPanel');
  const videoStatus = document.getElementById('videoJobStatus');
  const videoProgress = document.getElementById('videoProgress');
  const video = document.getElementById('videoResult');
  const download = document.getElementById('videoDownload');
  if (!form || !promptRow || !prompt || taskInputs.length !== 2 || !taskInputs.some((input) => input.value === 'h3') ||
      !taskInputs.some((input) => input.value === 'lip_sync') || !mode || !v2vModeOption || !aspectRatio || !aspectRatioRow || !optionsStatus ||
      !optionsError || !optionsRetry || !movieModel || !movieMegapixels || !movieDurationSeconds ||
      !movieDurationLabel || !movieSteps || !movieResolutionLabel || !movieResolutionValue || !movieStepsLabel || !movieStepsValue ||
      !movieMaxVideoBytesLabel || !movieMaxVideoBytes || !movieMaxAudioBytesLabel || !movieMaxAudioBytes ||
      !inputVideoRow || !inputVideo || !inputVideoLimit || !inputImageRow || !inputImage || !inputAudioRow || !inputAudio ||
      !inputAudioLabel || !inputAudioLimit || !seedRow || !generateButton || !status || !video) return;

  const savedVideoJobKey = 'rencrow.image.video.lastJobId';
  const savedVideoPendingKey = 'rencrow.image.video.pendingRequest';
  const videoJobPattern = /^vid_[A-Za-z0-9_-]{1,128}$/;
  const maxVideoImageBytes = 20 * 1024 * 1024;
  const maxVideoAudioBytes = 20 * 1024 * 1024;
  const maxVideoV2VBytes = 100 * 1024 * 1024;
  const maxVideoAspectRatioBytes = 64;
  const defaultAudioLimitNote = inputAudioLimit.textContent;
  let currentVideoID = '';
  let pollTimer = 0;
  let videoSubmitBusy = false;
  let videoOptions = null;
  let videoOptionsReady = false;
  let videoOptionsRevision = 0;

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
    generateButton.disabled = busy || !videoOptionsReady;
    prompt.disabled = busy || mode.value === 'v2v';
    for (const input of taskInputs) input.disabled = busy;
    mode.disabled = busy;
    inputVideo.disabled = busy || mode.value !== 'v2v';
    if (inputImage) inputImage.disabled = busy || mode.value !== 'i2v';
    inputAudio.disabled = busy || selectedVideoTask() !== 'lip_sync';
    if (seed) seed.disabled = busy || mode.value === 'v2v';
    aspectRatio.disabled = busy || !videoOptionsReady || mode.value === 'v2v';
    generateButton.textContent = label || (busy ? 'Generating...' : 'Generate video');
    if (busy) setStatus('generating', true);
  }

  function revealVideoResult() {
    empty.hidden = true;
    videoPanel.hidden = false;
  }

  function updateVideoModeInputs() {
    const lipSync = selectedVideoTask() === 'lip_sync';
    v2vModeOption.hidden = !lipSync;
    v2vModeOption.disabled = !lipSync;
    if (!lipSync && mode.value === 'v2v') mode.value = 't2v';
    const v2v = mode.value === 'v2v';
    promptRow.hidden = v2v;
    prompt.disabled = videoSubmitBusy || v2v;
    aspectRatioRow.hidden = v2v;
    aspectRatio.disabled = videoSubmitBusy || !videoOptionsReady || v2v;
    inputVideoRow.hidden = !v2v;
    inputVideo.disabled = videoSubmitBusy || !v2v;
    inputImageRow.hidden = v2v || mode.value !== 'i2v';
    inputImage.disabled = videoSubmitBusy || v2v || mode.value !== 'i2v';
    inputAudioRow.hidden = !lipSync;
    inputAudio.disabled = videoSubmitBusy || !lipSync;
    seedRow.hidden = v2v;
    seed.disabled = videoSubmitBusy || v2v;
    movieResolutionLabel.hidden = v2v;
    movieResolutionValue.hidden = v2v;
    movieStepsLabel.hidden = v2v;
    movieStepsValue.hidden = v2v;
    movieMaxVideoBytesLabel.hidden = !v2v;
    movieMaxVideoBytes.hidden = !v2v;
    movieMaxAudioBytesLabel.hidden = !v2v;
    movieMaxAudioBytes.hidden = !v2v;
  }

  function selectedVideoTask() {
    const selected = taskInputs.find((input) => input.checked);
    return selected ? selected.value : 'h3';
  }

  function validAspectRatio(value) {
    return typeof value === 'string' && value.length > 0 && new TextEncoder().encode(value).length <= maxVideoAspectRatioBytes &&
      value.trim() === value && !/[\u0000-\u001f\u007f]/.test(value);
  }

  function formatVideoByteLimit(bytes) {
    const mib = bytes / (1024 * 1024);
    const formattedMib = Number.isInteger(mib) ? String(mib) : mib.toFixed(2);
    return `${formattedMib} MiB (${new Intl.NumberFormat().format(bytes)} bytes)`;
  }

  function validateVideoOptions(payload, requestedMode, requestedTask) {
    if (!payload || typeof payload !== 'object' || payload.mode !== requestedMode ||
        (requestedTask === 'lip_sync' ? payload.task !== 'lip_sync' : Object.prototype.hasOwnProperty.call(payload, 'task')) ||
        typeof payload.model !== 'string' || !payload.model.trim() || payload.model.trim() !== payload.model ||
        payload.model.length > 128) {
      throw new Error('動画モデル設定が正しくありません。');
    }
    if (requestedMode === 'v2v') {
      const hasIrrelevantFields = ['aspect_ratios', 'default_aspect_ratio', 'megapixels', 'steps']
        .some((field) => Object.prototype.hasOwnProperty.call(payload, field));
      if (requestedTask !== 'lip_sync' || payload.model !== 'MuseTalk 1.5' || hasIrrelevantFields ||
          payload.duration_seconds !== 10 || !Number.isSafeInteger(payload.max_video_bytes) ||
          payload.max_video_bytes < 1 || payload.max_video_bytes > maxVideoV2VBytes ||
          payload.max_audio_bytes !== maxVideoAudioBytes) {
        throw new Error('動画モデル設定が正しくありません。');
      }
      return {
        model: payload.model,
        mode: payload.mode,
        task: requestedTask,
        durationSeconds: payload.duration_seconds,
        maxVideoBytes: payload.max_video_bytes,
        maxAudioBytes: payload.max_audio_bytes,
      };
    }
    if (!['t2v', 'i2v'].includes(requestedMode) || !Array.isArray(payload.aspect_ratios) || payload.aspect_ratios.length < 1 ||
        payload.aspect_ratios.length > 32 || !validAspectRatio(payload.default_aspect_ratio) ||
        !Number.isFinite(payload.megapixels) || payload.megapixels <= 0 || payload.megapixels > 64 ||
        !Number.isSafeInteger(payload.duration_seconds) || payload.duration_seconds < 1 || payload.duration_seconds > 3600 ||
        !Number.isSafeInteger(payload.steps) || payload.steps < 1 || payload.steps > 10000) {
      throw new Error('動画モデル設定が正しくありません。');
    }
    const seen = new Set();
    for (const value of payload.aspect_ratios) {
      if (!validAspectRatio(value) || seen.has(value)) throw new Error('動画の縦横比設定が正しくありません。');
      seen.add(value);
    }
    if (!seen.has(payload.default_aspect_ratio)) throw new Error('既定の縦横比が一覧にありません。');
    return {
      model: payload.model,
      mode: payload.mode,
      task: requestedTask,
      aspectRatios: [...payload.aspect_ratios],
      defaultAspectRatio: payload.default_aspect_ratio,
      megapixels: payload.megapixels,
      durationSeconds: payload.duration_seconds,
      steps: payload.steps,
    };
  }

  async function loadVideoOptions(requestedMode = mode.value, preferredAspect = '', requestedTask = selectedVideoTask()) {
    const revision = ++videoOptionsRevision;
    videoOptionsReady = false;
    videoOptions = null;
    optionsStatus.textContent = 'Loading video options…';
    optionsError.hidden = true;
    optionsError.textContent = '';
    optionsRetry.hidden = true;
    aspectRatio.replaceChildren();
    movieModel.textContent = '—';
    movieMegapixels.textContent = '—';
    movieDurationSeconds.textContent = '—';
    movieDurationLabel.textContent = requestedMode === 'v2v'
      ? 'Duration limit'
      : requestedTask === 'lip_sync' ? 'Audio max duration' : 'Duration';
    movieSteps.textContent = '—';
    movieMaxVideoBytes.textContent = '—';
    movieMaxAudioBytes.textContent = '—';
    updateVideoModeInputs();
    setBusy(false);

    try {
      const query = new URLSearchParams({mode: requestedMode});
      if (requestedTask === 'lip_sync') query.set('task', 'lip_sync');
      const response = await fetch(`/viewer/image/video/options?${query.toString()}`, {
        method: 'GET',
        cache: 'no-store',
        credentials: 'same-origin',
        headers: {'Accept': 'application/json'},
      });
      const payload = await response.json().catch(() => null);
      if (!response.ok) {
        const message = payload && typeof payload.message === 'string' ? payload.message : `HTTP ${response.status}`;
        throw new Error(message);
      }
      const options = validateVideoOptions(payload, requestedMode, requestedTask);
      if (revision !== videoOptionsRevision || mode.value !== requestedMode || selectedVideoTask() !== requestedTask) return null;
      videoOptions = options;
      if (requestedMode === 'v2v') {
        movieMaxVideoBytes.textContent = formatVideoByteLimit(options.maxVideoBytes);
        movieMaxAudioBytes.textContent = formatVideoByteLimit(options.maxAudioBytes);
        inputVideoLimit.textContent = `MP4 · up to ${formatVideoByteLimit(options.maxVideoBytes)}`;
        inputAudioLabel.textContent = 'Audio input (WAV PCM)';
        inputAudioLimit.textContent = `WAV PCM · up to ${formatVideoByteLimit(options.maxAudioBytes)}`;
      } else {
        const choices = options.aspectRatios.map((value) => {
          const option = document.createElement('option');
          option.value = value;
          option.textContent = value;
          return option;
        });
        const selectedAspect = preferredAspect && validAspectRatio(preferredAspect) ? preferredAspect : options.defaultAspectRatio;
        if (preferredAspect && !options.aspectRatios.includes(preferredAspect)) {
          const previous = document.createElement('option');
          previous.value = preferredAspect;
          previous.textContent = `${preferredAspect} (previous request)`;
          choices.push(previous);
        }
        aspectRatio.replaceChildren(...choices);
        aspectRatio.value = selectedAspect;
        inputAudioLabel.textContent = 'Speech or singing audio (WAV PCM)';
        inputAudioLimit.textContent = defaultAudioLimitNote;
      }
      movieModel.textContent = options.model;
      if (requestedMode !== 'v2v') movieMegapixels.textContent = String(options.megapixels);
      movieDurationSeconds.textContent = String(options.durationSeconds);
      if (requestedMode !== 'v2v') movieSteps.textContent = String(options.steps);
      videoOptionsReady = true;
      optionsStatus.textContent = `Video options available · ${options.mode}${requestedTask === 'lip_sync' ? ' · audio sync' : ''}`;
      updateVideoModeInputs();
      setBusy(false);
      return options;
    } catch (requestError) {
      if (revision !== videoOptionsRevision) return null;
      optionsStatus.textContent = 'Video options unavailable';
      const errorMessage = requestError && requestError.message ? requestError.message : '';
      optionsError.textContent = errorMessage === 'HTTP 404'
        ? '動画設定を読み込めませんでした (HTTP 404)。Movieは設定されたLAN内のIPv4アドレスから利用できます。ViewerのURLとCOREのLAN接続設定を確認してください。'
        : errorMessage
          ? `動画設定を読み込めませんでした: ${errorMessage}`
          : '動画設定を読み込めませんでした。';
      optionsError.hidden = false;
      optionsRetry.hidden = false;
      setBusy(false);
      return null;
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

  async function sha256HexFallback(value) {
    const bytes = value instanceof ArrayBuffer
      ? new Uint8Array(value)
      : new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
    const byteLength = bytes.byteLength;
    const blockCount = Math.ceil((byteLength + 9) / 64);
    const finalLengthOffset = blockCount * 64 - 8;
    const bitLengthHigh = Math.floor(byteLength / 0x20000000) >>> 0;
    const bitLengthLow = (byteLength * 8) >>> 0;
    const schedule = new Uint32Array(64);
    const hash = [
      0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
      0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
    ];
    const constants = [
      0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
      0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
      0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
      0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
      0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
      0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
      0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
      0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
    ];
    const rotateRight = (word, count) => (word >>> count) | (word << (32 - count));
    const paddedByteAt = (index) => {
      if (index < byteLength) return bytes[index];
      if (index === byteLength) return 0x80;
      if (index < finalLengthOffset) return 0;
      const lengthIndex = index - finalLengthOffset;
      if (lengthIndex < 4) return (bitLengthHigh >>> ((3 - lengthIndex) * 8)) & 0xff;
      return (bitLengthLow >>> ((7 - lengthIndex) * 8)) & 0xff;
    };

    for (let block = 0; block < blockCount; block += 1) {
      const offset = block * 64;
      for (let word = 0; word < 16; word += 1) {
        const byteOffset = offset + word * 4;
        schedule[word] = (
          (paddedByteAt(byteOffset) << 24) |
          (paddedByteAt(byteOffset + 1) << 16) |
          (paddedByteAt(byteOffset + 2) << 8) |
          paddedByteAt(byteOffset + 3)
        ) >>> 0;
      }
      for (let word = 16; word < 64; word += 1) {
        const previous15 = schedule[word - 15];
        const previous2 = schedule[word - 2];
        const smallSigma0 = rotateRight(previous15, 7) ^ rotateRight(previous15, 18) ^ (previous15 >>> 3);
        const smallSigma1 = rotateRight(previous2, 17) ^ rotateRight(previous2, 19) ^ (previous2 >>> 10);
        schedule[word] = (smallSigma1 + schedule[word - 7] + smallSigma0 + schedule[word - 16]) >>> 0;
      }

      let [a, b, c, d, e, f, g, h] = hash;
      for (let round = 0; round < 64; round += 1) {
        const bigSigma1 = rotateRight(e, 6) ^ rotateRight(e, 11) ^ rotateRight(e, 25);
        const choice = (e & f) ^ (~e & g);
        const temp1 = (h + bigSigma1 + choice + constants[round] + schedule[round]) >>> 0;
        const bigSigma0 = rotateRight(a, 2) ^ rotateRight(a, 13) ^ rotateRight(a, 22);
        const majority = (a & b) ^ (a & c) ^ (b & c);
        const temp2 = (bigSigma0 + majority) >>> 0;
        h = g;
        g = f;
        f = e;
        e = (d + temp1) >>> 0;
        d = c;
        c = b;
        b = a;
        a = (temp1 + temp2) >>> 0;
      }
      hash[0] = (hash[0] + a) >>> 0;
      hash[1] = (hash[1] + b) >>> 0;
      hash[2] = (hash[2] + c) >>> 0;
      hash[3] = (hash[3] + d) >>> 0;
      hash[4] = (hash[4] + e) >>> 0;
      hash[5] = (hash[5] + f) >>> 0;
      hash[6] = (hash[6] + g) >>> 0;
      hash[7] = (hash[7] + h) >>> 0;

      if ((block + 1) % 16384 === 0 && block + 1 < blockCount) {
        await new Promise((resolve) => window.setTimeout(resolve, 0));
      }
    }
    return hash.map((word) => word.toString(16).padStart(8, '0')).join('');
  }

  async function sha256Hex(value) {
    if (window.crypto && window.crypto.subtle && window.crypto.subtle.digest) {
      const digest = await window.crypto.subtle.digest('SHA-256', value);
      return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, '0')).join('');
    }
    return sha256HexFallback(value);
  }

  async function version2VideoRequestFingerprint(videoMode, promptText, seedValue, file) {
    const imageSha256 = file ? await sha256Hex(await file.arrayBuffer()) : '';
    const normalizedRequest = JSON.stringify({
      version: 2,
      mode: videoMode,
      prompt: promptText,
      seed: seedValue,
      aspect_ratio: aspectRatio.value,
      image_sha256: imageSha256,
    });
    return sha256Hex(new TextEncoder().encode(normalizedRequest));
  }

  async function videoRequestFingerprint(taskValue, videoMode, promptText, seedValue, imageFile, audioFile) {
    const imageSha256 = imageFile ? await sha256Hex(await imageFile.arrayBuffer()) : '';
    const audioSha256 = audioFile ? await sha256Hex(await audioFile.arrayBuffer()) : '';
    const normalizedRequest = JSON.stringify({
      version: 3,
      task: taskValue,
      mode: videoMode,
      prompt: promptText,
      seed: seedValue,
      aspect_ratio: aspectRatio.value,
      image_sha256: imageSha256,
      audio_sha256: audioSha256,
    });
    return sha256Hex(new TextEncoder().encode(normalizedRequest));
  }

  async function v2vRequestFingerprint(taskValue, videoMode, videoFile, audioFile) {
    const videoSha256 = await sha256Hex(await videoFile.arrayBuffer());
    const audioSha256 = await sha256Hex(await audioFile.arrayBuffer());
    const normalizedRequest = JSON.stringify({
      version: 4,
      task: taskValue,
      mode: videoMode,
      video_sha256: videoSha256,
      audio_sha256: audioSha256,
    });
    return sha256Hex(new TextEncoder().encode(normalizedRequest));
  }

  async function legacyVideoRequestFingerprint(videoMode, promptText, seedValue, file) {
    const imageSha256 = file ? await sha256Hex(await file.arrayBuffer()) : '';
    const normalizedRequest = JSON.stringify({
      version: 1,
      mode: videoMode,
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
      const validLegacyRequest = pending && pending.version === 1 && !Object.prototype.hasOwnProperty.call(pending, 'aspect_ratio');
      const validVersion2Request = pending && pending.version === 2 && validAspectRatio(pending.aspect_ratio);
      const validCurrentRequest = pending && pending.version === 3 && validAspectRatio(pending.aspect_ratio) &&
        ['h3', 'lip_sync'].includes(pending.task) && ['t2v', 'i2v'].includes(pending.mode);
      const validV2VRequest = pending && pending.version === 4 && pending.task === 'lip_sync' && pending.mode === 'v2v';
      if ((!validLegacyRequest && !validVersion2Request && !validCurrentRequest && !validV2VRequest) ||
          !/^[0-9a-f]{32}$/.test(pending.key || '') ||
          !/^[0-9a-f]{64}$/.test(pending.fingerprint || '') ||
          (pending.version !== 4 && (!['t2v', 'i2v'].includes(pending.mode) ||
            !Number.isSafeInteger(pending.seed) || pending.seed < 0 || pending.seed > 9007199254740991))) {
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
        currentVideoID = '';
        clearSavedVideoJob(id);
        resetVideoPlayer();
        videoStatus.textContent = 'この動画ジョブは見つかりません。';
        setStatus('not found', false);
        revealVideoResult();
        return;
      }
      if (response.status === 401 || response.status === 403) {
        currentVideoID = '';
        videoProgress.hidden = true;
        videoStatus.textContent = payload.message || `動画状態を取得できません。HTTP ${response.status}`;
        showError(videoStatus.textContent);
        revealVideoResult();
        return;
      }
      if (!response.ok || payload.id !== id || !['queued', 'running', 'succeeded', 'failed'].includes(payload.status)) {
        throw new Error(payload.message || `動画状態を取得できません。HTTP ${response.status}`);
      }
      error.hidden = true;
      revealVideoResult();
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
          showError('生成したMP4を再生できませんでした。');
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
      revealVideoResult();
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
    resetVideoPlayer();
    videoStatus.textContent = '保存した動画ジョブを確認しています…';
    revealVideoResult();
    pollVideoJob(id);
  }

  async function resumePendingVideoRequest() {
    const pending = loadPendingVideoRequest();
    if (!pending) return false;
    const pendingTask = pending.version >= 3 ? pending.task : 'h3';
    for (const input of taskInputs) input.checked = input.value === pendingTask;
    mode.value = pending.mode;
    if (pending.version !== 4) seed.value = String(pending.seed);
    updateVideoModeInputs();
    const hasAspectRatio = pending.version === 2 || pending.version === 3;
    const options = videoOptions && videoOptions.mode === pending.mode && videoOptions.task === pendingTask
      ? videoOptions
      : await loadVideoOptions(pending.mode, hasAspectRatio ? pending.aspect_ratio : '', pendingTask);
    if (hasAspectRatio && options) {
      if (!Array.from(aspectRatio.options).some((option) => option.value === pending.aspect_ratio)) {
        const previous = document.createElement('option');
        previous.value = pending.aspect_ratio;
        previous.textContent = `${pending.aspect_ratio} (previous request)`;
        aspectRatio.append(previous);
      }
      aspectRatio.value = pending.aspect_ratio;
    }
    resetVideoPlayer();
    videoStatus.textContent = pending.version === 4
      ? '前回の送信結果が不明です。同じMP4動画とWAV音声を選び直して安全に再試行できます。'
      : pendingTask === 'lip_sync'
      ? pending.mode === 'i2v'
        ? '前回の送信結果が不明です。同じプロンプト、縦横比、Seed、開始画像、音声を選び直して安全に再試行できます。'
        : '前回の送信結果が不明です。同じプロンプト、縦横比、Seed、音声を選び直して安全に再試行できます。'
      : pending.mode === 'i2v'
        ? '前回の送信結果が不明です。同じプロンプト、縦横比、Seedを入力し、同じ開始画像を選び直して安全に再試行できます。'
        : '前回の送信結果が不明です。同じプロンプト、縦横比、Seedで再試行できます。';
    revealVideoResult();
    setStatus('submission uncertain', true);
    return true;
  }

  async function resumeSavedVideoState() {
    if (!await resumePendingVideoRequest()) resumeSavedVideoJob();
    return true;
  }

  function loadVideoOptionsAndResume(requestedMode = mode.value, preferredAspect = '', requestedTask = selectedVideoTask()) {
    return loadVideoOptions(requestedMode, preferredAspect, requestedTask).then((options) => {
      if (options) resumeSavedVideoState();
      return options;
    });
  }

  function startVideoGeneration() {
    if (videoSubmitBusy) return;
    videoSubmitBusy = true;
    generateVideo()
      .catch(() => showError('動画生成を開始できませんでした。'))
      .finally(() => { videoSubmitBusy = false; });
  }

  async function generateVideo() {
    const videoMode = mode.value;
    const taskValue = selectedVideoTask();
    const isV2V = videoMode === 'v2v';
    if (!['t2v', 'i2v', 'v2v'].includes(videoMode) || (isV2V && taskValue !== 'lip_sync')) {
      showError('動画モードを選び直してください。');
      mode.focus();
      return;
    }
    if (!['h3', 'lip_sync'].includes(taskValue)) {
      showError('動画生成の種類を選び直してください。');
      return;
    }
    const promptText = isV2V ? '' : prompt.value.trim();
    if (!isV2V && !promptText) {
      showError('プロンプトを入力してください。');
      prompt.focus();
      return;
    }
    const seedValue = isV2V ? 0 : Number(seed.value);
    if (!isV2V && (seed.value.trim() === '' || !Number.isSafeInteger(seedValue) || seedValue < 0 || seedValue > 9007199254740991)) {
      showError('動画Seedは0以上の整数で入力してください。');
      seed.focus();
      return;
    }
    const aspectRatioValue = isV2V ? '' : aspectRatio.value;
    const requestTask = taskValue === 'lip_sync' ? 'lip_sync' : '';
    if (!videoOptionsReady || !videoOptions || videoOptions.mode !== videoMode || videoOptions.task !== taskValue ||
        (!isV2V && !validAspectRatio(aspectRatioValue))) {
      showError('選択した動画モードの設定を読み込んでください。');
      return;
    }
    const file = videoMode === 'i2v' ? inputImage.files[0] : null;
    if (videoMode === 'i2v' && !file) {
      showError('Image to videoでは開始画像を選択してください。');
      inputImage.focus();
      return;
    }
    const videoFile = isV2V ? inputVideo.files[0] : null;
    if (isV2V && !videoFile) {
      showError('Video to videoではMP4動画を選択してください。');
      inputVideo.focus();
      return;
    }
    if (videoFile && (videoFile.size === 0 || videoFile.size > videoOptions.maxVideoBytes ||
        (!['video/mp4', 'application/mp4'].includes(videoFile.type) && !/\.mp4$/i.test(videoFile.name)))) {
      showError(`動画は${formatVideoByteLimit(videoOptions.maxVideoBytes)}以下のMP4を選択してください。`);
      inputVideo.focus();
      return;
    }
    if (file && (file.size > maxVideoImageBytes || !['image/png', 'image/jpeg'].includes(file.type) && !/\.(png|jpe?g)$/i.test(file.name))) {
      showError('開始画像は20 MiB以下のPNGまたはJPEGを選択してください。');
      inputImage.focus();
      return;
    }
    const audioFile = requestTask ? inputAudio.files[0] : null;
    const wavType = ['audio/wav', 'audio/x-wav', 'audio/wave', 'audio/vnd.wave'].includes(audioFile && audioFile.type);
    if (requestTask && !audioFile) {
      showError('音声同期ではWAV PCM音声を選択してください。');
      inputAudio.focus();
      return;
    }
    const maxAudioBytes = isV2V ? videoOptions.maxAudioBytes : maxVideoAudioBytes;
    if (audioFile && (audioFile.size === 0 || audioFile.size > maxAudioBytes ||
        (!wavType && !/\.wav$/i.test(audioFile.name)))) {
      showError(`音声は${formatVideoByteLimit(maxAudioBytes)}以下のWAVファイルを選択してください。`);
      inputAudio.focus();
      return;
    }

    error.hidden = true;
    setBusy(true, 'Preparing...');
    videoStatus.textContent = '動画リクエストを準備しています…';
    revealVideoResult();
    resetVideoPlayer();
    let requestSubmitted = false;
    try {
      const fingerprint = isV2V
        ? await v2vRequestFingerprint(taskValue, videoMode, videoFile, audioFile)
        : await videoRequestFingerprint(taskValue, videoMode, promptText, seedValue, file, audioFile);
      const previous = loadPendingVideoRequest();
      const version2Fingerprint = !isV2V && previous && previous.version === 2 && taskValue === 'h3'
        ? await version2VideoRequestFingerprint(videoMode, promptText, seedValue, file)
        : '';
      const legacyFingerprint = !isV2V && previous && previous.version === 1 && taskValue === 'h3'
        ? await legacyVideoRequestFingerprint(videoMode, promptText, seedValue, file)
        : '';
      const matchesCurrentRequest = !isV2V && previous && previous.version === 3 && previous.fingerprint === fingerprint &&
        previous.task === taskValue && previous.mode === videoMode && previous.aspect_ratio === aspectRatioValue;
      const matchesV2VRequest = isV2V && previous && previous.version === 4 && previous.fingerprint === fingerprint &&
        previous.task === taskValue && previous.mode === videoMode;
      const matchesVersion2Request = previous && previous.version === 2 && previous.fingerprint === version2Fingerprint &&
        previous.mode === videoMode && previous.aspect_ratio === aspectRatioValue;
      const matchesLegacyRequest = previous && previous.version === 1 && previous.fingerprint === legacyFingerprint &&
        previous.mode === videoMode && aspectRatioValue === videoOptions.defaultAspectRatio;
      const matchesExistingRequest = matchesCurrentRequest || matchesV2VRequest || matchesVersion2Request || matchesLegacyRequest;
      if (!isV2V && !videoOptions.aspectRatios.includes(aspectRatioValue) && !matchesExistingRequest) {
        throw new Error('選択した縦横比は現在の動画設定で利用できません。');
      }
      const pending = matchesExistingRequest ? previous : isV2V
        ? {version: 4, key: makeIdempotencyKey(), fingerprint, task: taskValue, mode: videoMode}
        : {version: 3, key: makeIdempotencyKey(), fingerprint, task: taskValue, mode: videoMode, seed: seedValue, aspect_ratio: aspectRatioValue};
      if (pending !== previous) {
        // Persist before POST so a lost 202 can be retried with the same key.
        savePendingVideoRequest(pending);
      }

      const request = new FormData();
      request.append('mode', videoMode);
      if (requestTask) request.append('task', requestTask);
      if (isV2V) {
        request.append('video', videoFile, videoFile.name);
      } else {
        request.append('prompt', promptText);
        request.append('seed', String(seedValue));
        if (pending.version !== 1) request.append('aspect_ratio', aspectRatioValue);
        if (file) request.append('image', file, file.name);
      }
      if (audioFile) request.append('audio', audioFile, audioFile.name);

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
        body: request,
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

  form.addEventListener('submit', (event) => {
    event.preventDefault();
    startVideoGeneration();
  });
  prompt.addEventListener('keydown', function onPromptKeydown(event) {
    if ((event.ctrlKey || event.metaKey) && event.key === 'Enter') {
      event.preventDefault();
      form.requestSubmit();
    }
  });
  mode.addEventListener('change', updateVideoModeInputs);
  mode.addEventListener('change', () => loadVideoOptions(mode.value, '', selectedVideoTask()));
  for (const input of taskInputs) {
    input.addEventListener('change', () => {
      updateVideoModeInputs();
      loadVideoOptions(mode.value, '', selectedVideoTask());
    });
  }
  optionsRetry.addEventListener('click', () => {
    const pending = loadPendingVideoRequest();
    const preferredAspect = pending && (pending.version === 2 || pending.version === 3) && pending.mode === mode.value &&
      (pending.version !== 3 || pending.task === selectedVideoTask())
      ? pending.aspect_ratio
      : '';
    loadVideoOptionsAndResume(mode.value, preferredAspect, selectedVideoTask());
  });

  updateVideoModeInputs();
  loadVideoOptionsAndResume(mode.value);
})();
