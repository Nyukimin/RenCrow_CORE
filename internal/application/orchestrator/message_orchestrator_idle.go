package orchestrator

import "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"

type idleBusyGuardFactory struct {
	idleNotifier IdleNotifier
}

func newIdleBusyGuardFactory(idleNotifier IdleNotifier) *idleBusyGuardFactory {
	return &idleBusyGuardFactory{idleNotifier: idleNotifier}
}

func (f *idleBusyGuardFactory) SetNotifier(idleNotifier IdleNotifier) {
	f.idleNotifier = idleNotifier
}

func (f *idleBusyGuardFactory) BeginChat() func() {
	if f.idleNotifier == nil {
		return func() {}
	}
	f.idleNotifier.NotifyActivity()
	f.idleNotifier.SetChatBusy(true)
	return func() {
		f.idleNotifier.SetChatBusy(false)
	}
}

func (f *idleBusyGuardFactory) BeginWorker(route routing.Route) func() {
	if f.idleNotifier == nil || route == routing.RouteCHAT {
		return func() {}
	}
	f.idleNotifier.SetWorkerBusy(true)
	return func() {
		f.idleNotifier.SetWorkerBusy(false)
	}
}
