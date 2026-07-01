package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"

	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
)

// runMessageLoop はstdin/stdout上のJSON通信ループ
func runMessageLoop(ctx context.Context, handler AgentHandler, jsonOut io.Writer) error {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	encoder := json.NewEncoder(jsonOut)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Bytes()
		var msg domaintransport.Message
		if err := json.Unmarshal(line, &msg); err != nil {
			log.Printf("[picoclaw-agent] Failed to decode message: %v", err)
			// エラーをJSON応答として返す
			errResp := domaintransport.NewErrorMessage("agent", "unknown", "", "", fmt.Sprintf("decode error: %v", err))
			encoder.Encode(errResp)
			continue
		}

		// タイムアウト付きでハンドラ実行
		handlerCtx, handlerCancel := context.WithTimeout(ctx, shutdownTimeout)
		response, err := handler.HandleMessage(handlerCtx, msg)
		handlerCancel()

		if err != nil {
			log.Printf("[picoclaw-agent] Handler error: %v", err)
			errResp := domaintransport.NewErrorMessage(msg.To, msg.From, msg.SessionID, msg.JobID, fmt.Sprintf("handler error: %v", err))
			if encErr := encoder.Encode(errResp); encErr != nil {
				return fmt.Errorf("encode error response: %w", encErr)
			}
			continue
		}

		if err := encoder.Encode(response); err != nil {
			return fmt.Errorf("encode response: %w", err)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("stdin scanner: %w", err)
	}

	return nil // stdin closed (normal termination)
}
