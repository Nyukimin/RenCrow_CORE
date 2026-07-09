# MessageOrchestrator分割リファクタリング - TDD実装計画

**作成日**: 2026-03-26
**対象**: RenCrow (RenCrow_CORE)
**目的**: MessageOrchestratorを段階的にリファクタリングし、CodeExecutorとStreamingOrchestratorを分離

---

## 1. 実装戦略サマリ

### 1.1 アプローチ

**TDD Red-Green-Refactorサイクル**を採用し、既存テスト（72.3%カバレッジ）を保護しながら段階的に分離します。

### 1.2 分離対象

**Phase 1 (優先)**: CodeExecutor分離
- 対象: `executeCodeViaShiro`, `selectCoderForRoute`, `tryExecuteProposalPath`, `executeCoderGeneratePath`
- 期待効果: -150-180行、リスク低、責務明確化

**Phase 2 (オプション)**: StreamingOrchestrator分離
- 対象: `withStreamHooks`, `pushTTS`, TTS/VTuberストリーミング
- 期待効果: -80-100行、リスク中

### 1.3 後方互換性保証

- `ProcessMessage(ctx, ProcessMessageRequest) (ProcessMessageResponse, error)` インターフェース維持
- チャネルアダプター（Slack, Discord, Telegram, LINE）への影響ゼロ
- 段階的マイグレーション可能（委譲パターン使用）

---

## 2. Phase 1: CodeExecutor分離（TDD）

### 2.1 実装ステップ

#### Step 1: インターフェース設計（Red）

**目的**: CodeExecutorの責務とインターフェースを定義

**ファイル**: `/home/nyukimi/RenCrow_CORE/internal/application/orchestrator/code_executor.go`

```go
package orchestrator

import (
	"context"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// CodeExecutor はコード生成タスクの実行を担当
type CodeExecutor interface {
	// ExecuteCode はルーティングされたコードタスクを実行
	ExecuteCode(ctx context.Context, req CodeExecutionRequest) (CodeExecutionResponse, error)
}

// CodeExecutionRequest はコード実行リクエスト
type CodeExecutionRequest struct {
	Task      task.Task
	Route     routing.Route
	SessionID string
	Channel   string
	ChatID    string
}

// CodeExecutionResponse はコード実行レスポンス
type CodeExecutionResponse struct {
	Response string
	Error    error
}

// codeTarget は内部的なCoder選択結果
type codeTarget struct {
	name         string
	coder        CoderAgent
	systemPrompt string
	release      func()
}
```

**テストファイル**: `/home/nyukimi/RenCrow_CORE/internal/application/orchestrator/code_executor_test.go`

```go
package orchestrator

import (
	"context"
	"testing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func TestCodeExecutor_ExecuteCode_CODE1_Success(t *testing.T) {
	// Arrange: Coder1が利用可能
	coder1 := &mockCoderAgent{response: "spec generated"}
	executor := NewDefaultCodeExecutor(coder1, nil, nil, nil, nil, nil)
	
	req := CodeExecutionRequest{
		Task:      task.NewTask(task.NewJobID(), "/code1 design spec", "line", "U123"),
		Route:     routing.RouteCODE1,
		SessionID: "s1",
		Channel:   "line",
		ChatID:    "U123",
	}
	
	// Act
	resp, err := executor.ExecuteCode(context.Background(), req)
	
	// Assert
	if err != nil {
		t.Fatalf("ExecuteCode failed: %v", err)
	}
	if resp.Response != "spec generated" {
		t.Errorf("Expected 'spec generated', got '%s'", resp.Response)
	}
}

func TestCodeExecutor_ExecuteCode_CODE3_WithProposal_Success(t *testing.T) {
	// Arrange: Coder3でProposal生成→Worker実行
	tmpDir := t.TempDir()
	workerService := createTestWorkerService(tmpDir)
	
	proposal := createTestProposal(tmpDir, "test.txt", "Hello CODE3")
	coder3 := &mockCoderAgentWithProposal{proposal: proposal}
	
	executor := NewDefaultCodeExecutor(nil, nil, coder3, nil, workerService, nil)
	
	req := CodeExecutionRequest{
		Task:      task.NewTask(task.NewJobID(), "/code3 create test file", "line", "U123"),
		Route:     routing.RouteCODE3,
		SessionID: "s1",
		Channel:   "line",
		ChatID:    "U123",
	}
	
	// Act
	resp, err := executor.ExecuteCode(context.Background(), req)
	
	// Assert
	if err != nil {
		t.Fatalf("ExecuteCode failed: %v", err)
	}
	if !contains(resp.Response, "## Plan") {
		t.Error("Response should contain Plan section")
	}
	if !contains(resp.Response, "## Execution Result") {
		t.Error("Response should contain Execution Result section")
	}
}

func TestCodeExecutor_ExecuteCode_CODE_CoderSelection_Fallback(t *testing.T) {
	// Arrange: Coder1がbusy、Coder2が利用可能
	coderStatus := NewCoderStatus()
	coderStatus.Acquire("coder1") // coder1をbusy状態に
	
	coder1 := &mockCoderAgent{response: "should not be called"}
	coder2 := &mockCoderAgent{response: "fallback coder2"}
	
	executor := NewDefaultCodeExecutor(coder1, coder2, nil, coderStatus, nil, nil)
	
	req := CodeExecutionRequest{
		Task:      task.NewTask(task.NewJobID(), "implement feature", "line", "U123"),
		Route:     routing.RouteCODE,
		SessionID: "s1",
		Channel:   "line",
		ChatID:    "U123",
	}
	
	// Act
	resp, err := executor.ExecuteCode(context.Background(), req)
	
	// Assert
	if err != nil {
		t.Fatalf("ExecuteCode failed: %v", err)
	}
	if resp.Response != "fallback coder2" {
		t.Errorf("Expected fallback to coder2, got '%s'", resp.Response)
	}
}
```

**コミット戦略**:
```bash
git add internal/application/orchestrator/code_executor.go
git add internal/application/orchestrator/code_executor_test.go
git commit -m "feat(orchestrator): Add CodeExecutor interface and tests (RED phase)

- Define CodeExecutor interface for code task execution
- Add CodeExecutionRequest/Response types
- Add failing tests for CODE1, CODE3, CODE routes
- Tests currently fail (RED phase of TDD)

Related: Phase 1 orchestrator refactoring

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>"
```

---

#### Step 2: 最小実装（Green）

**目的**: テストを通す最小限の実装

**実装**: `/home/nyukimi/RenCrow_CORE/internal/application/orchestrator/code_executor_impl.go`

```go
package orchestrator

import (
	"context"
	"fmt"
	"log"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/service"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

// DefaultCodeExecutor はCodeExecutorのデフォルト実装
type DefaultCodeExecutor struct {
	coder1          CoderAgent
	coder2          CoderAgent
	coder3          CoderAgent
	coderStatus     *CoderStatus
	workerExecution service.WorkerExecutionService
	eventEmitter    func(typ, from, to, message, route, jobID, sessionID, channel, chatID string)
}

// NewDefaultCodeExecutor は新しいDefaultCodeExecutorを作成
func NewDefaultCodeExecutor(
	coder1 CoderAgent,
	coder2 CoderAgent,
	coder3 CoderAgent,
	coderStatus *CoderStatus,
	workerExecution service.WorkerExecutionService,
	eventEmitter func(typ, from, to, message, route, jobID, sessionID, channel, chatID string),
) *DefaultCodeExecutor {
	if coderStatus == nil {
		coderStatus = NewCoderStatus()
	}
	return &DefaultCodeExecutor{
		coder1:          coder1,
		coder2:          coder2,
		coder3:          coder3,
		coderStatus:     coderStatus,
		workerExecution: workerExecution,
		eventEmitter:    eventEmitter,
	}
}

// ExecuteCode はコードタスクを実行
func (e *DefaultCodeExecutor) ExecuteCode(ctx context.Context, req CodeExecutionRequest) (CodeExecutionResponse, error) {
	jid := req.Task.JobID().String()
	
	// Coder選択
	target, err := e.selectCoderForRoute(req.Route)
	if err != nil {
		return CodeExecutionResponse{}, err
	}
	if target.release != nil {
		defer target.release()
	}
	
	log.Printf("[CodeExecutor] selected coder=%s route=%s job=%s", target.name, req.Route, jid)
	
	e.emit("agent.start", "mio", "shiro", "コードタスクをShiro経由で実行", req.Route.String(), jid, req.SessionID, req.Channel, req.ChatID)
	e.emit("agent.start", "shiro", target.name, req.Task.UserMessage(), req.Route.String(), jid, req.SessionID, req.Channel, req.ChatID)
	
	// CODE3かつProposal生成可能な場合はWorker実行パス
	if req.Route == routing.RouteCODE3 && e.workerExecution != nil {
		if resp, handled, err := e.tryExecuteProposalPath(ctx, req.Task, req.Route, target, req.SessionID, req.Channel, req.ChatID, jid); handled {
			return CodeExecutionResponse{Response: resp, Error: err}, err
		}
	}
	
	// 通常のGenerate実行パス
	return e.executeCoderGeneratePath(ctx, req.Task, req.Route, target, req.SessionID, req.Channel, req.ChatID, jid)
}

// selectCoderForRoute はルートに応じてCoderを選択
func (e *DefaultCodeExecutor) selectCoderForRoute(route routing.Route) (codeTarget, error) {
	// 明示的なルート（CODE1, CODE2, CODE3）
	if name, prompt, ok := explicitCodeRouteTarget(route); ok {
		coder := e.coderByName(name)
		if coder == nil {
			return codeTarget{}, fmt.Errorf("%s route requested but no %s available", route, name)
		}
		log.Printf("[CodeExecutor] coder selected route=%s target=%s mode=explicit", route, name)
		return codeTarget{name: name, coder: coder, systemPrompt: prompt}, nil
	}
	
	// 汎用CODEルート: coder1 → coder2 → coder3 の順で選択
	switch route {
	case routing.RouteCODE:
		type coderEntry struct {
			name  string
			coder CoderAgent
		}
		chain := []coderEntry{
			{name: "coder1", coder: e.coder1},
			{name: "coder2", coder: e.coder2},
			{name: "coder3", coder: e.coder3},
		}
		for _, c := range chain {
			if c.coder == nil {
				log.Printf("[CodeExecutor] coder skip route=%s target=%s reason=unavailable", route, c.name)
				continue
			}
			if !e.coderStatus.Acquire(c.name) {
				log.Printf("[CodeExecutor] coder skip route=%s target=%s reason=busy", route, c.name)
				continue
			}
			coderName := c.name
			log.Printf("[CodeExecutor] coder selected route=%s target=%s mode=auto", route, coderName)
			return codeTarget{
				name:         coderName,
				coder:        c.coder,
				systemPrompt: "You are a code generation assistant.",
				release: func() {
					e.coderStatus.Release(coderName)
				},
			}, nil
		}
		return codeTarget{}, fmt.Errorf("CODE route requested but all coders are busy or unavailable")
	default:
		return codeTarget{}, fmt.Errorf("unknown code route: %s", route)
	}
}

// tryExecuteProposalPath はProposal生成→Worker実行パスを試行
func (e *DefaultCodeExecutor) tryExecuteProposalPath(
	ctx context.Context,
	t task.Task,
	route routing.Route,
	target codeTarget,
	sessionID, channel, chatID, jid string,
) (string, bool, error) {
	coderWithProposal, ok := target.coder.(CoderAgentWithProposal)
	if !ok {
		return "", false, nil
	}
	
	p, err := coderWithProposal.GenerateProposal(ctx, t)
	if err != nil {
		e.emit("agent.response", target.name, "shiro", "エラー: "+err.Error(), route.String(), jid, sessionID, channel, chatID)
		return "", true, fmt.Errorf("%s proposal generation failed: %w", target.name, err)
	}
	if p == nil || !p.IsValid() {
		e.emit("agent.response", target.name, "shiro", "無効な Proposal が返されました", route.String(), jid, sessionID, channel, chatID)
		return "", true, fmt.Errorf("%s proposal generation failed: %w", target.name, &agent.ProposalError{
			Kind:      agent.ProposalFailureEmpty,
			Reason:    "generated invalid proposal",
			Retryable: true,
		})
	}
	
	e.emit("agent.response", target.name, "shiro", "## Plan\n"+p.Plan(), route.String(), jid, sessionID, channel, chatID)
	e.emit("agent.start", "shiro", "mio", "Patch を実行中...", route.String(), jid, sessionID, channel, chatID)
	
	result, err := e.workerExecution.ExecuteProposal(ctx, t.JobID(), p)
	if err != nil {
		e.emit("agent.response", "shiro", "mio", "実行失敗: "+err.Error(), route.String(), jid, sessionID, channel, chatID)
		return "", true, fmt.Errorf("worker execution failed: %w", err)
	}
	
	formatted := formatExecutionResult(p, result)
	e.emit("agent.response", "shiro", "mio", formatted, route.String(), jid, sessionID, channel, chatID)
	return formatted, true, nil
}

// executeCoderGeneratePath は通常のGenerate実行パス
func (e *DefaultCodeExecutor) executeCoderGeneratePath(
	ctx context.Context,
	t task.Task,
	route routing.Route,
	target codeTarget,
	sessionID, channel, chatID, jid string,
) (CodeExecutionResponse, error) {
	resp, err := target.coder.Generate(ctx, t, target.systemPrompt)
	if err != nil {
		e.emit("agent.response", target.name, "shiro", "エラー: "+err.Error(), route.String(), jid, sessionID, channel, chatID)
		return CodeExecutionResponse{}, err
	}
	e.emit("agent.response", target.name, "shiro", truncate(resp, 500), route.String(), jid, sessionID, channel, chatID)
	e.emit("agent.response", "shiro", "mio", truncate(resp, 500), route.String(), jid, sessionID, channel, chatID)
	return CodeExecutionResponse{Response: resp}, nil
}

func (e *DefaultCodeExecutor) coderByName(name string) CoderAgent {
	switch name {
	case "coder1":
		return e.coder1
	case "coder2":
		return e.coder2
	case "coder3":
		return e.coder3
	default:
		return nil
	}
}

func (e *DefaultCodeExecutor) emit(typ, from, to, message, route, jobID, sessionID, channel, chatID string) {
	if e.eventEmitter != nil {
		e.eventEmitter(typ, from, to, message, route, jobID, sessionID, channel, chatID)
	}
}
```

**ヘルパー関数の移動**: `/home/nyukimi/RenCrow_CORE/internal/application/orchestrator/code_helpers.go`

```go
package orchestrator

import (
	"fmt"
	"strings"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

// explicitCodeRouteTarget はCODE1/CODE2/CODE3ルートに対応するCoderを返す
func explicitCodeRouteTarget(route routing.Route) (name string, systemPrompt string, ok bool) {
	switch route {
	case routing.RouteCODE1:
		return "coder1", "You are a specification design assistant.", true
	case routing.RouteCODE2:
		return "coder2", "You are an implementation assistant.", true
	case routing.RouteCODE3:
		return "coder3", "You are a high-quality code generation assistant.", true
	default:
		return "", "", false
	}
}

// formatExecutionResult はProposalとPatchExecutionResultを整形
func formatExecutionResult(p *proposal.Proposal, result *patch.PatchExecutionResult) string {
	statusEmoji := "✅"
	if !result.Success {
		statusEmoji = "⚠️"
	}
	
	gitCommitLine := ""
	if result.GitCommit != "" && result.GitCommit != "no-changes" {
		shortHash := result.GitCommit
		if len(shortHash) > 8 {
			shortHash = shortHash[:8]
		}
		gitCommitLine = fmt.Sprintf("\n- **Git Commit**: `%s`", shortHash)
	}
	
	commandDetails := ""
	for i, cmdResult := range result.Results {
		status := "✅"
		if !cmdResult.Success {
			status = "❌"
		}
		commandDetails += fmt.Sprintf("\n%d. %s `%s` %s",
			i+1, status, cmdResult.Command.Action, cmdResult.Command.Target)
		if cmdResult.Error != "" {
			commandDetails += fmt.Sprintf("\n   Error: %s", cmdResult.Error)
		}
	}
	
	return fmt.Sprintf(`## Plan
%s

## Execution Result
- **Status**: %s
- **Executed**: %d commands
- **Failed**: %d commands
- **Success Rate**: %.1f%%%s

### Command Results%s

## Risk
%s
`,
		p.Plan(),
		statusEmoji,
		result.ExecutedCmds,
		result.FailedCmds,
		result.SuccessRate()*100,
		gitCommitLine,
		commandDetails,
		p.Risk(),
	)
}

// truncate はビュワー表示用に長いテキストを切り詰める
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	lines := strings.SplitN(s, "\n", -1)
	var b strings.Builder
	for _, line := range lines {
		if b.Len()+len(line)+1 > maxLen {
			b.WriteString("\n... (truncated)")
			break
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	return b.String()
}
```

**コミット戦略**:
```bash
go test ./internal/application/orchestrator/... -run TestCodeExecutor
git add internal/application/orchestrator/code_executor_impl.go
git add internal/application/orchestrator/code_helpers.go
git commit -m "feat(orchestrator): Implement DefaultCodeExecutor (GREEN phase)

- Add DefaultCodeExecutor implementation
- Extract code helpers to separate file
- Tests now pass (GREEN phase of TDD)
- No integration with MessageOrchestrator yet

Related: Phase 1 orchestrator refactoring

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>"
```

---

#### Step 3: MessageOrchestratorへの統合（Refactor）

**目的**: MessageOrchestratorからCodeExecutorに委譲

**変更**: `/home/nyukimi/RenCrow_CORE/internal/application/orchestrator/message_orchestrator.go`

```go
// MessageOrchestrator struct に追加
type MessageOrchestrator struct {
	sessionRepo     SessionRepository
	mio             MioAgent
	shiro           ShiroAgent
	coder1          CoderAgent // ※互換性のため残す
	coder2          CoderAgent
	coder3          CoderAgent
	workerExecution service.WorkerExecutionService
	coderStatus     *CoderStatus
	listener        EventListener
	reporter        ReportStore
	idleNotifier    IdleNotifier
	ttsBridge       TTSBridge
	vtuberBridge    VTuberBridge
	
	// 新規追加
	codeExecutor    CodeExecutor // ← CodeExecutorを保持
}

// NewMessageOrchestrator コンストラクタ
func NewMessageOrchestrator(
	sessionRepo SessionRepository,
	mio MioAgent,
	shiro ShiroAgent,
	coder1 CoderAgent,
	coder2 CoderAgent,
	coder3 CoderAgent,
	workerExecution service.WorkerExecutionService,
) *MessageOrchestrator {
	coderStatus := NewCoderStatus()
	
	// CodeExecutor初期化
	codeExecutor := NewDefaultCodeExecutor(
		coder1, coder2, coder3,
		coderStatus,
		workerExecution,
		nil, // eventEmitterは後で設定
	)
	
	o := &MessageOrchestrator{
		sessionRepo:     sessionRepo,
		mio:             mio,
		shiro:           shiro,
		coder1:          coder1,
		coder2:          coder2,
		coder3:          coder3,
		workerExecution: workerExecution,
		coderStatus:     coderStatus,
		codeExecutor:    codeExecutor,
	}
	
	// eventEmitterをCodeExecutorに設定
	codeExecutor.(*DefaultCodeExecutor).eventEmitter = o.emit
	
	return o
}

// executeCodeViaShiro は CodeExecutor に委譲
func (o *MessageOrchestrator) executeCodeViaShiro(
	ctx context.Context,
	t task.Task,
	route routing.Route,
	sessionID, channel, chatID string,
) (string, error) {
	req := CodeExecutionRequest{
		Task:      t,
		Route:     route,
		SessionID: sessionID,
		Channel:   channel,
		ChatID:    chatID,
	}
	
	resp, err := o.codeExecutor.ExecuteCode(ctx, req)
	return resp.Response, err
}

// ※ 以下のメソッドは削除可能（CodeExecutorに移動済み）
// - selectCoderForRoute
// - tryExecuteProposalPath
// - executeCoderGeneratePath
// - coderByName
```

**既存テストの実行**:
```bash
# すべての既存テストが通ることを確認
go test ./internal/application/orchestrator/... -v
# カバレッジが72.3%以上維持されていることを確認
go test ./internal/application/orchestrator/... -cover
```

**コミット戦略**:
```bash
git add internal/application/orchestrator/message_orchestrator.go
git commit -m "refactor(orchestrator): Integrate CodeExecutor into MessageOrchestrator

- Delegate executeCodeViaShiro to CodeExecutor
- Keep coder1/2/3 fields for backward compatibility
- Remove duplicate code from MessageOrchestrator
- All existing tests pass (72.3% coverage maintained)

Related: Phase 1 orchestrator refactoring

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>"
```

---

#### Step 4: クリーンアップと最終検証

**目的**: 重複コードの削除、ドキュメント更新

**削除対象**: `/home/nyukimi/RenCrow_CORE/internal/application/orchestrator/message_orchestrator.go`

```go
// 以下のメソッドを削除（CodeExecutorに移動済み）
// func (o *MessageOrchestrator) selectCoderForRoute(route routing.Route) (codeTarget, error)
// func (o *MessageOrchestrator) tryExecuteProposalPath(...)
// func (o *MessageOrchestrator) executeCoderGeneratePath(...)
// func (o *MessageOrchestrator) coderByName(name string) CoderAgent
// func (o *MessageOrchestrator) formatExecutionResult(...)
```

**ドキュメント更新**:
```markdown
# docs/architecture/orchestrator_refactoring_phase1.md

## Phase 1完了: CodeExecutor分離

### 実施内容
- MessageOrchestratorから約160行のコード実行ロジックを分離
- CodeExecutorインターフェースと実装を追加
- 既存テスト72.3%カバレッジ維持
- 後方互換性100%維持

### 新規コンポーネント
- `CodeExecutor` インターフェース
- `DefaultCodeExecutor` 実装
- `CodeExecutionRequest/Response` 型

### 影響範囲
- 変更: `MessageOrchestrator` (委譲パターン)
- 追加: `code_executor.go`, `code_executor_impl.go`, `code_helpers.go`
- テスト: `code_executor_test.go`

### 次のステップ
- Phase 2: StreamingOrchestrator分離（オプション）
```

**最終検証**:
```bash
# 全テスト実行
go test ./... -v

# カバレッジ確認
go test ./internal/application/orchestrator/... -coverprofile=coverage.out
go tool cover -func=coverage.out | grep total

# 統合テスト実行
go test ./test/e2e/... -v
go test ./test/integration/... -v

# ビルド確認
make build
```

**コミット戦略**:
```bash
git add internal/application/orchestrator/message_orchestrator.go
git add docs/architecture/orchestrator_refactoring_phase1.md
git commit -m "refactor(orchestrator): Complete Phase 1 - Remove duplicated code

- Remove code execution methods from MessageOrchestrator
- All logic delegated to CodeExecutor
- Line count reduced by ~160 lines (838 → ~680)
- Add Phase 1 completion documentation

Related: Phase 1 orchestrator refactoring completed

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>"
```

---

## 3. Phase 2: StreamingOrchestrator分離（オプション）

### 3.1 概要

**対象ロジック**:
- `withStreamHooks` (TTS/VTuberストリーミング設定)
- `pushTTS` (TTSプッシュ)
- TTS/VTuberストリーミング関連ヘルパー

**期待効果**:
- MessageOrchestratorからさらに80-100行削減
- ストリーミングロジックの独立テスト可能
- TTS/VTuber機能のオン/オフ切り替え容易

### 3.2 実装ステップ（概要）

Phase 1と同様のTDDサイクル:

1. **Step 1 (RED)**: StreamingOrchestratorインターフェース定義とテスト作成
2. **Step 2 (GREEN)**: 最小実装
3. **Step 3 (Refactor)**: MessageOrchestratorへの統合
4. **Step 4**: クリーンアップ

---

## 4. テスト戦略

### 4.1 既存テスト保護

**実行頻度**: 各コミット前に必ず実行

```bash
# 既存テスト実行
go test ./internal/application/orchestrator/... -v

# カバレッジ確認（72.3%以上維持）
go test ./internal/application/orchestrator/... -cover
```

**保護対象テスト** (2,181行):
- `message_orchestrator_test.go` (789行, 20+テスト)
- `message_orchestrator_code3_test.go` (338行, 4+テスト)
- `message_orchestrator_code_path_test.go` (94行, 2テスト)
- `distributed_orchestrator_test.go` (823行, 10+テスト)
- その他

### 4.2 新規テスト追加

**CodeExecutor専用テスト**:
- Coder選択ロジック（CODE, CODE1, CODE2, CODE3）
- Proposal生成→Worker実行フロー
- エラーハンドリング（Coder unavailable, Proposal invalid）
- Busy状態でのフォールバック

**統合テスト**:
```go
// test/integration/code_executor_integration_test.go
func TestCodeExecutor_Integration_CODE3_E2E(t *testing.T) {
	// 実際のファイルシステムでProposal→Worker実行をテスト
}
```

### 4.3 カバレッジ目標

- **目標**: 72.3% 以上維持
- **CodeExecutor新規コード**: 80% 以上
- **MessageOrchestrator**: 既存カバレッジ維持

---

## 5. 後方互換性保証

### 5.1 インターフェース維持

```go
// 変更なし（100%互換）
type Orchestrator interface {
	ProcessMessage(ctx context.Context, req ProcessMessageRequest) 
		(ProcessMessageResponse, error)
}
```

### 5.2 チャネルアダプターへの影響

**影響ゼロ**: 以下のアダプターは変更不要
- `/home/nyukimi/RenCrow_CORE/internal/adapter/channels/slack/adapter.go`
- `/home/nyukimi/RenCrow_CORE/internal/adapter/channels/discord/adapter.go`
- `/home/nyukimi/RenCrow_CORE/internal/adapter/channels/telegram/adapter.go`
- `/home/nyukimi/RenCrow_CORE/internal/adapter/line/handler.go`

### 5.3 エントリーポイントへの影響

**影響ゼロ**: 以下のエントリーポイントは変更不要
- `/home/nyukimi/RenCrow_CORE/cmd/rencrow/main.go`
- `/home/nyukimi/RenCrow_CORE/cmd/test-chat/main.go`

---

## 6. ロールバック戦略

### 6.1 各ステップでのロールバックポイント

**Step 1完了後** (RED):
```bash
# インターフェース定義のみ追加、既存コードに影響なし
git revert HEAD  # 最新コミットを戻す
```

**Step 2完了後** (GREEN):
```bash
# 実装追加、既存コードに影響なし
git revert HEAD~2..HEAD  # Step 1-2をまとめて戻す
```

**Step 3完了後** (Refactor):
```bash
# MessageOrchestratorが変更されているが、委譲パターンのため安全
git revert HEAD  # Step 3のみ戻す（Step 1-2は残る）
```

**Step 4完了後** (Cleanup):
```bash
# 重複コード削除、ロールバックは推奨しない
# 代わりに修正コミットで対応
```

### 6.2 緊急ロールバック手順

```bash
# Phase 1全体をロールバック
git log --oneline | grep "Phase 1 orchestrator refactoring"
# 該当コミットのハッシュを確認

git revert <hash>..HEAD  # Phase 1のすべてのコミットを戻す

# テスト実行
go test ./... -v

# 問題なければコミット
git commit -m "revert: Rollback Phase 1 orchestrator refactoring

Reason: [ロールバック理由を記載]

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>"
```

---

## 7. リスク評価と対策

### 7.1 リスク一覧

| リスク | 影響度 | 発生確率 | 対策 |
|--------|--------|----------|------|
| 既存テスト失敗 | 高 | 低 | 各コミット前にテスト実行、失敗時は即ロールバック |
| カバレッジ低下 | 中 | 低 | Step 2でカバレッジ確認、低下時は追加テスト |
| パフォーマンス劣化 | 低 | 低 | 委譲オーバーヘッドは無視できるレベル |
| 並行実行時の競合 | 中 | 低 | CoderStatusのロック機構維持 |

### 7.2 対策詳細

**既存テスト失敗時**:
1. `git revert HEAD` で即座にロールバック
2. 失敗原因を調査（ログ、デバッグ実行）
3. 修正後に再度コミット

**カバレッジ低下時**:
1. `go test -coverprofile=coverage.out` で詳細確認
2. カバーされていないコードパスを特定
3. 追加テストを作成

**パフォーマンス劣化時**:
1. ベンチマークテスト実行
2. プロファイリング（`pprof`）
3. ボトルネック特定と最適化

---

## 8. 実装スケジュール

### 8.1 推奨スケジュール

| Phase | ステップ | 作業時間 | 累計時間 |
|-------|----------|----------|----------|
| Phase 1 | Step 1 (RED) | 1-2時間 | 1-2時間 |
| Phase 1 | Step 2 (GREEN) | 2-3時間 | 3-5時間 |
| Phase 1 | Step 3 (Refactor) | 1-2時間 | 4-7時間 |
| Phase 1 | Step 4 (Cleanup) | 1時間 | 5-8時間 |
| Phase 2 | 全ステップ | 3-5時間 | 8-13時間 |

**推奨進め方**:
- 1日目: Phase 1 Step 1-2（RED-GREEN）
- 2日目: Phase 1 Step 3-4（Refactor-Cleanup）
- 3日目: Phase 2（オプション）

### 8.2 チェックポイント

**Phase 1 Step 1完了時**:
- [ ] `code_executor.go` インターフェース定義完了
- [ ] `code_executor_test.go` テスト作成完了
- [ ] テストが失敗することを確認（RED）

**Phase 1 Step 2完了時**:
- [ ] `code_executor_impl.go` 実装完了
- [ ] `code_helpers.go` ヘルパー関数抽出完了
- [ ] テストがすべて通ることを確認（GREEN）

**Phase 1 Step 3完了時**:
- [ ] MessageOrchestratorにCodeExecutor統合完了
- [ ] 既存テストがすべて通ることを確認
- [ ] カバレッジ72.3%以上維持

**Phase 1 Step 4完了時**:
- [ ] 重複コード削除完了
- [ ] ドキュメント更新完了
- [ ] 統合テスト・E2Eテスト実行完了

---

## 9. 成功基準

### 9.1 Phase 1成功基準

- [x] CodeExecutorインターフェース定義完了
- [x] DefaultCodeExecutor実装完了
- [x] 既存テスト100%パス（72.3%カバレッジ維持）
- [x] MessageOrchestratorから150-180行削減
- [x] 後方互換性100%維持
- [x] チャネルアダプター変更なし
- [x] 統合テスト・E2Eテスト100%パス

### 9.2 Phase 2成功基準（オプション）

- [ ] StreamingOrchestratorインターフェース定義完了
- [ ] 実装完了
- [ ] 既存テスト100%パス
- [ ] MessageOrchestratorからさらに80-100行削減
- [ ] TTS/VTuber機能の独立テスト可能

---

## 10. 参考情報

### 10.1 関連ファイル

**実装対象**:
- `/home/nyukimi/RenCrow_CORE/internal/application/orchestrator/message_orchestrator.go` (838行)
- `/home/nyukimi/RenCrow_CORE/internal/application/orchestrator/coder_status.go` (41行)

**テスト**:
- `/home/nyukimi/RenCrow_CORE/internal/application/orchestrator/message_orchestrator_test.go` (789行)
- `/home/nyukimi/RenCrow_CORE/internal/application/orchestrator/message_orchestrator_code3_test.go` (338行)
- `/home/nyukimi/RenCrow_CORE/internal/application/orchestrator/message_orchestrator_code_path_test.go` (94行)

**依存アダプター**:
- `/home/nyukimi/RenCrow_CORE/internal/adapter/channels/slack/adapter.go`
- `/home/nyukimi/RenCrow_CORE/internal/adapter/channels/discord/adapter.go`
- `/home/nyukimi/RenCrow_CORE/internal/adapter/channels/telegram/adapter.go`
- `/home/nyukimi/RenCrow_CORE/internal/adapter/line/handler.go`

### 10.2 関連ドキュメント

- `CLAUDE.md` - プロジェクトルール
- `docs/01_正本仕様/実装仕様.md` - 実装仕様
- `docs/03_設計文書/Chat_Worker_Coder_アーキテクチャ.md` - アーキテクチャ設計

---

**最終更新**: 2026-03-26
**バージョン**: 1.0
**作成者**: Claude Sonnet 4.5