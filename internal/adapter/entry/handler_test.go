package entry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestHandle_Success(t *testing.T) {
	h := Handle(func(ctx context.Context, req Request) (Result, error) {
		return Result{SessionID: req.SessionID, Route: "CHAT", JobID: "j1", Response: "ok"}, nil
	})

	body := []byte(`{"platform":"viewer","channel":"viewer","user_id":"u1","message":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/entry", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json response: %v", err)
	}
	if out["ok"] != true {
		t.Fatalf("expected ok=true, got %+v", out)
	}
}

func TestHandle_RejectsEmptyMessage(t *testing.T) {
	h := Handle(func(ctx context.Context, req Request) (Result, error) {
		return Result{}, nil
	})
	req := httptest.NewRequest(http.MethodPost, "/entry", bytes.NewReader([]byte(`{"message":"  "}`)))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandleWithObserver_SuccessStages(t *testing.T) {
	var got []Stage
	h := HandleWithObserver(
		func(ctx context.Context, req Request) (Result, error) {
			return Result{SessionID: req.SessionID, Route: "CHAT", JobID: "j1", Response: "ok"}, nil
		},
		func(ctx context.Context, stage Stage, req Request, result *Result, err error) {
			got = append(got, stage)
		},
	)

	req := httptest.NewRequest(http.MethodPost, "/entry", bytes.NewReader([]byte(`{"message":"hello"}`)))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	want := []Stage{StageReceived, StagePlanning, StageApplying, StageVerifying, StageCompleted}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stages mismatch: got=%v want=%v", got, want)
	}
}

func TestHandleWithObserver_FailedStage(t *testing.T) {
	var got []Stage
	h := HandleWithObserver(
		func(ctx context.Context, req Request) (Result, error) {
			return Result{}, errors.New("boom")
		},
		func(ctx context.Context, stage Stage, req Request, result *Result, err error) {
			got = append(got, stage)
		},
	)

	req := httptest.NewRequest(http.MethodPost, "/entry", bytes.NewReader([]byte(`{"message":"hello"}`)))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}

	want := []Stage{StageReceived, StagePlanning, StageApplying, StageFailed}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stages mismatch: got=%v want=%v", got, want)
	}
}

func TestHandle_NormalizesCLIPlatformToLocalChannel(t *testing.T) {
	var captured Request
	h := Handle(func(ctx context.Context, req Request) (Result, error) {
		captured = req
		return Result{SessionID: req.SessionID, Route: "CHAT", JobID: "j1", Response: "ok"}, nil
	})

	body := []byte(`{"platform":"cli","user_id":"u1","message":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/entry", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if captured.Channel != "local" {
		t.Fatalf("expected channel=local, got %s", captured.Channel)
	}
	if captured.Platform != "cli" {
		t.Fatalf("expected platform=cli, got %s", captured.Platform)
	}
}

func TestHandle_PreservesProvidedSessionID(t *testing.T) {
	h := Handle(func(ctx context.Context, req Request) (Result, error) {
		return Result{SessionID: req.SessionID, Route: "CHAT", JobID: "j1", Response: "ok"}, nil
	})
	req := httptest.NewRequest(http.MethodPost, "/entry", bytes.NewReader([]byte(`{"platform":"viewer","channel":"viewer","user_id":"u1","session_id":"sess-123","message":"hello"}`)))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"session_id":"sess-123"`) {
		t.Fatalf("expected provided session_id in response, got %s", rec.Body.String())
	}
}
