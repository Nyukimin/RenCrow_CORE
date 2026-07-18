# rules_path_scoped_constraints.md - Path-scoped 制約

## 目的

RenCrow の module / directory 固有制約を、常時 `AGENTS.md` へ詰め込まずに参照できるようにする。

## 共通ルール

- 対象 path を触る前に、この表の制約と該当する既存 rule / skill を確認する。
- 手順は rule に書き込まず、skill へ分離する。
- module 境界が曖昧な場合は、編集前に owning module を確定する。

## Path constraints

| 対象 | 制約 | 関連 skill / rule |
| --- | --- | --- |
| `/home/nyukimi/RenCrow` | 管理 root。git root として扱わない。build / test / commit は具体 module で実行する。 | root `AGENTS.md` |
| `RenCrow_STT/**` | STT owner。timing probe は送受信を同時に測る。audio fixture と secure context を区別する。 | `skills/core/stt-latency-debug` |
| `RenCrow_TTS/**` | TTS owner。voice asset、engine boundary、latency measurement を分ける。 | module `AGENTS.md` |
| `RenCrow_LLM/**` | local LLM owner。OpenAI-compatible endpoint、model context、provider config を runtime proof で確認する。 | module `AGENTS.md` |
| `RenCrow_CMD/**` | CLI owner。server endpoint contract と audio-file input の互換性を壊さない。 | module `AGENTS.md` |
| `RenCrow_Tools/**` | 横断 tool の正本。新規 browser sidecar、変換、検証 CLI はここを優先する。 | root `AGENTS.md` |
| `modules/stt/**` | Core runtime 側の STT integration。STT engine 本体と混同しない。 | `skills/core/stt-latency-debug` |
| `internal/adapter/viewer/**` | Viewer UI。desktop / narrow / mobile、pointer-events、z-index、固定入力バー、overlay 干渉を実ブラウザで確認する。 | `rules/rules_viewer_ui.md`, `skills/core/viewer-live-verification` |
| `rencrow-data/**` | market data workflow。snapshot_id、approval_reason、paper_trade_log、CLI audit を保つ。 | `skills/core/rencrow-data-refresh-audit` |
| `systemd/**` | live service deployment。restart 前に service stop、残 process、port、health down を確認する。 | `skills/core/rencrow-service-rebuild-restart` |
| `internal/infrastructure/persistence/**` | DB / JSONL persistence。schema、audit trail、migration、direct write boundary を確認する。 | `rules/common/rules_state_management.md` |
| `docs/README.md` | 現行正本の索引、範囲、優先順位。正本追加ではなく既存 `01` から `09` へ収束させる。 | `docs/README.md` |
| docs直下の現行仕様 | 唯一の現行製品仕様。対象はallowlistで固定し、仕様変更時は実装・test・production wiringと同時に確認する。 | `docs/README.md` |
