package idlechat

import (
	"errors"
	"sync"
)

var errIdleChatGenerationWorkClosed = errors.New("idlechat generation work admission is closed")

// generationWorkLease keeps an admitted asynchronous generation alive until
// its Task/Run cleanup has completed. The lease is also used by callers that
// need to do a small amount of synchronous owner admission before launching
// their playback goroutine.
type generationWorkLease struct {
	once sync.Once
	done func()
}

func (l *generationWorkLease) release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.done != nil {
			l.done()
		}
	})
}

// acquireGenerationWork atomically admits one asynchronous generation before
// Stop can close the gate. The WaitGroup count is positive before the mutex is
// released, so Stop may safely wait while a just-admitted caller is launching
// its goroutine.
func (o *IdleChatOrchestrator) acquireGenerationWork() (*generationWorkLease, bool) {
	if o == nil {
		return nil, false
	}
	o.generationWorkMu.Lock()
	defer o.generationWorkMu.Unlock()
	if o.generationWorkClosed {
		return nil, false
	}
	o.generationWorkWG.Add(1)
	return &generationWorkLease{done: o.generationWorkWG.Done}, true
}

func (o *IdleChatOrchestrator) launchGenerationWork(lease *generationWorkLease, work func()) {
	if lease == nil {
		return
	}
	go func() {
		defer lease.release()
		if work != nil {
			work()
		}
	}()
}

func (o *IdleChatOrchestrator) startGenerationWork(work func()) bool {
	lease, ok := o.acquireGenerationWork()
	if !ok {
		return false
	}
	o.launchGenerationWork(lease, work)
	return true
}

func (o *IdleChatOrchestrator) closeGenerationWorkAdmission() {
	if o == nil {
		return
	}
	o.generationWorkMu.Lock()
	o.generationWorkClosed = true
	o.generationWorkMu.Unlock()
}

func (o *IdleChatOrchestrator) reopenGenerationWorkAdmission() {
	if o == nil || o.ctx == nil || o.ctx.Err() != nil {
		return
	}
	o.generationWorkMu.Lock()
	defer o.generationWorkMu.Unlock()
	if o.ctx.Err() == nil {
		o.generationWorkClosed = false
	}
}

func (o *IdleChatOrchestrator) waitGenerationWork() {
	if o == nil {
		return
	}
	o.generationWorkWG.Wait()
}
