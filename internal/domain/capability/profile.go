package capability

// Profile はノードの動作プロファイル
type Profile string

const (
	ProfileCoderHigh   Profile = "coder-high"   // Claude API 疎通OK
	ProfileCoderMid    Profile = "coder-mid"     // OpenAI / DeepSeek 疎通OK
	ProfileCoderLocal  Profile = "coder-local"   // Ollama のみ 8GB+
	ProfileWorkerOnly  Profile = "worker-only"   // Ollama のみ 4GB未満
	ProfileUnavailable Profile = "unavailable"   // LLM なし
)

const (
	memoryThresholdCoderLocalMB uint64 = 8 * 1024 // 8GB
)

// DetermineProfile は NodeCapabilities からノードの動作プロファイルを決定する。
// 判定優先順: Claude API > OpenAI/DeepSeek > Ollama(メモリ) > unavailable
func DetermineProfile(caps NodeCapabilities) Profile {
	for _, llm := range caps.LLMs {
		if !llm.Available {
			continue
		}
		switch llm.ProviderName {
		case "claude":
			return ProfileCoderHigh
		}
	}

	for _, llm := range caps.LLMs {
		if !llm.Available {
			continue
		}
		switch llm.ProviderName {
		case "openai", "deepseek":
			return ProfileCoderMid
		}
	}

	// Ollama のみ利用可能な場合はメモリで判定
	for _, llm := range caps.LLMs {
		if llm.Available && llm.ProviderName == "ollama" {
			if caps.Memory.AvailableMB >= memoryThresholdCoderLocalMB {
				return ProfileCoderLocal
			}
			return ProfileWorkerOnly
		}
	}

	return ProfileUnavailable
}
