# DuckDB の darwin 対応と起動デグラデーション

- Status: Accepted
- Date: 2026-07-08

## 決定

RenCrow の DuckDB archive 実装を `linux/amd64` に加えて `darwin/arm64` でもビルド対象にする。

Conversation manager の起動時に DuckDB store の初期化だけが失敗した場合は、起動全体を失敗させず、WARN ログを出して L2 archive を無効化した状態で継続する。Redis と VectorDB の初期化失敗は従来どおり起動失敗として扱う。

WARN ログには `L2 archive (DuckDB) disabled` を含め、デグラデーションが起きたことを起動ログから追跡できるようにする。

## 根拠

macOS 開発環境で `conversation.enabled: true` の起動時に `duckdb archive is not supported on this platform` が発生し、会話記憶全体が fatal で停止していた。

原因は `internal/infrastructure/persistence/conversation/duckdb/` と `internal/infrastructure/persistence/toolregistry/` の DuckDB 実装が `linux && amd64` のみを対象にしており、darwin では stub 実装に差し替わるためである。

使用中の `github.com/marcboeker/go-duckdb v1.8.5` は darwin/arm64 をサポートしており、この Mac では CGO を使う go-sqlite3 のビルドも通っている。したがって darwin/arm64 を実体実装の対象に含めるのが妥当である。

一方で DuckDB は L2 archive であり、Redis の短期記憶、L1 SQLite、VectorDB の長期検索とは責務が異なる。DuckDB 初期化だけで全会話機能を停止するより、L2 archive を明示的に無効化して起動を継続するほうが実運用上の可用性が高い。

## 却下案

linux 限定を維持し、Mac は記憶無効運用にする案は却下する。

理由は、Mac が開発・検証環境として使われており、`conversation.enabled: true` の通常起動と DuckDB 実体テストを macOS で確認できない状態が回帰検知を弱めるためである。また、ドライバ側が darwin/arm64 をサポートしているため、プラットフォーム制約として維持する根拠が弱い。

## 影響

正常に DuckDB を初期化できる linux/amd64 と darwin/arm64 では、既存の L2 archive 動作は変えない。

DuckDB 初期化に失敗した場合、Conversation manager は `duckdbStore=nil` の状態で起動する。この状態では L2 session history、domain search、DuckDB knowledge archive FTS、Parquet export は空結果または no-op になり、Recall は L2 をスキップして次の段へ進む。

L1 SQLite の archive sink は DuckDB store がある場合だけ配線する。DuckDB がない場合でも staging 保存や昇格処理は L1 SQLite 側で成功し、archive 同期だけをスキップする。

Redis と VectorDB の初期化失敗は従来どおりエラーにする。これは会話状態と長期検索の主要経路であり、DuckDB archive と同じデグラデーション対象にはしない。
