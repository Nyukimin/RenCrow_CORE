# Source Registry Domain Graph Promote UI 実装仕様

## 1. 目的

Source Registry staging の validated item を、Viewer から `target=domain_graph` として `domain_graph_assertion` へ promote できるようにする。

前段の `55_Domain_Graph_Assertion一覧検索API詳細実装仕様.md` で、保存済み assertion を Viewer / API / `pkg/rencrowclient` から一覧確認できる current view は実装済みである。一方、Viewer の Source Registry staging UI には `News` / `Knowledge` / `Memory` の promote button しかなく、既存 backend が受け付けている `domain_graph` promote を操作できない。

この仕様では、Viewer の Memory tab 内で Source Registry staging item を検証し、Domain Graph へ昇格し、直後に Domain Graph Assertions 表示で確認できる操作経路を完成させる。

## 2. 参考仕様

- `docs/01_正本仕様/実装仕様.md`
- `docs/10_新仕様/09_Memory_SourceRegistry仕様.md`
- `docs/10_新仕様/49_Movie_Graph_Mio_Topic仕様.md`
- `docs/10_新仕様/50_Hobby_Graph_Mio_Topic仕様.md`
- `docs/10_新仕様/52_Domain_Graph_DB経路実装仕様.md`
- `docs/10_新仕様/55_Domain_Graph_Assertion一覧検索API詳細実装仕様.md`
- `docs/10_新仕様/10_検証仕様.md`

## 3. 現行実装調査

### 3.1 backend

実装済み:

- `internal/adapter/viewer/source_registry_handler.go`
  - `/viewer/source-registry?action=promote` は `target=domain_graph` を受け付ける。
  - 必須 field は `id`、`domain`、`entity_type`。
  - 任意 field は `entity_id`、`relation_type`、`confidence`。
  - `confidence` 未指定時は `0.5`。
  - promote 後は `{target:"domain_graph", item:<L1DomainGraphAssertion>}` を返す。
- `internal/adapter/viewer/source_registry_handler_test.go`
  - domain graph promote の handler test は存在する。
- `pkg/rencrowclient/client.go`
  - `SourceRegistryPromoteRequest` は `Domain`、`EntityType`、`EntityID`、`RelationType`、`Confidence` を持つ。
  - client validation は `target=domain_graph` の `domain` / `entity_type` 必須、`confidence` 範囲を検証する。

不足:

- Viewer から `target=domain_graph` を送る UI がない。
- Viewer の promote 後に Domain Graph Assertions を自動更新しない。
- Viewer Node contract test で domain_graph promote payload が固定されていない。

### 3.2 Viewer Memory tab

実装済み:

- `internal/adapter/viewer/viewer.html`
  - Source Registry staging controls:
    - `sourceRegistryStagingStatus`
    - `sourceRegistryStagingTrust`
    - `sourceRegistryStagingCategory`
    - `sourceRegistryStagingDomain`
    - `sourceRegistryStagingNamespace`
  - Domain Graph Assertions controls:
    - `domainGraphDomain`
    - `domainGraphEntityType`
    - `domainGraphSourceID`
    - `domainGraphStatusFilter`
    - `domainGraphRefreshBtn`
- `internal/adapter/viewer/assets/js/tabs/memory.js`
  - `promoteSourceRegistryStaging(id, target)` は `news` / `knowledge` / `memory` の payload を作る。
  - `renderSourceRegistryStaging()` は `News` / `Knowledge` / `Memory` button を出す。
  - `refreshDomainGraphAssertions()` は実装済み。

不足:

- Domain Graph promote 用の入力欄がない。
- `promoteSourceRegistryStaging()` が `domain_graph` payload を作らない。
- `renderSourceRegistryStaging()` に `Domain Graph` button がない。
- promote 成功後に `refreshDomainGraphAssertions()` を呼ばない。

## 4. 実装範囲

### 4.1 実装すること

- Viewer Memory tab に Domain Graph promote 用入力欄を追加する。
- Source Registry staging row の action に `Graph` button を追加する。
- `promoteSourceRegistryStaging(id, "domain_graph")` で domain graph payload を組み立てる。
- promote 成功後に Source Registry staging / Memory snapshot / Domain Graph Assertions を更新する。
- Viewer Node contract test を追加または更新する。
- 既存 backend / client validation test は回帰確認として実行する。

### 4.2 実装しないこと

- backend の promote API 仕様変更。
- `domain_graph_assertion` schema 変更。
- assertion 一覧 API の拡張。
- Movie 固有 DB への変換。
- `movie_topic_candidates` 生成。
- Domain Graph 専用 tab。
- graph visualization。

## 5. UI 仕様

### 5.1 追加する入力欄

Source Registry staging controls に次を追加する。

```html
<input id="sourceRegistryStagingGraphDomain" placeholder="graph domain: movie">
<input id="sourceRegistryStagingGraphEntityType" placeholder="entity type: work / person">
<input id="sourceRegistryStagingGraphEntityID" placeholder="entity id: movie:1">
<input id="sourceRegistryStagingGraphRelation" placeholder="relation: catalog_fact">
<input id="sourceRegistryStagingGraphConfidence" placeholder="confidence 0.0-1.0">
```

配置は既存の `sourceRegistryStagingDomain` / `sourceRegistryStagingNamespace` の近くでよい。専用 card や modal は作らない。

### 5.2 default 値

Viewer 側 default:

| field | default | 理由 |
| --- | --- | --- |
| `domain` | `domainGraphDomain.value` があればそれ、なければ `sourceRegistryStagingGraphDomain.value`、それもなければ `movie` | 直前の Domain Graph Assertions filter と揃える |
| `entity_type` | `sourceRegistryStagingGraphEntityType.value`、なければ `work` | Movie / manga などの作品 item を初期対象にする |
| `entity_id` | 空なら送らない | ID は推測で作らない |
| `relation_type` | 空なら送らない | relation は推測で作らない |
| `confidence` | 空なら送らない | backend default `0.5` を使う |

注意:

- `entity_id` は staging ID から自動生成しない。
- `relation_type` は `catalog_fact` を勝手に入れない。明示入力があるときだけ送る。
- `confidence` は数値として finite な場合だけ送る。

### 5.3 button 表示

`renderSourceRegistryStaging()` の action column に `Graph` button を追加する。

```html
<button class="ctl-btn" onclick="promoteSourceRegistryStaging(&quot;...&quot;,&quot;domain_graph&quot;)">Graph</button>
```

button は pending / rejected item にも表示してよい。backend が rejected / pending promote を拒否し、Viewer は response body を status line に出す。UI 側で隠して状態不一致を作らない。

## 6. JS 仕様

### 6.1 payload builder

`promoteSourceRegistryStaging(id, target)` に `domain_graph` branch を追加する。

疑似コード:

```js
} else if (promotionTarget === 'domain_graph') {
  const graphDomain = document.getElementById('sourceRegistryStagingGraphDomain');
  const graphEntityType = document.getElementById('sourceRegistryStagingGraphEntityType');
  const graphEntityID = document.getElementById('sourceRegistryStagingGraphEntityID');
  const graphRelation = document.getElementById('sourceRegistryStagingGraphRelation');
  const graphConfidence = document.getElementById('sourceRegistryStagingGraphConfidence');
  const domainFilter = document.getElementById('domainGraphDomain');

  payload.domain = graphDomain && graphDomain.value.trim()
    ? graphDomain.value.trim()
    : (domainFilter && domainFilter.value.trim() ? domainFilter.value.trim() : 'movie');
  payload.entity_type = graphEntityType && graphEntityType.value.trim()
    ? graphEntityType.value.trim()
    : 'work';
  if (graphEntityID && graphEntityID.value.trim()) payload.entity_id = graphEntityID.value.trim();
  if (graphRelation && graphRelation.value.trim()) payload.relation_type = graphRelation.value.trim();
  if (graphConfidence && graphConfidence.value.trim()) {
    const confidence = Number(graphConfidence.value.trim());
    if (Number.isFinite(confidence)) payload.confidence = confidence;
  }
}
```

### 6.2 success handling

promote 成功後:

```js
setSourceRegistryStagingStatus('promoted=' + (data.target || promotionTarget), false);
refreshSourceRegistryStaging();
refreshMemorySnapshot();
if (promotionTarget === 'domain_graph') refreshDomainGraphAssertions();
```

`refreshMemorySnapshot()` は内部で `refreshDomainGraphAssertions()` を呼ぶが、domain graph promote 直後の見える化を明確にするため、`promotionTarget === "domain_graph"` の場合は直接呼んでよい。ただし二重 fetch が問題になる場合は `refreshMemorySnapshot()` 側の内部 refresh に一本化してもよい。その場合は test で Domain Graph refresh が呼ばれる経路を固定する。

### 6.3 error handling

既存通り、response body 付きで status line に出す。

例:

```text
HTTP 400: domain_graph domain is required
HTTP 400: failed to promote source registry staging item to domain graph
```

禁止:

- console-only error。
- `source registry staging promotion failed` の generic message で response body を潰す。
- backend error を成功扱いして Domain Graph Assertions を refresh する。

## 7. HTML / CSS 仕様

### 7.1 変更ファイル

- `internal/adapter/viewer/viewer.html`
- `internal/adapter/viewer/assets/js/tabs/memory.js`
- `internal/adapter/viewer/viewer_memory_panel.test.mjs`

CSS 追加は原則不要。既存 `.filters` と `.ctl-btn` を使う。

### 7.2 responsive

入力欄は既存 `.filters` の flex wrap に乗せる。新規 card / nested card は作らない。

長い `entity_id` / `relation_type` は input 内に留め、table 側へ新しい列を増やさない。

## 8. テスト仕様

### 8.1 Viewer static / contract

`internal/adapter/viewer/viewer_memory_panel.test.mjs` に追加または既存 test を拡張する。

確認:

- HTML に以下の ID が存在する。
  - `sourceRegistryStagingGraphDomain`
  - `sourceRegistryStagingGraphEntityType`
  - `sourceRegistryStagingGraphEntityID`
  - `sourceRegistryStagingGraphRelation`
  - `sourceRegistryStagingGraphConfidence`
- `renderSourceRegistryStaging()` が `domain_graph` action button を出す。
- `promoteSourceRegistryStaging("stg_1", "domain_graph")` が以下の payload を送る。
  - `target: "domain_graph"`
  - `domain`
  - `entity_type`
  - 任意入力がある場合 `entity_id`
  - 任意入力がある場合 `relation_type`
  - finite な数値入力がある場合 `confidence`
- promote 成功後に `refreshDomainGraphAssertions()` が呼ばれる。
- promote 失敗時に response body が status line に出る。

推奨 test 名:

```js
test('viewer builds source registry domain graph promotion payload', async () => {})
test('viewer refreshes domain graph assertions after source registry graph promotion', async () => {})
```

既存 `viewer renders source registry action errors with response body` に domain_graph 失敗 case を足してもよい。

### 8.2 Handler regression

backend は実装済みだが、回帰確認として次を実行する。

```bash
GOCACHE=/tmp/picoclaw-go-cache go test ./internal/adapter/viewer
```

最低限、既存の domain graph promote handler test が通ること。

### 8.3 Client regression

`pkg/rencrowclient` は実装済みだが、domain graph promote request validation を壊していないことを確認する。

```bash
GOCACHE=/tmp/picoclaw-go-cache go test ./pkg/rencrowclient
```

### 8.4 Viewer Node contract

```bash
node --test internal/adapter/viewer/viewer_memory_panel.test.mjs
```

## 9. 実装手順

1. `viewer.html` に Domain Graph promote 用 input を追加する。
2. `viewer_memory_panel.test.mjs` の hook test に新 ID を追加する。
3. `renderSourceRegistryStaging()` に `Graph` button を追加する。
4. `promoteSourceRegistryStaging()` に `domain_graph` branch を追加する。
5. promote 成功時に Domain Graph Assertions refresh を呼ぶ。
6. Node contract test を先に落としてから修正を通す。
7. Go regression test を実行する。
8. `git diff --check` を実行する。

## 10. 完了条件

- Viewer で Source Registry staging item の action に `Graph` button が出る。
- Viewer から `target=domain_graph` payload を送れる。
- `domain` / `entity_type` が payload に必ず入る。
- `entity_id` / `relation_type` / `confidence` は入力がある場合だけ入る。
- promote 成功後に Domain Graph Assertions 表示が更新される。
- promote 失敗時に response body が Viewer に表示される。
- 既存の News / Knowledge / Memory promote が壊れていない。
- `node --test internal/adapter/viewer/viewer_memory_panel.test.mjs` が通る。
- `GOCACHE=/tmp/picoclaw-go-cache go test ./internal/adapter/viewer ./pkg/rencrowclient` が通る。
- `git diff --check` が通る。

## 11. 停止条件

以下を見つけた場合は実装を止め、別タスクとして報告する。

- backend の `target=domain_graph` promote が current schema と矛盾している。
- `domain_graph_assertion` への promote 成功後、一覧 API に表示されない。
- Viewer から raw web text 全文を Domain Graph UI に出す必要が出た。
- secret / token / Authorization / cookie を evidence や payload に表示する必要が出た。
- Movie 固有 DB への変換をしないと UI 操作が成立しない、という前提が出た。

## 12. 次段候補

この仕様の完了後に進める候補:

- Domain Graph 専用 tab。
- Movie adapter: `domain_graph_assertion` から `movies` / `people` / `movie_people` へ反映。
- `movie_topic_candidates` 生成。
- Ops readiness に `domain_graph_available` / `domain_graph_status_available` を追加。
