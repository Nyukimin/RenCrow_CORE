# News Pack 本文途中切れ調査

## 概要

- 症状: News Pack の右側「Full article」に、末尾が `…` のRSS概要が本文として表示される記事があった。
- 対象: `RenCrow_CORE` のRSS記事取得、NHK本文抽出、News Pack Viewer表示。
- データ保全: 既存ニュース、取得済み本文、記事取得台帳は削除していない。

## 仮説

1. Viewerが取得済み本文を `short()` で省略している。
2. HTTP取得結果そのものが冒頭文だけで、保存前に本文が欠落している。
3. 本文未取得時のRSS概要を、Viewerが「Full article」として表示している。
4. NHKの短い記事では、本文先頭段落が抽出対象から外れている。

## 検証結果

- 仮説1は棄却した。右側本文欄は `newsContent(selected)` をそのまま表示し、CSSもスクロール枠であり文字数切り詰めはなかった。
- 仮説2は確認した。NHK記事を通常のHTTP GETで取得すると、公開HTMLには省略された導入文だけが含まれる場合がある。
- Firefoxの実ブラウザでは、同じURLで認証セッション確立後に完全なサーバーレンダリングHTMLが返り、本文末尾まで存在した。
- `https://news.web.nhk/tix/build_authorize` を経由しCookieを保持して記事URLへ戻る3リダイレクトのHTTPフローでも、ブラウザと同じ完全HTMLを取得できた。
- 仮説3は確認した。`article_fetch_status=unavailable` の記事でも `RawText` のRSS概要を全文欄へ出していた。
- 仮説4は確認した。NHKの「円相場 値下がり」では、先頭2段落と後半3段落の本文クラスは同じだが、本文マーカー `.c-part` は後半だけに付いていた。

## 根本原因

1. 新NHKサイトは最初の未認証HTMLで完全本文を返さず、従来の直接GETではRSS概要相当しか確認できなかった。
2. 本文未取得状態とRSS概要の表示境界がなく、右側の全文枠が未取得概要も本文として扱っていた。
3. NHK本文抽出が `.c-part` 付き段落だけを対象にしており、同一本文クラスに属する先頭段落を取りこぼす記事があった。
4. 全ソース共通の200文字下限により、省略なしで完結する114文字のNHK短報も未取得扱いになっていた。
5. 同じ記事URLが複数RSSに載った場合、取得台帳は共有されていたが、古いニュース行の `unavailable` 状態が残る場合があった。

## 修正

- NHKニュース記事URLだけ、NHKの公開認証セッション入口を経由し、Cookie jarを保持して完全HTMLを取得する。
- 記事URL単位の既存取得台帳をそのまま使い、同じ正規化URLの重複HTTP取得を防ぐ。
- NHK抽出は、`.c-part` 付き段落と同一の非空クラスを持つ段落も本文として連結する。関連記事カードは別クラスなので含めない。
- NHKの認証後本文マーカーから抽出した記事は、200文字未満でも末尾が省略記号でなければ完全な短報として扱う。
- 本文取得完了時と台帳再利用時に、同一URLの全ニュース行へ本文、ハッシュ、`ready` メタデータを原子的に同期する。
- `article_fetch_status != ready` の項目はRSS概要を全文欄へ出さず、「本文取得待ち」と表示する。
- News Pack更新時は、一覧順と未取得項目を保持したまま、最初の取得済み本文を右欄の初期選択にする。

## 関連ファイル

- `internal/infrastructure/webgather/http_fetcher.go`
- `internal/infrastructure/webgather/http_fetcher_test.go`
- `internal/infrastructure/webgather/html_extractor.go`
- `internal/infrastructure/webgather/html_extractor_test.go`
- `internal/adapter/viewer/assets/js/tabs/news-pack.js`
- `internal/adapter/viewer/assets/css/viewer.css`
- `internal/adapter/viewer/viewer_memory_panel.test.mjs`

## テスト・実機確認

- `go test ./internal/infrastructure/webgather ./internal/application/sourcefetcher ./internal/infrastructure/persistence/conversation/l1sqlite ./internal/adapter/viewer`: 成功。
- News Pack関連Nodeテスト4件: 成功。
- 実ブラウザで最新NHK記事「財務省 全国の景気判断据え置き」を確認し、本文517文字が末尾「判断を示しませんでした。」まで表示された。
- 未取得の「九州新幹線」を選択し、省略RSS文を出さず「本文取得待ち」を表示することを確認した。
- 短いNHK記事「円相場 値下がり」は修正前 `unavailable`、修正後 `ready` となり、先頭から末尾まで267文字が保存された。
- 同一URLの「スペースX 上場後初の決算」はNHK Top行だけが `ready`、Business行が省略された `unavailable` だったが、台帳再利用時の同期により両方とも519文字・`ready` になった。
- 114文字のNHK短報「九州新幹線」は修正前 `unavailable`、修正後 `ready` となり、実画面で末尾「見通しだということです。」まで表示された。
- 100件・ソースごと5件の最終ライブスナップショットは70件中65件が `ready`、`ready` 本文の末尾省略記号は0件だった。残る5件はOpenAIページの取得不可で、削除せず自動再試行対象として残している。
- サービスは `ActiveState=active`、`SubState=running`、`NRestarts=0`、`/health` 正常、`/ready` は `true`。
- 反映時の停止はsystemd再起動による正常終了で、ログは `Shutdown complete`。OOMやpanicではない。

## チェックリスト

- [x] 症状をライブAPIと実ブラウザで再現した。
- [x] 表示切り詰めと取得欠落を分離して確認した。
- [x] 取得元ページの認証後HTMLで完全本文を確認した。
- [x] 取得済み本文を削除・短縮しないことを確認した。
- [x] 未取得RSS概要を全文扱いしないテストを追加した。
- [x] NHK先頭段落を含め、関連記事を除外するテストを追加した。
- [x] 200文字未満の完結したNHK短報と、末尾省略短報を区別するテストを追加した。
- [x] 同一URLの複数ニュース行を削除せず同期するテストを追加した。
- [x] 関連GoテストとViewerテストを実行した。
- [x] ライブサービスのhealth/readinessと実画面を確認した。

## 得られた知見

- 「画面で途中切れ」に見えても、表示層、保存済みデータ、取得元レスポンスを別々に確認する必要がある。
- 新NHKサイトは通常GETと認証セッション後GETで本文量が異なるため、直接GETの成功だけでは全文取得を証明できない。
- `ready` とRSS概要は表示契約を分離し、未取得を完全本文のように見せないことが重要である。
- Serena MCPは当該ランタイムで無効だったため、Serenaメモリへの調査要約保存は実施できなかった。
