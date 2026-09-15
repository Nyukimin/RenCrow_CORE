# RenCrow_CORE 作業別ルール

対象作業の節だけを着手前に読む。文中のcode／設定pathはrepository root基準。製品仕様は`docs/README.md`から選び、共通方針は配布された共通AGENTS.mdを継承する。

<a id="contract"></a>
## COREの契約・routingを扱う場合

## プロジェクト概要

RenCrow（`RenCrow_CORE`）は、複数の LLM を役割分担させて動作する超軽量 AI アシスタントです。

目的：

- LINE / Slack などからの指示を受ける
- 複数の LLM を適切にルーティングする
- 低スペック環境でも安定動作する
- Chat / Worker / Coder の責務分離を保つ

---

## 最重要アーキテクチャ

このプロジェクトでは、以下の 3 役を厳密に分離する。

### Chat
- ユーザー対話を担当する
- ルーティング判断を担当する
- 結果返却を担当する
- 実装の詳細や破壊的操作を抱え込まない

### Worker
- 実行を担当する
- ファイル編集、コマンド実行、テスト実行を行う
- Coder が生成した `plan` / `patch` を実行する
- 実行結果を記録する

### Coder
- 設計とコード生成を担当する
- `plan` と `patch` を生成する
- 原則として破壊的操作を直接実行しない

---

## 最重要ルール

**Coder は破壊的操作を直接実行しない。**

Coder が行うのは次のみ：

- `plan` の生成
- `patch` の生成

実際の適用・実行は **必ず Worker が行う**。

この責務境界を崩してはいけない。

## 基本フロー

通常の処理フローは以下。

ユーザー入力
→ MessageOrchestrator
→ Mio が route decision
→ 選択された Chat / Worker / Coder / Advisor / Tool
→ Mio が結果を返す

実装時は、今の変更がどの層の責務かを先に判断すること。  
責務の違う層へロジックを混ぜないこと。

---

## ルーティングの考え方

主なカテゴリ：

- `CHAT`
- `PLAN`
- `ANALYZE`
- `OPS`
- `RESEARCH`
- `CODE`
- `CODE1`
- `CODE2`
- `CODE3`
- `CODE4`
- `WILD`

優先順位：

1. 明示コマンド
2. ルール辞書
3. 分類器
4. 安全側フォールバック

安全側フォールバックは `CHAT` とする。

詳細は `CLAUDE.md` と関連仕様を参照。

- 共有Viewer・会話・runtime・route・adapter・利用者向け挙動とcross-module意味論はCORE正本。CMDはCOREのCLI／client入口。該当変更はCOREを先に扱ってからCMDへ同期し、CMD専用なら実装前に理由を示す。正本索引は`docs/README.md`、Chat境界と標準Go配布境界は同`docs/04_アーキテクチャ概要.md`。
- Viewer／COREは対応moduleを経由する。LLMは`CORE → LLM Gateway → LLM Runtime → Backend → Model`、TTS／STTは各module→backend、画像生成はImage→ForgeNeo／Z-Image、認識はVision→Wild→Vision→CORE。Visionが前処理・正規化を所有し、raw mediaをCOREからWild／LLMへ直送しない。Mio／Shiro／Midori／Kuroによる文章生成の一部としてのCodexExe ImageGen利用だけは既存例外であり、通常Image経路の省略へ広げない。

ここでのChat／Worker／Coderは製品内部の役割であり、Codexのモデル分担を指定しない。共有契約を変える場合はCOREの現行仕様を先に確定し、client側の変更はその契約に従う。

routingの判断は`rules/routing-policy.md`を読む。

<a id="implementation"></a>
## COREのコード・設定・挙動を変える場合

実装・修正時は`rules/PROJECT_AGENT.md`と`rules/rules_domain.md`の該当部分を読む。Goのversion／module pathは`go.mod`、設定例は`config/config.yaml.example`の実ファイルを確認する。side effect・同期policy・degraded state・healthの意味を黙って変更しない。

### テスト方針

コード、設定、API、Viewer、runtime の挙動を変える場合は、`rules/common/rules_testing.md` を必ず適用する。

原則：

- 実装前に受入条件を定義し、期待した理由で失敗する Red を確認する
- Green は最小実装とし、その後に関連テストを通したまま Refactor する
- API / DB / adapter / config は unit に加えて統合・契約テストを行う
- Viewer、起動、service、WebSocket、stream、STT/TTS、外部連携は実runtime E2Eを行う
- Viewer は Playwright 実ブラウザで操作、network、console、最終状態、desktop / mobile を確認する
- 正常系をmockだけで代替せず、異常系fault injectionは実backendシナリオと分離する
- 未実施、失敗、flaky、環境不足がある場合は完了扱いせず、未確認範囲を報告する
- docs / コメントだけの変更は TDD / E2E 対象外だが、link / format / index を確認する

適用判断、隔離方法、シナリオ行列、完了証跡は `rules/common/rules_testing.md` を正とする。

archive文書を直接編集しない。CORE固有のarchitecture／backend／frontend／security／loggingを変更するときは、`rules/common/`の対応する規定だけ読む。

<a id="capability"></a>
## Tool・Skill・MCPを追加／変更する場合

新しいTool、Skill、MCPを追加するときは、`Runtime Capability Snapshot`へ接続し、対象Agentの
Stable RuntimeContextで認識でき、許可されたWorker経路で実行またはhandoffできることまでを
同じ変更の完了条件とする。Snapshotに出ないproduction capabilityを追加しただけの変更は未完了
として扱う。利用可能性は権限ではなく、Worker policyとAgentの役割境界を越えない。

並行した静的なTool一覧をPrompt、Workspace `tools.yaml`、docs、configへ複製しない。
`tools.yaml`は選択ガイダンスであり、runtimeの可用性・権限の正本ではない。次の既存経路を
必ず使い、起動時のsource/list、runtime wiring、metadata、prompt注入、policy付き実行、失敗時の
unavailable/fail-closed挙動を一続きで確認する。

| 種別 | 必須の起点・配線 | 必須テスト境界 |
| --- | --- | --- |
| Tool | `internal/infrastructure/tools/runner_registration.go`の登録とproduction Worker `RunnerV2.ListTools` → `cmd/rencrow/runtime_capability_snapshot.go` → Stable RuntimeContext | `cmd/rencrow/runtime_capability_snapshot_test.go`と該当Runner testでmetadata・Snapshot・実行policyを契約検証 |
| Skill | 設定済みrootの`internal/domain/context/skills_loader.go` → 起動時`SkillCatalog`／`skill.read`（Worker専用） → Snapshot | loader、`internal/infrastructure/tools/runner_skill_test.go`、snapshot testで名前・本文・無効状態を検証 |
| MCP | `cmd/rencrow/runtime_mcp_capabilities.go`の接続／`tools/list`観測 → `internal/infrastructure/tools/runner_mcp.go`の名前空間付きWorker adapter → 同一clientのlifecycle | MCP Runner testとsnapshot testで観測名・Worker metadata・Snapshot・停止処理を検証 |

directory scanによる実行能力の自動登録、未観測能力のavailable扱い、別providerへの自動fallback、
Skill本文による権限拡大を追加してはならない。AgentがSnapshotから選べても、実行は許可された
Workerまたは定義済みhandoffだけが担当する。

Tool実装では`TOOL_CONTRACT.md`も読む。

<a id="llm"></a>
## LLM連携・response処理を変更する場合

- 製品契約の正本は`docs/04_アーキテクチャ概要.md`の
  「CORE / RenCrow_LLM Chat境界」とする。
- COREはModel、provider、Backend、decoder、chat template、token、stop条件、
  コードフェンス等のモデル固有出力を認識しない。
- 正式経路は`CORE -> RenCrow LLM Gateway -> RenCrow LLM Runtime -> Backend -> Model`とする。
- RenCrow LLM RuntimeはCOREに対してユーザーChatと同等の論理message／assistant入出力を
  保つため、モデル／provider／decoder固有差をBackend／Model adapterと
  response-normalization境界で吸収する。
- 構造化出力時のモデル固有外装をCOREのparserやhandlerで補正しない。RenCrow_LLMを修正し、
  COREにはdomain contractの厳密検証だけを残す。
- 通常ChatのMarkdownを一律に除去せず、構造化出力contractに基づく正規化と区別する。

backendやmodel固有のcontext・常駐・management方式をCOREへ固定しない。

<a id="viewer"></a>
## Viewer・表示・音声同期を変更／調査する場合

3. **UI / Viewer は最低 1 セッションを追う**  
   表示不具合では、開始から終了まで最低 1 セッションを追い、表示本文、イベントログ、境界、終了状態を照合する。目視できない場合は描画ログを取る。

4. **Viewer は要約・到達性・非干渉を実ブラウザで確認する**  
   Ops / System / Jobs など監視系タブでも、初期表示は 3 から 5 個程度の要約ブロックに絞り、監査ログ、生テーブル、長文エラーは初期表示せず `details` などへ分離する。Viewer UI 変更では desktop に加えて narrow / mobile 幅でも確認し、長文や URL がカードを押し広げないことを確認する。lipsync、固定入力バー、toast、overlay などクリック干渉しやすい UI は、見た目だけでなく computed style の `pointer-events`、`z-index`、`position`、`background`、`border`、`box-shadow`、`backdrop-filter` を確認する。

8. **表示・音声・口パク・ログを混同しない**  
   表示は表示イベントまたは表示用 state を主たる入力とする。音声 chunk は音声再生と口パクのきっかけであり、本文表示の唯一の根拠にしない。

視覚方針は`DESIGN.md`、UI実務は`rules/rules_viewer_ui.md`、製品契約は現行仕様を読む。

<a id="state"></a>
## ID・cache・queue・永続状態を変更する場合

6. **ID を乱立させない**  
   新しい ID を追加する前に、既存の `session_id` などで表現できないか確認する。発話、応答、チャンク、セッションの単位を混同しない。

7. **cache / queue / pending 状態を乱立させない**  
   cache は性能改善や遅延吸収の道具であり、整合性設計の代替ではない。主たる真実、破棄タイミング、セッション境界、不正値混入防止を説明できない状態は追加しない。

`rules/common/rules_state_management.md`の該当制約を読む。

<a id="runtime"></a>
## Worker実行・ビルドを伴う再起動を扱う場合

## Worker 実行に関する注意

Worker は実Actorの依頼を実行する製品内の機構であり、Actor identityそのものではない。  
そのため、変更内容だけでなく次も重要である。

- 実行前に何をするか要約する
- 実行結果を記録する
- 失敗時は原因を切り分ける
- `job_id`、`session_id`、route、status などの追跡可能性を意識する
- ビルドが必要な案件では、再起動前に service 停止、残プロセス停止、`:18790` listen なし、`http://127.0.0.1:18790/health` 応答なしを確認してから、ビルド・起動を行う

ログとトレーサビリティの詳細は `rules/common/rules_logging.md` を参照。

---

## 再起動前の停止ルール

**RenCrow / RenCrow の再起動前には、必ず既存の関連作業を全停止すること。**

特に以下を満たさない限り、ビルド後の再起動を行ってはいけない。

1. `systemctl --user stop rencrow.service` を実行し、自動再起動元を止める
2. 残存する `rencrow` プロセスを停止する
3. `:18790` が listen されていないことを確認する
4. `http://127.0.0.1:18790/health` が応答しないことを確認する
5. その後にビルド・再起動を行う

このプロジェクトでは `rencrow.service` が `~/.local/bin/rencrow` を自動再起動することがある。  
そのため、**プロセスだけ止めて再起動してはいけない**。  
必ず service 停止まで含めてクリーンな停止状態を作ること。

<a id="evidence"></a>
## 観測・回帰・外部調査を行う場合

観測照合は`rules/common/rules_observation_verification.md`、回帰は`rules/common/rules_regression_prevention.md`、外部資料の証拠分離は`rules/rules_search_browse_evidence.md`の該当節を読む。

<a id="documentation"></a>
## 仕様・文書・コメントだけを変更する場合

TDD／runtime E2Eの対象外とし、link・format・index・CI guard・旧正本参照が残っていないことを確認する。製品仕様の入口は`docs/README.md`とし、別の正本を作らない。

<a id="placement"></a>
## 指示・path固有ルールを変更する場合

`rules/README.md`、`rules/rules_instruction_placement.md`、`rules/rules_path_scoped_constraints.md`を読む。
