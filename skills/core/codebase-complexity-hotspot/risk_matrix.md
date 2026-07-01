# Risk Matrix

## Low

- 純粋計算の重複削減
- `Map` / `Set` による単純 lookup 化
- 既存テストで順序、重複、欠損が覆われている

## Medium

- 処理順序に意味がある可能性
- 重複 key の扱いが変わる可能性
- `null` / `undefined` の扱いが変わる可能性
- UI render 結果に影響する可能性
- 非同期処理順序が変わる可能性

## High

- DB query の構造変更
- API 呼び出し順序の変更
- cache 導入
- 並列化
- 状態管理変更
- 認証、課金、決済、削除処理
- エラーハンドリングの挙動変更

High risk は Report-only に留め、修正する場合は別 Goal Contract と Human approval を要求する。
