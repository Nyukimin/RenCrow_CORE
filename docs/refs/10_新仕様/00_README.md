# RenCrow 新仕様

## このフォルダの役割

`docs/10_新仕様/` は、RenCrow の現行正本仕様 `docs/01_正本仕様/` とリファクタリング結果を統合した、新規リポジトリ移行用の最新仕様セットである。

新規リポジトリへ RenCrow を移す場合は、このフォルダを仕様入口として持っていく。旧リポジトリ内では `docs/01_正本仕様/` が履歴上の正本仕様として残るが、移行先で読むべき最新の整理済み仕様はこのフォルダである。

現行実装判断は `docs/10_新仕様/` を入口にする。旧 docs は履歴参照であり、現行実装として必要な項目は `13_実装項目インベントリ.md` と該当仕様文書へ反映する。

## 統合元

- `docs/01_正本仕様/`
- `docs/refactor/リファクタリング指針.md`
- `docs/refactor/フォルダ構成方針.md`
- `docs/refactor/検証方針.md`
- 現在の実装コード

旧 `docs/codebase-map/` と Phase 系中間文書は削除済みであり、現行実装判断では参照しない。実装と矛盾する場合は、現在のコードとこの新仕様群を優先する。

## 読み順

1. `01_新仕様_概要.md`
2. `02_モジュール構成仕様.md`
3. `90_Runtime_Topology_Config仕様.md`
4. `95_RenCrow_ToBe_統合仕様と実装方針.md`
5. `03_モジュール関係図.html`
6. `04_Chat_Worker_Coder仕様.md`
7. `05_Viewer仕様.md`
8. `06_IdleChat仕様.md`
9. `07_STT_TTS仕様.md`
10. `08_LLM_provider仕様.md`
11. `09_Memory_SourceRegistry仕様.md`
12. `89_RenCrow_Knowledge_Wiki運用仕様.md`
13. `10_検証仕様.md`
14. `11_分割再設計候補.md`
15. `12_新規リポジトリ移行方針.md`
16. `13_実装項目インベントリ.md`
17. `18_知識記憶システム構想.md`
18. `19_DCI_直接コーパス探索仕様.md`
19. `20_Tool_Harness_Contract_Mediation仕様.md`
20. `21_AI_Native_Engineering_Workflow仕様.md`
21. `22_Revenue_Operating_Principles仕様.md`
22. `23_Workstream_Operating_Loop仕様.md`
23. `24_Agent_Skill_Governance仕様.md`
24. `25_Codebase_Complexity_Hotspot_Skill仕様.md`
25. `26_Persona_Lore_and_Mutual_Observation仕様.md`
26. `27_Browser_Trace_to_API_Discovery仕様.md`
27. `28_SuperAgent_Harness_Reference_DeerFlow仕様.md`
28. `29_Sandbox_Promotion_Gate仕様.md`
29. `30_未実装項目実装仕様作成プロンプト.md`
30. `31_未実装項目実装仕様.md`
31. `32_E2E_runtime確認チェックリスト.md`
32. `33_現状残課題整理作業手順書.md`
33. `34_現状残課題クリア実装手順書.md`
34. `49_Movie_Graph_Mio_Topic仕様.md`
35. `50_Hobby_Graph_Mio_Topic仕様.md`
36. `51_Movie_Watch_Event実装仕様.md`

## 文書一覧

| 文書 | 役割 |
| --- | --- |
| `00_README.md` | 新仕様セットの入口、読み順、各文書の役割 |
| `01_新仕様_概要.md` | RenCrow の目的、原則、主要コンポーネントの位置づけ |
| `02_モジュール構成仕様.md` | Clean Architecture 風の層、主要実装箇所、仕様変更時に触る場所 |
| `90_Runtime_Topology_Config仕様.md` | `~/.rencrow/config.yaml` を module 配置と接続先の設計図として扱い、IP / host から endpoint を導出する runtime topology 仕様 |
| `95_RenCrow_ToBe_統合仕様と実装方針.md` | RenCrow のあるべき全体仕様、Agent / Advisor / Tool、Knowledge Relation、Economic Objective、実装 Phase、テスト条件を統合した実装方針 |
| `03_モジュール関係図.html` | モジュールの意味とつながりを概要図から詳細図まで示す HTML 図解 |
| `04_Chat_Worker_Coder仕様.md` | Chat / Worker / Coder の責務、route chain、plan / patch / execution 境界 |
| `05_Viewer仕様.md` | Viewer 表示、SSE event、log、history、audio trigger の契約 |
| `06_IdleChat仕様.md` | IdleChat の raw response、view data、audio trigger、forecast/story/normal mode |
| `07_STT_TTS仕様.md` | STT 入力経路、TTS provider / bridge / audio router、口パク trigger |
| `08_LLM_provider仕様.md` | Chat / Worker / Heavy / Wild / Coder provider と factory / middleware |
| `09_Memory_SourceRegistry仕様.md` | conversation memory、L1SQLite、VectorDB、DuckDB、Source Registry |
| `89_RenCrow_Knowledge_Wiki運用仕様.md` | 既存 docs / rules / memory / SQL / RecallPack を AI が参照しやすい Markdown Wiki として索引化する運用仕様 |
| `10_検証仕様.md` | unit / integration / e2e / live / browser 検証の標準 |
| `11_分割再設計候補.md` | 1 対 1 で説明しにくい箇所や将来の分割候補 |
| `12_新規リポジトリ移行方針.md` | 新規リポジトリへ持っていく docs と持っていかない旧 docs |
| `13_実装項目インベントリ.md` | 現行実装項目の実装済み / 部分実装 / 未実装 / 移行対象外一覧 |
| `18_知識記憶システム構想.md` | Personal Archive、Creative Knowledge、News、Daily Intake、Dream Consolidation など知識・記憶の入口構想 |
| `19_DCI_直接コーパス探索仕様.md` | RAG / VectorDB だけではなく、原文コーパスへ戻って証拠を調べ直す DCI 仕様 |
| `20_Tool_Harness_Contract_Mediation仕様.md` | Worker / Coder / DCI の tool call を validate-then-repair で調停し、Command Gate へ渡す共通 Tool Harness 仕様 |
| `21_AI_Native_Engineering_Workflow仕様.md` | Project Memory、Project Init、worktree、Skill、Subagent、Context tracking など AI が働く開発環境運用仕様 |
| `22_Revenue_Operating_Principles仕様.md` | 市場調査、商品設計、投稿、導線、顧客の声、Human Decision Gate を扱う収益化行動原則 |
| `23_Workstream_Operating_Loop仕様.md` | Workstream、Vault、Goal、Artifact、Heartbeat、Steering、Remote Control を扱う継続作業ループ仕様 |
| `24_Agent_Skill_Governance仕様.md` | Skill Registry、Bootstrap、Auto-trigger、PR Gate、Core / Plugin / Project 分離を扱う Agent 行動規律仕様 |
| `25_Codebase_Complexity_Hotspot_Skill仕様.md` | コードベースの複雑性ホットスポットを Report-only で検出し、Evidence、リスク、必要テストを提示する Skill 仕様 |
| `26_Persona_Lore_and_Mutual_Observation仕様.md` | lore / persona / trigger / canonical response / 相互観測を分離し、人格と成長ループを扱う仕様 |
| `27_Browser_Trace_to_API_Discovery仕様.md` | Browser Trace から API candidate、OpenAPI draft、coverage report を作り、Source Registry / Fetcher へ接続する仕様 |
| `28_SuperAgent_Harness_Reference_DeerFlow仕様.md` | DeerFlow を参考に、Lead Agent、Subagent、Sandbox、Skill、Context Engineering、Tracing を RenCrow の SuperAgent Harness として接続する仕様 |
| `29_Sandbox_Promotion_Gate仕様.md` | Sandbox での試行錯誤と、正式環境へ昇格するための Promotion Gate、Human approval、rollback plan を扱う仕様 |
| `30_未実装項目実装仕様作成プロンプト.md` | docs/10 内の未実装項目を洗い出し、重複仕様・対立仕様を整理して実装仕様化するための作業プロンプト |
| `31_未実装項目実装仕様.md` | docs/10 内の未実装 / 部分実装 / 要確認項目を整理し、参考仕様番号付きの段階的な実装 Phase へ落とした実装仕様 |
| `32_E2E_runtime確認チェックリスト.md` | `31` Phase 12 の E2E / runtime 要確認項目について、実機・browser・外部チャネル確認時の証跡、成功条件、失敗条件を定義するチェックリスト |
| `33_現状残課題整理作業手順書.md` | `31` と `32` で整理した残課題を、作業者が 1 件ずつ開始・確認・判定・docs 反映できる実務手順へ落とした作業手順書 |
| `34_現状残課題クリア実装手順書.md` | `33` の残課題を Goal 実行向けに小さな検証済み commit / push 単位へ分解し、自律継続条件と停止条件を定義する実装手順書 |
| `49_Movie_Graph_Mio_Topic仕様.md` | 映画.com カタログ、れんの鑑賞履歴、嗜好シグナル、Mio 話題候補、バックグラウンド収集理由を分離して扱う Movie Graph 仕様 |
| `50_Hobby_Graph_Mio_Topic仕様.md` | 映画、音楽、小説、漫画、アニメ、演劇、ビデオゲーム、ボードゲームを Mio の話題生成材料として扱う Hobby Graph 横断仕様 |
| `51_Movie_Watch_Event実装仕様.md` | 映画DBに「見た」鑑賞イベントを保存し、未解決タイトルとViewer表示へ接続する最初の実装仕様 |
| `77_STT音声_LLM音声_命名と経路仕様.md` | Viewer Chat マイク入力について、RenCrow_STT 経路を STT音声、RenCrow_LLM 直結経路を LLM音声として区別する命名・実装・検証仕様 |
| `78_LLM音声高速化_オーケストレーター実装プロンプト.md` | LLM音声経路を、測定・TDD・E2E・評価・再実装の反復で高速化するためのオーケストレータープロンプト |
| `78_LLM音声高速化_実行記録.md` | LLM音声高速化の実測結果、実装済みcommit、RenCrow_LLM live反映判定、Mac側反映後の再測定手順 |

## 基本方針

- モジュール化と疎結合を最重要原則とする。
- 単にファイルを分けるだけではモジュール化と扱わない。
- 仕様変更時に触る主担当ファイルを説明できる状態を維持する。
- Chat / Worker / Coder の責務境界を崩さない。
- Coder は破壊的操作を直接実行せず、plan / patch / proposal を生成する。
- Worker は実行主体として file / shell / git / test / patch 適用を担当する。
- fallback は正常系ではない。
- Viewer 表示、音声、口パク、ログを混同しない。
- repo example と live runtime config を混同しない。
- archive 文書を一次参照にしない。
