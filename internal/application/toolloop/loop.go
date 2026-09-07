package toolloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Config はツールループの設定
type Config struct {
	MaxIterations int // 最大反復回数（0の場合デフォルト10）
	MaxTokens     int // 1回のモデル呼び出しの最大出力トークン数（0の場合デフォルト4096）
	TaskID        modulecore.TaskID
	RunID         modulecore.RunID
	Actions       *actionmanager.Manager
}

func (c Config) maxIterations() int {
	if c.MaxIterations > 0 {
		return c.MaxIterations
	}
	return 10
}

func (c Config) maxTokens() int {
	if c.MaxTokens > 0 {
		return c.MaxTokens
	}
	return 4096
}

// Run はReActループを実行する
//
// フロー:
//  1. provider.Chat(messages + tools) を呼び出す
//  2. レスポンスに tool_calls がある場合:
//     a. 各 tool_call を toolRunner.ExecuteV2() で実行
//     b. 実行結果を role="tool" メッセージとして messages に追加
//     c. 1. に戻る
//  3. tool_calls がない場合（通常テキスト応答）:
//     → レスポンスの Content を返す
//  4. MaxIterations を超えた場合:
//     → 最後のレスポンスの Content を返す（途中打ち切り）
func Run(ctx context.Context, provider llm.ToolCallingProvider,
	toolRunner tool.RunnerV2, toolDefs []llm.ToolDefinition,
	messages []llm.ChatMessage, cfg Config) (string, error) {

	if err := validateLoopIdentity(cfg); err != nil {
		return "", err
	}

	maxIter := cfg.maxIterations()
	maxTokens := cfg.maxTokens()
	failedCalls := make(map[string]struct{})
	taskScoped := !cfg.TaskID.IsZero() && cfg.RunID != ""
	if taskScoped {
		if _, err := ensureLoopIdentity(ctx, cfg); err != nil {
			return "", err
		}
	}

	for i := 0; i < maxIter; i++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		log.Printf("[ToolLoop] iteration=%d/%d messages=%d tools=%d task_id=%s run_id=%s", i+1, maxIter, len(messages), len(toolDefs), cfg.TaskID.String(), string(cfg.RunID))

		resp, err := provider.Chat(ctx, llm.ChatRequest{
			Messages:        messages,
			Tools:           toolDefs,
			MaxTokens:       maxTokens,
			ReasoningEffort: llm.ReasoningEffortLow,
		})
		if err != nil {
			log.Printf("[ToolLoop] chat error iteration=%d err=%v", i+1, err)
			return "", fmt.Errorf("chat error at iteration %d: %w", i, err)
		}
		log.Printf("[ToolLoop] chat finish iteration=%d finish=%s tool_calls=%d content_len=%d", i+1, resp.FinishReason, len(resp.Message.ToolCalls), len(resp.Message.Content))

		// assistantメッセージを履歴に追加
		messages = append(messages, resp.Message)

		// tool_calls がなければ最終応答
		if len(resp.Message.ToolCalls) == 0 {
			log.Printf("[ToolLoop] complete iteration=%d", i+1)
			return resp.Message.Content, nil
		}

		// 各ツール呼び出しを実行
		for _, tc := range resp.Message.ToolCalls {
			signature, signatureOK := failedToolCallSignature(tc)
			if _, repeated := failedCalls[signature]; signatureOK && repeated {
				log.Printf("[ToolLoop] blocked repeated failed tool call name=%s", tc.Function.Name)
				return fmt.Sprintf("blocked: repeated identical failed tool call: %s", tc.Function.Name), nil
			}
			log.Printf("[ToolLoop] tool start name=%s args_keys=%d", tc.Function.Name, len(tc.Function.Arguments))
			execCtx := ctx
			if taskScoped {
				var bindErr error
				execCtx, bindErr = bindToolActionAttempt(execCtx, cfg, tc.Function.Name)
				if bindErr != nil {
					return "", bindErr
				}
			}
			result, toolErr := toolRunner.ExecuteV2(execCtx, tc.Function.Name, tc.Function.Arguments)
			if taskScoped {
				actionID, attemptID, bound := domainexecution.BoundActionAttemptFromContext(execCtx)
				if !bound {
					completionErr := fmt.Errorf("bound tool action attempt is unavailable")
					return "", errors.Join(toolErr, fmt.Errorf("complete tool attempt: %w", completionErr))
				}
				if completionErr := cfg.Actions.CompleteToolAttempt(execCtx, actionID, attemptID, result, toolErr); completionErr != nil {
					return "", errors.Join(toolErr, fmt.Errorf("complete tool attempt: %w", completionErr))
				}
			}

			var content string
			failed := false
			if toolErr != nil {
				log.Printf("[ToolLoop] tool error name=%s err=%v", tc.Function.Name, toolErr)
				content = fmt.Sprintf("Error: %v", toolErr)
				failed = true
			} else if result != nil && result.Error == nil {
				log.Printf("[ToolLoop] tool complete name=%s", tc.Function.Name)
				content = result.String()
			} else if result != nil && result.Error != nil {
				log.Printf("[ToolLoop] tool returned error name=%s err=%s", tc.Function.Name, result.Error.Message)
				content = fmt.Sprintf("Error: %s", result.Error.Message)
				failed = true
			} else {
				log.Printf("[ToolLoop] tool nil response name=%s", tc.Function.Name)
				content = "Error: nil response"
				failed = true
			}
			if failed && signatureOK {
				failedCalls[signature] = struct{}{}
			}

			messages = append(messages, llm.ChatMessage{
				Role:               "tool",
				Content:            content,
				ProviderToolCallID: tc.ID,
			})
		}
	}

	// MaxIterations超過 → 最後のassistantメッセージを返す
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			log.Printf("[ToolLoop] max iterations reached max=%d returning_last_assistant", maxIter)
			return messages[i].Content, nil
		}
	}

	return "", fmt.Errorf("max iterations (%d) exceeded with no assistant response", maxIter)
}

func failedToolCallSignature(call llm.ToolCall) (string, bool) {
	arguments, err := json.Marshal(call.Function.Arguments)
	if err != nil {
		return "", false
	}
	return call.Function.Name + "\n" + string(arguments), true
}

func validateLoopIdentity(cfg Config) error {
	taskSet := !cfg.TaskID.IsZero()
	runSet := cfg.RunID != ""
	if taskSet != runSet {
		return fmt.Errorf("task_id and run_id must both be set when either is set")
	}
	if taskSet {
		if cfg.Actions == nil {
			return fmt.Errorf("actions manager is required when task_id and run_id are set")
		}
		if err := cfg.TaskID.Validate(); err != nil {
			return fmt.Errorf("task_id must be canonical: %w", err)
		}
		if err := cfg.RunID.Validate(); err != nil {
			return fmt.Errorf("run_id must be canonical: %w", err)
		}
	}
	return nil
}

func bindToolActionAttempt(ctx context.Context, cfg Config, toolName string) (context.Context, error) {
	execCtx, err := ensureLoopIdentity(ctx, cfg)
	if err != nil {
		return nil, err
	}
	action, attempt, err := cfg.Actions.CreateAction(execCtx, actionmanager.CreateInput{
		TaskID: cfg.TaskID,
		RunID:  cfg.RunID,
		Kind:   domainaction.KindTool,
		Name:   toolName,
	})
	if err != nil {
		return nil, fmt.Errorf("create tool action: %w", err)
	}
	return domainexecution.WithBoundActionAttempt(execCtx, action.ActionID, attempt.AttemptID)
}

func ensureLoopIdentity(ctx context.Context, cfg Config) (context.Context, error) {
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("enclosing execution identity is required: %w", err)
	}
	if err := identity.TraceID.Validate(); err != nil {
		return nil, fmt.Errorf("enclosing execution trace identity is required: %w", err)
	}
	if identity.TaskID != cfg.TaskID || identity.RunID != cfg.RunID {
		return nil, fmt.Errorf("execution identity mismatch: context task_id=%s run_id=%s config task_id=%s run_id=%s", identity.TaskID, identity.RunID, cfg.TaskID, cfg.RunID)
	}
	return ctx, nil
}
