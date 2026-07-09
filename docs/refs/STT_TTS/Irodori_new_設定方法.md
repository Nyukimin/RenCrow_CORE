 TTS指定仕様

  TTSの voice / reference / seed / 生成パラメータはサーバ側で管理します。
  クライアントは以下だけ送ってください。

  {
    "voice": "Mio",
    "style": "neutral",
    "text": "読み上げる文章"
  }

  または:

  {
    "voice": "Shiro",
    "style": "firm",
    "text": "読み上げる文章"
  }

  利用可能Voice

  [
    "Mio",
    "Shiro"
  ]

  利用可能Style

  [
    "neutral",
    "flat",
    "calm",
    "soft",
    "firm",
    "bright",
    "urgent"
  ]

  解決ルール

  1. クライアントは voice, style, text を送る。
  2. サーバは voice から対応するreference、seed、生成パラメータを解決する。
  3. サーバは style から読み方presetを解決する。
  4. サーバ側で必要に応じて文章整形とパラメータ調整を行う。
  5. Irodori-TTSへ送信する。
  6. 生成されたWAV URLをクライアントへ返す。

  注意

  - クライアントは reference_audio を送らない。
  - クライアントは seed_raw を送らない。
  - クライアントは num_steps や cfg_scale_* を送らない。
  - キャラ固定に必要な設定はすべてサーバ側で管理する。
  - Irodoriにはネイティブな感情パラメータはないため、style はサーバ側presetとして扱う。

