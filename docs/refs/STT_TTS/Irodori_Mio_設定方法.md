
  Mio 音声指定

  Irodori-TTSで Female_01 / Mio を使う場合は、以下のreference音声を指定します。

  {
    "voice_id": "Female_01",
    "voice_name": "Mio",
    "provider": "irodori",
    "reference_audio": "/Users/yukimi/GenerativeAI/TTS/irodori/voices/Female_01_Mio/reference.wav",
    "reference_audio_url": "http://192.168.1.31:8090/voice_profile_tests/Female_01_Mio_test.wav",
    "num_steps": 40,
    "cfg_scale_text": 3.0,
    "cfg_scale_speaker": 5.0,
    "speaker_kv_scale_raw": "",
    "speaker_kv_min_t_raw": ""
  }

  Irodori Gradio APIへ渡す場合の要点:

  - uploaded_audio に reference_audio を渡す
  - RenCrow側からMacローカルパスを直接読めない場合は、reference_audio_url を取得して Gradio の /gradio_api/upload にアップロードし、その返却パスを uploaded_audio に渡す
  - num_steps は 40
  - cfg_scale_text は 3.0
  - cfg_scale_speaker は 5.0
  - speaker_kv_scale_raw は空欄
  - speaker_kv_min_t_raw は空欄
  - seedは通常ランダムでよい

  注意:

  - speaker_kv_scale_raw や speaker_kv_min_t_raw を強く指定すると、細かいノイズやマシンボイス感が増えやすいです。
  - Mioのreference音声はMac側ローカルパスです。クライアントPCから直接読むのではなく、TTSサーバ側で参照します。
  - クライアントは voice_id: "Female_01" または voice_name: "Mio" を送れば、サーバ側でこのreferenceに解決する実装にして
    ください。

  確認用音声:

  http://192.168.1.31:8090/voice_profile_tests/Female_01_Mio_test.wav

  TTS API接続先:

  http://192.168.1.31:7870
  
