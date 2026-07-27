package middleware

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// RawLogProvider logs raw LLM responses for every Generate/Chat call.
type RawLogProvider struct {
	inner domainllm.LLMProvider
	name  string
}

var (
	chatRawOnce   sync.Once
	chatRawFile   rawLogSink
	chatRawErr    error
	chatRawMu     sync.Mutex
	workerRawOnce sync.Once
	workerRawFile rawLogSink
	workerRawErr  error
	workerRawMu   sync.Mutex
	idleRawOnce   sync.Once
	idleRawFile   rawLogSink
	idleRawErr    error
	idleRawMu     sync.Mutex
)

// rawLogSink は生応答ログの書き出し先
//
// RotatingWriter を受けるためのインターフェース。上限に達すると世代交代する。
type rawLogSink interface {
	Write(p []byte) (int, error)
	Sync() error
	Close() error
}

func NewRawLogProvider(inner domainllm.LLMProvider, name string) *RawLogProvider {
	return &RawLogProvider{
		inner: inner,
		name:  strings.TrimSpace(name),
	}
}

func (p *RawLogProvider) Generate(ctx context.Context, req domainllm.GenerateRequest) (domainllm.GenerateResponse, error) {
	resp, err := p.inner.Generate(ctx, req)
	if err == nil {
		log.Printf(
			"[LLM][raw] provider=%s finish=%s tokens=%d max_tokens=%d msgs=%d content=%q",
			p.Name(),
			strings.TrimSpace(resp.FinishReason),
			resp.TokensUsed,
			req.MaxTokens,
			len(req.Messages),
			resp.Content,
		)
		switch strings.ToLower(strings.TrimSpace(p.Name())) {
		case "chat":
			writeChatRaw("generate", p.Name(), strings.TrimSpace(resp.FinishReason), req.MaxTokens, len(req.Messages), resp.Content)
		case "worker", "chatworker":
			writeWorkerRaw("generate", p.Name(), strings.TrimSpace(resp.FinishReason), req.MaxTokens, len(req.Messages), resp.Content)
		}
	}
	return resp, err
}

func (p *RawLogProvider) Name() string {
	if p.name != "" {
		return p.name
	}
	return p.inner.Name()
}

func (p *RawLogProvider) Chat(ctx context.Context, req domainllm.ChatRequest) (domainllm.ChatResponse, error) {
	tcp, ok := p.inner.(domainllm.ToolCallingProvider)
	if !ok {
		return domainllm.ChatResponse{}, fmt.Errorf("inner provider does not support Chat")
	}
	resp, err := tcp.Chat(ctx, req)
	if err == nil {
		log.Printf(
			"[LLM][raw] provider=%s chat_finish=%s chat_msgs=%d chat_content=%q",
			p.Name(),
			strings.TrimSpace(resp.FinishReason),
			len(req.Messages),
			resp.Message.Content,
		)
		switch strings.ToLower(strings.TrimSpace(p.Name())) {
		case "chat":
			writeChatRaw("chat", p.Name(), strings.TrimSpace(resp.FinishReason), 0, len(req.Messages), resp.Message.Content)
		case "worker", "chatworker":
			writeWorkerRaw("chat", p.Name(), strings.TrimSpace(resp.FinishReason), 0, len(req.Messages), resp.Message.Content)
		}
	}
	return resp, err
}

func writeChatRaw(kind, provider, finish string, maxTokens int, msgCount int, content string) {
	f := openChatRawFile()
	if f == nil {
		return
	}
	chatRawMu.Lock()
	defer chatRawMu.Unlock()

	// 1エントリを1回で書く。分割するとローテーション境界でログが分断される
	if _, err := f.Write([]byte(buildRawLogEntry(kind, provider, finish, maxTokens, msgCount, content))); err != nil {
		log.Printf("[LLM][raw] chat raw write failed: %v", err)
		return
	}
	if err := f.Sync(); err != nil {
		log.Printf("[LLM][raw] chat raw fsync failed: %v", err)
	}
}

func writeWorkerRaw(kind, provider, finish string, maxTokens int, msgCount int, content string) {
	f := openWorkerRawFile()
	if f == nil {
		return
	}
	workerRawMu.Lock()
	defer workerRawMu.Unlock()

	// 1エントリを1回で書く。分割するとローテーション境界でログが分断される
	if _, err := f.Write([]byte(buildRawLogEntry(kind, provider, finish, maxTokens, msgCount, content))); err != nil {
		log.Printf("[LLM][raw] worker raw write failed: %v", err)
		return
	}
	if err := f.Sync(); err != nil {
		log.Printf("[LLM][raw] worker raw fsync failed: %v", err)
	}
}

func openChatRawFile() rawLogSink {
	chatRawOnce.Do(func() {
		chatRawFile, chatRawErr = openRawLogSink("RENCROW_CHAT_RAW_LOG", "chat_raw.log", "chat")
	})
	if chatRawErr != nil {
		return nil
	}
	return chatRawFile
}

func openWorkerRawFile() rawLogSink {
	workerRawOnce.Do(func() {
		workerRawFile, workerRawErr = openRawLogSink("RENCROW_WORKER_RAW_LOG", "worker_raw.log", "worker")
	})
	if workerRawErr != nil {
		return nil
	}
	return workerRawFile
}

func writeIdleChatRaw(speaker, kind, provider, finish string, maxTokens int, msgCount int, content string) {
	f := openIdleChatRawFile()
	if f == nil {
		return
	}
	idleRawMu.Lock()
	defer idleRawMu.Unlock()

	// 1エントリを1回で書く。分割するとローテーション境界でログが分断される
	entry := buildRawLogEntry(kind, provider, finish, maxTokens, msgCount, content)
	entry = strings.Replace(entry, " kind=", " speaker="+speaker+" kind=", 1)
	if _, err := f.Write([]byte(entry)); err != nil {
		log.Printf("[LLM][raw] idle raw write failed: %v", err)
		return
	}
	if err := f.Sync(); err != nil {
		log.Printf("[LLM][raw] idle raw fsync failed: %v", err)
	}
}

func openIdleChatRawFile() rawLogSink {
	idleRawOnce.Do(func() {
		idleRawFile, idleRawErr = openRawLogSink("RENCROW_IDLECHAT_RAW_LOG", "IdleChat_raw.log", "idlechat")
	})
	if idleRawErr != nil {
		return nil
	}
	return idleRawFile
}
