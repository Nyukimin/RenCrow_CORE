# TTS Pronunciation Check Feature

## Owner

RenCrow_CORE Scheduler

## Responsibility

`tts_pronunciation_daily`をCOREの永続Schedulerへ登録し、RTX 5060 Tiが空くまで
IrodoriTTSの発音チェックを開始しません。RenCrow_TTSは時刻管理やGPU判定を持たず、
発音チェックTool APIだけを提供します。

## Flow

```text
CORE Scheduler due tick
  -> RTX機上のTool APIでRTX 5060 Tiを5回確認
  -> idle: RenCrow_TTS Tool POST /api/daily/run
  -> GET /api/daily/latestを完了までpoll
  -> Scheduler run logへ集計を保存

  -> busy: 5分後へdeferred
```

発声はMio、Shiro、Midoriを完全直列で処理します。確認文は一文に制限され、WAVは
各判定後に破棄されます。COREが保存するSchedulerログには集計だけが入り、WAVは
保存しません。

## Configuration

```yaml
tts:
  pronunciation_check:
    enabled: true
    tool_base_url: "http://192.168.1.205:7892"
    schedule: "cron 30 19 * * *" # UTC。JST 4:30
    gpu_match: "RTX 5060 Ti"
    min_free_mb: 768
    max_utilization_percent: 10
    idle_samples: 5
    sample_interval_seconds: 2
    retry_interval_seconds: 300
    timeout_minutes: 45
```

## Observation

`GET /viewer/scheduler`で`tts_pronunciation_daily`とrun logを確認します。手動実行も
同endpointの`action: run`を使い、GPU admissionを迂回しません。

COREの配置先にGPUは不要です。`tool_base_url`のRTX機が`nvidia-smi`を実行し、
`GET /api/gpu/status`でCOREへresource snapshotを返します。

## Error Contract

- GPU使用中: `deferred`として再試行時刻を更新
- `nvidia-smi`またはTool API障害: `failed`としてrun logへ記録
- Tool実行中のCORE終了: context cancelで停止
