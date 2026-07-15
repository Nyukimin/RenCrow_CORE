# rules_testing.md - テスト・品質保証ルール

**作成日**: 2025-12-11
**最終更新**: 2026-07-15
**バージョン**: 2.0
**目的**: TDD と E2E の適用条件、実行境界、完了判定を定義する
**適用範囲**: RenCrow_CORE のコード、設定、API、Viewer、runtime を変更するすべての作業

---

## 1. 基本方針

- GLOBAL_AGENT の方針に従い、ここではテスト運用の「具体レシピ」を定義する
- テストの目的は、「仕様の確認」と「変更時の安全網の確保」である
- テストコードは仕様書の一部として扱い、可読性を重視する
- テスト層は変更箇所ではなく、壊れる可能性がある利用者フローから決める
- テスト未実施、失敗、環境不足を「完了」と報告しない

### 1.1 適用判断

| 変更 | TDD | 統合・契約テスト | E2E |
| --- | --- | --- | --- |
| 純粋関数、domain、変換、validation | 必須 | 影響時 | 原則不要 |
| handler、API、DB、adapter、config | 必須 | 必須 | 利用者フローへ影響する場合は必須 |
| Viewer HTML / CSS / JavaScript / route | 必須 | API 契約を確認 | 実ブラウザで必須 |
| 起動、service、WebSocket、stream、STT/TTS、外部連携 | 必須 | 必須 | 実runtime経路で必須 |
| refactor、依存更新、migration | characterization test を先に追加 | 必須 | 既存フローへの影響時は必須 |
| docs・コメントだけの変更 | 不要 | 不要 | 不要。ただし link / format / index を確認 |

迷った場合は一段上のテスト層を選ぶ。局所テストだけでは利用者観測を再現できない変更は E2E 対象とする。

---

## 2. TDD（テスト駆動開発）

### 2.1 Red-Green-Refactor サイクル

1. Red  
   - 受入条件と壊れ方を先に定義し、「失敗するテスト」を書く
   - 対象実装を変更する前にテストを実行し、期待した理由で失敗することを確認する
   - compile error、fixture 不備、環境障害による失敗を Red の証拠にしない

2. Green  
   - テストが通る「最小限の実装」を書く
   - 仕様を満たす範囲で、過度な設計は行わない
   - 新しい要件を満たすために既存 assertion を弱めない

3. Refactor  
   - テストがすべて通っている状態を保ちながら、設計・命名・構造を改善する
   - focused test に加え、関連 package / module の回帰テストを再実行する

### 2.2 TDD を成立させる証跡

- Red で実行した test 名、command、期待した失敗理由
- Green 後の同一 test の成功
- 関連 test、lint、type check、build の結果
- バグ修正では、修正前の症状を固定する回帰テスト

実装前に Red を確認していない作業を、後からテストを追加しただけで「TDD」と呼ばない。

### 2.3 自動テストを書きにくい場合

- まず seam、fake、fixture、temporary store で自動化できないか確認する
- legacy code は characterization test で現在挙動を固定してから変更する
- 自動化できない場合は、実装前に理由、代替手順、期待結果、証跡保存先を明文化する
- 代替検証しかできない変更は、未自動化の残リスクを報告する

### 2.4 実装例（Python）

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

- E2E は unit / integration の代替ではなく、実際の境界接続と利用者フローを確認する最終層とする
- E2E 適用対象の新機能、仕様変更、回帰修正には、変更を通るシナリオを最低 1 本追加または更新する
- 新規・変更した E2E は、修正前または制御した fault で assertion が失敗することを確認し、緑のまま追加しただけにしない
- Viewer 変更は DOM 存在確認で終えず、実ブラウザの描画、操作、network、console、最終状態を確認する
- 起動、設定、DB、API、WebSocket、stream、外部 adapter をまたぐ変更は、実runtime経路を確認する
- E2E が失敗、未実施、または環境不足の場合は完了扱いしない。理由と未検証範囲を明示する

### 7.2 リスクベースのシナリオ行列

変更に関係する行だけを選び、選ばなかった理由を説明できるようにする。

| 観点 | 必須になる条件 |
| --- | --- |
| happy path | すべての機能変更 |
| empty / unavailable | optional store、未設定backend、初期状態を扱う |
| error / timeout / malformed response | network、外部API、非同期処理を扱う |
| boundary / long data | limit、pagination、長いID・本文、件数上限を扱う |
| refresh / reload / persistence | DB、cache、session、永続状態を扱う |
| desktop / narrow mobile | Viewer の layout、操作、表示を変える |
| cross-browser | 新規または重要な Viewer フロー、browser API、CSS差異へ触れる |
| non-interference | fixed UI、overlay、既存service、共有port・DBへ影響し得る |

正常系を mock だけで通してはならない。fault injection は異常系契約に限定し、実backendシナリオとレポート上で分離する。

### 7.3 検証ポイント

- 利用者が行う操作と、その結果として見える最終状態
- 対象 API / event / store が実際に通ったこと
- console error、page error、予期しない request failure がないこと
- 画面変更では desktop と narrow / mobile の表示、クリック到達性、横あふれ、固定UIとの非干渉
- refresh / reload 後にも仕様上維持すべき状態が残ること
- screenshot、report、tracker、実行ログなど再確認可能な証跡

「画面が開いた」「要素が存在した」「health が 200」だけでは E2E 合格にしない。

### 7.4 効率的な実行段階

- Red / Green 中は focused unit・integration と、対象browser / viewport 1 組の focused E2E を優先する
- 完了前は適用判断表とシナリオ行列で選んだ全ケースを実行する
- release / 大規模変更では、既存の重要フローを含む full E2E suite を実行する
- 組合せ爆発は unit / integration で担保し、E2E は境界接続と利用者フローに絞る
- build、fixture seed、server起動は安全な範囲で共有し、assertionごとの再起動を避ける

---

## 8. ブラウザ操作テスト（Selenium / Playwright 等）

### 8.1 RenCrow Viewer の標準

- Node.js Playwright を第一候補とし、依存は `package.json` / lock file で管理する
- 現在のsourceからbuildしたserverと、Playwright管理browserを使う
- 最低限 Chromium で確認する。新規・重要フローは Firefox も確認する
- Viewer変更は desktop と narrow / mobile の両方を確認する
- 正常系は実 HTTP API と実 store / fixture を使う

### 8.2 隔離と安全

- 実行ごとに空きport、temporary HOME、workspace、session、DB、log、outputを分離する
- 既存の `rencrow.service`、利用者browser、共有port、本番DBを停止・削除・上書きしない
- browser context と子processは成功・失敗を問わず終了処理する
- 外部送信、課金、production write は fake / local server へ差し替え、実行しない
- broad `pkill` を cleanup に使わない。テスト自身が起動した PID / process group だけを停止する

### 8.3 flaky test

- 失敗を無制限 retry で隠さない
- 同一条件の再実行は原因切り分け目的で最大 2 回までとする
- retry でだけ成功した場合は flaky と記録し、安定化するまで完了判定に使わない
- fixed sleep より、観測可能な状態・event・responseを待つ

---

## 9. Lint とテストの関係

- コミット前には、最低限以下を必須とする：
  - format / lint / type check / build のうち対象言語に存在するもの
  - focused test と関連 package / module test
  - 適用判断表で必要になった integration / E2E
  - `git diff --check` と生成物・secret の混入確認
- CI では、Lint とテストを自動実行し、失敗時はマージをブロックする

### 9.1 完了報告

最低限、次を報告する。

- Red で固定した仕様または再現症状
- 実行した command と成功 / 失敗件数
- E2E の browser、viewport、scenario
- report / screenshot / tracker の保存先
- skip、未確認、flaky、環境制約、残リスク

実行していない test を「通過」と書かない。以前の実行結果を使う場合は、対象commitまたは差分が同一であることを確認する。

---

## 10. プロジェクト固有ルール

- RenCrow 固有の対象packageと代表commandは `rules/PROJECT_AGENT.md` に従う
- Viewer / Playwright の言語・依存管理は `rules/rules_domain.md` に従う
- 実機観測と完了判断は `rules/common/rules_observation_verification.md` に従う
- UI の表示・操作・responsive 条件は `rules/rules_viewer_ui.md` に従う
- 個別仕様により本ルールを厳しくできるが、黙って弱めてはならない
