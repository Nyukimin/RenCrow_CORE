# STT / TTS 仕様

## 目的

STT / TTS は Viewer と会話体験に接続する audio 境界である。

STT は通常 chat への入力経路、TTS は応答や IdleChat の音声出力経路として扱う。Viewer 表示本文、音声 chunk、口パク trigger、ログを混同しない。

## STT

### 入力経路

```text
Browser mic
  -> Viewer STT client
  -> RenCrow same-origin /stt
  -> STT gateway / provider
  -> final text
  -> normal chat input
```

STT input は通常 chat に流す。IdleChat に直接流さない。

Viewer の browser-facing STT WebSocket は RenCrow が提供する同一 origin の `/stt` を正とする。Viewer が MacBook STT Gateway などの provider / gateway URL へ直接接続する構成は正規経路ではない。

RenCrow は `/stt` で browser からの `start` / audio chunk / `stop` と STT Gateway からの `ready` / `progress` / `partial` / `final` / `closed` / `error` を中継または provider 境界へ接続する。これにより Viewer は secure context、CORS、LAN IP、provider 実装差分を直接抱えない。

### 主な実装箇所

| 領域 | 主担当 |
| --- | --- |
| STT runtime factory | `cmd/picoclaw/stt_runtime_factory.go` |
| STT runtime config | `cmd/picoclaw/stt_runtime_config.go` |
| STT WebSocket | `cmd/picoclaw/stt_runtime_websocket.go` |
| STT HTTP / audio | `cmd/picoclaw/stt_runtime_http.go`, `cmd/picoclaw/stt_runtime_audio.go` |
| STT provider | `internal/infrastructure/stt` |
| route registration | `cmd/picoclaw/routes.go` |

### 注意

- `/stt` を主 endpoint とする。
- `/stt-ws` と `/ws` は互換 endpoint として扱う。
- `/viewer/runtime-config` の `stt_stream_url` は、Viewer から見た browser-facing URL を返す。原則として同一 origin の `/stt`、または Tailscale HTTPS origin の `wss://<ubuntu-tailnet-host>/stt` を返す。
- `stt_stream_url` に MacBook STT Gateway 直の `ws://<gateway-host>:8766/stt` を返してはいけない。Gateway 直 URL は RenCrow server-side の接続先または診断用であり、Viewer の通常接続先ではない。
- trailing silence がないと final text に進まない場合がある。
- gateway 未設定時の fallback は品質低下を伴う回復経路であり、正常系として扱わない。

### provider / busy policy

STT provider は `internal/infrastructure/stt` の `Provider` 境界で扱う。

| 項目 | 内容 |
| --- | --- |
| `external_http` | 既存の外部 HTTP STT provider。`provider_url` へ WAV multipart を送る。 |
| `openai-api` | OpenAI-compatible transcription endpoint 用 engine 名。実装は HTTP provider 境界を使うが、engine 名を `external_http` と分けて記録する。 |
| `busy_policy: queue_latest` | provider が処理中の場合、pending は最新 1 件だけ保持し、古い pending は `PROVIDER_BUSY` として supersede する。 |
| `busy_policy: reject` | provider が処理中の場合、新規入力を `PROVIDER_BUSY` で拒否する。 |
| `busy_policy: direct` | busy policy wrapper を通さず provider を直接呼ぶ。検証や特殊運用向け。 |

`queue_latest` は無制限 queue ではない。古い pending を破棄した場合も成功扱いにせず、error code として追跡する。

STT provider failure、timeout、busy、no speech は通常 chat 成功として隠さない。STT input は final text になった場合だけ通常 chat に接続し、IdleChat へ直接流さない。

## TTS

### 出力経路

```text
response / IdleChat event
  -> TTS bridge
  -> provider
  -> audio bytes / media URL
  -> audio router / Viewer playback
  -> lipsync trigger
```

音声 chunk は本文表示の唯一の根拠ではない。

### Viewer playback / スピーカ OFF 時の待ち合わせ

Viewer のスピーカボタンが ON の場合、TTS chunk の進行は原則として browser audio playback の再生完了に同期する。

Viewer のスピーカボタンが OFF の場合、音声を再生しないため、TTS 音声の実再生時間や audio element の完了待ちに依存してはいけない。
この場合の読み上げ進行は、chunk 間の待ち合わせを固定 500ms とする。

この 500ms は「音声なし表示のテンポ」を保つための Viewer 側 playback fallback 間隔であり、TTS provider の生成時間、audio router の配信間隔、口パク duration、本文表示生成の根拠とは混同しない。

期待動作:

- スピーカ ON: TTS audio chunk を再生し、再生完了または失敗境界に従って次 chunk へ進む。
- スピーカ OFF: 音声再生を行わず、各 chunk 表示後 500ms 待って次 chunk へ進む。
- スピーカ OFF 時も、TTS provider / audio router / lipsync の成功扱いにしてはいけない。

### 発話単位の TTS 待ち timeout

発話ごとに TTS を 5 秒で打ち切って通常会話として進める仕様は採用しない。

TTS は provider の初回ロード、GPU / CPU 負荷、queue 待ち、長文分割、ネットワーク、browser audio playback により 5 秒を超えることがある。5 秒以内に必ず返ることを前提にしてはいけない。

発話単位の UI 待ち上限は 60 秒を基準とする。60 秒以内に TTS audio chunk が到着し、再生可能な場合は、スピーカ ON では browser audio playback の再生完了に同期して次へ進む。

60 秒を超えた場合、その発話の音声は `tts_error=true` / `tts_error_kind=timeout` として扱う。表示本文がある場合は `display_only` として描画を完了してよいが、音声・口パク・TTS provider 成功として扱ってはいけない。

timeout 後に遅れて到着した audio chunk は、session_id / utterance_id / chunk_index で古い発話のものとして識別し、現在の発話や次 session の音声として再生してはいけない。

### session drain

session drain は「全音声を必ず待ち切る処理」ではなく、session 境界を乱さないための短い後始末時間である。

session 終了時に未完了 TTS がある場合、drain の UI 待ち上限は 60 秒を基準とする。60 秒を超えて残る音声は `session_audio_timeout` として閉じ、次 session へ進む。

drain timeout 時も、Viewer 表示本文は区切りのよい状態まで描画してよい。ただし、音声成功扱い、口パク成功扱い、別 session への音声持ち越しは行わない。

### 主な実装箇所

| 領域 | 主担当 |
| --- | --- |
| TTS runtime factory | `cmd/picoclaw/tts_runtime_factory.go` |
| IdleChat TTS queue / pending / voice | `cmd/picoclaw/idlechat_tts*.go` |
| RenCrow TTS bridge | `internal/infrastructure/tts/rencrow_tts_*.go` |
| Irodori provider | `internal/infrastructure/tts/irodori_*.go` |
| SBV2 provider | `internal/infrastructure/tts/sbv2_provider.go` |
| audio router | `internal/infrastructure/audiorouter` |
| Viewer audio route | `internal/adapter/viewer/audio_router_sse.go`, `/viewer/tts/audio` |
| lipsync / VTuber | `internal/infrastructure/vtuber`, `cmd/picoclaw/tts_vtuber_lipsync.go`, `cmd/picoclaw/vtuber_bridge.go` |

## 口パク trigger

口パクは音声出力や TTS event に同期する演出である。本文表示やログを口パクの根拠にしない。

TTS が失敗した場合、Viewer 表示が成功していても音声・口パクは成功扱いしない。

## 検証

STT:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./internal/infrastructure/stt ./cmd/picoclaw
```

TTS:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./internal/infrastructure/tts ./internal/infrastructure/audiorouter ./cmd/picoclaw
```

live / browser では次を確認する。

- mic permission
- WebSocket 接続
- final text
- busy policy が `queue_latest` / `reject` の期待動作をすること
- `openai-api` engine 名が provider 状態として区別されること
- normal chat 入力への接続
- TTS provider response
- browser playback
- lipsync trigger
- log と表示本文の分離
