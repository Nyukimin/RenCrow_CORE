# coding_agent_modes.md

# RenCrow コーディングエージェント運用モード仕様

## 1. 概要

RenCrow におけるコーディングAIは、単にコードを書く存在ではなく、目的に応じて振る舞いを切り替える作業主体として扱う。

本仕様では、コーディングAIを次の2形態に分ける。

| モード | 目的 | 主な対象 | 優先する価値 |
|---|---|---|---|
| Safe Build Mode | 既存システムを壊さずに変更を積み上げる | RenCrow本体、既存DB、既存環境、設定、運用系 | 安全性、再現性、影響範囲の把握 |
| Tool Build Mode | れんが作ってほしい道具を素早く形にする | 新規ツール、小物スクリプト、補助アプリ、実験用CLI | 速度、明確な入出力、使いやすさ |

2形態に分ける理由は、既存システムの改修と新規ツール制作では、必要な慎重さが違うためである。

既存システムを変更する場合、コードだけでなく、DB、記憶、設定、ログ、運用手順、環境変数、仮想環境などに影響する可能性がある。そのため、変更は小さく、検証可能で、元に戻せる単位に分ける必要がある。

一方で、新しい小物ツールや検証用プロトタイプを作る場合は、既存本体に触れず、独立した場所で作ることで、速く試すことができる。

この2つを混ぜると、次のような問題が起きやすい。

- 小物ツールの勢いで既存本体を大きく書き換えてしまう。
- 既存改修の慎重さを新規ツール制作にも持ち込み、開発が遅くなる。
- Worker と Coder の責務が曖昧になり、判断と実装が混ざる。
- 作業ログや記憶候補の扱いが不安定になる。

そのため RenCrow では、コーディングAIを **Safe Build Mode** と **Tool Build Mode** に分けて運用する。

なお、Worker / Coder はモードそのものではない。Worker / Coder は、各モード内で動く役割である。

```text
Safe Build Mode
  ├─ Worker: 調査・設計・リスク確認
  └─ Coder : 小差分実装・テスト・報告

Tool Build Mode
  ├─ Worker: 要望整理・仕様化・最小設計
  └─ Coder : 新規実装・README・サンプル
```

---

## 2. 用語定義

| 用語 | 定義 |
|---|---|
| Safe Build Mode | 既存システムを壊さず、小さな差分で安全に変更を積み上げるモード。既存コード、既存DB、既存環境、運用手順に触る場合は原則このモードを使う。 |
| Tool Build Mode | れんが作ってほしい新規ツール、小物スクリプト、補助アプリ、検証用プロトタイプを作るモード。既存本体から切り離して作ることを基本にする。 |
| Worker | 考える、調べる、設計する、検証する、判断する役割。作業方針、影響範囲、リスク、テスト観点を整理する。 |
| Coder | 読む、書く、直す、テストする、差分を出す役割。Worker が整理した方針に沿って実装する。 |
| staging | 外部取得データ、作業結果、記憶候補、検証前の成果物を一時的に置く領域。正式DBや本体へ入れる前の仮置き場所。 |
| validator | staging に置かれた成果物や記憶候補を検証し、正式採用してよいか確認する仕組み。 |
| official DB | RenCrow が正式な記録として扱うDB。ユーザー記憶、知識DB、Source Registry、運用ログなどを含む。 |
| memory candidate | 記憶として使えそうだが、まだ正式確定していない候補。Coder や Worker は作成できるが、直接確定してはいけない。 |
| confirmed memory | validator または れん の確認を通った正式記憶。以後の想起や判断に使える。 |
| repo_memory | リポジトリ固有の構造、重要ファイル、設計上の制約、過去の変更履歴などの記憶。 |
| failure_pattern | 過去に失敗した作業パターン。原因、症状、避けるべき操作、再発防止策を含む。 |
| accepted_pattern | 過去にうまくいった実装・運用パターン。再利用してよい安全な手順や構成を含む。 |

---

## 3. Safe Build Mode

Safe Build Mode は、既存システムを壊さずに変更を積み上げるためのモードである。

このモードでは、速く書くことよりも、既存の動作を守ることを優先する。変更は小さく、影響範囲を明確にし、テストとログで確認できる形にする。

### 3.1 対象例

Safe Build Mode の対象は、既存の構造や運用に影響するもの全般である。

- RenCrow本体
- Chat / Worker / Coder
- LLMサーバ
- 記憶システム
- staging / validator
- Source Registry
- 既存DB
- 既存ログ
- 既存設定ファイル
- PowerShell運用
- 既存の実行環境
- 既存のCI、テスト、ジョブ定義
- 既存のモデル配置、venv、CUDA、MLX、Ollama周辺

### 3.2 基本方針

Safe Build Mode では、次の方針を守る。

- 変更前に読む。
- 変更前に影響範囲を出す。
- 変更前に既存ルール、記憶、過去失敗例を確認する。
- 変更は最小単位に分ける。
- テストなしで完了扱いしない。
- 作業ログを残す。
- 記憶化できそうな知見は memory candidate にする。
- 正式DBや正式記憶への反映は validator を通す。
- 判断に迷う場合は、変更を止めて Worker に戻す。

### 3.3 基本手順

Safe Build Mode の標準手順は次の通り。

1. まず読む  
   関連する仕様、コード、設定、ログ、テストを確認する。

2. 影響範囲を確認する  
   変更対象がどの機能、DB、設定、運用、テストに影響するかを整理する。

3. 既存ルール・記憶・過去失敗例を確認する  
   `coding_rule`、`repo_memory`、`failure_pattern`、`environment_constraint` を参照する。

4. 変更方針を出す  
   何を変えるか、何を変えないか、どのテストで確認するかを明確にする。

5. 小さく直す  
   変更を一度に広げず、差分を小さく保つ。

6. テストする  
   可能な範囲で単体テスト、簡易実行、ログ確認を行う。

7. ログを残す  
   読んだファイル、変更したファイル、実行したコマンド、結果を記録する。

8. 記憶候補を作る  
   今後も使えそうな注意点、成功パターン、失敗パターンを `memory candidate` として残す。

9. validator を通す  
   DB、Source Registry、記憶、設定などへ反映する場合は validator を通す。

10. 採用判断を待つ  
   重要な変更は、れんまたは上位判断者の採用判断を待つ。

### 3.4 禁止事項

Safe Build Mode では、次を禁止する。

- 勝手な大改造
- 既存環境の無断変更
- official DB への直接書き込み
- Source Registry への無審査追加
- confirmed memory / pinned memory への直接昇格
- 既存設定ファイルの広範囲な書き換え
- 既存ファイルの大規模削除
- 不要なリネームや移動
- 破壊的コマンドの無断実行
- テストなしの完了報告
- 3回失敗しても同じ方針で続けること

---

## 4. Tool Build Mode

Tool Build Mode は、れんが作ってほしい道具を作るためのモードである。

このモードでは、既存本体を直接変えず、独立した道具として小さく作ることを基本にする。最初からRenCrow本体へ深く組み込むのではなく、単体で動くものを作り、必要に応じて後から連携口を作る。

### 4.1 対象例

Tool Build Mode の対象は、新規作成の補助ツールや検証用プロトタイプである。

- ログビューア
- Markdown変換ツール
- JSONL validator
- RSS取得スクリプト
- HTML Viewer
- 小さいFastAPIサーバ
- PowerShell運用ツール
- CSV整形ツール
- 実験用CLI
- RenCrow補助ツール
- テストデータ生成ツール
- 設定ファイルチェックツール
- レポート生成ツール

### 4.2 基本方針

Tool Build Mode では、次の方針を守る。

- 目的を満たす最小構成で作る。
- 入力と出力を明確にする。
- 新規ディレクトリで作る。
- 既存本体への直接接続を急がない。
- 単体で動くことを確認する。
- README、サンプル入力、実行例を付ける。
- 必要なら後からRenCrow連携口を作る。

### 4.3 基本手順

Tool Build Mode の標準手順は次の通り。

1. 目的を確認する  
   れんが何をしたいのか、どんな作業を楽にしたいのかを整理する。

2. 最小仕様を決める  
   最初に必要な機能だけを決める。余分な拡張は後回しにする。

3. 入力と出力を決める  
   ファイル、標準入力、CLI引数、JSON、Markdown、HTMLなど、入出力形式を明確にする。

4. 新規ディレクトリで作る  
   横断的に再利用するツールは `/home/nyukimi/RenCrow/RenCrow_Tools` に作る。実験段階だけなら `experiments/`、`sandbox/` など、既存本体から切り離した場所で作る。

5. 動くものを作る  
   まず最小の実行可能な形にする。

6. サンプル入力で確認する  
   想定される入力例を使って実行確認する。

7. テストを書く  
   最低限、主要な入出力のテストを用意する。

8. READMEを書く  
   目的、使い方、実行例、制約を書く。

9. 必要ならRenCrow連携口を作る  
   連携は後付けできるようにし、本体との結合を弱く保つ。

### 4.4 注意事項

Tool Build Mode でも、何でも自由に壊してよいわけではない。

特に、次の場合は Safe Build Mode に切り替える。

- 既存DBへ書き込む必要が出た。
- 既存設定を変更する必要が出た。
- 既存RenCrow本体に直接組み込む必要が出た。
- Source Registry や記憶システムに関係し始めた。
- 既存環境に影響するコマンドが必要になった。

---

## 5. Worker / Coder の役割分担

Worker と Coder は、各モードの中で分担して動く。

Worker は、判断と設計を担当する。Coder は、実装と確認を担当する。

| モード | Worker の役割 | Coder の役割 |
|---|---|---|
| Safe Build Mode | 調査、設計、リスク確認、影響範囲確認、既存ルール確認、過去失敗例確認、テスト観点作成 | 関連ファイルの読解、小差分実装、テスト実行、ログ確認、差分報告、memory candidate 作成 |
| Tool Build Mode | 要望整理、仕様化、最小設計、技術選定、入出力定義、README構成 | 新規実装、サンプル作成、テスト作成、README作成、実行例作成、必要に応じた連携口作成 |

### 5.1 Safe Build Mode における Worker

Safe Build Mode の Worker は、次を担当する。

- 既存仕様を読む。
- 影響範囲を調べる。
- 既存ルールと衝突しないか確認する。
- 変更手順を分解する。
- テスト観点を決める。
- 失敗時に前提を見直す。
- Coder に渡す作業範囲を限定する。

### 5.2 Safe Build Mode における Coder

Safe Build Mode の Coder は、次を担当する。

- 指定範囲だけ変更する。
- 差分を小さく保つ。
- テストを実行する。
- ログを読む。
- 修正内容を報告する。
- 記憶候補を出す。
- 自分の判断で大きな設計変更をしない。

### 5.3 Tool Build Mode における Worker

Tool Build Mode の Worker は、次を担当する。

- れんの要望を仕様にする。
- 入力と出力を決める。
- 最小構成を決める。
- 使用技術を選ぶ。
- READMEの構成を決める。
- 将来のRenCrow連携可能性を整理する。

### 5.4 Tool Build Mode における Coder

Tool Build Mode の Coder は、次を担当する。

- 新規ツールを作る。
- サンプル入力を作る。
- 実行コマンドを書く。
- テストを作る。
- READMEを書く。
- 必要ならRenCrow連携口を作る。

---

## 6. モード切り替え条件

モードは、作業対象と影響範囲によって切り替える。

### 6.1 Safe Build Mode にする条件

次のいずれかに当てはまる場合は、Safe Build Mode を選ぶ。

- 既存コードを変更する。
- 既存DBに触る。
- 既存運用に影響する。
- 既存環境を変更する。
- 本番系設定に触る。
- 過去に壊した領域に触る。
- 記憶に関係する。
- Source Registry に関係する。
- validator に関係する。
- 失敗時の影響範囲が大きい。
- 既存ユーザーデータ、ログ、記憶候補に影響する。
- モデル配置、venv、CUDA、MLX、Ollamaなどに触る。

### 6.2 Tool Build Mode にする条件

次の条件を満たす場合は、Tool Build Mode を選べる。

- 新しい小物ツールを作る。
- 既存本体にまだ接続しない。
- 横断ツールなら `/home/nyukimi/RenCrow/RenCrow_Tools`、実験なら `sandbox/`、`experiments/` 配下で作れる。
- 入出力が明確である。
- 失敗しても既存環境に影響しない。
- 単体で動作確認できる。
- 既存DBへ直接書き込まない。
- 既存設定を変更しない。

### 6.3 迷った場合のルール

判定に迷う場合は、Safe Build Mode を選ぶ。

理由は、Tool Build Mode で始めた作業が、途中で既存DBや既存本体に触れることがあるためである。影響範囲が不明な場合は、安全側に倒す。

---

## 7. ガードレール

RenCrow のコーディングAIは、作業速度よりも、環境と記憶を壊さないことを優先する。

### 7.1 禁止事項

次の操作は禁止する。

- official DB への直接write
- `user:<uid>` への直接upsert
- memory の直接確定
- Source Registry への無審査追加
- 検索結果の自動大量投入
- 共有環境の無断変更
- venv / CUDA / モデル配置の無断変更
- 既存ファイルの大規模削除
- 不要なリネームや移動
- `Move-Item` や `rm -rf` のような破壊的操作の無断実行
- テストなしで完了報告すること
- 3回失敗しても同じ方針で続けること
- ログを読まずに修正を繰り返すこと
- エラー内容を確認せずに依存関係を追加すること
- 本体に接続する前提で小物ツールを作り始めること

### 7.2 許可される範囲

次の範囲は、原則として許可される。

- `staging/` への出力
- `reports/` への出力
- `logs/` への出力
- `/home/nyukimi/RenCrow/RenCrow_Tools` 配下での新規作成
- `experiments/` 配下での新規作成
- `sandbox/` 配下での新規作成
- Pull Request または差分案の作成
- validatorテスト
- source候補の提案
- memory candidate の作成
- README、サンプル、テストの作成
- 既存ファイルを変更しない範囲での調査スクリプト作成

### 7.3 破壊的操作の扱い

破壊的操作が必要に見える場合は、ただちに作業を止める。

破壊的操作には、次のようなものを含む。

- ディレクトリ削除
- 大規模リネーム
- 環境移動
- 仮想環境の削除
- DBファイルの上書き
- 既存ログの削除
- モデルファイルの移動
- 依存パッケージの大規模更新

これらが必要な場合は、Worker に戻し、理由、代替案、バックアップ方法、復旧方法を整理する。

---

## 8. 記憶の使い方

コーディング用記憶は、雑談用の記憶とは扱いが違う。

雑談では「過去に何を話したか」が重要になる。コーディングでは、「何で壊れたか」「何が安全だったか」「この環境でやってはいけないこと」が重要になる。

### 8.1 コーディング用記憶の型

| 型 | 内容 | 例 |
|---|---|---|
| coding_rule | 常に守る開発ルール | テストなしで完了扱いしない。破壊的操作は確認する。 |
| repo_memory | リポジトリ固有の構造や重要ファイル | API本体は `src/server/`、設定は `config/` にある。 |
| failure_pattern | 過去に失敗した作業パターン | 依存関係を先に更新して既存環境を壊した。 |
| accepted_pattern | 成功した実装パターン | staging → validator → promoter の順に処理する。 |
| environment_constraint | OS、venv、モデル配置、PowerShell運用などの制約 | WindowsではPowerShell前提。共有venvを勝手に変更しない。 |
| command_history | 実行したコマンドと結果 | `pytest tests/test_x.py` が失敗し、原因はfixture不足だった。 |
| previous_diff | 直近の差分 | 前回は `validator.py` の入力チェックだけ変更した。 |
| known_risk | 触ると壊れやすい場所 | DB migration、モデルロード、Source Registry 周辺。 |
| worklog | その日の作業記録 | 読んだファイル、変更したファイル、テスト結果。 |

### 8.2 Safe Build Mode で参照する記憶

Safe Build Mode では、次の記憶を強く参照する。

- `coding_rule`
- `repo_memory`
- `environment_constraint`
- `failure_pattern`
- `accepted_pattern`
- `command_history`
- `previous_diff`
- `known_risk`
- `worklog`

Safe Build Mode では、作業開始時にこれらを Recall Pack に含めることを基本にする。

特に、次のような情報は優先度を高くする。

- 過去に壊した領域
- 触ると復旧が難しい環境
- DBや記憶に関係する処理
- Source Registry や validator に関係する処理
- 共有環境、venv、モデル配置に関する制約

### 8.3 Tool Build Mode で参照する記憶

Tool Build Mode では、必要最小限の記憶を参照する。

- `preferred_stack`
- `tool_template`
- `recent_tool_patterns`
- `coding_rule`
- `accepted_pattern`

Tool Build Mode では、過去の成功パターンを使って素早く作ることを重視する。ただし、既存本体へ接続する段階になったら Safe Build Mode に切り替える。

### 8.4 記憶を使うときの注意

- 推測だけで正式記憶にしない。
- 一度の失敗をすぐ `failure_pattern` に確定しない。
- 繰り返し出た制約、れんが明示した制約、validatorを通った情報だけを確定する。
- 古い記憶と新しい記憶が矛盾する場合は、Worker が確認する。
- 環境制約は強く扱うが、誤った制約が残ると開発を止めるため、定期的に見直す。

---

## 9. 記憶の昇格ルール

コーディングAIは、観測した情報をいきなり正式記憶にしてはいけない。

記憶は次の段階で扱う。

| 段階 | 意味 | 例 | 昇格条件 |
|---|---|---|---|
| observed | その場で観測しただけの情報 | あるコマンドが今回失敗した。 | 作業ログに記録する。 |
| candidate | 繰り返し使えそうな記憶候補 | このテストは環境変数がないと失敗しやすい。 | Worker が候補として整理する。 |
| confirmed | validator または れん の確認を通った正式記憶 | このrepoではDB変更前にmigrationテストが必須。 | validator または れん の確認を通す。 |
| pinned | 常に守るべき固定ルール | official DBへ直接writeしない。 | れんの明示、または運用規約として固定する。 |

### 9.1 昇格の原則

- Coder は `observed` と `candidate` まで作れる。
- Worker は `candidate` を整理し、validator に渡せる。
- `confirmed` への昇格は validator または れん の確認が必要である。
- `pinned` への昇格は、れんの明示、または運用規約としての採用が必要である。
- コーディングAIは、`confirmed` や `pinned` に直接昇格してはいけない。

### 9.2 memory candidate の例

```json
{
  "memory_id": "memcand_20260515_001",
  "type": "failure_pattern",
  "summary": "既存DBスキーマ変更時にvalidatorを通さず直接変更すると復旧が難しくなる",
  "evidence_event_ids": ["evt_20260515_001"],
  "status": "candidate",
  "proposed_by": "coder",
  "needs_review_by": "worker_or_ren"
}
```

---

## 10. 作業ログ仕様

各作業では、あとから検証できる作業ログを残す。

作業ログは、単なる報告ではなく、次回以降の記憶候補の材料になる。

### 10.1 ログ項目

| 項目 | 内容 |
|---|---|
| event_id | 作業イベントID。後から参照できる一意のID。 |
| mode | `safe_build` または `tool_build`。 |
| worker_id | 方針整理を担当したWorkerのID。いない場合はnull。 |
| coder_id | 実装を担当したCoderのID。 |
| repo | 対象リポジトリ。 |
| branch | 作業ブランチ。 |
| task_summary | 作業の要約。 |
| files_read | 読んだファイル一覧。 |
| files_changed | 変更したファイル一覧。 |
| commands_run | 実行したコマンド一覧。 |
| test_results | テスト結果。 |
| errors | 発生したエラー。 |
| retry_count | リトライ回数。 |
| failure_reason | 失敗理由。成功時はnull。 |
| next_action | 次に必要な作業。 |
| memory_candidates | 記憶候補。 |

### 10.2 JSON例

```json
{
  "event_id": "evt_20260515_001",
  "mode": "safe_build",
  "worker_id": "worker_arch_001",
  "coder_id": "coder_impl_001",
  "repo": "RenCrow",
  "branch": "safe/validator-schema-check",
  "task_summary": "staging validator の JSONL 入力チェックを追加した",
  "files_read": [
    "docs/coding_agent_modes.md",
    "src/validator/jsonl_validator.py",
    "tests/test_jsonl_validator.py"
  ],
  "files_changed": [
    "src/validator/jsonl_validator.py",
    "tests/test_jsonl_validator.py"
  ],
  "commands_run": [
    "pytest tests/test_jsonl_validator.py"
  ],
  "test_results": {
    "status": "passed",
    "summary": "12 tests passed"
  },
  "errors": [],
  "retry_count": 0,
  "failure_reason": null,
  "next_action": "Worker が validator 全体の影響範囲を確認する",
  "memory_candidates": [
    {
      "type": "accepted_pattern",
      "summary": "validator変更時はテストを同時に追加すると影響範囲を確認しやすい",
      "status": "candidate"
    }
  ]
}
```

---

## 11. 失敗時のルール

失敗時は、ただリトライするのではなく、仮説を更新する。

### 11.1 リトライ方針

| 回数 | 対応 |
|---|---|
| 1回目の失敗 | エラーを読む。ログ、スタックトレース、入力条件を確認する。 |
| 2回目の失敗 | 仮説を変える。原因候補を広げ、別の修正方針を試す。 |
| 3回目の失敗 | 前提を見直し、Workerへ戻す。作業を自動継続しない。 |
| 4回目以降 | れんまたは上位判断を待つ。破壊的な回避策を使わない。 |

### 11.2 失敗時に必ず記録すること

- 何をしようとしたか。
- どのコマンドを実行したか。
- どのエラーが出たか。
- 何を仮説として修正したか。
- なぜ失敗したと考えたか。
- 次に何を確認すべきか。
- `failure_pattern` 候補にすべきか。

### 11.3 禁止される回避策

失敗したからといって、次のような回避策を勝手に使ってはいけない。

- 依存関係を大量に更新する。
- venv を作り直す。
- 既存DBを削除する。
- 設定ファイルを丸ごと置き換える。
- テストを無効化する。
- エラー箇所を握りつぶす。
- 既存仕様を読まずに別実装へ置き換える。

### 11.4 Workerへ戻す条件

次の場合は、Coder は作業を止め、Worker に戻す。

- 同じ作業で3回失敗した。
- エラー原因が環境、DB、設定、記憶に関係しそうである。
- 修正が当初の範囲を超え始めた。
- 破壊的操作が必要に見える。
- 既存仕様と矛盾しそうである。

---

## 12. Coderへの指示テンプレート

ここでは、Coder に渡す指示テンプレートを定義する。

### 12.1 Safe Build Mode 用テンプレート

```text
あなたは RenCrow の Safe Build Mode で動く Coder です。

目的:
既存システムを壊さず、小さな差分で安全に変更してください。

作業ルール:
1. まず関連ファイルを読んでください。
2. 影響範囲を整理してください。
3. 既存ルール、repo_memory、failure_pattern、environment_constraint を確認してください。
4. 変更案を小さくしてください。
5. 既存挙動を壊さないでください。
6. official DB、user:<uid>、Source Registry、confirmed memory へ直接書き込まないでください。
7. 破壊的操作、不要な移動、不要なリネームをしないでください。
8. テストを実行してください。
9. 差分、実行コマンド、テスト結果を報告してください。
10. 今後に使えそうな注意点があれば memory candidate として出してください。

失敗時:
- 1回目はエラーを読んでください。
- 2回目は仮説を変えてください。
- 3回目は作業を止め、Workerへ戻してください。

出力:
- 読んだファイル
- 変更したファイル
- 変更内容
- 実行したコマンド
- テスト結果
- 残ったリスク
- memory candidate
```

### 12.2 Tool Build Mode 用テンプレート

```text
あなたは RenCrow の Tool Build Mode で動く Coder です。

目的:
れんの依頼を満たす最小ツールを、新規ディレクトリで作ってください。

作業ルール:
1. 目的を満たす最小ツールを作ってください。
2. 入力と出力を明確にしてください。
3. 横断ツールは /home/nyukimi/RenCrow/RenCrow_Tools に、実験段階のものは experiments/ または sandbox/ に新規ディレクトリを作ってください。
4. 既存RenCrow本体へ直接接続しないでください。
5. READMEを付けてください。
6. サンプル入力と実行例を付けてください。
7. テストを付けてください。
8. 既存RenCrow連携は後付け可能な形にしてください。

禁止:
- official DBへ直接writeしない。
- Source Registryを変更しない。
- 既存設定を変更しない。
- 既存環境を変更しない。

出力:
- 作成したディレクトリ
- 作成したファイル
- 使い方
- 実行例
- テスト結果
- 今後のRenCrow連携案
```

---

## 13. 具体例

### 13.1 例1: RenCrowの記憶DBスキーマを変更したい

判定: Safe Build Mode

理由:

- 既存DBに触る。
- 記憶システムに影響する。
- 失敗時の影響範囲が大きい。
- validator や migration が必要になる可能性がある。

Worker の作業:

- 現在のDBスキーマを読む。
- 既存の記憶型、validator、stagingとの関係を確認する。
- migration 方針を出す。
- 影響範囲を整理する。
- テスト観点を作る。

Coder の作業:

- 指定されたスキーマ差分だけを実装する。
- migration またはDDLを小さく作る。
- 既存データを壊さないテストを追加する。
- 実行結果をログに残す。
- 注意点を memory candidate にする。

### 13.2 例2: JSONLの形式チェックツールを作りたい

判定: Tool Build Mode

理由:

- 新規小物ツールである。
- 単体で動作確認できる。
- 既存本体に接続しなくても作れる。
- `/home/nyukimi/RenCrow/RenCrow_Tools` 配下で作れる。

Worker の作業:

- JSONLでチェックしたい項目を整理する。
- 入力ファイル、出力形式、エラー表示を決める。
- 最小仕様を決める。
- README構成を考える。

Coder の作業:

- `/home/nyukimi/RenCrow/RenCrow_Tools/tools/jsonl_validator/` にツールを作る。
- サンプルJSONLを用意する。
- 正常系と異常系のテストを書く。
- READMEを書く。
- 将来の staging validator 連携案をメモする。

### 13.3 例3: 既存LLMサーバのOpenAI互換APIを直したい

判定: Safe Build Mode

理由:

- 既存サーバに触る。
- 既存API挙動に影響する。
- Chat / Worker / Coder の呼び出しに影響する可能性がある。
- 環境やモデルロードにも影響しうる。

Worker の作業:

- OpenAI互換APIの期待仕様を確認する。
- 既存実装と差分を整理する。
- どのエンドポイントに影響するか確認する。
- テスト観点を決める。
- リスクを整理する。

Coder の作業:

- 該当エンドポイントだけを小さく修正する。
- 既存レスポンス形式を壊さない。
- APIテストを追加または実行する。
- ログを確認する。
- 変更点と残リスクを報告する。

### 13.4 例4: MarkdownからHTMLを作る小物ツールが欲しい

判定: Tool Build Mode

理由:

- 新規ツールである。
- 既存本体と切り離して作れる。
- 入力と出力が明確である。
- 失敗しても既存環境に影響しない。

Worker の作業:

- 入力Markdown、出力HTML、CSSの扱いを決める。
- CLIにするかGUIにするか決める。
- 最小仕様を決める。
- READMEの構成を作る。

Coder の作業:

- `/home/nyukimi/RenCrow/RenCrow_Tools/tools/md_to_html/` に実装する。
- サンプルMarkdownを用意する。
- 変換結果のHTMLを確認する。
- テストを書く。
- READMEを書く。

---

## 14. 最終まとめ

Safe Build Mode は、既存システムを守りながら育てるためのモードである。

このモードでは、既存コード、既存DB、既存設定、既存環境、記憶、Source Registry、validator への影響を慎重に扱う。変更は小さく、テスト可能で、ログに残る形にする。

Tool Build Mode は、れんの発想をすばやく道具にするためのモードである。

このモードでは、新規ツール、小物スクリプト、補助アプリ、検証用プロトタイプを、既存本体から切り離して作る。まず単体で動くものを作り、必要なら後からRenCrowに接続する。

Worker / Coder は、それぞれのモード内で役割分担する。

Worker は、考える、調べる、設計する、検証する、判断する。Coder は、読む、書く、直す、テストする、差分を出す。

RenCrowのコーディングAIは、「コードを書くAI」ではなく、次の二本立てで運用する。

- 安全に変更を積み上げるAI
- 道具を形にするAI

この分離により、RenCrow本体を壊さずに育てながら、れんのための便利な道具も素早く増やせる。

このドキュメントは、RenCrowのコーディングエージェント運用規約の初期版である。
