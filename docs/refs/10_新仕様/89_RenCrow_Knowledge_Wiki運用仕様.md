# RenCrow Knowledge Wiki 運用仕様

- Status: draft v1
- Date: 2026-06-25
- Scope: `docs/wiki/`, L1 SQLite Wiki index, RecallPack, Mio / Worker / Coder の参照導線
- Related: `09_Memory_SourceRegistry仕様.md`, `../01_正本仕様/18_Memory_Lifecycle_Recall_Context.md`, `82_Claude_Code指示配置ガバナンス仕様.md`, `86_Search_Discovery_Browse_Evidence分離仕様.md`

## 目的

RenCrow Knowledge Wiki は、既存 docs、rules、skills、AGENTS、Source Registry、Memory DB を置き換える正本ではない。

目的は、RenCrow 内の分散した知識を Markdown で薄く索引化し、AI agent が次の作業へ接続できる形に保つことである。

- 人間が読む仕様入口を保つ。
- AI が読むための安定した見出し、frontmatter、source、related を持つ。
- SQL index へ投影し、Mio の RecallPack が必要時だけ参照できる。
- raw docs 全体を prompt に投げず、選別済み snippet と provenance だけを渡す。

この Wiki は「動く辞書」であり、仕様正本、記憶正本、外部情報正本ではない。

## 階層

| Layer | RenCrow での実体 | 役割 |
| --- | --- | --- |
| Context | `AGENTS.md`, module `AGENTS.md`, `rules/`, `docs/10_新仕様/82_*` | 常時ルール、配置規約、参照先 routing |
| Automation | hooks / permissions / tests / CI | 忘れると危険な制約の強制 |
| Function | commands / skills / CLI / Viewer | 明示操作、再利用可能な作業単位 |
| Coordination | Worker / Coder / subagent / RecallPack | 文脈分離、役割分担、Mio への選別注入 |
| Knowledge Wiki | `docs/wiki/` + L1 SQLite wiki index | 上記を横断する AI-readable な索引 |

Knowledge Wiki は独立した第5層ではなく、既存4層を横断して「どこを見るべきか」を返す索引である。

## 配置

```text
docs/wiki/
  index.md
  log.md
  concepts/
    chat-worker.md
    viewer-api.md
    recall-pack.md
    source-registry.md
    memory-lifecycle.md
  modules/
    picoclaw-multillm.md
    rencrow-cmd.md
```

`docs/wiki/index.md` は Wiki 入口、`docs/wiki/log.md` は変更ログである。
概念は `concepts/`、repo / module / CLI の立ち位置は `modules/` に置く。

## frontmatter

Wiki page は YAML frontmatter を必須にする。

```yaml
---
type: concept
status: active
owner: core
canonical_source: docs/10_新仕様/09_Memory_SourceRegistry仕様.md
source:
  - docs/10_新仕様/09_Memory_SourceRegistry仕様.md
related:
  - docs/wiki/concepts/recall-pack.md
updated: 2026-06-25
---
```

必須 field:

| field | 意味 |
| --- | --- |
| `type` | `index`, `log`, `concept`, `module`, `spec`, `runbook` |
| `status` | `draft`, `active`, `archived`, `deprecated` |
| `owner` | 主担当 module または `core` |
| `canonical_source` | 判断の正本となる既存 docs / code / rule |
| `source` | 根拠にしたファイル一覧 |
| `related` | 関連 Wiki / 仕様 / module |
| `updated` | 最終更新日 |

## source と related のルール

`source` は根拠である。外部検索 snippet や未読記事を source にしない。
外部情報は Source Registry / staging / validation を通過した後で、該当する docs または DB record を source にする。

`related` は navigation である。根拠ではない。
Mio が prompt に利用する場合も、related は補助リンクとして扱い、真偽判断には使わない。

## SQL index

L1 SQLite に `wiki_page_index` と `wiki_page_index_fts` を持つ。

`wiki_page_index` は Markdown Wiki の frontmatter と短い要約を保持する hot index である。

| column | 内容 |
| --- | --- |
| `page_id` | stable ID。例: `concept:recall-pack` |
| `path` | repo root からの Markdown path |
| `title` | H1 title |
| `type` / `status` / `owner` | frontmatter 由来 |
| `canonical_source` | 正本 source |
| `source_paths_json` | source 配列 |
| `related_json` | related 配列 |
| `summary` | RecallPack に入れる短い説明 |
| `content_hash` | Markdown body の hash |
| `created_at` / `updated_at` | index 作成・更新時刻 |

`wiki_page_index_fts` は検索用の投影であり、正本ではない。

## index 更新 CLI

初期投入と更新は `picoclaw knowledge index-wiki` で行う。

```bash
GOCACHE=/tmp/picoclaw-gocache go run ./cmd/picoclaw knowledge index-wiki docs/wiki --repo-root . --json
```

この CLI は `docs/wiki/**/*.md` の frontmatter と H1 / 短い本文要約だけを読み、L1 SQLite の `wiki_page_index` に保存する。
正本 docs 全体の全文投入や Source Registry staging の直接昇格は行わない。

## RecallPack 連携

Mio は Markdown docs 全体を直接読まない。
Mio が読むのは RecallPack に選別された Wiki snippet だけである。

導線:

```text
user message
  -> ConversationEngine.BeginTurn
  -> L1SQLite.SearchWikiPageIndex
  -> RecallPack.WikiSnippets
  -> FilterForRole("chat")
  -> ToPromptMessages()
```

Chat / IdleChat では、外部情報要求または仕様確認意図がある場合だけ Wiki snippet を入れる。
Worker / Coder は作業文脈として Wiki snippet を使えるが、source を失わない。

## Memory / SQL / Mio の境界

| 領域 | 役割 | Wiki との関係 |
| --- | --- | --- |
| UserMemory | ユーザー固有の好み、制約、関係 | Wiki に混ぜない |
| Conversation Memory | 会話 thread / summary / RecallPack | Wiki snippet の採用 trace を残す |
| Knowledge | validated / promoted 済み外部知識 | Wiki の source になり得る |
| Source Registry | 外部 source の取得・検証入口 | Wiki へ直接 promote しない |
| OperationMemory | 運用手順、障害対応、再発防止 | 必要なら Wiki で索引化する |
| Wiki index | docs/rules/module の地図 | 正本 DB ではなく検索投影 |

Mio の記憶と Wiki は分離する。
Wiki page に「Mio が覚えるべきこと」を書いても UserMemory にはならない。
UserMemory に昇格する場合は Memory Lifecycle の review / confirmation を通る。

## 更新ルール

- 仕様を増やしたら、関連する Wiki page の `source` と `summary` を更新する。
- 正本仕様が変わったら、Wiki は追従する。Wiki を根拠に正本仕様を上書きしない。
- `status: archived` または `deprecated` の page は RecallPack の通常検索対象から外す。
- 1 page は原則 200 行以内にする。長くなる場合は source docs に戻す。
- 変更理由は `docs/wiki/log.md` に残す。

## 初期対象

初期 Wiki は次を作る。

- ChatWorker
- Viewer API
- RenCrow_CMD
- RecallPack
- Source Registry
- Memory Lifecycle
- `picoclaw_multiLLM`

## 検証

最低限の確認:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./internal/domain/conversation ./internal/infrastructure/persistence/conversation
```

期待:

- Wiki page が frontmatter を持つ。
- L1 SQLite が Wiki index を保存・検索できる。
- `picoclaw knowledge index-wiki` が Wiki page を L1 SQLite へ投入できる。
- RecallPack が Wiki snippet を prompt / trace / budget / role filter に含められる。
- Chat role では role 不一致や archived page が prompt に混ざらない。
