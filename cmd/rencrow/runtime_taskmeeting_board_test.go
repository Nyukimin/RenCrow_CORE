package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	workstreampersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type meetingBoardStoredSend struct {
	request boardcommunication.SendRequest
	receipt boardcommunication.SendReceipt
}

type meetingBoardTestClient struct {
	rows        []meetingBoardStoredSend
	lostAt      int
	lost        bool
	unavailable bool
	forge       bool
}

func (c *meetingBoardTestClient) PrepareSend(request boardcommunication.SendRequest) (boardcommunication.PreparedSend, error) {
	return (&boardcommunication.Client{}).PrepareSend(request)
}

func (c *meetingBoardTestClient) SendPrepared(_ context.Context, prepared boardcommunication.PreparedSend) (boardcommunication.SendReceipt, error) {
	if c.unavailable {
		return boardcommunication.SendReceipt{}, boardcommunication.ErrUnavailable
	}
	request, err := prepared.Request()
	if err != nil {
		return boardcommunication.SendReceipt{}, err
	}
	for _, row := range c.rows {
		if row.request.IdempotencyKey == request.IdempotencyKey && row.request.ThreadID == request.ThreadID {
			if row.request.Body != request.Body || row.request.Title != request.Title || row.request.ChannelID != request.ChannelID || !sameTaskMeetingBoardRecipients(row.request.Recipients, request.Recipients) {
				return boardcommunication.SendReceipt{}, errors.New("idempotency_conflict")
			}
			return row.receipt, nil
		}
	}
	threadID := request.ThreadID
	if threadID == "" {
		threadID = fmt.Sprintf("thread-%d", len(c.rows)+1)
	}
	receipt := boardcommunication.SendReceipt{ThreadID: threadID, MessageID: fmt.Sprintf("message-%d", len(c.rows)+1), Status: "stored", StoredAt: time.Now().UTC()}
	c.rows = append(c.rows, meetingBoardStoredSend{request: request, receipt: receipt})
	if !c.lost && c.lostAt == len(c.rows) {
		c.lost = true
		return boardcommunication.SendReceipt{}, boardcommunication.ErrUnknownOutcome
	}
	return receipt, nil
}

func (c *meetingBoardTestClient) ReadThread(_ context.Context, threadID string, page boardcommunication.PageRequest) (boardcommunication.ThreadPage, error) {
	result := boardcommunication.ThreadPage{Thread: boardcommunication.BoardThread{ID: threadID, Space: "rencrow"}}
	started := page.Cursor == ""
	for _, row := range c.rows {
		if row.receipt.ThreadID != threadID {
			continue
		}
		if row.request.Title != "" {
			result.Thread.Title = row.request.Title
		}
		if !started {
			if row.receipt.MessageID == page.Cursor {
				started = true
			}
			continue
		}
		// Deliberately return one row per page to exercise canonical readback.
		if len(result.Thread.Messages) > 0 {
			result.NextCursor = result.Thread.Messages[0].ID
			break
		}
		body := row.request.Body
		if c.forge {
			body = "forged"
		}
		result.Thread.Messages = append(result.Thread.Messages, boardcommunication.BoardMessage{ID: row.receipt.MessageID, Seq: int64(indexMeetingBoardRow(c.rows, row.receipt.MessageID) + 1), By: "rencrow-comm", Body: body,
			ChannelID: row.request.ChannelID, ThreadID: threadID, Recipients: row.request.Recipients})
	}
	return result, nil
}

func indexMeetingBoardRow(rows []meetingBoardStoredSend, messageID string) int {
	for index, row := range rows {
		if row.receipt.MessageID == messageID {
			return index
		}
	}
	return -1
}

func meetingBoardTranscript() workstreampersistence.TaskMeetingMinutesTranscript {
	transcript := workstreampersistence.TaskMeetingMinutesTranscript{OwnerUserID: "ren", Outcome: domainmeeting.StatusCompleted,
		OccurrenceKey: domainmeeting.OccurrenceKey{ScheduleID: modulecore.NewScheduleID(), LocalDate: "2026-11-19", Slot: "08:00", Timezone: "Asia/Tokyo"},
		Audience:      []string{"mio", "shiro", "kuro", "midori", "aka", "ao", "kin", "gin"}}
	for _, actor := range transcript.Audience {
		id := modulecore.NewMessageID()
		transcript.Reports = append(transcript.Reports, workstreampersistence.TaskMeetingMinutesReport{MessageID: id, ActorID: actor,
			Body: actor + "発言" + strings.Repeat("あ🙂\n", 1500), Current: domainmeeting.ReportField{State: "known", Text: actor + "の現状"}, Next: domainmeeting.ReportField{State: "known", Text: actor + "の次の仕事"}})
		transcript.MessageOrder = append(transcript.MessageOrder, id)
	}
	id := modulecore.NewMessageID()
	transcript.Answers = []workstreampersistence.TaskMeetingMinutesAnswer{{MessageID: id, ActorID: "shiro", ReporterID: "mio", Body: "回答は省略しない"}}
	transcript.MessageOrder = append(transcript.MessageOrder, id)
	return transcript
}

func TestTaskMeetingBoardOnePageIncludesAllUtterancesAndUnicode(t *testing.T) {
	transcript := meetingBoardTranscript()
	route := domainmeeting.BoardPublication{ChannelID: "rencrow", SenderPrincipalID: "rencrow-comm", Recipients: []string{}}
	client := &meetingBoardTestClient{}
	if err := (runtimeTaskMeetingBoardPublisher{client}).Publish(context.Background(), route, transcript); err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	threadID := client.rows[0].receipt.ThreadID
	for index, row := range client.rows {
		if row.receipt.ThreadID != threadID || utf8.RuneCountInString(row.request.Body) > 8000 || !utf8.ValidString(row.request.Body) || len(row.request.Recipients) != 0 || len(row.request.Mentions) != 0 || row.request.Kind != "" {
			t.Fatalf("chunk %d did not stay on the same quiet BOARD page", index)
		}
		if index == 0 {
			if utf8.RuneCountInString(row.request.Body) > 600 || !strings.Contains(row.request.Body, "報告: 8/8人") || !strings.Contains(row.request.Body, "悩みへの回答: 1件") {
				t.Fatal("root BOARD topic summary must be brief and describe the whole meeting")
			}
			continue
		}
		_, content, found := strings.Cut(row.request.Body, "\n")
		if !found || !strings.HasPrefix(row.request.Body, fmt.Sprintf("報告会の記録（%d/%d）\n", index, len(client.rows)-1)) {
			t.Fatalf("chunk %d has no stable part count heading", index)
		}
		all.WriteString(content)
	}
	if len(client.rows) < 2 {
		t.Fatal("long transcript must be chunked")
	}
	for _, report := range transcript.Reports {
		if !strings.Contains(all.String(), report.Body) || !strings.Contains(all.String(), report.Current.Text) || !strings.Contains(all.String(), report.Next.Text) {
			t.Fatalf("missing complete report for %s", report.ActorID)
		}
	}
	if !strings.Contains(all.String(), transcript.Answers[0].Body) {
		t.Fatal("missing concern answer")
	}
}

func TestTaskMeetingBoardWhitespaceSegmentsRemainCompleteAndLegal(t *testing.T) {
	transcript := meetingBoardTranscript()
	transcript.Reports = transcript.Reports[:1]
	transcript.Reports[0].Body = "開始" + strings.Repeat(" ", 24000) + "末尾"
	transcript.MessageOrder = []modulecore.MessageID{transcript.Reports[0].MessageID}
	transcript.Answers = nil
	_, chunks, err := renderTaskMeetingBoardMinutes(transcript)
	if err != nil {
		t.Fatal(err)
	}
	var source strings.Builder
	for _, chunk := range chunks[1:] {
		if strings.TrimSpace(chunk) == "" || utf8.RuneCountInString(chunk) > 8000 {
			t.Fatal("BOARD would reject the deterministic whitespace segment")
		}
		_, content, _ := strings.Cut(chunk, "\n")
		source.WriteString(content)
	}
	if !strings.Contains(source.String(), transcript.Reports[0].Body) {
		t.Fatal("part headings changed or trimmed original whitespace")
	}
}

func TestTaskMeetingBoardPartialLabelsUnsharedAndMissingReports(t *testing.T) {
	transcript := meetingBoardTranscript()
	transcript.Outcome = domainmeeting.StatusPartial
	transcript.Reports = transcript.Reports[:1]
	transcript.Reports[0].Body = "生成済みだが共有結果は不明"
	transcript.Reports[0].ConcernDeclaration = domainmeeting.ConcernsNotDisclosed
	transcript.MessageOrder = []modulecore.MessageID{transcript.Reports[0].MessageID}
	transcript.Answers = nil
	transcript.Messages = []workstreampersistence.TaskMeetingMinutesMessageProof{{MessageID: transcript.Reports[0].MessageID, DeliveryState: domainmeeting.DeliveryUnknown}}
	_, chunks, err := renderTaskMeetingBoardMinutes(transcript)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(chunks, "")
	for _, expected := range []string{"報告会の結果: 一部未完了", "共有状態: 共有結果不明", "悩み: 未共有", "【shiro】\n報告は記録されていません。", transcript.Reports[0].Body} {
		if !strings.Contains(text, expected) {
			t.Fatalf("partial minutes omit honest state %q", expected)
		}
	}
	if strings.Contains(text, "全員に共有済み") {
		t.Fatal("unknown generation was relabeled as shared")
	}
}

func TestTaskMeetingBoardReplayAfterLostRootOrChunkAndUnavailable(t *testing.T) {
	for _, lostAt := range []int{1, 3} {
		t.Run(fmt.Sprint(lostAt), func(t *testing.T) {
			transcript := meetingBoardTranscript()
			route := domainmeeting.BoardPublication{ChannelID: "rencrow", SenderPrincipalID: "rencrow-comm", Recipients: []string{}}
			client := &meetingBoardTestClient{lostAt: lostAt}
			if err := (runtimeTaskMeetingBoardPublisher{client}).Publish(context.Background(), route, transcript); !errors.Is(err, boardcommunication.ErrUnknownOutcome) {
				t.Fatalf("lost response must stay pending: %v", err)
			}
			if err := (runtimeTaskMeetingBoardPublisher{client}).Publish(context.Background(), route, transcript); err != nil {
				t.Fatal(err)
			}
			count := len(client.rows)
			client.unavailable = true
			if err := (runtimeTaskMeetingBoardPublisher{client}).Publish(context.Background(), route, transcript); !errors.Is(err, boardcommunication.ErrUnavailable) {
				t.Fatalf("unavailable must stay pending: %v", err)
			}
			client.unavailable = false
			if err := (runtimeTaskMeetingBoardPublisher{client}).Publish(context.Background(), route, transcript); err != nil || len(client.rows) != count {
				t.Fatalf("recovery duplicated a page/message: %v, %d != %d", err, len(client.rows), count)
			}
			transcript.Reports[0].Body = "changed"
			if err := (runtimeTaskMeetingBoardPublisher{client}).Publish(context.Background(), route, transcript); err == nil || len(client.rows) != count {
				t.Fatalf("same occurrence changed content must conflict without creating a page: %v", err)
			}
		})
	}
}

func TestTaskMeetingBoardDistinctOccurrencesAndCanonicalReadback(t *testing.T) {
	transcript := meetingBoardTranscript()
	route := domainmeeting.BoardPublication{ChannelID: "rencrow", SenderPrincipalID: "rencrow-comm", Recipients: []string{}}
	client := &meetingBoardTestClient{}
	if err := (runtimeTaskMeetingBoardPublisher{client}).Publish(context.Background(), route, transcript); err != nil {
		t.Fatal(err)
	}
	firstThread := client.rows[0].receipt.ThreadID
	count := len(client.rows)
	transcript.OccurrenceKey.Slot = "12:00"
	if err := (runtimeTaskMeetingBoardPublisher{client}).Publish(context.Background(), route, transcript); err != nil || client.rows[count].receipt.ThreadID == firstThread {
		t.Fatalf("new occurrence must get exactly its own page: %v", err)
	}
	client.forge = true
	if err := (runtimeTaskMeetingBoardPublisher{client}).Publish(context.Background(), route, transcript); !errors.Is(err, boardcommunication.ErrProtocol) {
		t.Fatalf("forged readback cannot complete publication: %v", err)
	}
}
