# Route別Runtime検証 E2Eテスト

Autonomous Executor のroute別runtime動作を検証するE2Eテストスイート。

## 概要

全11テストケースで以下を検証：
- ✅ CHAT routeがautonomous executorをバイパス
- ✅ OPS routeの成功・retry・repair失敗フロー
- ✅ CODE/CODE1/CODE2/CODE3 routeのcoder統合
- ✅ PLAN/ANALYZE/RESEARCH routeのreasoning統合

## テスト実行

### 全テスト実行

```bash
go test -tags=e2e ./test/autonomous_verification/ -v
```

### 特定テストのみ実行

```bash
# OPS retryフロー確認
go test -tags=e2e ./test/autonomous_verification/ -run TestE2E_OPS_RetryThenSuccess -v

# CODE3 Proposal path確認
go test -tags=e2e ./test/autonomous_verification/ -run TestE2E_CODE3_SuccessFlow -v
```

### テストリスト表示

```bash
go test -tags=e2e ./test/autonomous_verification/ -list=.
```

### カバレッジ計測

```bash
go test -tags=e2e ./test/autonomous_verification/ -coverprofile=coverage.out
go tool cover -html=coverage.out
```

## テストケース一覧

| テストケース | Route | 検証内容 |
|------------|-------|---------|
| TestE2E_CHAT_BypassAutonomousExecutor | CHAT | executorバイパス、report未保存 |
| TestE2E_OPS_SuccessFlow | OPS | 成功フロー（AttemptCount=1） |
| TestE2E_OPS_RetryThenSuccess | OPS | 初回失敗→retry成功（AttemptCount=2, RepairCount=1） |
| TestE2E_OPS_RepairExhausted | OPS | Repair失敗（Status=failed, ErrorKind設定） |
| TestE2E_CODE_SuccessFlow | CODE | Coder1（DeepSeek）統合 |
| TestE2E_CODE1_SuccessFlow | CODE1 | 明示的CODE1 route |
| TestE2E_CODE2_SuccessFlow | CODE2 | 明示的CODE2 route（OpenAI） |
| TestE2E_CODE3_SuccessFlow | CODE3 | Proposal path検証（Claude） |
| TestE2E_PLAN_SuccessFlow | PLAN | Mio reasoning統合 |
| TestE2E_ANALYZE_SuccessFlow | ANALYZE | Mio reasoning統合 |
| TestE2E_RESEARCH_SuccessFlow | RESEARCH | Mio reasoning統合 |

## ファイル構成

```
test/autonomous_verification/
├── README.md                        # このファイル
├── autonomous_verification_test.go  # 11テストケース（750行）
├── mock_agents.go                   # Mock Agent実装（86行）
└── test_helpers.go                  # テストインフラ（349行）
```

## 実装詳細

### テストインフラ（test_helpers.go）

- **StageRecorder**: autonomous executor のstage遷移記録
- **MockReportStore**: execution report の in-memory保存
- **EventCapture**: orchestrator event の記録
- **buildTestOrchestrator**: 本番同等のorchestrator構築（mock注入可能）
- **assert関数**: assertStageSequence, assertExecutionReport等

### Mock Agent（mock_agents.go）

- **MockMioAgent**: ルーティング・会話のmock
- **MockShiroAgent**: Worker実行のmock
- **MockCoderAgent**: コード生成・Proposal生成のmock

各MockはFunc fieldsで動作をカスタマイズ可能：
```go
mockMio := &MockMioAgent{
    DecideActionFunc: func(ctx context.Context, task task.Task) (routing.Decision, error) {
        return routing.Decision{Route: routing.RouteOPS, ...}, nil
    },
}
```

## 検証項目

各テストで以下を検証：

### ExecutionReport検証
- Route（正しいrouteが記録されているか）
- Capability（code_change / generic_execution）
- Status（passed / failed）
- AttemptCount（実行試行回数）
- RepairCount（repair試行回数）
- ErrorKind, FailureReason（失敗時）

### レスポンス検証
- 空でないレスポンスが返る
- 適切なrouteが選択される

### CHAT route特別検証
- Stage遷移なし（executorバイパス）
- ExecutionReport未保存
- agent.response イベント発火

## 制約事項

1. **実際のLLM呼び出しなし**: Mock Agentを使用してLLM API料金を節約
2. **Git auto-commit無効**: テスト環境でgit履歴を汚さない
3. **ビルドタグ必須**: `-tags=e2e` を指定しないとテストが実行されない

## トラブルシューティング

### テストが実行されない

```bash
# ビルドタグを忘れている
go test ./test/autonomous_verification/  # ❌
go test -tags=e2e ./test/autonomous_verification/  # ✅
```

### コンパイルエラー

```bash
# キャッシュクリア
go clean -cache -testcache
go test -tags=e2e -c ./test/autonomous_verification/
```

## 関連ドキュメント

- **実装仕様**: `/.claude/plans/temporal-enchanting-emerson.md`
- **Autonomous Executor**: `internal/application/autonomous/executor.go`
- **Message Orchestrator**: `internal/application/orchestrator/message_orchestrator.go`
- **Contract Normalizer**: `internal/application/contract/normalizer.go`

---

**実装日**: 2026-03-19
**テストカバレッジ目標**: 主要パス80%以上
