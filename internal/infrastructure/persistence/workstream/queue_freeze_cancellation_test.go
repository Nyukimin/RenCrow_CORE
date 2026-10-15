package workstream

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type queueFreezeCancellationStore interface {
	SaveQueueFreeze(context.Context, domainworkstream.QueueFreeze) error
	GetQueueFreeze(context.Context, string) (domainworkstream.QueueFreeze, bool, error)
	CancelQueueFreeze(context.Context, string, domainworkstream.QueueFreezeCancellation) (domainworkstream.QueueFreeze, error)
	ResolveQueueFreezeAndAcquireLease(context.Context, string, domainworkstream.QueueFreezeResolution, domainworkstream.ImplementationLease) (domainworkstream.QueueFreeze, domainworkstream.ImplementationLease, bool, error)
	AcquireImplementationLeaseIfUnfrozen(context.Context, domainworkstream.ImplementationLease) (bool, string, error)
	GetImplementationLease(context.Context, string) (domainworkstream.ImplementationLease, bool, error)
}

type queueFreezeCancellationHarness struct {
	store     queueFreezeCancellationStore
	close     func()
	reopen    func() queueFreezeCancellationStore
	lineCount func() int
}

func TestQueueFreezeCancellationPersistsAndReplaysExactly(t *testing.T) {
	for _, factory := range queueFreezeCancellationStoreFactories() {
		t.Run(factory.name, func(t *testing.T) {
			harness := factory.new(t)
			defer harness.close()
			ctx := context.Background()
			freeze := queueFreezeCancellationFixture()
			if err := harness.store.SaveQueueFreeze(ctx, freeze); err != nil {
				t.Fatalf("SaveQueueFreeze() error = %v", err)
			}
			otherFreeze := queueFreezeCancellationFixture()
			otherFreeze.FreezeID = "freeze-cancel-keep-active"
			otherFreeze.BlockedUnitID = "unit-blocked-keep-active"
			if err := harness.store.SaveQueueFreeze(ctx, otherFreeze); err != nil {
				t.Fatalf("SaveQueueFreeze(other) error = %v", err)
			}
			payload := queueFreezeCancellationPayload()
			missingEventIDs := append([]modulecore.EventID(nil), payload.ExpectedMissingTransitionEventIDs...)

			got, err := harness.store.CancelQueueFreeze(ctx, freeze.FreezeID, payload)
			if err != nil {
				t.Fatalf("CancelQueueFreeze() error = %v", err)
			}
			payload.ExpectedMissingTransitionEventIDs[0] = "evt_00000000-0000-7000-8000-000000000003"
			if !reflect.DeepEqual(got.ObservedMissingTransitionEventIDs, missingEventIDs) {
				t.Fatalf("persisted event IDs alias the request payload: got %v, want %v", got.ObservedMissingTransitionEventIDs, missingEventIDs)
			}
			payload.ExpectedMissingTransitionEventIDs = missingEventIDs
			assertQueueFreezeCancelled(t, got, freeze, payload)
			encoded, err := json.Marshal(got)
			if err != nil || !strings.Contains(string(encoded), `"resolution_disposition":"cancelled"`) || !strings.Contains(string(encoded), `"observed_missing_transition_event_ids"`) {
				t.Fatalf("cancelled freeze JSON = %s, err = %v", encoded, err)
			}
			if _, found, err := harness.store.GetImplementationLease(ctx, "atlas_implementation"); err != nil || found {
				t.Fatalf("cancellation acquired an implementation lease: found=%v err=%v", found, err)
			}
			otherAfterCancellation, found, err := harness.store.GetQueueFreeze(ctx, otherFreeze.FreezeID)
			if err != nil || !found || !reflect.DeepEqual(otherAfterCancellation, otherFreeze) {
				t.Fatalf("other freeze changed: got=%+v found=%v err=%v want=%+v", otherAfterCancellation, found, err, otherFreeze)
			}
			linesAfterFirstWrite := harness.lineCount()

			replayed, err := harness.store.CancelQueueFreeze(ctx, freeze.FreezeID, payload)
			if err != nil || !reflect.DeepEqual(replayed, got) {
				t.Fatalf("exact replay = %+v, err = %v; want %+v", replayed, err, got)
			}
			if gotLines := harness.lineCount(); gotLines != linesAfterFirstWrite {
				t.Fatalf("exact replay changed freeze record count: got %d, want %d", gotLines, linesAfterFirstWrite)
			}
			reusedAction := payload
			reusedAction.BlockedUnitID = otherFreeze.BlockedUnitID
			reusedAction.BlockedRevision = otherFreeze.BlockedRevision
			reusedAction.ExpectedFreezeRevision = otherFreeze.FreezeRevision
			if _, err := harness.store.CancelQueueFreeze(ctx, otherFreeze.FreezeID, reusedAction); !errors.Is(err, domainworkstream.ErrQueueFreezeResolutionConflict) {
				t.Fatalf("reuse of cancellation action across freezes error = %v", err)
			}

			conflicting := payload
			conflicting.ResolutionPayloadHash = "different-payload-hash"
			if _, err := harness.store.CancelQueueFreeze(ctx, freeze.FreezeID, conflicting); !errors.Is(err, domainworkstream.ErrQueueFreezeResolutionConflict) {
				t.Fatalf("same action with different hash error = %v", err)
			}
			conflicting = payload
			conflicting.Reason = "different cancellation reason"
			if _, err := harness.store.CancelQueueFreeze(ctx, freeze.FreezeID, conflicting); !errors.Is(err, domainworkstream.ErrQueueFreezeResolutionConflict) {
				t.Fatalf("same action and hash with different reason error = %v", err)
			}
			conflicting = payload
			conflicting.ActionID = modulecore.ActionID("act_00000000-0000-5000-8000-000000000002")
			if _, err := harness.store.CancelQueueFreeze(ctx, freeze.FreezeID, conflicting); !errors.Is(err, domainworkstream.ErrQueueFreezeResolutionConflict) {
				t.Fatalf("different action with same hash error = %v", err)
			}
			conflicting = payload
			conflicting.ExpectedMissingTransitionEventIDs = []modulecore.EventID{"evt_00000000-0000-7000-8000-000000000003"}
			if _, err := harness.store.CancelQueueFreeze(ctx, freeze.FreezeID, conflicting); !errors.Is(err, domainworkstream.ErrQueueFreezeResolutionConflict) {
				t.Fatalf("same action and hash with a different missing-event set error = %v", err)
			}

			harness.close()
			harness.store = harness.reopen()
			persisted, found, err := harness.store.GetQueueFreeze(ctx, freeze.FreezeID)
			if err != nil || !found {
				t.Fatalf("GetQueueFreeze() after reopen found=%v err=%v", found, err)
			}
			assertQueueFreezeCancelled(t, persisted, freeze, payload)
			replayed, err = harness.store.CancelQueueFreeze(ctx, freeze.FreezeID, payload)
			if err != nil || !reflect.DeepEqual(replayed, persisted) {
				t.Fatalf("restart replay = %+v, err = %v; want %+v", replayed, err, persisted)
			}
		})
	}
}

func TestValidateQueueFreezeCancellationRequiresSortedUniqueCanonicalMissingEventIDs(t *testing.T) {
	cases := []struct {
		name  string
		ids   []modulecore.EventID
		valid bool
	}{
		{name: "empty", ids: nil, valid: true},
		{name: "sorted canonical", ids: queueFreezeCancellationEventIDs(), valid: true},
		{name: "unsorted", ids: []modulecore.EventID{queueFreezeCancellationEventIDs()[1], queueFreezeCancellationEventIDs()[0]}},
		{name: "duplicate", ids: []modulecore.EventID{queueFreezeCancellationEventIDs()[0], queueFreezeCancellationEventIDs()[0]}},
		{name: "invalid canonical id", ids: []modulecore.EventID{"evt_bad"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			payload := queueFreezeCancellationPayload()
			payload.ExpectedMissingTransitionEventIDs = testCase.ids
			err := domainworkstream.ValidateQueueFreezeCancellation(payload)
			if testCase.valid && err != nil {
				t.Fatalf("ValidateQueueFreezeCancellation() error = %v", err)
			}
			if !testCase.valid && err == nil {
				t.Fatal("ValidateQueueFreezeCancellation() accepted invalid event IDs")
			}
		})
	}
}

func TestQueueFreezeCancellationRejectsPendingActionIDReuseAcrossFreezes(t *testing.T) {
	for _, factory := range queueFreezeCancellationStoreFactories() {
		t.Run(factory.name, func(t *testing.T) {
			harness := factory.new(t)
			defer harness.close()
			ctx := context.Background()
			target := queueFreezeCancellationFixture()
			other := queueFreezeCancellationFixture()
			other.FreezeID = "freeze-cancel-pending-action"
			other.BlockedUnitID = "unit-blocked-pending-action"
			other.ActionID = queueFreezeCancellationPayload().ActionID
			other.ResolutionPayloadHash = "pending-payload"
			if err := harness.store.SaveQueueFreeze(ctx, target); err != nil {
				t.Fatalf("SaveQueueFreeze(target) error = %v", err)
			}
			if err := harness.store.SaveQueueFreeze(ctx, other); err != nil {
				t.Fatalf("SaveQueueFreeze(other pending) error = %v", err)
			}
			if _, err := harness.store.CancelQueueFreeze(ctx, target.FreezeID, queueFreezeCancellationPayload()); !errors.Is(err, domainworkstream.ErrQueueFreezeResolutionConflict) {
				t.Fatalf("CancelQueueFreeze() with another freeze's pending action error = %v", err)
			}
		})
	}
}

func TestQueueFreezeActionIDCannotBeReusedAcrossCancellationAndReplacement(t *testing.T) {
	for _, factory := range queueFreezeCancellationStoreFactories() {
		for _, order := range []string{"cancel-then-replacement", "replacement-then-cancel"} {
			t.Run(factory.name+"/"+order, func(t *testing.T) {
				harness := factory.new(t)
				defer harness.close()
				ctx := context.Background()
				first := queueFreezeCancellationFixture()
				first.FreezeID = "freeze-action-first-" + order
				first.BlockedUnitID = "unit-blocked-first-" + order
				second := queueFreezeCancellationFixture()
				second.FreezeID = "freeze-action-second-" + order
				second.BlockedUnitID = "unit-blocked-second-" + order
				for _, freeze := range []domainworkstream.QueueFreeze{first, second} {
					if err := harness.store.SaveQueueFreeze(ctx, freeze); err != nil {
						t.Fatalf("SaveQueueFreeze(%s) error = %v", freeze.FreezeID, err)
					}
				}
				actionID := queueFreezeCancellationPayload().ActionID

				if order == "cancel-then-replacement" {
					if _, err := harness.store.CancelQueueFreeze(ctx, first.FreezeID, queueFreezeCancellationFor(first, actionID)); err != nil {
						t.Fatalf("CancelQueueFreeze(first) error = %v", err)
					}
					resolution, replacement := queueFreezeReplacementFor(second, actionID)
					before, found, err := harness.store.GetQueueFreeze(ctx, second.FreezeID)
					if err != nil || !found {
						t.Fatalf("GetQueueFreeze(second) before replacement found=%v err=%v", found, err)
					}
					if _, _, acquired, err := harness.store.ResolveQueueFreezeAndAcquireLease(ctx, second.FreezeID, resolution, replacement); !errors.Is(err, domainworkstream.ErrQueueFreezeResolutionConflict) || acquired {
						t.Fatalf("replacement reused cancellation action: acquired=%v err=%v", acquired, err)
					}
					after, found, err := harness.store.GetQueueFreeze(ctx, second.FreezeID)
					if err != nil || !found || !reflect.DeepEqual(after, before) {
						t.Fatalf("rejected replacement changed second freeze: after=%+v found=%v err=%v before=%+v", after, found, err, before)
					}
					if _, found, err := harness.store.GetImplementationLease(ctx, replacement.LeaseName); err != nil || found {
						t.Fatalf("rejected replacement acquired lease: found=%v err=%v", found, err)
					}
					return
				}

				resolution, replacement := queueFreezeReplacementFor(first, actionID)
				if _, _, acquired, err := harness.store.ResolveQueueFreezeAndAcquireLease(ctx, first.FreezeID, resolution, replacement); err != nil || !acquired {
					t.Fatalf("ResolveQueueFreezeAndAcquireLease(first) acquired=%v err=%v", acquired, err)
				}
				before, found, err := harness.store.GetQueueFreeze(ctx, second.FreezeID)
				if err != nil || !found {
					t.Fatalf("GetQueueFreeze(second) before cancellation found=%v err=%v", found, err)
				}
				if _, err := harness.store.CancelQueueFreeze(ctx, second.FreezeID, queueFreezeCancellationFor(second, actionID)); !errors.Is(err, domainworkstream.ErrQueueFreezeResolutionConflict) {
					t.Fatalf("cancellation reused replacement action error = %v", err)
				}
				after, found, err := harness.store.GetQueueFreeze(ctx, second.FreezeID)
				if err != nil || !found || !reflect.DeepEqual(after, before) {
					t.Fatalf("rejected cancellation changed second freeze: after=%+v found=%v err=%v before=%+v", after, found, err, before)
				}
			})
		}
	}
}

func TestQueueFreezeCancellationExactReplaySurvivesLaterDuplicateActionID(t *testing.T) {
	for _, factory := range queueFreezeCancellationStoreFactories() {
		t.Run(factory.name, func(t *testing.T) {
			harness := factory.new(t)
			defer harness.close()
			ctx := context.Background()
			freeze := queueFreezeCancellationFixture()
			payload := queueFreezeCancellationPayload()
			if err := harness.store.SaveQueueFreeze(ctx, freeze); err != nil {
				t.Fatalf("SaveQueueFreeze(target) error = %v", err)
			}
			cancelled, err := harness.store.CancelQueueFreeze(ctx, freeze.FreezeID, payload)
			if err != nil {
				t.Fatalf("CancelQueueFreeze(target) error = %v", err)
			}
			duplicate := queueFreezeCancellationFixture()
			duplicate.FreezeID = "freeze-later-duplicate-action"
			duplicate.BlockedUnitID = "unit-later-duplicate-action"
			duplicate.ActionID = payload.ActionID
			duplicate.ResolutionPayloadHash = "later-duplicate-payload"
			if err := harness.store.SaveQueueFreeze(ctx, duplicate); err != nil {
				t.Fatalf("SaveQueueFreeze(later duplicate) error = %v", err)
			}

			replayed, err := harness.store.CancelQueueFreeze(ctx, freeze.FreezeID, payload)
			if err != nil || !reflect.DeepEqual(replayed, cancelled) {
				t.Fatalf("exact cancellation replay after later duplicate = %+v, err = %v; want %+v", replayed, err, cancelled)
			}
		})
	}
}

func queueFreezeCancellationFor(freeze domainworkstream.QueueFreeze, actionID modulecore.ActionID) domainworkstream.QueueFreezeCancellation {
	payload := queueFreezeCancellationPayload()
	payload.ActionID = actionID
	payload.ExpectedFreezeRevision = freeze.FreezeRevision
	payload.BlockedUnitID = freeze.BlockedUnitID
	payload.BlockedRevision = freeze.BlockedRevision
	return payload
}

func queueFreezeReplacementFor(freeze domainworkstream.QueueFreeze, actionID modulecore.ActionID) (domainworkstream.QueueFreezeResolution, domainworkstream.ImplementationLease) {
	replacement := domainworkstream.ImplementationLease{
		LeaseName: "atlas_implementation", HolderUnitID: "unit-replacement-" + freeze.FreezeID,
		HolderWorkstreamID: "workstream-replacement-" + freeze.FreezeID, Stage: "QUEUED", Revision: "1",
		AcquiredAt: freeze.CreatedAt, HeartbeatAt: freeze.CreatedAt,
	}
	resolution := domainworkstream.QueueFreezeResolution{
		ExpectedFreezeRevision: freeze.FreezeRevision,
		ActionID:               actionID,
		ReplacementUnitID:      replacement.HolderUnitID,
		SupersedesUnitID:       freeze.BlockedUnitID,
		BlockerResolutionRefs:  []domainbacklog.EvidenceRef{{Kind: "fix", Ref: "fix-proof-" + freeze.FreezeID, Verified: true, VerificationResult: domainbacklog.EvidenceVerificationVerified}},
		ResolutionPayloadHash:  "replacement-payload-" + freeze.FreezeID,
	}
	return resolution, replacement
}

func TestQueueFreezeCancellationRejectsUnsafeRequestsAndStates(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(context.Context, queueFreezeCancellationStore, domainworkstream.QueueFreeze) error
		mutate  func(*domainworkstream.QueueFreezeCancellation)
		wantErr error
	}{
		{
			name: "stale freeze revision",
			mutate: func(payload *domainworkstream.QueueFreezeCancellation) {
				payload.ExpectedFreezeRevision--
			},
			wantErr: domainworkstream.ErrQueueFreezeRevisionConflict,
		},
		{
			name: "different blocked unit",
			mutate: func(payload *domainworkstream.QueueFreezeCancellation) {
				payload.BlockedUnitID = "other-blocked-unit"
			},
			wantErr: domainworkstream.ErrQueueFreezeResolutionConflict,
		},
		{
			name: "different blocked revision",
			mutate: func(payload *domainworkstream.QueueFreezeCancellation) {
				payload.BlockedRevision++
			},
			wantErr: domainworkstream.ErrQueueFreezeResolutionConflict,
		},
		{
			name: "empty reason",
			mutate: func(payload *domainworkstream.QueueFreezeCancellation) {
				payload.Reason = "  "
			},
		},
		{
			name: "pending replacement resolution",
			prepare: func(ctx context.Context, store queueFreezeCancellationStore, freeze domainworkstream.QueueFreeze) error {
				freeze.ActionID = modulecore.ActionID("act_00000000-0000-5000-8000-000000000009")
				freeze.ReplacementUnitID = "replacement-pending"
				freeze.ResolutionPayloadHash = "pending-resolution-hash"
				return store.SaveQueueFreeze(ctx, freeze)
			},
			wantErr: domainworkstream.ErrQueueFreezeResolutionConflict,
		},
		{
			name: "held implementation lease",
			prepare: func(ctx context.Context, store queueFreezeCancellationStore, freeze domainworkstream.QueueFreeze) error {
				lease := domainworkstream.ImplementationLease{
					LeaseName: "other_held_lease", HolderUnitID: "unit-with-lease",
					HolderWorkstreamID: "workstream-with-lease", AcquiredAt: freeze.CreatedAt, HeartbeatAt: freeze.CreatedAt,
				}
				acquired, reason, err := store.AcquireImplementationLeaseIfUnfrozen(ctx, lease)
				if err != nil || !acquired || reason != "" {
					return errors.New("could not prepare held implementation lease")
				}
				return store.SaveQueueFreeze(ctx, freeze)
			},
			wantErr: domainworkstream.ErrImplementationLeaseHeld,
		},
		{
			name: "already resolved by replacement",
			prepare: func(ctx context.Context, store queueFreezeCancellationStore, freeze domainworkstream.QueueFreeze) error {
				freeze.Status = domainworkstream.QueueFreezeResolved
				freeze.ActionID = modulecore.ActionID("act_00000000-0000-5000-8000-000000000010")
				freeze.ReplacementUnitID = "replacement-already-resolved"
				freeze.ReplacementLease = domainworkstream.ImplementationLease{LeaseName: "atlas_implementation", HolderUnitID: freeze.ReplacementUnitID}
				freeze.ResolutionAcquired = true
				freeze.ResolutionPayloadHash = "replacement-resolution-hash"
				return store.SaveQueueFreeze(ctx, freeze)
			},
			wantErr: domainworkstream.ErrQueueFreezeResolutionConflict,
		},
		{
			name: "unknown status",
			prepare: func(ctx context.Context, store queueFreezeCancellationStore, freeze domainworkstream.QueueFreeze) error {
				freeze.Status = "unknown"
				return store.SaveQueueFreeze(ctx, freeze)
			},
			wantErr: domainworkstream.ErrQueueFreezeResolutionConflict,
		},
	}

	for _, factory := range queueFreezeCancellationStoreFactories() {
		for _, testCase := range cases {
			t.Run(factory.name+"/"+testCase.name, func(t *testing.T) {
				harness := factory.new(t)
				defer harness.close()
				ctx := context.Background()
				freeze := queueFreezeCancellationFixture()
				if testCase.prepare != nil {
					if err := testCase.prepare(ctx, harness.store, freeze); err != nil {
						t.Fatalf("prepare state: %v", err)
					}
				} else if err := harness.store.SaveQueueFreeze(ctx, freeze); err != nil {
					t.Fatalf("SaveQueueFreeze() error = %v", err)
				}
				payload := queueFreezeCancellationPayload()
				if testCase.mutate != nil {
					testCase.mutate(&payload)
				}
				before, found, err := harness.store.GetQueueFreeze(ctx, freeze.FreezeID)
				if err != nil || !found {
					t.Fatalf("GetQueueFreeze() before cancellation found=%v err=%v", found, err)
				}

				if _, err := harness.store.CancelQueueFreeze(ctx, freeze.FreezeID, payload); testCase.wantErr != nil && !errors.Is(err, testCase.wantErr) {
					t.Fatalf("CancelQueueFreeze() error = %v, want %v", err, testCase.wantErr)
				} else if testCase.wantErr == nil && err == nil {
					t.Fatal("CancelQueueFreeze() accepted a request that should be rejected")
				}
				current, found, err := harness.store.GetQueueFreeze(ctx, freeze.FreezeID)
				if err != nil || !found {
					t.Fatalf("GetQueueFreeze() found=%v err=%v", found, err)
				}
				if !reflect.DeepEqual(current, before) {
					t.Fatalf("rejected cancellation changed freeze: before=%+v after=%+v", before, current)
				}
			})
		}
	}
}

type queueFreezeCancellationStoreFactory struct {
	name string
	new  func(*testing.T) *queueFreezeCancellationHarness
}

func queueFreezeCancellationStoreFactories() []queueFreezeCancellationStoreFactory {
	return []queueFreezeCancellationStoreFactory{
		{
			name: "jsonl",
			new: func(t *testing.T) *queueFreezeCancellationHarness {
				root := t.TempDir()
				return &queueFreezeCancellationHarness{
					store: NewJSONLStore(root), close: func() {},
					reopen: func() queueFreezeCancellationStore { return NewJSONLStore(root) },
					lineCount: func() int {
						payload, err := os.ReadFile(filepath.Join(root, "queue_freeze.jsonl"))
						if err != nil {
							t.Fatalf("read JSONL freeze records: %v", err)
						}
						return len(strings.Split(strings.TrimSpace(string(payload)), "\n"))
					},
				}
			},
		},
		{
			name: "sqlite",
			new: func(t *testing.T) *queueFreezeCancellationHarness {
				path := filepath.Join(t.TempDir(), "workstream.db")
				store, err := NewSQLiteStore(path)
				if err != nil {
					t.Fatalf("NewSQLiteStore() error = %v", err)
				}
				closeStore := func() {
					if err := store.Close(); err != nil {
						t.Errorf("SQLiteStore.Close() error = %v", err)
					}
				}
				return &queueFreezeCancellationHarness{
					store: store, close: closeStore,
					reopen: func() queueFreezeCancellationStore {
						reopened, err := NewSQLiteStore(path)
						if err != nil {
							t.Fatalf("NewSQLiteStore() after reopen error = %v", err)
						}
						store = reopened
						return reopened
					},
					lineCount: func() int { return -1 },
				}
			},
		},
	}
}

func queueFreezeCancellationFixture() domainworkstream.QueueFreeze {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	return domainworkstream.QueueFreeze{
		FreezeID: "freeze-cancel", BlockedUnitID: "unit-blocked", BlockedRevision: 3, FreezeRevision: 5,
		ReasonCode: "stage_failed", InvalidatedFromStage: "BUILD",
		EvidenceRefs: []domainbacklog.EvidenceRef{{Kind: "history", Ref: "blocked-evidence"}},
		Status:       domainworkstream.QueueFreezeActive, CreatedAt: now, UpdatedAt: now,
	}
}

func queueFreezeCancellationPayload() domainworkstream.QueueFreezeCancellation {
	return domainworkstream.QueueFreezeCancellation{
		ActionID:               modulecore.ActionID("act_00000000-0000-5000-8000-000000000001"),
		ExpectedFreezeRevision: 5, BlockedUnitID: "unit-blocked", BlockedRevision: 3,
		Reason: "retry purpose withdrawn", ResolutionPayloadHash: "cancel-payload-hash",
		ExpectedMissingTransitionEventIDs: queueFreezeCancellationEventIDs(),
	}
}

func queueFreezeCancellationEventIDs() []modulecore.EventID {
	return []modulecore.EventID{
		"evt_00000000-0000-7000-8000-000000000001",
		"evt_00000000-0000-7000-8000-000000000002",
	}
}

func assertQueueFreezeCancelled(t *testing.T, got, original domainworkstream.QueueFreeze, payload domainworkstream.QueueFreezeCancellation) {
	t.Helper()
	if got.Status != domainworkstream.QueueFreezeResolved || got.ResolutionDisposition != domainworkstream.QueueFreezeDispositionCancelled || got.CancellationReason != payload.Reason {
		t.Fatalf("cancelled freeze disposition = status %q, disposition %q, reason %q", got.Status, got.ResolutionDisposition, got.CancellationReason)
	}
	if got.ActionID != payload.ActionID || got.ResolutionPayloadHash != payload.ResolutionPayloadHash || got.ResolutionAcquired {
		t.Fatalf("cancelled freeze action/hash/acquired = %q/%q/%v", got.ActionID, got.ResolutionPayloadHash, got.ResolutionAcquired)
	}
	if !reflect.DeepEqual(got.ObservedMissingTransitionEventIDs, payload.ExpectedMissingTransitionEventIDs) {
		t.Fatalf("cancelled freeze missing transition event IDs = %v, want %v", got.ObservedMissingTransitionEventIDs, payload.ExpectedMissingTransitionEventIDs)
	}
	if got.ReplacementUnitID != "" || got.ReplacementLease != (domainworkstream.ImplementationLease{}) || got.SupersedesUnitID != "" || len(got.BlockerResolutionRefs) != 0 {
		t.Fatalf("cancelled freeze retained replacement data: %+v", got)
	}
	if got.FreezeID != original.FreezeID || got.BlockedUnitID != original.BlockedUnitID || got.BlockedRevision != original.BlockedRevision || got.FreezeRevision != original.FreezeRevision || got.ReasonCode != original.ReasonCode || got.InvalidatedFromStage != original.InvalidatedFromStage || !reflect.DeepEqual(got.EvidenceRefs, original.EvidenceRefs) || !got.CreatedAt.Equal(original.CreatedAt) {
		t.Fatalf("cancellation changed original freeze evidence or identity: %+v", got)
	}
	if got.ResolvedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("cancelled freeze timestamps are incomplete: %+v", got)
	}
}
