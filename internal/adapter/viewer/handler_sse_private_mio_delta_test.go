package viewer

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestPrivateMioDeltaSSEUsesOwnerLaneWithoutCanonicalFilter(t *testing.T) {
	hub := NewEventHub(8)
	var filterCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ownerID := r.URL.Query().Get("owner")
		hub.HandleSSEWithFilterAndPrivateMioDeltas(w, r, func(context.Context, orchestrator.OrchestratorEvent) (bool, error) {
			filterCalls.Add(1)
			return false, nil
		}, ownerID)
	}))
	defer server.Close()

	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	ownerRequest, err := http.NewRequestWithContext(ownerCtx, http.MethodGet, server.URL+"?owner=ren&from=now", nil)
	if err != nil {
		t.Fatal(err)
	}
	ownerResponse, err := http.DefaultClient.Do(ownerRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer ownerResponse.Body.Close()
	if ownerResponse.StatusCode != http.StatusOK {
		t.Fatalf("owner SSE status=%d", ownerResponse.StatusCode)
	}
	waitForPrivateMioDeltaSubscriber(t, hub, "ren")

	otherCtx, cancelOther := context.WithCancel(context.Background())
	defer cancelOther()
	otherRequest, err := http.NewRequestWithContext(otherCtx, http.MethodGet, server.URL+"?owner=other&from=now", nil)
	if err != nil {
		t.Fatal(err)
	}
	otherResponse, err := http.DefaultClient.Do(otherRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer otherResponse.Body.Close()
	if otherResponse.StatusCode != http.StatusOK {
		t.Fatalf("other-owner SSE status=%d", otherResponse.StatusCode)
	}
	waitForPrivateMioDeltaSubscriber(t, hub, "other")

	publishPrivateDeltaForTest(t, hub, "ren", privateMioDeltaTestEvent("private prefix"), newViewerTestPublicationQualification())
	ownerData := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(ownerResponse.Body)
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				ownerData <- "read error: " + readErr.Error()
				return
			}
			if strings.HasPrefix(line, "data: ") {
				ownerData <- line
				return
			}
		}
	}()
	select {
	case line := <-ownerData:
		if !strings.Contains(line, `"type":"agent.response_delta"`) || !strings.Contains(line, `"content":"private prefix"`) {
			t.Fatalf("owner received unexpected private SSE frame: %s", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("authenticated owner SSE did not receive the private delta")
	}

	otherData := make(chan error, 1)
	go func() {
		_, readErr := bufio.NewReader(otherResponse.Body).ReadString('\n')
		if readErr == nil {
			otherData <- nil
			return
		}
		otherData <- readErr
	}()
	select {
	case readErr := <-otherData:
		if readErr == nil || readErr != io.EOF && !strings.Contains(readErr.Error(), "canceled") {
			t.Fatalf("another owner received or unexpectedly failed on private SSE: %v", readErr)
		}
	case <-time.After(100 * time.Millisecond):
		// No frame was written to the other owner's lane.
	}
	if got := filterCalls.Load(); got != 0 {
		t.Fatalf("private delta invoked canonical event filter %d times", got)
	}
}

func TestPrivateMioDeltaSSEClaimPrecedesBlockedSocketWriteAndRevokesOnlyPendingSuffix(t *testing.T) {
	hub := NewEventHub(8)
	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/viewer/events?from=now", nil).WithContext(requestCtx)
	writer := newBlockingSSEWriter()
	done := make(chan struct{})
	go func() {
		hub.HandleSSEWithFilterAndPrivateMioDeltas(writer, request, func(context.Context, orchestrator.OrchestratorEvent) (bool, error) {
			return false, nil
		}, "ren")
		close(done)
	}()
	waitForPrivateMioDeltaSubscriber(t, hub, "ren")

	qualification := newViewerTestPublicationQualification()
	second := qualification.Retain()
	publishPrivateDeltaForTest(t, hub, "ren", privateMioDeltaTestEvent("claimed prefix"), qualification)
	publishPrivateDeltaForTest(t, hub, "ren", privateMioDeltaTestEvent("pending suffix"), second)
	select {
	case <-writer.entered:
		// ClaimOwnerScopedLiveEvent already returned before the socket Write
		// blocked, so this prefix has crossed the publication linearization point.
	case <-time.After(2 * time.Second):
		t.Fatal("SSE writer did not reach the blocking socket write")
	}
	if qualification.useCount() != 1 {
		t.Fatalf("claim use count=%d at blocked write, want exactly the already-claimed prefix", qualification.useCount())
	}
	qualification.revoke()
	close(writer.unblock)

	deadline := time.Now().Add(2 * time.Second)
	for qualification.releaseCount() < 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if qualification.releaseCount() != 4 {
		t.Fatalf("qualification release count=%d after pending suffix drop, want two producer and two subscriber handles", qualification.releaseCount())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SSE handler did not stop after request cancellation")
	}
	body := writer.String()
	if !strings.Contains(body, `"content":"claimed prefix"`) || strings.Contains(body, `"content":"pending suffix"`) {
		t.Fatalf("SSE body did not preserve only the already-claimed prefix: %s", body)
	}
}

func TestPrivateMioSSEWritesOrdinaryCanonicalFinalWithoutGuardTicket(t *testing.T) {
	hub := NewEventHub(8)
	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/viewer/events?from=now", nil).WithContext(requestCtx)
	writer := newBlockingSSEWriter()
	done := make(chan struct{})
	var filterCalls atomic.Int32
	go func() {
		hub.HandleSSEWithFilterAndPrivateMioDeltas(writer, request, func(_ context.Context, event orchestrator.OrchestratorEvent) (bool, error) {
			filterCalls.Add(1)
			return event.ChannelAddress == "cmd:ren" && event.Visibility == "private" && event.PublicationIncarnation == "session-incarnation-ren-1", nil
		}, "ren")
		close(done)
	}()
	waitForPrivateMioDeltaSubscriber(t, hub, "ren")

	final := privateMioFinalTestEvent("durable synchronous final")
	final.EventSeq = 1
	final.Visibility = "private"
	final.PublicationIncarnation = "session-incarnation-ren-1"
	if published, err := hub.PublishLive(final); err != nil || !published {
		t.Fatalf("PublishLive published=%t err=%v", published, err)
	}
	select {
	case <-writer.entered:
		close(writer.unblock)
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("owner SSE dropped a route-approved canonical private Mio final with no live claim ticket")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("owner SSE handler did not stop after cancellation")
	}
	if calls := filterCalls.Load(); calls != 1 {
		t.Fatalf("canonical final policy calls=%d, want exactly one owner/incarnation filter", calls)
	}
	if body := writer.String(); !strings.Contains(body, `"type":"agent.response"`) || !strings.Contains(body, `"content":"durable synchronous final"`) {
		t.Fatalf("canonical private final was not written: %s", body)
	}
}

func TestPrivateMioSSEDropsGuardedFinalRevokedBeforeWriterClaim(t *testing.T) {
	hub := NewEventHub(8)
	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/viewer/events?from=now", nil).WithContext(requestCtx)
	writer := newBlockingSSEWriter()
	done := make(chan struct{})
	filterReachedFinal := make(chan struct{})
	continueFilter := make(chan struct{})
	go func() {
		hub.HandleSSEWithFilterAndPrivateMioDeltas(writer, request, func(_ context.Context, event orchestrator.OrchestratorEvent) (bool, error) {
			if event.Type == "agent.response" {
				close(filterReachedFinal)
				<-continueFilter
				return true, nil
			}
			return event.Type == "metrics.latency", nil
		}, "ren")
		close(done)
	}()
	waitForPrivateMioDeltaSubscriber(t, hub, "ren")

	qualification := newViewerTestPublicationQualification()
	final := privateMioFinalTestEvent("must remain unclaimed")
	final.EventSeq = 1
	accepted, cleanup, err := hub.PublishAcceptedWithClaim(final, "ren", qualification)
	cleanup()
	if err != nil || !accepted {
		t.Fatalf("PublishAcceptedWithClaim accepted=%t err=%v", accepted, err)
	}
	select {
	case <-filterReachedFinal:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not reach the guarded final policy filter")
	}
	qualification.revoke()
	marker := orchestrator.NewEventWithTraceID(final.TraceID, "metrics.latency", "mio", "user", "after stale final", "CHAT", string(final.TaskID), string(final.SessionID), "viewer", "cmd:ren")
	marker.EventSeq = 2
	marker.TurnID = final.TurnID
	marker.MessageID = final.MessageID
	marker.RunID = final.RunID
	if published, err := hub.PublishLive(marker); err != nil || !published {
		t.Fatalf("PublishLive marker published=%t err=%v", published, err)
	}
	close(continueFilter)
	select {
	case <-writer.entered:
		close(writer.unblock)
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("SSE handler did not continue after dropping the stale final")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not stop after cancellation")
	}
	body := writer.String()
	if strings.Contains(body, `"content":"must remain unclaimed"`) || !strings.Contains(body, `"content":"after stale final"`) {
		t.Fatalf("stale guarded final was written or following event was lost: %s", body)
	}
	if qualification.releaseCount() != 2 {
		t.Fatalf("revoked final qualification releases=%d, want producer and pending subscriber references", qualification.releaseCount())
	}
}

func TestPrivateMioSSEDoesNotWriteGuardedFinalAfterOverflowDropsTicket(t *testing.T) {
	hub := NewEventHub(128)
	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/viewer/events?from=now", nil).WithContext(requestCtx)
	writer := newBlockingSSEWriter()
	done := make(chan struct{})
	go func() {
		hub.HandleSSEWithFilterAndPrivateMioDeltas(writer, request, func(_ context.Context, event orchestrator.OrchestratorEvent) (bool, error) {
			return event.Type == "agent.response" && event.ChannelAddress == "cmd:ren" && event.Visibility == "private" && event.PublicationIncarnation == "session-incarnation-ren-1", nil
		}, "ren")
		close(done)
	}()
	waitForPrivateMioDeltaSubscriber(t, hub, "ren")

	// Hold the writer after it has claimed a prefix, leaving the guarded final
	// in the owner's bounded queue while later canonical events overflow it.
	prefixQualification := newViewerTestPublicationQualification()
	publishPrivateDeltaForTest(t, hub, "ren", privateMioDeltaTestEvent("claimed prefix"), prefixQualification)
	select {
	case <-writer.entered:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("SSE writer did not block after claiming the prefix")
	}

	qualification := newViewerTestPublicationQualification()
	final := privateMioFinalTestEvent("must not escape overflow removal")
	final.EventSeq = 1
	accepted, cleanup, err := hub.PublishAcceptedWithClaim(final, "ren", qualification)
	cleanup()
	if err != nil || !accepted {
		t.Fatalf("PublishAcceptedWithClaim accepted=%t err=%v", accepted, err)
	}

	// The first queued frame is the guarded final. Subsequent canonical events
	// fill the bounded channel; one additional event removes the subscriber and
	// releases its claim while the closed channel still contains the final.
	for seq := int64(2); seq <= int64(privateMioDeltaClientCapacity+1); seq++ {
		filler := orchestrator.NewEventWithTraceID(final.TraceID, "metrics.latency", "mio", "user", "buffered", "CHAT", string(final.TaskID), string(final.SessionID), "viewer", "cmd:ren")
		filler.EventSeq = modulecore.EventSeq(seq)
		if published, publishErr := hub.PublishLive(filler); publishErr != nil || !published {
			t.Fatalf("PublishLive filler seq=%d published=%t err=%v", seq, published, publishErr)
		}
	}
	qualification.revoke()
	close(writer.unblock)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SSE handler did not drain the closed owner channel")
	}
	if body := writer.String(); !strings.Contains(body, `"content":"claimed prefix"`) || strings.Contains(body, `"content":"must not escape overflow removal"`) {
		t.Fatalf("closed-channel drain wrote a guarded final after its ticket was removed: %s", body)
	} else if strings.Contains(body, guardedOwnerScopedFrameMarker) {
		t.Fatalf("internal guarded-frame marker leaked into SSE output: %s", body)
	}
	if qualification.releaseCount() != 2 {
		t.Fatalf("overflowed final qualification releases=%d, want producer and dropped subscriber refs", qualification.releaseCount())
	}
}

type blockingSSEWriter struct {
	header  http.Header
	mu      sync.Mutex
	body    strings.Builder
	entered chan struct{}
	unblock chan struct{}
	blocked atomic.Bool
}

func newBlockingSSEWriter() *blockingSSEWriter {
	return &blockingSSEWriter{header: make(http.Header), entered: make(chan struct{}), unblock: make(chan struct{})}
}

func (w *blockingSSEWriter) Header() http.Header { return w.header }

func (*blockingSSEWriter) WriteHeader(int) {}

func (w *blockingSSEWriter) Write(data []byte) (int, error) {
	if strings.HasPrefix(string(data), "data: ") && w.blocked.CompareAndSwap(false, true) {
		close(w.entered)
		<-w.unblock
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(data)
}

func (*blockingSSEWriter) Flush() {}

func (w *blockingSSEWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

func waitForPrivateMioDeltaSubscriber(t *testing.T, hub *EventHub, ownerID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		hub.mu.RLock()
		count := len(hub.privateMioDeltaClients[ownerID])
		hub.mu.RUnlock()
		if count > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("owner %q did not subscribe to the private Mio delta lane", ownerID)
}
