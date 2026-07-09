# security_boundary.md

## 目的

この文書は、RenCrow における安全境界を定義する。
対象は Chat / Worker / Coder / Hook / Scheduler が扱う権限、Sandbox、ネットワーク、MCP、共有環境保護であり、
「どこまで自動でやってよいか」「どこからは止めるべきか」を固定することを目的とする。

v0.1 では特に以下を重視する。

- Coder の自由実行を境界で制御すること
- Worker の探索範囲を責務内に閉じること
- Chat に最終判断を集中させること
- 共有環境を壊しうる変更を自動化しないこと
- 外部接続と MCP の自動拡張を抑制すること

---

## 基本原則

1. 権限は「信頼」ではなく「境界」で決める。
2. 自動許可は Sandbox 内の限定作業にだけ与える。
3. 非破壊手段がある場合、破壊的手段を先に選ばない。
4. 共有環境、共通ツールチェーン、既存稼働系への変更は高リスクとみなす。
5. ネットワーク、外部プロセス、MCP は明示的な許可単位で開ける。
6. Hook が境界チェックに失敗したときは、続行ではなく停止を優先する。
7. 最終的な許可判断は Chat が持つ。Worker と Coder は境界を越える自己判断をしない。

---

## 境界モデル

RenCrow の安全境界は 5 層で扱う。

1. Actor boundary
2. Filesystem boundary
3. Execution boundary
4. Network boundary
5. Integration boundary

各層は独立ではなく、より外側の層ほど影響が大きい。

- Actor boundary は「誰が何をしてよいか」
- Filesystem boundary は「どこを読めて、どこを書けるか」
- Execution boundary は「何を実行してよいか」
- Network boundary は「どこへ接続してよいか」
- Integration boundary は「外部能力を誰が有効化してよいか」

---

## 1. Actor boundary

### 1.1 Chat

Chat は判断主体であり、実行主体ではない。

許可:
- 記憶参照
- ルーティング判断
- 委譲タスク生成
- 保存可否判断
- 最終応答生成

不許可:
- 広範囲ファイル編集
- 危険コマンド実行
- Sandbox 外実行
- MCP の自動有効化
- 共有環境変更の自動決定

### 1.2 Worker

Worker は調査主体であり、実装修正主体ではない。

許可:
- 読み取り中心の探索
- ログ要約
- 差分比較
- 履歴検索
- 整備ジョブ実行

不許可:
- 実ファイル編集
- package install / uninstall
- 共有設定変更
- 外部接続の自動拡張
- 人物記憶の更新

### 1.3 Coder

Coder は実装主体だが、最も厳しい境界で囲う。

許可:
- 許可スコープ内でのコード編集
- 許可ツールでのテスト実行
- 差分確認
- 検証実行

不許可:
- Sandbox 外の自動実行
- 未許可ディレクトリ変更
- 共有環境の破壊的変更
- MCP 全開放
- main / master 直 push

### 1.4 Hook

Hook は境界補強機構であり、自律作業主体ではない。

許可:
- 停止
- 許可 / 拒否判定
- 監査記録
- 候補抽出

不許可:
- 重い探索
- 自動修正
- 自動権限昇格

---

## 2. Filesystem boundary

### 2.1 パス区分

v0.1 ではファイルシステムを次の 4 区分で扱う。

1. read_only_scope
2. writable_workspace
3. managed_artifacts
4. protected_shared_environment

### 2.2 read_only_scope

対象例:
- 調査対象 repo の読み取り
- 過去ログ
- Event / Audit の参照
- ドキュメント参照

許可 actor:
- Chat: 間接参照のみ
- Worker: 読み取り可
- Coder: 読み取り可

### 2.3 writable_workspace

対象例:
- 作業コピー
- 一時生成物
- 検証用ファイル
- 許可済み編集対象ソース

許可 actor:
- Coder: 書き込み可
- Worker: 原則書き込み不可。ただし整備ジョブの出力先に限定して可
- Chat: 書き込み不可

### 2.4 managed_artifacts

対象例:
- reports/
- artifacts/
- audit/
- runtime/
- events/

許可 actor:
- Worker: 整備系ファイル出力可
- Coder: 監査補助ファイル出力可
- Chat: 直接書き込み不可

### 2.5 protected_shared_environment

対象例:
- 共通 venv
- site-packages
- CUDA 関連
- モデル共通保存先
- システム PATH 配下
- 共有ビルド成果物
- 稼働中サービスの設定ディレクトリ

原則:
- 自動変更禁止
- 自動移動禁止
- 自動削除禁止
- 上書き更新禁止

ここへの変更は、明示許可がない限り Coder でも行わない。

### 2.6 非破壊原則

同じ目的を達成できるなら、優先順位は以下とする。

1. 読み取り確認
2. 設定差し替え
3. コピー
4. ジャンクション / シンボリックリンク等の非破壊迂回
5. 新規作成
6. 置換
7. 削除
8. 物理移動

Move-Item のような物理移動は、実行環境・モデル格納・venv・site-packages・ビルド成果物に対しては原則禁止とする。

---

## 3. Execution boundary

### 3.1 実行モード

実行は次の 3 モードに分ける。

- safe_auto
- gated_auto
- manual_only

### 3.2 safe_auto

条件:
- Sandbox 内
- 許可済みツールのみ
- writable_workspace 内のみ変更
- 共有環境へ影響しない

対象例:
- 単体テスト
- 差分確認
- 静的解析
- 一時ディレクトリでの検証
- 読み取り専用探索

### 3.3 gated_auto

条件:
- Hook 通過必須
- 制約付きで自動実行可
- 実行前後の監査必須

対象例:
- 許可済みファイルへの編集
- ローカル build
- 限定的な検証スクリプト
- 整備ジョブのインデックス更新

### 3.4 manual_only

対象例:
- 共有環境変更
- システム設定変更
- package manager による全体更新
- サービス停止 / 再起動
- 稼働中設定の上書き
- 外部資格情報変更
- 本番系 push / deploy

これらは自動実行しない。提案または手順化までに留める。

### 3.4.1 RenCrow 自己ライフサイクル変更

Repair / Coder Proposal / Worker 自動実行では、RenCrow 本体の稼働プロセスと live binary を変更しない。

manual_only とする対象:
- `rencrow.service` の `start` / `stop` / `restart` / `reload` / `enable` / `disable`
- `rencrow` プロセスへの `pkill` / `killall`
- `make install`
- `~/.local/bin/rencrow` へのコピー、上書き、削除

正しい処理:
1. Coder は「再起動または install が必要」と Proposal / plan に明記する
2. Worker は自動実行せず `approval_required` として停止する
3. Chat / Viewer はユーザーに manual approval を求める
4. 承認後、外側の operator が service を停止し、port 停止確認、install、起動、health check を順に行う

自動修復ジョブが自分自身を再起動すると、実行中の HTTP connection、Repair 状態、IdleChat 停止状態を失う可能性がある。そのため、自己ライフサイクル変更は「修復の一部」ではなく「承認済み運用手順」として扱う。

### 3.5 実行禁止コマンドの例

少なくとも以下は PreToolUse Hook で検出し、原則拒否する。

- `rm -rf` 相当
- `del /f /s /q` の広域削除
- `Move-Item` による実行環境移動
- main / master 直 push
- 無制限な `git clean`
- 許可されていない package manager 実行
- 未許可のシステムサービス操作
- Sandbox 外パスへの破壊的書き込み

---

## 4. Sandbox boundary

### 4.1 Sandbox の役割

Sandbox は「許可ルールの代替」ではない。 
Sandbox は、許可された actor が安全に速く動くための外枠である。

### 4.2 基本方針

- Coder の自動実行は Sandbox 前提とする
- Sandbox が使えない場合、実行モードを引き下げる
- `failIfUnavailable` 相当の考え方を採る
- Sandbox 不可時に自動で unsandboxed にフォールバックしない

### 4.3 Sandbox 内で許可しやすいもの

- ソース編集
- 一時生成物の出力
- ローカルテスト
- 差分確認
- ログの整形

### 4.4 Sandbox でも禁止するもの

- protected_shared_environment への変更
- 明示許可のない外部ネットワーク接続
- 未許可 MCP 起動
- 認証情報の読み出しや書き換え

---

## 5. Network boundary

### 5.1 原則

ネットワーク接続はデフォルト拒否、明示許可で開放する。

許可単位は以下のいずれかとする。

- ドメイン
- ホスト
- API 種別
- 用途
- actor

### 5.2 Chat

Chat は原則として自分で外部接続しない。 
必要な場合も、取得主体は明示的なツールまたは委譲先に限定する。

### 5.3 Worker

Worker は調査のために限定的な外部参照を行うことがあるが、スコープを固定する。

許可例:
- 明示されたドキュメント取得
- 指定ソースからのメタデータ確認

不許可例:
- 勝手な検索範囲拡張
- 認証が必要な外部領域への自動接続
- 結果保存先の自動拡張

### 5.4 Coder

Coder のネットワークはさらに厳しく扱う。

許可例:
- 明示許可された依存情報確認
- 明示許可された検証用アクセス

不許可例:
- 任意の package install
- リモートコード取得
- 未許可の API 呼び出し
- 自動アップデート

### 5.5 資格情報

認証情報、トークン、秘密鍵は protected_shared_environment に準じて扱う。

- 自動書換禁止
- 自動再発行禁止
- ログ出力禁止
- Event / Audit への平文記録禁止

---

## 6. MCP / Integration boundary

### 6.1 原則

MCP や外部統合は「便利だから全部開ける」対象ではない。  
Integration boundary は、最も見落としやすい拡張経路として厳格に扱う。

### 6.2 デフォルト方針

- `enableAllProjectMcpServers: false` 相当を原則とする
- git 管理下設定ファイルからの自動有効化をしない
- actor ごとに許可統合を分ける
- 有効化前に対象サーバの責務を明示する

### 6.3 Chat

Chat は MCP の利用判断を持つが、有効化処理を自動で行わない。

### 6.4 Worker

Worker は読み取り系統合のみを候補にする。  
書き込み系、実行系、権限拡張系の MCP は扱わない。

### 6.5 Coder

Coder は必要最小限の統合のみを使う。  
コード編集や検証に直接必要なもの以外は開けない。

### 6.6 禁止事項

- 未審査の MCP 全開放
- repo 内設定だけを根拠にした自動有効化
- Hook を経由しない統合追加
- Event 記録なしの統合実行

---

## 7. 共有環境保護

### 7.1 対象

共有環境とは、複数用途・複数 actor・既存稼働系が依存している基盤を指す。

例:
- 共通 Python 環境
- CUDA / cuDNN などの共有 GPU スタック
- モデル共通キャッシュ
- グローバル PATH 配下ツール
- 稼働中サービスの設定
- 共有の build / artifact 参照先

### 7.2 保護ルール

1. まず非破壊ルートを検討する
2. 共有環境の変更は高リスク扱いにする
3. 変更前に影響範囲を説明可能でなければ自動変更しない
4. 変更する場合でも元と先の両方を確認する
5. 削除や移動は最後の手段にする

### 7.3 明示的禁止

- ユーザー確認なしの CUDA 更新
- ユーザー確認なしの共通ツールチェーン更新
- venv / site-packages / model store の物理移動
- 稼働中参照先の無断置換

---

## 8. Hook による境界強制

### 8.1 Delegation Hook

見る点:
- command が責務に合っているか
- constraints に安全条件が入っているか
- Worker に実装を渡していないか
- Coder に調査だけの曖昧依頼を渡していないか

### 8.2 PreToolUse Hook

見る点:
- 実行パスが writable_workspace か
- protected_shared_environment に触れていないか
- 実行モードが safe_auto または gated_auto か
- 禁止コマンドに該当しないか
- unsandboxed 実行になっていないか

### 8.3 PostToolUse Hook

見る点:
- 実行内容が記録されたか
- 変更対象が scope 内か
- 失敗理由が取得できたか

### 8.4 Stop Hook

見る点:
- 境界違反を合理化していないか
- 検証前に完了扱いしていないか
- 共有環境変更を黙って残していないか

---

## 9. リスクレベル

### low

- 読み取り中心
- 一時領域のみ
- 共有環境非依存

対応:
- safe_auto 可

### medium

- 限定的な編集あり
- テストや build を伴う
- 差分監査必要

対応:
- gated_auto
- Hook 強化

### high

- 共有環境に近い
- 本番や既存稼働に波及する
- 外部接続や統合拡張を伴う

対応:
- manual_only または Chat にエスカレーション
- 段階分離必須

---

## 10. 失敗時の扱い

境界チェックで失敗した場合、少なくとも以下を返す。

- どの境界で止まったか
- 何が禁止条件に当たったか
- 代替の非破壊手段があるか
- Chat に戻すべきか

境界違反を検出したのに「たぶん大丈夫」で続行することは禁止する。

---

## 11. v0.1 の要点

- Chat は判断するが、自分で危険実行しない
- Worker は読むが、書き換えない
- Coder は直せるが、Sandbox と Hook の内側だけで直す
- 共有環境は自動変更しない
- ネットワークと MCP はデフォルトで閉じる
- 非破壊ルートを常に先に検討する

この 6 本を security boundary の最小原則とする。
