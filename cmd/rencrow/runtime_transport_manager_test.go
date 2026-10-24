package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRuntimeTransportManagerUsesSharedQueueForAdmittedWrites(t *testing.T) {
	queue, err := newRuntimePersistenceQueue()
	if err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{})
	started := make(chan struct{})
	var releaseOnce sync.Once
	releaseBlocked := func() { releaseOnce.Do(func() { close(blocked) }) }
	var admission *persistencequeue.Admission
	t.Cleanup(func() {
		releaseBlocked()
		if admission != nil {
			_ = admission.Release()
		}
		shutdownRuntimeQueue(t, queue)
	})

	actionOwner, err := newRuntimeActionOwner(t.TempDir(), actionpersistence.OpenOptions{Index: true}, queue)
	if err != nil {
		t.Fatal(err)
	}
	taskID, runID, traceID := modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewTraceID()
	ctx, err := domainexecution.WithIdentity(context.Background(), taskID, runID, traceID)
	if err != nil {
		t.Fatal(err)
	}
	createdAction, attempt, err := actionOwner.manager.CreateAction(ctx, actionmanager.CreateInput{
		TaskID: taskID, RunID: runID, Kind: action.KindLLM, Name: "llm.chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = domainexecution.WithBoundActionAttempt(ctx, createdAction.ActionID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}

	manager, err := newRuntimeTransportManager(t.TempDir(), actionOwner.manager, queue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Enqueue(persistencequeue.PersistenceCommand{
		Owner: persistencequeue.OwnerTransport, Kind: "test.block", SizeBytes: 1,
		Apply: func(context.Context) error {
			close(started)
			<-blocked
			return nil
		},
	}, 1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("shared persistence worker did not enter the test blocker")
	}

	admission, err = persistencequeue.Admit(queue, 2, 1<<16)
	if err != nil {
		t.Fatal(err)
	}
	admittedContext := persistencequeue.WithAdmission(ctx, admission)
	startedAt := time.Now()
	request, err := manager.BeginRequest(admittedContext, "runtime.test")
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(startedAt); elapsed > 250*time.Millisecond {
		t.Fatalf("admitted Transport write waited for the blocked worker: %v", elapsed)
	}
	requests, err := manager.ListRequests(admittedContext, domaintransport.RequestFilter{AttemptID: attempt.AttemptID})
	if err != nil || len(requests) != 1 || requests[0].RequestID != request.RequestID {
		t.Fatalf("Transport read-your-writes result=%#v err=%v", requests, err)
	}

	releaseBlocked()
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("wait for admitted Transport write: %v", err)
	}
	if err := admission.Release(); err != nil {
		t.Fatalf("release Transport admission: %v", err)
	}
	admission = nil
}
