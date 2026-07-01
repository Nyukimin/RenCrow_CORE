# /dci-search

## Purpose
RAG や要約ではなく、許可されたローカルコーパスの原文から根拠を直接探す。

## Agent
Worker

## Required Skill
core.dci-search

## Required Context
- 探索したい問い
- 探索対象ディレクトリ
- 参照すべき仕様またはログ

## Steps
1. 探索意図を 1 行で確認する。
2. allowlist 内の範囲だけを対象にする。
3. `rg` / read-only file read で候補を探す。
4. Evidence Pack と Search Trace を残す。
5. 見つからなかった範囲と検索語も明記する。

## Output
```text
結論:
根拠:
探索範囲:
検索語:
見つからなかったもの:
次に必要な情報:
```
