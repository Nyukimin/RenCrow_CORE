# rules_backend.md - バックエンド共通ルール

**作成日**: 2025-12-11  
**目的**: バックエンド開発における共通ルール  
**対象**: Python、Go、C、C++等のバックエンド開発全般

---

## 対象言語

このルールセットで対象となる言語：

- **Python** (.py) - スクリプト、ツール、バックエンド
- **Go** (.go) - バックエンド、CLIツール
- **C** (.c) - ファームウェア、低レベル処理
- **C++** (.cc, .cpp) - システム開発、ライブラリ
- **C/C++ Header** (.h) - ヘッダーファイル

---

## 1. Pythonコーディングスタイル

### 1.1 フォーマット・Lint

#### 推奨ツール

```bash
# 保存時に自動実行（推奨）
black .          # フォーマット
isort .          # import整理
flake8 .         # 静的解析
```

#### ruff使用

```bash
# 単一ファイルのチェック
ruff check path/to/file.py

# 自動修正
ruff check --fix path/to/file.py

# プロジェクト全体のチェック
ruff check .
```

**重要**: Lintエラーがある場合は修正してからコミットすること。

### 1.2 命名規則

| 種類 | 規則 | 例 |
|------|------|-----|
| モジュール | snake_case | `analyze_logs.py` |
| クラス | PascalCase | `DataAnalyzer` |
| 関数・変数 | snake_case | `analyze_logs` |
| 定数 | UPPER_SNAKE_CASE | `MAX_RETRY_COUNT` |
| プライベート | _prefix | `_internal_method` |

### 1.3 型ヒント（必須）

```python
def fetch_data(user_id: str, count: int = 10) -> list[dict]:
    """データを取得する。"""
    ...
```

### 1.4 Docstring（Google Style）

```python
def process_item(item_id: str, mode: str) -> ProcessResult:
    """アイテムを処理する。

    Args:
        item_id: 対象アイテムID
        mode: 実行モード

    Returns:
        処理結果

    Raises:
        ItemNotFoundError: アイテムが見つからない場合
    """
    ...
```

### 1.5 インポート順序

```python
# 1. 標準ライブラリ
import os
from datetime import datetime

# 2. サードパーティ
import yaml
from selenium import webdriver

# 3. ローカル
from shared_modules.utils import helper_function
from project.module import ClassName
```

---

## 2. エラーハンドリング

### 2.1 基本方針

- エラーは**握りつぶさない**
- 適切な粒度で**ログを残す**
- 可能な限り**復旧を試みる**
- ユーザーに**わかりやすく伝える**

### 2.2 例外設計

#### カスタム例外の階層（例）

```python
# base exceptions
class ProjectError(Exception):
    """プロジェクト基底例外"""
    pass

class ConfigError(ProjectError):
    """設定関連エラー"""
    pass

class APIError(ProjectError):
    """外部API関連エラー"""
    pass

# 具体的な例外（プロジェクト固有）
class ItemNotFoundError(ConfigError):
    """アイテムが見つからない"""
    pass

class RateLimitError(APIError):
    """レート制限超過"""
    pass
```

**注意**: 例外階層の具体的な設計は各プロジェクトの`rules_domain.md`に記載してください。

#### 例外の使い分け

| 状況 | 例外 | 対応 |
|------|------|------|
| 設定ファイルなし | `FileNotFoundError` | 処理停止 |
| 設定値が不正 | `ConfigError` | 処理停止 |
| 一時的なネットワーク障害 | `ConnectionError` | リトライ |
| API制限 | `RateLimitError` | 待機後リトライ |
| 認証エラー | `AuthenticationError` | 設定修正が必要 |

### 2.3 リトライ戦略

#### 指数バックオフ

```python
import time
from functools import wraps

def retry_with_backoff(max_retries=3, base_delay=1.0):
    def decorator(func):
        @wraps(func)
        def wrapper(*args, **kwargs):
            for attempt in range(max_retries):
                try:
                    return func(*args, **kwargs)
                except (ConnectionError, TimeoutError) as e:
                    if attempt == max_retries - 1:
                        raise
                    delay = base_delay * (2 ** attempt)
                    logger.warning(f"リトライ {attempt + 1}/{max_retries}, {delay}秒待機")
                    time.sleep(delay)
        return wrapper
    return decorator

@retry_with_backoff(max_retries=3)
def call_api(endpoint: str) -> dict:
    ...
```

#### リトライ対象の判断

| エラー種類 | リトライ | 理由 |
|-----------|---------|------|
| ネットワークタイムアウト | ✅ | 一時的な問題 |
| レート制限 | ✅ | 待機後に復旧 |
| 認証エラー | ❌ | 設定修正が必要 |
| バリデーションエラー | ❌ | 入力修正が必要 |
| サーバー500エラー | ✅ | 一時的な問題の可能性 |

### 2.4 ログ出力

#### ログレベルの使い分け

```python
import logging

logger = logging.getLogger(__name__)

# DEBUG: 開発時のデバッグ情報
logger.debug(f"API呼び出し: {endpoint}, params={params}")

# INFO: 正常な処理の記録
logger.info(f"処理成功: item_id={item_id}, result={result}")

# WARNING: 注意が必要だが処理継続
logger.warning(f"リトライ実行: attempt={attempt}, reason={reason}")

# ERROR: エラー発生、処理失敗
logger.error(f"処理失敗: item_id={item_id}, error={error}")

# CRITICAL: システム停止レベル
logger.critical(f"データベース接続不可: {error}")
```

#### 構造化ログ

```python
import json
from datetime import datetime

def log_structured(level, message, **context):
    log_entry = {
        "timestamp": datetime.utcnow().isoformat(),
        "level": level,
        "message": message,
        **context
    }
    logger.log(getattr(logging, level), json.dumps(log_entry, ensure_ascii=False))

# 使用例
log_structured(
    "INFO",
    "処理完了",
    item_id="item123",
    processing_time_ms=1523
)
```

---

## 3. Lintingルール（必須）

### 3.1 基本方針

**Lintチェックは必須です。Lintエラーがあるコードはコミットしてはいけません。**

- **コード編集後は必ずLintを実行**
- **Lintエラーがある場合は修正してからコミット**
- **コミット前チェックリストにLintチェックを含める**
- エラーがある場合は修正してから次の作業に進む

### 3.2 Python: ruff使用

#### 基本コマンド

```bash
# プロジェクト全体のチェック（必須）
ruff check .

# 特定ディレクトリのチェック
ruff check src/

# 自動修正付きチェック
ruff check --fix path/to/file.py

# フォーマットチェック（使用時）
ruff format --check .
```

#### コミット前の必須チェック

```bash
# 1. Lintチェック（必須）
ruff check .

# 2. フォーマットチェック（必須、ruff format使用時）
ruff format --check .

# 3. 自動修正（エラーがある場合）
ruff check --fix .
ruff format .
```

### 3.3 その他の言語のLintツール

#### JavaScript/TypeScript

```bash
# ESLint（必須）
npm run lint

# Prettier（フォーマット、必須）
npm run format:check  # または prettier --check .
```

#### Go

```bash
# go vet（必須）
go vet ./...

# golangci-lint（使用時、必須）
golangci-lint run
```

#### C/C++

```bash
# clang-format（フォーマット、必須）
clang-format -i src/**/*.{c,h,cc,cpp}

# clang-tidy（静的解析、推奨）
clang-tidy src/**/*.{c,h,cc,cpp}
```

### 3.3 よくあるエラーと対処

| コード | 説明 | 対処 |
|--------|------|------|
| F401 | 未使用インポート | 削除するか `# noqa: F401` |
| F841 | 未使用変数 | 削除するか `_` プレフィックス |
| E999 | 構文エラー | 構文を修正 |
| I001 | インポート順序 | `ruff check --fix` で自動修正 |

### 3.4 ruff設定

設定は `pyproject.toml` の `[tool.ruff]` セクションに記載。

#### 有効なルール

- E: pycodestyle errors
- W: pycodestyle warnings  
- F: Pyflakes (構文エラー、未使用インポート等)
- I: isort (インポート順序)
- B: flake8-bugbear (バグの可能性があるコード)
- C4: flake8-comprehensions
- UP: pyupgrade
- SIM: flake8-simplify

#### 除外ディレクトリ

```toml
[tool.ruff]
exclude = [
    "large_binaries/",
    "chrome_profiles/",
    ".venv/",
    "venv/",
    "build/",
    "dist/"
]
```

---

## 4. データ処理アプローチの選択原則

### 4.1 二つのアプローチ

#### アプローチA：事後クレンジング（実用性と柔軟性）

- **定義**: まずデータを寛容に読み込み、その後、プログラム上でデータの欠損や型を修正する
- **例**: `pd.read_csv(...)` の後に `.fillna('')` や `.astype(int)` を適用する

#### アプローチB：事前定義（厳密性と一貫性）

- **定義**: データの入り口（読み込み時）で、型や構造を厳密に定義し、不正なデータが存在できない状態を保証する
- **例**: `pd.read_csv` に `dtype` や `keep_default_na=False` を指定する

### 4.2 優位性の判断基準

#### 「事前定義」が優位になる場合

1. **入力データのスキーマ（構造）が固定的、あるいは信頼できる場合**
   - 例：自分たちで制御・生成しているCSVファイル

2. **フェイルファスト（Fail-Fast）を重視する場合**
   - データの不整合を検知した瞬間に処理を停止させ、バグの早期発見と波及防止を優先
   - 例：パイプライン処理において、上流のバグを下流で即座に検出したい場合

3. **処理のライフサイクル全体で、データの型と意味の一貫性が最優先される場合**
   - 例：金融データ、科学技術計算など、わずかな型の不一致も許されないドメイン

#### 「事後クレンジング」が優位になる場合

1. **入力データのスキーマが変動する、あるいは信頼できない場合**
   - 例：外部APIから提供されるJSON、ユーザーがアップロードする多様な形式のファイル

2. **システムの可用性（Availability）を重視する場合**
   - 多少のデータ欠損があっても処理を続行し、サービスを停止させないことを優先

3. **開発の初期段階やプロトタイピングで、迅速な実装が求められる場合**

### 4.3 プロジェクトでの適用

各プロジェクトの特性に応じて、どちらのアプローチを優先するかは`PROJECT_AGENT.md`または`rules_domain.md`に記載してください。

---

## 5. Goコーディングスタイル

### 5.1 フォーマット・Lint

#### 推奨ツール

```bash
# フォーマット
go fmt ./...

# 静的解析
go vet ./...

# リンター（golangci-lint使用時）
golangci-lint run
```

### 5.2 命名規則

| 種類 | 規則 | 例 |
|------|------|-----|
| パッケージ | 小文字、単語 | `analyzer` |
| 型 | PascalCase | `DataAnalyzer` |
| 関数・変数 | camelCase（公開） / camelCase（非公開） | `AnalyzeLogs` / `analyzeLogs` |
| 定数 | PascalCase（公開） / camelCase（非公開） | `MaxRetryCount` / `maxRetryCount` |
| インターフェース | PascalCase、-er接尾辞（推奨） | `Reader`, `Writer` |

### 5.3 コメント

```go
// Package analyzer provides data analysis functionality.
package analyzer

// AnalyzeLogs processes log files and returns analysis results.
// It reads from the specified path and returns an error if the file cannot be read.
func AnalyzeLogs(path string) (*AnalysisResult, error) {
    ...
}
```

---

## 6. C/C++コーディングスタイル

### 6.1 フォーマット・Lint

#### 推奨ツール

```bash
# フォーマット（clang-format使用時）
clang-format -i src/**/*.{c,h,cc,cpp}

# 静的解析（clang-tidy使用時）
clang-tidy src/**/*.{c,h,cc,cpp}
```

### 6.2 命名規則

| 種類 | 規則 | 例 |
|------|------|-----|
| 関数 | snake_case | `analyze_logs` |
| 変数 | snake_case | `log_count` |
| 定数 | UPPER_SNAKE_CASE | `MAX_RETRY_COUNT` |
| 型定義 | snake_case_t（C） / PascalCase（C++） | `log_entry_t` / `LogEntry` |
| マクロ | UPPER_SNAKE_CASE | `MAX_BUFFER_SIZE` |

### 6.3 ヘッダーガード

```c
#ifndef ANALYZER_H
#define ANALYZER_H

// ヘッダー内容

#endif /* ANALYZER_H */
```

### 6.4 メモリ管理

- **動的メモリ確保**: `malloc`/`free`（C）、`new`/`delete`（C++）
- **リソース管理**: RAIIパターン（C++）、適切なクリーンアップ（C）
- **メモリリーク**: 確保したメモリは必ず解放すること

---

**最終更新**: 2025-12-11  
**バージョン**: 1.0
