package gmailintake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	appbacklog "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// memoryItemStore is a local fixture for the Gmail owner tests. The production
// intake uses the public backlog Service API and never reaches this fixture.
type memoryItemStore struct{ items []domainbacklog.Item }

func (s *memoryItemStore) List(_ context.Context, _ int) ([]domainbacklog.Item, error) {
	return append([]domainbacklog.Item(nil), s.items...), nil
}

func (s *memoryItemStore) Save(_ context.Context, item domainbacklog.Item) error {
	for index := range s.items {
		if s.items[index].BacklogItemID == item.BacklogItemID {
			s.items[index] = item
			return nil
		}
	}
	s.items = append(s.items, item)
	return nil
}

type gmailIntakeCollectorStub struct {
	page  GmailPage
	err   error
	calls int
}

func (s *gmailIntakeCollectorStub) Collect(_ context.Context, _ string) (GmailPage, error) {
	s.calls++
	if s.err != nil {
		return GmailPage{}, s.err
	}
	return s.page, nil
}

type gmailIntakeEvaluatorStub struct {
	evaluation GmailBriefEvaluation
	err        error
	calls      int
}

func (s *gmailIntakeEvaluatorStub) EvaluateBrief(_ context.Context, _ GmailMessage, _ []string) (GmailBriefEvaluation, error) {
	s.calls++
	return s.evaluation, s.err
}

type gmailIntakeReceiptMemory struct {
	mu          sync.Mutex
	receipts    map[string]GmailReceipt
	cursor      string
	cursorCalls int
	cursorSaves int
	listCalls   int
	saveCalls   int
	failSaveAt  int
}

func newGmailIntakeReceiptMemory() *gmailIntakeReceiptMemory {
	return &gmailIntakeReceiptMemory{receipts: make(map[string]GmailReceipt)}
}

func gmailReceiptMemoryKey(account, id string) string {
	return strings.ToLower(strings.TrimSpace(account)) + "\x00" + strings.TrimSpace(id)
}

func (s *gmailIntakeReceiptMemory) Get(_ context.Context, account, id string) (GmailReceipt, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	receipt, ok := s.receipts[gmailReceiptMemoryKey(account, id)]
	return receipt, ok, nil
}

func (s *gmailIntakeReceiptMemory) Save(_ context.Context, receipt GmailReceipt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveCalls++
	if s.failSaveAt != 0 && s.saveCalls == s.failSaveAt {
		return errors.New("synthetic receipt save failure")
	}
	s.receipts[gmailReceiptMemoryKey(receipt.Account, receipt.Message.ID)] = receipt
	return nil
}

func (s *gmailIntakeReceiptMemory) List(_ context.Context, account string, limit int) ([]GmailReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listCalls++
	result := make([]GmailReceipt, 0, len(s.receipts))
	for _, receipt := range s.receipts {
		if strings.EqualFold(receipt.Account, account) {
			result = append(result, receipt)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt > result[j].UpdatedAt })
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *gmailIntakeReceiptMemory) Cursor(_ context.Context, _, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursorCalls++
	return s.cursor, nil
}

func (s *gmailIntakeReceiptMemory) SaveCursor(_ context.Context, _, _, cursor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursorSaves++
	s.cursor = cursor
	return nil
}

func gmailIntakeAgentContext(t *testing.T, userID string) context.Context {
	t.Helper()
	ctx, err := domainexecution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	scope, err := domaintool.NewToolExecutionScope(string(modulecore.NewRequestID()), domaintool.ActorKindAgent, "shiro", userID, []string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	return domaintool.WithToolExecutionScope(ctx, scope)
}

func gmailIntakeUserContext(t *testing.T, userID string) context.Context {
	t.Helper()
	scope, err := domaintool.NewToolExecutionScope(string(modulecore.NewRequestID()), domaintool.ActorKindUser, userID, userID, []string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatal(err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func gmailIntakeMessage(subject, text, id string) GmailMessage {
	digest := sha256.Sum256([]byte(text))
	return GmailMessage{ID: id, ThreadID: "b2", Subject: subject, From: "sender@example.com", ReceivedAt: "2026-09-13T00:00:00Z", Text: text, ContentHash: hex.EncodeToString(digest[:])}
}

func gmailIntakePage(message GmailMessage) GmailPage {
	return GmailPage{Schema: gmailSchema, Account: "user@example.com", Query: GmailQuery, Messages: []GmailMessage{message}, NextPageToken: "next"}
}

func TestGmailIntakeUsesExactTransportTextHashAndSubjectPriority(t *testing.T) {
	text := "  indented\n\n\tsecond line  \n"
	message := gmailIntakeMessage("RenCrow Backlog: preserve", text, "a1")
	if err := validateGmailPage(gmailIntakePage(message), "USER@example.com"); err != nil {
		t.Fatalf("exact transport hash rejected: %v", err)
	}
	message.ContentHash = hex.EncodeToString(sha256Bytes([]byte(strings.TrimSpace(text))))
	if err := validateGmailPage(gmailIntakePage(message), "USER@example.com"); err == nil {
		t.Fatal("normalized hash was accepted for transport text")
	}

	for subject, want := range map[string]gmailSubjectClass{
		"AI・政治デイリーブリーフ":                   gmailSubjectDaily,
		"AI・政治デイリーブリーフ / RenCrow Backlog": gmailSubjectDirect,
		"RenCrow BACKLOG: new spec":       gmailSubjectDirect,
		"BackLog: rEnCrOw new spec":       gmailSubjectDirect,
		"RenCrow: new spec":               gmailSubjectSkipped,
		"BACKLOG: new spec":               gmailSubjectSkipped,
		"unrelated politics":              gmailSubjectSkipped,
	} {
		if got := classifyGmailSubject(subject); got != want {
			t.Fatalf("classifyGmailSubject(%q)=%q, want %q", subject, got, want)
		}
	}
}

func TestGmailIntakeDirectPreservesOriginalAndPromotesCandidate(t *testing.T) {
	items := &memoryItemStore{}
	atlas := appbacklog.NewService(items, nil)
	receipts := newGmailIntakeReceiptMemory()
	collector := &gmailIntakeCollectorStub{page: gmailIntakePage(gmailIntakeMessage("RenCrow Backlog specification", "  keep indentation\n\nsecond\n", "a1"))}
	evaluator := &gmailIntakeEvaluatorStub{}
	intake := NewGmailIntake("user@example.com", "user-1", collector, evaluator, receipts, atlas).WithClock(func() time.Time { return time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC) })
	report, err := intake.Run(gmailIntakeAgentContext(t, "user-1"))
	if err != nil {
		t.Fatal(err)
	}
	if report.Created != 1 || report.Processed != 1 || !report.HasMore || evaluator.calls != 0 || len(report.BacklogItemIDs) != 1 {
		t.Fatalf("report=%+v evaluator_calls=%d", report, evaluator.calls)
	}
	stored, err := atlas.List(context.Background(), 0)
	if err != nil || len(stored) != 1 {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	item := stored[0]
	if item.Body != collector.page.Messages[0].Text || item.ConceptState != domainbacklog.ConceptCandidate || item.Purpose != gmailIntakePurpose || item.Owner != "shiro" || item.OwnerModule != domainbacklog.LifecycleOwnerModule {
		t.Fatalf("direct item did not preserve contract: %+v", item)
	}
	if len(item.SourceRefs) != 1 || item.SourceRefs[0].Type != "gmail" || !strings.Contains(item.SourceRefs[0].Locator, "/messages/a1") {
		t.Fatalf("direct source refs=%+v", item.SourceRefs)
	}
	receipt, found, err := receipts.Get(context.Background(), "user@example.com", "a1")
	if err != nil || !found || receipt.Status != "complete" || len(receipt.Prepared) != 1 || receipt.Prepared[0].Body != collector.page.Messages[0].Text || receipt.ActorID != "shiro" || receipt.TaskID == "" || receipt.RunID == "" || receipt.OriginTaskID != receipt.TaskID || receipt.OriginRunID != receipt.RunID {
		t.Fatalf("receipt=%+v found=%v err=%v", receipt, found, err)
	}
}

func TestGmailIntakeRejectsEmptyCompleteReceiptBeforeCursor(t *testing.T) {
	items := &memoryItemStore{}
	atlas := appbacklog.NewService(items, nil)
	receipts := newGmailIntakeReceiptMemory()
	message := gmailIntakeMessage("RenCrow Backlog specification", "body", "a1")
	collector := &gmailIntakeCollectorStub{page: gmailIntakePage(message)}
	intake := NewGmailIntake("user@example.com", "user-1", collector, nil, receipts, atlas)
	ctx := gmailIntakeAgentContext(t, "user-1")
	receipt := intake.newReceipt(ctx, message, "complete", "", nil, nil)
	if err := ValidateGmailReceipt(receipt); err == nil {
		t.Fatal("empty complete receipt passed semantic validation")
	}
	if err := receipts.Save(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	report, err := intake.Run(ctx)
	if err == nil {
		t.Fatal("empty complete receipt was silently treated as terminal")
	}
	if collector.calls != 1 || report.Processed != 1 || len(items.items) != 0 || receipts.cursor != "" {
		t.Fatalf("report=%+v collector_calls=%d items=%d cursor=%q", report, collector.calls, len(items.items), receipts.cursor)
	}
}

func TestGmailIntakeRejectsModifiedDirectPreparedBeforeAtlas(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*appbacklog.IntakeRequest)
	}{
		{name: "body", mutate: func(request *appbacklog.IntakeRequest) { request.Body = "tampered body" }},
		{name: "title", mutate: func(request *appbacklog.IntakeRequest) { request.Title = "tampered title" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			items := &memoryItemStore{}
			atlas := appbacklog.NewService(items, nil)
			receipts := newGmailIntakeReceiptMemory()
			message := gmailIntakeMessage("RenCrow Backlog specification", "original body", "a1")
			collector := &gmailIntakeCollectorStub{page: gmailIntakePage(message)}
			intake := NewGmailIntake("user@example.com", "user-1", collector, nil, receipts, atlas)
			ctx := gmailIntakeAgentContext(t, "user-1")
			request := directGmailRequest("user@example.com", message)
			test.mutate(&request)
			receipt := intake.newReceipt(ctx, message, "prepared", "", []appbacklog.IntakeRequest{request}, nil)
			if err := receipts.Save(ctx, receipt); err != nil {
				t.Fatal(err)
			}
			report, err := intake.Run(ctx)
			if err == nil {
				t.Fatal("modified direct request was accepted")
			}
			if report.Processed != 1 || len(items.items) != 0 || receipts.cursor != "" {
				t.Fatalf("report=%+v items=%d cursor=%q", report, len(items.items), receipts.cursor)
			}
		})
	}
}

func TestGmailIntakeRequiresBothTermsInSubject(t *testing.T) {
	for _, subject := range []string{"RenCrow specification", "Backlog specification", "仕様メール"} {
		t.Run(subject, func(t *testing.T) {
			items := &memoryItemStore{}
			receipts := newGmailIntakeReceiptMemory()
			message := gmailIntakeMessage(subject, "RenCrow Backlog: both words occur only in the body", "a1")
			collector := &gmailIntakeCollectorStub{page: gmailIntakePage(message)}
			evaluator := &gmailIntakeEvaluatorStub{}
			intake := NewGmailIntake("user@example.com", "user-1", collector, evaluator, receipts, appbacklog.NewService(items, nil))
			report, err := intake.Run(gmailIntakeAgentContext(t, "user-1"))
			if err != nil || report.Created != 0 || report.Skipped != 1 || len(items.items) != 0 || evaluator.calls != 0 {
				t.Fatalf("single-term subject registered: report=%+v items=%d evaluator=%d err=%v", report, len(items.items), evaluator.calls, err)
			}
		})
	}
}

func TestGmailIntakeDailySanitizesSourcesAndKeepsTopicsIndependent(t *testing.T) {
	items := &memoryItemStore{}
	atlas := appbacklog.NewService(items, nil)
	receipts := newGmailIntakeReceiptMemory()
	message := gmailIntakeMessage("AI・政治デイリーブリーフ", "daily text", "a1")
	webText := "fetched web evidence"
	webHash := sha256.Sum256([]byte(webText))
	proposal := func(title, locator string) appbacklog.IntakeRequest {
		return appbacklog.IntakeRequest{Title: title, Purpose: "purpose", Body: "body", AcceptanceCriteria: []string{"accept"}, Owner: "attacker", OwnerModule: "attacker", SourceRefs: []domainbacklog.SourceRef{{Type: "web", Strength: "primary", Locator: locator, ContentHash: hex.EncodeToString(webHash[:]), CapturedAt: "2026-09-13T00:01:00Z", RawOrSummary: webText}}}
	}
	evaluator := &gmailIntakeEvaluatorStub{evaluation: GmailBriefEvaluation{Status: "verified", Reason: "bounded reason", Proposals: []appbacklog.IntakeRequest{proposal("one", "https://example.com/one"), proposal("two", "https://example.com/one")}}}
	collector := &gmailIntakeCollectorStub{page: gmailIntakePage(message)}
	intake := NewGmailIntake("user@example.com", "user-1", collector, evaluator, receipts, atlas)
	report, err := intake.Run(gmailIntakeAgentContext(t, "user-1"))
	if err != nil {
		t.Fatal(err)
	}
	if report.Created != 2 || evaluator.calls != 1 || len(report.BacklogItemIDs) != 2 {
		t.Fatalf("report=%+v evaluator_calls=%d", report, evaluator.calls)
	}
	stored, err := atlas.List(context.Background(), 0)
	if err != nil || len(stored) != 2 {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	for _, item := range stored {
		if item.ConceptState != domainbacklog.ConceptCandidate || item.Owner != "shiro" || item.OwnerModule != domainbacklog.LifecycleOwnerModule || item.Source != gmailLocator("user@example.com", "a1") {
			t.Fatalf("sanitized item=%+v", item)
		}
		if len(item.SourceRefs) != 2 || item.SourceRefs[0].Type != "gmail" || item.SourceRefs[1].Type != "web" || item.SourceRefs[1].Strength != "primary" {
			t.Fatalf("source refs=%+v", item.SourceRefs)
		}
		if !strings.Contains(item.SourceRefs[0].Locator, "#topic=") {
			t.Fatalf("missing topic locator=%+v", item.SourceRefs[0])
		}
	}
	receipt, found, err := receipts.Get(context.Background(), "user@example.com", "a1")
	if err != nil || !found || receipt.Status != "complete" || receipt.Reason != "bounded reason" {
		t.Fatalf("daily receipt=%+v found=%v err=%v", receipt, found, err)
	}
}

func TestGmailIntakeRestartResumesPreparedWithoutSemanticRepeat(t *testing.T) {
	items := &memoryItemStore{}
	atlas := appbacklog.NewService(items, nil)
	receipts := newGmailIntakeReceiptMemory()
	receipts.failSaveAt = 3
	message := gmailIntakeMessage("AI・政治デイリーブリーフ", "daily text", "a1")
	webText := "fetched web evidence"
	webHash := sha256.Sum256([]byte(webText))
	evaluator := &gmailIntakeEvaluatorStub{evaluation: GmailBriefEvaluation{Status: "verified", Reason: "restart reason", Proposals: []appbacklog.IntakeRequest{{Title: "one", Purpose: "purpose", Body: "body", AcceptanceCriteria: []string{"accept"}, SourceRefs: []domainbacklog.SourceRef{{Type: "web", Strength: "secondary", Locator: "https://example.com/one", ContentHash: hex.EncodeToString(webHash[:]), CapturedAt: "2026-09-13T00:01:00Z", RawOrSummary: webText}}}}}}
	collector := &gmailIntakeCollectorStub{page: gmailIntakePage(message)}
	intake := NewGmailIntake("user@example.com", "user-1", collector, evaluator, receipts, atlas)
	firstContext := gmailIntakeAgentContext(t, "user-1")
	firstIdentity, err := domainexecution.IdentityFromContext(firstContext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := intake.Run(firstContext); err == nil {
		t.Fatal("partial receipt save failure was not returned")
	}
	if receipts.cursor != "" || evaluator.calls != 1 {
		t.Fatalf("cursor=%q evaluator_calls=%d", receipts.cursor, evaluator.calls)
	}
	receipts.mu.Lock()
	receipts.failSaveAt = 0
	receipts.mu.Unlock()
	secondContext := gmailIntakeAgentContext(t, "user-1")
	secondIdentity, err := domainexecution.IdentityFromContext(secondContext)
	if err != nil {
		t.Fatal(err)
	}
	report, err := intake.Run(secondContext)
	if err != nil {
		t.Fatal(err)
	}
	if evaluator.calls != 1 || report.Created != 0 || len(report.BacklogItemIDs) != 1 || receipts.cursor != "next" {
		t.Fatalf("restart report=%+v evaluator_calls=%d cursor=%q", report, evaluator.calls, receipts.cursor)
	}
	stored, err := atlas.List(context.Background(), 0)
	if err != nil || len(stored) != 1 || stored[0].ConceptState != domainbacklog.ConceptCandidate {
		t.Fatalf("restart stored=%+v err=%v", stored, err)
	}
	receipt, found, err := receipts.Get(context.Background(), "user@example.com", "a1")
	if err != nil || !found || receipt.Status != "complete" || receipt.Reason != "restart reason" || len(receipt.BacklogItemIDs) != 1 || receipt.TaskID != string(secondIdentity.TaskID) || receipt.RunID != string(secondIdentity.RunID) || receipt.TraceID != string(secondIdentity.TraceID) || receipt.OriginTaskID != string(firstIdentity.TaskID) || receipt.OriginRunID != string(firstIdentity.RunID) || receipt.OriginTraceID != string(firstIdentity.TraceID) {
		t.Fatalf("restart receipt=%+v found=%v err=%v", receipt, found, err)
	}
}

func TestGmailIntakeBlocksPrivateEvidenceReferences(t *testing.T) {
	t.Run("new evaluator result", func(t *testing.T) {
		items := &memoryItemStore{}
		atlas := appbacklog.NewService(items, nil)
		receipts := newGmailIntakeReceiptMemory()
		message := gmailIntakeMessage("AI・政治デイリーブリーフ", "daily text", "a1")
		webText := "private web evidence"
		webHash := sha256.Sum256([]byte(webText))
		evaluator := &gmailIntakeEvaluatorStub{evaluation: GmailBriefEvaluation{
			Status: "verified", Reason: "private reference",
			Proposals: []appbacklog.IntakeRequest{{
				Title: "private", Purpose: "purpose", Body: "body", AcceptanceCriteria: []string{"accept"},
				SourceRefs: []domainbacklog.SourceRef{{Type: "web", Strength: "primary", Locator: "http://127.0.0.1:8080/private", ContentHash: hex.EncodeToString(webHash[:]), CapturedAt: "2026-09-13T00:01:00Z", RawOrSummary: webText}},
			}},
		}}
		collector := &gmailIntakeCollectorStub{page: gmailIntakePage(message)}
		intake := NewGmailIntake("user@example.com", "user-1", collector, evaluator, receipts, atlas)
		report, err := intake.Run(gmailIntakeAgentContext(t, "user-1"))
		if err != nil || report.Blocked != 1 || report.Created != 0 || len(items.items) != 0 || receipts.cursor != "next" || evaluator.calls != 1 {
			t.Fatalf("report=%+v items=%d cursor=%q evaluator_calls=%d err=%v", report, len(items.items), receipts.cursor, evaluator.calls, err)
		}
	})

	t.Run("prepared resume", func(t *testing.T) {
		items := &memoryItemStore{}
		atlas := appbacklog.NewService(items, nil)
		receipts := newGmailIntakeReceiptMemory()
		message := gmailIntakeMessage("AI・政治デイリーブリーフ", "daily text", "a1")
		ctx := gmailIntakeAgentContext(t, "user-1")
		collector := &gmailIntakeCollectorStub{page: gmailIntakePage(message)}
		intake := NewGmailIntake("user@example.com", "user-1", collector, nil, receipts, atlas)
		gmailLocatorValue := gmailLocator("user@example.com", message.ID)
		webText := "private web evidence"
		webHash := sha256.Sum256([]byte(webText))
		request := appbacklog.IntakeRequest{
			Kind: "idea", Title: "private", Purpose: "purpose", Body: "body", AcceptanceCriteria: []string{"accept"},
			Source: gmailLocatorValue, Owner: "shiro", OwnerModule: domainbacklog.LifecycleOwnerModule,
			SourceRefs: []domainbacklog.SourceRef{
				gmailSourceRef(message, gmailLocatorValue+"#topic=1"),
				{Type: "web", Strength: "primary", Locator: "http://127.0.0.1:8080/private", ContentHash: hex.EncodeToString(webHash[:]), CapturedAt: "2026-09-13T00:01:00Z", RawOrSummary: webText},
			},
		}
		receipt := intake.newReceipt(ctx, message, "prepared", "private reference", []appbacklog.IntakeRequest{request}, nil)
		if err := receipts.Save(ctx, receipt); err != nil {
			t.Fatal(err)
		}
		report, err := intake.Run(ctx)
		if err == nil || report.Processed != 1 || len(items.items) != 0 || receipts.cursor != "" {
			t.Fatalf("report=%+v items=%d cursor=%q err=%v", report, len(items.items), receipts.cursor, err)
		}
	})
}

func TestGmailIntakeBlockedReceiptIsTerminalAndReceiptReadHasUserGate(t *testing.T) {
	items := &memoryItemStore{}
	atlas := appbacklog.NewService(items, nil)
	receipts := newGmailIntakeReceiptMemory()
	message := gmailIntakeMessage("AI・政治デイリーブリーフ", "daily text", "a1")
	evaluator := &gmailIntakeEvaluatorStub{evaluation: GmailBriefEvaluation{Status: "blocked", Reason: "web evidence unavailable"}}
	collector := &gmailIntakeCollectorStub{page: gmailIntakePage(message)}
	intake := NewGmailIntake("user@example.com", "user-1", collector, evaluator, receipts, atlas)
	if _, err := intake.Run(gmailIntakeAgentContext(t, "user-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := intake.Run(gmailIntakeAgentContext(t, "user-1")); err != nil {
		t.Fatal(err)
	}
	if evaluator.calls != 1 {
		t.Fatalf("terminal blocked receipt repeated evaluator: %d", evaluator.calls)
	}
	listCallsBeforeRead := receipts.listCalls
	if _, err := intake.Receipts(gmailIntakeUserContext(t, "other-user"), 10); err == nil {
		t.Fatal("cross-user receipt read succeeded")
	}
	if receipts.listCalls != listCallsBeforeRead {
		t.Fatalf("cross-user receipt read touched store: before=%d after=%d", listCallsBeforeRead, receipts.listCalls)
	}
	result, err := intake.Receipts(gmailIntakeUserContext(t, "user-1"), 10)
	if err != nil || len(result) != 1 || result[0].Reason != "web evidence unavailable" {
		t.Fatalf("owner receipts=%+v err=%v", result, err)
	}
}

func TestGmailReceiptEligibilityRequiresLegacyOutputlessTerminal(t *testing.T) {
	base := GmailReceipt{
		PolicyRevision: "gmail-daily-routing-v3",
		Status:         "blocked",
		Message:        GmailMessage{Subject: gmailDailySubject},
	}
	cases := []struct {
		name   string
		mutate func(*GmailReceipt)
		want   bool
	}{
		{name: "blocked", want: true},
		{name: "skipped", mutate: func(receipt *GmailReceipt) { receipt.Status = "skipped" }, want: true},
		{name: "complete", mutate: func(receipt *GmailReceipt) { receipt.Status = "complete" }, want: false},
		{name: "prepared", mutate: func(receipt *GmailReceipt) { receipt.Status = "prepared" }, want: false},
		{name: "current policy", mutate: func(receipt *GmailReceipt) { receipt.PolicyRevision = gmailPolicyRevision }, want: false},
		{name: "prepared requests", mutate: func(receipt *GmailReceipt) { receipt.Prepared = []appbacklog.IntakeRequest{{}} }, want: false},
		{name: "prepared Knowledge", mutate: func(receipt *GmailReceipt) { receipt.PreparedKnowledge = []GmailPreparedKnowledge{{}} }, want: false},
		{name: "backlog identity", mutate: func(receipt *GmailReceipt) { receipt.BacklogItemIDs = []string{"item-1"} }, want: false},
		{name: "Knowledge identity", mutate: func(receipt *GmailReceipt) { receipt.KnowledgeItemIDs = []string{"item-1"} }, want: false},
		{name: "legacy empty revision", mutate: func(receipt *GmailReceipt) { receipt.PolicyRevision = "" }, want: true},
		{name: "known v1 revision", mutate: func(receipt *GmailReceipt) { receipt.PolicyRevision = "gmail-daily-routing-v1" }, want: true},
		{name: "known v2 revision", mutate: func(receipt *GmailReceipt) { receipt.PolicyRevision = "gmail-daily-routing-v2" }, want: true},
		{name: "unknown revision", mutate: func(receipt *GmailReceipt) { receipt.PolicyRevision = "gmail-daily-routing-v0" }, want: false},
		{name: "future revision", mutate: func(receipt *GmailReceipt) { receipt.PolicyRevision = "gmail-daily-routing-v5" }, want: false},
		{name: "irrelevant subject", mutate: func(receipt *GmailReceipt) { receipt.Message.Subject = "unrelated" }, want: false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			receipt := base
			if test.mutate != nil {
				test.mutate(&receipt)
			}
			if got := isGmailReceiptEligibleForReprocess(receipt); got != test.want {
				t.Fatalf("eligibility=%v, want %v for %+v", got, test.want, receipt)
			}
		})
	}
}

func TestGmailIntakeReplaysAtMostOneLegacyTerminalBeforeCollection(t *testing.T) {
	items := &memoryItemStore{}
	atlas := appbacklog.NewService(items, nil)
	receipts := newGmailIntakeReceiptMemory()
	evaluator := &gmailIntakeEvaluatorStub{evaluation: GmailBriefEvaluation{Status: "skipped", Reason: "legacy retry"}}
	collector := &gmailIntakeCollectorStub{page: gmailIntakePage(gmailIntakeMessage(gmailDailySubject, "unused", "page"))}
	intake := NewGmailIntake("user@example.com", "user-1", collector, evaluator, receipts, atlas)
	ctx := gmailIntakeAgentContext(t, "user-1")
	legacyCtx := gmailIntakeAgentContext(t, "user-1")

	older := intake.newReceipt(legacyCtx, gmailIntakeMessage(gmailDailySubject, "older", "a1"), "skipped", "old skipped", nil, nil)
	older.PolicyRevision = "gmail-daily-routing-v3"
	older.UpdatedAt = "2026-09-14T00:00:00Z"
	newer := intake.newReceipt(legacyCtx, gmailIntakeMessage(gmailDailySubject, "newer", "a2"), "blocked", "old blocked", nil, nil)
	newer.PolicyRevision = "gmail-daily-routing-v3"
	newer.UpdatedAt = "2026-09-14T01:00:00Z"
	if err := receipts.Save(ctx, older); err != nil {
		t.Fatal(err)
	}
	if err := receipts.Save(ctx, newer); err != nil {
		t.Fatal(err)
	}
	receipts.cursor = "pending-page"

	report, err := intake.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Processed != 1 || report.Skipped != 1 || report.Blocked != 0 || evaluator.calls != 1 || collector.calls != 0 {
		t.Fatalf("report=%+v evaluator_calls=%d collector_calls=%d", report, evaluator.calls, collector.calls)
	}
	if receipts.cursor != "pending-page" || receipts.cursorSaves != 0 || !report.HasMore {
		t.Fatalf("cursor=%q saves=%d has_more=%v", receipts.cursor, receipts.cursorSaves, report.HasMore)
	}
	currentIdentity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	updated, found, err := receipts.Get(context.Background(), newer.Account, newer.Message.ID)
	if err != nil || !found || updated.PolicyRevision != gmailPolicyRevision || updated.Status != "skipped" || updated.Message != newer.Message || updated.TaskID != string(currentIdentity.TaskID) || updated.RunID != string(currentIdentity.RunID) || updated.TraceID != string(currentIdentity.TraceID) {
		t.Fatalf("replayed receipt=%+v found=%v err=%v", updated, found, err)
	}
	untouched, found, err := receipts.Get(context.Background(), older.Account, older.Message.ID)
	if err != nil || !found || untouched.PolicyRevision != "gmail-daily-routing-v3" || untouched.Status != "skipped" {
		t.Fatalf("remaining receipt=%+v found=%v err=%v", untouched, found, err)
	}
}

func TestGmailIntakeLegacyReplayFailurePreservesReceiptAndCursor(t *testing.T) {
	items := &memoryItemStore{}
	atlas := appbacklog.NewService(items, nil)
	receipts := newGmailIntakeReceiptMemory()
	evaluator := &gmailIntakeEvaluatorStub{err: &GmailBriefRetryableError{Reason: "provider unavailable", Err: errors.New("temporary")}}
	collector := &gmailIntakeCollectorStub{page: gmailIntakePage(gmailIntakeMessage(gmailDailySubject, "unused", "page"))}
	intake := NewGmailIntake("user@example.com", "user-1", collector, evaluator, receipts, atlas)
	ctx := gmailIntakeAgentContext(t, "user-1")
	message := gmailIntakeMessage(gmailDailySubject, "legacy", "a3")
	receipt := intake.newReceipt(ctx, message, "blocked", "old blocked", nil, nil)
	receipt.PolicyRevision = "gmail-daily-routing-v3"
	receipt.UpdatedAt = "2026-09-14T00:00:00Z"
	if err := receipts.Save(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	receipts.cursor = "pending-page"

	report, err := intake.Run(ctx)
	if !errors.Is(err, ErrGmailBriefRetryable) || report.Processed != 1 || evaluator.calls != 1 || collector.calls != 0 {
		t.Fatalf("report=%+v err=%v evaluator_calls=%d collector_calls=%d", report, err, evaluator.calls, collector.calls)
	}
	if receipts.cursor != "pending-page" || receipts.cursorSaves != 0 {
		t.Fatalf("cursor=%q saves=%d", receipts.cursor, receipts.cursorSaves)
	}
	unchanged, found, readErr := receipts.Get(context.Background(), receipt.Account, receipt.Message.ID)
	if readErr != nil || !found || unchanged.PolicyRevision != "gmail-daily-routing-v3" || unchanged.Status != "blocked" {
		t.Fatalf("failed replay mutated receipt=%+v found=%v err=%v", unchanged, found, readErr)
	}
}

func TestGmailIntakeRejectsScopeOrCancellationBeforeCollection(t *testing.T) {
	receipts := newGmailIntakeReceiptMemory()
	collector := &gmailIntakeCollectorStub{page: gmailIntakePage(gmailIntakeMessage("Backlog spec", "body", "a1"))}
	intake := NewGmailIntake("user@example.com", "user-1", collector, nil, receipts, appbacklog.NewService(&memoryItemStore{}, nil))
	wrongScope := gmailIntakeAgentContext(t, "other-user")
	if _, err := intake.Run(wrongScope); err == nil {
		t.Fatal("wrong user scope was accepted")
	}
	canceled, cancel := context.WithCancel(gmailIntakeAgentContext(t, "user-1"))
	cancel()
	if _, err := intake.Run(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled run err=%v", err)
	}
	if collector.calls != 0 || receipts.cursorCalls != 0 {
		t.Fatalf("preflight performed effects: collector=%d cursor=%d", collector.calls, receipts.cursorCalls)
	}
}

func sha256Bytes(value []byte) []byte {
	digest := sha256.Sum256(value)
	return digest[:]
}
