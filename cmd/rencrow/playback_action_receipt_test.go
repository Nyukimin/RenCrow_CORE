package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func TestPlaybackActionRecorderPersistsActualOutcomeIdempotently(t *testing.T) {
	tasks, actions := newTTSActionTestOwners(t)
	recorder, err := newPlaybackActionRecorder(actions, tasks, "mio")
	if err != nil {
		t.Fatal(err)
	}
	ack := ttsPlaybackAckRequest{
		PublicPlaybackRef: "response-1",
		SessionID:         "session-1",
		UtteranceID:       "utterance-1",
		Status:            "ended",
	}
	if err := recorder.Record(context.Background(), ack); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Record(context.Background(), ack); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	storedActions, err := actions.ListActions(context.Background(), domainaction.Filter{})
	if err != nil || len(storedActions) != 1 {
		t.Fatalf("actions=%#v err=%v", storedActions, err)
	}
	action := storedActions[0]
	attempts, err := actions.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: action.ActionID})
	if err != nil {
		t.Fatal(err)
	}
	task, err := tasks.Get(context.Background(), action.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := tasks.GetRun(context.Background(), action.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if action.Kind != domainaction.KindPlayback || action.Status != domainaction.StatusSucceeded || len(attempts) != 1 || attempts[0].Status != domainaction.AttemptStatusSucceeded {
		t.Fatalf("Playback Action lifecycle action=%#v attempts=%#v", action, attempts)
	}
	if task.Status != domaintask.StatusSucceeded || run.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("Playback Task/Run lifecycle task=%#v run=%#v", task, run)
	}
}

func TestTTSPlaybackAckDoesNotRecordInactiveViewerObservation(t *testing.T) {
	resetActiveViewerControlForTest()
	tasks, actions := newTTSActionTestOwners(t)
	recorder, err := newPlaybackActionRecorder(actions, tasks, "mio")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(ttsPlaybackAckRequest{
		PublicPlaybackRef: "inactive-response",
		ViewerClientID:    "inactive-viewer",
		Status:            "ended",
	})
	req := httptest.NewRequest(http.MethodPost, "/viewer/tts/playback-ack", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handleTTSPlaybackAck(recorder)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	storedActions, err := actions.ListActions(context.Background(), domainaction.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(storedActions) != 0 {
		t.Fatalf("inactive Viewer observation created Playback Action: %#v", storedActions)
	}
}
