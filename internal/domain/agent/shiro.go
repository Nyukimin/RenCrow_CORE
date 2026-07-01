package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
)

const shiroMaxTokens = 4096

// ShiroAgent は Worker（実行・道具係）を担当するエンティティ
type ShiroAgent struct {
	llmProvider     llm.LLMProvider
	toolRunner      ToolRunner
	mcpClient       MCPClient
	systemPrompt    string
	subagentManager SubagentManager // v1.0: ReActループ統合
	persona         *AgentPersona   // v4.2: Optional Agent Persona
	lightMemory     *LightMemory    // Optional: short-term memory
	conversation    conversation.ConversationEngine
}

// NewShiroAgent は新しいShiroAgentを作成
func NewShiroAgent(
	llmProvider llm.LLMProvider,
	toolRunner ToolRunner,
	mcpClient MCPClient,
	systemPrompt string,
	subagentManager SubagentManager,
) *ShiroAgent {
	return &ShiroAgent{
		llmProvider:     llmProvider,
		toolRunner:      toolRunner,
		mcpClient:       mcpClient,
		systemPrompt:    systemPrompt,
		subagentManager: subagentManager,
	}
}

// WithPersona は AgentPersona を設定する（Builder パターン）
func (s *ShiroAgent) WithPersona(persona AgentPersona) *ShiroAgent {
	s.persona = &persona
	return s
}

// WithLightMemory は LightMemory を設定する（Builder パターン）。
func (s *ShiroAgent) WithLightMemory(memory *LightMemory) *ShiroAgent {
	s.lightMemory = memory
	return s
}

func (s *ShiroAgent) WithConversationEngine(engine conversation.ConversationEngine) *ShiroAgent {
	s.conversation = engine
	return s
}

// Execute はWorkerタスクを実行
// v1.0: SubagentManager が設定されている場合は ReActLoop を使ってツールを自律的に選択・実行する
func (s *ShiroAgent) Execute(ctx context.Context, t task.Task) (string, error) {
	systemPrompt := s.systemPrompt
	if s.persona != nil {
		systemPrompt = s.persona.BuildSystemPrompt(s.systemPrompt)
	}
	systemPrompt = ensureShiroJapaneseResponsePrompt(systemPrompt)

	// SubagentManager が設定されている場合は ReActLoop を使用
	if s.subagentManager != nil {
		result, err := s.runSubagentSafely(ctx, SubagentTask{
			AgentName:    "shiro",
			Instruction:  t.UserMessage(),
			SystemPrompt: systemPrompt,
		})
		if err != nil {
			return "", err
		}
		return result.Output, nil
	}

	messages := []llm.Message{{Role: "system", Content: systemPrompt}}
	if s.conversation != nil {
		recallPack, err := s.conversation.BeginTurn(ctx, t.ChatID(), t.UserMessage())
		if err != nil {
			log.Printf("[Shiro] BeginTurn failed: %v", err)
		} else if recallPack != nil {
			filtered := recallPack.FilterForRole("worker")
			if err := recordRecallTrace(ctx, s.conversation, t.ChatID(), t.JobID().String(), "worker", filtered); err != nil {
				log.Printf("[Shiro] RecordRecallTrace failed: %v", err)
			}
			messages = append(messages, filtered.ToPromptMessages()...)
		}
	}
	if s.lightMemory != nil {
		messages = append(messages, s.lightMemory.RecentMessages(t.ChatID())...)
	}
	messages = append(messages, userMessageWithAttachments(t.UserMessage(), t.Attachments()))
	req := llm.GenerateRequest{
		Messages:    messages,
		MaxTokens:   shiroMaxTokens,
		Temperature: 0.3, // Workerは確実性重視
	}

	resp, err := s.llmProvider.Generate(ctx, req)
	if err != nil {
		return "", err
	}
	if s.lightMemory != nil {
		s.lightMemory.Record(t.ChatID(), t.UserMessage(), resp.Content)
	}

	if s.conversation != nil {
		if err := endConversationTurnAs(ctx, s.conversation, t.ChatID(), t.UserMessage(), resp.Content, conversation.SpeakerShiro); err != nil {
			log.Printf("[Shiro] EndTurn failed: %v", err)
		}
	}
	return resp.Content, nil
}

func (s *ShiroAgent) runSubagentSafely(ctx context.Context, t SubagentTask) (res SubagentResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("subagent runtime panic: %v", r)
		}
	}()
	res, err = s.subagentManager.RunSync(ctx, t)
	return res, err
}

func ensureShiroJapaneseResponsePrompt(systemPrompt string) string {
	const guard = "出力言語の絶対ルール: Shiroは必ず自然な日本語で応答する。英語での説明、英語の見出し、英語だけの完了報告は禁止する。ユーザーが英語を求めた場合だけ、必要最小限の英語を併記してよい。"
	if strings.Contains(systemPrompt, "必ず自然な日本語で応答") {
		return systemPrompt
	}
	if systemPrompt == "" {
		return guard
	}
	return systemPrompt + "\n\n" + guard
}

// ExecuteTool はツールを実行
func (s *ShiroAgent) ExecuteTool(ctx context.Context, toolName string, args map[string]interface{}) (string, error) {
	resp, err := s.toolRunner.ExecuteV2(ctx, toolName, args)
	if err != nil {
		return "", err
	}
	if resp.IsError() {
		return "", fmt.Errorf("%s", resp.Error.Message)
	}
	return resp.String(), nil
}

// ExecuteMCPTool はMCPツールを実行
func (s *ShiroAgent) ExecuteMCPTool(ctx context.Context, serverName, toolName string, args map[string]interface{}) (string, error) {
	return s.mcpClient.CallTool(ctx, serverName, toolName, args)
}
