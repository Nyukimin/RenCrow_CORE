# Codebase Complexity Hotspot

## Purpose

コードベースを調査し、計算量、繰り返し lookup、N+1、render 負荷などの複雑性ホットスポットを発見し、Report-only で改善候補を提示する。

この Skill は最適化 patch を自動適用するためのものではない。証拠、リスク、必要テスト、次アクションを整理し、修正する場合は別 Goal と Human approval に分離する。

## When to Use

- ユーザーが重い箇所や複雑な箇所の調査を依頼した。
- O(n^2)、N+1、繰り返し scan / lookup の疑いがある。
- UI render、API request path、batch path の負荷が疑われる。
- 安全に最適化できる候補を先に棚卸ししたい。

## When Not to Use

- 具体的なバグ修正が目的で、hotspot 調査ではない。
- 小さな文言修正、UI デザイン調整、単純な docs 変更。
- 本番負荷試験や外部 API 負荷試験が必要。
- すでに修正対象 hotspot が決まっており、実装 Goal が別に存在する。

## Required Inputs

- scan scope
- 対象外ディレクトリ
- 既知の hot path またはユーザー観測
- 利用可能なテストまたは代替確認手順

## Procedure

1. Project Init Pack または現行構成を確認する。
2. scan scope を決め、generated / vendored / build artifact を除外する。
3. ループ、lookup、DB/API 呼び出し、render path、serialization、regex を探索する。
4. hotspot 候補を Evidence 付きで抽出する。
5. before / after の複雑性を推定する。
6. risk level を low / medium / high に分類する。
7. 必要テストを列挙する。
8. 変更は行わず、Complexity Hotspot Report を出す。

## Output Format

`report_template.md` の `Complexity Hotspot Report` 形式に従う。

## Safety / Stop Conditions

- デフォルトではコードを変更しない。
- ベンチマークなしに高速化を断言しない。
- 高リスク改善は提案のみとする。
- 修正する場合は 1 hotspot = 1 Goal = 1 patch proposal に分離する。
- cache、DB query rewrite、concurrency、認証、課金、削除処理は Human approval 必須。
