package boardcommunication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

const (
	maxDiaryTextBytes = 128 << 10
	maxDiaryJSONBytes = 800 << 10
)

// DiaryWrite is the CORE actor/date scope and exact revision payload for a
// BOARD diary write. The actor is checked against the client's pinned principal.
type DiaryWrite struct {
	ActorID    string `json:"actor_id"`
	Date       string `json:"date"`
	Revision   int64  `json:"revision"`
	Body       string `json:"body"`
	Reflection string `json:"reflection"`
}

// DiaryEntry is the canonical BOARD diary row returned after a write.
type DiaryEntry struct {
	DiaryWrite
	UpdatedAt     time.Time `json:"updated_at"`
	CorrelationID string    `json:"-"`
}

type diaryWriteRequest struct {
	Revision   int64  `json:"revision"`
	Body       string `json:"body"`
	Reflection string `json:"reflection"`
}

type diaryEntryResponse struct {
	ActorID    string    `json:"actor_id"`
	Date       string    `json:"date"`
	Revision   int64     `json:"revision"`
	Body       string    `json:"body"`
	Reflection string    `json:"reflection"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type diaryWriteResponse struct {
	Entry         diaryEntryResponse `json:"entry"`
	CorrelationID string             `json:"correlation_id"`
}

type diaryErrorResponse struct {
	Error         string `json:"error"`
	CorrelationID string `json:"correlation_id"`
}

// PutDiary writes exactly one actor/date revision through the authenticated,
// connection-pinned BOARD client and returns only the verified stored entry.
func (c *Client) PutDiary(ctx context.Context, input DiaryWrite) (DiaryEntry, error) {
	if c == nil || ctx == nil || !validDiaryDate(input.Date) || input.Revision < 1 ||
		strings.TrimSpace(input.Body) == "" || strings.TrimSpace(input.Reflection) == "" ||
		!utf8.ValidString(input.Body) || !utf8.ValidString(input.Reflection) ||
		len(input.Body)+len(input.Reflection) > maxDiaryTextBytes {
		return DiaryEntry{}, ErrInvalidRequest
	}
	c.mu.RLock()
	pinned := c.expectedConnection
	c.mu.RUnlock()
	actorID, ok := domainconversation.BoardCommunicationMeetingInternalActorID(pinned.PrincipalID)
	if !ok || actorID != input.ActorID {
		return DiaryEntry{}, ErrInvalidRequest
	}
	requestBody, err := json.Marshal(diaryWriteRequest{
		Revision: input.Revision, Body: input.Body, Reflection: input.Reflection,
	})
	if err != nil || len(requestBody) > maxDiaryJSONBytes {
		return DiaryEntry{}, ErrInvalidRequest
	}
	response, release, err := c.do(ctx, http.MethodPut, "/api/v1/diaries/"+url.PathEscape(input.Date), requestBody, "")
	if err != nil {
		return DiaryEntry{}, err
	}
	defer release()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, readErr := readBounded(response.Body, maxResponseBodyBytes)
		if readErr != nil {
			return DiaryEntry{}, ErrUnknownOutcome
		}
		return DiaryEntry{}, diaryWriteResponseError(response.StatusCode, body, response.Header)
	}
	body, err := readBounded(response.Body, maxResponseBodyBytes)
	if err != nil {
		return DiaryEntry{}, ErrUnknownOutcome
	}
	var wire diaryWriteResponse
	if decodeStrictJSON(body, &wire) != nil || !validResponseCorrelation(wire.CorrelationID) ||
		response.Header.Get("X-Correlation-ID") != wire.CorrelationID || wire.Entry.ActorID != input.ActorID ||
		wire.Entry.Date != input.Date || wire.Entry.Revision != input.Revision || wire.Entry.Body != input.Body ||
		wire.Entry.Reflection != input.Reflection || wire.Entry.UpdatedAt.IsZero() {
		return DiaryEntry{}, ErrUnknownOutcome
	}
	return DiaryEntry{
		DiaryWrite:    input,
		UpdatedAt:     wire.Entry.UpdatedAt,
		CorrelationID: wire.CorrelationID,
	}, nil
}

func validDiaryDate(value string) bool {
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Format("2006-01-02") == value
}

func diaryWriteResponseError(status int, body []byte, header http.Header) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return ErrAuthentication
	}
	if status >= 500 && status <= 599 {
		return ErrUnavailable
	}
	var wire diaryErrorResponse
	if decodeStrictJSON(body, &wire) != nil || wire.Error == "" || !validResponseCorrelation(wire.CorrelationID) ||
		header.Get("X-Correlation-ID") != wire.CorrelationID {
		return ErrUnknownOutcome
	}
	if status == http.StatusConflict {
		if wire.Error == "connection_identity_mismatch" {
			return ErrConnectionIdentityMismatch
		}
		return ErrConflict
	}
	return ErrUnknownOutcome
}
