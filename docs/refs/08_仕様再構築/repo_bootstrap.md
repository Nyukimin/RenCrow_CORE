# repo_bootstrap.md

## 目的

この文書は、RenCrow の仕様群を新規リポジトリへ配置し、最小構成で立ち上げるための初期手順を定義する。

本書の目的は、以下の3点である。

1. 仕様書群の配置場所を固定する
2. 実装の着手順を固定する
3. 最初の段階で作り込まないものを明確にする

この文書は「全部入りの完成形」を要求しない。まずは Chat / Worker / Coder / Event / Hook の最小ループを通すことを優先する。

---

## 立ち上げ原則

新規リポジトリの立ち上げでは、以下を守る。

- 仕様書を先に置き、実装は後から追う
- EventId と Hook の枠組みを先に決める
- Chat だけを先に賢くしない
- Worker と Coder の責務を混ぜない
- 永続化の境界を先に決める
- Sandbox や権限が未整備のまま Coder を強くしない
- 自動化は「観測できるもの」から入れる

---

## 推奨ディレクトリ配置

以下を初期構成の基準とする。

```text
repo-root/
├─ docs/
│  ├─ specs/
│  │  ├─ chat_spec.md
│  │  ├─ worker_spec.md
│  │  ├─ coder_spec.md
│  │  ├─ event_schema.md
│  │  ├─ hook_policy.md
│  │  ├─ commands.md
│  │  ├─ storage_layout.md
│  │  ├─ routing_rules.md
│  │  ├─ task_payloads.md
│  │  ├─ runtime_state.md
│  │  ├─ session_lifecycle.md
│  │  ├─ failure_recovery.md
│  │  ├─ maintenance_jobs.md
│  │  ├─ observability.md
│  │  ├─ security_boundary.md
│  │  ├─ memory_policy.md
│  │  ├─ artifact_policy.md
│  │  ├─ integration_contracts.md
│  │  └─ repo_bootstrap.md
│  └─ decisions/
│     ├─ ADR-0001-actor-boundary.md
│     ├─ ADR-0002-eventid-policy.md
│     └─ ADR-0003-sandbox-policy.md
│
├─ runtime/
│  ├─ sessions/
│  ├─ locks/
│  └─ temp/
│
├─ storage/
│  ├─ events/
│  ├─ memory/
│  │  ├─ profile/
│  │  ├─ skills/
│  │  ├─ history/
│  │  └─ candidates/
│  ├─ artifacts/
│  └─ audit/
│
├─ apps/
│  ├─ chat/
│  ├─ worker/
│  └─ coder/
│
├─ hooks/
│  ├─ delegation/
│  ├─ pre_tool_use/
│  ├─ post_tool_use/
│  ├─ stop/
│  └─ memory_candidate/
│
├─ schemas/
│  ├─ event/
│  ├─ task/
│  ├─ result/
│  └─ memory/
│
├─ scripts/
│  ├─ bootstrap/
│  ├─ verify/
│  └─ maintenance/
│
└─ tests/
   ├─ contract/
   ├─ hooks/
   ├─ routing/
   └─ e2e/
```

---

## 仕様書群の置き方

仕様書は `docs/specs/` に集約する。

理由は以下。

- 実装コードと分離しやすい
- 仕様変更履歴をコード変更と分けて追いやすい
- Chat / Worker / Coder の境界契約を先に固定しやすい

`docs/decisions/` には、仕様ではなく「採用した判断」を残す。

たとえば以下。

- なぜ Worker と Coder を分けたか
- なぜ EventId を親子構造にしたか
- なぜ Sandbox 外を明示許可にしたか

仕様書は「こうする」を書く。ADR は「なぜそうしたか」を書く。

---

## 最初に配置するファイル

リポジトリ作成直後は、少なくとも以下を最初に置く。

必須:

- `docs/specs/chat_spec.md`
- `docs/specs/worker_spec.md`
- `docs/specs/coder_spec.md`
- `docs/specs/event_schema.md`
- `docs/specs/hook_policy.md`
- `docs/specs/integration_contracts.md`
- `docs/specs/storage_layout.md`
- `docs/specs/security_boundary.md`

推奨:

- `docs/specs/commands.md`
- `docs/specs/task_payloads.md`
- `docs/specs/routing_rules.md`
- `docs/specs/runtime_state.md`
- `docs/specs/session_lifecycle.md`

後追いでよい:

- `docs/specs/maintenance_jobs.md`
- `docs/specs/observability.md`
- `docs/specs/memory_policy.md`
- `docs/specs/artifact_policy.md`
- `docs/specs/failure_recovery.md`
- `docs/specs/repo_bootstrap.md`

---

## 実装フェーズ

新規リポジトリでは、以下の順で実装する。

### Phase 0: 骨組みだけ作る

目的は「置き場所」と「契約」を固定すること。

ここでは以下だけ行う。

- ディレクトリ作成
- 仕様書配置
- 最低限の README 作成
- Event / Task / Result の JSON スキーマ雛形作成
- テストディレクトリ作成

この段階では、LLM 呼び出しをまだ実装しなくてよい。

### Phase 1: EventId を通す

目的は「何が起きたか追える状態」にすること。

ここでは以下を実装する。

- RootEventId 発行
- ChildEventId 発行
- Event 書き込み
- Event の親子関係保存
- 簡単な Event viewer またはログ出力

この段階では、まず Chat 単独でもよい。重要なのは、後で Worker / Coder を足しても追跡できること。

### Phase 2: Chat の最小ルーティングを通す

目的は「自分で返す / Worker に送る / Coder に送る」の分岐を作ること。

ここでは以下を実装する。

- user input 正規化
- routing_rules に基づく一次判定
- 構造化 payload 生成
- 結果の統合
- 最終応答生成

この段階では、Chat はまだ高度な記憶を持たなくてよい。

### Phase 3: Worker の /investigate を通す

目的は「読む仕事」を Chat から分離すること。

ここでは以下を実装する。

- Worker 独立コンテキスト
- `/investigate` task の受理
- コード探索またはログ探索
- 短い summary と evidence 返却
- Worker task event 記録

重要なのは、Worker に編集機能を持たせないこと。

### Phase 4: Coder の /plan と /implement を通す

目的は「直す仕事」を Hook と Sandbox の中で動かすこと。

ここでは以下を実装する。

- `/plan` の受理と返却
- `/implement` の受理
- PreToolUse Hook
- PostToolUse Hook
- 変更ファイル記録
- 実行コマンド監査記録

この段階では、必ず検証前に stop させない。

### Phase 5: /verify を通す

目的は「変更したら検証する」を強制すること。

ここでは以下を実装する。

- `/verify` の受理
- テスト実行
- 差分確認
- 検証結果の返却
- Stop Hook による未完了検査

この段階で、Coder の最小ループが完成する。

### Phase 6: Memory と保存候補を分ける

目的は「保存候補抽出」と「保存確定」を分離すること。

ここでは以下を実装する。

- runtime candidate 保存
- Chat による保存可否判定
- Worker による memory 書き込み
- skill candidate 抽出
- history 要約保存

この段階でも、人物記憶は最小限でよい。

### Phase 7: Maintenance と Observability を足す

目的は「継続運用」を可能にすること。

ここでは以下を実装する。

- 日次 / 週次 / 起動時 maintenance job
- Event stream view
- Hook record view
- audit log 検索
- 異常系の見える化

---

## 最初に作るべき最小実装セット

新規リポジトリで最初に必要なのは、全部ではない。

最小セットは以下。

1. EventId 発行器
2. Chat ルータ
3. Worker `/investigate`
4. Coder `/plan`
5. Coder `/implement`
6. Coder `/verify`
7. PreToolUse Hook
8. PostToolUse Hook
9. Stop Hook
10. Event 保存
11. Audit 保存

この11個が通れば、最初の end-to-end が成立する。

---

## 初期フェーズで作り込まないもの

以下は後回しでよい。

- 多チャネル統合の完成版
- 高度な長期人物記憶
- 自動 skill 発火
- MCP の広域自動接続
- marketplace 的な plugin 配布
- 大規模ダッシュボード
- 完全自動保守
- 複数 Worker / 複数 Coder の同時並列制御

最初は「1 Chat / 1 Worker / 1 Coder / 1 storage / 1 event line」で十分。

---

## 初期ブートストラップ手順

### Step 1: 仕様を置く

- `docs/specs/` に仕様群を配置する
- `docs/decisions/` を作る
- `README.md` にアーキテクチャ概要を書く

### Step 2: スキーマを置く

- `schemas/event/` に Event schema
- `schemas/task/` に payload schema
- `schemas/result/` に result schema
- `schemas/memory/` に memory schema

### Step 3: 保存先を作る

- `storage/events/`
- `storage/memory/`
- `storage/artifacts/`
- `storage/audit/`
- `runtime/sessions/`

### Step 4: Chat を立ち上げる

- 入力正規化
- routing 実装
- result 統合
- 応答生成

### Step 5: Worker を立ち上げる

- `/investigate` のみ実装
- 返却形式を契約で固定
- 編集禁止を Hook で保証

### Step 6: Coder を立ち上げる

- `/plan /implement /verify` を段階実装
- Hook 3種を有効化
- Sandbox なしなら危険操作を拒否

### Step 7: 失敗ループを確認する

- Worker fail → Chat 戻し
- Hook reject → Chat 戻し
- Verify fail → plan へ戻す
- Memory candidate → Chat 判定

---

## テスト戦略

初期リポジトリでは、ユニットテストより契約テストを先に置く。

優先順位は以下。

1. schema validation test
2. routing contract test
3. hook rejection test
4. result contract test
5. end-to-end single flow test

特に以下は最初にテストする。

- Chat が不正な result を受理しないこと
- Coder が禁止コマンドを Hook で拒否されること
- Worker が編集系 task を受理しないこと
- Event の親子関係が壊れないこと
- Verify 未実行で completed にしないこと

---

## 初期 README に書くべきこと

新規リポジトリの `README.md` には最低限以下を書く。

- この repo の目的
- Chat / Worker / Coder の責務
- docs/specs/ を仕様の正本とすること
- EventId と Hook を共通基盤とすること
- 最初に参照すべき仕様書の順番
- 実行時の禁止事項の概要

README は長くしすぎない。入口だけを案内する。

---

## 運用開始判定

以下を満たしたら、v0.1 として運用開始してよい。

- Chat が routing できる
- Worker が investigate できる
- Coder が plan / implement / verify を通せる
- Hook が危険操作を止める
- Event と audit が追える
- failure recovery の戻り先が固定されている
- memory 保存が runtime candidate と分離されている

この時点で「便利」より「壊れにくい」を優先する。

---

## v0.1 の立ち上げ原則を一文で言うと

先に賢くするのではなく、先に境界と追跡可能性を作る。

この原則を崩さなければ、あとから Chat を強くしても、Worker/Coder を増やしても、全体が壊れにくい。
