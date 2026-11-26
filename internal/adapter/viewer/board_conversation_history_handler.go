package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

const BoardConversationHistoryPath = "/viewer/board/conversation-history"

type BoardConversationTurn struct {
	ActorID string `json:"actor_id"`
	Text    string `json:"text"`
}

type BoardConversation struct {
	ID    string                  `json:"id"`
	At    time.Time               `json:"at"`
	Turns []BoardConversationTurn `json:"turns"`
}

type BoardConversationHistoryReader interface {
	ReadBoardConversationHistory(context.Context, string) ([]BoardConversation, error)
}

// NewBoardConversationHistoryHandler exposes private routine transcripts only to
// the configured owner via the existing BOARD loopback read profile.
func NewBoardConversationHistoryHandler(reader BoardConversationHistoryReader, token []byte) http.HandlerFunc {
	token = append([]byte(nil), token...)
	return func(w http.ResponseWriter, r *http.Request) {
		if !memoryOwnerDirectLocalRequest(r) || r.URL == nil || r.URL.Path != BoardConversationHistoryPath {
			writeMemoryOwnerError(w, 404, "not_found")
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeMemoryOwnerError(w, 405, "method_not_allowed")
			return
		}
		if len(token) == 0 {
			writeMemoryOwnerError(w, 503, "owner_auth_unavailable")
			return
		}
		if !memoryOwnerBearerAuthorized(r, token) {
			writeMemoryOwnerError(w, 401, "unauthorized")
			return
		}
		if !boardStudioOwnerProfileAllowed(r) {
			writeMemoryOwnerError(w, 403, "forbidden")
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		dates := query["date"]
		if err != nil || len(query) != 1 || len(dates) != 1 {
			writeMemoryOwnerError(w, 400, "invalid_date")
			return
		}
		date, err := time.Parse("2006-01-02", dates[0])
		if err != nil || date.Format("2006-01-02") != dates[0] {
			writeMemoryOwnerError(w, 400, "invalid_date")
			return
		}
		if reader == nil {
			writeMemoryOwnerError(w, 503, "conversation_history_unavailable")
			return
		}
		items, err := reader.ReadBoardConversationHistory(r.Context(), dates[0])
		if err != nil {
			writeMemoryOwnerError(w, 503, "conversation_history_unavailable")
			return
		}
		if items == nil {
			items = []BoardConversation{}
		}
		body, err := json.Marshal(struct {
			Items []BoardConversation `json:"items"`
		}{Items: items})
		// BOARD's existing proxy accepts at most 2 MiB. Never return a truncated day
		// as a complete empty/successful history.
		if err != nil || len(body) > 2<<20 {
			writeMemoryOwnerError(w, 503, "conversation_history_unavailable")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(body)
	}
}
