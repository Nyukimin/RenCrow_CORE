# RenCrow ATLAS アイデア記録・仕様保存 仕様 v1

## 1. 目的
ATLAS Itemを名称やキーワードだけで保存せず、そのItem単体で「何の問題を解決したかったか」「何を実現したいか」「なぜ検討したか」「Chat上に完成仕様があったか」を復元できる設計記録として保存する。

最上位原則:
ATLAS Item = 名前ではなく、意図を復元できる設計記録。

## 2. 必須情報
- Title: 短い識別名。
- Purpose: 何のためのアイデアか。必須。
- Problem: 現状の何が問題か。
- Idea: 何をするアイデアか。必須。
- Expected Effect: 導入で期待する変化。
- RenCrow Relation: 内部のどこと接続するか。
- Background: アイデアが生まれた背景。

## 3. Chat Context
Chat由来Itemはconversation provenanceを保持する。正確なmessage_idが利用できない場合は捏造せず、date/topic等の論理locatorで保存する。
ATLAS Item自身にPurpose/Problem/Idea/Backgroundを持ち、元Chatを開かなくても概要を復元できるようにする。

## 4. Specification Artifact
Chat内ですでに機能仕様、実装仕様、API仕様、DB仕様、Memory仕様、Agent仕様、UI仕様、TDD/E2E計画、確定Design Decisionが作られている場合、独立Artifactとして保存する。
ATLAS Itemはspecification_refs[]で参照する。

## 5. 原仕様保全
完成したChat仕様は要約だけへ置換しない。
Original Chat Specification = immutable capture。
ATLAS Summary = derived projection。
仕様変更時はv1/v2/v3として履歴を残し、旧版はsuperseded_byで結ぶ。

## 6. Item Schema
Purpose / Problem / Idea / ExpectedEffect / Background / RelationRefs / ConversationRefs / SpecificationRefsを追加する。

## 7. 昇格条件
Radar最低条件: Title / Purpose / Source / CapturedAt。
Purposeを復元できない場合はintake_incomplete。

Candidate最低条件: Purpose / Problem / Idea / RenCrow Relation / Source。
名前だけではCandidateにしない。

Adopted最低条件: Acceptance Criteria / Owner Module / Affected Modules / SpecificationまたはSpecification Required。

## 8. Viewer
Item詳細:
タイトル
→ 何のため
→ 何が問題
→ 何をする
→ 期待効果
→ RenCrowとの関係
→ 背景
→ 仕様
→ 情報源
→ Concept/Delivery State。

## 9. Currentにも意図を残す
実装完了後もPurpose等を削除しない。Currentは機能名だけの一覧にしない。

## 10. 既存Item Migration
現行ATLAS Itemを元Chat、既存仕様、GitHub、uploaded specsへ遡り、Purpose/Problem/Idea/Background/Specをbackfillする。
根拠のない内容をLLMが創作して埋めない。確認できない場合はunresolved/needs_context。

## 11. 最終原則
ATLASは「何というアイデアがあったか」ではなく、「なぜ考えたか」「何を変えたかったか」「どう実現するつもりだったか」「どこまで仕様化したか」「何を実装したか」を復元できるRenCrowの設計記憶とする。
