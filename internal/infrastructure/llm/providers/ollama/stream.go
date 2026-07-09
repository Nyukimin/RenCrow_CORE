package ollama

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// readStream は Ollama の NDJSON ストリームを読み込む
func (p *OllamaProvider) readStream(body io.Reader, onToken llm.StreamCallback) (llm.GenerateResponse, error) {
	var full strings.Builder
	decoder := json.NewDecoder(body)

	for {
		var chunk struct {
			Response string `json:"response"`
			Done     bool   `json:"done"`
		}
		if err := decoder.Decode(&chunk); err != nil {
			if err == io.EOF {
				break
			}
			return llm.GenerateResponse{}, fmt.Errorf("failed to decode stream chunk: %w", err)
		}

		if chunk.Response != "" {
			full.WriteString(chunk.Response)
			onToken(chunk.Response)
		}

		if chunk.Done {
			break
		}
	}

	return llm.GenerateResponse{
		Content:      full.String(),
		TokensUsed:   0,
		FinishReason: "stop",
	}, nil
}
