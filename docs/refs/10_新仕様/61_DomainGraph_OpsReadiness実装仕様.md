# Domain Graph Ops Readiness 実装仕様

## 1. 目的

Domain Graph の Viewer API availability を `/viewer/runtime-config` の `runtime_readiness` に表示する。

`GET /viewer/domain-graph/assertions` と `POST /viewer/movie-catalog/domain-graph-sync` が実装されても、Ops / runtime-config では Source Registry や Memory Layers と同じ粒度で Domain Graph の状態を確認できない。これでは「Domain Graph が使えるのか、handler が登録されているだけなのか」を切り分けにくい。

## 2. 実装範囲

### 2.1 実装すること

- `RuntimeDependencyReadiness` に次を追加する。
  - `domain_graph_available`
  - `domain_graph_status_available`
- `buildRuntimeDependencyReadiness(...)` で値を算出する。
- `pkg/rencrowclient` の runtime-config response validation に追加する。
- Viewer / cmd / client test を更新する。

### 2.2 実装しないこと

- Viewer Ops UI の新しい表示カード。
- Qdrant sync status。
- Domain Graph 専用 tab。

## 3. readiness 定義

| field | true condition |
| --- | --- |
| `domain_graph_available` | Conversation が enabled で、L1 SQLite path が設定されている |
| `domain_graph_status_available` | `viewerDomainGraphAssertions` handler が登録されている |

Source Registry と同じ L1-backed current view として扱う。

## 4. validation

`pkg/rencrowclient.RuntimeConfig` は次を malformed current view として拒否する。

- `domain_graph_available` / `domain_graph_status_available` が欠落している。
- `domain_graph_available=true` なのに `conversation_enabled=false`。
- `domain_graph_available=true` なのに `l1_sqlite_config_present=false`。
- `domain_graph_available=true` なのに `domain_graph_status_available=false`。

## 5. 変更ファイル

- `internal/adapter/viewer/debug_system_handler.go`
- `internal/adapter/viewer/runtime_config_test.go`
- `cmd/rencrow/runtime_readiness.go`
- `cmd/rencrow/runtime_readiness_test.go`
- `pkg/rencrowclient/client.go`
- `pkg/rencrowclient/client_test.go`

## 6. 検証コマンド

```bash
GOCACHE=/tmp/rencrow-go-cache go test ./internal/adapter/viewer ./cmd/rencrow ./pkg/rencrowclient
git diff --check
```

## 7. 完了条件

- `/viewer/runtime-config` に Domain Graph readiness が出る。
- client validation が field 欠落や不整合を成功扱いしない。
- Source Registry / Memory Layers の既存 readiness を壊さない。
