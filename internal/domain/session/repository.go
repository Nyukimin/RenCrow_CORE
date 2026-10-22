package session

import "context"

// CreationIntent is immutable owner metadata attached atomically to a
// canonical session reservation. It is exposed as a lookup result, not as a
// mutable Session field.
type CreationIntent struct {
	Runtime string `json:"runtime"`
	OwnerID string `json:"owner_id"`
}

// SessionRepository はセッション永続化の抽象化
type SessionRepository interface {
	Save(ctx context.Context, session *Session) error
	Load(ctx context.Context, id string) (*Session, error)
	Delete(ctx context.Context, id string) error
	Exists(ctx context.Context, id string) (bool, error)
}
