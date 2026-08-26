# IdleChat / Forecast 不安定化精査

- 調査日時: 2026-08-26 08:27 JST
- 対象: 2026-08-25 18:14:26 UTC に起動した `rencrow.service` (CORE)
- スコープ: 調査後、COREの生命周期Boundaryを修正し、build・配備・再起動・正規Forecast E2Eを実施した。

## 結論

`IdleChat/Forecastが不安定`という所見は **confirmed**。ただし、元の集計値に2点補正が必要である。

1. Daily source brief 失敗は72件で一致する。内訳は `context canceled` 53、実際の `EMPTY_FINAL_CONTENT` 17、JSON不完全1、JSON objectなし1。文字列検索の19件のうち2件は、Forecast prompt内の過去topic文字列に含まれたもので、Daily briefの失敗ではない。
2. 調査着手時点の topic 最終失敗は13件。内訳は news 5、external 4、movie 4で、すべて最終エラーは `topic_generation_failed: context canceled`。診断event 49行は13 sessionの再試行eventを含むため、49 sessionではない。調査中も現象は継続し、23:26 UTCに14件目が発生した。
3. Forecastの `signal: killed` は5件ではなく6件。すべてHeartbeat watchdogのinterruptと同一時刻である。

主原因は二つある。

- **Forecast watchdogの進捗stage不整合**: Forecastはドメイン告知のTTS完了後、CodexExeで長いdialogue episode生成へ入るが、watchdog stageを生成中へ更新しない。Heartbeatは2分間更新のない `tts_done`をstallとみなし、IdleChatの親contextをcancelする。`CodexExecRunner` はそのcontext配下のprocess groupを停止するため `signal: killed` となる。live設定のCodex timeoutは600,000 msで、6件は約2.4〜2.9分でkillされており、10分timeoutは原因ではない。
- **Workerの長時間無token占有とcancel連鎖**: Daily briefは64記事を1件ずつ直列処理し、1件ごとに最大10分のcontextを作る。Gatewayで観測したWorker失敗は `first_token_ms=0`のまま281〜437秒占有して `EMPTY_FINAL_CONTENT`へ至る。調査時点のGateway集計はWorker started 29 / completed 6 / failed 23。この長時間リクエストと前景IdleChatが同じ制限queueを使い、topic側で119〜159秒queue後のcancelも発生している。

## 件数と時系列の証拠

### Daily source brief

- CORE: failed 72件、paused-for-foreground 7件。
- 18:22〜20:51 UTCのGateway Worker失敗は多くが400秒前後、queue 0 ms、first token 0 ms。これはCORE client queue待ちではなく、Worker targetが生成開始後に有効contentを1 tokenも返さない現象を示す。
- 19:00 JST相当ではなく19:00 UTC (04:00 JST) の定時seed再取得と、起動後の先行enrichmentが重なった時間帯に `context canceled` が短時間に連鎖した。
- `beginDailySeedEnrichment` はcacheのstatusでのみ多重起動を防ぐ。daily seed再取得がcacheを更新すると新しいenrichmentが始まり得る一方、orchestratorにはジョブ全体のcancel/joinがなく、実行中batch用cancelは単一field `dailyEnrichmentCancel` である。これは重複実行時の所有権を表現できない。
- 53件の `context canceled` 全件を1つのローカルcancel発行箇所に個別対応させるtrace IDがCORE logにない。したがって、重複enrichmentとshared cancel fieldが個々の53件すべての直接原因であることは **uncertain**。一方、長時間Worker占有と前景interruptによるcancel連鎖自体はlogでconfirmed。

### Topic generation

- `TopicGenerator` は同じ親contextで3回再試行する。contextが既にcanceledの場合でも3回すべて呼ぶため、1 sessionがattempt 1/2/3 + finalの4失敗eventを出す。
- 19:41:32 UTCのnews失敗は、topic generation stageが120秒に達しHeartbeat watchdogがinterruptしたことを直接確認した。同時刻にChatWorkerはqueue 119,114 msでfailed。
- 他の多くは、session開始直後に親contextまたは下位providerから `context canceled` を受け、3回の再試行が同一時刻で終了している。これらはGatewayへrequestが到達しない例もあり、COREの親context生命週期とqueue取得前のcancelが直接境界である。
- トピック生成が常に壊れているわけではない。ChatWorkerは同期間に40 started / 40 completedの通常応答も返しており、モデルやGatewayの全面停止はrefuted。

### Forecast

killの時刻は 19:03:32、19:45:32、20:33:32、21:26:32、22:17:32、23:03:32 UTC。すべて同時刻に次の順でlogが出る。

1. `watchdog recovery triggered ... stage=tts_done ... age=145〜176s`
2. `Interrupted: reason=heartbeat_idlechat_sequence_stall`
3. `Domain stopped before dialogue playback ... CodexExe ... signal: killed`

Forecastのコードはドメイン告知の `waitForTTSDoneForEvent`まではstageを更新するが、その後の `popForecastTopic` および `prepareDialogueEpisode` 前後で `markWatchdogStage` を呼ばない。このため正常に長いCodex生成をstallと誤認する。

## 既存所見の切り分け

過去の調査で確認されたgenre prompt/validator不整合、Shiro voiceの旧 `male_01`、日本語uptakeのfalse negativeは、現在配備されたCORE commit `d0d0290`に取り込み済み。今回の `context canceled` / `signal: killed` の原因ではない。

## CLI / LLM / Boundary 分類

| 工程 | 区分 | owner | 入力 -> 出力 | 失敗status / 証跡 |
|---|---|---|---|---|
| seed取得、URL本文取得、件数集計 | CLI | CORE | config/URL/log -> cache/集計 | source_unavailable, journal |
| 翻訳、用語抽出、brief、topic/dialogue文生成 | LLM | CORE -> RenCrow_LLM / CodexExe | bounded prompt -> schema/text | EMPTY_FINAL_CONTENT, context canceled, structured diagnostic |
| queue、timeout、context cancel、schema validation、watchdog、cache反映 | Boundary | CORE / RenCrow_LLM | request/state -> accepted/failed/receipt | journal, request_id, watchdog recovery |

LLMは翻訳・要約・創作生成の意味処理に使われる。一方、重複実行排除、queue、timeout、cancel、retry打ち切りは決定的Boundaryで扱うべきで、今回の不安定化は主にこのBoundaryの生命週期不整合である。

## 修正内容

1. Forecastのtopic取得・dialogue生成開始・成功・失敗をwatchdog stageに追加した。`dialogue_generation`はCodexExeの10分上限内では汎用2分watchdogが回収せず、上限後は回収可能にした。
2. TopicGeneratorは呼び出し前とprovider応答後に `ctx.Err()` を確認し、cancel済みcontextでattempt 2/3を実行しない。
3. Daily enrichmentにjob単位のgeneration、context、cancel、done所有権を追加した。新cacheは旧jobをcancel・joinしてから開始し、旧generationのcache公開を拒否する。foreground interruptは現行jobのbatchだけをcancelし、同じ記事を新contextで再試行する。

4. RenCrow_LLM Gatewayへnetwork streaming専用の `first_output_timeout` を追加した。response header待ちを含む最初の有効content/tool callまでを制限し、到達後は既存の生成全体timeoutだけを維持する。90秒の初回live試験は物理Workerのprompt処理も打ち切ったため採用せず、異常群281〜437秒より手前の240秒へ補正した。
5. Dailyの翻訳・用語抽出・補足・要約requestは、COREの既存contract `ReasoningEffortLow` を明示するようにした。live payloadで `think=low`、`reasoning_effort=low`、`chat_template_kwargs.enable_thinking=true` がGateway受信時とtarget送信時の双方に存在することを確認した。

## 修正後の検証

- `go test ./internal/application/idlechat -count=1`: pass
- `go test -race ./internal/application/idlechat -count=1`: pass
- Makefileの正規package集合に対する `go vet`: pass
- `make build`: pass
- build / installed binary SHA-256: `5d0ac624da7e9a3d6f99901622a964b08519467b45a9f1851ce528d7d8e292dd` で一致
- 再起動後: `rencrow.service active/running`、`NRestarts=0`、`/health status=ok`、Mio/Worker/Shiro/Kuro/MidoriのRenCrow_LLM readinessすべてok
- 正規 `POST /viewer/idlechat/forecast` E2E: session `forecast-1787702298`で `dialogue_generation` を601秒まで維持し、Heartbeatは毎分 `status=ok`。旧障害域145〜176秒を超えてもwatchdog interrupt / `signal: killed` なし。603秒後に `dialogue_ready turns=100` へ進み、Viewer用transcriptへMio/Shiroの4発話が到達した。検証sessionはその後明示停止した。
- 状態復帰の再起動後: `disabled=false`、`manual_mode=false`、`chat_active=false`。
- RenCrow_LLM: config/httpapi focused test、first-output race test、`go vet ./...`、`go build ./...` pass。`first_output_timeout`配備時のbuild / installed SHA-256は `2e885086c14b6dfa923ecf9f4fe5c6ff2477afef447c1ba519f5c3c582ff02c8` で一致。続く固定Runtime ingress切替と`api_key_file`配備後は `5b1ca750433b6dffb79014d468ffe317f7b5503583aa391cb017e2d8965515c9` で一致。
- live Daily request `llmreq_d0b94500-e788-4fd5-a36f-25ff3cec133b` は現行live route上で無出力のまま `generation_ms=90003` に `TARGET_FIRST_OUTPUT_TIMEOUT` でfailedとなり、直後0.754秒で次のDaily requestが開始した。capacity解放とtyped retryable errorは確認したが、90秒設定は後続のprompt処理まで打ち切ったため240秒へ補正した。
- low-reasoning配備後も旧8082 proxy経由のrequest `llmreq_e9344caf-538b-42f5-8eeb-6efdb8990f80` は有効contentを返さず、240,004 msで同typed errorとなった。過去調査でも8082 proxyがBackendのtool/final contractを欠落させる境界と判定されていたため、reasoning exhaustion単独原因の仮説は棄却した。
- Gateway targetに排他的な`api_key_file` credential参照を追加し、既存0600 token fileを使って固定Runtime ingress `192.168.1.31:8091/v1/backends/<id>`へWorker、ChatWorker、Coder、Wildを切り替えた。Portの変更・再利用は行っていない。
- 切替後のDaily Workerは69,075 ms / first token 511 ms、81,646 ms / first token 559 ms、78,680 ms / first token 377 msで連続completed。Shiro、Midori、Coder1のalias smokeもそれぞれ有効contentを返した。Gateway processの接続は8091だけで、8082/8084へのlive socketは0件だった。
- CORE `runtime_topology`にはdisabled `local_llm`や旧ops互換用の8082/8084参照が残るが、実生成は`llm_gateway.base_url=http://127.0.0.1:8090`からRenCrow_LLM Gatewayへ入る。これらはlive consumerではなく、今回の切替で削除・Port変更していない。
- 次回04:00 JSTの定時Daily enrichmentにおける長期再発観測は時間依存のため未実施。

## 確認バイアス再点検

- 成功例も集計し、Gateway全面停止仮説を棄却した。
- 文字列検索の `EMPTY_FINAL_CONTENT=19` を個別eventへ戻し、17件に補正した。
- Forecastは設定値だけでなく、全6件の `watchdog -> interrupt -> killed` の順序を確認した。
- 過去のgenre/TTS/uptake不具合を現行commitと照合し、今回の原因から除外した。
- trace不足で53 cancel全件の個別起点を確定できない境界は `uncertain` のまま残した。
