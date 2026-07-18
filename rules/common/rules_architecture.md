# rules_architecture.md - 仕様・設計・アーキテクチャルール

**作成日**: 2025-12-11  
**最終更新**: 2025-12-11  
**バージョン**: 1.0  
**目的**: 仕様決定・設計・技術選定・エラーハンドリング等の共通指針を定義する  
**適用範囲**: 新機能設計・既存機能の拡張・技術選定を行う全プロジェクト

---

## 1. 仕様検討プロセス

### 1.1 基本フロー

```text
1. 要件の明確化
   ↓
2. 既存実装の確認
   ↓
3. 選択肢の列挙
   ↓
4. トレードオフの評価
   ↓
5. 決定と記録（必要に応じて ADR）
```

### 1.2 5W1H による整理

- **What**: 何を実現するのか（機能の概要）
- **Why**: なぜ必要なのか（背景・目的）
- **Who**: 誰が使うのか（ユーザー／システム）
- **When**: いつ使われるのか（タイミング・頻度）
- **Where**: どこで動くのか（環境・インフラ）
- **How**: どう実現するのか（大まかな技術方針）

### 1.3 受け入れ基準（Acceptance Criteria）

```yaml
機能: 機能名
受け入れ基準:
  - ユーザーが○○すると、△△が表示されること
  - 入力Xが不正な場合、エラーYが返されること
  - 正常ケースで応答時間がZ秒以内であること
```

---

## 2. 技術選定

### 2.1 評価マトリクスの例

| 基準           | 重み | 選択肢A | 選択肢B |
|----------------|------|---------|---------|
| 機能充足度     | 30% | 9       | 7       |
| 学習コスト     | 20% | 6       | 9       |
| 保守性         | 25% | 8       | 8       |
| パフォーマンス | 15% | 7       | 8       |
| コスト         | 10% | 9       | 6       |
| **合計**       |      | **7.8** | **7.6** |

- 数値はあくまで比較用であり、最終決定には定性的な観点も含めて判断する

---

## 3. 設計原則

### 3.1 SOLID 原則（要約）

- **S**ingle Responsibility: 単一責任（1 クラス／1 モジュールは 1 つの役割）
- **O**pen/Closed: 拡張に開き、修正に閉じる
- **L**iskov Substitution: 置換可能性（サブクラスは親と置き換え可能であるべき）
- **I**nterface Segregation: インターフェース分離
- **D**ependency Inversion: 依存性逆転

### 3.2 KISS / YAGNI

- **KISS**: 「可能な限りシンプルに保つ」
- **YAGNI**: 「必要になるまで作らない」

### 3.3 実践的な指針

- 各スクリプト／モジュールは一つの目的に特化させる
- 共通処理は関数／メソッド化し、重複を避ける（DRY）
- エラーハンドリングは後からではなく、最初から考慮する
- テストしやすい構造（依存の注入、インターフェース分離など）を意識する

---

## 4. リトライ機構

### 4.1 適用範囲

- 外部 API 呼び出し
- ネットワーク I/O
- 外部ストレージアクセス（DB、オブジェクトストレージ等）

### 4.2 原則

- 無限リトライは禁止する
- 最大リトライ回数と初期待機時間を設定可能にする
- リトライ間隔は指数バックオフを基本とする
- リトライのたびにログを記録する（少なくとも WARNING）

### 4.3 実装例（Python）

```python
import time
from typing import Callable, TypeVar, Optional

T = TypeVar("T")


def retry_with_backoff(
    func: Callable[[], T],
    max_retries: int = 3,
    initial_delay: float = 5.0,
    backoff_factor: float = 2.0,
) -> Optional[T]:
    """指数バックオフを使用した汎用リトライ関数。"""
    delay = initial_delay
    for attempt in range(max_retries):
        try:
            return func()
        except Exception as e:
            if attempt == max_retries - 1:
                logger.error("リトライ失敗（%d回試行）: %s", max_retries, e)
                raise
            logger.warning("リトライ %d/%d: %s", attempt + 1, max_retries, e)
            time.sleep(delay)
            delay *= backoff_factor
    return None
```

---

## 5. バッチ処理

### 5.1 原則

- 同種の処理は、可能であれば一括取得＋ループ処理で行う
- 一件の失敗で全体を止めず、可能な限り続行する
- 最大処理件数を設定可能にし、レート制限に対応できるようにする

### 5.2 実装例（Python）

```python
def process_items(items: list[Item], max_items: int = 100) -> ProcessResult:
    results: list[Result] = []
    errors: list[dict] = []

    for item in items[:max_items]:
        try:
            result = process_single_item(item)
            results.append(result)
        except Exception as e:
            logger.error("項目 %s の処理に失敗: %s", item.id, e)
            errors.append({"item": item.id, "error": str(e)})
            # 1 件の失敗で全体を停止せず続行

    return ProcessResult(success=results, errors=errors)
```

---

## 6. 既存モジュール再利用

### 6.1 原則

- 新機能実装前に、既存モジュールで代替できないかを必ず確認する
- 再利用可能な関数やクラスがある場合は、それを拡張する方向で検討する
- 既存コードの設計意図に沿う形で拡張し、一貫性を保つ

### 6.2 実装上の注意

- コードの重複を避ける（DRY）
- 既存のインターフェースを尊重し、破壊的変更は最小限に
- どうしても大きく変える必要がある場合は ADR で記録し、影響範囲を明示する

---

## 7. タイムアウトと設定管理

### 7.1 タイムアウト

- タイムアウト値をコード中にハードコードせず、設定ファイルで管理する
- 開発／ステージング／本番など環境ごとに設定を変えられるようにする
- タイムアウト種別（ログイン、ページロード、API等）ごとに値を分ける

```yaml
# config.yaml
timeouts:
  login: 30       # 秒
  page_load: 60
  api_call: 10
```

```python
timeout = config.get("timeouts", {}).get("api_call", 10)
```

### 7.2 設定ファイルの共通／個別

- 共通設定とユーザー／アカウント別設定を分離する
- 個別設定が共通設定を上書きするルールを明示する

```python
def load_config(common_path: str, user_path: str | None = None) -> dict:
    config = load_yaml(common_path)
    if user_path and os.path.exists(user_path):
        user_config = load_yaml(user_path)
        config = {**config, **user_config}
    validate_required_keys(config)
    return config
```

---

## 8. ADR（Architecture Decision Record）

### 8.1 目的

- 重要な技術的決定を、後から追跡できるようにする
- 「なぜその選択をしたのか」を残す

### 8.2 テンプレート

```markdown
# ADR-001: 技術選定の記録（例）

## ステータス
採用 / 却下 / 検討中

## コンテキスト
- どのような背景・制約・要件があったか

## 決定
- 採用した技術やアーキテクチャの要点

## 理由
- 採用理由（比較検討した選択肢との違い）

## 結果
- 現時点での影響・メリット・デメリット
```

### 8.3 管理場所

- 比較検討中のADRはGit管理外の `logs/decisions/ADR-XXX_<タイトル>.md` に保存する
- 採用した決定は `docs/README.md` から該当する現行正本へ反映し、ADRを第二の正本にしない
- 番号は通し番号とし、調査中の変更履歴を追いやすい形に保つ

---

## 9. ツール・CLI・テストの言語選択

### 9.1 基本原則

新しい CLI、テスト、補助スクリプト、E2E を作るときは、ユーザーに都度判断させず、プロジェクト側の技術選定ルールに従う。

判断順序:

1. 既存のインストール済みツール、自作ツール、`/home/nyukimi/RenCrow/RenCrow_Tools` / リポジトリ内 `scripts/` / `experiments/` / `sandbox/` の再利用
2. プロジェクト本体の主言語
3. 対象領域と同じ言語圏
4. 既存テスト・既存ツールとの整合
5. 配布・運用のしやすさ
6. ライブラリ適性

### 9.1.1 ツール導入と再利用

外部ツール、CLI、MCP、ブラウザ自動化ツール、補助スクリプト実行基盤を導入する前に、次を確認する。

- 目的、導入元、実行権限、ネットワークアクセス、ファイル書き込み範囲
- 既存ツール、自作ツール、インストール済み skill / plugin / MCP で代替できるか
- 一度だけ使う検証なのか、今後も再利用する恒久ツールなのか
- 依存管理、更新方法、削除方法、記録場所

新規ツール作成は最後の手段とする。作成またはインストールしたツールは、場所、用途、起動方法を記録し、次回以降の同種作業では先に再利用する。

### 9.2 CLI

CLI は、原則としてプロジェクト本体の主言語を第一候補にする。

理由:

- 既存 config / domain / API 型を再利用しやすい
- 配布・起動・運用の依存を増やしにくい
- 本体と同じエラー処理・ログ処理に寄せやすい

例外として、短命の解析や実験スクリプトは Python / Node.js などを選んでよい。
ただし恒久 CLI に昇格する場合は、主言語への移植を検討する。

### 9.3 ブラウザ E2E

ブラウザ E2E は、対象フロントエンドと同じ言語圏を優先する。

- HTML / CSS / JavaScript の Viewer や Web UI なら Node.js Playwright を第一候補にする
- Python の既存 pytest 資産がある場合のみ Python Playwright を検討する
- DOM、console、network、WebSocket、browser context を扱うテストは、Node.js 側に寄せる

### 9.4 データ・音声・解析スクリプト

データ処理、音声処理、ログ解析、実験的な集計では Python を候補にする。

ただし、恒久運用に組み込む場合は以下を再確認する。

- 依存環境を増やしてよいか
- 本体 CLI と責務が重複しないか
- 再現可能な lock / requirements 管理があるか
- CI / systemd / 運用手順に自然に乗るか

### 9.5 依存管理

言語ごとの依存管理を混同しない。

- Node.js: `package.json` / `package-lock.json`
- Python: `requirements.txt` / `pyproject.toml` / lock file
- Go: `go.mod` / `go.sum`

Playwright も、Node.js で使うなら Node の依存として管理し、Python で使うなら Python の依存として管理する。

---

## 10. プロジェクト固有ルール

- 実際に採用するリトライポリシー（回数・待機時間）
- 設定ファイルの分割戦略
- どのレベルの決定を ADR にするか

これらは各プロジェクトの `PROJECT_AGENT.md` または `rules_domain.md` にて上書き・追加定義できる。
