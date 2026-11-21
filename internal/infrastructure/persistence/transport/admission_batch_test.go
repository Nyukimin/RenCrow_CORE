package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestAdmittedTransportBurstUsesOneWALCommitAndReopensInFIFOOrder(t *testing.T) {
	const writes = 12
	tempBase, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatalf("resolve test temp directory: %v", err)
	}
	root, err := os.MkdirTemp(tempBase, "transport-batch-")
	if err != nil {
		t.Fatalf("create transport batch root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := persistencequeue.NewPersistenceQueue(writes+1, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Shutdown(context.Background())
	if err := store.SetPersistenceQueue(queue); err != nil {
		t.Fatalf("bind transport store to queue: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	if _, err := queue.Enqueue(persistencequeue.PersistenceCommand{
		Owner: persistencequeue.OwnerSession, Kind: "session.batch_test_gate", SizeBytes: 1,
		Apply: func(context.Context) error { close(started); <-release; return nil },
	}, 1); err != nil {
		t.Fatalf("enqueue queue gate: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("queue gate did not start")
	}

	var observationMu sync.Mutex
	var observations []jsonlbatch.TxObservation
	store.batch.SetTxObserver(func(_ context.Context, observation jsonlbatch.TxObservation) {
		observationMu.Lock()
		observations = append(observations, observation)
		observationMu.Unlock()
	})
	admission, err := persistencequeue.Admit(queue, writes, 1<<20)
	if err != nil {
		t.Fatalf("admit transport burst: %v", err)
	}
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	wantRequests := make([]domaintransport.Request, 0, writes)
	wantResponses := make([]domaintransport.Response, 0, writes)
	for index := 0; index < writes; index++ {
		var request domaintransport.Request
		if index == 1 {
			request = wantRequests[0]
			request.Status = domaintransport.RequestStatusResponded
			completedAt := request.SentAt.Add(time.Second)
			request.CompletedAt = &completedAt
			request.Summary = "completed after queued sent state"
		} else {
			requestIndex := index
			if index > 1 {
				requestIndex--
			}
			request = batchTestRequest(requestIndex)
		}
		wantRequests = append(wantRequests, request)
		response := batchTestResponse(index, request)
		wantResponses = append(wantResponses, response)
		if err := store.Transaction(ctx, func(tx domaintransport.Store) error {
			if err := tx.SaveRequest(ctx, request); err != nil {
				return err
			}
			return tx.SaveResponse(ctx, response)
		}); err != nil {
			t.Fatalf("enqueue request/response transaction %d: %v", index, err)
		}
	}
	if err := admission.Release(); err != nil {
		t.Fatalf("release admission: %v", err)
	}
	close(release)
	barrierCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := admission.Barrier(barrierCtx); err != nil {
		t.Fatalf("wait for durable transport burst: %v", err)
	}

	observationMu.Lock()
	gotObservations := append([]jsonlbatch.TxObservation(nil), observations...)
	observationMu.Unlock()
	if len(gotObservations) != 1 {
		t.Fatalf("WAL write transactions for %d accepted transport records = %d, want 1: %+v", writes, len(gotObservations), gotObservations)
	}
	if got := gotObservations[0]; got.Err != nil || !got.Acquired ||
		!reflect.DeepEqual(got.Files, []string{requestFilename, responseFilename}) {
		t.Fatalf("WAL transaction observation = %+v, want one durable request/response-file write", got)
	}
	t.Logf("fixed queued burst: accepted=%d, WAL write transactions=%d", writes, len(gotObservations))

	data, err := os.ReadFile(filepath.Join(root, requestFilename))
	if err != nil {
		t.Fatal(err)
	}
	var gotRequests []domaintransport.Request
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		var request domaintransport.Request
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			t.Fatalf("decode persisted request row: %v", err)
		}
		gotRequests = append(gotRequests, request)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan persisted request rows: %v", err)
	}
	if !reflect.DeepEqual(gotRequests, wantRequests) {
		t.Fatalf("persisted request history = %+v, want every full request in FIFO order %+v", gotRequests, wantRequests)
	}
	responseData, err := os.ReadFile(filepath.Join(root, responseFilename))
	if err != nil {
		t.Fatal(err)
	}
	var gotResponses []domaintransport.Response
	responseScanner := bufio.NewScanner(bytes.NewReader(responseData))
	for responseScanner.Scan() {
		var response domaintransport.Response
		if err := json.Unmarshal(responseScanner.Bytes(), &response); err != nil {
			t.Fatalf("decode persisted response row: %v", err)
		}
		gotResponses = append(gotResponses, response)
	}
	if err := responseScanner.Err(); err != nil {
		t.Fatalf("scan persisted response rows: %v", err)
	}
	if !reflect.DeepEqual(gotResponses, wantResponses) {
		t.Fatalf("persisted response history = %+v, want FIFO order %+v", gotResponses, wantResponses)
	}
	if err := jsonlbatch.CheckSettled(root, []string{requestFilename, responseFilename}); err != nil {
		t.Fatalf("transport WAL after commit = %v, want settled", err)
	}

	reopened, err := NewJSONLStore(root)
	if err != nil {
		t.Fatalf("reopen transport store: %v", err)
	}
	loaded, err := reopened.ListRequests(context.Background(), domaintransport.RequestFilter{})
	if err != nil {
		t.Fatalf("read requests after reopen/recovery: %v", err)
	}
	if len(loaded) != writes-1 {
		t.Fatalf("latest request projection after reopen = %d, want %d unique IDs", len(loaded), writes-1)
	}
	updated, err := reopened.GetRequest(context.Background(), wantRequests[0].RequestID)
	if err != nil {
		t.Fatalf("read updated request after reopen: %v", err)
	}
	if !reflect.DeepEqual(updated, wantRequests[1]) {
		t.Fatalf("latest repeated request = %+v, want terminal update %+v", updated, wantRequests[1])
	}
}

func batchTestRequest(index int) domaintransport.Request {
	return domaintransport.Request{
		RequestID: modulecore.NewRequestID(), TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(),
		ActionID: modulecore.NewActionID(), AttemptID: modulecore.NewAttemptID(), Operation: fmt.Sprintf("batch-%02d", index),
		Sequence: index + 1, Status: domaintransport.RequestStatusSent,
		SentAt: time.Date(2026, 11, 21, 0, 0, index, 0, time.UTC),
	}
}

func batchTestResponse(index int, request domaintransport.Request) domaintransport.Response {
	return domaintransport.Response{
		ResponseID: modulecore.NewResponseID(), RequestID: request.RequestID,
		TaskID: request.TaskID, RunID: request.RunID, ActionID: request.ActionID, AttemptID: request.AttemptID,
		ExternalRef: fmt.Sprintf("external-%02d", index), ReceivedAt: request.SentAt.Add(time.Minute),
	}
}
