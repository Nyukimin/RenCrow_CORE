package main

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	appresearch "github.com/Nyukimin/RenCrow_CORE/internal/application/research"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

type researchBoardOwnerSpy struct{ commits, verifies int }

func (s *researchBoardOwnerSpy) Commit(context.Context, engine.CommitRequest) (c.Receipt, error) {
	s.commits++
	return c.Receipt{}, nil
}
func (s *researchBoardOwnerSpy) Verify(context.Context, c.Envelope, c.Receipt) (c.Receipt, error) {
	s.verifies++
	return c.Receipt{}, nil
}

func TestResearchBoardResolverUsesOnlyTheRegisteredAuthenticatedDestination(t *testing.T) {
	spy := &researchBoardOwnerSpy{}
	resolves := 0
	owner := &runtimeResearchBoardOwner{
		destinations: map[string]appresearch.BoardDestination{"approved": {Ref: "approved", OwnerID: "ren", DataScope: "user"}},
		resolve:      func(appresearch.BoardDestination) (engine.OwnerPort, error) { resolves++; return spy, nil },
	}
	for _, denied := range []struct {
		env c.Envelope
		ref string
	}{
		{c.Envelope{DataScope: "public"}, "approved"},
		{c.Envelope{DataScope: "user", OwnerID: "another-user"}, "approved"},
		{c.Envelope{DataScope: "user", OwnerID: "ren"}, "unknown"},
	} {
		if _, err := owner.Commit(context.Background(), engine.CommitRequest{Envelope: denied.env, Target: c.SaveTarget{Target: "board", DestinationRef: denied.ref}}); err == nil {
			t.Fatalf("unauthorized destination was resolved: %+v", denied)
		}
		if _, err := owner.Verify(context.Background(), denied.env, c.Receipt{DestinationRef: denied.ref}); err == nil {
			t.Fatalf("unauthorized receipt destination was resolved: %+v", denied)
		}
	}
	if resolves != 0 || spy.commits != 0 || spy.verifies != 0 {
		t.Fatal("denied destination contacted a connection or owner")
	}
	env := c.Envelope{DataScope: "user", OwnerID: "ren", Actor: "mio"}
	if _, err := owner.Commit(context.Background(), engine.CommitRequest{Envelope: env, Target: c.SaveTarget{Target: "board", DestinationRef: "approved"}}); err != nil {
		t.Fatal(err)
	}
	if resolves != 1 || spy.commits != 1 {
		t.Fatal("registered authenticated destination was not selected exactly once")
	}
}

func TestResearchBoardConnectionLookupUsesActualPrincipalAndDoesNotOpenAnother(t *testing.T) {
	root, other := &boardCommunicationBoot{settings: boardConnectionSettings{ExpectedPrincipalID: "principal-root"}}, &boardCommunicationBoot{settings: boardConnectionSettings{ExpectedPrincipalID: "principal-other"}}
	deps := &Dependencies{boardCommunicationRootBoot: root, boardCommunicationActorBoots: map[string]*boardCommunicationBoot{"mio": other}}
	if researchExistingBoardBoot(deps, "principal-root") != root || researchExistingBoardBoot(deps, "principal-other") != other || researchExistingBoardBoot(deps, "mio") != nil {
		t.Fatal("BOARD principal was inferred from an Agent name or changed")
	}
	e := &engine.Engine{}
	if err := connectResearchBoardOwner(&config.Config{}, deps, e); err != nil || e.Ports.Owner != nil {
		t.Fatal("empty destination configuration changed owner wiring")
	}
	invalid := &config.Config{Research: config.ResearchConfig{BoardDestinations: []config.ResearchBoardDestinationConfig{{Ref: "public", DataScope: "public"}}}}
	if err := connectResearchBoardOwner(invalid, deps, e); err == nil {
		t.Fatal("unprovisioned publication authority was accepted")
	}
}
