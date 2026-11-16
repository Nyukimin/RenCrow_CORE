package gmailintake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	appbacklog "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
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
	puts   int
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
	return s.diary, s.found, nil
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
	if archive.calls != 2 || board.puts != 1 {
		t.Fatalf("backfill sinks archive=%d board=%d", archive.calls, board.puts)
	}
}

func TestSameDateDifferentDiaryPayloadBlocksAndKeepsProtectedArchive(t *testing.T) {
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
	if _, status, _, err := g.processDiary(gmailIntakeAgentContext(t, "ren"), second); err != nil || status != "blocked" {
		t.Fatalf("conflicting diary status=%q err=%v", status, err)
	}
	receipt, found, err := receipts.Get(context.Background(), "nyukimin0811@gmail.com", second.ID)
	if err != nil || !found || receipt.Status != "blocked" || receipt.Reason != "diary_remote_conflict" || !receipt.ArchiveSaved || receipt.DiarySaved {
		t.Fatalf("conflict receipt=%#v found=%t err=%v", receipt, found, err)
	}
	if archive.calls != 2 || board.puts != 1 {
		t.Fatalf("conflict overwrote or dropped source: archive=%d board writes=%d", archive.calls, board.puts)
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
