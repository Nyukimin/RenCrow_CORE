# Reference Index

## High-Value References

| category | path | read when |
|---|---|---|
| 承認フロー / Coder3 | `05_LLM運用プロンプト設計/Coder3_Claude_API仕様.md` | 承認フロー存廃、Coder3 / Gin / Claude API 詳細を確認するとき |
| LLM 常駐管理 | `05_LLM運用プロンプト設計/LLM_Ollama常駐管理.md` | RenCrow_LLM の常駐、起動、切替、モデル運用を確認するとき |
| LLM 分類 | `05_LLM運用プロンプト設計/LLM_分類整理.md` | Kin / CODE4 や route 分類の履歴を確認するとき |
| runbook | `06_実装ガイド進行管理/` | 過去作業手順や実装経緯を確認するとき |
| IdleChat 通常 | `07_IdleChat仕様/IdleChat仕様.md` | IdleChat 通常モードを実装・修正するとき |
| IdleChat 停止 | `07_IdleChat仕様/IdleChat即時停止仕様.md` | ユーザー操作による中断、停止、割り込みを確認するとき |
| IdleChat ID | `07_IdleChat仕様/会話ID仕様.md` | session_id / message_id / turn_index 境界を確認するとき |
| STT/TTS | `STT_TTS/README.md` | 音声入出力の入口を確認するとき |
| STT streaming | `STT_TTS/STT_Streaming_Client仕様.md` | STT streaming client 契約を確認するとき |
| TTS | `STT_TTS/RenCrow_TTS_仕様.md` | TTS endpoint、Viewer 同期、音声出力を確認するとき |
| codebase summary | `codebase-map/RUN_SUMMARY.md` | 実装と仕様の乖離や解析結果を確認するとき |
| codebase modules | `codebase-map/modules/` | 影響範囲を module 単位で確認するとき |
| new specs overview | `10_新仕様/00_README.md` | 新仕様フォルダ全体の位置づけを確認するとき |
| To-Be canonical specs | `../02_正本仕様/10_RenCrow_ToBe_統合仕様.md` | Advisor、Knowledge Relation、Economic Objective、Ops の正本仕様を確認するとき |
| Chat / Worker / Coder | `10_新仕様/04_Chat_Worker_Coder仕様.md` | Agent 境界、CODE1-4、Shiro 経由実行を確認するとき |
| Runtime topology | `10_新仕様/90_Runtime_Topology_Config仕様.md` | config.yaml を topology map として扱う判断を確認するとき |
| Viewer | `09_Viewer/Viewer仕様.md` | Viewer UI / 表示 / 入力の詳細を確認するとき |
| Memory | `memory/RenCrow_memory_system_implementation_spec.md` | memory system の実装詳細を確認するとき |
| Refactor | `refactor/リファクタリング指針.md` | リファクタリング判断を確認するとき |

## Do Not Treat As Entry

次の種類は reference に含まれていても、通常入口や正本として扱わない。

- 日付付き runbook / 作業ログ。
- 採用状態が不明な AI 提案。
- tmp / old / archive 配下の資料。
- 実装確認なしの古い設計文書。

必要な場合は `docs/00_引き継ぎ/` の DOC チケットに根拠と期限を記録してから使う。
