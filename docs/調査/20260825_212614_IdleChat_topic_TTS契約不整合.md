---
title: 調査 — IdleChat topic／TTS契約不整合
date: 2026-08-25 21:26 JST
status: confirmed
skill: debug-investigate
symptom: IdleChatのsingle／double topic生成が繰り返しschema拒否され、朗読TTSがVOICE_NOT_FOUNDで失敗する
frequency: 2026-08-25にtopic契約違反とTTS失敗を反復
inputs: CORE／TTS journal、CORE／TTS health、validator・prompt・voice mapping、既存正本と調査履歴
related: docs/04_アーキテクチャ概要.md、docs/02_機能仕様.md、docs/05_設定リファレンス.md
---

## 概要

single／doubleの候補JSONとseedは正常だったが、生成topicがseedのgenre文字列を含まず、COREの決定的validatorが全候補を拒否した。別系統ではCOREがShiroへ旧`male_01`を送る一方、稼働中Gatewayの公開voiceは`mio`／`shiro`／`midori`であり、朗読が404になった。両方ともCodexExeの一時停止では説明できない独立した契約不整合である。

## 調査経緯

### 仮説1: topic promptとvalidatorの契約が不一致

- **根拠**: 旧single／double promptは「中心にする」「両方を使う」とだけ指示し、requestのgenre値を必須文へ展開していなかった。一方validatorはsingleで`genre_1`、doubleで両genreをtopicへ要求する。
- **検証結果**: 確認
- **証拠**:
  - `modules/chat/topic_policy.go:265-289`はseedのgenreを候補topicへ含むことを決定的に検査する。
  - 2026-08-25のCORE journalで、JSON候補はparse済みなのに `topic_contract_violation: single topic must contain genre_1` がattempt 1〜3で反復し、`seed_summary=single genre_1=地域コミュニティ`、`topic_generation_no_candidates`、stock refill failureへ終端した。集計はsingle 542件、double 30件の同型拒否。
  - doubleでも`genre_1=防災 genre_2=再生医療`等のseedに対し、候補が両語を含まず `double topic must contain both genres` になった。
- **チェックリスト結果**:
  - □ 確証バイアス: 反証としてJSON構文、candidate_count、seed値は有効で、外部categoryの生成・Judge成功も確認した。意味品質だけを原因としなかった。
  - □ 頻度制約: 単発の生成揺らぎではなく、各attemptと複数時刻・両categoryで反復した。
  - □ ライフサイクル: seed選択 → CodexExe候補 → candidate validator → attempt再試行 → stock失敗までを追跡した。
  - □ 既存知見: docs/02とdocs/04のStock／episode境界、過去のIdleChat調査を照合し、seed選択や在庫正本の問題と混同しなかった。

### 仮説2: COREのShiro voice mappingがGateway公開IDと不一致

- **根拠**: incident時のHEAD `b4e2567`にある`modules/tts/idlechat_voice.go:8-20`はShiroを`male_01`へ写像していたが、Gatewayは公開voice IDをhealthで列挙する。
- **検証結果**: 確認
- **証拠**:
  - `curl http://127.0.0.1:7870/health/ready` はreadyを返し、voicesは`mio`／`shiro`／`midori`だけを広告した。
  - CORE journalは同日 `TTS push failed: /synthesis failed status=404 code=VOICE_NOT_FOUND` を反復した。一方TTS Gateway journalは`voice=mio`の合成成功を記録しており、Gateway全体停止やIrodori未readyではない。
  - `cmd/rencrow/idlechat_tts.go:65-74`は`CharacterID`と`VoiceID`を同じTTS session startへ渡すため、voice IDを直す際もcharacter identityを変更してはならない。
- **チェックリスト結果**:
  - □ 確証バイアス: TTS health／mio合成成功を反証として確認し、Gateway停止とは判定しなかった。
  - □ 頻度制約: `VOICE_NOT_FOUND`は同日に493件で、単一発話の偶発失敗ではない。
  - □ ライフサイクル: CORE session start → Gateway `/synthesis` → 404 → CORE error receiptを追跡し、mio成功という補償経路も確認した。
  - □ 既存知見: CORE→TTS Gateway→Irodoriの所有境界を既存正本と照合し、backend直結を仮説にしなかった。

### 仮説3: CodexExe kill、JSON parse、seed、healthの一時異常が主因

- **根拠**: 2026-08-25 21:17 JST頃にForecastのCodexExe dialogueが`signal: killed`で停止したため、同じ失敗を説明できる可能性を検討した。
- **検証結果**: 棄却
- **証拠**:
  - そのkillは一つのForecast dialogueを一時停止しただけで、直後以降もsingle／doubleの候補JSONはparseされ、seedは非空・doubleは異なる2語、TTS Gateway healthはreadyだった。
  - killの前後を通じて、topic失敗の固定errorはgenre literal欠落、TTS失敗の固定errorはunknown voiceであり、再試行回数を使い切っても同じ境界へ到達した。
- **チェックリスト結果**:
  - □ 確証バイアス: JSON／seed／healthが正常な反証を採用し、killへ原因を過度に寄せなかった。
  - □ 頻度制約: kill後にもschema拒否とVOICE_NOT_FOUNDが反復したため、単一job終了の頻度モデルと矛盾する。
  - □ ライフサイクル: topicの有限retryとTTSのrequest errorを別々に追跡し、cancelを成功やschema修復へ丸めなかった。
  - □ 既存知見: 過去のIdleChat調査・現行docsのretry／Gateway境界を確認し、旧仮説を無根拠に再利用しなかった。

### 仮説4: 日本語対話のuptake検査が空白区切りを前提としている

- **根拠**: 修正後のlive E2Eではtopic生成・Judge・Stock投入・session開始まで成功したが、自然な12 turn対話がsuffix修復を3回使い切り、再生前に停止した。
- **検証結果**: 確認
- **証拠**:
  - session `idle-1787661971-topic-00` のturn 3「本人へ電話してみたいな」に対し、turn 4は「電話の前に」で明示的に受けていたが、`dialogue_no_uptake`になった。
  - `hasDialogueUptake`は正規化後の文を`strings.Fields`で分割していた。空白を置かない通常の日本語文は全文が一tokenとなるため、共有語`電話`を検出できなかった。
  - 同じturnは`dialogue_category_axis_missing`も持ったが、uptakeを正しく認識すればscoreは60から80となり、既定の品質閾値を満たす。category判定や閾値を緩和する必要はない。
- **チェックリスト結果**:
  - □ 確証バイアス: LLM出力を無条件に正しいとはせず、artifactの全revisionと各failure reasonを比較した。
  - □ 頻度制約: 4 revisionで同型のfalse negativeがturn 2〜6へ移動し、有限repairでも収束しなかった。
  - □ ライフサイクル: topic採用 → dialogue生成 → CORE品質検査 → suffix修復 → failed → playback停止まで追跡した。
  - □ 既存知見: 決定的validatorを維持し、retry増加や品質閾値低下を回避した。

## 根本原因

- **原因1**: 候補生成promptがseed genreの意味利用だけを指示し、validatorが要求するgenre文字列の逐語包含をrequestごとの具体値として明示していなかった。
- **原因2**: COREのShiro向けIdleChat TTS mappingがGateway公開IDではない旧`male_01`を送信していた。character identityとvoice IDの境界が仕様として固定されていなかった。
- **原因3**: 日本語の前発話取り込み判定が空白区切りtokenだけを比較し、通常の日本語対話にある共有語彙アンカーを認識できなかった。
- **影響範囲**: single／double Stock補充、bootstrap、heartbeat／idle refill、日本語dialogue episodeの品質検査、およびShiro発話を含む全IdleChat TTS。JSON parse、seed選択、Gateway readiness、Mio合成成功はこれらの失敗を解消しない。

## 修正案

1. CORE正本へ、singleは`genre_1`、doubleは`genre_1`／`genre_2`の値そのものをtopicへ含める契約と、promptへ具体値を展開する規則を追加する。validatorは引き続き最終境界として保持する。
2. IdleChat TTSはcharacter identityをMio／Shiroのまま保持し、Gateway `voice_id`をそれぞれ`mio`／`shiro`へ固定する。`male_01`／`female_01`はGateway voice IDとして扱わず、非一致は`VOICE_NOT_FOUND`で可視化する。
3. 実装・再build・restart後は、rendered promptのliteral確認、single／doubleのTopic Stock公開、Gateway `/health/ready`、Mio／Shiroの実`/synthesis`、CORE journalのerror消失を同じcanonical routeで確認する。本調査ではコード変更・test・deployは実施していない。
4. uptake検査は既存の英語・空白token・指示語判定を保持し、日本語では句読点境界を跨がない2文字以上の共有語彙アンカーを追加する。一般語だけの一致は除外し、閾値・category軸・repair上限は変更しない。

## 関連ソースファイル

- `docs/04_アーキテクチャ概要.md:1563-1664` - topic seed／validator境界とepisode TTS経路の正本。
- `modules/chat/topic_policy.go:228-289` - seed必須条件とsingle／doubleのliteral包含validator。
- `internal/application/idlechat/topic_generator_prompt.go:12-42`、`prompts/idle_chat/topic_generator_single.md:6-8`、`topic_generator_double.md:6-8` - prompt値展開とカテゴリ指示。
- `internal/application/idlechat/dialogue_quality.go`、`dialogue_quality_test.go` - 日本語uptake判定と肯定／否定境界。
- `modules/tts/idlechat_voice.go:8-21`、`cmd/rencrow/idlechat_tts.go:65-74` - character／voice IDのCORE mappingとGateway request。
- `journalctl --user -u rencrow.service`、`curl http://127.0.0.1:7870/health/ready` - 2026-08-25のlive receipt／health evidence。

## 教訓（将来の調査への知見）

- LLM出力がJSONとしてparseできても、seedとvalidatorの意味・逐語契約が満たされた証拠にはならない。promptには実値を展開し、CORE validatorを最終判定にする。
- Gatewayのhealth一覧は物理backendの成功だけでなく、requestで使う公開IDのallowlistとして照合する。character identity、voice ID、音声再生結果を別々にtraceする。
