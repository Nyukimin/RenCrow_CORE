package boardcommunication

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newMioDiaryClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(Options{
		URL: server.URL,
		TokenSource: func(context.Context) ([]byte, error) {
			return []byte(syntheticBearer), nil
		},
		ExpectedConnection: ConnectionIdentity{
			PrincipalID:       "rencrow-comm",
			JournalInstanceID: syntheticConnectionIdentity.JournalInstanceID,
		},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(client.Close)
	return client, server
}

func TestPutDiaryUsesPinnedActorAndExactWriteScope(t *testing.T) {
	input := DiaryWrite{
		ActorID:    "mio",
		Date:       "2026-11-13",
		Revision:   3,
		Body:       "confirmed work",
		Reflection: "short reflection",
	}
	client, _ := newMioDiaryClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/diaries/2026-11-13" || r.URL.RawQuery != "" {
			t.Errorf("request = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer "+syntheticBearer ||
			r.Header.Get("X-BOARD-Principal-ID") != "rencrow-comm" ||
			r.Header.Get("X-BOARD-Journal-Instance-ID") != syntheticConnectionIdentity.JournalInstanceID {
			t.Errorf("request omitted authentication or pinned connection headers")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		} else if len(body) != 3 || body["revision"] != float64(input.Revision) || body["body"] != input.Body || body["reflection"] != input.Reflection {
			t.Errorf("request body = %#v, want only revision/body/reflection", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Correlation-ID", syntheticCorrelation)
		_, _ = io.WriteString(w, `{"entry":{"actor_id":"mio","date":"2026-11-13","revision":3,"body":"confirmed work","reflection":"short reflection","updated_at":"2026-11-13T14:15:00Z"},"correlation_id":"`+syntheticCorrelation+`"}`)
	}))

	entry, err := client.PutDiary(context.Background(), input)
	if err != nil {
		t.Fatalf("PutDiary: %v", err)
	}
	if entry.DiaryWrite != input || entry.CorrelationID != syntheticCorrelation ||
		!entry.UpdatedAt.Equal(time.Date(2026, 11, 13, 14, 15, 0, 0, time.UTC)) {
		t.Fatalf("PutDiary entry = %+v", entry)
	}
}

func TestPutDiaryRejectsUnpinnedActorAndInvalidDateBeforeRequest(t *testing.T) {
	var requests int
	client, _ := newMioDiaryClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	for _, input := range []DiaryWrite{
		{ActorID: "shiro", Date: "2026-11-13", Revision: 1, Body: "body", Reflection: "reflection"},
		{ActorID: "mio", Date: "2026-02-30", Revision: 1, Body: "body", Reflection: "reflection"},
		{ActorID: "mio", Date: "2026-11-13", Revision: 0, Body: "body", Reflection: "reflection"},
		{ActorID: "mio", Date: "2026-11-13", Revision: -1, Body: "body", Reflection: "reflection"},
		{ActorID: "mio", Date: "2026-11-13", Revision: 1, Body: "body", Reflection: "   "},
		{ActorID: "mio", Date: "2026-11-13", Revision: 1, Body: strings.Repeat("x", maxDiaryTextBytes), Reflection: "x"},
	} {
		if _, err := client.PutDiary(context.Background(), input); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("PutDiary(%+v) error = %v, want ErrInvalidRequest", input, err)
		}
	}
	if requests != 0 {
		t.Fatalf("invalid inputs sent %d requests", requests)
	}
}

func TestPutDiarySupports128KiBCombinedTextUnderBOARDJSONLimit(t *testing.T) {
	input := DiaryWrite{
		ActorID: "mio", Date: "2026-11-13", Revision: 1,
		Body: strings.Repeat("x", 80<<10), Reflection: "brief reflection",
	}
	client, _ := newMioDiaryClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) <= maxRequestBodyBytes || len(body) > maxDiaryJSONBytes {
			t.Errorf("request JSON size = %d, err = %v; want > %d and <= %d", len(body), err, maxRequestBodyBytes, maxDiaryJSONBytes)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Correlation-ID", syntheticCorrelation)
		response := diaryWriteResponse{
			Entry: diaryEntryResponse{
				ActorID: input.ActorID, Date: input.Date, Revision: input.Revision,
				Body: input.Body, Reflection: input.Reflection,
				UpdatedAt: time.Date(2026, 11, 13, 14, 15, 0, 0, time.UTC),
			},
			CorrelationID: syntheticCorrelation,
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	entry, err := client.PutDiary(context.Background(), input)
	if err != nil {
		t.Fatalf("PutDiary with 80 KiB body: %v", err)
	}
	if entry.Body != input.Body {
		t.Fatal("PutDiary did not return the exact written body")
	}
}

func TestPutDiaryChecksExactResponseAndMapsConflict(t *testing.T) {
	input := DiaryWrite{ActorID: "mio", Date: "2026-11-13", Revision: 2, Body: "body", Reflection: "reflection"}
	tests := []struct {
		name   string
		status int
		body   string
		header string
		want   error
	}{
		{name: "entry mismatch", status: http.StatusOK, body: `{"entry":{"actor_id":"shiro","date":"2026-11-13","revision":2,"body":"body","reflection":"reflection","updated_at":"2026-11-13T14:15:00Z"},"correlation_id":"` + syntheticCorrelation + `"}`, header: syntheticCorrelation, want: ErrUnknownOutcome},
		{name: "missing correlation header", status: http.StatusOK, body: `{"entry":{"actor_id":"mio","date":"2026-11-13","revision":2,"body":"body","reflection":"reflection","updated_at":"2026-11-13T14:15:00Z"},"correlation_id":"` + syntheticCorrelation + `"}`, want: ErrUnknownOutcome},
		{name: "conflict", status: http.StatusConflict, body: `{"error":"revision_conflict","correlation_id":"` + syntheticCorrelation + `"}`, header: syntheticCorrelation, want: ErrConflict},
		{name: "pinned identity changed", status: http.StatusConflict, body: `{"error":"connection_identity_mismatch","correlation_id":"` + syntheticCorrelation + `"}`, header: syntheticCorrelation, want: ErrConnectionIdentityMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := newMioDiaryClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.header != "" {
					w.Header().Set("X-Correlation-ID", tt.header)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			_, err := client.PutDiary(context.Background(), input)
			requireCategory(t, err, tt.want)
			if strings.Contains(err.Error(), "revision_conflict") {
				t.Fatal("PutDiary exposed BOARD error text")
			}
		})
	}
}
