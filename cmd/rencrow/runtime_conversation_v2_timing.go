package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
)

const (
	responseFirstTimingEnvironment    = "RENCROW_RESPONSE_FIRST_TIMING"
	responseFirstTimingEndpoint       = "/debug/response-first/timing"
	responseFirstTimingExportSchema   = 1
	responseFirstTimingExportMaxBytes = 16 << 20
	responseFirstTimingStopTimeout    = 12 * time.Second
)

type responseFirstV2Processing struct {
	lifecycle           *viewer.V2ProcessingLifecycle
	timing              *turntiming.BatchRecorder
	backgroundDrainStop func(context.Context) error
	timingControlMu     sync.Mutex
	timingStopDone      bool
	timingStopErr       error
}

// bindAcceptedReportTiming attaches one bounded batch slot to the canonical
// IDs returned by EventStore. A retried report receipt finds that same slot,
// even when the retry's newly issued envelope IDs were discarded.
func (r *responseFirstConversationRuntime) bindAcceptedReportTiming(ctx context.Context, accepted conversationruntime.AcceptedTurn) context.Context {
	if ctx == nil || r == nil || r.processing == nil || r.processing.timing == nil {
		return ctx
	}
	turn, created, err := r.processing.timing.FindOrBeginTurn(string(accepted.TurnID), string(accepted.InputEvent.TraceID))
	if err != nil {
		log.Printf("response-first report timing unavailable turn_id=%s reason=%s", accepted.TurnID, responseFirstTimingBindReason(err))
		return ctx
	}
	if created {
		turn.Mark(turntiming.PointV2Accepted, turntiming.ComponentV2ConversationRuntime, 1, 0)
	}
	return turntiming.WithBatchTurnRecorder(ctx, turn)
}

func responseFirstTimingBindReason(err error) string {
	switch {
	case errors.Is(err, turntiming.ErrTurnCapacity):
		return "turn_capacity"
	case errors.Is(err, turntiming.ErrAlreadyFrozen):
		return "batch_frozen"
	case errors.Is(err, turntiming.ErrIdentityConflict):
		return "identity_conflict"
	case errors.Is(err, turntiming.ErrInvalidIdentity):
		return "invalid_identity"
	default:
		return "recorder_unavailable"
	}
}

// processAcceptedReport records the task-drain processing envelope. The
// conversation application adds its own readview, prompt, generation, commit,
// and publish spans through the same context-carried turn handle.
func (r *responseFirstConversationRuntime) processAcceptedReport(ctx context.Context, accepted viewer.V2AcceptedTurn) error {
	turn, timingEnabled := turntiming.BatchTurnFromContext(ctx)
	var span turntiming.Span
	var spanActive bool
	if timingEnabled {
		span, spanActive = turn.Begin(turntiming.PointV2AsyncStart, turntiming.ComponentV2ConversationRuntime, 1, 0, turntiming.Span{})
	}
	err := r.Process(ctx, accepted)
	if timingEnabled {
		if spanActive {
			turn.End(span, responseFirstTimingResult(err))
		}
		if err != nil {
			turn.MarkResult(turntiming.PointV2Error, turntiming.ComponentV2ConversationRuntime, 1, uint32(turntiming.PointV2AsyncStart), responseFirstTimingResult(err))
		}
		turn.Mark(turntiming.PointV2ForegroundComplete, turntiming.ComponentV2ConversationRuntime, 1, 0)
		turn.FinishForeground()
	}
	return err
}

func responseFirstTimingResult(err error) turntiming.Result {
	switch {
	case err == nil:
		return turntiming.ResultOK
	case errors.Is(err, context.Canceled):
		return turntiming.ResultCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return turntiming.ResultTimeout
	default:
		return turntiming.ResultError
	}
}

func newResponseFirstV2ProcessingFromEnvironment() (*responseFirstV2Processing, error) {
	processing := &responseFirstV2Processing{
		lifecycle: viewer.NewV2ProcessingLifecycle(context.Background()),
	}
	if os.Getenv(responseFirstTimingEnvironment) != "1" {
		return processing, nil
	}
	batch, err := turntiming.NewBatchRecorder()
	if err != nil {
		return nil, err
	}
	processing.timing = batch
	return processing, nil
}

func (p *responseFirstV2Processing) freezeTimingBatch(incomplete bool) error {
	if p == nil || p.timing == nil {
		return nil
	}
	if incomplete {
		return p.timing.FreezeIncomplete()
	}
	return p.timing.Freeze()
}

func (r *responseFirstConversationRuntime) freezeTimingBatch(incomplete bool) error {
	if r == nil || r.processing == nil {
		return turntiming.ErrInvalidConfig
	}
	return r.processing.freezeTimingBatch(incomplete)
}

// timingSnapshot exposes only a frozen post-batch view. Live recorder arrays
// remain private and writable until the registered work drain completes.
func (r *responseFirstConversationRuntime) timingSnapshot() (turntiming.BatchSnapshot, error) {
	if r == nil || r.processing == nil || r.processing.timing == nil {
		return turntiming.BatchSnapshot{}, turntiming.ErrInvalidConfig
	}
	return r.processing.timing.Snapshot()
}

func (p *responseFirstV2Processing) stopRegisteredWork(ctx context.Context) error {
	if p == nil {
		return nil
	}
	var stopErr error
	if p.lifecycle != nil {
		if err := p.lifecycle.Shutdown(ctx); err != nil {
			stopErr = errors.Join(stopErr, err)
		}
	}
	if p.backgroundDrainStop != nil {
		if err := p.backgroundDrainStop(ctx); err != nil {
			stopErr = errors.Join(stopErr, err)
		}
	}
	return stopErr
}

// closeTimingAdmissionAndWait stops new Viewer ingress and waits for admitted
// foreground processing without canceling its process-owned context. Durable
// background lanes are intentionally left for process shutdown; until their
// work is registered against child handles, their presence makes this batch
// incomplete.
func (p *responseFirstV2Processing) closeTimingAdmissionAndWait(ctx context.Context) error {
	if p == nil {
		return nil
	}
	var waitErr error
	if p.lifecycle != nil {
		p.lifecycle.CloseAdmission()
		waitErr = p.lifecycle.Wait(ctx)
	}
	if p.timing != nil {
		p.timing.CloseAdmission()
		if p.backgroundDrainStop != nil {
			p.timing.MarkWorkTrackingIncomplete()
		}
	}
	return waitErr
}

func (r *responseFirstConversationRuntime) stopTimingBatch() (turntiming.BatchSnapshot, error) {
	return r.stopTimingBatchWithTimeout(responseFirstTimingStopTimeout)
}

func (r *responseFirstConversationRuntime) stopTimingBatchWithTimeout(timeout time.Duration) (turntiming.BatchSnapshot, error) {
	if r == nil || r.processing == nil || r.processing.timing == nil || timeout <= 0 {
		return turntiming.BatchSnapshot{}, turntiming.ErrInvalidConfig
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return r.stopTimingBatchWithContext(ctx)
}

func (r *responseFirstConversationRuntime) stopTimingBatchWithContext(ctx context.Context) (turntiming.BatchSnapshot, error) {
	if r == nil || r.processing == nil || r.processing.timing == nil {
		return turntiming.BatchSnapshot{}, turntiming.ErrInvalidConfig
	}
	p := r.processing
	p.timingControlMu.Lock()
	defer p.timingControlMu.Unlock()
	if p.timingStopDone {
		snapshot, err := p.timing.Snapshot()
		return snapshot, errors.Join(p.timingStopErr, err)
	}
	if snapshot, err := p.timing.Snapshot(); err == nil {
		p.timingStopDone = true
		return snapshot, nil
	} else if !errors.Is(err, turntiming.ErrNotFrozen) {
		return turntiming.BatchSnapshot{}, err
	}

	stopErr := p.closeTimingAdmissionAndWait(ctx)
	if stopErr == nil {
		stopErr = p.timing.WaitDrained(ctx)
	}
	if stopErr == nil {
		stopErr = p.timing.Freeze()
	}
	if stopErr != nil {
		freezeErr := p.timing.FreezeIncomplete()
		if freezeErr != nil && !errors.Is(freezeErr, turntiming.ErrAlreadyFrozen) {
			stopErr = errors.Join(stopErr, freezeErr)
		}
	}
	snapshot, snapshotErr := p.timing.Snapshot()
	if snapshotErr != nil {
		return turntiming.BatchSnapshot{}, errors.Join(stopErr, snapshotErr)
	}
	p.timingStopDone = true
	p.timingStopErr = stopErr
	return snapshot, stopErr
}

func (r *responseFirstConversationRuntime) stopV2Foreground() {
	if r == nil || r.processing == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), responseFirstTimingStopTimeout)
	defer cancel()
	stopErr := r.processing.stopRegisteredWork(ctx)
	if r.processing.timing == nil {
		if stopErr != nil {
			log.Printf("response-first Viewer registered work drain incomplete error_type=%T", stopErr)
		}
		return
	}
	snapshot, err := r.stopTimingBatchWithContext(ctx)
	err = errors.Join(stopErr, err)
	if err != nil || snapshot.IsIncomplete() {
		log.Printf("response-first Viewer timing batch freeze incomplete error_type=%T", err)
	}
}

type responseFirstTimingStatus struct {
	State             string `json:"state"`
	Complete          bool   `json:"complete"`
	CollectionStarted bool   `json:"collection_started"`
	RestartRequired   bool   `json:"restart_required"`
	TurnCount         uint32 `json:"turn_count"`
	ReservedBytes     uint64 `json:"reserved_bytes"`
	ExportMaxBytes    int    `json:"export_max_bytes"`
}

func registerResponseFirstTimingRoutes(mux *http.ServeMux, runtime *responseFirstConversationRuntime) {
	if mux == nil || runtime == nil || runtime.processing == nil || runtime.processing.timing == nil || os.Getenv(responseFirstTimingEnvironment) != "1" {
		return
	}
	mux.HandleFunc(responseFirstTimingEndpoint, localOnlyHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeResponseFirstTimingStatus(w, runtime, false, nil, nil)
	}))
	mux.HandleFunc(responseFirstTimingEndpoint+"/start", localOnlyHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		state, _, err := responseFirstTimingBatchState(runtime)
		if err != nil {
			http.Error(w, "timing batch unavailable", http.StatusServiceUnavailable)
			return
		}
		if state != "collecting" {
			http.Error(w, "timing batch is already frozen; restart CORE to begin another batch", http.StatusConflict)
			return
		}
		writeResponseFirstTimingStatus(w, runtime, false, nil, nil)
	}))
	mux.HandleFunc(responseFirstTimingEndpoint+"/stop", localOnlyHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		snapshot, err := runtime.stopTimingBatch()
		if snapshot.TurnCount() == 0 && err != nil && !snapshot.IsIncomplete() {
			http.Error(w, "timing batch stop failed", http.StatusServiceUnavailable)
			return
		}
		writeResponseFirstTimingStatus(w, runtime, true, &snapshot, err)
	}))
	mux.HandleFunc(responseFirstTimingEndpoint+"/export", localOnlyHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		snapshot, err := runtime.timingSnapshot()
		if errors.Is(err, turntiming.ErrNotFrozen) {
			http.Error(w, "timing batch is still collecting", http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, "timing batch unavailable", http.StatusServiceUnavailable)
			return
		}
		body, err := encodeResponseFirstTimingExport(snapshot, responseFirstTimingExportMaxBytes)
		if errors.Is(err, errResponseFirstTimingExportTooLarge) {
			http.Error(w, "timing export exceeds the bounded response size", http.StatusRequestEntityTooLarge)
			return
		}
		if err != nil {
			http.Error(w, "timing export unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="response-first-timing.json"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
}

func responseFirstTimingBatchState(runtime *responseFirstConversationRuntime) (string, turntiming.BatchSnapshot, error) {
	snapshot, err := runtime.timingSnapshot()
	if errors.Is(err, turntiming.ErrNotFrozen) {
		return "collecting", turntiming.BatchSnapshot{}, nil
	}
	if err != nil {
		return "unavailable", turntiming.BatchSnapshot{}, err
	}
	complete, _, _ := responseFirstTimingCaptureQuality(snapshot)
	if !complete {
		return "incomplete", snapshot, nil
	}
	return "frozen", snapshot, nil
}

func writeResponseFirstTimingStatus(w http.ResponseWriter, runtime *responseFirstConversationRuntime, restartRequired bool, knownSnapshot *turntiming.BatchSnapshot, stopErr error) {
	state := "collecting"
	snapshot := turntiming.BatchSnapshot{}
	if knownSnapshot != nil {
		snapshot = *knownSnapshot
		complete, _, _ := responseFirstTimingCaptureQuality(snapshot)
		state = "frozen"
		if !complete || stopErr != nil {
			state = "incomplete"
		}
	} else {
		current, currentSnapshot, err := responseFirstTimingBatchState(runtime)
		if err != nil {
			http.Error(w, "timing batch unavailable", http.StatusServiceUnavailable)
			return
		}
		state, snapshot = current, currentSnapshot
	}
	response := responseFirstTimingStatus{
		State: state, Complete: state == "frozen", CollectionStarted: true,
		RestartRequired: restartRequired || state != "collecting",
		ReservedBytes:   runtime.processing.timing.ReservedBytes(),
		ExportMaxBytes:  responseFirstTimingExportMaxBytes,
	}
	if state != "collecting" {
		response.TurnCount = snapshot.TurnCount()
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(response)
}

type responseFirstTimingExportSummary struct {
	SchemaVersion   int    `json:"schema_version"`
	Complete        bool   `json:"complete"`
	DrainIncomplete bool   `json:"drain_incomplete"`
	DroppedStamps   uint64 `json:"dropped_stamps"`
	MissingSpanEnds uint64 `json:"missing_span_ends"`
	TurnCount       uint32 `json:"turn_count"`
}

type responseFirstTimingTurnExportHeader struct {
	TurnIndex       uint32 `json:"turn_index"`
	TurnID          string `json:"turn_id,omitempty"`
	TraceID         string `json:"trace_id,omitempty"`
	DroppedStamps   uint32 `json:"dropped_stamps"`
	MissingSpanEnds uint32 `json:"missing_span_ends"`
	ForegroundDone  bool   `json:"foreground_done"`
	PendingChildren uint32 `json:"pending_children"`
}

type responseFirstTimingStampExport struct {
	OffsetNS  int64  `json:"offset_ns"`
	SpanID    uint32 `json:"span_id"`
	ParentID  uint32 `json:"parent_id"`
	Point     uint16 `json:"point"`
	Phase     uint8  `json:"phase"`
	Result    uint8  `json:"result"`
	Component uint16 `json:"component"`
	Attempt   uint16 `json:"attempt"`
	Ref       uint32 `json:"ref"`
}

var errResponseFirstTimingExportTooLarge = errors.New("response-first timing export exceeds limit")

type boundedTimingJSONBuffer struct {
	data []byte
	max  int
}

func newBoundedTimingJSONBuffer(maxBytes int) (*boundedTimingJSONBuffer, error) {
	if maxBytes < 1 {
		return nil, errResponseFirstTimingExportTooLarge
	}
	return &boundedTimingJSONBuffer{data: make([]byte, 0, maxBytes), max: maxBytes}, nil
}

func (b *boundedTimingJSONBuffer) Write(value []byte) (int, error) {
	if b == nil || len(value) > b.max-len(b.data) {
		return 0, errResponseFirstTimingExportTooLarge
	}
	b.data = append(b.data, value...)
	return len(value), nil
}

func encodeResponseFirstTimingExport(snapshot turntiming.BatchSnapshot, maxBytes int) ([]byte, error) {
	buffer, err := newBoundedTimingJSONBuffer(maxBytes)
	if err != nil {
		return nil, err
	}
	complete, dropped, missing := responseFirstTimingCaptureQuality(snapshot)
	summary, err := json.Marshal(responseFirstTimingExportSummary{
		SchemaVersion: responseFirstTimingExportSchema, Complete: complete,
		DrainIncomplete: snapshot.IsIncomplete(), DroppedStamps: dropped,
		MissingSpanEnds: missing, TurnCount: snapshot.TurnCount(),
	})
	if err != nil {
		return nil, err
	}
	if len(summary) == 0 || summary[len(summary)-1] != '}' {
		return nil, errors.New("invalid timing export summary")
	}
	if _, err := buffer.Write(summary[:len(summary)-1]); err != nil {
		return nil, err
	}
	if _, err := buffer.Write([]byte(`,"turns":[`)); err != nil {
		return nil, err
	}
	for turnIndex := uint32(0); turnIndex < snapshot.TurnCount(); turnIndex++ {
		turn, ok := snapshot.Turn(turnIndex)
		if !ok {
			return nil, errors.New("frozen timing turn is unavailable")
		}
		if turnIndex > 0 {
			if _, err := buffer.Write([]byte{','}); err != nil {
				return nil, err
			}
		}
		turnID, traceID, identified := turn.Identity()
		if !identified {
			turnID, traceID = "", ""
		}
		header, err := json.Marshal(responseFirstTimingTurnExportHeader{
			TurnIndex: turnIndex, TurnID: turnID, TraceID: traceID,
			DroppedStamps: turn.Dropped(), MissingSpanEnds: turn.MissingSpanEnds(),
			ForegroundDone: turn.ForegroundDone(), PendingChildren: turn.PendingChildren(),
		})
		if err != nil {
			return nil, err
		}
		if len(header) == 0 || header[len(header)-1] != '}' {
			return nil, errors.New("invalid timing turn export header")
		}
		if _, err := buffer.Write(header[:len(header)-1]); err != nil {
			return nil, err
		}
		if _, err := buffer.Write([]byte(`,"stamps":[`)); err != nil {
			return nil, err
		}
		for stampIndex := uint32(0); stampIndex < turn.StampCount(); stampIndex++ {
			stamp, ok := turn.Stamp(stampIndex)
			if !ok {
				return nil, errors.New("frozen timing stamp is unavailable")
			}
			if stampIndex > 0 {
				if _, err := buffer.Write([]byte{','}); err != nil {
					return nil, err
				}
			}
			row, err := json.Marshal(responseFirstTimingStampExport{
				OffsetNS: stamp.OffsetNS, SpanID: stamp.SpanID, ParentID: stamp.ParentID,
				Point: stamp.Point, Phase: stamp.Phase, Result: stamp.Result,
				Component: stamp.Component, Attempt: stamp.Attempt, Ref: stamp.Ref,
			})
			if err != nil {
				return nil, err
			}
			if _, err := buffer.Write(row); err != nil {
				return nil, err
			}
		}
		if _, err := buffer.Write([]byte("]}")); err != nil {
			return nil, err
		}
	}
	if _, err := buffer.Write([]byte("]}")); err != nil {
		return nil, err
	}
	return buffer.data, nil
}

func responseFirstTimingCaptureQuality(snapshot turntiming.BatchSnapshot) (complete bool, dropped, missing uint64) {
	complete = !snapshot.IsIncomplete()
	for index := uint32(0); index < snapshot.TurnCount(); index++ {
		turn, ok := snapshot.Turn(index)
		if !ok {
			return false, dropped, missing
		}
		turnDropped := turn.Dropped()
		turnMissing := turn.MissingSpanEnds()
		dropped += uint64(turnDropped)
		missing += uint64(turnMissing)
		if turnDropped != 0 || turnMissing != 0 || !turn.ForegroundDone() || turn.PendingChildren() != 0 {
			complete = false
		}
	}
	return complete, dropped, missing
}
