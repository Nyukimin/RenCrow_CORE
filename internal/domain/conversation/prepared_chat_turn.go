package conversation

// PreparedChatTurnGuard carries opaque, turn-scoped eligibility for a ready
// RecallPack. It exposes no mutable projection or session-manager state.
type PreparedChatTurnGuard interface {
	// OwnerID and SourceVersion identify private prepared recall. They are empty
	// for a public head-only turn.
	OwnerID() string
	SourceVersion() string
	BindingGeneration() BindingGeneration
	MatchesPreparation(*ConversationTurnPreparation) bool
	// Validate is a short RAM-only check for the bound head and prepared source.
	// Call it before prompt or retrieval use while this preparation is live.
	Validate() error
	// Use holds the prepared projection generation and head-lifecycle read guards
	// while enqueue runs. It does not require the consumed head preparation to
	// remain current: ordinary EndTurn advances it before final publication,
	// while reset/flush lifecycle changes still revoke this guard.
	// The callback must perform only a bounded in-memory admission/enqueue; it
	// must not perform DB, HTTP, LLM, or other external I/O.
	Use(enqueue func() error) error
	// RetainPublication creates a bounded, revocable qualification for a pending
	// live publication. It must be called from inside Use. The returned handle
	// does not retain the conversation-head lease and remains usable after this
	// guard is released; callers must release it on every claim/drop path.
	RetainPublication() (PreparedChatTurnPublicationGuard, error)
	// Release ends the projection lease and abandons any still-live head lease.
	// It is safe to call after a commit consumed the head preparation.
	Release() error
}

// PreparedChatTurnPublicationGuard qualifies one pending live publication at
// the writer claim point. It binds the exact owner/source generation and
// session head-lifecycle epoch. Retain creates another reference to the same
// qualification; it does not extend its fixed maximum lifetime. Implementations
// must keep Use short and RAM-only.
type PreparedChatTurnPublicationGuard interface {
	Use(claim func() error) error
	Retain() PreparedChatTurnPublicationGuard
	Release()
}

// PreparedChatTurn is the pack and canonical RAM head admitted together for
// one binding generation.
type PreparedChatTurn struct {
	RecallPack  *RecallPack
	Preparation *ConversationTurnPreparation
	Guard       PreparedChatTurnGuard
}
