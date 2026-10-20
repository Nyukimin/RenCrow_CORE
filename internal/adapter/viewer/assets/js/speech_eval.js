(function () {
  'use strict';

  const MANIFEST_URL = '/viewer/tts/audio?path=speech-eval/manifest.json';
  const DIAGNOSTIC_MANIFEST_URL = '/viewer/tts/audio?path=speech-eval/diagnostic.json';
  const AUDIO_PATH = '/viewer/tts/audio';
  const MANIFEST_SCHEMA = 'rencrow.speech-eval/1';
  const DIAGNOSTIC_SCHEMA = 'rencrow.speech-diagnostic/1';
  const RESULTS_SCHEMA = 'rencrow.speech-eval-results/1';
  const STORAGE_PREFIX = 'rencrow.speech-eval.v1:';
  const CASE_COUNT = 20;
  const RATING_FIELDS = [
    { key: 'intent_fit', label: '意図への適合' },
    { key: 'lack_of_overacting', label: '過剰な演技の少なさ' },
    { key: 'mio_voice_preservation', label: 'Mioらしい声の維持' },
    { key: 'cue_position_fit', label: 'キューの位置と自然さ', allowNA: true },
    { key: 'overall_adoption', label: '全体としての採用意向' },
  ];
  const DIAGNOSTIC_GROUP_IDS = ['neutral', 'expressive'];
  const DIAGNOSTIC_CLIP_IDS = ['original', 'gain-6db', 'legacy'];

  function isRecord(value) {
    return value !== null && typeof value === 'object' && !Array.isArray(value);
  }

  function validateAudioURL(rawURL, origin, setID) {
    if (typeof rawURL !== 'string' || rawURL.trim() === '') {
      throw new Error('音声URLが指定されていないか、形式が正しくありません。');
    }

    let base;
    let parsed;
    try {
      base = new URL(origin);
      parsed = new URL(rawURL, base);
    } catch {
      throw new Error('音声URLの形式が正しくありません。');
    }

    if (
      parsed.origin !== base.origin ||
      parsed.pathname !== AUDIO_PATH ||
      parsed.hash !== '' ||
      parsed.username !== '' ||
      parsed.password !== ''
    ) {
      throw new Error('音声URLにはこのViewerの音声ルートを指定してください。');
    }

    const params = Array.from(parsed.searchParams.entries());
    if (params.length !== 1 || params[0][0] !== 'path') {
      throw new Error('音声URLにはローカルのpathだけを指定してください。');
    }

    const path = params[0][1];
    const segments = path.split('/');
    if (
      segments.length !== 3 ||
      segments[0] !== 'speech-eval' ||
      segments[1] !== setID ||
      !segments[2].endsWith('.wav') ||
      segments[2].length <= '.wav'.length ||
      /%(?:2e|2f|5c)/i.test(path) ||
      path.includes('\\') ||
      /[\u0000-\u001f\u007f]/.test(path) ||
      segments.some((segment) => segment === '' || segment === '.' || segment === '..')
    ) {
      throw new Error('音声URLはこの評価セット内のWAVファイルを指定してください。');
    }

    return `${parsed.pathname}${parsed.search}`;
  }

  function validateManifest(manifest, origin) {
    if (!isRecord(manifest) || manifest.schema_version !== MANIFEST_SCHEMA) {
      throw new Error(`評価セットのschema_versionは${MANIFEST_SCHEMA}である必要があります。`);
    }
    if (typeof manifest.set_id !== 'string' || manifest.set_id.trim() === '' || manifest.set_id.length > 200) {
      throw new Error('評価セットのset_idは200文字以内で指定してください。');
    }
    if (
      manifest.set_id === '.' || manifest.set_id === '..' ||
      manifest.set_id.includes('/') || manifest.set_id.includes('\\') ||
      /[\u0000-\u001f\u007f]/.test(manifest.set_id)
    ) {
      throw new Error('評価セットのset_idは安全な単一のpath要素で指定してください。');
    }
    if (!Array.isArray(manifest.items) || manifest.items.length !== CASE_COUNT) {
      throw new Error(`評価セットにはケースを${CASE_COUNT}件指定してください。`);
    }

    const seenIDs = new Set();
    const items = manifest.items.map((item, index) => {
      if (!isRecord(item)) throw new Error(`ケース${index + 1}の形式が正しくありません。`);
      if (typeof item.id !== 'string' || item.id.trim() === '' || item.id.length > 100) {
        throw new Error(`ケース${index + 1}のidが正しくありません。`);
      }
      if (seenIDs.has(item.id)) throw new Error(`ケースid「${item.id}」が重複しています。`);
      seenIDs.add(item.id);
      if (typeof item.text !== 'string' || typeof item.intent !== 'string') {
        throw new Error(`ケース「${item.id}」にはセリフと意図を指定してください。`);
      }
      if (typeof item.cue_applicable !== 'boolean') {
        throw new Error(`ケース「${item.id}」にはcue_applicableを指定してください。`);
      }
      if (
        typeof item.cue_hint !== 'string' ||
        (item.cue_applicable && item.cue_hint.trim() === '') ||
        (!item.cue_applicable && item.cue_hint !== '')
      ) {
        throw new Error(`ケース「${item.id}」のcue_hintとcue_applicableが一致しません。`);
      }

      return {
        id: item.id,
        text: item.text,
        intent: item.intent,
        cue_applicable: item.cue_applicable,
        cue_hint: item.cue_hint,
        a_url: validateAudioURL(item.a_url, origin, manifest.set_id),
        b_url: validateAudioURL(item.b_url, origin, manifest.set_id),
      };
    });

    return { schema_version: MANIFEST_SCHEMA, set_id: manifest.set_id, items };
  }

  function validateDiagnosticAudioURL(rawURL, origin, setID) {
    const validatedURL = validateAudioURL(rawURL, origin, setID);
    const path = new URL(validatedURL, origin).searchParams.get('path');
    const filename = path.slice(`speech-eval/${setID}/`.length);
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]*\.wav$/i.test(filename)) {
      throw new Error('診断音声ファイル名に安全でない文字が含まれています。');
    }

    return validatedURL;
  }

  function validateDiagnosticManifest(manifest, origin) {
    if (!isRecord(manifest) || manifest.schema_version !== DIAGNOSTIC_SCHEMA) {
      throw new Error(`診断セットのschema_versionは${DIAGNOSTIC_SCHEMA}である必要があります。`);
    }
    if (
      typeof manifest.set_id !== 'string' ||
      !/^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$/.test(manifest.set_id)
    ) {
      throw new Error('診断セットのset_idは安全なpath要素で指定してください。');
    }
    if (typeof manifest.text !== 'string' || manifest.text.trim() === '' || manifest.text.length > 2000) {
      throw new Error('診断セットには2000文字以内の比較文を指定してください。');
    }
    if (!Array.isArray(manifest.groups) || manifest.groups.length !== DIAGNOSTIC_GROUP_IDS.length) {
      throw new Error('診断セットにはneutralとexpressiveの2グループを指定してください。');
    }

    const groups = manifest.groups.map((group, groupIndex) => {
      const expectedGroupID = DIAGNOSTIC_GROUP_IDS[groupIndex];
      if (!isRecord(group) || group.id !== expectedGroupID) {
        throw new Error(`診断グループ${groupIndex + 1}は${expectedGroupID}である必要があります。`);
      }
      if (typeof group.label !== 'string' || group.label.trim() === '' || group.label.length > 80) {
        throw new Error(`診断グループ${expectedGroupID}のlabelが正しくありません。`);
      }
      if (!Array.isArray(group.clips) || group.clips.length !== DIAGNOSTIC_CLIP_IDS.length) {
        throw new Error(`診断グループ${expectedGroupID}には音声を3件指定してください。`);
      }

      const clips = group.clips.map((clip, clipIndex) => {
        const expectedClipID = DIAGNOSTIC_CLIP_IDS[clipIndex];
        if (!isRecord(clip) || clip.id !== expectedClipID) {
          throw new Error(`診断グループ${expectedGroupID}の音声${clipIndex + 1}は${expectedClipID}である必要があります。`);
        }
        if (typeof clip.label !== 'string' || clip.label.trim() === '' || clip.label.length > 120) {
          throw new Error(`診断音声${expectedClipID}のlabelが正しくありません。`);
        }
        return {
          id: expectedClipID,
          label: clip.label,
          url: validateDiagnosticAudioURL(clip.url, origin, manifest.set_id),
        };
      });

      return { id: expectedGroupID, label: group.label, clips };
    });

    return { schema_version: DIAGNOSTIC_SCHEMA, set_id: manifest.set_id, text: manifest.text, groups };
  }

  function isValidRating(value, field) {
    if (Number.isInteger(value) && value >= 1 && value <= 5) return true;
    return field.key === 'cue_position_fit' && value === 'N/A';
  }

  function emptyCaseRatings() {
    return { a: Object.create(null), b: Object.create(null), preference: null };
  }

  function normalizeSavedState(rawState, manifest) {
    const normalized = { set_id: manifest.set_id, cases: Object.create(null) };
    if (!isRecord(rawState) || rawState.set_id !== manifest.set_id || !isRecord(rawState.cases)) {
      return normalized;
    }

    for (const item of manifest.items) {
      const saved = rawState.cases[item.id];
      if (!isRecord(saved)) continue;
      const entry = emptyCaseRatings();
      for (const clip of ['a', 'b']) {
        const clipValues = saved[clip];
        if (!isRecord(clipValues)) continue;
        for (const field of RATING_FIELDS) {
          const value = clipValues[field.key];
          if (isValidRating(value, field)) entry[clip][field.key] = value;
        }
      }
      if (saved.preference === 'a' || saved.preference === 'tie' || saved.preference === 'b') {
        entry.preference = saved.preference;
      }
      normalized.cases[item.id] = entry;
    }
    return normalized;
  }

  function caseIsComplete(item, entry) {
    if (!entry || !entry.a || !entry.b || !entry.preference) return false;
    const allRatingsValid = (clip) => RATING_FIELDS.every((field) => isValidRating(clip[field.key], field));
    return allRatingsValid(entry.a) && allRatingsValid(entry.b);
  }

  function exportCases(manifest, state) {
    return manifest.items.map((item) => {
      const saved = state.cases[item.id] || emptyCaseRatings();
      const ratings = {};
      for (const clip of ['a', 'b']) {
        ratings[clip] = {};
        for (const field of RATING_FIELDS) ratings[clip][field.key] = saved[clip][field.key] ?? null;
      }
      return {
        case_id: item.id,
        text: item.text,
        intent: item.intent,
        cue_hint: item.cue_hint,
        cue_applicable: item.cue_applicable,
        ratings,
        preference: saved.preference,
      };
    });
  }

  function csvCell(value) {
    const text = value === null || value === undefined ? '' : String(value);
    return `"${text.replaceAll('"', '""')}"`;
  }

  function safeFilePart(value) {
    const safe = String(value).replace(/[^A-Za-z0-9._-]+/g, '_').replace(/^\.+|\.+$/g, '').slice(0, 80);
    return safe || 'set';
  }

  function initialize(browserWindow) {
    const document = browserWindow && browserWindow.document;
    if (!document) return Promise.resolve(null);

    const ui = {
      status: document.getElementById('speechEvalStatus'),
      progress: document.getElementById('speechEvalProgress'),
      caseNumber: document.getElementById('speechEvalCaseNumber'),
      text: document.getElementById('speechEvalText'),
      intent: document.getElementById('speechEvalIntent'),
      cueHint: document.getElementById('speechEvalCueHint'),
      pair: document.getElementById('speechEvalPair'),
      preference: document.getElementById('speechEvalPreference'),
      previous: document.getElementById('speechEvalPrevious'),
      next: document.getElementById('speechEvalNext'),
      exportJSON: document.getElementById('speechEvalExportJson'),
      exportCSV: document.getElementById('speechEvalExportCsv'),
      diagnosticPanel: document.getElementById('speechEvalDiagnosticPanel'),
      diagnosticText: document.getElementById('speechEvalDiagnosticText'),
      diagnosticGroups: document.getElementById('speechEvalDiagnosticGroups'),
    };
    if (Object.values(ui).some((element) => !element)) return Promise.resolve(null);

    let manifest = null;
    let state = null;
    let currentIndex = 0;
    let activeAudios = [];
    let storageUnavailable = false;

    function setStatus(message, kind = 'info') {
      ui.status.textContent = message;
      ui.status.dataset.kind = kind;
    }

    function saveState() {
      if (!manifest || !state) return;
      try {
        browserWindow.localStorage.setItem(`${STORAGE_PREFIX}${manifest.set_id}`, JSON.stringify(state));
      } catch {
        storageUnavailable = true;
        setStatus('評価は続けられますが、ブラウザー保存領域を利用できません。再読み込み後に記録が残らない可能性があります。', 'error');
      }
    }

    function getCaseRatings(caseID) {
      if (!Object.hasOwn(state.cases, caseID)) state.cases[caseID] = emptyCaseRatings();
      return state.cases[caseID];
    }

    function updateProgress() {
      const completeCount = manifest.items.reduce((count, item) => {
        return count + (caseIsComplete(item, state.cases[item.id]) ? 1 : 0);
      }, 0);
      ui.progress.textContent = `${completeCount} / ${CASE_COUNT} 件評価済み`;
    }

    function stopCurrentAudio() {
      for (const audio of activeAudios) {
        pauseAudio(audio);
      }
      activeAudios = Array.from(ui.diagnosticGroups.querySelectorAll('audio'));
    }

    function pauseAudio(audio) {
      try {
        audio.pause();
        audio.currentTime = 0;
      } catch {
        // The browser can reject seeking while a media resource is changing.
      }
    }

    function registerAudio(audio) {
      audio.addEventListener('play', () => {
        for (const other of activeAudios) {
          if (other !== audio) pauseAudio(other);
        }
      });
      activeAudios.push(audio);
    }

    function addOption(select, value, label) {
      const option = document.createElement('option');
      option.value = value;
      option.textContent = label;
      select.appendChild(option);
    }

    function ratingSelectValue(value) {
      if (value === 'N/A') return 'na';
      return Number.isInteger(value) ? String(value) : '';
    }

    function renderRating(item, clip, field, savedRatings) {
      const row = document.createElement('label');
      row.className = 'speech-eval__rating';
      const label = document.createElement('span');
      label.className = 'speech-eval__rating-label';
      label.textContent = field.label;

      const select = document.createElement('select');
      select.dataset.clip = clip;
      select.dataset.rating = field.key;
      addOption(select, '', '未評価');
      addOption(select, '1', '1 — よくない');
      addOption(select, '2', '2');
      addOption(select, '3', '3');
      addOption(select, '4', '4');
      addOption(select, '5', '5 — とてもよい');
      if (field.allowNA) addOption(select, 'na', '該当なし（N/A）');

      let storedValue = savedRatings[field.key];
      if (field.key === 'cue_position_fit' && !item.cue_applicable && storedValue === undefined) {
        storedValue = 'N/A';
        savedRatings[field.key] = storedValue;
        saveState();
      }
      select.value = ratingSelectValue(storedValue);
      select.addEventListener('change', () => {
        const value = select.value === 'na' ? 'N/A' : select.value;
        setRating(item.id, clip, field.key, value);
      });

      row.appendChild(label);
      row.appendChild(select);
      if (field.key === 'cue_position_fit') {
        const note = document.createElement('span');
        note.className = 'speech-eval__cue-note';
        note.textContent = item.cue_applicable
          ? '表示されたキューの位置が自然か、読み上げに反映されているかを評価してください。'
          : 'このケースにキューはありません。N/Aは未評価ではなく「評価対象外」を示します。';
        row.appendChild(note);
      }
      return row;
    }

    function renderClip(item, clip, url, savedRatings) {
      const section = document.createElement('section');
      section.className = 'speech-eval__clip';
      const heading = document.createElement('h3');
      heading.className = 'speech-eval__clip-title';
      heading.textContent = clip === 'a' ? 'A' : 'B';

      const audio = document.createElement('audio');
      audio.className = 'speech-eval__audio';
      audio.controls = true;
      audio.preload = 'none';
      audio.src = url;
      audio.setAttribute('aria-label', `音声${clip === 'a' ? 'A' : 'B'}`);
      registerAudio(audio);

      const ratings = document.createElement('div');
      ratings.className = 'speech-eval__ratings';
      for (const field of RATING_FIELDS) ratings.appendChild(renderRating(item, clip, field, savedRatings));

      section.appendChild(heading);
      section.appendChild(audio);
      section.appendChild(ratings);
      return section;
    }

    function renderDiagnosticClip(group, clip) {
      const card = document.createElement('article');
      card.className = 'speech-eval__diagnostic-clip';

      const label = document.createElement('h4');
      label.className = 'speech-eval__diagnostic-clip-label';
      label.textContent = clip.label;

      const audio = document.createElement('audio');
      audio.className = 'speech-eval__diagnostic-audio';
      audio.controls = true;
      audio.preload = 'none';
      audio.src = clip.url;
      audio.setAttribute('aria-label', `${group.label} · ${clip.label}`);
      registerAudio(audio);

      card.appendChild(label);
      card.appendChild(audio);
      return card;
    }

    function renderDiagnostic(manifest) {
      ui.diagnosticText.textContent = manifest.text;
      const groups = manifest.groups.map((group) => {
        const section = document.createElement('section');
        section.className = 'speech-eval__diagnostic-group';

        const heading = document.createElement('h3');
        heading.className = 'speech-eval__diagnostic-group-title';
        heading.textContent = group.label;

        const clips = document.createElement('div');
        clips.className = 'speech-eval__diagnostic-clips';
        for (const clip of group.clips) clips.appendChild(renderDiagnosticClip(group, clip));

        section.appendChild(heading);
        section.appendChild(clips);
        return section;
      });
      ui.diagnosticGroups.replaceChildren(...groups);
      ui.diagnosticPanel.hidden = false;
    }

    function loadDiagnosticManifest() {
      ui.diagnosticPanel.hidden = true;
      ui.diagnosticText.textContent = '';
      ui.diagnosticGroups.replaceChildren();
      return Promise.resolve()
        .then(() => browserWindow.fetch(DIAGNOSTIC_MANIFEST_URL, { cache: 'no-store', credentials: 'omit' }))
        .then((response) => {
          if (!response || !response.ok) return null;
          return response.json();
        })
        .then((rawManifest) => {
          if (!rawManifest) return;
          const diagnosticManifest = validateDiagnosticManifest(rawManifest, browserWindow.location.origin);
          renderDiagnostic(diagnosticManifest);
        })
        .catch(() => {
          ui.diagnosticPanel.hidden = true;
          ui.diagnosticText.textContent = '';
          ui.diagnosticGroups.replaceChildren();
        });
    }

    function renderCurrentCase() {
      stopCurrentAudio();
      const item = manifest.items[currentIndex];
      const saved = getCaseRatings(item.id);
      ui.caseNumber.textContent = `${String(currentIndex + 1).padStart(2, '0')} · ${item.id}`;
      ui.text.textContent = item.text;
      ui.intent.textContent = item.intent;
      ui.cueHint.textContent = item.cue_hint;
      ui.preference.value = saved.preference || '';
      ui.preference.disabled = false;
      ui.previous.disabled = currentIndex === 0;
      ui.next.disabled = currentIndex === manifest.items.length - 1;
      ui.pair.replaceChildren(
        renderClip(item, 'a', item.a_url, saved.a),
        renderClip(item, 'b', item.b_url, saved.b),
      );
      updateProgress();
    }

    function setRating(caseID, clip, fieldKey, value) {
      if (!manifest || !['a', 'b'].includes(clip)) return false;
      const field = RATING_FIELDS.find((candidate) => candidate.key === fieldKey);
      const item = manifest.items.find((candidate) => candidate.id === caseID);
      if (!field || !item) return false;

      if (value === '') {
        delete getCaseRatings(caseID)[clip][fieldKey];
        saveState();
        updateProgress();
        return true;
      }

      let normalizedValue = value;
      if (value !== 'N/A') {
        normalizedValue = typeof value === 'number' ? value : Number(value);
      }
      if (!isValidRating(normalizedValue, field)) return false;
      getCaseRatings(caseID)[clip][fieldKey] = normalizedValue;
      saveState();
      updateProgress();
      return true;
    }

    function setPreference(caseID, preference) {
      if (!manifest || !manifest.items.some((item) => item.id === caseID)) return false;
      if (preference !== '' && !['a', 'tie', 'b'].includes(preference)) return false;
      getCaseRatings(caseID).preference = preference || null;
      if (manifest.items[currentIndex].id === caseID) ui.preference.value = preference;
      saveState();
      updateProgress();
      return true;
    }

    function navigate(index) {
      if (!manifest || !Number.isInteger(index)) return currentIndex;
      const nextIndex = Math.max(0, Math.min(manifest.items.length - 1, index));
      if (nextIndex !== currentIndex) {
        currentIndex = nextIndex;
        renderCurrentCase();
      }
      return currentIndex;
    }

    function exportData() {
      return {
        schema_version: RESULTS_SCHEMA,
        set_id: manifest.set_id,
        cases: exportCases(manifest, state),
      };
    }

    function download(filename, content, type) {
      if (!browserWindow.Blob || !browserWindow.URL || typeof browserWindow.URL.createObjectURL !== 'function') {
        setStatus('このブラウザーではファイルを書き出せません。', 'error');
        return false;
      }
      const blob = new browserWindow.Blob([content], { type });
      const objectURL = browserWindow.URL.createObjectURL(blob);
      const link = document.createElement('a');
      link.href = objectURL;
      link.download = filename;
      link.click();
      const revoke = () => browserWindow.URL.revokeObjectURL(objectURL);
      if (typeof browserWindow.setTimeout === 'function') browserWindow.setTimeout(revoke, 0);
      else revoke();
      setStatus('評価結果を書き出しました。', 'ready');
      return true;
    }

    function exportJSON() {
      if (!manifest) return false;
      const filename = `speech-eval-${safeFilePart(manifest.set_id)}.json`;
      return download(filename, JSON.stringify(exportData(), null, 2), 'application/json;charset=utf-8');
    }

    function exportCSV() {
      if (!manifest) return false;
      const columns = ['set_id', 'case_id', 'text', 'intent', 'cue_hint', 'cue_applicable'];
      for (const clip of ['a', 'b']) {
        for (const field of RATING_FIELDS) columns.push(`${clip}_${field.key}`);
      }
      columns.push('preference');
      const rows = [columns.map(csvCell).join(',')];
      for (const item of manifest.items) {
        const entry = state.cases[item.id] || emptyCaseRatings();
        const row = [manifest.set_id, item.id, item.text, item.intent, item.cue_hint, item.cue_applicable];
        for (const clip of ['a', 'b']) {
          for (const field of RATING_FIELDS) row.push(entry[clip][field.key] ?? '');
        }
        row.push(entry.preference || '');
        rows.push(row.map(csvCell).join(','));
      }
      const filename = `speech-eval-${safeFilePart(manifest.set_id)}.csv`;
      return download(filename, `${rows.join('\r\n')}\r\n`, 'text/csv;charset=utf-8');
    }

    ui.previous.addEventListener('click', () => navigate(currentIndex - 1));
    ui.next.addEventListener('click', () => navigate(currentIndex + 1));
    ui.preference.addEventListener('change', () => {
      const item = manifest && manifest.items[currentIndex];
      if (item) setPreference(item.id, ui.preference.value);
    });
    ui.exportJSON.addEventListener('click', exportJSON);
    ui.exportCSV.addEventListener('click', exportCSV);

    return Promise.resolve()
      .then(() => browserWindow.fetch(MANIFEST_URL, { cache: 'no-store', credentials: 'omit' }))
      .then((response) => {
        if (!response || !response.ok) throw new Error('評価セットを読み込めませんでした。');
        return response.json();
      })
      .then((rawManifest) => {
        manifest = validateManifest(rawManifest, browserWindow.location.origin);
        try {
          const saved = browserWindow.localStorage.getItem(`${STORAGE_PREFIX}${manifest.set_id}`);
          state = normalizeSavedState(saved ? JSON.parse(saved) : null, manifest);
        } catch {
          storageUnavailable = true;
          state = normalizeSavedState(null, manifest);
        }
        ui.exportJSON.disabled = false;
        ui.exportCSV.disabled = false;
        renderCurrentCase();
        if (storageUnavailable) {
          setStatus('評価セットを読み込みましたが、ブラウザー保存領域を利用できません。再読み込み後に記録が残らない可能性があります。', 'error');
        } else {
          setStatus(`${CASE_COUNT}件のケースを読み込みました · セット ${manifest.set_id}`, 'ready');
        }
        const app = Object.freeze({
          setRating,
          setPreference,
          navigate,
          exportJSON,
          exportCSV,
        });
        const query = new URLSearchParams(browserWindow.location.search || '');
        if (query.get('diagnostic') !== 'mio') return app;
        return loadDiagnosticManifest().then(() => app);
      })
      .catch((error) => {
        setStatus(error instanceof Error ? error.message : '音声評価セットを読み込めませんでした。', 'error');
        return null;
      });
  }

  const speechEvaluation = { initialize, validateDiagnosticManifest, validateManifest };
  if (typeof module !== 'undefined' && module.exports) module.exports = speechEvaluation;
  if (typeof window !== 'undefined' && window.document) void initialize(window);
})();
