# rules_domain.md - RenCrow_CORE ドメイン実装ルール

**最終更新**: 2026-07-18

## 0. このファイルの役割

このファイルはRenCrow_CORE固有の実装・運用上の注意を定義します。製品仕様の正本ではありません。

製品の所有範囲、機能、agent、architecture、config、API、安全、実装状況、運用契約は `docs/README.md` から該当する現行正本を参照します。一般的なGo、test、logging、state、securityは `rules/common/` に従います。

## 1. 外部LLM境界

- COREはrole、agent、request context、response、health projectionを扱う。
- backendごとのmodel process、KV session、sampling、management runtimeはRenCrow_LLM側の責務とする。
- Ollama、llama.cpp、MLXなど特定backendの常駐方法、context上限、management commandをCOREの固定要件にしない。
- inference endpointとmanagement endpointを混同しない。
- configで選択したroleやruntimeが利用不能な場合、別roleへ黙ってfallbackしない。
- model固有のsecretをsource、sample config、test、logへ保存しない。

製品境界は `docs/01_システム概要.md`、`docs/04_アーキテクチャ概要.md`、設定は `docs/05_設定リファレンス.md` に従います。

## 2. routingとagent

- 通常入力のroute owner、明示commandの優先、agentの責務は現行正本に従う。
- Shiroとの会話とShiro/Workerのside effect実行を同一視しない。
- Coder、Advisor、Toolをside effectの最終責任者にしない。
- agent間移譲の会話eventを内部logだけで代替しない。
- route、agent、runtime、modelを同じ識別子として扱わない。

製品契約は `docs/02_機能仕様.md` と `docs/03_キャラクター・エージェント仕様.md`、AI作業時の振り分けは `rules/routing-policy.md` に従います。

## 3. conversation、Memory、storage

- conversation、episode recall、確定Memory、Knowledge検索を区別する。
- recall結果をagentの偽発言としてhistoryへ挿入しない。
- stateにはowner、寿命、永続化先、再構築元を定める。
- runtime storageを過去資料や特定toolの都合で置き換えない。
- migrationはschema、audit、rollback、既存dataの互換性を確認する。

製品契約は `docs/02_機能仕様.md`、storage構成は `docs/04_アーキテクチャ概要.md`、実装制約は `rules/common/rules_state_management.md` に従います。

## 4. PORTAL、Debug Viewer、音声

- RenCrow_PORTALは外部向けview/live/lab、COREはDebug Viewerを所有する。
- PORTALからDebug、Ops、Repair、LLM管理、設定変更APIを透過公開しない。
- 表示本文、TTS音声生成、音声取得、browser再生、口パクを別の成功条件として扱う。
- STT録音状態と認識結果の投入先を分離する。
- recipient、TTS、STTなどclient-localの選択をCOREのglobal stateへ誤昇格しない。

製品契約は `docs/02_機能仕様.md`、`docs/04_アーキテクチャ概要.md`、`docs/06_Public_API仕様.md` に従います。

## 5. health、restart、logging

- `/health/live`はCORE processのliveness確認に使う。
- `/health`は依存を含む総合状態、`/ready`は受付可能状態として分ける。
- 外部LLM、STT、TTSの停止だけを理由にCOREを再起動しない。
- panicやhang後のrestartをCORE process自身だけへ依存させない。
- incident、panic stack、repair、retentionの状態を別々の一時logへ散らさない。

endpoint契約は `docs/06_Public_API仕様.md`、再起動・自己修復・7日保持は `docs/09_運用ログ・panic保存仕様.md` に従います。

## 6. 実機確認

Viewer、IdleChat、STT、TTS、external runtimeを含む変更はtest通過だけで完了扱いしません。

- 1 session以上の開始から終了まで追う。
- 表示、event、log、network、最終stateを照合する。
- desktopとnarrow/mobileを実ブラウザで確認する。
- backend health、実request、生成成功、取得成功、再生成功を分ける。
- unavailable、pending、degraded、errorを空の成功へ丸めない。

詳細は `rules/common/rules_observation_verification.md` と `rules/common/rules_testing.md` に従います。

## 7. 言語とtool所有

- CORE runtimeと恒久的なCORE運用CLIはGoを第一候補にする。
- Go versionとmodule pathは `go.mod` を確認する。
- Viewerのbrowser E2Eはrepository管理されたNode.js/Playwrightを優先する。
- Pythonは音声・data・解析・一時調査の補助に限定し、恒久runtimeへ採用する場合はmodule境界を再確認する。
- 横断的に再利用するtool、browser sidecar、converter、validation CLIはRenCrow_Toolsへ置く。
- `RenCrow_CORE/tools/`へ新しい横断toolを追加しない。

## 8. 更新ルール

- 製品契約が変わる場合は、先に `docs/README.md` に列挙された該当正本を更新する。
- このファイルにはCORE固有の実装上の注意だけを置く。
- 一般化できる内容は `rules/common/`、反復手順はskill、機械的安全柵はhooks/permissionsへ置く。
