# OpenAI News 本文取得失敗の調査

## 対象

- Source ID: `rss:news:openai`
- URL: `https://openai.com/index/third-party-cyber-evaluations-involving-openai-models/`
- 調査日時: 2026-08-05 15:22 JST

## 結論

NewsPack に保存されているのは全文ではなく、OpenAI 公式 RSS の約 144 文字の description である。
記事 URL の通常 HTTP 取得は Cloudflare challenge から HTTP 403 を返し、RenCrow はこれを
`blocked_by_policy` と正しく検出している。本文抽出器が途中で切ったのではない。

## 確認証跡

1. `l1_news_item` の対象レコードは RSS title と description のみで、
   `article_fetch_status=unavailable`、`article_fetch_error_code=blocked_by_policy` だった。
2. `l1_news_article_fetch` は対象 URL に対し 20 回失敗し、article chars は 0 だった。
3. RenCrow HTTPFetcher と同じ User-Agent / Accept の直接取得は HTTP 403、
   `cf-mitigated: challenge`、`Enable JavaScript and cookies to continue` を返した。
4. OpenAI 公式 `https://openai.com/news/rss.xml` は HTTP 200 だが、対象 item に
   `content:encoded` はなく description のみだった。
5. 管理済み Chromium による read-only 取得もタイトル `Just a moment...` のままで、
   `main` 要素の待機が 30 秒で timeout した。
6. OpenAI 公式 robots.txt と sitemap は取得できるが、全文を返す公式の構造化配信経路は
   確認できなかった。
7. 既存 Webwright は設定上 enabled だが、現行 upstream CLI で `-c` option が廃止され、
   既存 runner は起動できない。また Webwright の成果は仕様上
   `review_required=true` / `auto_promote=false` であり、記事原文として自動採用できない。

## 採用しない対応

- RSS description を全文と記録する。
- Cloudflare challenge を User-Agent 偽装や自動解除で回避する。
- LLM の要約・生成文を原文として保存する。
- 公式 URL と第三者 reader のテキストを出典情報なしに混同する。

## 代替経路の予備確認

Jina AI Reader (`https://r.jina.ai/http://openai.com/...`) は対象記事を HTTP 200、
8,689 bytes の Markdown として返し、本文の最終段落まで取得できた。
ただしこれは OpenAI 公式配信ではなく第三者の外部変換 service である。
RenCrow 標準 Go runtime に外部 system を追加しないルールにより、Ren の明示承認なしには
自動 fallback として実装・有効化しない。

## 必要な次の判断

第三者 reader を OpenAI 記事のみに限定した optional fallback として使う場合は、
次を仕様として明示したうえで別途実装する。

- 既存の直接 HTTP 取得を常に第一経路とする。
- `blocked_by_policy` の場合だけ optional reader を使う。
- 許可 host は `openai.com` に限定する。
- 元 URL、reader URL、取得時刻、hash、取得経路を metadata に保存する。
- 失敗時は RSS description を消さず、全文成功とは表示しない。
- 標準 profile では disabled を保つ。

## 承認後の実装結果（2026-08-05 15:49 JST）

Ren は、OpenAI 記事への直接取得が拒否された場合に限り Jina AI Reader を使うことを
明示承認した。上記の「必要な次の判断」はこの承認により解消し、次の限定 fallback を実装した。

- 通常 HTTP を常に先に実行し、error code が厳密に `blocked_by_policy` の場合だけ Reader を呼ぶ。
- Reader の取得対象 host は明示 allowlist とし、稼働設定では `openai.com` のみに限定する。
- Reader 応答の `URL Source` が元記事の host、path、query と一致しない場合は採用しない。
- Reader の外装、画像、link destination を本文から除き、段落と末尾を保持する。
- 元 URL、Reader URL、取得時刻、本文 SHA-256、取得 provider、extractor を取得台帳と
  News item metadata の双方に保存する。
- Reader が失敗しても既存の RSS title / description は削除しない。
- URL 単位の取得台帳を再利用し、同じ記事本文を重複取得しない。

### 稼働確認

1. 再起動後の直接取得は対象 URL で `blocked_by_policy` となり、限定 fallback が選択された。
2. 最初の巡回では Reader 取得が成立しなかったが、RSS レコードは削除されず、5分後の
   通常の自動再試行で `jina_reader` により復旧した。
3. `l1_news_article_fetch` は `status=ready`、`article_chars=8208`、
   `attempt_count=25`、provider `jina_reader`、extractor `jina_reader_markdown` となった。
4. NewsPack API の対象 URL は1件だけで、見出し込み `RawText` は8262文字だった。
   Reader 外装は含まず、末尾は `…` / `...` で終わっていない。
5. Viewer の News Pack で対象行を選択し、右側の全文枠に8208文字が表示された。
   全文枠は `overflow-y:auto`、本文高3051px、表示高274pxで、最終段落までDOMに存在した。
6. Firefox による実ブラウザ確認では console error は0件だった。

稼働設定は `${HOME}/.rencrow/config.yaml` で明示的に enabled とした。コード既定値は
disabled のままであり、第三者 Reader は標準 profile の必須構成ではない。
