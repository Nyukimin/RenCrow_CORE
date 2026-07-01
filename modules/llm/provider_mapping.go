package llm

import (
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
)

const (
	RoleChat   = "chat"
	RoleWorker = "worker"
	RoleHeavy  = "heavy"
	RoleWild   = "wild"
)

type RoleProviders struct {
	Chat   Provider
	Worker Provider
	Heavy  Provider
	Wild   Provider
}

type GenerateOutput struct {
	Content      string
	TokensUsed   int
	FinishReason string
	ResponseID   core.ResponseID
}

func BuildRoleProviderMap(providers RoleProviders) map[string]Provider {
	return map[string]Provider{
		RoleChat:   providers.Chat,
		RoleWorker: providers.Worker,
		RoleHeavy:  providers.Heavy,
		RoleWild:   providers.Wild,
	}
}

func NormalizeRoleName(role string) string {
	return strings.ToLower(strings.TrimSpace(role))
}

func BuildGenerateResponse(output GenerateOutput) GenerateResponse {
	return GenerateResponse{
		Content:      output.Content,
		TokensUsed:   output.TokensUsed,
		FinishReason: output.FinishReason,
		ResponseID:   output.ResponseID,
	}
}
