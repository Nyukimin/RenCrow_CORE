---
title: 調査 — IdleChat topic prompt／validator契約不一致
date: 2026-08-27 19:04
status: source_fixed_deploy_pending_at_record_time
skill: debug-investigate
symptom: IdleChat topic候補が長さ超過、meta語漏洩、Judge低得点で全拒否される
frequency: 2026-08-27の再起動前sessionでsingle／doubleを中心に反復。再配備後の短期観測では未再発
inputs: CORE journal、現行prompt／validator、既存調査、focused test
related: docs/調査/20260826_112314_IdleChat_Daily再起動後不安定化.md、docs/調査/20260825_212614_IdleChat_topic_TTS契約不整合.md
---

## 概要

起動後にactive sessionが空だった直接理由は、実装済みのDaily foreground優先契約であり不具合ではなかった。一方、再起動前に観測したtopic候補全拒否は、validatorが要求する4〜90文字とmeta語禁止をcanonical common promptが伝えていない契約不一致としてTDDで確認し、promptへ同じ境界を明示した。

## 調査経緯

### 仮説1: Daily enrichmentがIdleChat開始を不正に阻害する

- **根拠**: 再配備後、IdleChatはenabledだがactive sessionが空で、同時にDaily記事解析が進行していた。
- **検証結果**: 棄却
- **証拠**:
  - `checkAndStartChat`は自動modeの間だけ`dailyEnrichmentJob != nil`を開始抑止条件にしている。
  - `TestAutomaticIdleChatWaitsForDailyEnrichmentCompletion`とDaily foreground/cancel回帰testは成功した。
  - 手動modeはこの条件から除外され、Daily側もforeground busy時に同じstageで待機・再開する。
- **チェックリスト結果**:
  - □ 確証バイアス: sessionが空という観測だけで障害とせず、既存契約testと手動modeの反証を確認した。
  - □ 頻度制約: startup Daily実行中だけ開始が遅れる観測と設計条件が一致する。
  - □ ライフサイクル: Daily job begin／finish、foreground pause／resume、自動／手動開始を確認した。
  - □ 既存知見: 2026-08-26の前景優先修正と矛盾しない。

### 仮説2: topic生成promptが決定的validatorの制約を伝えていない

- **根拠**: live journalでは`topic length out of range`と`topic leaks meta term "生成"`が候補ごとに反復したが、common promptに文字数と全meta語の禁止がなかった。
- **検証結果**: 確認
- **証拠**:
  - `validateCommonTopic`は4〜90 Unicode runeを要求し、`CommonForbiddenMetaTerms`を拒否する。
  - RED testはcanonical rendered promptに4〜90文字契約がなく失敗した。
  - common promptへ長さとmeta語禁止を追加後、focused test、既存prompt test、`modules/chat`、IdleChat全packageが成功した。
  - testは禁止語を複製せず`modulechat.CommonForbiddenMetaTerms`を参照し、将来のvalidator driftも検出する。
- **チェックリスト結果**:
  - □ 確証バイアス: validatorを緩める案を採らず、再配備後の短期観測では未再発という反証境界も保持した。
  - □ 頻度制約: model出力が制約を偶然満たす場合は成功し、長文・meta語を含む候補群ではattemptごとに反復する条件と一致する。
  - □ ライフサイクル: seed展開、prompt生成、候補parse、candidate validation、有限retry、最終失敗を追跡した。
  - □ 既存知見: 過去のgenre literal prompt／validator不一致と同型だが、今回は長さと共通meta語という未投影境界に限定した。

### 仮説3: Gateway、TTS、CORE serviceの停止が主因

- **根拠**: 過去に`context canceled`、`signal: killed`、TTS voice不一致が存在した。
- **検証結果**: 棄却
- **証拠**:
  - 調査時のCORE、RenCrow_LLM Gateway、205 TTSはreadyだった。
  - 再配備後の短期観測では`context canceled`、`signal: killed`、`VOICE_NOT_FOUND`は0件だった。
  - 候補拒否はprovider成功後の決定的validatorで発生していた。
- **チェックリスト結果**:
  - □ 確証バイアス: 過去の障害名を現在の原因へ流用せず、現行healthと失敗境界を照合した。
  - □ 頻度制約: service全面停止なら成功候補が混在する観測を説明できない。
  - □ ライフサイクル: request成功からcandidate validationまでを分離した。
  - □ 既存知見: 過去のTTS／watchdog原因は修正済みで、今回のvalidator拒否とは別境界である。

## 根本原因

- **原因**: topic品質の決定的正本であるvalidatorの文字数・meta語制約が、生成入力のcanonical common promptへ投影されていなかった。
- **メカニズム**: LLMは意味上妥当でも90文字を超える候補や`生成`等を含む候補を返し、parse後に全候補がvalidatorで拒否され、有限retryを使い切った。
- **影響範囲**: common promptを使うsingle、double、external、movie、news、forecast、storyのtopic候補生成。seed literal内の同語はvalidatorと同様に許容する。

## 修正案

1. common promptに4〜90 Unicode文字と`CommonForbiddenMetaTerms`の禁止を明示し、seed literal例外を維持する。
2. validator、Judge閾値、retry回数は緩めない。
3. build／deploy／restart後、正規IdleChat sessionで候補採用、dialogue、TTS、Viewer表示まで確認する。

## 関連ソースファイル

- `modules/chat/topic_policy.go` - common topicの長さ・meta語validator正本。
- `prompts/idle_chat/topic_generator_common.md` - 全category共通の生成prompt。
- `internal/application/idlechat/topic_generator_prompt_test.go` - promptとvalidatorの機械的整合test。
- `internal/application/idlechat/orchestrator_monitor.go` - Daily実行中の自動IdleChat開始抑止。

## 教訓（将来の調査への知見）

- LLM出力を決定的validatorで拘束する場合、promptは同じ制約を具体的に伝え、testはvalidator正本からdriftを検出する。
- foreground優先による待機を停止と誤認せず、自動／手動、job begin／finish、pause／resumeを一つのlife cycleとして確認する。
