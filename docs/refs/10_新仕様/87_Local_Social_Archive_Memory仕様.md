# Local Social Archive Memory 仕様

## 1. 目的

本仕様は、birdclaw の設計から RenCrow に取り入れるべき要素を定義する。

対象は Twitter/X などの個人 social data を、外部 SaaS 依存ではなく local-first に保存、検索、要約、監査できる仕組みである。

本仕様では、この仕組みを **Local Social Archive Memory** と呼ぶ。

参考として読んだ source:

```text
https://birdclaw.sh/
https://github.com/steipete/birdclaw
https://birdclaw.sh/data-architecture.html
https://birdclaw.sh/spec.html
https://birdclaw.sh/cli.html
```

birdclaw をそのまま RenCrow に組み込むのではない。

取り込むのは、archive-first、local SQLite、FTS5、JSON CLI、Git-friendly backup、privacy boundary、read-only-first の設計である。

## 2. 背景

RenCrow は、外部情報収集、Memory、Source Registry、Domain Graph、Idle Job、Research Router を持つ。

一方、Twitter/X のような social platform は次のリスクを持つ。

```text
- API / cookie / browser session / rate limit / platform terms に依存する。
- DMs、likes、bookmarks、mentions など privacy-sensitive なデータを含む。
- reply、post、block、mute など外部副作用が強い操作を含む。
- live sync だけに頼ると再現性と監査性が弱い。
```

birdclaw は、この問題に対し、local-first SQLite、archive import、cached live reads、FTS5、JSON CLI、local web UI、Git-friendly text backup という方向を示している。

RenCrow ではこれを、個人 social memory の正本設計として参考にする。

## 3. 位置づけ

本仕様は以下に接続する。

```text
09_Memory_SourceRegistry仕様
  observed / staging / validated / promoted の保存境界。

19_DCI_直接コーパス探索仕様
  raw archive / JSONL / SQLite / backup shard を直接探索する方針。

46_Web情報収集ツール仕様
  外部情報取得と staging の分離。

83_空き時間ジョブ実行基盤仕様
  archive import / read-only sync / backup / digest を低優先度 job にする方針。

86_Search_Discovery_Browse_Evidence分離仕様
  search result ではなく source_read / browser_evidence を根拠にする方針。
```

## 4. 採用する設計要素

### 4.1 Archive-first

RenCrow では social platform の個人記憶を live API だけで構築しない。

最初の正本は、ユーザーが明示的に提供した公式 archive export とする。

```text
archive import
  -> local raw archive preservation
  -> normalized SQLite
  -> FTS index
  -> validation / digest / recall
```

live sync は補助であり、archive を置き換えない。

### 4.2 Local-first SQLite

個人 social memory は local SQLite に保存する。

初期 table domain:

```text
social_accounts
social_posts
social_profiles
social_edges
social_collections
social_dms
social_media
social_import_runs
social_sync_runs
social_backup_runs
```

RenCrow の L1 SQLite / Domain Graph と直結せず、最初は独立 DB とする。

理由:

```text
- privacy-sensitive なデータを分離する。
- DMs や likes を通常 Knowledge と混ぜない。
- 削除や export の境界を明確にする。
- social platform 固有 schema churn を core memory に波及させない。
```

### 4.3 FTS5 local search

検索は外部検索ではなく local FTS5 を第一経路にする。

対象:

```text
- authored posts
- liked posts
- bookmarked posts
- mentions
- selected DMs
- imported profile bios
- URLs / link previews
```

DM は default で検索対象に含めない。ユーザーが明示的に `include_dm=true` を指定した場合だけ対象にする。

### 4.4 JSON CLI

RenCrow_Tools 側に将来作る CLI は、agent / Worker が扱いやすい stable JSON envelope を返す。

原則:

```text
- JSON result は stdout
- progress / warning / diagnostics は stderr
- exit_code を意味づける
- raw artifact path を返す
- destructive / write 操作は v0 に含めない
```

### 4.5 Git-friendly backup

SQLite の内容を、そのまま DB backup するだけでなく、deterministic JSONL shard として private repository や archive directory に出せるようにする。

例:

```text
backup/social/twitter/posts/2026.jsonl
backup/social/twitter/dms/conversation_<id>.jsonl
backup/social/twitter/profiles.jsonl
backup/social/twitter/media_manifest.jsonl
```

backup は private / local-only を前提にする。

public repository へ push しない。

### 4.6 Privacy boundary

DM、private likes、bookmarks、mutual graph、block/mute data は sensitive として扱う。

初期既定:

```text
include_dm: false
include_media: manifest_only
include_live_reads: false
include_write_actions: false
allow_external_summary: false
```

Digest / AI scoring / RecallPack へ入れる場合は privacy gate を通す。

### 4.7 Read-only first

RenCrow は social platform の write client にならない。

初期実装で許可するのは read-only のみ。

許可:

```text
- archive import
- local search
- local digest
- local backup
- read-only live sync proposal
- media manifest
```

禁止:

```text
- post
- reply
- DM send
- block / unblock
- mute / unmute
- follow / unfollow
- like / unlike
- bookmark write
```

これらを扱う場合は、別仕様、別 target、Human approval、dry-run、review UI、外部副作用 log を必須にする。

## 5. データ境界

### 5.1 SocialArchiveRoot

既定 path:

```text
~/.rencrow/social_archive/
```

構成:

```text
social_archive/
  twitter_x/
    db/social.sqlite
    raw_archives/
    media/
    backups/
    runs/
    exports/
```

### 5.2 Raw Archive

ユーザー提供の archive zip / extracted directory は raw artifact として保存する。

```json
{
  "archive_id": "x_archive_20260623_000001",
  "platform": "twitter_x",
  "source_path": "/path/to/twitter-archive.zip",
  "stored_path": "raw_archives/x_archive_20260623_000001.zip",
  "sha256": "sha256:...",
  "import_status": "pending",
  "provided_by": "user",
  "provided_at": "2026-06-23T00:00:00Z"
}
```

### 5.3 Import Run

import は idempotent にする。

```json
{
  "run_id": "social_import_20260623_000001",
  "archive_id": "x_archive_20260623_000001",
  "platform": "twitter_x",
  "selected_slices": ["tweets", "likes", "bookmarks"],
  "status": "completed",
  "inserted": 1200,
  "updated": 44,
  "skipped": 180,
  "failed": 0,
  "started_at": "2026-06-23T00:00:00Z",
  "finished_at": "2026-06-23T00:01:00Z"
}
```

### 5.4 Social Post

```json
{
  "post_id": "1891234567890",
  "platform": "twitter_x",
  "account_id": "local_account",
  "author_id": "author_123",
  "collection": "liked",
  "text": "example",
  "created_at": "2026-01-01T00:00:00Z",
  "source_kind": "archive",
  "source_archive_id": "x_archive_20260623_000001",
  "raw_ref": "artifact://social_archive/raw/...",
  "visibility": "private",
  "validation_status": "imported"
}
```

### 5.5 Social DM

DM は別 table に保存し、default recall / digest / search から除外する。

```json
{
  "dm_id": "dm_001",
  "conversation_id": "conv_001",
  "platform": "twitter_x",
  "sender_id": "user_a",
  "recipient_ids": ["user_b"],
  "text": "private message",
  "created_at": "2026-01-01T00:00:00Z",
  "source_kind": "archive",
  "sensitivity": "private_dm",
  "default_recall_allowed": false
}
```

## 6. CLI / Tool contract

### 6.1 archive import

```bash
rencrow-social archive import /path/to/twitter-archive.zip \
  --platform twitter_x \
  --select tweets,likes,bookmarks \
  --json
```

出力:

```json
{
  "ok": true,
  "run_id": "social_import_20260623_000001",
  "archive_id": "x_archive_20260623_000001",
  "db_path": "~/.rencrow/social_archive/twitter_x/db/social.sqlite",
  "counts": {
    "inserted": 1200,
    "updated": 44,
    "skipped": 180,
    "failed": 0
  }
}
```

### 6.2 local search

```bash
rencrow-social search posts "local-first" \
  --collection liked \
  --limit 50 \
  --json
```

DM を含める場合:

```bash
rencrow-social search posts "contract" --include-dm --json
```

`--include-dm` は明示指定必須。

### 6.3 digest

```bash
rencrow-social digest today --platform twitter_x --json
rencrow-social digest week --platform twitter_x --exclude-dm --json
```

DM は default exclude。

### 6.4 backup

```bash
rencrow-social backup export --platform twitter_x --jsonl --json
```

## 7. Idle Job target

Idle Job Scheduler に追加する場合の target:

```json
{
  "target": "social_archive_backup",
  "display_name": "Social archive backup",
  "executor": "builtin_or_tool",
  "side_effect_level": "local_artifact",
  "approval_required": false,
  "artifact_type": "social_backup_run",
  "default_idle_policy": {
    "require_health_ok": true,
    "max_parallel": 1,
    "skip_when_active_user_session": true
  }
}
```

live sync は初期 target にしない。

将来追加する場合:

```text
social_archive_live_sync_readonly
  approval_required: true
  side_effect_level: external_read

social_archive_action_proposal
  approval_required: true
  side_effect_level: external_write_proposal_only
```

## 8. Viewer

Viewer に追加する場合は、最初は Ops / Memory から分離する。

候補:

```text
/viewer/social-archive
```

初期表示:

```text
- archive import status
- DB stats
- last backup
- FTS search box
- privacy filters
- digest preview
- DM excluded indicator
```

DM / private data は初期表示しない。

検索結果も、DM を含む場合は明示 indicator を出す。

## 9. RenCrow Memory との接続

Social Archive は通常 Memory へ直結しない。

接続ルール:

```text
social_archive row
  -> recall candidate
  -> user review / validator
  -> promoted memory / knowledge
```

自動 promote 禁止:

```text
- liked post
- bookmarked post
- DM
- block / mute
- follow graph
```

promotion できるもの:

```text
- ユーザーが明示的に保存したいと指定した投稿
- validated された自分の過去投稿
- 公開投稿で、Source Registry evidence として保存する価値があるもの
- digest から抽出した decision / commitment / learning の候補
```

## 10. Security / Privacy

必須:

```text
- local-only default
- DM default excluded
- external write disabled
- cookie / login profile not required for archive import
- raw archive hash preservation
- private backup path
- export redaction option
- delete / purge path
```

禁止:

```text
- private DM の通常 prompt 注入
- social DB を Qdrant へ丸ごと upsert
- public repo への backup push
- cookie / token / auth header の artifact 保存
- platform write action の自動実行
```

## 11. birdclaw から採用しないもの

初期実装では採用しない。

```text
- Twitter/X write client
- reply / post / DM send
- block / mute remote write
- cookie-backed write fallback
- local web UI の全面移植
- Node / pnpm stack の直接導入
- birdclaw DB schema の丸ごとコピー
```

## 12. Acceptance Criteria

Phase 1: spec / prototype

```text
- archive-first / read-only-first の仕様がある。
- social archive root と SQLite boundary が定義されている。
- DM default exclude が明記されている。
- JSON CLI contract がある。
```

Phase 2: import / search

```text
- sample archive fixture を import できる。
- posts / likes / bookmarks を SQLite に正規化できる。
- FTS5 で local search できる。
- import run が idempotent である。
```

Phase 3: backup / digest

```text
- deterministic JSONL backup を出せる。
- DM を除外した digest を作れる。
- backup artifact に raw hash と run log が残る。
```

Phase 4: Memory connection

```text
- social archive row を memory candidate として提示できる。
- 自動 promote されない。
- user review / validator 経由でのみ promoted memory にできる。
```

## 13. まとめ

birdclaw は RenCrow にとって、Twitter/X 個人記憶の local warehouse 設計として参考になる。

採用するのは、archive-first、local SQLite、FTS5、JSON CLI、Git-friendly backup、privacy boundary、read-only-first である。

採用しないのは、write client、cookie-backed write fallback、Node stack の直接導入、schema 丸ごとコピーである。
