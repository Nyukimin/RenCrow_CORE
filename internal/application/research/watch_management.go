package research

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
)

// WatchManagementItem contains only safe owner state. Payloads, cursors, and
// lease credentials remain inside the persistence owner.
type WatchManagementItem struct {
	WatchID       string    `json:"watch_id"`
	OwnerID       string    `json:"owner_id"`
	Scope         string    `json:"scope"`
	Enabled       bool      `json:"enabled"`
	NextDueAt     time.Time `json:"next_due_at"`
	LastAttemptAt time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`
	Receipt       c.Receipt `json:"receipt"`
}

// WatchManagementPort is an optional management seam over the existing
// watch owner. Listing is read-only; refresh leases are acquired only by
// RefreshAdapter.Prepare.
type WatchManagementPort interface {
	ListDue(context.Context, c.Envelope, int) ([]WatchManagementItem, error)
	Read(context.Context, c.Envelope, string) (WatchManagementItem, error)
	Disable(context.Context, c.Envelope, string) (WatchManagementItem, error)
}

type dueResearchWatchStore interface {
	DueResearchWatches(context.Context, string, string, time.Time, int) ([]l1sqlite.L1ResearchWatch, error)
}

type disableResearchWatchStore interface {
	DisableResearchWatch(context.Context, string, string, string) (bool, error)
}

var _ WatchManagementPort = (*WatchAdapter)(nil)

func (a *WatchAdapter) ListDue(ctx context.Context, env c.Envelope, limit int) ([]WatchManagementItem, error) {
	if a == nil || a.store == nil || ctx == nil {
		return nil, errors.New("unavailable: research watch management owner")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateWatchManagementEnvelope(env); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 20 {
		return nil, errors.New("invalid_request: due watch limit must be 1..20")
	}
	store, ok := a.store.(dueResearchWatchStore)
	if !ok {
		return nil, errors.New("unavailable: read-only due watch listing is unsupported")
	}
	owner, err := watchManagementOwnerID(env)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	rows, err := store.DueResearchWatches(ctx, env.DataScope, owner, now, limit)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(rows) > limit {
		return nil, errors.New("unavailable: due watch owner exceeded requested limit")
	}
	items := make([]WatchManagementItem, 0, len(rows))
	for _, row := range rows {
		if row.OwnerID != owner || row.Scope != env.DataScope || !row.Enabled || row.NextDueAt.After(now) || !validManagementWatchID(row.WatchID) || !validWatchDigest(row.PayloadDigest) {
			return nil, errors.New("unavailable: due watch owner returned an invalid row")
		}
		items = append(items, watchManagementItem(row, now))
	}
	return items, nil
}

func (a *WatchAdapter) Read(ctx context.Context, env c.Envelope, watchID string) (WatchManagementItem, error) {
	if a == nil || a.store == nil || ctx == nil {
		return WatchManagementItem{}, errors.New("unavailable: research watch management owner")
	}
	if err := ctx.Err(); err != nil {
		return WatchManagementItem{}, err
	}
	if err := validateWatchManagementEnvelope(env); err != nil {
		return WatchManagementItem{}, err
	}
	watchID, err := validateManagementWatchID(watchID)
	if err != nil {
		return WatchManagementItem{}, err
	}
	owner, err := watchManagementOwnerID(env)
	if err != nil {
		return WatchManagementItem{}, err
	}
	row, found, err := a.store.GetResearchWatch(ctx, env.DataScope, owner, watchID)
	if err != nil {
		return WatchManagementItem{}, err
	}
	if !found {
		return WatchManagementItem{}, errors.New("not_found: research watch")
	}
	if err := ctx.Err(); err != nil {
		return WatchManagementItem{}, err
	}
	if row.OwnerID != owner || row.Scope != env.DataScope || row.WatchID != watchID || !validWatchDigest(row.PayloadDigest) {
		return WatchManagementItem{}, errors.New("unavailable: exact watch owner readback failed")
	}
	return watchManagementItem(row, time.Now().UTC()), nil
}

func (a *WatchAdapter) Disable(ctx context.Context, env c.Envelope, watchID string) (WatchManagementItem, error) {
	if a == nil || a.store == nil || ctx == nil {
		return WatchManagementItem{}, errors.New("unavailable: research watch management owner")
	}
	if err := ctx.Err(); err != nil {
		return WatchManagementItem{}, err
	}
	if err := validateWatchManagementEnvelope(env); err != nil {
		return WatchManagementItem{}, err
	}
	watchID, err := validateManagementWatchID(watchID)
	if err != nil {
		return WatchManagementItem{}, err
	}
	store, ok := a.store.(disableResearchWatchStore)
	if !ok {
		return WatchManagementItem{}, errors.New("unavailable: watch disable is unsupported")
	}
	owner, err := watchManagementOwnerID(env)
	if err != nil {
		return WatchManagementItem{}, err
	}
	updated, err := store.DisableResearchWatch(ctx, env.DataScope, owner, watchID)
	if err != nil {
		return WatchManagementItem{}, err
	}
	if !updated {
		return WatchManagementItem{}, errors.New("not_found: research watch")
	}
	item, err := a.Read(ctx, env, watchID)
	if err != nil {
		return WatchManagementItem{}, err
	}
	if item.Enabled {
		return WatchManagementItem{}, errors.New("unavailable: watch disable was not verified")
	}
	return item, nil
}

func validateManagementWatchID(watchID string) (string, error) {
	if !validManagementWatchID(watchID) {
		return "", errors.New("invalid_request: watch id")
	}
	return watchID, nil
}

func validManagementWatchID(watchID string) bool {
	if strings.TrimSpace(watchID) != watchID || !strings.HasPrefix(watchID, "rw_") || len(watchID) < 8 || len(watchID) > 256 {
		return false
	}
	for _, r := range watchID {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func validWatchDigest(digest string) bool {
	if len(digest) != 64 || strings.ToLower(digest) != digest {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func validateWatchManagementEnvelope(env c.Envelope) error {
	if strings.TrimSpace(env.Actor) == "" || env.Actor != strings.TrimSpace(env.Actor) || strings.TrimSpace(env.PolicyRevision) == "" || env.PolicyRevision != strings.TrimSpace(env.PolicyRevision) {
		return errors.New("blocked_by_policy: trusted watch management principal is invalid")
	}
	if env.DataScope == "user" {
		if strings.TrimSpace(env.OwnerID) == "" || env.OwnerID != strings.TrimSpace(env.OwnerID) || env.Actor != env.OwnerID {
			return errors.New("blocked_by_policy: trusted watch owner is invalid")
		}
		return nil
	}
	if env.DataScope != "public" || env.OwnerID != "" {
		return errors.New("blocked_by_policy: trusted watch scope is invalid")
	}
	return nil
}

func watchManagementOwnerID(env c.Envelope) (string, error) {
	if err := validateWatchManagementEnvelope(env); err != nil {
		return "", err
	}
	if env.DataScope == "user" {
		return env.OwnerID, nil
	}
	return env.Actor, nil
}

func watchManagementItem(row l1sqlite.L1ResearchWatch, verifiedAt time.Time) WatchManagementItem {
	return WatchManagementItem{
		WatchID: row.WatchID, OwnerID: row.OwnerID, Scope: row.Scope, Enabled: row.Enabled,
		NextDueAt: row.NextDueAt, LastAttemptAt: row.LastAttemptAt, LastSuccessAt: row.LastSuccessAt,
		Receipt: c.Receipt{Owner: "core.source_registry", OwnerRoute: "research.watch", AuditRef: "research_watch:" + row.WatchID,
			CanonicalID: row.WatchID, Digest: row.PayloadDigest, State: "committed", Scope: row.Scope,
			CommittedAt: row.CreatedAt, VerifiedAt: verifiedAt},
	}
}

// ValidateFor checks that an owner response still matches the requested principal and scope.
func (item WatchManagementItem) ValidateFor(expectedOwner, expectedScope string) error {
	if !validManagementWatchID(item.WatchID) || item.OwnerID != expectedOwner || item.Scope != expectedScope || item.Receipt.Owner != "core.source_registry" || item.Receipt.OwnerRoute != "research.watch" || item.Receipt.AuditRef != "research_watch:"+item.WatchID || item.Receipt.CanonicalID != item.WatchID || !validWatchDigest(item.Receipt.Digest) || item.Receipt.State != "committed" || item.Receipt.Scope != expectedScope || item.Receipt.CommittedAt.IsZero() || item.Receipt.VerifiedAt.IsZero() {
		return fmt.Errorf("unavailable: invalid watch management receipt")
	}
	return nil
}
