# SpeechPlan の TTS receipt identity 不一致（2026-10-20）

## 文書情報

- 日付: 2026-10-20 JST
- 引き継ぎ元: RenCrow × Irodori-TTS 発話統合仕様 v1.0 の 206／207 実経路 E2E
- 契約正本: `docs/02_機能仕様.md` の「Main LLM発話統合（SpeechPlan v1.0）」
- 検出・根拠: 206 CORE の `speech.plan.observed` は `valid=true`、同じ turn の `speech.transport.observed` は `failure_stage=receipt_begin`。Gateway への直接認証済み合成は WAV を返した。

## 結論

### Failure / Problem

Mio の SpeechPlan と表示文は生成されたが、CORE が Gateway へ音声要求を送る前に receipt 開始で失敗し、Viewer で実音声が再生されなかった。失敗中も表示文は維持された。

### Cause

`MessageOrchestrator.ProcessMessage` は選択済み Task／Run を context に結ぶ前に TTS セッションを開始していた。TTS Action は別 Task／Run／Trace に保存され、後続の SpeechPlan 送信では会話ルートの identity と組み合わされた。Transport owner の `BeginRequest` は Action／Attempt と Task／Run の不一致を拒否した。`DistributedOrchestrator` も同じ順序で TTS を開始しており、共有 bridge の厳格な identity 照合を追加すると既存の分散 TTS も拒否されるため、両経路を同じ変更で修正する。

### Lesson / Invariant

TTS セッション開始、発話送信、終了には、選択済みの同一 Task／Run／Trace を使う。TTS Action／Attempt と Transport Request／Response はその lineage に属する。異なる identity の入力を黙って上書きしない。

### Enforcement / Tests

CORE は両オーケストレーターの TTS 開始前に選択済み identity を結び、TTS action bridge は保存済みセッション identity と呼出し context の一致を要求する。会話経路は Transport owner の実 `BeginRequest`／`CompleteResponse` を通すテストと Task／Run／Trace 不一致の拒否テストを持つ。分散経路は開始・送信・終了で同じ identity を使うテストを持つ。

## 実測

| 境界 | 結果 |
|---|---|
| SpeechPlan 検証 | 有効。Mio の本文と utterance を保持 |
| 206→207 Gateway 直接合成 | 認証済み要求で WAV 200 |
| Viewer 正規経路 | `receipt_begin` で停止。Gateway 到達前 |
| 修正後の実画面 E2E | この記録時点では未確認 |

## 未確認

修正配備後の Viewer 実再生、複数 utterance の先読み、利用者割り込み後の遅延音声破棄は、配備後 E2E で確認する。
