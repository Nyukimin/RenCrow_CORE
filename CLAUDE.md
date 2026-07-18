# CLAUDE.md - RenCrow_CORE 作業案内

**最終更新**: 2026-07-18

## このファイルの役割

このファイルは、Claude Codeまたは同等のAI開発環境がRenCrow_COREで作業するための短い入口です。製品仕様の正本ではありません。

製品仕様の唯一の正本は `docs/README.md` と、そこに列挙された9つの仕様書です。仕様、実装、設定、運用の判断でこのファイルと現行正本が衝突した場合は、現行正本を優先します。

`AGENTS.md` と `rules/` は作業者向けの実行制約です。製品仕様を再定義せず、現行正本の契約を安全に実装・検証するために使います。

## 読む順番

1. `AGENTS.md`
2. `docs/README.md`
3. 対象領域に対応する現行正本
4. 対象作業に関係する `rules/`
5. Viewerや見た目の作業では `DESIGN.md`
6. 関連コード、test、production wiring、config

## 現行正本

| 文書 | 主な対象 |
| --- | --- |
| `docs/01_システム概要.md` | COREの目的、所有範囲、外部module境界 |
| `docs/02_機能仕様.md` | 会話、routing、Memory、IdleChat、PORTAL、音声 |
| `docs/03_キャラクター・エージェント仕様.md` | Mio、Shiro、Kuro、Midori、Coder、Advisor、Tool |
| `docs/04_アーキテクチャ概要.md` | module、UI、code、依存、storage境界 |
| `docs/05_設定リファレンス.md` | config読込、主要section、外部endpoint |
| `docs/06_Public_API仕様.md` | health、Viewer、PORTAL、client契約 |
| `docs/07_安全・承認・データ方針.md` | approval、secret、Memory、degraded state |
| `docs/08_実装状況・ロードマップ.md` | 実装済み、未実装、deployment依存、旧構想 |
| `docs/09_運用ログ・panic保存仕様.md` | logging、panic、restart、self-repair、retention |

## 作業上の不変条件

- archive branch、Knowledge、削除済みdocs、版付き旧仕様を現行正本にしない。
- 現行正本に不足がある場合は、実装・test・production wiringを照合し、`docs/README.md`に列挙された該当文書を更新する。別の正本を作らない。
- CORE、PORTAL、CMD、LLM、STT、TTSなどのmodule所有境界は現行正本に従う。
- providerやbackend固有のmodel、context、常駐、management方式をCOREの固定要件にしない。
- Goのversionとmodule pathは `go.mod`、公開設定例は `config/config.yaml.example` を実ファイルとして確認する。
- secretはsource、docs、log、trace、artifactへ保存しない。
- side effect、approval、degraded state、healthの意味を黙って変更しない。
- docsだけの変更でも、索引、相対link、CI guard、旧正本参照がないことを確認する。

## 作業ルールの入口

- `rules/PROJECT_AGENT.md`: RenCrow_CORE固有の実装手順
- `rules/routing-policy.md`: routing判断の実務ルール
- `rules/rules_viewer_ui.md`: Debug ViewerのUI実務ルール
- `rules/rules_instruction_placement.md`: 指示の配置先
- `rules/rules_path_scoped_constraints.md`: path固有制約
- `rules/rules_search_browse_evidence.md`: 外部調査の証拠分離
- `rules/rules_domain.md`: CORE固有の実装・運用補足
- `rules/common/`: architecture、backend、frontend、security、state、test、loggingの共通制約

`TOOL_CONTRACT.md` はtool実装の契約、`DESIGN.md` は視覚方針です。どちらも製品仕様の現行正本を置き換えません。

## 基本確認

```bash
git status --short --branch
grep '^module\|^go ' go.mod
go test ./...
go build ./...
```

対象がViewer、service、外部runtimeを含む場合は、unit testだけで完了扱いせず、`AGENTS.md`と関連rulesに従って実runtime・実ブラウザ・healthを確認します。
