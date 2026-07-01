package middleware

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	domainllm "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
)

// RawLogProvider logs raw LLM responses for every Generate/Chat call.
type RawLogProvider struct {
	inner domainllm.LLMProvider
	name  string
}

var (
	chatRawOnce   sync.Once
	chatRawFile   *os.File
	chatRawErr    error
	chatRawMu     sync.Mutex
	workerRawOnce sync.Once
	workerRawFile *os.File
	workerRawErr  error
	workerRawMu   sync.Mutex
	idleRawOnce   sync.Once
	idleRawFile   *os.File
	idleRawErr    error
	idleRawMu     sync.Mutex
)

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

	ts := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := f.WriteString(
		fmt.Sprintf("ts=%s kind=%s provider=%s finish=%s max_tokens=%d msgs=%d\n", ts, kind, provider, finish, maxTokens, msgCount),
	); err != nil {
		log.Printf("[LLM][raw] chat raw write header failed: %v", err)
		return
	}
	if _, err := f.WriteString(content); err != nil {
		log.Printf("[LLM][raw] chat raw write content failed: %v", err)
		return
	}
	if !strings.HasSuffix(content, "\n") {
		if _, err := f.WriteString("\n"); err != nil {
			log.Printf("[LLM][raw] chat raw write newline failed: %v", err)
			return
		}
	}
	if _, err := f.WriteString("----\n"); err != nil {
		log.Printf("[LLM][raw] chat raw write separator failed: %v", err)
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

	ts := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := f.WriteString(
		fmt.Sprintf("ts=%s kind=%s provider=%s finish=%s max_tokens=%d msgs=%d\n", ts, kind, provider, finish, maxTokens, msgCount),
	); err != nil {
		log.Printf("[LLM][raw] worker raw write header failed: %v", err)
		return
	}
	if _, err := f.WriteString(content); err != nil {
		log.Printf("[LLM][raw] worker raw write content failed: %v", err)
		return
	}
	if !strings.HasSuffix(content, "\n") {
		if _, err := f.WriteString("\n"); err != nil {
			log.Printf("[LLM][raw] worker raw write newline failed: %v", err)
			return
		}
	}
	if _, err := f.WriteString("----\n"); err != nil {
		log.Printf("[LLM][raw] worker raw write separator failed: %v", err)
		return
	}
	if err := f.Sync(); err != nil {
		log.Printf("[LLM][raw] worker raw fsync failed: %v", err)
	}
}

func openChatRawFile() *os.File {
	chatRawOnce.Do(func() {
		path := strings.TrimSpace(os.Getenv("PICOCLAW_CHAT_RAW_LOG"))
		if path == "" {
			path = "/home/nyukimi/.picoclaw/logs/chat_raw.log"
		}
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			chatRawErr = err
			log.Printf("[LLM][raw] chat raw mkdir failed: %v", err)
			return
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			chatRawErr = err
			log.Printf("[LLM][raw] chat raw open failed: %v", err)
			return
		}
		chatRawFile = f
		log.Printf("[LLM][raw] chat raw file enabled: %s", path)
	})
	if chatRawErr != nil {
		return nil
	}
	return chatRawFile
}

func openWorkerRawFile() *os.File {
	workerRawOnce.Do(func() {
		path := strings.TrimSpace(os.Getenv("PICOCLAW_WORKER_RAW_LOG"))
		if path == "" {
			path = "/home/nyukimi/.picoclaw/logs/worker_raw.log"
		}
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			workerRawErr = err
			log.Printf("[LLM][raw] worker raw mkdir failed: %v", err)
			return
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			workerRawErr = err
			log.Printf("[LLM][raw] worker raw open failed: %v", err)
			return
		}
		workerRawFile = f
		log.Printf("[LLM][raw] worker raw file enabled: %s", path)
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

	ts := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := f.WriteString(
		fmt.Sprintf("ts=%s speaker=%s kind=%s provider=%s finish=%s max_tokens=%d msgs=%d\n", ts, speaker, kind, provider, finish, maxTokens, msgCount),
	); err != nil {
		log.Printf("[LLM][raw] idle raw write header failed: %v", err)
		return
	}
	if _, err := f.WriteString(content); err != nil {
		log.Printf("[LLM][raw] idle raw write content failed: %v", err)
		return
	}
	if !strings.HasSuffix(content, "\n") {
		if _, err := f.WriteString("\n"); err != nil {
			log.Printf("[LLM][raw] idle raw write newline failed: %v", err)
			return
		}
	}
	if _, err := f.WriteString("----\n"); err != nil {
		log.Printf("[LLM][raw] idle raw write separator failed: %v", err)
		return
	}
	if err := f.Sync(); err != nil {
		log.Printf("[LLM][raw] idle raw fsync failed: %v", err)
	}
}

func openIdleChatRawFile() *os.File {
	idleRawOnce.Do(func() {
		path := strings.TrimSpace(os.Getenv("PICOCLAW_IDLECHAT_RAW_LOG"))
		if path == "" {
			path = "/home/nyukimi/.picoclaw/logs/IdleChat_raw.log"
		}
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			idleRawErr = err
			log.Printf("[LLM][raw] idle raw mkdir failed: %v", err)
			return
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			idleRawErr = err
			log.Printf("[LLM][raw] idle raw open failed: %v", err)
			return
		}
		idleRawFile = f
		log.Printf("[LLM][raw] idle raw file enabled: %s", path)
	})
	if idleRawErr != nil {
		return nil
	}
	return idleRawFile
}
