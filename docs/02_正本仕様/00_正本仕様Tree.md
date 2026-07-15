# RenCrow_CORE 正本仕様Tree

- status: active
- owner: RenCrow_CORE
- source: 2026-07-15 正本仕様棚卸し
- last_reviewed: 2026-07-15
- scope: `docs/02_正本仕様/` の責務、依存関係、優先順位、派生資料との境界

## 1. この文書の役割

この文書は、RenCrow_CORE の正本仕様を探すための索引である。
新しい挙動や契約そのものは定義せず、どの仕様が何を所有し、どの順で読むかを定義する。

正本仕様の実体は `docs/02_正本仕様/` に置く。`docs/01_理解/`、`docs/03_記憶検索/`、`docs/04_構築指標/`、`docs/refs/` は正本を説明・実装・補足する資料であり、正本を黙って上書きしない。

## 2. 正本仕様Tree

```text
RenCrow_CORE
├── A. 現行システム契約
│   ├── 01_仕様.md
│   │   └── 02_実装仕様.md
│   │       ├── 03_Runtime_Config.md
│   │       └── 04_IdleChat.md
│   │
│   ├── B. Ver0.80 モジュール構造
│   │   └── 05_RenCrow_CORE_Ver0.80_モジュール構成仕様.md
│   │       └── 06_RenCrow_CORE_Ver0.80_モジュール化実装仕様.md
│   │           ├── 07_RenCrow_CORE_Ver0.80_組み換え実装作業資料.md
│   │           └── 08_RenCrow_CORE_Ver0.80_Public_Repo起点化仕様.md
│   │
│   └── C. 外部moduleとのBridge契約
│       └── 09_Game_Bridge_Observer_API.md
│           └── external canonical: RenCrow_GAMES Observer / Replay仕様
│
├── D. To-Be 目標状態
│   └── 10_RenCrow_ToBe_統合仕様.md
│       ├── 11_RenCrow_ToBe_統合実装仕様.md
│       │   ├── derived: 04_構築指標/03_Advisor_AgentProfile接続実装仕様.md
│       │   ├── derived: 04_構築指標/04_KnowledgeRelation接続実装仕様.md
│       │   ├── derived: 04_構築指標/05_EconomicObjective接続実装仕様.md
│       │   └── derived: 04_構築指標/06_ToBe_Ops表示実装仕様.md
│       └── 12_CORE_機能台帳.md
│           └── evidence detail: refs/10_新仕様/13_実装項目インベントリ.md
│
└── E. 正本外の補助層
    ├── 01_理解/        初見向け説明
    ├── 03_記憶検索/    構築判断用アーキテクチャ
    ├── 04_構築指標/    実装順・受入・接続詳細
    ├── 05_運用/        セットアップ・現状確認
    ├── refs/           詳細・旧正本・補助仕様
    ├── 調査/           日付付き調査証跡
    └── 99_整理/        保持・除外・再分類の根拠
```

## 3. 正本インベントリ

| ID | 文書 | 役割 | lifecycle | 親・根拠 | 更新する場面 |
| --- | --- | --- | --- | --- | --- |
| `01` | `01_仕様.md` | 現行システムの利用者向け挙動、ルーティング、安全境界 | active | 最上位の現行仕様 | システムの意味・挙動・権限を変える |
| `02` | `02_実装仕様.md` | `01` を実装する唯一の統合実装正本 | active | `01` | I/O、route、状態、ログ、実装契約を変える |
| `03` | `03_Runtime_Config.md` | runtime topology、Config選択、LLM/TTS/STT接続境界 | active / specific | `01`, `02` | Config schema、endpoint、起動時選択を変える |
| `04` | `04_IdleChat.md` | IdleChatの状態、停止、TTS、Viewer契約 | active / specific | `01`, `02` | IdleChatのライフサイクル・表示・音声を変える |
| `05` | `05_RenCrow_CORE_Ver0.80_モジュール構成仕様.md` | module / feature / legacy-body の構造正本 | active / versioned | `02`, `03` | module責務・依存方向・状態所有を変える |
| `06` | `06_RenCrow_CORE_Ver0.80_モジュール化実装仕様.md` | `05` を壊さず実装するPhaseと検証 | active / versioned | `05` | モジュール移行手順・完了条件を変える |
| `07` | `07_RenCrow_CORE_Ver0.80_組み換え実装作業資料.md` | registrar・非削除確認の作業資料 | supporting | `06` | 作業手順を更新する。単独で仕様変更しない |
| `08` | `08_RenCrow_CORE_Ver0.80_Public_Repo起点化仕様.md` | Public repo初期投入時の公開境界 | frozen baseline | `05`～`07` | 再export・新規公開起点を作る場合だけ見直す |
| `09` | `09_Game_Bridge_Observer_API.md` | CORE側のread-only Game Bridge契約 | active / specific | RenCrow_GAMES正本 | proxy API・Viewer観察境界を変える |
| `10` | `10_RenCrow_ToBe_統合仕様.md` | Advisor、Knowledge Relation、Economic Objective等の目標状態 | active / target | `01`～`06`とrefsの採用判断 | To-Be責務・domain・安全境界を変える |
| `11` | `11_RenCrow_ToBe_統合実装仕様.md` | `10` のPhase、migration、test、acceptance | active / target implementation | `10` | To-Be実装順・移行・受入を変える |
| `12` | `12_CORE_機能台帳.md` | contract、実装、facade、legacy、conceptの現在状態 | active / inventory | code、test、`05`, `10`, `11` | production wiringや実装状態が変わる |

## 4. 優先順位と衝突時の判断

1. 対象機能に固有の正本を、一般仕様より先に適用する。
   - runtime Config は `03`。
   - IdleChat は `04`。
   - module構造は `05`、移行手順は `06`。
   - Game Bridgeは `09`。
2. `01` は「何をするか」、`02` は「どう実装するか」を所有する。片方だけを変更して矛盾させない。
3. `10`と`11`はTo-Be目標であり、記載だけで現行実装済みとは判断しない。
4. 現在の実装状態は`12`を入口にし、code、test、runtime、E2Eで確認する。
5. `07`は作業資料、`04_構築指標/`は派生実装資料である。親の正本を変更せずに意味を変えない。
6. `docs/refs/`は補助参照である。ファイル名や過去の位置づけに「正本」とあっても、現在の正本へ昇格するまでは直接優先しない。
7. LLM、TTS、STT、Vision、Games、Toolsの実装本体は各module repoを正本とし、COREは接続契約だけを所有する。

## 5. 目的別の読む経路

| 目的 | 読む順 |
| --- | --- |
| ルーティング・Agent・安全境界 | `01` → `02` → `12` |
| Runtime / endpoint / Config | `01` → `02` → `03` |
| IdleChat | `02` → `04` → 必要な`refs/07_IdleChat仕様/` |
| module分離・refactor | `05` → `06` → `07` → `12` |
| Public exportの履歴・再実施 | `05` → `06` → `08` |
| Game Observer接続 | `09` → RenCrow_GAMESの正本 |
| Advisor / AgentProfile | `10` → `11` → `12` → `04_構築指標/03` |
| Knowledge Relation / Recall | `10` → `11` → `12` → `04_構築指標/04` |
| Economic Objective / Revenue | `10` → `11` → `12` → `04_構築指標/05` |
| To-Be Ops | `10` → `11` → `12` → `04_構築指標/06` |

## 6. 外部moduleの正本境界

| 領域 | COREが所有するもの | 実装本体の正本 |
| --- | --- | --- |
| LLM | provider接続、role routing、health、Viewer proxy | `RenCrow_LLM` |
| TTS | provider接続、payload、再生状態、Viewer同期境界 | `RenCrow_TTS` |
| STT | provider接続、Viewer入力、WebSocket境界 | `RenCrow_STT` |
| Vision | 接続、成果物参照、Viewer連携 | `RenCrow_Vision` |
| Games | Bridge、Persona / Recall / Memory / Router接続 | `RenCrow_GAMES` |
| Tools | 呼び出し、検証結果の受領 | `RenCrow_Tools` |

## 7. 棚卸しで確認した整理課題

| ID | 状態 | 内容 | 扱い |
| --- | --- | --- | --- |
| `DOC-TREE-01` | resolved | 正本12本の役割と親子関係が番号一覧だけでは分からない | 本Treeを正本入口へ追加 |
| `DOC-PATH-01` | resolved | activeなAGENTS / rulesに移動前の`01_正本仕様`、`10_新仕様`参照が残る | 現行正本または`docs/refs/`へ修正 |
| `DOC-ROLE-01` | resolved | `03_記憶検索`が本文で「最小正本」を名乗り、配置規約と衝突する | 構築判断資料へ表記を統一 |
| `DOC-ROLE-02` | tracked | `07`は正本folder内の作業資料 | path互換のため移動せず、本Treeでsupportingと明記 |
| `DOC-LIFE-01` | tracked | `08`は完了済みPublic起点化の一回性が強い | path互換のため残し、frozen baselineとして扱う |
| `DOC-GAP-01` | tracked | Memory lifecycle、TTS/Viewer、Viewer詳細、指示配置等の詳細仕様はrefsにしかない | 個別改訂時に`02_正本仕様`へ吸収・昇格するか判断 |
| `DOC-GAP-02` | tracked | `02_実装仕様`のルール辞書とWorkerInput詳細は旧分割アーカイブを履歴ソースにしている | 改訂時に旧ZIP、現行code、testを監査し、必要な契約を`02`へ吸収 |
| `DOC-SIZE-01` | tracked | `01`、`02`、`10`が大きく、現行契約・履歴・実装状況が混在する | 意味を変えずに章分割する別作業とする |

## 8. 更新ルール

- 正本を追加・廃止・再分類したら、このTreeと`docs/README.md`を同時に更新する。
- `source_spec`、親子関係、lifecycleを明記する。
- 作業資料、調査ログ、派生実装資料を正本へ昇格する場合は、採用理由と置換対象を記録する。
- 移動時はリンクを全検索し、互換リンクまたは参照更新を同じ変更に含める。
- To-Beの実装状態が変わったら、仕様本文だけでなく`12_CORE_機能台帳.md`を更新する。
