package gmailintake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"

	appbacklog "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
)

type diaryArchiveWriterStub struct {
	calls      int
	failFirst  int
	items      []domainkm.PersonalArchiveEntry
	sourceKeys []string
	actorIDs   []string
}

func (s *diaryArchiveWriterStub) SavePersonalArchiveEntryWithReceipt(_ context.Context, item domainkm.PersonalArchiveEntry, sourceKey, actorID string) (bool, error) {
	s.calls++
	s.items = append(s.items, item)
	s.sourceKeys = append(s.sourceKeys, sourceKey)
	s.actorIDs = append(s.actorIDs, actorID)
	if s.calls <= s.failFirst {
		return false, errors.New("synthetic archive sink failure")
	}
	return true, nil
}

type diaryBoardWriterStub struct {
	diary  GmailDiary
	found  bool
	gets   int
	puts   int
	getErr error
	putErr error
}

type diaryBackfillCollectorStub struct {
	pages  map[string]GmailPage
	tokens []string
}

func (*diaryBackfillCollectorStub) Collect(context.Context, string) (GmailPage, error) {
	return GmailPage{}, nil
}
func (s *diaryBackfillCollectorStub) CollectDiaries(_ context.Context, token string) (GmailPage, error) {
	s.tokens = append(s.tokens, token)
	return s.pages[token], nil
}

func (s *diaryBoardWriterStub) GetDiary(context.Context, string) (GmailDiary, bool, error) {
	s.gets++
	return s.diary, s.found, s.getErr
}
func (s *diaryBoardWriterStub) PutDiary(_ context.Context, diary GmailDiary) (GmailDiary, error) {
	s.puts++
	s.diary, s.found = diary, true
	if s.putErr != nil {
		return GmailDiary{}, s.putErr
	}
	return diary, nil
}

func TestParseGmailDiaryKeepsTopLevelSectionsAndIgnoresQuotedHeadings(t *testing.T) {
	message := GmailMessage{Subject: "【ルミナとれんの会話記録】2026-11-15", From: "Ren <nyukimi@gmail.com>", ReceivedAt: "2026-11-16T01:02:03Z", Text: "1. 今日の会話\n相談した。\n> 3. ルミナの感想\nquoted\n2. 今日のまとめ\n一緒に進んだ。\n### 3. ルミナの感想\nうれしかった。"}
	got, err := parseGmailDiary(message)
	if err != nil {
		t.Fatal(err)
	}
	if got.Date != "2026-11-15" || got.Body != "1. 今日の会話\n相談した。\n> 3. ルミナの感想\nquoted\n\n2. 今日のまとめ\n一緒に進んだ。" || got.Reflection != "### 3. ルミナの感想\nうれしかった。" {
		t.Fatalf("unexpected parsed diary: %#v", got)
	}
}

func TestParseGmailDiaryAcceptsMarkdownTitleHeadingsButNotIndentedListHeadings(t *testing.T) {
	message := GmailMessage{Subject: "【ルミナとれんの会話記録】2026-11-15", From: "nyukimi@gmail.com", ReceivedAt: "2026-11-16T01:02:03Z", Text: "# 今日の会話\n会話\n## 今日のまとめ\nまとめ\n### ルミナの感想\n感想"}
	if _, err := parseGmailDiary(message); err != nil {
		t.Fatalf("Markdown title headings rejected: %v", err)
	}
	message.Text = "  1. 今日の会話\n会話\n2. 今日のまとめ\nまとめ\n3. ルミナの感想\n感想"
	if _, err := parseGmailDiary(message); err == nil {
		t.Fatal("indented list item was accepted as a top-level heading")
	}
}

func TestParseGmailDiaryAcceptsExactLegacyPlainAndDatedHeadings(t *testing.T) {
	message := GmailMessage{Subject: "【ルミナとれんの会話記録】2026-09-20", From: "nyukimi@gmail.com", ReceivedAt: "2026-11-16T01:02:03Z", Text: "2026年9月20日 今日の会話\n会話\n今日のまとめ\nまとめ\nルミナの感想\n感想"}
	got, err := parseGmailDiary(message)
	if err != nil {
		t.Fatalf("exact legacy dated headings rejected: %v", err)
	}
	if got.Body != "2026年9月20日 今日の会話\n会話\n\n今日のまとめ\nまとめ" || got.Reflection != "ルミナの感想\n感想" {
		t.Fatalf("legacy source headings were rewritten: %#v", got)
	}
	message.Text = strings.Replace(message.Text, "2026年9月20日", "2026年9月21日", 1)
	if _, err := parseGmailDiary(message); err == nil {
		t.Fatal("dated first heading contradicting subject date was accepted")
	}
}

func TestParseGmailDiaryPreservesCodeFencesAndRejectsEmptyOrOutOfOrderSections(t *testing.T) {
	message := GmailMessage{Subject: "【ルミナとれんの会話記録】2026-11-15", From: "nyukimi@gmail.com", ReceivedAt: "2026-11-16T01:02:03Z", Text: "1. 今日の会話\n会話\n````markdown\n2. 今日のまとめ\nquoted code\n```\nmore code\n````\n2. 今日のまとめ\nまとめ\n3. ルミナの感想\n感想"}
	got, err := parseGmailDiary(message)
	if err != nil {
		t.Fatalf("fenced diary rejected: %v", err)
	}
	if !strings.Contains(got.Body, "````markdown\n2. 今日のまとめ\nquoted code\n```\nmore code\n````") {
		t.Fatalf("fence markers or quoted code were changed: %q", got.Body)
	}
	message.Text = "1. 今日の会話\n   \n2. 今日のまとめ\nまとめ\n3. ルミナの感想\n感想"
	if _, err := parseGmailDiary(message); err == nil {
		t.Fatal("heading-only first section was accepted")
	}
	message.Text = "2. 今日のまとめ\nまとめ\n1. 今日の会話\n会話\n3. ルミナの感想\n感想"
	if _, err := parseGmailDiary(message); err == nil {
		t.Fatal("out-of-order diary sections were accepted")
	}
}

func TestParseGmailDiaryBlocksMalformedOrContradictorySource(t *testing.T) {
	base := GmailMessage{Subject: "【ルミナとれんの会話記録】2026-02-30", From: "nyukimi@gmail.com", ReceivedAt: "2026-11-16T01:02:03Z", Text: "1. 今日の会話\na\n2. 今日のまとめ\nb\n3. ルミナの感想\nc"}
	if _, err := parseGmailDiary(base); err == nil {
		t.Fatal("invalid subject date accepted")
	}
	base.Subject = "【ルミナとれんの会話記録】2026-11-15"
	base.From = "spoof@example.com"
	if _, err := parseGmailDiary(base); err == nil {
		t.Fatal("wrong author accepted")
	}
	base.From = "nyukimi@gmail.com"
	base.Text = "1. 今日の会話\na\n2. 今日のまとめ\nb\n> 3. ルミナの感想\nc"
	if _, err := parseGmailDiary(base); err == nil {
		t.Fatal("quoted section accepted")
	}
}

func TestClassifyGmailDiaryRequiresExactSubject(t *testing.T) {
	if classifyGmailSubject("【ルミナとれんの会話記録】2026-11-15") != gmailSubjectDiary {
		t.Fatal("exact diary subject was not selected")
	}
	if classifyGmailSubject("Re: 【ルミナとれんの会話記録】2026-11-15") != gmailSubjectSkipped {
		t.Fatal("reply subject was selected as a diary")
	}
}

func TestProcessGmailDiaryWritesProtectedArchiveAndBoardDiaryWithReceipt(t *testing.T) {
	text := "1. 今日の会話\n相談した。\n2. 今日のまとめ\n一緒に進んだ。\n3. ルミナの感想\nうれしかった。"
	digest := sha256.Sum256([]byte(text))
	message := GmailMessage{ID: "a1b2c3", ThreadID: "d4e5f6", Subject: "【ルミナとれんの会話記録】2026-11-15", From: "Ren <nyukimi@gmail.com>", ReceivedAt: "2026-11-16T01:02:03Z", Text: text, ContentHash: hex.EncodeToString(digest[:])}
	receipts := newGmailIntakeReceiptMemory()
	archive, board := &diaryArchiveWriterStub{}, &diaryBoardWriterStub{}
	g := NewGmailIntake("nyukimin0811@gmail.com", "ren", nil, nil, receipts, (*appbacklog.Service)(nil)).WithPersonalArchiveWriter(archive).WithDiaryWriter(board)
	if _, status, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), message); err != nil || status != "complete" {
		t.Fatalf("diary intake status=%q err=%v", status, err)
	}
	receipt, found, err := receipts.Get(context.Background(), "nyukimin0811@gmail.com", message.ID)
	if err != nil || !found || receipt.Status != "complete" || !receipt.ArchiveSaved || !receipt.DiarySaved {
		t.Fatalf("diary receipt not complete: found=%t receipt=%#v err=%v", found, receipt, err)
	}
	if archive.calls != 1 || board.puts != 1 || board.diary.ActorID != "lumina" || board.diary.Date != "2026-11-15" {
		t.Fatalf("unexpected sinks: archive=%d board=%#v", archive.calls, board)
	}
	if archive.items[0].AuthorID != "lumina" || archive.actorIDs[0] != "shiro" || archive.sourceKeys[0] != archive.items[0].SourceRef {
		t.Fatalf("archive provenance mixed ingester and author identity: item=%#v source=%q actor=%q", archive.items[0], archive.sourceKeys[0], archive.actorIDs[0])
	}
	if err := ValidateGmailReceipt(receipt); err != nil {
		t.Fatalf("completed diary receipt invalid: %v", err)
	}
}

func TestRunDiaryBackfillUsesFixedQueryAndLeavesGeneralCursorUntouched(t *testing.T) {
	makeMessage := func(id, text string) GmailMessage {
		digest := sha256.Sum256([]byte(text))
		return GmailMessage{ID: id, ThreadID: "d4e5f6", Subject: "【ルミナとれんの会話記録】2026-11-15", From: "nyukimi@gmail.com", ReceivedAt: "2026-11-16T01:02:03Z", Text: text, ContentHash: hex.EncodeToString(digest[:])}
	}
	first := makeMessage("a1b2c3", "1. 今日の会話\na\n2. 今日のまとめ\nb\n3. ルミナの感想\nc")
	second := makeMessage("f1e2d3", "1. 今日の会話\na\n2. 今日のまとめ\nb\n3. ルミナの感想\nc")
	collector := &diaryBackfillCollectorStub{pages: map[string]GmailPage{
		"":     {Schema: gmailSchema, Account: "nyukimin0811@gmail.com", Query: GmailDiaryQuery, Messages: []GmailMessage{first}, NextPageToken: "next"},
		"next": {Schema: gmailSchema, Account: "nyukimin0811@gmail.com", Query: GmailDiaryQuery, Messages: []GmailMessage{second}},
	}}
	receipts, archive, board := newGmailIntakeReceiptMemory(), &diaryArchiveWriterStub{}, &diaryBoardWriterStub{}
	g := NewGmailIntake("nyukimin0811@gmail.com", "ren", collector, nil, receipts, nil).WithPersonalArchiveWriter(archive).WithDiaryWriter(board)
	report, err := g.RunDiaryBackfill(gmailIntakeAgentContext(t, "ren"))
	if err != nil || report.Processed != 2 || report.Created != 1 {
		t.Fatalf("backfill report=%#v err=%v", report, err)
	}
	if len(collector.tokens) != 2 || collector.tokens[0] != "" || collector.tokens[1] != "next" || receipts.cursorCalls != 0 || receipts.cursorSaves != 0 {
		t.Fatalf("backfill changed ordinary cursor or paging: tokens=%#v cursorReads=%d cursorWrites=%d", collector.tokens, receipts.cursorCalls, receipts.cursorSaves)
	}
	if archive.calls != 2 || board.puts != 2 || board.diary.Revision != 2 {
		t.Fatalf("backfill sinks archive=%d board=%d", archive.calls, board.puts)
	}
}

func TestSameDateDifferentDiaryPayloadIsComposedAndKeepsProtectedArchive(t *testing.T) {
	makeMessage := func(id, text string) GmailMessage {
		digest := sha256.Sum256([]byte(text))
		return GmailMessage{ID: id, ThreadID: "d4e5f6", Subject: "【ルミナとれんの会話記録】2026-11-15", From: "nyukimi@gmail.com", ReceivedAt: "2026-11-16T01:02:03Z", Text: text, ContentHash: hex.EncodeToString(digest[:])}
	}
	first := makeMessage("a1b2c3", "1. 今日の会話\na\n2. 今日のまとめ\nb\n3. ルミナの感想\nc")
	second := makeMessage("f1e2d3", "1. 今日の会話\nchanged\n2. 今日のまとめ\nb\n3. ルミナの感想\nc")
	receipts, archive, board := newGmailIntakeReceiptMemory(), &diaryArchiveWriterStub{}, &diaryBoardWriterStub{}
	g := NewGmailIntake("nyukimin0811@gmail.com", "ren", nil, nil, receipts, nil).WithPersonalArchiveWriter(archive).WithDiaryWriter(board)
	if _, status, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), first); err != nil || status != "complete" {
		t.Fatalf("first diary status=%q err=%v", status, err)
	}
	if _, status, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), second); err != nil || status != "complete" {
		t.Fatalf("second diary status=%q err=%v", status, err)
	}
	receipt, found, err := receipts.Get(context.Background(), "nyukimin0811@gmail.com", second.ID)
	if err != nil || !found || receipt.Status != "complete" || !receipt.ArchiveSaved || !receipt.DiarySaved {
		t.Fatalf("composed receipt=%#v found=%t err=%v", receipt, found, err)
	}
	if archive.calls != 2 || board.puts != 2 || board.diary.Revision != 2 {
		t.Fatalf("composition lost source: archive=%d board writes=%d revision=%d", archive.calls, board.puts, board.diary.Revision)
	}
}

func TestSameDateDiarySourcesAreComposedInReceivedOrder(t *testing.T) {
	makeMessage := func(id, receivedAt, text string) GmailMessage {
		digest := sha256.Sum256([]byte(text))
		return GmailMessage{ID: id, ThreadID: "d4e5f6", Subject: "【ルミナとれんの会話記録】2025-08-16", From: "nyukimi@gmail.com", ReceivedAt: receivedAt, Text: text, ContentHash: hex.EncodeToString(digest[:])}
	}
	late := makeMessage("f1e2d3", "2026-11-18T03:00:00Z", "1. 今日の会話\nlate\n2. 今日のまとめ\nlate summary\n3. ルミナの感想\nlate reflection")
	early := makeMessage("a1b2c3", "2026-11-18T01:00:00Z", "1. 今日の会話\nearly\n2. 今日のまとめ\nearly summary\n3. ルミナの感想\nearly reflection")
	receipts, archive, board := newGmailIntakeReceiptMemory(), &diaryArchiveWriterStub{}, &diaryBoardWriterStub{}
	g := NewGmailIntake("nyukimin0811@gmail.com", "ren", nil, nil, receipts, nil).WithPersonalArchiveWriter(archive).WithDiaryWriter(board)
	if _, status, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), late); err != nil || status != "complete" {
		t.Fatalf("first diary status=%q err=%v", status, err)
	}
	if _, status, _, err := g.processMessage(gmailIntakeAgentContext(t, "ren"), early); err != nil || status != "complete" {
		t.Fatalf("second diary status=%q err=%v", status, err)
	}
	wantBody := "【メール1／受信: 2026-11-18 10:00:00 JST】\n1. 今日の会話\nearly\n\n2. 今日のまとめ\nearly summary\n\n【メール2／受信: 2026-11-18 12:00:00 JST】\n1. 今日の会話\nlate\n\n2. 今日のまとめ\nlate summary"
	wantReflection := "【メール1／受信: 2026-11-18 10:00:00 JST】\n3. ルミナの感想\nearly reflection\n\n【メール2／受信: 2026-11-18 12:00:00 JST】\n3. ルミナの感想\nlate reflection"
	if board.diary.Revision != 2 || board.diary.Body != wantBody || board.diary.Reflection != wantReflection {
		t.Fatalf("same-date diary composition mismatch: revision=%d body=%q reflection=%q", board.diary.Revision, board.diary.Body, board.diary.Reflection)
	}
	if archive.calls != 2 || archive.items[0].OriginalText != late.Text || archive.items[1].OriginalText != early.Text {
		t.Fatalf("source archive was changed or repeated: calls=%d", archive.calls)
	}
	if _, status, _, err := g.processMessage(gmailIntakeAgentContext(t, "ren"), early); err != nil || status != "complete" {
		t.Fatalf("same-source replay status=%q err=%v", status, err)
	}
	if archive.calls != 2 || board.puts != 2 {
		t.Fatalf("replay duplicated a sink: archive=%d BOARD=%d", archive.calls, board.puts)
	}
}

func TestIdenticalDiaryBodiesFromDistinctMessagesRemainSeparateSources(t *testing.T) {
	makeMessage := func(id, receivedAt string) GmailMessage {
		text := "1. 今日の会話\nsame\n2. 今日のまとめ\nsame summary\n3. ルミナの感想\nsame reflection"
		digest := sha256.Sum256([]byte(text))
		return GmailMessage{ID: id, ThreadID: "d4e5f6", Subject: "【ルミナとれんの会話記録】2025-08-16", From: "nyukimi@gmail.com", ReceivedAt: receivedAt, Text: text, ContentHash: hex.EncodeToString(digest[:])}
	}
	first := makeMessage("a1b2c3", "2026-11-18T01:00:00Z")
	second := makeMessage("f1e2d3", "2026-11-18T02:00:00Z")
	receipts, archive, board := newGmailIntakeReceiptMemory(), &diaryArchiveWriterStub{}, &diaryBoardWriterStub{}
	g := NewGmailIntake("nyukimin0811@gmail.com", "ren", nil, nil, receipts, nil).WithPersonalArchiveWriter(archive).WithDiaryWriter(board)
	for _, message := range []GmailMessage{first, second} {
		if _, status, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), message); err != nil || status != "complete" {
			t.Fatalf("identical source %s status=%q err=%v", message.ID, status, err)
		}
	}
	if board.diary.Revision != 2 || !strings.Contains(board.diary.Body, "【メール1／受信: 2026-11-18 10:00:00 JST】") || !strings.Contains(board.diary.Body, "【メール2／受信: 2026-11-18 11:00:00 JST】") || archive.calls != 2 {
		t.Fatalf("identical but distinct sources were collapsed: diary=%#v archives=%d", board.diary, archive.calls)
	}
}

func TestDiaryOwnerEditAfterBaselineIsNeverOverwritten(t *testing.T) {
	makeMessage := func(id string) GmailMessage {
		text := "1. 今日の会話\nsource\n2. 今日のまとめ\nsummary\n3. ルミナの感想\nreflection"
		digest := sha256.Sum256([]byte(text))
		return GmailMessage{ID: id, ThreadID: "d4e5f6", Subject: "【ルミナとれんの会話記録】2025-08-16", From: "nyukimi@gmail.com", ReceivedAt: "2026-11-18T01:00:00Z", Text: text, ContentHash: hex.EncodeToString(digest[:])}
	}
	receipts, archive, board := newGmailIntakeReceiptMemory(), &diaryArchiveWriterStub{}, &diaryBoardWriterStub{}
	g := NewGmailIntake("nyukimin0811@gmail.com", "ren", nil, nil, receipts, nil).WithPersonalArchiveWriter(archive).WithDiaryWriter(board)
	if _, status, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), makeMessage("a1b2c3")); err != nil || status != "complete" {
		t.Fatalf("baseline source status=%q err=%v", status, err)
	}
	ownerEdit := board.diary
	ownerEdit.Revision++
	ownerEdit.Reflection += "\nowner edit"
	board.diary = ownerEdit
	beforePuts := board.puts
	if _, status, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), makeMessage("f1e2d3")); err != nil {
		t.Fatalf("owner edit recovery err=%v", err)
	} else if status != "blocked" {
		t.Fatalf("owner edit status=%q, want blocked", status)
	}
	if board.puts != beforePuts || board.diary.Reflection != ownerEdit.Reflection || archive.calls != 2 {
		t.Fatalf("owner edit was overwritten or source dropped: puts=%d before=%d diary=%#v archives=%d", board.puts, beforePuts, board.diary, archive.calls)
	}
}

func TestUnresolvedSameDateIntentBlocksNextPublicationAfterArchivingSource(t *testing.T) {
	makeMessage := func(id, receivedAt string) GmailMessage {
		text := "1. 今日の会話\n" + id + "\n2. 今日のまとめ\nsummary\n3. ルミナの感想\nreflection"
		digest := sha256.Sum256([]byte(text))
		return GmailMessage{ID: id, ThreadID: "d4e5f6", Subject: "【ルミナとれんの会話記録】2025-08-16", From: "nyukimi@gmail.com", ReceivedAt: receivedAt, Text: text, ContentHash: hex.EncodeToString(digest[:])}
	}
	receipts, archive, board := newGmailIntakeReceiptMemory(), &diaryArchiveWriterStub{}, &diaryBoardWriterStub{putErr: errors.New("response lost")}
	g := NewGmailIntake("nyukimin0811@gmail.com", "ren", nil, nil, receipts, nil).WithPersonalArchiveWriter(archive).WithDiaryWriter(board)
	first := makeMessage("a1b2c3", "2026-11-18T01:00:00Z")
	if _, _, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), first); err == nil {
		t.Fatal("unknown BOARD acknowledgment unexpectedly succeeded")
	}
	board.putErr = nil
	board.diary.Revision++
	board.diary.Reflection += "\nowner edit"
	beforePuts := board.puts
	second := makeMessage("f1e2d3", "2026-11-18T02:00:00Z")
	if _, status, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), second); err != nil || status != "blocked" {
		t.Fatalf("new same-date source status=%q err=%v", status, err)
	}
	firstReceipt, _, _ := receipts.Get(context.Background(), g.account, first.ID)
	secondReceipt, _, _ := receipts.Get(context.Background(), g.account, second.ID)
	if board.puts != beforePuts || !firstReceipt.ArchiveSaved || firstReceipt.DiarySaved || secondReceipt.Status != "prepared" || !secondReceipt.ArchiveSaved || secondReceipt.PreparedDiary.PublicationIntent != nil {
		t.Fatalf("unresolved prior intent was bypassed: puts=%d/%d first=%#v second=%#v", board.puts, beforePuts, firstReceipt, secondReceipt)
	}
}

func TestPendingIntentRecoveryRefreshesExecutionIdentityAndPreservesOrigin(t *testing.T) {
	text := "1. 今日の会話\nsource\n2. 今日のまとめ\nsummary\n3. ルミナの感想\nreflection"
	digest := sha256.Sum256([]byte(text))
	message := GmailMessage{ID: "a1b2c3", ThreadID: "d4e5f6", Subject: "【ルミナとれんの会話記録】2025-08-16", From: "nyukimi@gmail.com", ReceivedAt: "2026-11-18T01:00:00Z", Text: text, ContentHash: hex.EncodeToString(digest[:])}
	receipts, archive, board := newGmailIntakeReceiptMemory(), &diaryArchiveWriterStub{}, &diaryBoardWriterStub{putErr: errors.New("response lost")}
	g := NewGmailIntake("nyukimin0811@gmail.com", "ren", &diaryBackfillCollectorStub{pages: map[string]GmailPage{}}, nil, receipts, nil).WithPersonalArchiveWriter(archive).WithDiaryWriter(board)
	ctx1 := gmailIntakeAgentContext(t, "ren")
	if _, _, _, err := g.processDiary(ctx1, message); err == nil {
		t.Fatal("unknown BOARD acknowledgment unexpectedly succeeded")
	}
	before, found, err := receipts.Get(context.Background(), g.account, message.ID)
	if err != nil || !found || before.PreparedDiary.PublicationIntent == nil {
		t.Fatalf("pending receipt missing: found=%t err=%v", found, err)
	}
	archiveOriginal := before.PreparedDiary.Archive
	intentTarget := before.PreparedDiary.PublicationIntent.Target
	ctx2 := gmailIntakeAgentContext(t, "ren")
	identity2, err := domainexecution.IdentityFromContext(ctx2)
	if err != nil {
		t.Fatal(err)
	}
	board.putErr = nil
	if err := g.resolvePendingDiaryPublicationIntents(ctx2); err != nil {
		t.Fatalf("recover pending intent in second execution: %v", err)
	}
	after, found, err := receipts.Get(context.Background(), g.account, message.ID)
	if err != nil || !found || after.Status != "complete" || !after.DiarySaved {
		t.Fatalf("pending receipt not completed: found=%t receipt=%#v err=%v", found, after, err)
	}
	if after.TaskID != string(identity2.TaskID) || after.RunID != string(identity2.RunID) || after.TraceID != string(identity2.TraceID) || after.OriginTaskID != before.OriginTaskID || after.OriginRunID != before.OriginRunID || after.OriginTraceID != before.OriginTraceID {
		t.Fatalf("execution identity transition invalid: before=%#v after=%#v current=%#v", before, after, identity2)
	}
	if !reflect.DeepEqual(after.PreparedDiary.Archive, archiveOriginal) || !sameDiary(board.diary, intentTarget) || after.PreparedDiary.PublicationIntent != nil {
		t.Fatalf("recovery altered original/archive/target contract: receipt=%#v board=%#v", after.PreparedDiary, board.diary)
	}
}

func TestNormalGmailRunDoesNotDependOnBOARDForUnrelatedDirectMail(t *testing.T) {
	text := "1. 今日の会話\nsource\n2. 今日のまとめ\nsummary\n3. ルミナの感想\nreflection"
	digest := sha256.Sum256([]byte(text))
	diaryMessage := GmailMessage{ID: "a1b2c3", ThreadID: "d4e5f6", Subject: "【ルミナとれんの会話記録】2025-08-16", From: "nyukimi@gmail.com", ReceivedAt: "2026-11-18T01:00:00Z", Text: text, ContentHash: hex.EncodeToString(digest[:])}
	receipts, archive, board := newGmailIntakeReceiptMemory(), &diaryArchiveWriterStub{}, &diaryBoardWriterStub{putErr: errors.New("response lost")}
	directMessage := gmailIntakeMessage("RenCrow Backlog specification", "new backlog details", "d4e5f6")
	collector := &gmailIntakeCollectorStub{page: GmailPage{Schema: gmailSchema, Account: "nyukimin0811@gmail.com", Query: GmailQuery, Messages: []GmailMessage{directMessage}}}
	items := &memoryItemStore{}
	atlas := appbacklog.NewService(items, nil)
	g := NewGmailIntake("nyukimin0811@gmail.com", "ren", collector, nil, receipts, atlas).WithPersonalArchiveWriter(archive).WithDiaryWriter(board)
	if _, _, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), diaryMessage); err == nil {
		t.Fatal("unknown diary BOARD acknowledgment unexpectedly succeeded")
	}
	board.getErr = errors.New("BOARD unavailable")
	report, err := g.Run(gmailIntakeAgentContext(t, "ren"))
	if err != nil || report.Processed != 1 || report.Created != 1 || collector.calls != 1 || len(items.items) != 1 {
		t.Fatalf("unrelated direct intake depended on BOARD: report=%#v collector=%d items=%d err=%v", report, collector.calls, len(items.items), err)
	}
}

func TestDiaryPublicationUsesObservedRemoteRevisionNotSourceCount(t *testing.T) {
	makeMessage := func(id, receivedAt, content string) GmailMessage {
		text := "1. 今日の会話\n" + content + "\n2. 今日のまとめ\nsummary\n3. ルミナの感想\nreflection"
		digest := sha256.Sum256([]byte(text))
		return GmailMessage{ID: id, ThreadID: "d4e5f6", Subject: "【ルミナとれんの会話記録】2025-08-16", From: "nyukimi@gmail.com", ReceivedAt: receivedAt, Text: text, ContentHash: hex.EncodeToString(digest[:])}
	}
	receipts, archive, board := newGmailIntakeReceiptMemory(), &diaryArchiveWriterStub{}, &diaryBoardWriterStub{}
	g := NewGmailIntake("nyukimin0811@gmail.com", "ren", nil, nil, receipts, nil).WithPersonalArchiveWriter(archive).WithDiaryWriter(board)
	first := makeMessage("a1b2c3", "2026-11-18T01:00:00Z", "first")
	if _, status, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), first); err != nil || status != "complete" {
		t.Fatalf("baseline source status=%q err=%v", status, err)
	}
	board.diary.Revision = 7 // Existing compatible BOARD revisions need not equal source count.
	second := makeMessage("f1e2d3", "2026-11-18T02:00:00Z", "second")
	if _, status, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), second); err != nil || status != "complete" {
		t.Fatalf("second source status=%q err=%v", status, err)
	}
	if board.diary.Revision != 8 || !strings.Contains(board.diary.Body, "first") || !strings.Contains(board.diary.Body, "second") {
		t.Fatalf("target did not use observed revision + 1: %#v", board.diary)
	}
}

func TestLegacyOwnerResolvedDiarySetAcknowledgesAtObservedRevisionWithoutPut(t *testing.T) {
	makeMessage := func(id, receivedAt, content string) GmailMessage {
		text := "1. 今日の会話\n" + content + "\n2. 今日のまとめ\nsummary\n3. ルミナの感想\nreflection"
		digest := sha256.Sum256([]byte(text))
		return GmailMessage{ID: id, ThreadID: "d4e5f6", Subject: "【ルミナとれんの会話記録】2025-08-16", From: "nyukimi@gmail.com", ReceivedAt: receivedAt, Text: text, ContentHash: hex.EncodeToString(digest[:])}
	}
	receipts, archive, board := newGmailIntakeReceiptMemory(), &diaryArchiveWriterStub{}, &diaryBoardWriterStub{}
	g := NewGmailIntake("nyukimin0811@gmail.com", "ren", nil, nil, receipts, nil).WithPersonalArchiveWriter(archive).WithDiaryWriter(board)
	first := makeMessage("a1b2c3", "2026-11-18T01:00:00Z", "first")
	second := makeMessage("f1e2d3", "2026-11-18T02:00:00Z", "second")
	for _, message := range []GmailMessage{first, second} {
		if _, status, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), message); err != nil || status != "complete" {
			t.Fatalf("source status=%q err=%v", status, err)
		}
	}
	board.diary.Revision = 9
	secondReceipt, found, err := receipts.Get(context.Background(), g.account, second.ID)
	if err != nil || !found {
		t.Fatalf("read source receipt: found=%t err=%v", found, err)
	}
	secondReceipt.Status, secondReceipt.Reason, secondReceipt.DiarySaved = "blocked", "diary_remote_conflict", false
	if err := receipts.Save(context.Background(), secondReceipt); err != nil {
		t.Fatalf("prepare legacy conflict receipt: %v", err)
	}
	beforePuts := board.puts
	if _, status, _, err := g.processMessage(gmailIntakeAgentContext(t, "ren"), second); err != nil || status != "complete" {
		t.Fatalf("legacy revision-nine recovery status=%q err=%v", status, err)
	}
	if board.puts != beforePuts {
		t.Fatalf("legacy exact owner payload was rewritten: PUTs=%d before=%d", board.puts, beforePuts)
	}
}

func TestComposedDiaryOverBOARDCombinedLimitIsBlockedAfterArchive(t *testing.T) {
	makeMessage := func(id string, receivedAt string) GmailMessage {
		text := "1. 今日の会話\n" + strings.Repeat("b", 38<<10) + "\n2. 今日のまとめ\nsummary\n3. ルミナの感想\n" + strings.Repeat("r", 30<<10)
		digest := sha256.Sum256([]byte(text))
		return GmailMessage{ID: id, ThreadID: "d4e5f6", Subject: "【ルミナとれんの会話記録】2025-08-16", From: "nyukimi@gmail.com", ReceivedAt: receivedAt, Text: text, ContentHash: hex.EncodeToString(digest[:])}
	}
	receipts, archive, board := newGmailIntakeReceiptMemory(), &diaryArchiveWriterStub{}, &diaryBoardWriterStub{}
	g := NewGmailIntake("nyukimin0811@gmail.com", "ren", nil, nil, receipts, nil).WithPersonalArchiveWriter(archive).WithDiaryWriter(board)
	for index, message := range []GmailMessage{makeMessage("a1b2c3", "2026-11-18T01:00:00Z"), makeMessage("f1e2d3", "2026-11-18T02:00:00Z")} {
		_, status, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), message)
		if index == 0 && (err != nil || status != "complete") {
			t.Fatalf("first source status=%q err=%v", status, err)
		}
		if index == 1 && (err != nil || status != "blocked") {
			t.Fatalf("oversized composition status=%q err=%v", status, err)
		}
	}
	receipt, found, err := receipts.Get(context.Background(), g.account, "f1e2d3")
	if err != nil || !found || receipt.Reason != "diary_publication_payload_too_large" || !receipt.ArchiveSaved || receipt.DiarySaved || archive.calls != 2 || board.puts != 1 {
		t.Fatalf("oversized publication lost or retried source: receipt=%#v found=%t archive=%d puts=%d err=%v", receipt, found, archive.calls, board.puts, err)
	}
}

func TestDiarySourceScopeRejectsCrossOwnerMemberAndPendingIntent(t *testing.T) {
	makeMessage := func(id string, receivedAt string) GmailMessage {
		text := "1. 今日の会話\nsource\n2. 今日のまとめ\nsummary\n3. ルミナの感想\nreflection"
		digest := sha256.Sum256([]byte(text))
		return GmailMessage{ID: id, ThreadID: "d4e5f6", Subject: "【ルミナとれんの会話記録】2025-08-16", From: "nyukimi@gmail.com", ReceivedAt: receivedAt, Text: text, ContentHash: hex.EncodeToString(digest[:])}
	}
	t.Run("acknowledged member", func(t *testing.T) {
		receipts, archive, board := newGmailIntakeReceiptMemory(), &diaryArchiveWriterStub{}, &diaryBoardWriterStub{}
		g := NewGmailIntake("nyukimin0811@gmail.com", "ren", nil, nil, receipts, nil).WithPersonalArchiveWriter(archive).WithDiaryWriter(board)
		first := makeMessage("a1b2c3", "2026-11-18T01:00:00Z")
		if _, status, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), first); err != nil || status != "complete" {
			t.Fatalf("first source status=%q err=%v", status, err)
		}
		member, _, _ := receipts.Get(context.Background(), g.account, first.ID)
		member.PreparedDiary.Archive.UserID = "other-owner"
		if err := receipts.Save(context.Background(), member); err != nil {
			t.Fatalf("save cross-owner member fixture: %v", err)
		}
		beforePuts := board.puts
		_, _, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), makeMessage("f1e2d3", "2026-11-18T02:00:00Z"))
		if err == nil || board.puts != beforePuts {
			t.Fatalf("cross-owner member allowed publication: err=%v puts=%d before=%d", err, board.puts, beforePuts)
		}
	})
	t.Run("pending intent", func(t *testing.T) {
		receipts, archive, board := newGmailIntakeReceiptMemory(), &diaryArchiveWriterStub{}, &diaryBoardWriterStub{putErr: errors.New("response lost")}
		g := NewGmailIntake("nyukimin0811@gmail.com", "ren", &diaryBackfillCollectorStub{pages: map[string]GmailPage{}}, nil, receipts, nil).WithPersonalArchiveWriter(archive).WithDiaryWriter(board)
		message := makeMessage("a1b2c3", "2026-11-18T01:00:00Z")
		if _, _, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), message); err == nil {
			t.Fatal("unknown BOARD acknowledgment unexpectedly succeeded")
		}
		pending, found, err := receipts.Get(context.Background(), g.account, message.ID)
		if err != nil || !found || pending.PreparedDiary.PublicationIntent == nil {
			t.Fatalf("pending intent missing: found=%t err=%v", found, err)
		}
		pending.PreparedDiary.Archive.UserID = "other-owner"
		if err := receipts.Save(context.Background(), pending); err != nil {
			t.Fatalf("save cross-owner pending fixture: %v", err)
		}
		beforePuts, beforeSaves := board.puts, receipts.saveCalls
		if _, err := g.RunDiaryBackfill(gmailIntakeAgentContext(t, "ren")); err == nil {
			t.Fatal("cross-owner pending intent was acknowledged")
		}
		latest, _, _ := receipts.Get(context.Background(), g.account, message.ID)
		if board.puts != beforePuts || receipts.saveCalls != beforeSaves {
			t.Fatalf("cross-owner pending intent caused write or ACK: puts=%d saves=%d", board.puts, receipts.saveCalls)
		}
		if latest.DiarySaved {
			t.Fatal("cross-owner pending receipt was ACKed")
		}
	})
}

func TestDiaryCompositionAcceptsExactBOARDCombinedBoundary(t *testing.T) {
	makeReceipt := func(id, receivedAt string, bodyLen, reflectionLen int) GmailReceipt {
		text := "1. 今日の会話\n" + strings.Repeat("b", bodyLen) + "\n2. 今日のまとめ\nsummary\n3. ルミナの感想\n" + strings.Repeat("r", reflectionLen)
		digest := sha256.Sum256([]byte(text))
		message := GmailMessage{ID: id, ThreadID: "d4e5f6", Subject: "【ルミナとれんの会話記録】2025-08-16", From: "nyukimi@gmail.com", ReceivedAt: receivedAt, Text: text, ContentHash: hex.EncodeToString(digest[:])}
		parsed, err := parseGmailDiary(message)
		if err != nil {
			t.Fatal(err)
		}
		return GmailReceipt{Message: message, PreparedDiary: &GmailPreparedDiary{Diary: GmailDiary{ActorID: "lumina", Date: parsed.Date, Revision: 1, Body: parsed.Body, Reflection: parsed.Reflection}}}
	}
	first, second := makeReceipt("a1b2c3", "2026-11-18T01:00:00Z", 1000, 1000), makeReceipt("f1e2d3", "2026-11-18T02:00:00Z", 1000, 1000)
	initial, _, err := composeDiaryReceipts([]GmailReceipt{first, second})
	if err != nil {
		t.Fatal(err)
	}
	delta := maxGmailDiaryCombinedTextBytes - len(initial.Body) - len(initial.Reflection)
	firstAdd, secondAdd := delta/2, delta-delta/2
	first = makeReceipt("a1b2c3", "2026-11-18T01:00:00Z", 1000, 1000+firstAdd)
	second = makeReceipt("f1e2d3", "2026-11-18T02:00:00Z", 1000, 1000+secondAdd)
	got, _, err := composeDiaryReceipts([]GmailReceipt{first, second})
	if err != nil || len(got.Body)+len(got.Reflection) != maxGmailDiaryCombinedTextBytes {
		t.Fatalf("exact BOARD payload boundary got=%d err=%v", len(got.Body)+len(got.Reflection), err)
	}
	second = makeReceipt("f1e2d3", "2026-11-18T02:00:00Z", 1000, 1001+secondAdd)
	if _, _, err := composeDiaryReceipts([]GmailReceipt{first, second}); !errors.Is(err, errGmailDiaryPayloadTooLarge) {
		t.Fatalf("over-boundary payload error=%v", err)
	}
}

func TestPreparedDiaryUnknownBoardAckResolvesBeforeNewSource(t *testing.T) {
	makeMessage := func(id, receivedAt, text string) GmailMessage {
		digest := sha256.Sum256([]byte(text))
		return GmailMessage{ID: id, ThreadID: "d4e5f6", Subject: "【ルミナとれんの会話記録】2025-08-16", From: "nyukimi@gmail.com", ReceivedAt: receivedAt, Text: text, ContentHash: hex.EncodeToString(digest[:])}
	}
	first := makeMessage("a1b2c3", "2026-11-18T01:00:00Z", "1. 今日の会話\nfirst\n2. 今日のまとめ\nfirst summary\n3. ルミナの感想\nfirst reflection")
	second := makeMessage("f1e2d3", "2026-11-18T02:00:00Z", "1. 今日の会話\nsecond\n2. 今日のまとめ\nsecond summary\n3. ルミナの感想\nsecond reflection")
	receipts, archive, board := newGmailIntakeReceiptMemory(), &diaryArchiveWriterStub{}, &diaryBoardWriterStub{putErr: errors.New("response lost after BOARD commit")}
	g := NewGmailIntake("nyukimin0811@gmail.com", "ren", nil, nil, receipts, nil).WithPersonalArchiveWriter(archive).WithDiaryWriter(board)
	if _, _, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), first); err == nil {
		t.Fatal("unknown BOARD acknowledgement was accepted")
	}
	pending, found, err := receipts.Get(context.Background(), g.account, first.ID)
	if err != nil || !found || pending.Status != "prepared" || pending.DiarySaved {
		t.Fatalf("unknown acknowledgement was not durable: found=%t status=%q ack=%t err=%v", found, pending.Status, pending.DiarySaved, err)
	}
	board.putErr = nil
	if _, status, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), second); err != nil || status != "complete" {
		t.Fatalf("new source did not resume prior intent first: status=%q err=%v", status, err)
	}
	firstReceipt, found, err := receipts.Get(context.Background(), g.account, first.ID)
	if err != nil || !found || firstReceipt.Status != "complete" || !firstReceipt.DiarySaved {
		t.Fatalf("prior intent was not acknowledged: found=%t status=%q ack=%t err=%v", found, firstReceipt.Status, firstReceipt.DiarySaved, err)
	}
	if board.puts != 2 || archive.calls != 2 || board.diary.Revision != 2 || !strings.Contains(board.diary.Body, "first summary") || !strings.Contains(board.diary.Body, "second summary") {
		t.Fatalf("unexpected recovery writes: puts=%d archive=%d revision=%d", board.puts, archive.calls, board.diary.Revision)
	}
}

func TestLegacyOwnerResolvedDiaryRevisionTwoCanRecoverReceiptsByReadback(t *testing.T) {
	makeMessage := func(id, receivedAt, text string) GmailMessage {
		digest := sha256.Sum256([]byte(text))
		return GmailMessage{ID: id, ThreadID: "d4e5f6", Subject: "【ルミナとれんの会話記録】2025-08-16", From: "nyukimi@gmail.com", ReceivedAt: receivedAt, Text: text, ContentHash: hex.EncodeToString(digest[:])}
	}
	first := makeMessage("a1b2c3", "2026-11-18T01:00:00Z", "1. 今日の会話\nfirst\n2. 今日のまとめ\nfirst summary\n3. ルミナの感想\nfirst reflection")
	second := makeMessage("f1e2d3", "2026-11-18T02:00:00Z", "1. 今日の会話\nsecond\n2. 今日のまとめ\nsecond summary\n3. ルミナの感想\nsecond reflection")
	receipts, archive, board := newGmailIntakeReceiptMemory(), &diaryArchiveWriterStub{}, &diaryBoardWriterStub{}
	g := NewGmailIntake("nyukimin0811@gmail.com", "ren", nil, nil, receipts, nil).WithPersonalArchiveWriter(archive).WithDiaryWriter(board)
	if _, status, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), first); err != nil || status != "complete" {
		t.Fatalf("first diary status=%q err=%v", status, err)
	}
	if _, status, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), second); err != nil || status != "complete" {
		t.Fatalf("second diary status=%q err=%v", status, err)
	}
	secondReceipt, found, err := receipts.Get(context.Background(), g.account, second.ID)
	if err != nil || !found {
		t.Fatalf("read legacy receipt: found=%t err=%v", found, err)
	}
	secondReceipt.Status, secondReceipt.Reason, secondReceipt.DiarySaved = "blocked", "diary_remote_conflict", false
	secondReceipt.PreparedDiary.PublicationIntent = nil
	if err := receipts.Save(context.Background(), secondReceipt); err != nil {
		t.Fatalf("prepare legacy conflict receipt: %v", err)
	}
	// The owner has already resolved the date to the agreed two-source revision.
	board.diary = GmailDiary{ActorID: "lumina", Date: "2025-08-16", Revision: 2,
		Body:       "【メール1／受信: 2026-11-18 10:00:00 JST】\n1. 今日の会話\nfirst\n\n2. 今日のまとめ\nfirst summary\n\n【メール2／受信: 2026-11-18 11:00:00 JST】\n1. 今日の会話\nsecond\n\n2. 今日のまとめ\nsecond summary",
		Reflection: "【メール1／受信: 2026-11-18 10:00:00 JST】\n3. ルミナの感想\nfirst reflection\n\n【メール2／受信: 2026-11-18 11:00:00 JST】\n3. ルミナの感想\nsecond reflection"}
	board.found = true
	board.puts = 0
	if _, status, _, err := g.processMessage(gmailIntakeAgentContext(t, "ren"), second); err != nil || status != "complete" {
		t.Fatalf("owner-resolved readback status=%q err=%v", status, err)
	}
	got, found, err := receipts.Get(context.Background(), g.account, second.ID)
	if err != nil || !found || got.Status != "complete" || !got.ArchiveSaved || !got.DiarySaved {
		t.Fatalf("legacy receipt was not ACK-recovered: found=%t status=%q archive=%t diary=%t err=%v", found, got.Status, got.ArchiveSaved, got.DiarySaved, err)
	}
	if board.gets == 0 || board.puts != 0 || archive.calls != 2 {
		t.Fatalf("readback recovery wrote a duplicate: GET=%d PUT=%d archive=%d", board.gets, board.puts, archive.calls)
	}
}

func TestDiarySinkFailuresResumeOnlyTheUnacknowledgedSideEffect(t *testing.T) {
	makeMessage := func(id string) GmailMessage {
		text := "1. 今日の会話\n会話\n2. 今日のまとめ\nまとめ\n3. ルミナの感想\n感想"
		digest := sha256.Sum256([]byte(text))
		return GmailMessage{ID: id, ThreadID: "d4e5f6", Subject: "【ルミナとれんの会話記録】2026-11-15", From: "nyukimi@gmail.com", ReceivedAt: "2026-11-16T01:02:03Z", Text: text, ContentHash: hex.EncodeToString(digest[:])}
	}
	t.Run("archive retries before board is touched", func(t *testing.T) {
		message := makeMessage("a1b2c3")
		receipts, archive, board := newGmailIntakeReceiptMemory(), &diaryArchiveWriterStub{failFirst: 1}, &diaryBoardWriterStub{}
		g := NewGmailIntake("nyukimin0811@gmail.com", "ren", nil, nil, receipts, nil).WithPersonalArchiveWriter(archive).WithDiaryWriter(board)
		ctx := gmailIntakeAgentContext(t, "ren")
		if _, _, _, err := g.processDiary(ctx, message); err == nil {
			t.Fatal("archive sink failure was swallowed")
		}
		prepared, found, err := receipts.Get(context.Background(), g.account, message.ID)
		if err != nil || !found || prepared.Status != "prepared" || prepared.ArchiveSaved || board.puts != 0 {
			t.Fatalf("unexpected pre-retry checkpoint: %#v found=%t err=%v board puts=%d", prepared, found, err, board.puts)
		}
		if _, status, _, err := g.processMessage(gmailIntakeAgentContext(t, "ren"), message); err != nil || status != "complete" {
			t.Fatalf("archive retry status=%q err=%v", status, err)
		}
		if archive.calls != 2 || board.puts != 1 {
			t.Fatalf("retry duplicated/dropped sinks: archive=%d board=%d", archive.calls, board.puts)
		}
	})
	t.Run("unknown board write outcome links existing identical diary", func(t *testing.T) {
		message := makeMessage("f1e2d3")
		receipts, archive, board := newGmailIntakeReceiptMemory(), &diaryArchiveWriterStub{}, &diaryBoardWriterStub{putErr: errors.New("synthetic response loss")}
		g := NewGmailIntake("nyukimin0811@gmail.com", "ren", nil, nil, receipts, nil).WithPersonalArchiveWriter(archive).WithDiaryWriter(board)
		if _, _, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), message); err == nil {
			t.Fatal("unknown BOARD write outcome was swallowed")
		}
		prepared, found, err := receipts.Get(context.Background(), g.account, message.ID)
		if err != nil || !found || prepared.Status != "prepared" || !prepared.ArchiveSaved || prepared.DiarySaved {
			t.Fatalf("unexpected board retry checkpoint: %#v found=%t err=%v", prepared, found, err)
		}
		if _, status, _, err := g.processMessage(gmailIntakeAgentContext(t, "ren"), message); err != nil || status != "complete" {
			t.Fatalf("BOARD retry status=%q err=%v", status, err)
		}
		if archive.calls != 1 || board.puts != 1 {
			t.Fatalf("BOARD retry repeated acknowledged sink: archive=%d board=%d", archive.calls, board.puts)
		}
	})
}

func TestOldPreparedDiaryReceiptUsesExactBOARDReadbackWithoutIntent(t *testing.T) {
	text := "1. 今日の会話\n会話\n2. 今日のまとめ\nまとめ\n3. ルミナの感想\n感想"
	digest := sha256.Sum256([]byte(text))
	message := GmailMessage{ID: "a1b2c3", ThreadID: "d4e5f6", Subject: "【ルミナとれんの会話記録】2026-11-15", From: "nyukimi@gmail.com", ReceivedAt: "2026-11-16T01:02:03Z", Text: text, ContentHash: hex.EncodeToString(digest[:])}
	t.Run("exact source is readback acknowledged", func(t *testing.T) {
		receipts, archive, board := newGmailIntakeReceiptMemory(), &diaryArchiveWriterStub{}, &diaryBoardWriterStub{}
		g := NewGmailIntake("nyukimin0811@gmail.com", "ren", nil, nil, receipts, nil).WithPersonalArchiveWriter(archive).WithDiaryWriter(board)
		if _, status, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), message); err != nil || status != "complete" {
			t.Fatalf("initial source status=%q err=%v", status, err)
		}
		receipt, _, _ := receipts.Get(context.Background(), g.account, message.ID)
		receipt.Status, receipt.Reason, receipt.DiarySaved = "prepared", "", false
		receipt.PolicyRevision = "gmail-daily-routing-v4"
		receipt.PreparedDiary.PublicationIntent = nil
		if err := receipts.Save(context.Background(), receipt); err != nil {
			t.Fatalf("prepare old-format receipt: %v", err)
		}
		beforePuts, beforeArchive := board.puts, archive.calls
		if _, status, _, err := g.processMessage(gmailIntakeAgentContext(t, "ren"), message); err != nil || status != "complete" {
			t.Fatalf("old-format exact readback status=%q err=%v", status, err)
		}
		recovered, _, _ := receipts.Get(context.Background(), g.account, message.ID)
		if !recovered.DiarySaved || recovered.Status != "complete" || recovered.PreparedDiary.PublicationIntent != nil || board.puts != beforePuts || archive.calls != beforeArchive {
			t.Fatalf("old-format source was not readback-only recovered: receipt=%#v puts=%d/%d archive=%d/%d", recovered, board.puts, beforePuts, archive.calls, beforeArchive)
		}
	})
	t.Run("one-character mismatch is never acknowledged", func(t *testing.T) {
		receipts, archive, board := newGmailIntakeReceiptMemory(), &diaryArchiveWriterStub{}, &diaryBoardWriterStub{}
		g := NewGmailIntake("nyukimin0811@gmail.com", "ren", nil, nil, receipts, nil).WithPersonalArchiveWriter(archive).WithDiaryWriter(board)
		if _, status, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), message); err != nil || status != "complete" {
			t.Fatalf("initial source status=%q err=%v", status, err)
		}
		receipt, _, _ := receipts.Get(context.Background(), g.account, message.ID)
		receipt.Status, receipt.Reason, receipt.DiarySaved = "prepared", "", false
		receipt.PreparedDiary.PublicationIntent = nil
		if err := receipts.Save(context.Background(), receipt); err != nil {
			t.Fatal(err)
		}
		board.diary.Reflection += "!"
		beforePuts := board.puts
		if _, status, _, err := g.processMessage(gmailIntakeAgentContext(t, "ren"), message); err != nil || status != "blocked" {
			t.Fatalf("mismatched readback status=%q err=%v", status, err)
		}
		recovered, _, _ := receipts.Get(context.Background(), g.account, message.ID)
		if recovered.DiarySaved || recovered.Reason != "diary_remote_conflict" || board.puts != beforePuts {
			t.Fatalf("mismatched remote text was ACKed or overwritten: receipt=%#v puts=%d/%d", recovered, board.puts, beforePuts)
		}
	})
}

func TestDiaryReceiptOwnerBindingRejectsChangedOwnerBeforeReplayEffects(t *testing.T) {
	text := "1. 今日の会話\n会話\n2. 今日のまとめ\nまとめ\n3. ルミナの感想\n感想"
	digest := sha256.Sum256([]byte(text))
	message := GmailMessage{ID: "a1b2c3", ThreadID: "d4e5f6", Subject: "【ルミナとれんの会話記録】2026-11-15", From: "nyukimi@gmail.com", ReceivedAt: "2026-11-16T01:02:03Z", Text: text, ContentHash: hex.EncodeToString(digest[:])}

	t.Run("prepared receipt rejects configured owner change and direct resume", func(t *testing.T) {
		receipts, archive, board := newGmailIntakeReceiptMemory(), &diaryArchiveWriterStub{failFirst: 1}, &diaryBoardWriterStub{}
		ownerA := NewGmailIntake("nyukimin0811@gmail.com", "owner-a", nil, nil, receipts, nil).WithPersonalArchiveWriter(archive).WithDiaryWriter(board)
		if _, _, _, err := ownerA.processDiary(gmailIntakeAgentContext(t, "owner-a"), message); err == nil {
			t.Fatal("synthetic archive failure was swallowed")
		}
		prepared, found, err := receipts.Get(context.Background(), ownerA.account, message.ID)
		if err != nil || !found || prepared.Status != "prepared" || prepared.PreparedDiary.Archive.UserID != "owner-a" {
			t.Fatalf("prepared owner-A receipt=%#v found=%t err=%v", prepared, found, err)
		}
		ownerBArchive, ownerBBoard := &diaryArchiveWriterStub{}, &diaryBoardWriterStub{}
		ownerB := NewGmailIntake(ownerA.account, "owner-b", nil, nil, receipts, nil).WithPersonalArchiveWriter(ownerBArchive).WithDiaryWriter(ownerBBoard)
		if _, _, _, err := ownerB.processMessage(gmailIntakeAgentContext(t, "owner-b"), message); err == nil {
			t.Fatal("prepared owner-A receipt replayed under owner B")
		}
		if _, _, _, err := ownerB.resumePreparedDiary(gmailIntakeAgentContext(t, "owner-b"), prepared); err == nil {
			t.Fatal("direct prepared diary resume accepted owner mismatch")
		}
		if ownerBArchive.calls != 0 || ownerBBoard.puts != 0 || archive.calls != 1 || board.puts != 0 {
			t.Fatalf("owner mismatch caused side effects: replay archive=%d board=%d original archive=%d board=%d", ownerBArchive.calls, ownerBBoard.puts, archive.calls, board.puts)
		}
	})

	t.Run("complete receipt rejects terminal replay under changed owner", func(t *testing.T) {
		receipts := newGmailIntakeReceiptMemory()
		ownerAArchive, ownerABoard := &diaryArchiveWriterStub{}, &diaryBoardWriterStub{}
		ownerA := NewGmailIntake("nyukimin0811@gmail.com", "owner-a", nil, nil, receipts, nil).WithPersonalArchiveWriter(ownerAArchive).WithDiaryWriter(ownerABoard)
		if _, status, _, err := ownerA.processDiary(gmailIntakeAgentContext(t, "owner-a"), message); err != nil || status != "complete" {
			t.Fatalf("owner-A source status=%q err=%v", status, err)
		}
		complete, found, err := receipts.Get(context.Background(), ownerA.account, message.ID)
		if err != nil || !found || complete.Status != "complete" {
			t.Fatalf("complete owner-A receipt=%#v found=%t err=%v", complete, found, err)
		}
		ownerBArchive, ownerBBoard := &diaryArchiveWriterStub{}, &diaryBoardWriterStub{}
		ownerB := NewGmailIntake(ownerA.account, "owner-b", nil, nil, receipts, nil).WithPersonalArchiveWriter(ownerBArchive).WithDiaryWriter(ownerBBoard)
		if _, _, _, err := ownerB.processMessage(gmailIntakeAgentContext(t, "owner-b"), message); err == nil {
			t.Fatal("complete owner-A receipt terminal replay was accepted under owner B")
		}
		if _, _, _, err := ownerB.resumePreparedDiary(gmailIntakeAgentContext(t, "owner-b"), complete); err == nil {
			t.Fatal("direct complete diary resume accepted owner mismatch")
		}
		if ownerBArchive.calls != 0 || ownerBBoard.puts != 0 {
			t.Fatalf("terminal owner mismatch caused side effects: archive=%d board=%d", ownerBArchive.calls, ownerBBoard.puts)
		}
	})
}
