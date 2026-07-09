package middleware

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

const (
	LLMQueuePolicyWait   = "wait"
	LLMQueuePolicyReject = "reject"
)

type LLMTimeoutPhase string

const (
	LLMTimeoutPhaseQueue    LLMTimeoutPhase = "queue"
	LLMTimeoutPhaseGenerate LLMTimeoutPhase = "generate"
)

type LLMPhaseError struct {
	Phase LLMTimeoutPhase
	Err   error
}

func (e LLMPhaseError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("llm %s failed", e.Phase)
	}
	return fmt.Sprintf("llm %s failed: %v", e.Phase, e.Err)
}

func (e LLMPhaseError) Unwrap() error {
	return e.Err
}

type LimitedProviderOptions struct {
	Alias             string
	QueueTimeout      time.Duration
	GenerationTimeout time.Duration
	QueuePolicy       string
}

// LimitedProvider bounds concurrent requests for an LLMProvider.
// Multiple LimitedProviders can share the same global semaphore while keeping
// their own per-model semaphore.
type LimitedProvider struct {
	inner             domainllm.LLMProvider
	name              string
	alias             string
	global            chan struct{}
	model             chan struct{}
	queueTimeout      time.Duration
	generationTimeout time.Duration
	queuePolicy       string
}

func NewLimitedProvider(inner domainllm.LLMProvider, name string, global, model chan struct{}) *LimitedProvider {
	return NewLimitedProviderWithOptions(inner, name, global, model, LimitedProviderOptions{})
}

func NewLimitedProviderWithOptions(inner domainllm.LLMProvider, name string, global, model chan struct{}, opts LimitedProviderOptions) *LimitedProvider {
	queuePolicy := strings.ToLower(strings.TrimSpace(opts.QueuePolicy))
	if queuePolicy == "" {
		queuePolicy = LLMQueuePolicyWait
	}
	return &LimitedProvider{
		inner:             inner,
		name:              name,
		alias:             strings.TrimSpace(opts.Alias),
		global:            global,
		model:             model,
		queueTimeout:      opts.QueueTimeout,
		generationTimeout: opts.GenerationTimeout,
		queuePolicy:       queuePolicy,
	}
}

func (p *LimitedProvider) Generate(ctx context.Context, req domainllm.GenerateRequest) (domainllm.GenerateResponse, error) {
	totalStart := time.Now()
	release, waited, err := p.acquire(ctx)
	if err != nil {
		log.Printf("[LLM][client_queue] llm.generate.error provider=%s alias=%s phase=queue waited_ms=%d error=%q total_ms=%d",
			p.Name(), p.Alias(), waited.Milliseconds(), err.Error(), time.Since(totalStart).Milliseconds())
		return domainllm.GenerateResponse{}, err
	}
	defer release()
	generationCtx, cancel := p.generationContext(ctx)
	defer cancel()
	log.Printf("[LLM][client_queue] llm.generate.start provider=%s alias=%s timeout_ms=%d max_tokens=%d",
		p.Name(), p.Alias(), p.generationTimeout.Milliseconds(), req.MaxTokens)
	generationStart := time.Now()
	resp, err := p.inner.Generate(generationCtx, req)
	elapsed := time.Since(generationStart)
	total := time.Since(totalStart)
	if err != nil {
		phase := LLMTimeoutPhaseGenerate
		if generationCtx.Err() != nil {
			log.Printf("[LLM][client_queue] llm.generate.timeout provider=%s alias=%s elapsed_ms=%d timeout_ms=%d total_ms=%d error=%q",
				p.Name(), p.Alias(), elapsed.Milliseconds(), p.generationTimeout.Milliseconds(), total.Milliseconds(), err.Error())
		}
		log.Printf("[LLM][client_queue] llm.generate.error provider=%s alias=%s phase=%s elapsed_ms=%d total_ms=%d error=%q",
			p.Name(), p.Alias(), phase, elapsed.Milliseconds(), total.Milliseconds(), err.Error())
		return domainllm.GenerateResponse{}, LLMPhaseError{Phase: phase, Err: err}
	}
	log.Printf("[LLM][client_queue] llm.generate.done provider=%s alias=%s elapsed_ms=%d total_ms=%d finish=%q tokens=%d",
		p.Name(), p.Alias(), elapsed.Milliseconds(), total.Milliseconds(), strings.TrimSpace(resp.FinishReason), resp.TokensUsed)
	return resp, nil
}

func (p *LimitedProvider) Name() string {
	if p.name != "" {
		return p.name
	}
	return p.inner.Name()
}

func (p *LimitedProvider) Alias() string {
	if p.alias != "" {
		return p.alias
	}
	return p.Name()
}

func (p *LimitedProvider) Chat(ctx context.Context, req domainllm.ChatRequest) (domainllm.ChatResponse, error) {
	tcp, ok := p.inner.(domainllm.ToolCallingProvider)
	if !ok {
		return domainllm.ChatResponse{}, fmt.Errorf("inner provider does not support Chat")
	}
	totalStart := time.Now()
	release, waited, err := p.acquire(ctx)
	if err != nil {
		log.Printf("[LLM][client_queue] llm.generate.error provider=%s alias=%s phase=queue waited_ms=%d error=%q total_ms=%d",
			p.Name(), p.Alias(), waited.Milliseconds(), err.Error(), time.Since(totalStart).Milliseconds())
		return domainllm.ChatResponse{}, err
	}
	defer release()
	generationCtx, cancel := p.generationContext(ctx)
	defer cancel()
	log.Printf("[LLM][client_queue] llm.generate.start provider=%s alias=%s timeout_ms=%d max_tokens=0",
		p.Name(), p.Alias(), p.generationTimeout.Milliseconds())
	generationStart := time.Now()
	resp, err := tcp.Chat(generationCtx, req)
	elapsed := time.Since(generationStart)
	total := time.Since(totalStart)
	if err != nil {
		phase := LLMTimeoutPhaseGenerate
		if generationCtx.Err() != nil {
			log.Printf("[LLM][client_queue] llm.generate.timeout provider=%s alias=%s elapsed_ms=%d timeout_ms=%d total_ms=%d error=%q",
				p.Name(), p.Alias(), elapsed.Milliseconds(), p.generationTimeout.Milliseconds(), total.Milliseconds(), err.Error())
		}
		log.Printf("[LLM][client_queue] llm.generate.error provider=%s alias=%s phase=%s elapsed_ms=%d total_ms=%d error=%q",
			p.Name(), p.Alias(), phase, elapsed.Milliseconds(), total.Milliseconds(), err.Error())
		return domainllm.ChatResponse{}, LLMPhaseError{Phase: phase, Err: err}
	}
	log.Printf("[LLM][client_queue] llm.generate.done provider=%s alias=%s elapsed_ms=%d total_ms=%d finish=%q tokens=0",
		p.Name(), p.Alias(), elapsed.Milliseconds(), total.Milliseconds(), strings.TrimSpace(resp.FinishReason))
	return resp, nil
}

func (p *LimitedProvider) acquire(parent context.Context) (func(), time.Duration, error) {
	start := time.Now()
	log.Printf("[LLM][client_queue] llm.queue.wait.start provider=%s alias=%s queue_policy=%s timeout_ms=%d",
		p.Name(), p.Alias(), p.queuePolicy, p.queueTimeout.Milliseconds())
	ctx := parent
	cancel := func() {}
	if p.queuePolicy == LLMQueuePolicyReject {
		ctx, cancel = context.WithCancel(parent)
	} else if p.queueTimeout > 0 {
		var cancelFunc context.CancelFunc
		ctx, cancelFunc = context.WithTimeout(parent, p.queueTimeout)
		cancel = cancelFunc
	}
	defer cancel()
	acquiredGlobal := false
	acquiredModel := false
	release := func() {
		if acquiredModel && p.model != nil {
			<-p.model
		}
		if acquiredGlobal && p.global != nil {
			<-p.global
		}
	}
	if p.global != nil {
		if p.queuePolicy == LLMQueuePolicyReject {
			select {
			case p.global <- struct{}{}:
				acquiredGlobal = true
			default:
				waited := time.Since(start)
				log.Printf("[LLM][client_queue] llm.queue.timeout provider=%s alias=%s waited_ms=%d timeout_ms=%d scope=global",
					p.Name(), p.Alias(), waited.Milliseconds(), p.queueTimeout.Milliseconds())
				return nil, waited, LLMPhaseError{Phase: LLMTimeoutPhaseQueue, Err: context.DeadlineExceeded}
			}
		} else {
			select {
			case p.global <- struct{}{}:
				acquiredGlobal = true
			case <-ctx.Done():
				waited := time.Since(start)
				log.Printf("[LLM][client_queue] llm.queue.timeout provider=%s alias=%s waited_ms=%d timeout_ms=%d scope=global",
					p.Name(), p.Alias(), waited.Milliseconds(), p.queueTimeout.Milliseconds())
				return nil, waited, LLMPhaseError{Phase: LLMTimeoutPhaseQueue, Err: ctx.Err()}
			}
		}
	}
	if p.model != nil {
		if p.queuePolicy == LLMQueuePolicyReject {
			select {
			case p.model <- struct{}{}:
				acquiredModel = true
			default:
				release()
				waited := time.Since(start)
				log.Printf("[LLM][client_queue] llm.queue.timeout provider=%s alias=%s waited_ms=%d timeout_ms=%d scope=model",
					p.Name(), p.Alias(), waited.Milliseconds(), p.queueTimeout.Milliseconds())
				return nil, waited, LLMPhaseError{Phase: LLMTimeoutPhaseQueue, Err: context.DeadlineExceeded}
			}
		} else {
			select {
			case p.model <- struct{}{}:
				acquiredModel = true
			case <-ctx.Done():
				release()
				waited := time.Since(start)
				log.Printf("[LLM][client_queue] llm.queue.timeout provider=%s alias=%s waited_ms=%d timeout_ms=%d scope=model",
					p.Name(), p.Alias(), waited.Milliseconds(), p.queueTimeout.Milliseconds())
				return nil, waited, LLMPhaseError{Phase: LLMTimeoutPhaseQueue, Err: ctx.Err()}
			}
		}
	}
	waited := time.Since(start)
	log.Printf("[LLM][client_queue] llm.queue.wait.done provider=%s alias=%s waited_ms=%d",
		p.Name(), p.Alias(), waited.Milliseconds())
	return release, waited, nil
}

func (p *LimitedProvider) generationContext(parent context.Context) (context.Context, context.CancelFunc) {
	if p.generationTimeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, p.generationTimeout)
}
