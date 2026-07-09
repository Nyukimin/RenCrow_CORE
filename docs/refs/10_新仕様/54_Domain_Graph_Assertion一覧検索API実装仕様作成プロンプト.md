# Domain Graph Assertion 一覧検索 API 実装仕様作成プロンプト

あなたは RenCrow / picoclaw_multiLLM の仕様整理担当兼実装仕様作成担当です。

目的は、`docs/10_新仕様/53_Domain_Graph_Assertion一覧検索API実装仕様.md` を入口として、Domain Graph assertion の一覧・検索 API を実装できる粒度まで落とし込むことです。

今回は実装そのものは行いません。仕様調査、現行 code の確認、責務分解、段階実装計画、検証条件、停止条件を整理し、実装担当者がそのまま着手できる実装仕様を作成してください。

作成先は次とします。

- `docs/10_新仕様/55_Domain_Graph_Assertion一覧検索API詳細実装仕様.md`

## 最初に読むもの

必ず以下を読むこと。

1. `AGENTS.md`
2. `CLAUDE.md`
3. `docs/01_正本仕様/実装仕様.md`
4. `docs/10_新仕様/09_Memory_SourceRegistry仕様.md`
5. `docs/10_新仕様/52_Domain_Graph_DB経路実装仕様.md`
6. `docs/10_新仕様/53_Domain_Graph_Assertion一覧検索API実装仕様.md`
7. `docs/10_新仕様/49_Movie_Graph_Mio_Topic仕様.md`
8. `docs/10_新仕様/50_Hobby_Graph_Mio_Topic仕様.md`
9. `docs/10_新仕様/10_検証仕様.md`

必要に応じて以下も確認すること。

- `docs/10_新仕様/13_実装項目インベントリ.md`
- `docs/10_新仕様/32_E2E_runtime確認チェックリスト.md`
- `rules/common/rules_state_management.md`
- `rules/common/rules_logging.md`
- `rules/common/rules_testing.md`
- `rules/common/rules_observation_verification.md`

## production code 確認対象

少なくとも以下を `rg` とファイル読み取りで確認すること。

```text
internal/infrastructure/persistence/conversation/l1_sqlite_domain_graph.go
internal/infrastructure/persistence/conversation/l1_sqlite_schema.go
internal/infrastructure/persistence/conversation/l1_sqlite_types.go
internal/infrastructure/persistence/conversation/l1_sqlite_store_test.go
internal/adapter/viewer/source_registry_handler.go
internal/adapter/viewer/source_registry_handler_test.go
cmd/picoclaw/runtime_viewer_handlers.go
cmd/picoclaw/routes.go
pkg/rencrowclient/client.go
internal/adapter/viewer/assets/js/tabs/memory.js
internal/adapter/viewer/viewer.html
```

確認観点:

- `domain_graph_assertion` schema と Go 型の現状
- validated staging から domain_graph へ promote する既存経路
- Source Registry / Memory 系 handler の route 追加パターン
- L1 store が未設定のときに 503 を返す runtime handler パターン
- `pkg/rencrowclient` の current view validation パターン
- Viewer Memory / Source Registry UI の fetch failure 表示パターン
- Node contract test / Go handler test の既存パターン

## 実装仕様で必ず守る原則

- Domain Graph assertion は外部世界の関係事実の current view であり、raw web text ではない。
- pending staging を assertion current view として扱わない。
- Qdrant sync 済みと誤認させない。
- source URL / source ID / raw hash / validation status / confidence / evidence を失わない。
- malformed current view を client success として扱わない。
- Viewer 表示 state と永続化 state を混同しない。
- fallback を成功扱いしない。
- L1 store 無効時は route 404 ではなく 503 `domain graph unavailable` とする。
- 初期 Viewer 表示は重い graph visualization ではなく、一覧・要約・details に限定する。
- raw text 全文を初期表示しない。
- evidence JSON は `details` 等に閉じる。
- Chat / Worker / Coder / Adapter / Application / Domain / Infrastructure の責務境界を崩さない。

## 調査タスク

次を調査し、実装仕様に反映してください。

### 1. Persistence

- `domain_graph_assertion` の schema
- `L1DomainGraphAssertion` の JSON response 化に必要な field
- query filter の SQL 組み立て方針
- `limit` / `offset` の validation
- `validation_status` の既定値と許可値
- evidence JSON の scan / marshal / unmarshal

### 2. Viewer API handler

- 新規 handler package / file の置き場所
- `GET /viewer/domain-graph/assertions` の query parse
- L1 store 未設定時の unavailable handler
- response DTO
- invalid request の HTTP status と body
- raw text を返さないことの確認

### 3. Runtime wiring

- `cmd/picoclaw/runtime_viewer_handlers.go` に追加する依存
- `cmd/picoclaw/routes.go` に追加する route
- `/viewer/runtime-config` readiness に出すかどうか
- L1 store disabled の live runtime で blocked state をどう見せるか

### 4. Client

- `DomainGraphAssertionsRequest`
- `DomainGraphAssertion`
- `DomainGraphAssertionsResponse`
- query string 組み立て
- malformed current view validation
- duplicate ID detection
- timestamp / confidence / status validation

### 5. Viewer UI

- 初期表示で出す要約
- domain 別 count
- source_id 別 count
- latest 10件
- low confidence assertion
- evidence details
- fetch failure 表示
- mobile / narrow width で URL がはみ出さない表示

### 6. Tests

- persistence unit test
- handler test
- client test
- Viewer Node contract test
- runtime route readiness test
- 必要なら live manual check

## 実装仕様の構成

作成する `55_Domain_Graph_Assertion一覧検索API詳細実装仕様.md` は、必ず以下の構成にしてください。

```markdown
# Domain Graph Assertion 一覧検索 API 詳細実装仕様

## 1. 目的

## 2. 参考仕様

## 3. 現行実装調査
### 3.1 Domain Graph persistence
### 3.2 Source Registry promote
### 3.3 Viewer handler / runtime route
### 3.4 rencrowclient validation
### 3.5 Viewer Memory UI

## 4. 実装範囲
### 4.1 Phase 1 で実装すること
### 4.2 Phase 1 で実装しないこと
### 4.3 Phase 2 以降

## 5. アーキテクチャ
### 5.1 persistence
### 5.2 adapter / Viewer API
### 5.3 cmd runtime
### 5.4 client
### 5.5 Viewer UI

## 6. データ契約
### 6.1 query DTO
### 6.2 response DTO
### 6.3 assertion item
### 6.4 evidence JSON
### 6.5 error response

## 7. Query policy
### 7.1 filters
### 7.2 normalization
### 7.3 pagination
### 7.4 default validation_status
### 7.5 sort order

## 8. Viewer API 仕様
### 8.1 endpoint
### 8.2 success response
### 8.3 error response
### 8.4 unavailable response

## 9. Client 仕様
### 9.1 request
### 9.2 response validation
### 9.3 malformed current view rejection

## 10. Viewer 表示仕様
### 10.1 summary
### 10.2 table
### 10.3 details
### 10.4 failure state
### 10.5 responsive constraints

## 11. Logs / Evidence

## 12. Tests
### 12.1 Persistence
### 12.2 Handler
### 12.3 Client
### 12.4 Viewer Node contract
### 12.5 Runtime route
### 12.6 Live manual check

## 13. 実装手順

## 14. 完了条件

## 15. 停止条件

## 16. 将来拡張
```

## 実装仕様で明記する完了条件

次を必ず完了条件に含めてください。

- validated staging から promote した assertion が `GET /viewer/domain-graph/assertions` で見える
- domain / entity_type / entity_id / relation_type / source_id で絞り込める
- `limit` / `offset` が効く
- L1 store 無効時は 503 `domain graph unavailable`
- `pkg/rencrowclient` が malformed current view を拒否する
- Viewer が fetch failure を stale table で隠さない
- raw text / pending staging / Qdrant sync と assertion current view を混同しない

## 停止条件

次に該当する場合は実装仕様内で停止条件として明記してください。

- `domain_graph_assertion` schema と現行 code が矛盾している
- L1 store の無効時 handler pattern が既存 runtime と合わない
- Viewer に追加することで Memory / Source Registry の既存 UI が大きく崩れる
- evidence JSON に secret / token / raw web text 全文を表示する必要が出る
- Qdrant sync の状態を同時実装しないと誤表示を避けられない

## 出力ルール

- 実装そのものは行わない。
- production code を変更しない。
- `55_Domain_Graph_Assertion一覧検索API詳細実装仕様.md` だけを作成または更新する。
- 仕様内に、実装対象ファイル、追加する関数、追加するテスト名、確認コマンドを具体的に書く。
- 曖昧な項目は「未定」ではなく、判断理由つきで Phase 1 / Phase 2 / 停止条件へ分類する。
