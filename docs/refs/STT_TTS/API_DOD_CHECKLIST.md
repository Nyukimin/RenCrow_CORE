# API DOD チェックリスト

本書は `API-DOD-*` のレビュー記録テンプレート。  
実装PRごとに本テンプレートをコピーして記録する。

## 1. 命名規則
- 形式: `API-DOD-<領域>-<側>-<連番>`
- 領域: `STT` / `TTS`
- 側: `C`（Client） / `S`（Server）

## 2. 実施手順
1. 対象PRで変更した API 文書を特定
2. 該当 `API-DOD-*` を列挙
3. 各IDごとに検証コマンドを実行し結果を採取
4. 各IDを `PASS/FAIL/N.A.` で判定
5. 証跡（ログ/レスポンス/PRコメントURL）を記録
6. FAIL は原因と対応予定を記録

## 3. 記録テンプレート

### 3.1 メタ情報
- PR/変更名:
- 実施日:
- 実施者:
- 対象領域: STT / TTS
- 対象側: Client / Server

### 3.2 判定（簡易）
- `API-DOD-STT-C-01`: [ ] PASS [ ] FAIL [ ] N.A.
- `API-DOD-STT-C-02`: [ ] PASS [ ] FAIL [ ] N.A.
- `API-DOD-STT-C-03`: [ ] PASS [ ] FAIL [ ] N.A.
- `API-DOD-STT-C-04`: [ ] PASS [ ] FAIL [ ] N.A.

- `API-DOD-TTS-C-01`: [ ] PASS [ ] FAIL [ ] N.A.
- `API-DOD-TTS-C-02`: [ ] PASS [ ] FAIL [ ] N.A.
- `API-DOD-TTS-C-03`: [ ] PASS [ ] FAIL [ ] N.A.
- `API-DOD-TTS-C-04`: [ ] PASS [ ] FAIL [ ] N.A.
- `API-DOD-TTS-C-05`: [ ] PASS [ ] FAIL [ ] N.A.

- `API-DOD-STT-S-01`: [ ] PASS [ ] FAIL [ ] N.A.
- `API-DOD-STT-S-02`: [ ] PASS [ ] FAIL [ ] N.A.
- `API-DOD-STT-S-03`: [ ] PASS [ ] FAIL [ ] N.A.
- `API-DOD-STT-S-04`: [ ] PASS [ ] FAIL [ ] N.A.
- `API-DOD-STT-S-05`: [ ] PASS [ ] FAIL [ ] N.A.

- `API-DOD-TTS-S-01`: [ ] PASS [ ] FAIL [ ] N.A.
- `API-DOD-TTS-S-02`: [ ] PASS [ ] FAIL [ ] N.A.
- `API-DOD-TTS-S-03`: [ ] PASS [ ] FAIL [ ] N.A.
- `API-DOD-TTS-S-04`: [ ] PASS [ ] FAIL [ ] N.A.
- `API-DOD-TTS-S-05`: [ ] PASS [ ] FAIL [ ] N.A.
- `API-DOD-TTS-S-06`: [ ] PASS [ ] FAIL [ ] N.A.

### 3.3 実行結果記録（必須）
各IDについて、最低1行を記録する。

| API-DOD ID | 判定 | 検証コマンド | 実行結果（要約） | 証跡URL/ログパス | 実施者 | 実施日時 |
|---|---|---|---|---|---|---|
| API-DOD-STT-C-01 | PASS | `wscat -c ws://.../ws` | `speech_start/final` 受信 | PRコメントURL or ログ | name | YYYY-MM-DD HH:mm |
| API-DOD-... | PASS/FAIL/N.A. | `<command>` | `<summary>` | `<url or path>` | `<name>` | `<datetime>` |

### 3.4 FAIL項目の記録
- ID:
- 失敗内容:
- 影響範囲:
- 修正方針:
- 期限:

## 4. 運用ルール
- API 文書変更時は、該当IDの判定を必ず更新する
- `N.A.` を選ぶ場合は理由を必須記載
- 仕様変更でIDを追加/削除した場合、本テンプレートも同期更新する
