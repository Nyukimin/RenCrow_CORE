package toolloop

import (
	"context"
	"fmt"
	"log"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
)

// Config はツールループの設定
type Config struct {
	MaxIterations int // 最大反復回数（0の場合デフォルト10）
}

func (c Config) maxIterations() int {
	if c.MaxIterations > 0 {
		return c.MaxIterations
	}
	return 10
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

	maxIter := cfg.maxIterations()

	for i := 0; i < maxIter; i++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		log.Printf("[ToolLoop] iteration=%d/%d messages=%d tools=%d", i+1, maxIter, len(messages), len(toolDefs))

		resp, err := provider.Chat(ctx, llm.ChatRequest{
			Messages: messages,
			Tools:    toolDefs,
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
			log.Printf("[ToolLoop] tool start name=%s args_keys=%d", tc.Function.Name, len(tc.Function.Arguments))
			result, err := toolRunner.ExecuteV2(ctx, tc.Function.Name, tc.Function.Arguments)

			var content string
			if err != nil {
				log.Printf("[ToolLoop] tool error name=%s err=%v", tc.Function.Name, err)
				content = fmt.Sprintf("Error: %v", err)
			} else if result != nil && result.Error == nil {
				log.Printf("[ToolLoop] tool complete name=%s", tc.Function.Name)
				content = fmt.Sprintf("%v", result.Result)
			} else if result != nil && result.Error != nil {
				log.Printf("[ToolLoop] tool returned error name=%s err=%s", tc.Function.Name, result.Error.Message)
				content = fmt.Sprintf("Error: %s", result.Error.Message)
			} else {
				log.Printf("[ToolLoop] tool nil response name=%s", tc.Function.Name)
				content = "Error: nil response"
			}

			messages = append(messages, llm.ChatMessage{
				Role:       "tool",
				Content:    content,
				ToolCallID: tc.ID,
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
