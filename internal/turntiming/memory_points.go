package turntiming

import "errors"

var (
	ErrInvalidIdentity  = errors.New("turntiming: invalid canonical turn identity")
	ErrIdentityConflict = errors.New("turntiming: canonical turn identity conflicts with an existing slot")
)

// Compact point IDs for response-first v2 timing batches. These numeric values
// are the stable export vocabulary for the fixed RAM recorder.
const (
	PointV2Ingress uint16 = 1 + iota
	PointV2Accept
	PointV2Accepted
	PointV2AsyncStart
	PointV2ReadView
	PointV2PromptComposition
	PointV2Generation
	PointV2Commit
	PointV2Publish
	PointV2ForegroundComplete
	PointV2Error
)

const (
	ComponentV2ViewerHandler uint16 = 1 + iota
	ComponentV2ConversationRuntime
)

// FindOrBeginTurn atomically returns the existing slot for a canonical
// TurnID/TraceID pair or reserves and identifies one new slot. This is used
// when an owner receipt may be returned again after a retry; retries retain
// the original TurnID and must not consume another bounded batch slot.
func (b *BatchRecorder) FindOrBeginTurn(turnID, traceID string) (TurnRecorder, bool, error) {
	if b == nil {
		return TurnRecorder{}, false, ErrInvalidConfig
	}
	if !validCanonicalFixedID(turnID, "turn_") || !validCanonicalFixedID(traceID, "trc_") {
		return TurnRecorder{}, false, ErrInvalidIdentity
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.frozen {
		return TurnRecorder{}, false, ErrAlreadyFrozen
	}
	var requestedTurnID [41]byte
	var requestedTraceID [40]byte
	copy(requestedTurnID[:], turnID)
	copy(requestedTraceID[:], traceID)
	for index := uint32(0); index < b.turnCount; index++ {
		state := &b.turns[index]
		if !state.identitySet {
			continue
		}
		if state.turnID == requestedTurnID || state.traceID == requestedTraceID {
			if state.turnID != requestedTurnID || state.traceID != requestedTraceID {
				return TurnRecorder{}, false, ErrIdentityConflict
			}
			return TurnRecorder{batch: b, index: index}, false, nil
		}
	}
	if b.turnCount >= b.turnCapacity {
		return TurnRecorder{}, false, ErrTurnCapacity
	}
	index := b.turnCount
	state := &b.turns[index]
	state.turnID, state.traceID = requestedTurnID, requestedTraceID
	state.identitySet = true
	b.turnCount++
	return TurnRecorder{batch: b, index: index}, true, nil
}
