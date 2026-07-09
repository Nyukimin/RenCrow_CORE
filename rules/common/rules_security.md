# rules_security.md - セキュリティ・依存関係・アクセス制御ルール

**作成日**: 2025-12-11  
**最終更新**: 2025-12-11  
**バージョン**: 1.0  
**目的**: ソースコードと依存関係のセキュリティを確保し、機密情報の漏洩を防止する  
**適用範囲**: すべてのリポジトリ・プロジェクト

---

## 1. 環境変数と設定ファイル

### 1.1 .env ファイル

- API キー、DB 接続情報などの機密情報は `.env` に記述し、リポジトリにはコミットしない
- `.env` は必ず `.gitignore` に含める

```bash
# .env（例）
API_KEY=your_api_key_here
DATABASE_URL=postgresql://user:pass@host:5432/dbname
```

### 1.2 コードからの読み込み（Python）

```python
import os
from dotenv import load_dotenv

load_dotenv()

API_KEY = os.environ.get("API_KEY")
if not API_KEY:
    raise ConfigError("API_KEY が設定されていません")
```

- 重要な環境変数が未設定の場合は、起動時に明示的にエラーとする

---

## 2. .gitignore テンプレート

### 2.1 基本設定

```gitignore
# 環境変数
.env
.env.local
.env.*.local

# 認証情報
*.pem
*.key
credentials.json
profiles/
chrome_profiles/

# ログ
logs/
*.log

# キャッシュ・ビルド成果物
__pycache__/
.pytest_cache/
.coverage
htmlcov/
build/
dist/
*.pyc
*.pyo

# 依存関係のインストール済みディレクトリ
venv/
.venv/
node_modules/
vendor/
target/

# 大きなバイナリ
large_binaries/
```

- プロジェクトに応じて適宜追加・修正する

---

## 3. 依存関係のセキュリティチェック

### 3.1 Python

```bash
# 脆弱性スキャン
pip install safety pip-audit

safety check
pip-audit

# 更新可能なパッケージの確認
pip list --outdated
```

### 3.2 Node.js

```bash
# 脆弱性チェック
npm audit

# 更新可能なパッケージの確認
npm outdated
```

### 3.3 運用指針

- 定期的（例: 月 1 回）に依存関係のスキャンを実施し、結果を確認する
- 重大な脆弱性が検出された場合は、優先度を上げて更新対応を行う

---

## 4. ファイル権限とアクセス制御

### 4.1 機密ファイルの権限

```bash
# 設定ファイル: 所有者のみ読み書き
chmod 600 .env
chmod 600 config/*.yaml

# ログディレクトリ: 所有者のみアクセス
chmod 700 logs/
```

- 特にサーバー環境では、OS レベルの権限設定を徹底する

---

## 5. 入力バリデーション

### 5.1 設定ファイルの検証（Python + Pydantic）

```python
from pydantic import BaseModel, validator


class Config(BaseModel):
    key: str
    value: str

    @validator("key")
    def validate_key(cls, v: str) -> str:
        if not v:
            raise ValueError("key は必須です")
        return v
```

- 外部入力（ユーザー入力・設定ファイル・APIレスポンス）には必ず検証を入れる
- バリデーションエラーは握りつぶさず、ログとともに適切にハンドリングする

---

## 6. ログとセキュリティの境界

- `rules_logging.md` に従い、ログには機密情報を出力しない
- セキュリティインシデントに関連するログは、保全目的で安全な場所に保存する
- ログの保存期間・暗号化・アクセス権限はプロジェクトごとに定める

---

## 7. API キー・トークンの扱い

- API キー／トークンは `.env` やシークレットマネージャーで管理し、
  - コード内に直書きしない
  - Git の履歴に残さない
- 万が一コミットしてしまった場合：
  1. 当該キーを直ちに無効化またはローテーションする
  2. 影響範囲を確認し、必要に応じて報告・対応を行う

---

## 8. プロジェクト固有ルール

- 本番環境でのアクセス制御ポリシー
- 使用するシークレットマネージャー（AWS Secrets Manager, GCP Secret Manager 等）
- ログ・バックアップの暗号化方針

これらは各プロジェクトの `PROJECT_AGENT.md` または `rules_domain.md` にて詳細定義する。

---

## 9. RenCrow 自己ライフサイクル変更ガード

Repair / Coder Proposal / Worker 自動実行では、RenCrow 本体の稼働プロセスと live binary を変更しない。

自動実行禁止:
- `rencrow.service` の `start` / `stop` / `restart` / `reload` / `enable` / `disable`
- `rencrow` プロセスへの `pkill` / `killall`
- `make install`
- `~/.local/bin/rencrow` へのコピー、上書き、削除

必要な場合は Proposal に「承認が必要な運用手順」として記録し、Worker は `approval_required` で停止する。承認後の停止、port 停止確認、install、起動、health check は外側の operator が行う。
