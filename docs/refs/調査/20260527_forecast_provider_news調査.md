# Forecast provider / Google News 調査

## 結論

2026-05-27 時点の初回調査では、Forecast 用 LLM provider は `Coder1 > Coder2 > Coder3 > Coder4 > Worker > Chat > Wild` の順で選択されていた。後続修正で Forecast 専用 policy は `Coder1 primary -> 外部LLM 1回のみ -> 明示エラー` に変更した。Worker / Chat / Wild へは落とさない。

一方、Google News RSS の 400 は再現した。原因は `fetchGoogleNewsSeeds()` が検索語の空白だけを `+` に置換し、日本語などの非 ASCII 文字を percent-encode していないこと。`q=はしか感染拡大` は HTTP 400、`q=%E3%81%AF...` の percent-encoded URL は HTTP 200 だった。

## 調査対象

- `cmd/picoclaw/runtime_idlechat.go`
- `internal/application/idlechat/forecast_topic_generation.go`
- `internal/application/idlechat/forecast_trend_sources.go`
- `internal/application/idlechat/forecast_topic_stock.go`
- `~/.picoclaw/config.yaml`
- `~/.picoclaw/logs/picoclaw.log`

調査時点ではコード変更なし。後続の最小修正で、Google News URL encode と Forecast LLM error log の診断強化を実装した。

## Forecast topic / keyword パイプライン

`generateForecastTopicInline()` の流れ:

1. `fetchTrendSeeds(domain)`
   - Google Trends JP RSS
   - Reddit hot
   - はてブ hotentry
   - ドメイン別スコアリング
2. `fetchDomainSeeds(domain, 10)`
   - NHK RSS など、domain に紐づく RSS 見出し取得
3. `extractForecastKeyword(domain, allHeadlines)`
   - 集めた見出しから検索キーワードを Forecast LLM で抽出
4. `fetchGoogleNewsSeeds(keyword, 5)`
   - 抽出キーワードで Google News RSS 検索
5. `generateForecastTopic(domain, seeds)`
   - seed から Forecast お題を Forecast LLM で生成

該当箇所:

- `internal/application/idlechat/forecast_topic_generation.go:13-22`
- `internal/application/idlechat/forecast_topic_generation.go:129-169`
- `internal/application/idlechat/forecast_topic_generation.go:173-208`

## LLM provider 選択

Forecast provider は `buildIdleChatRuntime()` で `selectForecastProvider()` により選ばれ、`SetForecastProvider()` で IdleChat orchestrator に渡される。

初回調査時点の優先順位:

1. Coder1
2. Coder2
3. Coder3
4. Coder4
5. Worker
6. Chat
7. Wild

後続修正後の Forecast 専用 policy:

1. primary: Coder1
2. primary が provider 作成失敗、または Generate 失敗した場合だけ external を1回試す
3. external は Coder2 / Coder3 / Coder4 のうち最初に作成できた1つ
4. external も失敗したら `FORECAST_TOPIC_GENERATION_FAILED` を表示
5. Worker / Chat / Wild へは落とさない

該当箇所:

- `cmd/picoclaw/runtime_idlechat.go:50-53`
- `cmd/picoclaw/runtime_idlechat.go:114-149`

runtime log では以下を確認:

```text
IdleChat: Forecast provider set to Coder1 deepseek (deepseek-coder), topic stock filling
```

live config では以下:

- `coder1.enabled=true`, `provider=deepseek`, `model=deepseek-coder`
- `coder2.enabled=true`, `provider=openai`, `model=gpt-4o-mini`
- `coder3.enabled=true`, `provider=claude`, `model=claude-sonnet-4-20250514`
- `coder4.enabled=true`, `provider=gemini`, `model=gemini-2.5-flash`
- `local_llm.enabled=true`, Chat/Worker/Wild は local OpenAI-compatible

したがって、現在の Forecast LLM は基本的に Coder1。Coder1 が provider 作成または Generate に失敗した場合だけ、外部LLMを1回試す。

## OpenAI 429 について

今回確認した現行ログには、`insufficient_quota`、`status 429`、OpenAI quota エラーは残っていなかった。

ただし、コード上は Coder2 が enabled かつ OpenAI provider であるため、Coder1 が無効、API key 不足、provider 作成失敗、Generate 失敗、設定変更などで失敗すると、外部LLM 1回として Coder2 OpenAI が使われ得る。

429 が出る箇所は、責務上は以下の LLM 呼び出し:

- `extractForecastKeyword()` の `o.forecastLLM().Generate(...)`
- `generateForecastTopic()` の `o.forecastLLM().Generate(...)`

該当箇所:

- `internal/application/idlechat/forecast_topic_generation.go:190-197`
- `internal/application/idlechat/forecast_topic_generation.go:144-147`

## Google News RSS 400

現行コード:

```go
rssURL := fmt.Sprintf("https://news.google.com/rss/search?q=%s&hl=ja&gl=JP&ceid=JP:ja",
    strings.ReplaceAll(keyword, " ", "+"))
```

該当箇所:

- `internal/application/idlechat/forecast_trend_sources.go:16-25`

これは空白だけを `+` に置換し、日本語などを URL encode しない。実測では以下の差が出た。

| URL | HTTP |
| --- | ---: |
| `https://news.google.com/rss/search?q=はしか感染拡大&hl=ja&gl=JP&ceid=JP:ja` | 400 |
| `https://news.google.com/rss/search?q=%E3%81%AF%E3%81%97%E3%81%8B%E6%84%9F%E6%9F%93%E6%8B%A1%E5%A4%A7&hl=ja&gl=JP&ceid=JP:ja` | 200 |

runtime log にも同種の 400 が複数ある。

```text
[Forecast] Google News RSS failed (q=生成AIのコスト効率): google news rss status 400
[Forecast] Google News RSS failed (q=AIによる雇用年齢シフト): google news rss status 400
[Forecast] Google News RSS failed (q=DuckDuckGo AI検索離れ): google news rss status 400
[Forecast] Google News RSS failed (q=はしか感染拡大): google news rss status 400
```

## 責務整理

外部取得:

- Google Trends / Reddit / Hatena / NHK RSS / Google News RSS。
- LLM provider quota とは別責務。
- HTTP status、URL 種別、domain、keyword をログに残すべき。

RSS / seed 整形:

- 見出し抽出、重複排除、domain scoring。
- 空 seed や HTTP 失敗は topic generation failure と混ぜない。

LLM keyword / topic 生成:

- Forecast LLM provider の責務。
- provider label、route、domain、phase、error_code をログで分けるべき。
- OpenAI 429 は TTS/ACK 失敗ではなく Forecast LLM provider 失敗。

topic stock:

- 起動時に全 domain の background fill が走る。
- Forecast お題の直前だけでなく、stock 補充時にも LLM / RSS / Google News が動く。
- 該当箇所: `internal/application/idlechat/forecast_topic_stock.go:152-164`, `209-223`

## 修正単位案

P0 ではない。TTS/ACK 本線とは分離する。

1. Google News RSS query encoding 修正
   - `strings.ReplaceAll(keyword, " ", "+")` を `url.QueryEscape(keyword)` へ変更。
   - 追加テスト: 日本語 keyword で URL が percent-encoded されること。
   - 検証: bad URL 400 / encoded URL 200 の再現をテストまたは helper で固定。

2. Forecast provider 選択ログの診断強化
   - 起動時に selected provider label は出ている。
   - LLM Generate error 時に phase (`keyword` / `topic`), domain, provider label 相当, error を出す。
   - OpenAI 429 が出た場合に TTS/ACK と混ざらないよう `[Forecast]` phase 付きで明示する。

3. Coder1 skip 時の Coder2 OpenAI 到達を明示
   - `selectForecastProvider()` は現在 Coder1 > Coder2 > Coder3 > Coder4。
   - skip ログはあるが、最終選択後の phase ごとの provider 表示はない。
   - Coder1 が壊れた場合に Coder2 OpenAI に進むことをテストとログで追いやすくする。

4. fallback 表示/生成の扱い見直し
   - `generateForecastTopic()` は LLM 失敗時に fallback topic を返していた。
   - ユーザー方針上、fallback で真因を隠さない。
   - LLM 失敗時は `FORECAST_TOPIC_GENERATION_FAILED` と `error_code` / `phase` / `domain` / `provider` を表示する。
   - topic stock へ失敗 topic は入れない。

## 次に実装するなら

最小の実装順:

1. Google News RSS encoding の修正とテスト。
2. Forecast LLM phase/provider/error_code ログの追加。
3. provider 選択の回帰テストを現仕様として固定。
4. fallback topic を stock に入れる条件の整理。

## 後続実装結果

実装済み:

- `fetchGoogleNewsSeeds()` の URL 生成を `url.QueryEscape(keyword)` に変更。
- 日本語 keyword の percent-encode テストを追加。
- Forecast LLM error log に `phase`, `domain`, `provider`, `error_code`, `error` を出すように変更。
- Coder1 が壊れている場合に Coder2 OpenAI を external provider として選ぶ provider 選択テストを追加。
- Forecast LLM 失敗時の汎用 topic / domain keyword fallback を廃止。
- Forecast topic 生成失敗時は `FORECAST_TOPIC_GENERATION_FAILED error_code=... phase=... domain=... provider=...` を表示する。
- Forecast topic stock 補充では、失敗 topic を保存しない。
- Forecast provider policy を `Coder1 primary -> external 1回 -> error` に変更。
- Forecast は Worker / Chat / Wild へ落とさない。
- primary Generate 失敗後に external を1回だけ使うテストを追加。

検証:

- `GOCACHE=/tmp/picoclaw-go-cache go test ./internal/application/idlechat`
- `GOCACHE=/tmp/picoclaw-go-cache go test ./cmd/picoclaw`
- `git diff --check`

## 2026-05-27 live smoke

`df8319e` 反映後に `make install`、`picoclaw.service` 再起動を行い、起動ログで Forecast provider policy の反映を確認した。

起動ログ:

- `IdleChat: Forecast provider set to primary=Coder1 deepseek (deepseek-coder) external=Coder2 openai (gpt-4o-mini), topic stock filling`

Forecast smoke:

- session: `forecast-1779867061`
- topic: `[AI技術] AI採用と年齢差別が変える雇用の信頼構造`
- status: topic 確定、transcript 2件、pending 0
- console error: 0
- smoke 範囲の Forecast log では Google News RSS 400 なし
- smoke 範囲の Forecast log では OpenAI 429 / `insufficient_quota` なし
- smoke 範囲の Forecast log では `FORECAST_TOPIC_GENERATION_FAILED` なし

失敗系:

- live config を壊す fault injection は実施していない。
- targeted test で `Coder1失敗 -> external 1回 -> external失敗なら明示エラー` を確認済み。

残課題:

- smoke 中に Viewer/TTS ACK 側で `status=fallback` が観測された。
- これは Forecast provider / topic generation の失敗ではなく、Viewer の TTS playback ACK 表記の別問題として分離する。
- 後続修正で ACK `status=fallback` は `status=error` と `error_code` に正規化する。
