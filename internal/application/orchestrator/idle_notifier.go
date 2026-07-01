package orchestrator

// IdleNotifier はIdleChat制御向けの活動通知インターフェース。
type IdleNotifier interface {
	NotifyActivity()
	SetChatBusy(busy bool)
	SetWorkerBusy(busy bool)
}
