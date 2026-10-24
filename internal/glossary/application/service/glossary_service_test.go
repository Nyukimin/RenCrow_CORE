package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/glossary/domain/entity"
)

type fakeGlossaryRepository struct {
	saved      *entity.GlossaryItem
	saveErr    error
	byTerm     *entity.GlossaryItem
	recent     []*entity.GlossaryItem
	byCategory []*entity.GlossaryItem
}

func (f *fakeGlossaryRepository) Save(ctx context.Context, item *entity.GlossaryItem) error {
	f.saved = item
	return f.saveErr
}

func (f *fakeGlossaryRepository) FindByTerm(ctx context.Context, term string) (*entity.GlossaryItem, error) {
	return f.byTerm, nil
}

func (f *fakeGlossaryRepository) FindRecent(ctx context.Context, limit int) ([]*entity.GlossaryItem, error) {
	return f.recent, nil
}

func (f *fakeGlossaryRepository) FindByCategory(ctx context.Context, category string, limit int) ([]*entity.GlossaryItem, error) {
	return f.byCategory, nil
}

func (f *fakeGlossaryRepository) Delete(ctx context.Context, id string) error {
	return nil
}

func TestGlossaryServiceAddAndQueries(t *testing.T) {
	repo := &fakeGlossaryRepository{
		byTerm:     entity.NewGlossaryItem("Mio", "chat agent", "manual", "agent"),
		recent:     []*entity.GlossaryItem{entity.NewGlossaryItem("Shiro", "worker", "manual", "agent")},
		byCategory: []*entity.GlossaryItem{entity.NewGlossaryItem("Aka", "coder", "manual", "agent")},
	}
	svc := NewGlossaryService(repo)

	added, err := svc.AddGlossaryItem(context.Background(), "RenCrow", "assistant", "manual", "project")
	if err != nil {
		t.Fatalf("AddGlossaryItem failed: %v", err)
	}
	if repo.saved != added || added.Term != "RenCrow" || added.Category != "project" {
		t.Fatalf("unexpected saved item: %#v", repo.saved)
	}

	found, err := svc.SearchByTerm(context.Background(), "Mio")
	if err != nil || found.Term != "Mio" {
		t.Fatalf("SearchByTerm = %#v, %v", found, err)
	}
	recent, err := svc.GetRecentGlossary(context.Background(), 1)
	if err != nil || len(recent) != 1 || recent[0].Term != "Shiro" {
		t.Fatalf("GetRecentGlossary = %#v, %v", recent, err)
	}
	category, err := svc.GetByCategory(context.Background(), "agent", 1)
	if err != nil || len(category) != 1 || category[0].Term != "Aka" {
		t.Fatalf("GetByCategory = %#v, %v", category, err)
	}
}

func TestGlossaryServiceAddReturnsSaveError(t *testing.T) {
	saveErr := errors.New("save failed")
	svc := NewGlossaryService(&fakeGlossaryRepository{saveErr: saveErr})

	item, err := svc.AddGlossaryItem(context.Background(), "RenCrow", "assistant", "manual", "project")
	if !errors.Is(err, saveErr) {
		t.Fatalf("expected save error, got %v", err)
	}
	if item != nil {
		t.Fatalf("item should be nil on save error, got %#v", item)
	}
}

func TestGlossaryPromptSnapshotBoundsItemsAndBytes(t *testing.T) {
	recent := make([]*entity.GlossaryItem, maxRecentPromptItems+4)
	for i := range recent {
		recent[i] = entity.NewGlossaryItem(
			strings.Repeat("語", 100),
			strings.Repeat("説明", 1000),
			strings.Repeat("源", 100),
			"test",
		)
	}
	repo := &fakeGlossaryRepository{recent: recent}
	svc := NewGlossaryService(repo)
	if err := svc.RefreshRecentPromptSnapshot(context.Background()); err != nil {
		t.Fatalf("RefreshRecentPromptSnapshot: %v", err)
	}

	contextText, topics := svc.RecentPromptSnapshot(maxRecentPromptItems + 10)
	if len(topics) == 0 || len(topics) > maxRecentPromptItems {
		t.Fatalf("snapshot item count=%d, want 1..%d", len(topics), maxRecentPromptItems)
	}
	if len(contextText) > maxRecentPromptBytes {
		t.Fatalf("recent context bytes=%d, limit=%d", len(contextText), maxRecentPromptBytes)
	}
	topicBytes := 0
	for _, topic := range topics {
		if len(topic) > maxRecentPromptItemBytes {
			t.Fatalf("topic bytes=%d, per-item limit=%d", len(topic), maxRecentPromptItemBytes)
		}
		topicBytes += len(topic)
	}
	if topicBytes > maxRecentPromptBytes {
		t.Fatalf("total topic bytes=%d, limit=%d", topicBytes, maxRecentPromptBytes)
	}

	topics[0] = "caller mutation"
	_, again := svc.RecentPromptSnapshot(1)
	if len(again) != 1 || again[0] == "caller mutation" {
		t.Fatalf("snapshot topics were mutable through returned slice: %v", again)
	}
}

type delayedRecentRepository struct {
	fakeGlossaryRepository
	mu          sync.Mutex
	recentItems []*entity.GlossaryItem
	readCount   int
	started     chan struct{}
	release     chan struct{}
	before      func()
	after       func(context.Context)
}

func (r *delayedRecentRepository) SetGlossaryMutationHooks(before func(), after func(context.Context)) {
	r.mu.Lock()
	r.before = before
	r.after = after
	r.mu.Unlock()
	return
}

func (r *delayedRecentRepository) Save(ctx context.Context, item *entity.GlossaryItem) error {
	r.mu.Lock()
	before, after := r.before, r.after
	r.mu.Unlock()
	if before != nil {
		before()
	}
	r.mu.Lock()
	r.recentItems = []*entity.GlossaryItem{item}
	r.mu.Unlock()
	if after != nil {
		after(context.WithoutCancel(ctx))
	}
	return nil
}

func (r *delayedRecentRepository) FindRecent(_ context.Context, _ int) ([]*entity.GlossaryItem, error) {
	r.mu.Lock()
	r.readCount++
	read := r.readCount
	items := append([]*entity.GlossaryItem(nil), r.recentItems...)
	if read == 1 {
		close(r.started)
	}
	r.mu.Unlock()
	if read == 1 {
		<-r.release
	}
	return items, nil
}

func TestOlderPromptSnapshotRefreshCannotRepublishAfterMutation(t *testing.T) {
	oldItem := entity.NewGlossaryItem("Old", "stale", "test", "test")
	repo := &delayedRecentRepository{
		recentItems: []*entity.GlossaryItem{oldItem},
		started:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	svc := NewGlossaryService(repo)
	refreshDone := make(chan error, 1)
	go func() {
		refreshDone <- svc.RefreshRecentPromptSnapshot(context.Background())
	}()
	<-repo.started

	if _, err := svc.AddGlossaryItem(context.Background(), "New", "current", "test", "test"); err != nil {
		t.Fatalf("AddGlossaryItem: %v", err)
	}
	close(repo.release)
	if err := <-refreshDone; err != nil {
		t.Fatalf("older RefreshRecentPromptSnapshot: %v", err)
	}

	contextText, topics := svc.RecentPromptSnapshot(5)
	if strings.Contains(contextText, "Old: stale") || len(topics) != 1 || topics[0] != "New: current" {
		t.Fatalf("older refresh republished after mutation: context=%q topics=%v", contextText, topics)
	}
}

type concurrentGlossaryMutationRepository struct {
	fakeGlossaryRepository
	mu                sync.Mutex
	items             []*entity.GlossaryItem
	before            func()
	after             func(context.Context)
	firstWritten      chan struct{}
	allowFirstFinish  chan struct{}
	secondStarted     chan struct{}
	allowSecondCommit chan struct{}
}

func (r *concurrentGlossaryMutationRepository) SetGlossaryMutationHooks(before func(), after func(context.Context)) {
	r.mu.Lock()
	r.before, r.after = before, after
	r.mu.Unlock()
}

func (r *concurrentGlossaryMutationRepository) Save(ctx context.Context, item *entity.GlossaryItem) error {
	r.mu.Lock()
	before, after := r.before, r.after
	r.mu.Unlock()
	if before != nil {
		before()
	}
	switch item.Term {
	case "First":
		r.mu.Lock()
		r.items = append(r.items, item)
		r.mu.Unlock()
		close(r.firstWritten)
		<-r.allowFirstFinish
	case "Second":
		close(r.secondStarted)
		<-r.allowSecondCommit
		r.mu.Lock()
		r.items = append(r.items, item)
		r.mu.Unlock()
	}
	if after != nil {
		after(context.WithoutCancel(ctx))
	}
	return nil
}

func (r *concurrentGlossaryMutationRepository) FindRecent(_ context.Context, _ int) ([]*entity.GlossaryItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*entity.GlossaryItem(nil), r.items...), nil
}

func TestConcurrentGlossaryMutationsKeepSnapshotRevokedUntilAllFinish(t *testing.T) {
	repo := &concurrentGlossaryMutationRepository{
		items:             []*entity.GlossaryItem{entity.NewGlossaryItem("Initial", "stored", "test", "test")},
		firstWritten:      make(chan struct{}),
		allowFirstFinish:  make(chan struct{}),
		secondStarted:     make(chan struct{}),
		allowSecondCommit: make(chan struct{}),
	}
	svc := NewGlossaryService(repo)
	if err := svc.RefreshRecentPromptSnapshot(context.Background()); err != nil {
		t.Fatalf("initial snapshot warm: %v", err)
	}

	firstDone := make(chan error, 1)
	go func() {
		_, err := svc.AddGlossaryItem(context.Background(), "First", "committed first", "test", "test")
		firstDone <- err
	}()
	<-repo.firstWritten

	secondDone := make(chan error, 1)
	go func() {
		_, err := svc.AddGlossaryItem(context.Background(), "Second", "pending second", "test", "test")
		secondDone <- err
	}()
	<-repo.secondStarted

	close(repo.allowFirstFinish)
	if err := <-firstDone; err != nil {
		t.Fatalf("first mutation: %v", err)
	}
	if contextText, topics := svc.RecentPromptSnapshot(5); contextText != "" || len(topics) != 0 {
		t.Fatalf("prompt snapshot republished while second mutation was pending: context=%q topics=%v", contextText, topics)
	}

	close(repo.allowSecondCommit)
	if err := <-secondDone; err != nil {
		t.Fatalf("second mutation: %v", err)
	}
	contextText, topics := svc.RecentPromptSnapshot(5)
	if !strings.Contains(contextText, "First: committed first") || !strings.Contains(contextText, "Second: pending second") || len(topics) != 3 {
		t.Fatalf("final snapshot after concurrent mutations: context=%q topics=%v", contextText, topics)
	}
}

type chatLookupRepository struct {
	fakeGlossaryRepository
	mu              sync.Mutex
	item            *entity.GlossaryItem
	findErr         error
	findCalls       int
	lastQuery       string
	findStarted     chan struct{}
	releaseFind     chan struct{}
	findStartedOnce sync.Once
	before          func()
	after           func(context.Context)
	deleteAttempted chan struct{}
	deleteCommitted chan struct{}
}

func (r *chatLookupRepository) SetGlossaryMutationHooks(before func(), after func(context.Context)) {
	r.mu.Lock()
	r.before, r.after = before, after
	r.mu.Unlock()
}

func (r *chatLookupRepository) FindByTerm(_ context.Context, term string) (*entity.GlossaryItem, error) {
	r.mu.Lock()
	r.findCalls++
	r.lastQuery = term
	item, err := r.item, r.findErr
	started, release := r.findStarted, r.releaseFind
	r.mu.Unlock()
	if started != nil && release != nil {
		r.findStartedOnce.Do(func() { close(started) })
		<-release
	}
	return item, err
}

func (r *chatLookupRepository) FindRecent(_ context.Context, _ int) ([]*entity.GlossaryItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.item == nil {
		return []*entity.GlossaryItem{}, nil
	}
	return []*entity.GlossaryItem{r.item}, nil
}

func (r *chatLookupRepository) Save(ctx context.Context, item *entity.GlossaryItem) error {
	r.mu.Lock()
	before, after := r.before, r.after
	r.mu.Unlock()
	if before != nil {
		before()
	}
	r.mu.Lock()
	r.item = item
	r.mu.Unlock()
	if after != nil {
		after(context.WithoutCancel(ctx))
	}
	return nil
}

func (r *chatLookupRepository) Delete(ctx context.Context, _ string) error {
	r.mu.Lock()
	before, after := r.before, r.after
	deleteAttempted, deleteCommitted := r.deleteAttempted, r.deleteCommitted
	r.mu.Unlock()
	if deleteAttempted != nil {
		close(deleteAttempted)
	}
	if before != nil {
		before()
	}
	r.mu.Lock()
	r.item = nil
	r.mu.Unlock()
	if deleteCommitted != nil {
		close(deleteCommitted)
	}
	if after != nil {
		after(context.WithoutCancel(ctx))
	}
	return nil
}

func TestLookupForChatDistinguishesEmptyNotFoundAndUnavailable(t *testing.T) {
	local := &chatLookupRepository{}
	svc := NewGlossaryService(local)

	if got := svc.LookupForChat(context.Background(), " \t\n"); got.Status != GlossaryChatLookupEmpty || len(got.Records) != 0 {
		t.Fatalf("empty query outcome = %#v", got)
	}
	if got := svc.LookupForChat(context.Background(), strings.Repeat("x", maxChatGlossaryQueryRunes+1)); got.Status != GlossaryChatLookupInvalid {
		t.Fatalf("overlong query outcome = %#v, want invalid_query", got)
	}
	if got := svc.LookupForChat(context.Background(), "missing"); got.Status != GlossaryChatLookupNotFound || got.Qualification != nil {
		t.Fatalf("local miss outcome = %#v, want not_found without qualification", got)
	}

	var nilService *GlossaryService
	if got := nilService.LookupForChat(context.Background(), "term"); got.Status != GlossaryChatLookupUnavailable {
		t.Fatalf("nil service outcome = %#v, want unavailable", got)
	}
	var nilRepository *chatLookupRepository
	if got := NewGlossaryService(nilRepository).LookupForChat(context.Background(), "term"); got.Status != GlossaryChatLookupUnavailable {
		t.Fatalf("typed-nil repository outcome = %#v, want unavailable", got)
	}
	remote := NewGlossaryService(&fakeGlossaryRepository{byTerm: entity.NewGlossaryItem("Term", "definition", "manual", "test")})
	if got := remote.LookupForChat(context.Background(), "Term"); got.Status != GlossaryChatLookupUnavailable || got.Qualification != nil || len(got.Records) != 0 {
		t.Fatalf("positive hookless result = %#v, want fail closed unavailable", got)
	}
	remoteMiss := NewGlossaryService(&fakeGlossaryRepository{})
	if got := remoteMiss.LookupForChat(context.Background(), "missing"); got.Status != GlossaryChatLookupNotFound {
		t.Fatalf("hookless miss outcome = %#v, want not_found", got)
	}
	if local.findCalls != 1 {
		t.Fatalf("empty/invalid query unexpectedly reached repository: calls=%d, want 1", local.findCalls)
	}
}

func TestLookupForChatReturnsExactProvenanceAndRAMQualification(t *testing.T) {
	item := entity.NewGlossaryItem("Mio", "chat agent", "manual", "agent")
	repo := &chatLookupRepository{item: item}
	svc := NewGlossaryService(repo)

	got := svc.LookupForChat(context.Background(), "Mio")
	if got.Status != GlossaryChatLookupFound || len(got.Records) != 1 || got.Qualification == nil {
		t.Fatalf("lookup outcome = %#v, want one qualified record", got)
	}
	if got.Records[0].Reference != item.ID || got.Records[0].Term != item.Term || got.Records[0].Explanation != item.Explanation || got.Records[0].Source != item.Source || got.Records[0].UpdatedAt != item.UpdatedAt {
		t.Fatalf("record lost owner provenance/content: got %#v, item %#v", got.Records[0], item)
	}
	if repo.lastQuery != "Mio" || repo.findCalls != 1 {
		t.Fatalf("owner lookup query/calls = %q/%d, want exact Mio/1", repo.lastQuery, repo.findCalls)
	}

	admitted := false
	if err := got.Qualification.Use(func() error {
		admitted = true
		return nil
	}); err != nil {
		t.Fatalf("Use current qualification: %v", err)
	}
	if !admitted || repo.findCalls != 1 {
		t.Fatalf("RAM admission=%t, repository calls=%d; Use must not read owner DB", admitted, repo.findCalls)
	}
}

func TestLookupForChatMutationDuringReadFailsClosedAndDoesNotHoldLockAcrossDB(t *testing.T) {
	oldItem := entity.NewGlossaryItem("Term", "old definition", "manual", "test")
	repo := &chatLookupRepository{
		item:        oldItem,
		findStarted: make(chan struct{}),
		releaseFind: make(chan struct{}),
	}
	svc := NewGlossaryService(repo)
	lookupDone := make(chan GlossaryChatLookup, 1)
	go func() { lookupDone <- svc.LookupForChat(context.Background(), "Term") }()
	<-repo.findStarted

	// The owner write must be able to invalidate while the exact DB read is
	// still blocked. If LookupForChat holds snapshotMu during FindByTerm, this
	// operation cannot complete until releaseFind is closed.
	mutationDone := make(chan error, 1)
	go func() {
		_, err := svc.AddGlossaryItem(context.Background(), "Term", "new definition", "manual", "test")
		mutationDone <- err
	}()
	if err := <-mutationDone; err != nil {
		t.Fatalf("owner mutation during blocked lookup: %v", err)
	}
	close(repo.releaseFind)
	got := <-lookupDone
	if got.Status != GlossaryChatLookupUnavailable || got.Qualification != nil || len(got.Records) != 0 {
		t.Fatalf("overlapping lookup outcome = %#v, want unavailable", got)
	}
}

func TestGlossaryQualificationSerializesUseAgainstDelete(t *testing.T) {
	item := entity.NewGlossaryItem("Term", "definition", "manual", "test")
	repo := &chatLookupRepository{
		item:            item,
		deleteAttempted: make(chan struct{}),
		deleteCommitted: make(chan struct{}),
	}
	svc := NewGlossaryService(repo)
	lookup := svc.LookupForChat(context.Background(), "Term")
	if lookup.Qualification == nil {
		t.Fatalf("lookup has no qualification: %#v", lookup)
	}

	useStarted, releaseUse := make(chan struct{}), make(chan struct{})
	useDone := make(chan error, 1)
	go func() {
		useDone <- lookup.Qualification.Use(func() error {
			close(useStarted)
			<-releaseUse
			return nil
		})
	}()
	<-useStarted

	deleteDone := make(chan error, 1)
	go func() { deleteDone <- repo.Delete(context.Background(), item.ID) }()
	<-repo.deleteAttempted
	select {
	case <-repo.deleteCommitted:
		t.Fatal("delete committed while qualified RAM admission held the generation lock")
	default:
	}

	close(releaseUse)
	if err := <-useDone; err != nil {
		t.Fatalf("qualified admission: %v", err)
	}
	if err := <-deleteDone; err != nil {
		t.Fatalf("delete after qualified admission: %v", err)
	}
	select {
	case <-repo.deleteCommitted:
	default:
		t.Fatal("delete did not commit after admission released")
	}
}

func TestGlossaryQualificationRejectsAdmissionAfterDeleteReceipt(t *testing.T) {
	item := entity.NewGlossaryItem("Term", "definition", "manual", "test")
	repo := &chatLookupRepository{item: item}
	svc := NewGlossaryService(repo)
	lookup := svc.LookupForChat(context.Background(), "Term")
	if lookup.Qualification == nil {
		t.Fatalf("lookup has no qualification: %#v", lookup)
	}
	if err := repo.Delete(context.Background(), item.ID); err != nil {
		t.Fatalf("owner delete: %v", err)
	}
	admitted := false
	err := lookup.Qualification.Use(func() error {
		admitted = true
		return nil
	})
	if !errors.Is(err, ErrGlossaryChatQualificationStale) || admitted {
		t.Fatalf("stale qualification error/admission = %v/%t, want stale/false", err, admitted)
	}
}

func TestLookupForChatRepositoryErrorAndNonExactPositiveAreUnavailable(t *testing.T) {
	readErr := errors.New("owner read failed")
	repo := &chatLookupRepository{findErr: readErr}
	svc := NewGlossaryService(repo)
	if got := svc.LookupForChat(context.Background(), "Term"); got.Status != GlossaryChatLookupUnavailable {
		t.Fatalf("repository error outcome = %#v, want unavailable", got)
	}

	repo = &chatLookupRepository{item: entity.NewGlossaryItem("term", "definition", "manual", "test")}
	svc = NewGlossaryService(repo)
	if got := svc.LookupForChat(context.Background(), "Term"); got.Status != GlossaryChatLookupUnavailable || got.Qualification != nil {
		t.Fatalf("non-exact owner row outcome = %#v, want unavailable", got)
	}
}
