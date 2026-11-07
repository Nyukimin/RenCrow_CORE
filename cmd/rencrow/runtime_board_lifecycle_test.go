package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
)

const boardLifecycleTestBearer = "synthetic-lifecycle-token"

type boardLifecycleSyntheticStream struct {
	events     chan boardcommunication.BoardEvent
	consumed   chan int64
	closed     chan struct{}
	finished   chan struct{}
	closeOnce  sync.Once
	finishOnce sync.Once
}

func newBoardLifecycleSyntheticStream() *boardLifecycleSyntheticStream {
	return &boardLifecycleSyntheticStream{
		events: make(chan boardcommunication.BoardEvent, 8), consumed: make(chan int64, 8),
		closed: make(chan struct{}), finished: make(chan struct{}),
	}
}

func (stream *boardLifecycleSyntheticStream) Next() (boardcommunication.BoardEvent, error) {
	select {
	case event := <-stream.events:
		select {
		case stream.consumed <- event.Seq:
		default:
		}
		return event, nil
	case <-stream.closed:
		stream.finishOnce.Do(func() { close(stream.finished) })
		return boardcommunication.BoardEvent{}, boardcommunication.ErrUnavailable
	}
}

func (stream *boardLifecycleSyntheticStream) Close() error {
	stream.closeOnce.Do(func() { close(stream.closed) })
	return nil
}

func TestRunBoardCommunicationLifecycleDrainsAndCoalescesSSEWake(t *testing.T) {
	stream := newBoardLifecycleSyntheticStream()
	firstCatchUpStarted := make(chan struct{}, 1)
	releaseFirstCatchUp := make(chan struct{})
	catchUpCalls := make(chan int, 8)
	var closeCalls atomic.Int32
	var catchUpCount atomic.Int32
	generation := boardCommunicationLifecycleGeneration{
		openEvents: func(context.Context, int64) (boardCommunicationEventStream, error) {
			return stream, nil
		},
		catchUp: func(ctx context.Context) error {
			call := int(catchUpCount.Add(1))
			catchUpCalls <- call
			if call == 1 {
				close(firstCatchUpStarted)
				select {
				case <-releaseFirstCatchUp:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		},
		close: func() {
			closeCalls.Add(1)
			_ = stream.Close()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	runExited := make(chan struct{})
	go func() {
		runDone <- runBoardCommunicationLifecycle(ctx, boardCommunicationLifecycleDeps{
			enabled: true,
			open:    func(context.Context) (boardCommunicationLifecycleGeneration, error) { return generation, nil },
		})
		close(runExited)
	}()
	defer func() {
		cancel()
		select {
		case <-runExited:
		case <-time.After(time.Second):
			t.Error("lifecycle did not stop after cancellation")
		}
	}()
	select {
	case <-firstCatchUpStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("startup durable catch-up did not start")
	}
	if call := <-catchUpCalls; call != 1 {
		t.Fatalf("first catch-up call = %d, want 1", call)
	}
	for sequence := int64(1); sequence <= 3; sequence++ {
		stream.events <- boardcommunication.BoardEvent{Seq: sequence, Type: "message_posted", Time: "2026-11-05T03:07:00Z"}
		select {
		case got := <-stream.consumed:
			if got != sequence {
				t.Fatalf("drained SSE sequence = %d, want %d", got, sequence)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("SSE reader did not drain sequence %d while catch-up was blocked", sequence)
		}
	}
	close(releaseFirstCatchUp)
	select {
	case call := <-catchUpCalls:
		if call != 2 {
			t.Fatalf("catch-up calls = %d before wake, want startup call 1", call)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("coalesced SSE wake did not trigger durable catch-up")
	}
	select {
	case call := <-catchUpCalls:
		t.Fatalf("three drained hints caused extra catch-up call %d; want one coalesced wake", call)
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("runBoardCommunicationLifecycle() after cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("lifecycle did not return after cancellation")
	}
	select {
	case <-stream.finished:
	case <-time.After(time.Second):
		t.Fatal("lifecycle returned before its SSE reader stopped")
	}
	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("generation close calls = %d, want 1", got)
	}
}

func TestRunBoardCommunicationLifecycleReconnectsAndCatchesUpWithPinnedSyntheticSSE(t *testing.T) {
	pin := boardConnectionTestPin()
	var streamOpens, badIdentity atomic.Int32
	firstEventWritten := make(chan struct{})
	endFirstStream := make(chan struct{})
	var endFirstOnce sync.Once
	defer func() { endFirstOnce.Do(func() { close(endFirstStream) }) }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+boardLifecycleTestBearer ||
			r.Header.Get("X-BOARD-Principal-ID") != pin.PrincipalID ||
			r.Header.Get("X-BOARD-Journal-Instance-ID") != pin.JournalInstanceID ||
			r.Header.Get("Last-Event-ID") == "" {
			badIdentity.Add(1)
			http.Error(w, "synthetic bound SSE request mismatch", http.StatusForbidden)
			return
		}
		attempt := streamOpens.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("synthetic SSE response does not support flushing")
			return
		}
		flusher.Flush()
		if attempt == 1 {
			event := boardcommunication.BoardEvent{Seq: 81, Type: "message_posted", Time: "2026-11-05T03:07:00Z"}
			data, err := json.Marshal(event)
			if err != nil {
				t.Errorf("marshal synthetic event: %v", err)
				return
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: board\ndata: %s\n\n", event.Seq, data); err != nil {
				return
			}
			flusher.Flush()
			close(firstEventWritten)
			select {
			case <-endFirstStream:
			case <-r.Context().Done():
			}
			return
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	var openCalls, catchUpCalls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	runExited := make(chan struct{})
	go func() {
		runDone <- runBoardCommunicationLifecycle(ctx, boardCommunicationLifecycleDeps{
			enabled: true,
			open: func(context.Context) (boardCommunicationLifecycleGeneration, error) {
				openCalls.Add(1)
				client, err := boardcommunication.NewClient(boardcommunication.Options{
					URL: server.URL,
					ExpectedConnection: boardcommunication.ConnectionIdentity{
						PrincipalID: pin.PrincipalID, JournalInstanceID: pin.JournalInstanceID,
					},
					TokenSource: func(context.Context) ([]byte, error) { return []byte(boardLifecycleTestBearer), nil },
				})
				if err != nil {
					return boardCommunicationLifecycleGeneration{}, err
				}
				return boardCommunicationLifecycleGeneration{
					openEvents: func(ctx context.Context, after int64) (boardCommunicationEventStream, error) {
						return client.OpenEvents(ctx, after)
					},
					catchUp: func(context.Context) error {
						catchUpCalls.Add(1)
						return nil
					},
					close: client.Close,
				}, nil
			},
			wait: func(ctx context.Context, _ time.Duration) error {
				timer := time.NewTimer(time.Millisecond)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-timer.C:
					return nil
				}
			},
		})
		close(runExited)
	}()
	defer func() {
		cancel()
		endFirstOnce.Do(func() { close(endFirstStream) })
		select {
		case <-runExited:
		case <-time.After(time.Second):
			t.Error("lifecycle did not stop after cancellation")
		}
	}()
	select {
	case <-firstEventWritten:
	case <-time.After(2 * time.Second):
		t.Fatal("first authenticated synthetic SSE stream did not deliver its event")
	}
	waitForBoardLifecycleCondition(t, "hint catch-up", func() bool { return catchUpCalls.Load() >= 2 })
	endFirstOnce.Do(func() { close(endFirstStream) })
	waitForBoardLifecycleCondition(t, "reconnect startup catch-up", func() bool {
		return streamOpens.Load() >= 2 && catchUpCalls.Load() >= 3
	})
	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("runBoardCommunicationLifecycle() after cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("lifecycle did not return after cancellation")
	}
	if got := badIdentity.Load(); got != 0 {
		t.Fatalf("synthetic SSE requests with mismatched pin or auth = %d, want 0", got)
	}
	if got := openCalls.Load(); got < 2 {
		t.Fatalf("connected generations = %d, want at least 2", got)
	}
}

func TestRunBoardCommunicationLifecycleBoundsUnavailableRetryAndLeavesDisabledLaneInert(t *testing.T) {
	var disabledOpens atomic.Int32
	if err := runBoardCommunicationLifecycle(context.Background(), boardCommunicationLifecycleDeps{
		open: func(context.Context) (boardCommunicationLifecycleGeneration, error) {
			disabledOpens.Add(1)
			return boardCommunicationLifecycleGeneration{}, errors.New("disabled lane opened")
		},
	}); err != nil {
		t.Fatalf("disabled optional lifecycle returned error: %v", err)
	}
	if got := disabledOpens.Load(); got != 0 {
		t.Fatalf("disabled optional lifecycle opened %d connections, want 0", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var opens atomic.Int32
	var delays []time.Duration
	err := runBoardCommunicationLifecycle(ctx, boardCommunicationLifecycleDeps{
		enabled: true,
		open: func(context.Context) (boardCommunicationLifecycleGeneration, error) {
			opens.Add(1)
			return boardCommunicationLifecycleGeneration{}, errors.New("synthetic unavailable connection")
		},
		wait: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			if len(delays) == 6 {
				cancel()
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("unavailable optional lifecycle stopped CORE with error: %v", err)
	}
	if got := opens.Load(); got != 6 || len(delays) != 6 {
		t.Fatalf("bounded retry attempts/waits = %d/%d, want 6/6", got, len(delays))
	}
	for index, delay := range delays {
		if delay <= 0 || delay > boardCommunicationLifecycleMaxRetry {
			t.Fatalf("retry delay[%d] = %s, want within (0,%s]", index, delay, boardCommunicationLifecycleMaxRetry)
		}
		if index > 0 && delay < delays[index-1] {
			t.Fatalf("retry delay[%d] = %s, decreased from %s", index, delay, delays[index-1])
		}
	}
	if delays[len(delays)-1] != boardCommunicationLifecycleMaxRetry {
		t.Fatalf("retry backoff capped at %s, want %s", delays[len(delays)-1], boardCommunicationLifecycleMaxRetry)
	}
}

func waitForBoardLifecycleCondition(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", description)
		case <-ticker.C:
		}
	}
}
