package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
	meetingapp "github.com/Nyukimin/RenCrow_CORE/internal/application/taskmeeting"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	workstreampersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
)

// The immutable Workstream transcript remains the source. BOARD owns its
// derived topic and permanent idempotency receipts; no second outbox is needed.
type runtimeTaskMeetingMinutesPublisher interface {
	Publish(context.Context, domainmeeting.BoardPublication, workstreampersistence.TaskMeetingMinutesTranscript) error
}

type taskMeetingBoardClient interface {
	PrepareSend(boardcommunication.SendRequest) (boardcommunication.PreparedSend, error)
	SendPrepared(context.Context, boardcommunication.PreparedSend) (boardcommunication.SendReceipt, error)
	ReadThread(context.Context, string, boardcommunication.PageRequest) (boardcommunication.ThreadPage, error)
}

type runtimeTaskMeetingBoardPublisher struct{ client taskMeetingBoardClient }

func (e *runtimeTaskMeetingOccurrenceExecutor) publishTaskMeetingMinutes(ctx context.Context, snapshot runtimeTaskMeetingOccurrenceSnapshot, meeting domainmeeting.Meeting) error {
	route := snapshot.Declaration.BoardPublication
	if route == nil {
		return nil
	}
	if err := route.Validate(); err != nil {
		return err
	}
	if e.deps == nil || e.deps.taskCenterUserID != snapshot.Declaration.OwnerUserID || meeting.Key != snapshot.Key {
		return eventstore.ErrTaskMeetingForbidden
	}
	store, ok := e.deps.workstreamStore.(*workstreampersistence.SQLiteStore)
	if !ok || store == nil {
		return meetingapp.ErrUnavailable
	}
	ref, found, err := store.FindTerminalTaskMeetingMinutes(ctx, snapshot.Declaration.OwnerUserID, meeting.Key, meeting.TranscriptRequestID, meeting.Guard(), func(authCtx context.Context, owner string, key domainmeeting.OccurrenceKey, requested string, guard domainmeeting.Guard, actual *workstreampersistence.TaskMeetingMinutesReference) error {
		if owner != snapshot.Declaration.OwnerUserID || key != snapshot.Key || requested != meeting.TranscriptRequestID || guard != meeting.Guard() {
			return eventstore.ErrTaskMeetingForbidden
		}
		if actual != nil && (actual.ConfigSHA256 != snapshot.Registration.ConfigSHA256 || actual.RegistrationEvidenceID != snapshot.Registration.RegistrationEvidenceID || !sameRuntimeStringSlice(actual.Audience, snapshot.Declaration.Audience)) {
			return eventstore.ErrTaskMeetingForbidden
		}
		return authCtx.Err()
	})
	if err != nil || !found {
		return fmt.Errorf("meeting BOARD minutes unavailable: %w", errors.Join(err, meetingapp.ErrUnavailable))
	}
	artifact, err := store.ReadTaskMeetingMinutes(ctx, ref, func(authCtx context.Context, actual workstreampersistence.TaskMeetingMinutesReference) error {
		if actual.OwnerUserID != snapshot.Declaration.OwnerUserID || actual.OccurrenceKey != snapshot.Key.String() ||
			actual.ConfigSHA256 != snapshot.Registration.ConfigSHA256 || actual.RegistrationEvidenceID != snapshot.Registration.RegistrationEvidenceID ||
			!sameRuntimeStringSlice(actual.Audience, snapshot.Declaration.Audience) {
			return eventstore.ErrTaskMeetingForbidden
		}
		return authCtx.Err()
	})
	if err != nil {
		return err
	}
	publisher := e.minutesPublisher
	if publisher == nil {
		boot := e.deps.boardCommunicationActorBoots[snapshot.Declaration.CoordinatorActorID]
		composition := boot.currentComposition()
		if composition == nil || composition.boardClient == nil || composition.deps.Pin.OwnerID != snapshot.Declaration.OwnerUserID ||
			composition.deps.Pin.PrincipalID != route.SenderPrincipalID || !containsTaskMeetingBoardChannel(composition.deps.AllowedChannelIDs, route.ChannelID) {
			return fmt.Errorf("meeting BOARD publication is pending: %w", meetingapp.ErrUnavailable)
		}
		publisher = runtimeTaskMeetingBoardPublisher{client: composition.boardClient}
	}
	if err := publisher.Publish(ctx, *route, artifact.Transcript); err != nil {
		return fmt.Errorf("publish meeting minutes to BOARD: %w", err)
	}
	return nil
}

func containsTaskMeetingBoardChannel(channels []string, requested string) bool {
	for _, channel := range channels {
		if channel == requested {
			return true
		}
	}
	return false
}

func (p runtimeTaskMeetingBoardPublisher) Publish(ctx context.Context, route domainmeeting.BoardPublication, transcript workstreampersistence.TaskMeetingMinutesTranscript) error {
	if ctx == nil || p.client == nil || route.Validate() != nil || transcript.OccurrenceKey.Validate() != nil || transcript.OwnerUserID == "" {
		return meetingapp.ErrUnavailable
	}
	title, chunks, err := renderTaskMeetingBoardMinutes(transcript)
	if err != nil {
		return err
	}
	stored := make(map[string]string, len(chunks))
	orderedIDs := make([]string, 0, len(chunks))
	threadID := ""
	for index, body := range chunks {
		request := boardcommunication.SendRequest{ChannelID: route.ChannelID, ThreadID: threadID, Body: body,
			Recipients: append([]string{}, route.Recipients...), IdempotencyKey: taskMeetingBoardMessageKey(transcript.OwnerUserID, transcript.OccurrenceKey, index)}
		if index == 0 {
			request.Title = title
		}
		prepared, err := p.client.PrepareSend(request)
		if err != nil {
			return err
		}
		receipt, err := p.client.SendPrepared(ctx, prepared)
		if err != nil {
			// BOARD commits the key and exact payload atomically. A later
			// checkpoint replays these same bytes, including after response loss.
			return err
		}
		if threadID == "" {
			threadID = receipt.ThreadID
		}
		if receipt.ThreadID != threadID || receipt.MessageID == "" || receipt.Status != "stored" {
			return boardcommunication.ErrProtocol
		}
		if _, duplicate := stored[receipt.MessageID]; duplicate {
			return boardcommunication.ErrProtocol
		}
		stored[receipt.MessageID] = body
		orderedIDs = append(orderedIDs, receipt.MessageID)
	}
	// Verify the owner-stored page rather than treating a POST response as
	// publication. Pagination stays bounded by the immutable source size.
	cursor := ""
	seenCursors := map[string]bool{}
	verified := 0
	var lastSequence int64
	for pages := 0; pages <= len(chunks); pages++ {
		page, err := p.client.ReadThread(ctx, threadID, boardcommunication.PageRequest{Limit: 10, Cursor: cursor})
		if err != nil {
			return err
		}
		if page.Thread.ID != threadID || page.Thread.Space != route.ChannelID || page.Thread.Title != title {
			return boardcommunication.ErrProtocol
		}
		for _, message := range page.Thread.Messages {
			body, expected := stored[message.ID]
			if !expected {
				continue
			}
			if verified >= len(orderedIDs) || message.ID != orderedIDs[verified] || message.Seq <= lastSequence {
				return boardcommunication.ErrProtocol
			}
			if message.Body != body || message.By != route.SenderPrincipalID || message.ChannelID != route.ChannelID || message.ThreadID != threadID ||
				message.Kind != "" || message.ReplyTo != "" || len(message.Mentions) != 0 || !sameTaskMeetingBoardRecipients(message.Recipients, route.Recipients) {
				return boardcommunication.ErrProtocol
			}
			delete(stored, message.ID)
			verified++
			lastSequence = message.Seq
		}
		if len(stored) == 0 {
			return nil
		}
		if page.NextCursor == "" {
			return boardcommunication.ErrProtocol
		}
		if page.NextCursor == cursor || seenCursors[page.NextCursor] {
			return boardcommunication.ErrProtocol
		}
		seenCursors[page.NextCursor] = true
		cursor = page.NextCursor
	}
	return boardcommunication.ErrProtocol
}

func sameTaskMeetingBoardRecipients(left, right []string) bool {
	a, b := append([]string{}, left...), append([]string{}, right...)
	sort.Strings(a)
	sort.Strings(b)
	return sameRuntimeStringSlice(a, b)
}

func taskMeetingBoardMessageKey(owner string, key domainmeeting.OccurrenceKey, index int) string {
	// Route/content changes deliberately do not produce another root key:
	// BOARD must reject a conflicting payload for the same occurrence.
	digest := sha256.Sum256([]byte(fmt.Sprintf("taskmeeting.board.v1\x00%s\x00%s\x00%d", owner, key.String(), index)))
	return "taskmeeting-minutes-" + hex.EncodeToString(digest[:])
}

func renderTaskMeetingBoardMinutes(transcript workstreampersistence.TaskMeetingMinutesTranscript) (string, []string, error) {
	title := fmt.Sprintf("報告会 %s %s", transcript.OccurrenceKey.LocalDate, transcript.OccurrenceKey.Slot)
	var body strings.Builder
	fmt.Fprintf(&body, "%s（%s）\n報告会の結果: %s\n参加予定: %s\n", title, transcript.OccurrenceKey.Timezone, meetingBoardOutcome(transcript.Outcome), strings.Join(transcript.Audience, "、"))
	if transcript.ReasonCode != "" {
		fmt.Fprintf(&body, "理由: %s\n", transcript.ReasonCode)
	}
	if len(transcript.Unresolved) > 0 {
		fmt.Fprintf(&body, "未解決: %s\n", strings.Join(transcript.Unresolved, "、"))
	}
	utterances := make(map[string]string, len(transcript.Reports)+len(transcript.Answers))
	delivery := make(map[string]string, len(transcript.Messages))
	for _, proof := range transcript.Messages {
		delivery[string(proof.MessageID)] = proof.DeliveryState
	}
	reported := make(map[string]bool, len(transcript.Reports))
	for _, report := range transcript.Reports {
		reported[report.ActorID] = true
		var section strings.Builder
		fmt.Fprintf(&section, "\n【%s：現状・次にやること・悩み】\n共有状態: %s\n%s\n", report.ActorID, meetingBoardDelivery(delivery[string(report.MessageID)]), report.Body)
		fmt.Fprintf(&section, "現状: %s\n次にやること: %s\n", meetingBoardReportField(report.Current), meetingBoardReportField(report.Next))
		if report.ConcernDeclaration == domainmeeting.ConcernsNone {
			section.WriteString("悩み: なし\n")
		} else if report.ConcernDeclaration == domainmeeting.ConcernsNotDisclosed {
			section.WriteString("悩み: 未共有\n")
		}
		for _, concern := range report.Concerns {
			fmt.Fprintf(&section, "悩み: %s\n", concern.Description)
		}
		utterances[string(report.MessageID)] = section.String()
	}
	for _, answer := range transcript.Answers {
		utterances[string(answer.MessageID)] = fmt.Sprintf("\n【%s → %s の悩みへの回答】\n共有状態: %s\n%s\n", answer.ActorID, answer.ReporterID, meetingBoardDelivery(delivery[string(answer.MessageID)]), answer.Body)
	}
	for _, id := range transcript.MessageOrder {
		section, found := utterances[string(id)]
		if !found {
			return "", nil, boardcommunication.ErrInvalidRequest
		}
		body.WriteString(section)
		delete(utterances, string(id))
	}
	if len(utterances) != 0 || !utf8.ValidString(body.String()) {
		return "", nil, boardcommunication.ErrInvalidRequest
	}
	for _, actorID := range transcript.Audience {
		if !reported[actorID] {
			fmt.Fprintf(&body, "\n【%s】\n報告は記録されていません。\n", actorID)
		}
	}
	body.WriteString("\n以上、報告会の記録。\n")
	runes := []rune(body.String())
	const contentRunesPerPart = 7900
	partCount := (len(runes) + contentRunesPerPart - 1) / contentRunesPerPart
	// BOARD also uses the root body as the topic summary. Keep that summary
	// brief, and put every full utterance in the following ordered messages.
	roster := strings.Join(transcript.Audience, "、")
	if utf8.RuneCountInString(roster) > 200 {
		roster = fmt.Sprintf("%d人（全員の一覧は記録本文に掲載）", len(transcript.Audience))
	}
	missing := 0
	for _, actorID := range transcript.Audience {
		if !reported[actorID] {
			missing++
		}
	}
	summary := fmt.Sprintf("%s（%s）\n報告会の結果: %s\n参加予定: %s\n報告: %d/%d人、悩みへの回答: %d件、未報告: %d人\n発言・回答の全文を、このページ内に順番に掲載します。", title, transcript.OccurrenceKey.Timezone, meetingBoardOutcome(transcript.Outcome), roster, len(reported), len(transcript.Audience), len(transcript.Answers), missing)
	chunks := make([]string, 1, partCount+1)
	chunks[0] = summary
	for len(runes) > 0 {
		length := min(contentRunesPerPart, len(runes))
		// Every part has an ordinary heading, so even a source segment made
		// entirely of whitespace is a valid BOARD body. The source is never
		// trimmed; ordered part contents reconstruct the complete transcript.
		chunks = append(chunks, fmt.Sprintf("報告会の記録（%d/%d）\n%s", len(chunks), partCount, string(runes[:length])))
		runes = runes[length:]
	}
	return title, chunks, nil
}

func meetingBoardOutcome(outcome string) string {
	switch outcome {
	case domainmeeting.StatusCompleted:
		return "完了"
	case domainmeeting.StatusPartial:
		return "一部未完了"
	case domainmeeting.StatusMissed:
		return "未開催"
	default:
		return outcome
	}
}

func meetingBoardDelivery(state string) string {
	switch state {
	case domainmeeting.DeliveryDelivered:
		return "全員に共有済み"
	case domainmeeting.DeliveryUnsent:
		return "未送信（生成された発言）"
	case domainmeeting.DeliverySending:
		return "送信中（全員への共有は未確認）"
	case domainmeeting.DeliveryUndelivered:
		return "共有未完了"
	default:
		return "共有結果不明"
	}
}

func meetingBoardReportField(field domainmeeting.ReportField) string {
	if field.Text != "" {
		return field.Text
	}
	return field.State
}
