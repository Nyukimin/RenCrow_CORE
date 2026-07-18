# AGENTS.md

## このファイルの役割

このファイルは、RenCrow_CORE で作業する AI エージェント向けの最小実務ルールです。
製品仕様の唯一の正本は `docs/README.md` と、そこに列挙された9つの仕様書です。`CLAUDE.md` と `rules/` は作業制約であり、製品仕様を再定義しません。

このファイルは常時必要な判断基準だけを置く。path 固有の制約は `rules/`、再利用手順は `skills/`、機械的に止めるべき危険操作は hooks / permissions へ分離する。
指示配置は `rules/rules_instruction_placement.md`、path 固有制約は `rules/rules_path_scoped_constraints.md` に従う。

---

## 最初に読む順序

作業前に、必要に応じて次の順で確認すること。

1. `AGENTS.md`
2. `docs/README.md`
3. `docs/README.md` から選んだ対象領域の現行正本
4. `CLAUDE.md` と対象作業に関係する `rules/`
5. Viewer / UI / 見た目に関わる作業では `DESIGN.md`
6. 関連する実装ファイル、test、production wiring、config
7. 必要に応じて以下を参照
   - `rules/PROJECT_AGENT.md`
   - `rules/common/GLOBAL_AGENT.md`
   - `rules/common/rules_architecture.md`
   - `rules/common/rules_backend.md`
   - `rules/common/rules_frontend.md`
   - `rules/common/rules_observation_verification.md`
   - `rules/common/rules_regression_prevention.md`
   - `rules/common/rules_security.md`
   - `rules/common/rules_state_management.md`
   - `rules/common/rules_testing.md`
   - `rules/common/rules_logging.md`
   - `rules/routing-policy.md`
   - `rules/rules_viewer_ui.md`
   - `rules/rules_instruction_placement.md`
   - `rules/rules_path_scoped_constraints.md`
   - `rules/rules_search_browse_evidence.md`
   - `rules/rules_domain.md`

実装判断で迷った場合、**`docs/README.md` から該当する現行正本へ戻る**。不足事項を旧資料やarchiveで補完せず、production wiringとtestを照合して現行正本を更新する。
Viewer / UI / 見た目の判断で迷った場合、**視覚方針は `DESIGN.md`** とし、製品契約は現行正本、実装・状態管理・検証の制約はrulesへ戻る。

---

## 基本的な行動規範

AI エージェントは、実装や調査の成果をその場限りの回答で終わらせない。

- うまくいった手順、プロンプト、コマンド、検証観点、失敗から得た教訓は、必要に応じて docs / rules / prompts / skills / runbook へ残す。
- 「どうやったか」を説明するときは、抽象論だけでなく、実際に再利用できるプロンプト、コマンド、差分、チェックリストを添える。
- 回答は個別ユーザーだけでなく、後続の Worker / Coder / 人間が再利用できる形を意識する。
- 共有のための文書化は重くしすぎず、短いメモ、実例、検証済みコマンド、失敗時の見方から始める。
- ただし、安全制約、責務境界、検証条件はプロンプトだけに閉じず、コード、テスト、ログ、ルールへ落とす。

---

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

### コーディングAIの Safe / Tool Build Mode

コーディング作業は、次の2形態を区別する。

- **Safe Build Mode**: 既存コード、既存DB、既存環境、設定、運用系に触る作業。既存システムを壊さず、小さい差分、影響範囲、テスト、ログを優先する。
- **Tool Build Mode**: 新規ツール、小物スクリプト、補助アプリ、検証用CLIなどを、`/home/nyukimi/RenCrow/RenCrow_Tools`、`experiments/`、`sandbox/` 配下で既存本体から切り離して作る作業。

横断的に再利用するツール、ブラウザ sidecar、データ変換、検証用 CLI は `RenCrow_Tools` を正本とする。`RenCrow_CORE/tools/` は既存互換または本体密結合の残置場所であり、新規の横断ツール置き場にしない。

判断に迷う場合は Safe Build Mode に倒す。Tool Build Mode でも、既存本体、DB、設定、運用、Source Registry、memory、validator に踏み込む場合は Safe Build Mode として扱う。

---

## 絶対に守る検証・状態管理ルール

以下は、実装・調査・修正の種類を問わず必ず守る。

0. **仕様なし報告を禁止する**  
   「仕様がない」「未定義」「以前からそうだっただけ」と報告する前に、必ず正本仕様、関連 docs、rules、直近テスト、直近差分を検索する。ユーザーが「さっきまでできていた」「前に決めた」「仕様にあったはず」と指摘した場合は、仕様存在または回帰の強い証拠として扱い、実装側・テスト側・自分の確認漏れを先に疑う。確認できていない場合は「未確認」と報告し、「仕様なし」と断言してはいけない。

1. **ユーザー観測を Ground Truth とする**  
   ユーザーの実機観測は、AI の推測、局所テスト、ログ解釈より優先する。観測と分析が矛盾したら、分析側を疑って調査をやり直す。

2. **テスト通過だけで完了扱いしない**  
   ユニットテスト、Node/Go テスト、health ok、DOM 要素の存在だけで「直った」と判断してはいけない。実機またはそれに相当する E2E 確認で、対象フローが成立することを確認する。

3. **UI / Viewer は最低 1 セッションを追う**  
   表示不具合では、開始から終了まで最低 1 セッションを追い、表示本文、イベントログ、境界、終了状態を照合する。目視できない場合は描画ログを取る。

4. **Viewer は要約・到達性・非干渉を実ブラウザで確認する**  
   Ops / System / Jobs など監視系タブでも、初期表示は 3 から 5 個程度の要約ブロックに絞り、監査ログ、生テーブル、長文エラーは初期表示せず `details` などへ分離する。Viewer UI 変更では desktop に加えて narrow / mobile 幅でも確認し、長文や URL がカードを押し広げないことを確認する。live-mode、lipsync、固定入力バー、toast、overlay などクリック干渉しやすい UI は、見た目だけでなく computed style の `pointer-events`、`z-index`、`position`、`background`、`border`、`box-shadow`、`backdrop-filter` を確認する。

5. **不具合リストを優先度で消さない**  
   観測された事象は番号付きで保持する。優先度は作業順のためだけに使い、品質判断では全事象の状態を追跡する。「代表例」だけで完了扱いしない。

6. **ID を乱立させない**  
   新しい ID を追加する前に、既存の `session_id` などで表現できないか確認する。発話、応答、チャンク、セッションの単位を混同しない。

7. **cache / queue / pending 状態を乱立させない**  
   cache は性能改善や遅延吸収の道具であり、整合性設計の代替ではない。主たる真実、破棄タイミング、セッション境界、不正値混入防止を説明できない状態は追加しない。

8. **表示・音声・口パク・ログを混同しない**  
   表示は表示イベントまたは表示用 state を主たる入力とする。音声 chunk は音声再生と口パクのきっかけであり、本文表示の唯一の根拠にしない。

詳細は以下に従う。

- `rules/common/rules_regression_prevention.md`
- `rules/common/rules_observation_verification.md`
- `rules/common/rules_state_management.md`

---

## 絶対に守る実装開始・TDD・コミットルール

以下は例外なく守る。

1. **ユーザーの OK なしにコードを書き始めない**  
   仕様検討、設計相談、調査、方針確認の段階では、ユーザーが明示的に「実装して」「進めて」「OK」などの実装許可を出すまで、コード変更を開始しない。調査のための読み取りやログ確認はよいが、ファイル編集はしない。

2. **仕様は TDD で実装する**  
   新機能、仕様変更、バグ修正では、先に再現テスト、失敗テスト、または検証観点を定義してから実装する。テストを書けない場合でも、実装前に代替の検証手順を明文化する。

3. **コミットメッセージは日本語で書く**  
   commit message は日本語で書く。`feat:` / `fix:` / `docs:` などの prefix を使う場合でも、説明本文は日本語にする。

---

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

---

## 実装前の確認

変更前に必ず行うこと。

1. 対象タスクの責務を確認する  
   - Chat / Worker / Coder のどこか
2. `docs/README.md` から対象領域の現行正本を確認する
3. 対象ファイルだけでなく周辺コードも読む
4. 既存の命名・構造・流れを把握する

**仕様を読まずに実装しない。**  
**コードの現在動作を理解せずに修正しない。**

---

## 編集ポリシー

変更はできるだけ小さく、局所的に行う。

守ること：

- 最小変更を優先する
- 関係ない箇所を触らない
- 既存の命名と設計意図を尊重する
- ハックでごまかさず、根本原因を確認する
- より深い問題を見つけたら、勝手に拡張せず報告する
- 疎結合とモジュール境界を守る
- 既に分かれている処理は、理由なく 1 ファイルや 1 関数へ統合しない
- ファイル分割やモジュール化は段階的に行い、各段階で確認する

避けること：

- 大規模リファクタリング
- 無関係な cleanup
- 推測による仕様追加
- 責務をまたぐロジック移動

---

## 安全ルール

以下は高リスク操作として扱う。

- 依存関係の追加・更新
- 外部ツール、CLI、MCP、ブラウザ自動化ツール、補助スクリプト実行基盤の新規インストール
- ファイル削除
- CI / build / deploy 設定変更
- 大規模な横断修正
- セーフガードの無効化
- API キーやシークレットの取り扱い変更

これらは独断で進めず、確認を前提に扱うこと。

ただし、ユーザーが「危険性を確認した上でインストールしてよい」と明示した作業では、AI は次を満たす場合に限りインストールしてよい。

- 目的、導入元、実行権限、ネットワークアクセス、ファイル書き込み範囲を確認する
- 既存のインストール済みツール、自作ツール、`/home/nyukimi/RenCrow/RenCrow_Tools`、リポジトリ内 `scripts/`、`experiments/`、`sandbox/` で代替できないか先に確認する
- 新規ツール作成より、既存ツールの再利用・拡張を優先する
- インストールまたは作成したツールは、場所、用途、起動方法を記録し、次回以降は再利用を優先する
- 危険性が高い、出所が不明、シークレットが必要、既存環境を壊す可能性がある場合は実行前に停止して報告する

また、以下は禁止：

- API キーを設定ファイルへ平文保存する
- 保護ファイルパターンや Git auto-commit を無効化する
- backendやmodel固有のcontext、常駐、management方式をCOREへ固定する

---

## Worker 実行に関する注意

Worker は実行主体である。  
そのため、変更内容だけでなく次も重要である。

- 実行前に何をするか要約する
- 実行結果を記録する
- 失敗時は原因を切り分ける
- `job_id`、`session_id`、route、status などの追跡可能性を意識する
- ビルドが必要な案件では、再起動前に service 停止、残プロセス停止、`:18790` listen なし、`http://127.0.0.1:18790/health` 応答なしを確認してから、ビルド・起動を行う

ログとトレーサビリティの詳細は `CLAUDE.md` と `rules/common/rules_logging.md` を参照。

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

---

## テスト方針

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

---

## コードスタイル

このリポジトリの既存 Go コードの流儀を優先する。

推奨：

- 小さい関数
- 明示的な命名
- 読みやすい分岐
- 意図の分かる構造
- 必要最小限のコメント

避けること：

- 不要な抽象化
- 賢すぎる書き方
- その場しのぎの分岐追加
- 仮置きロジックの放置

---

## ドキュメント参照方針

このプロジェクトには製品仕様、作業制約、視覚方針、tool契約がある。役割を混同しない。

### 最小限で必ず意識するもの
- `AGENTS.md`  
  作業ルール
- `docs/README.md`
  唯一の現行正本の入口
- `CLAUDE.md`  
  AI作業時の短い参照案内
- `rules/routing-policy.md`
  ルーティング判断の実務ポリシー
- `rules/rules_viewer_ui.md`
  RenCrow Viewer の新 UI / 新タブ追加時の見た目と情報量の実務ルール

### 必要に応じて読むもの
- `docs/01_システム概要.md`
- `docs/02_機能仕様.md`
- `docs/03_キャラクター・エージェント仕様.md`
- `docs/04_アーキテクチャ概要.md`
- `docs/05_設定リファレンス.md`
- `docs/06_Public_API仕様.md`
- `docs/07_安全・承認・データ方針.md`
- `docs/08_実装状況・ロードマップ.md`
- `docs/09_運用ログ・panic保存仕様.md`
- `DESIGN.md`
- `TOOL_CONTRACT.md`

### 共通ルール
- `rules/common/GLOBAL_AGENT.md`
- `rules/common/rules_architecture.md`
- `rules/common/rules_backend.md`
- `rules/common/rules_frontend.md`
- `rules/common/rules_observation_verification.md`
- `rules/common/rules_regression_prevention.md`
- `rules/common/rules_security.md`
- `rules/common/rules_state_management.md`
- `rules/common/rules_testing.md`
- `rules/common/rules_logging.md`

### プロジェクト固有ルール
- `rules/PROJECT_AGENT.md`
- `rules/routing-policy.md`
- `rules/rules_viewer_ui.md`
- `rules/rules_domain.md`

### 旧 docs 参照ルール
- archive branch、Knowledge、削除済みdocs、版付き旧仕様、引き継ぎ資料を現行正本として参照しない。
- 現行正本に不足がある場合は、旧資料を補助正本にせず、実装・test・production wiringを確認して `docs/README.md` に列挙された該当文書を更新する。

---

## 作業手順

作業時は次の順序を基本とする。

1. 対象ファイルを特定する
2. 周辺コードを読む
3. 現在の挙動を短く説明する
4. 最小変更案を考える
5. 実装する
6. もっとも小さい妥当な確認を行う
7. 変更点、確認内容、残るリスクを報告する

---

## 良い変更の条件

良い変更とは次を満たすもの。

- 正しい責務の層にある
- 差分が小さい
- テストまたは確認がある
- ログや追跡性を壊していない
- 無関係な変更が混ざっていない
- 仕様と矛盾しない

---

## 禁止事項の要約

- 仕様未確認の実装
- Coder による破壊的操作の直接実行
- セーフガード無効化
- ログを残さない実行
- シークレットの平文保存
- 仕様と実装を黙ってずらす
- archive 文書の直接編集

---

## 迷ったときの判断

迷った場合は次を優先する。

1. 責務分離を守る
2. `docs/README.md` から対象領域の現行正本に戻る
3. 変更を小さく保つ
4. 安全側に倒す
5. 勝手に広げず、分離して報告する
