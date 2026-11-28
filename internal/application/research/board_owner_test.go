package research

import (
	"context"
	"errors"
	"testing"

	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

func TestBoardDestinationRequiresOneClosedConfiguredTarget(t *testing.T) {
	base := BoardDestination{
		Ref: "weekly-research", OwnerID: "owner-a", PrincipalID: "ren-core", ChannelID: "research",
		NewThread: true, Recipients: []string{"agent-a"}, DataScope: "public", Title: "Weekly research",
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid new-thread destination: %v", err)
	}
	existing := base
	existing.NewThread = false
	existing.ThreadID = "thread-1"
	existing.Title = ""
	if err := existing.Validate(); err != nil {
		t.Fatalf("valid existing-thread destination: %v", err)
	}
	for _, mutate := range []struct {
		name string
		fn   func(*BoardDestination)
	}{
		{name: "both thread modes", fn: func(d *BoardDestination) { d.ThreadID = "thread-1" }},
		{name: "neither thread mode", fn: func(d *BoardDestination) { d.NewThread = false }},
		{name: "unordered recipients", fn: func(d *BoardDestination) { d.Recipients = []string{"peer-b", "peer-a"} }},
		{name: "duplicate recipients", fn: func(d *BoardDestination) { d.Recipients = []string{"peer-a", "peer-a"} }},
		{name: "sender is recipient", fn: func(d *BoardDestination) { d.Recipients = []string{"ren-core"} }},
		{name: "untrusted scope", fn: func(d *BoardDestination) { d.DataScope = "internal" }},
		{name: "arbitrary ref syntax", fn: func(d *BoardDestination) { d.Ref = "../research" }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			candidate := base
			candidate.Recipients = append([]string(nil), base.Recipients...)
			mutate.fn(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid BOARD destination was accepted")
			}
		})
	}
}

func TestBoardOperationIDDeduplicatesExactDestinationAndContentAcrossRuns(t *testing.T) {
	scope := BoardPublicationScope{OwnerID: "owner-a", PrincipalID: "ren-core", JournalInstanceID: "journal_0123456789abcdef0123456789abcdef"}
	first := BoardOperationID(scope, "weekly-research", "public", "research", "", true, "Weekly", "sha256:report-a", []string{"agent-a"})
	second := BoardOperationID(scope, "weekly-research", "public", "research", "", true, "Weekly", "sha256:report-a", []string{"agent-a"})
	if first == "" || first != second {
		t.Fatalf("same destination/content operation IDs differ: %q and %q", first, second)
	}
	changed := BoardOperationID(scope, "weekly-research", "public", "research", "", true, "Weekly", "sha256:report-b", []string{"agent-a"})
	if changed == first {
		t.Fatal("changed report digest reused the same semantic operation ID")
	}
	otherDestination := BoardOperationID(scope, "another-ref", "public", "research", "", true, "Weekly", "sha256:report-a", []string{"agent-a"})
	if otherDestination == first {
		t.Fatal("different registered destination reused the same semantic operation ID")
	}
}

func TestOwnerRouterUsesTypedKnowledgeAndBoardOwners(t *testing.T) {
	knowledge := &boardOwnerPortStub{owner: "core.l1_staging", route: KnowledgeStagingTarget}
	board := &boardOwnerPortStub{owner: BoardOwnerName, route: BoardOwnerRoute}
	router := NewOwnerRouter(knowledge, board)

	for _, target := range []struct {
		name  string
		owner *boardOwnerPortStub
		value c.SaveTarget
	}{
		{name: "knowledge", owner: knowledge, value: c.SaveTarget{Target: KnowledgeStagingTarget}},
		{name: "board", owner: board, value: c.SaveTarget{Target: "board", DestinationRef: "weekly-research"}},
	} {
		t.Run(target.name, func(t *testing.T) {
			if _, err := router.Commit(context.Background(), engine.CommitRequest{Target: target.value}); err != nil {
				t.Fatalf("Commit: %v", err)
			}
			if target.owner.commits != 1 {
				t.Fatalf("routed owner Commit count = %d, want 1", target.owner.commits)
			}
		})
	}
	if _, err := router.Commit(context.Background(), engine.CommitRequest{Target: c.SaveTarget{Target: "artifact"}}); !errors.Is(err, ErrBoardOwnerUnavailable) {
		t.Fatalf("artifact target reached OwnerRouter: %v", err)
	}
	if _, err := router.Commit(context.Background(), engine.CommitRequest{Target: c.SaveTarget{Target: "news_owner"}}); !errors.Is(err, ErrBoardOwnerUnavailable) {
		t.Fatalf("unconnected target reached OwnerRouter: %v", err)
	}
	if _, err := router.Verify(context.Background(), c.Envelope{}, c.Receipt{Owner: BoardOwnerName}); err != nil {
		t.Fatalf("board receipt Verify: %v", err)
	}
	if board.verifies != 1 || knowledge.verifies != 0 {
		t.Fatalf("receipt dispatch knowledge=%d board=%d", knowledge.verifies, board.verifies)
	}
	if _, err := router.Reconcile(context.Background(), engine.CommitRequest{Target: c.SaveTarget{Target: "board"}}); err != nil {
		t.Fatalf("board Reconcile: %v", err)
	}
	if board.reconciles != 1 || knowledge.reconciles != 0 {
		t.Fatalf("reconcile dispatch knowledge=%d board=%d", knowledge.reconciles, board.reconciles)
	}
}

type boardOwnerPortStub struct {
	owner      string
	route      string
	commits    int
	verifies   int
	reconciles int
}

func (s *boardOwnerPortStub) Commit(context.Context, engine.CommitRequest) (c.Receipt, error) {
	s.commits++
	return c.Receipt{Owner: s.owner, OwnerRoute: s.route}, nil
}

func (s *boardOwnerPortStub) Verify(_ context.Context, _ c.Envelope, receipt c.Receipt) (c.Receipt, error) {
	s.verifies++
	return receipt, nil
}

func (s *boardOwnerPortStub) Reconcile(context.Context, engine.CommitRequest) (engine.ReconcileResult, error) {
	s.reconciles++
	return engine.ReconcileResult{RetrySafe: true}, nil
}

var _ engine.OwnerPort = (*boardOwnerPortStub)(nil)
var _ engine.ReconcilePort = (*boardOwnerPortStub)(nil)
