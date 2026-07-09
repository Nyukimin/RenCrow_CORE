# RenCrow_CORE Ver0.80 AgentList・機能リスト整理

作成日: 2026-07-01
対象: `RenCrow_CORE` / RenCrow_CORE Ver0.80 構築判断
目的: AgentList と機能リストを、人格 Agent、補助ロール、機能単位へ分けて整理する。

## 位置づけ

この文書は Ver0.80 構築前の整理メモである。AgentList 本体の一次参照は `docs/01_理解/02_キャラクター・エージェント仕様.md` とする。

実装仕様側に古い 5 Agent 構成が残る場合でも、この整理では次の 8 体を現行 AgentList 本体として扱う。

## AgentList 本体

| Agent | 役割 | 主責務 | 備考 |
| --- | --- | --- | --- |
| Mio | Chat | ユーザー対話、ルーティング判断、結果返却、統合 | LINE / Viewer の会話窓口 |
| Shiro | Worker | 実行、ツール呼び出し、patch / command 適用、ログ記録 | 実行主体 |
| Aka | Coder1 | 仕様設計、アーキテクチャ設計、方針整理 | 直接実行しない |
| Ao | Coder2 | 実装、テストコード作成、既存コードへの適合 | 直接実行しない |
| Gin | Coder3 | 高品質推論、複雑作業、難解な実装、最適化 | 直接実行しない |
| Kin | Coder4 | 補助 Coder、レビュー、仕上げ、代替案検討 | 直接実行しない |
| Kuro | Heavy | 深い分析、根本原因調査、最終技術レビュー、安全ゲート | Heavy 枠 |
| Midori | Wild | 創作、画像プロンプト、視覚解釈、横方向探索 | Wild 枠 |

## AgentList 補助欄

次は人格 Agent ではない。AgentList 本体に混ぜず、運用・実行・統括ロールとして別枠に置く。

| 補助ロール | 分類 | 扱い |
| --- | --- | --- |
| SuperAgent / LeadAgent | 統括実行 | 長い作業や複数タスクの統括。人格 Agent ではない。 |
| Subagent / ResearchAgent 系 | 分担実行 | 調査や部分作業の分担単位。人格 Agent ではない。 |
| Heartbeat Worker | 定期実行 | Backlog / Workstream / Revenue などの定期処理を起動する。 |
| BrowserActor | ブラウザ操作 | Web 操作・ブラウザ実行系の tool agent。 |
| Distributed remote agent / `rencrow-agent` | 分散実行 | remote worker / coder 実行単位。 |
| ChatWorker provider role | provider alias | Shiro / Worker 系の短文応答や IdleChat 向け alias。 |
| ToolHarness / DCI 実行ロール | tool mediation | tool contract、直接コーパス探索、実行境界を扱う。 |

## 機能リスト

Ver0.80 では、UI タブ名や Go package 名をそのまま機能境界にしない。ユーザー価値と実行責務で機能単位を切る。

| 領域 | 機能 | Ver0.80 での扱い |
| --- | --- | --- |
| Chat | 通常 Chat / route 解決 / 最終返答 | Mio / Chat 機能に閉じる。Viewer は route alias 変換を持たない。 |
| Agent | AgentList / Character Runtime / Persona | 8 体の正本対応を固定し、補助ロールと分ける。 |
| Worker / Coder | Worker 実行 / Coder1-4 / patch・command 適用 | Shiro 実行境界と Coder 非実行境界を分離する。 |
| Heavy / Wild | Kuro / Midori / deep analysis / creative exploration | Chat の相手としての経路と、Heavy / Wild 実行枠を混同しない。 |
| IdleChat | IdleChat / story / topic / stop / ChatWorker | 通常 Chat とは別機能として閉じる。 |
| Backlog | Backlog / Backlog Runner | Viewer 表示、JSONL store、Heartbeat 連携をひとまとまりにする。 |
| Heartbeat | Heartbeat / Workstream Heartbeat / Viewer active-control heartbeat / SSE heartbeat | 定期運用 heartbeat と Viewer 通信 heartbeat を区別する。 |
| Scheduler | scheduler / idle jobs / due jobs | Backlog や Workstream と連動するが、起動判定は別機能にする。 |
| Workstream | Workstream / Vault update / Steering | Heartbeat から起動される運用ループとして扱う。 |
| Revenue | Revenue daily routine | Workstream 連携の収益運用機能として扱う。 |
| Repair | Autonomous repair / self-repair | Chat 経路とは別の out-of-band repair plane として扱う。 |
| Attachment | Viewer attachment input / attachment pipeline | Chat 入力補助として分離する。 |
| Voice | VoiceChat / VDS / AudioRouter | STT / TTS と別に、音声 Chat 経路として整理する。 |
| STT | STT Viewer input / streaming / finalizer | TTS と混ぜず、音声入力機能として閉じる。 |
| TTS | TTS playback / timeout / drain / Viewer sync | STT と混ぜず、音声出力機能として閉じる。 |
| Avatar | VTuber / Live2D / Character Runtime 表示 | Viewer 表示・感情同期と runtime state を分ける。 |
| Image | ComfyUI / image generation | Midori / Wild から使われる創作系 tool 機能として扱う。 |
| Web | BrowserActor / WebGather / Webwright fetch | discovery、source read、browser evidence を分ける。 |
| Browser Trace | BrowserTrace / BrowserTrace-to-API | Web 操作記録と API 化候補抽出を分ける。 |
| Source | Source Registry / source fetcher | 外部情報の保存境界と review 境界を持つ。 |
| Knowledge | Knowledge import / wiki index / vocabulary / glossary | 記憶・知識検索とは別に、知識資産管理として扱う。 |
| Reports | Evidence / verification / reports | QA evidence、実行結果、検証結果を集約する。 |
| Ops | Health / doctor / status / LLM Ops / local LLM runtime | 運用確認と LLM 起動・切替管理をまとめる。 |
| Distributed | Distributed execution / transport / remote worker | `rencrow-agent` と transport を分散実行機能として扱う。 |
| Security | Security / sandbox / capability registry | 実行可否、権限、promotion gate を扱う。 |
| Registry | Module registry / capability registry | Ver0.80 の機能境界の登録・参照点にする。 |
| Governance | Skill governance / package validation | Agent skill と package の安全性・整合性を扱う。 |
| Maintenance | Artifact cleanup / History repair / Extension health / OTEL export | 運用保守機能として、主機能から分ける。 |
| Channels | LINE / Telegram / Discord / Slack | 外部入口 adapter として Chat 本体から分ける。 |

## 正規化ルール

- AgentList 本体は人格 Agent 8 体に限定する。
- 補助ロールは AgentList 本体に混ぜず、運用・実行・統括ロールとして表に出す。
- Heartbeat は 1 機能名で済ませず、定期運用、Workstream、Viewer active-control、SSE を区別する。
- BrowserActor / WebGather / Webwright fetch は Web 系として近いが、同一機能に潰さない。
- STT / TTS / VoiceChat / AudioRouter / VDS は音声領域にまとめつつ、入力、出力、会話経路、ルーティングを分ける。
- Ver0.80 のフォルダ境界は UI タブ名ではなく、責務境界と依存方向で決める。

## 追加で直すべきズレ

| 対象 | 状態 | 対応 |
| --- | --- | --- |
| `docs/02_正本仕様/02_実装仕様.md` | 古い 5 Agent 構成の記述が残る | Kin / Kuro / Midori を含む 8 Agent 構成へ更新候補にする。 |
| `internal/adapter/viewer/assets/js/viewer.js` | Coder1-4 の表示名整理コミット由来の揺れが残る可能性がある | 実装変更前に Viewer 表示、role target、runtime config を照合する。 |
| `docs/refs/キャラクター仕様/` | 参照用まとめに Coder 対応の古い揺れがあった | この整理で `Aka/Coder1`, `Ao/Coder2`, `Gin/Coder3`, `Kin/Coder4` へ補正した。 |
