# Search Discovery / Browse Evidence 分離仕様

## 1. 目的

本仕様は、RenCrow の外部情報調査において、検索とブラウジングを明確に分離する。

基本原則は次である。

```text
Search = discovery only
Browse / Fetch = evidence
Browser = rendered / interactive verification
```

検索は URL 候補を見つけるためだけに使い、検索結果 snippet や ranking を根拠として扱わない。

根拠にできるのは、実際に URL を開いて読んだ source、取得した artifact、または real browser で観測した evidence だけである。

## 2. 背景

LLM agent は、検索結果の snippet、title、ranking だけで内容を理解したように扱うことがある。

これは RenCrow では禁止する。

理由:

```text
- snippet は検索 provider による要約であり、原文ではない。
- ranking は relevance の手がかりであり、正確性の根拠ではない。
- snippet は古い、切れている、誤解を誘う、または SEO 文である可能性がある。
- URL を開かずに根拠化すると Source Registry / audit / memory の provenance が壊れる。
```

RenCrow では、検索は discovery provider、読む行為は fetch / browser provider として別 artifact にする。

## 3. 位置づけ

本仕様は以下に接続する。

```text
09_Memory_SourceRegistry仕様
  Source Registry / staging / validation / promotion の保存境界。

46_Web情報収集ツール仕様
  Web Gather の Discovery / Fetch / Extract / Stage 分離。

71_Browser操作ツール仕様
  real browser / headless browser による rendered evidence。

83_空き時間ジョブ実行基盤仕様
  news / movie / research などの定期情報収集 job。

85_急上昇AIリポジトリ評価メモ
  Agent-Reach 的 capability router から取り込むべき read-only research router 方針。
```

## 4. 用語

### 4.1 Search

検索 provider に query を投げ、URL 候補を得る行為。

例:

```text
- SearXNG
- YaCy
- Google Custom Search
- GitHub search
- `gh search`
- web search API
- Agent-Reach 的 discovery channel
```

Search の出力は `search_result` であり、根拠ではない。

### 4.2 Browse / Fetch

URL を開き、HTTP response、本文、metadata、raw hash、取得時刻を保存する行為。

例:

```text
- HTTP fetch
- GitHub README / release / issue の取得
- RSS entry URL の取得
- Jina Reader 経由の read
- trafilatura / readability 抽出
```

Browse / Fetch の出力は `source_read` であり、根拠候補になる。

### 4.3 Browser

real browser または headless browser でページを描画し、DOM、accessibility tree、screenshot、network、console を観測する行為。

Browser は次の場合に使う。

```text
- JS rendering が必要
- UI / layout / button / navigation を確認する
- login profile を使った read-only 画面確認
- screenshot / DOM / network evidence が必要
- static fetch と rendered result が違う可能性がある
```

### 4.4 Evidence

RenCrow が根拠として扱える artifact。

最低限:

```text
- source_url
- retrieved_at
- fetch_or_browser_provider
- status
- title
- raw_hash or screenshot path
- extracted_text or DOM snapshot
- evidence_kind
```

## 5. 基本ルール

### 5.1 検索は極限まで少なくする

既知 URL がある場合、検索しない。

```text
Good:
  ユーザーが GitHub URL を渡した -> その URL を開いて読む。

Bad:
  URL があるのに repo 名で検索し直す。
```

検索は discovery が必要な場合だけ使う。

初期方針:

```text
- 既知 URL: search_count = 0
- 公式ページ候補を探す: search_count <= 1
- 比較調査: search_count <= 2 を基本
- それ以上必要なら理由を artifact に残す
```

### 5.2 検索結果 snippet を根拠にしない

検索結果の title、snippet、ranking は候補選定にだけ使う。

禁止:

```text
- snippet の内容を事実として回答する
- snippet だけで採用 / 不採用判断をする
- 検索結果一覧を読んだだけで source を読んだ扱いにする
- search_result を Source Registry promoted evidence にする
```

許可:

```text
- snippet で候補 URL の優先順位を決める
- どの URL を開くかの短い理由にする
- source_read 前の discovery artifact に残す
```

### 5.3 検索先は必ず開いて読む

評価、要約、比較、仕様化、採用判断に使う URL は必ず `source_read` にする。

```text
search_result
  -> selected_url
  -> source_read
  -> evidence
  -> summary / decision
```

URL が開けない場合は `read_failed` として扱い、根拠化しない。

### 5.4 公式・一次情報を優先する

技術調査では以下を優先する。

```text
1. 公式 docs
2. GitHub repository README / docs / releases
3. 仕様書 / paper / standards
4. maintainer issue / discussion
5. trustworthy secondary source
```

二次情報だけで判断する場合は、その制約を明記する。

### 5.5 Browser は必要な時だけ使う

通常の README、docs、plain HTML は HTTP fetch / source_read でよい。

real browser / headless browser を使うべき場合:

```text
- JS により本文が生成される
- UI の表示やクリック可否が判断対象
- screenshot evidence が必要
- network trace が必要
- login profile が必要
- local Viewer の実挙動を確認する
```

`browser_actor` / `agent-browser` / Playwright を使った場合は、screenshot、DOM snapshot、final URL、console/network summary を evidence として保存する。

## 6. Artifact 分離

### 6.1 search_result

Search の結果。

```json
{
  "artifact_type": "search_result",
  "query": "agent browser dev",
  "provider": "searxng",
  "searched_at": "2026-06-23T00:00:00Z",
  "result_count": 5,
  "results": [
    {
      "rank": 1,
      "title": "Agent Browser",
      "url": "https://agent-browser.dev/",
      "snippet": "A browser for agents..."
    }
  ],
  "used_as_evidence": false
}
```

`used_as_evidence` は必ず false とする。

### 6.2 source_read

URL を開いて読んだ結果。

```json
{
  "artifact_type": "source_read",
  "source_url": "https://github.com/kenn-io/agentsview",
  "retrieved_at": "2026-06-23T00:00:00Z",
  "provider": "http",
  "http_status": 200,
  "title": "agentsview",
  "content_type": "text/markdown",
  "raw_hash": "sha256:...",
  "extractor": "github_readme",
  "text_preview": "Local-first dashboard...",
  "read_ok": true
}
```

### 6.3 browser_evidence

Browser で確認した結果。

```json
{
  "artifact_type": "browser_evidence",
  "source_url": "http://127.0.0.1:18790/viewer",
  "final_url": "http://127.0.0.1:18790/viewer",
  "observed_at": "2026-06-23T00:00:00Z",
  "browser": "chromium",
  "headless": true,
  "screenshot_path": "workspace/browser_runs/run_001/screenshot.png",
  "dom_snapshot_path": "workspace/browser_runs/run_001/dom.json",
  "console_error_count": 0,
  "read_ok": true
}
```

## 7. Research Router 方針

Backlog `backlog-rencrow-readonly-research-router` で実装する read-only research router は、本仕様を前提にする。

### 7.1 Capability router

Router は platform ごとに backend を選ぶ。

```text
github:
  primary: gh / GitHub API / raw GitHub URL
  fallback: browser read

youtube:
  primary: yt-dlp metadata / subtitles
  fallback: browser read

rss:
  primary: feedparser
  fallback: HTTP fetch

web:
  primary: HTTP fetch + readability
  fallback: Jina Reader / browser
```

### 7.2 Health check

各 backend は health check を持つ。

```text
available
degraded
requires_config
requires_login
blocked
not_installed
```

### 7.3 Read-only boundary

Router v0 は read-only に限定する。

禁止:

```text
- post
- comment
- like
- follow
- subscribe
- issue / PR create
- account setting change
- cookie import
- login automation
```

これらは別 target とし、Human approval を必須にする。

## 8. Source Registry / Memory 連携

Search result は Source Registry の正式 source にしない。

Source Registry staging に入れてよいのは、`source_read` または `browser_evidence` を持つものだけである。

```text
search_result:
  staging 不可 / discovery cache のみ

source_read:
  staging 可 / validation 待ち

browser_evidence:
  staging 可 / UI evidence または rendered source evidence
```

Memory / Knowledge / Domain Graph へ promote する場合も、`source_read` または `browser_evidence` への参照を必須にする。

## 9. 回答時の表現

Agent は外部情報を使った回答で、次を区別して報告する。

```text
searched:
  URL 候補を探した。

read:
  URL を開いて読んだ。

verified in browser:
  実ブラウザ / headless browser で表示や挙動を確認した。

not verified:
  開けていない、または browser では未確認。
```

禁止表現:

```text
- 検索結果によると...
- snippet では...
- 見た感じ...
```

推奨表現:

```text
- 公式 README を読んだ範囲では...
- release page を開いて確認したところ...
- real browser では未確認だが、HTTP fetch では...
- 検索では候補を見つけただけで、根拠には使っていない。
```

## 10. Acceptance Criteria

実装時の完了条件:

```text
1. search_result と source_read が別 artifact になる。
2. search_result.used_as_evidence は常に false。
3. search_and_fetch は検索候補を必ず fetch してから要約する。
4. fetch 失敗 URL は evidence に使われない。
5. Source Registry staging は source_read / browser_evidence のみ受け付ける。
6. browser verification が必要な task では screenshot / DOM / final URL を保存する。
7. 最終 report に searched / read / browser-verified の数を出せる。
```

## 11. 非目標

初期実装では次を行わない。

```text
- すべての調査で real browser を必須化する
- 検索を完全禁止する
- Google / 外部検索 API を正本化する
- login / cookie が必要な platform を自動巡回する
- search snippet を memory に promote する
```

## 12. まとめ

RenCrow では検索を URL discovery に限定する。

調査の根拠は、実際に開いて読んだ `source_read`、または browser で観測した `browser_evidence` に限定する。

検索結果そのものを読んだ扱いにしない。
