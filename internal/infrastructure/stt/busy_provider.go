package stt

import (
	"context"

	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
)

type busyPolicyProvider struct {
	inner  Provider
	policy string
	sem    chan struct{}
	queue  chan transcribeRequest
}

type transcribeRequest struct {
	ctx  context.Context
	wav  []byte
	resp chan transcribeResponse
}

type transcribeResponse struct {
	result Result
	err    error
}

func NewBusyPolicyProvider(inner Provider, policy string) Provider {
	if inner == nil {
		return nil
	}
	plan := modulestt.BuildBusyPolicyPlan(policy)
	p := &busyPolicyProvider{
		inner:  inner,
		policy: plan.Policy,
		sem:    make(chan struct{}, 1),
	}
	if plan.UsesQueue {
		p.queue = make(chan transcribeRequest, 1)
		go p.runQueue()
	}
	return p
}

func (p *busyPolicyProvider) Name() string {
	return p.inner.Name()
}

func (p *busyPolicyProvider) Health(ctx context.Context) Health {
	return p.inner.Health(ctx)
}

func (p *busyPolicyProvider) Transcribe(ctx context.Context, wav []byte) (Result, error) {
	switch p.policy {
	case BusyPolicyDirect:
		return p.inner.Transcribe(ctx, wav)
	case BusyPolicyReject:
		return p.transcribeRejectBusy(ctx, wav)
	case BusyPolicyQueueLatest:
		return p.transcribeQueueLatest(ctx, wav)
	default:
		return p.transcribeQueueLatest(ctx, wav)
	}
}

func (p *busyPolicyProvider) transcribeRejectBusy(ctx context.Context, wav []byte) (Result, error) {
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
		return p.inner.Transcribe(ctx, wav)
	default:
		return Result{}, NewError(ErrorProviderBusy, "stt provider is busy", nil)
	}
}

func (p *busyPolicyProvider) transcribeQueueLatest(ctx context.Context, wav []byte) (Result, error) {
	req := transcribeRequest{
		ctx:  ctx,
		wav:  append([]byte(nil), wav...),
		resp: make(chan transcribeResponse, 1),
	}
	for {
		select {
		case p.queue <- req:
			return waitTranscribeResponse(ctx, req.resp)
		default:
			select {
			case old := <-p.queue:
				old.resp <- transcribeResponse{err: NewError(ErrorProviderBusy, "stt request superseded by newer audio", nil)}
			default:
			}
		}
	}
}

func (p *busyPolicyProvider) runQueue() {
	for req := range p.queue {
		if err := req.ctx.Err(); err != nil {
			req.resp <- transcribeResponse{err: err}
			continue
		}
		result, err := p.inner.Transcribe(req.ctx, req.wav)
		req.resp <- transcribeResponse{result: result, err: err}
	}
}

func waitTranscribeResponse(ctx context.Context, resp <-chan transcribeResponse) (Result, error) {
	select {
	case out := <-resp:
		return out.result, out.err
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}
