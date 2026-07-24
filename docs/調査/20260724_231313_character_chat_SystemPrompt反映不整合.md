---
title: 調査 — character chat SystemPrompt反映不整合
date: 2026-07-24 23:13
status: resolved
skill: debug-investigate
symptom: character chatでJST時刻または選択characterのPersonaが応答へ正しく反映されない
frequency: 対象経路のrequestごとに再現
inputs: live Viewer API応答、CORE journal、source、unit test
related: docs/03_キャラクター・エージェント仕様.md、docs/06_Public_API仕様.md
---

## 概要

character chatのSystemPrompt自体にはJST時刻が追加されていましたが、後段の日時middleware、互換Chat APIのrecipient欠落、共有Recall Personaの混入という3つの独立した競合がありました。各原因を順に修正し、Mio、Shiro、Kuro、Midoriのlive応答で固有名とJST時刻を確認しました。

## 調査経緯

### 仮説1: 後段の日時middlewareがUTC時刻で上書きする

- **根拠**: live Mioが実際のJSTより9時間前の時刻を`JST`として返した。
- **検証結果**: 確認
- **証拠**:
  - `DateTimeProvider`がserver local timeをtimezoneなしで最新user messageへ追加していた。
  - 全primary LLM providerが同middlewareを通るため、characterごとではなく共通して発生する。
  - 稼働binaryにはJST SystemPrompt実装が含まれていたため、古いbinary仮説は棄却した。
- **チェックリスト結果**:
  - ☑ 確証バイアス: 単発のモデル誤答も候補にしたが、毎requestで矛盾指示を作るsourceとlive時差が一致した。
  - ☑ 頻度制約: middlewareを通る対象requestごとに成立し、観測頻度と矛盾しない。
  - ☑ ライフサイクル: start/stopや資源解放を伴わず、非該当。
  - ☑ 既存知見: 関連する過去の`docs/調査/`記録はなかった。

### 仮説2: 互換Chat APIがcharacterを表示だけに使い、routingへ渡していない

- **根拠**: `/viewer/api/chat`のresponseは指定characterを表示する一方、orchestrator requestにrecipientがなかった。
- **検証結果**: 確認
- **証拠**:
  - responderは`character_id`を`ChatID`へ設定していたが、`To`は空のままだった。
  - 空recipientはMioへ正規化される。
  - 修正後のlive Shiro requestは`agent.start from=shiro`となり、Shiro名とJST時刻を返した。
- **チェックリスト結果**:
  - ☑ 確証バイアス: main Viewerは別の`/viewer/send`を使い、既に`to`を送っている反証を確認した。
  - ☑ 頻度制約: 互換`/viewer/api/chat`だけで毎回発生し、main Viewerには発生しない。
  - ☑ ライフサイクル: request変換だけであり、非該当。
  - ☑ 既存知見: 関連する過去の`docs/調査/`記録はなかった。

### 仮説3: 共有RecallのMio Personaが別characterのSystemPromptと競合する

- **根拠**: routing修正後、event上はKuroとMidoriだったが、両者ともMioと名乗った。
- **検証結果**: 確認
- **証拠**:
  - `RecallPack.ToPromptMessages`は保存Personaをsystem messageとして生成する。
  - Heavy、Wild、Shiro Chatの固有SystemPromptと、共有Recall内のMio Personaが同一requestへ入っていた。
  - Personaだけを除外し、会話履歴、UserProfile、要約、Knowledgeを維持した後、live KuroとMidoriが自分の名前とJST時刻を返した。
- **チェックリスト結果**:
  - ☑ 確証バイアス: providerが`SystemPrompt` fieldを落とす仮説を確認したが、OpenAI互換変換は同fieldをsystem messageへ含めていた。
  - ☑ 頻度制約: ConversationEngineがRecallPackを返す別character requestで成立する。
  - ☑ ライフサイクル: Personaと共有contextの組み立てだけであり、非該当。
  - ☑ 既存知見: 関連する過去の`docs/調査/`記録はなかった。

## 根本原因

- **原因**:
  1. SystemPromptのJST時刻より後に、timezoneなしのserver local timeを強いuser指示として追加していた。
  2. 互換Chat APIが`character_id`をorchestratorのrecipientへ渡していなかった。
  3. 共有RecallのMio Personaを別characterの固有SystemPromptへ重ねていた。
- **メカニズム**: 後段かつ強い指示、空recipientのMio fallback、複数Personaのsystem結合により、正しい固有Promptが存在しても応答時の優先順位が崩れた。
- **影響範囲**: Mio、Shiro、Kuro、Midoriの通常Chat、互換Live2D Chat、ConversationEngineを使うWorker、Heavy、Wild。

## 修正案

1. canonical JST行がSystemPromptにある場合、日時middlewareは重複注入しない。
2. 内部LLMへ日時を補う場合もcanonical JST形式へ統一する。
3. `/viewer/api/chat`の`character_id`を`ProcessMessageRequest.To`へ渡す。
4. 非Mio characterへ共有Recallを渡す際はPersona SystemPromptだけを除き、その他の共有記憶を維持する。

## 関連ソースファイル

- `internal/infrastructure/llm/middleware/datetime.go` - JST統一と重複日時の抑止。
- `cmd/rencrow/live2d_chat_responder.go` - `character_id`からrecipientへの引き渡し。
- `internal/domain/conversation/recall_pack.go` - 共有contextから保存Personaだけを除外する操作。
- `internal/domain/agent/mio.go` - Shiro Chatなど非Mio recipientのRecall組み立て。
- `internal/domain/agent/heavy.go` - KuroのRecall組み立て。
- `internal/domain/agent/wild.go` - MidoriのRecall組み立て。
- `internal/domain/agent/shiro.go` - Shiro WorkerのRecall組み立て。

## 教訓（将来の調査への知見）

- Prompt本文の存在だけで反映済みと判断せず、provider直前の全system/user instructionとlive応答を確認する。
- 共有すべきMemoryと、共有してはいけないPersona SystemPromptを別のcontextとして扱う。
- characterを表示するfieldと、runtime routingのrecipient fieldを同一契約としてテストする。
