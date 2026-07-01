# rules_testing.md - テスト・品質保証ルール

**作成日**: 2025-12-11  
**最終更新**: 2025-12-11  
**バージョン**: 1.0  
**目的**: すべてのプロジェクトで共通して利用できるテスト・品質保証の詳細ルールを定義する  
**適用範囲**: 自動テスト／手動テスト／E2E テストを実行するすべてのプロジェクト

---

## 1. 基本方針

- GLOBAL_AGENT の方針に従い、ここではテスト運用の「具体レシピ」を定義する
- テストの目的は、「仕様の確認」と「変更時の安全網の確保」である
- テストコードは仕様書の一部として扱い、可読性を重視する

---

## 2. TDD（テスト駆動開発）

### 2.1 Red-Green-Refactor サイクル

1. Red  
   - まず「失敗するテスト」を書く
   - テストが失敗することを確認してから実装に入る

2. Green  
   - テストが通る「最小限の実装」を書く
   - 仕様を満たす範囲で、過度な設計は行わない

3. Refactor  
   - テストがすべて通っている状態を保ちながら、設計・命名・構造を改善する

### 2.2 実装例（Python）

```python
# tests/unit/test_calculator.py

def test_calculate_result_with_valid_input():
    calc = Calculator()
    result = calc.calculate(input_data)
    assert result["status"] == "success"


# src/calculator.py

class Calculator:
    def calculate(self, data: dict) -> dict:
        # Green フェーズでは最小限の実装
        return {"status": "success"}
```

---

## 3. テストの原則

### 3.1 FIRST 原則

- **Fast**: 高速に実行できること
- **Independent**: 他のテストに依存しないこと
- **Repeatable**: どの環境でも再現可能であること
- **Self-validating**: 成否が明確に判断できること
- **Timely**: 実装と同時、または前に書かれていること

### 3.2 AAA パターン

```python
def test_example():
    # Arrange: 準備
    calculator = Calculator()
    input_data = prepare_test_data()

    # Act: 実行
    result = calculator.process(input_data)

    # Assert: 検証
    assert result is not None
    assert result["status"] == "success"
```

---

## 4. テスト構成と命名規則

### 4.1 ディレクトリ構成

```text
tests/
├── conftest.py          # 共通フィクスチャ
├── unit/                # ユニットテスト
│   ├── test_module1.py
│   └── test_module2.py
├── integration/         # 統合テスト
│   └── test_workflow.py
└── e2e/                 # E2E テスト
    └── test_scenarios.py
```

### 4.2 命名規則

- テストファイル: `test_<モジュール名>.py` / `test_<module>.ts`
  - 例: `test_calculator.py`, `test_userService.ts`
- テスト関数: `test_<機能名>_<条件>`
  - 例: `test_calculate_result_with_valid_input`
- テストクラス（必要な場合）: `Test<対象クラス名>`

---

## 5. カバレッジとテストの範囲

### 5.1 カバレッジ目標（目安）

| 対象             | 目標 |
|------------------|------|
| 全体             | 80%  |
| ビジネスロジック | 90%  |
| ユーティリティ   | 70%  |

- カバレッジは「質より量」ではなく、「重要な分岐が守られているか」を優先して評価する
- 目標に届かない場合でも、どの部分が未カバーかを明示する

### 5.2 テスト対象の優先度

1. 重要なビジネスロジック
2. 外部 API との連携部分
3. 状態変化を伴う処理
4. ユーティリティ関数

---

## 6. テストフレームワーク

### 6.1 推奨ツール

- **Python**
  - テスト: `pytest`
  - カバレッジ: `coverage.py` / `pytest-cov`
- **JavaScript / TypeScript**
  - テスト: `Jest`
  - E2E: `Playwright` / `Puppeteer` / `Cypress`
- **Go**
  - テスト: `testing` パッケージ（標準）
- **Bash**
  - テスト: `bats`（Bash Automated Testing System）

---

## 7. E2E テスト

### 7.1 実施原則

- 新機能のリリース前には、対象機能に対応する E2E テストを最低 1 本用意する
- クリティカルなユーザーフロー（ログイン、決済など）は必ず E2E テストで担保する
- E2E テストは「網羅」ではなく、「代表的なシナリオ」を重視する

### 7.2 検証ポイント

- 正常系の動作（Happy Path）
- 代表的な異常系（入力エラー、タイムアウト等）
- エッジケース（境界値、大量データ）
- 必要に応じてパフォーマンス（レスポンスタイムなど）

---

## 8. ブラウザ操作テスト（Selenium / Playwright 等）

### 8.1 テスト実行前のクリーンアップ

ブラウザ操作を含むテストでは、既存プロセスが干渉することを防ぐため、テスト前にクリーンアップを行う。

```bash
# macOS / Linux
pkill -f chrome || true
pkill -f chromium || true

# Windows
taskkill /F /IM chrome.exe 2>nul || true
```

- CI/CD 上でも同様のクリーンアップを推奨する
- ポート競合や既存セッションの影響を避けることが目的

---

## 9. Lint とテストの関係

- コミット前には、最低限以下を実施することを推奨する：
  - Lint 実行（言語別のルールに従う）
  - 関連テストの実行（ユニットテストまたは対象範囲のテスト）
- CI では、Lint とテストを自動実行し、失敗時はマージをブロックする

---

## 10. プロジェクト固有ルール

- 各プロジェクトの `PROJECT_AGENT.md` または `rules_domain.md` で、
  - カバレッジの最低ライン
  - 必須とするテスト種別（ユニット／統合／E2E）
  - 使用するテストフレームワーク
  を上書き・追加定義できるものとする。
