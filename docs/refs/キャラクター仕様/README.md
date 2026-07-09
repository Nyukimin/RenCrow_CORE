# RenCrow キャラクター仕様まとめ

このフォルダは、RenCrow のキャラクター / エージェント仕様を読みやすく集約した参照用まとめである。
正本は `docs/01_理解/02_キャラクター・エージェント仕様.md` と `docs/02_正本仕様/02_実装仕様.md` にあり、このフォルダはそれらを置き換えない。

## 一次参照

- `docs/01_理解/02_キャラクター・エージェント仕様.md`
- `docs/02_正本仕様/02_実装仕様.md`
- `docs/refs/10_新仕様/04_Chat_Worker_Coder仕様.md`
- `docs/refs/10_新仕様/26_Persona_Lore_and_Mutual_Observation仕様.md`
- `docs/refs/10_新仕様/13_実装項目インベントリ.md`
- `workspace/persona/`
- `internal/domain/agent/`

## 現行の考え方

RenCrow の現行仕様では、キャラクターは単なる口調設定ではなく、実行責務と安全境界を持つ Agent として扱う。

中核は次の 3 分割である。

| 境界 | 主なキャラクター | 主責務 |
| --- | --- | --- |
| Chat | Mio | ユーザー対話、ルーティング判断、結果返却 |
| Worker | Shiro | 実行、ツール呼び出し、patch / command 適用、ログ記録 |
| Coder | Aka / Ao / Gin / Kin | plan / patch / proposal 生成 |

Coder1-4 の対応は次で固定する。

| Coder | キャラクター | 主な用途 |
| --- | --- | --- |
| Coder1 | Aka | 仕様設計、アーキテクチャ設計、方針整理 |
| Coder2 | Ao | 実装、テストコード作成、既存コードへの適合 |
| Coder3 | Gin | 高品質推論、複雑作業、難解な実装、最適化 |
| Coder4 | Kin | 補助 Coder、レビュー、仕上げ、代替案検討 |

補助的に、深い分析用の Heavy と創作探索用の Wild がある。

| 実行枠 | キャラクター | 主責務 |
| --- | --- | --- |
| Heavy | Kuro | 深い分析、根本原因調査、リスク確認 |
| Wild | Midori | 創作、画像プロンプト、雰囲気抽出、横方向の探索 |

## 重要な注意

- Coder は破壊的操作を直接実行しない。Coder が返すのは plan / patch / proposal / risk / cost hint である。
- 実行、採用、ログ記録、安全確認は Worker が担当する。
- `Mio / Shiro / Aka / Ao / Gin / Kin` の表示・選択と persona / observation 基盤はある。
- ただし、全人格が常時会話参加する「完全なキャラ会話 runtime」は現行では未実装 / 将来実装扱いである。
- 旧キャラ体系である `ルミナ / クラリス / ノクス` は削除済み / 移行済みで、現行残課題にはしない。

## キャラクター別

- [Mio](./mio.md)
- [Shiro](./shiro.md)
- [Aka](./aka.md)
- [Ao](./ao.md)
- [Gin](./gin.md)
- [Kin](./kin.md)
- [Kuro](./kuro.md)
- [Midori](./midori.md)
