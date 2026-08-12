# RenCrow_CORE 現行正本

この README と `01` から `10` の仕様書は、`main` における RenCrow_CORE の唯一の現行正本です。利用者、連携モジュール開発者、contributor が同じ現在仕様を参照できるよう、採用済みの製品契約だけを置きます。

## 読む順番

1. [システム概要](01_システム概要.md)
2. [機能仕様](02_機能仕様.md)
3. [キャラクター・エージェント仕様](03_キャラクター・エージェント仕様.md)
4. [アーキテクチャ概要](04_アーキテクチャ概要.md)
5. [設定リファレンス](05_設定リファレンス.md)
6. [Public API 仕様](06_Public_API仕様.md)
7. [安全・自動実行・データ方針](07_安全・自動実行・データ方針.md)
8. [実装状況・ロードマップ](08_実装状況・ロードマップ.md)
9. [運用ログ・panic保存仕様](09_運用ログ・panic保存仕様.md)
10. [ログ仕様](10_ログ仕様.md)

## 文書の位置づけ

- `main` のこの11ファイルだけを、Public向けか内部向けかを問わない現行の製品仕様正本とします。
- `AGENTS.md`、`CLAUDE.md`、`rules/` は作業者向けの実行制約です。製品仕様を再定義せず、この正本を参照します。
- COREと兄弟モジュールの連携境界、入出力の意味、責務分担はこの正本で定義します。兄弟モジュールのdocsは各モジュール内部の実装詳細を補足できますが、この正本を上書きしません。差異がある場合はこの正本を基準に兄弟モジュール側を更新します。
- 全moduleは[システム概要のNo-Human-Gate](01_システム概要.md#no-human-gateとreject再考)と
  [安全方針のReject-Driven Revision](07_安全・自動実行・データ方針.md#reject-driven-revision)に従います。
  人の判断待ちを製品workflowへ追加せず、reject時は前提・設計・思想を再考した新revisionを検証します。
- 標準Go配布、Ubuntu／Windows／macOS共通契約、外部system、optional sidecar、
  Python／Node.js依存、CUDA用WSLの扱いは
  [アーキテクチャ概要の「標準Go配布境界」](04_アーキテクチャ概要.md#標準go配布境界)を正本とします。
- Character SystemPrompt、Stable RuntimeContext、RecallPack、Variable RuntimeContext、
  User Messageの責務、順序、AI使用境界、KVキャッシュ、Viewer表示、必須検証は
  [アーキテクチャ概要の「Prompt Context Assembly」](04_アーキテクチャ概要.md#prompt-context-assembly)を
  唯一の現行正本とし、実装、ログ、test、兄弟moduleのdocsで別構造を再定義しません。
- Durable Data Storeの媒体role、mount、module別subtree、CORE DB path、追加判断主体、impact class、
  Storage Proposal、Manifest、lifecycle、backup／restore契約は
  [設定リファレンスの「DB物理配置とbackup」](05_設定リファレンス.md#db物理配置とbackup)を
  唯一の現行正本とします。兄弟moduleのREADMEは所有subtreeと設定入口だけを補足します。
- 実装、production wiring、test、config は現在状態を確認する証拠です。正本と差異が見つかった場合は事実を照合し、採用する契約をこの正本へ反映してから実装を合わせます。
- 実装済み、未実装、deployment依存を区別します。
- 現行正本に必要な情報が不足している場合は、該当する `01` から `10` の文書を更新します。別の正本ディレクトリ、版付き正本、補助正本を追加しません。

最終整理日: 2026-08-12
