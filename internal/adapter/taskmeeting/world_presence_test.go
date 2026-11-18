package taskmeeting

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	appmeeting "github.com/Nyukimin/RenCrow_CORE/internal/application/taskmeeting"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
)

type scopedWorldSnapshotClientFixture struct {
	scope worldread.ReadScope
	calls int
}

func TestNewConfiguredWorldSnapshotClientRequiresCredential(t *testing.T) {
	const tokenEnv = "RENCROW_TASKMEETING_WORLD_TEST_TOKEN"
	t.Setenv(tokenEnv, "")
	_, err := NewConfiguredWorldSnapshotClient(config.WorldProjectionConfig{
		BaseURL: "https://world.example.test", TokenEnv: tokenEnv,
		WorldID: "house", TimelineID: "main", TimelineEpoch: "epoch-4",
		Actors: []config.WorldProjectionActorConfig{{ActorID: "world-mio"}},
	}, time.Second)
	if err == nil || !strings.Contains(err.Error(), tokenEnv) {
		t.Fatalf("missing credential error = %v, want an error naming the configured environment", err)
	}
}

func TestNewConfiguredWorldSnapshotClientUsesExactScopeWithoutRequest(t *testing.T) {
	const tokenEnv = "RENCROW_TASKMEETING_WORLD_TEST_TOKEN"
	t.Setenv(tokenEnv, "synthetic-world-token")
	client, err := NewConfiguredWorldSnapshotClient(config.WorldProjectionConfig{
		BaseURL: "https://world.example.test", TokenEnv: tokenEnv,
		WorldID: "house", TimelineID: "main", TimelineEpoch: "epoch-4",
		Actors: []config.WorldProjectionActorConfig{{ActorID: "world-shiro"}, {ActorID: "world-mio"}},
	}, 3*time.Second)
	if err != nil {
		t.Fatalf("NewConfiguredWorldSnapshotClient: %v", err)
	}
	scope := client.ReadScope()
	want := worldread.ReadScope{
		BaseURL: "https://world.example.test", WorldID: "house", TimelineID: "main", TimelineEpoch: "epoch-4",
		ActorIDs: []string{"world-mio", "world-shiro"},
	}
	if !reflect.DeepEqual(scope, want) {
		t.Fatalf("ReadScope = %+v, want %+v", scope, want)
	}
	encoded, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "synthetic-world-token") {
		t.Fatal("ReadScope exposed the configured credential")
	}
}

func (client *scopedWorldSnapshotClientFixture) ReadScope() worldread.ReadScope { return client.scope }
func (client *scopedWorldSnapshotClientFixture) Snapshot(context.Context) (worldread.Snapshot, error) {
	client.calls++
	return worldread.Snapshot{SchemaVersion: 1}, nil
}

func TestPinnedWorldSnapshotReaderDefersClientAndChecksFrozenScope(t *testing.T) {
	declaration, _, _ := worldPresenceTestDeclaration()
	actorIDs := make([]string, 0, len(declaration.WorldActorBindings))
	for _, binding := range declaration.WorldActorBindings {
		actorIDs = append(actorIDs, binding.WorldActorID)
	}
	var factoryCalls int
	client := &scopedWorldSnapshotClientFixture{scope: worldread.ReadScope{
		BaseURL: declaration.WorldBaseURL, WorldID: declaration.WorldID, TimelineID: declaration.TimelineID,
		TimelineEpoch: declaration.TimelineEpoch, ActorIDs: actorIDs,
	}}
	reader, err := NewPinnedWorldSnapshotReader(declaration, func() (TaskMeetingScopedWorldSnapshotClient, error) {
		factoryCalls++
		return client, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if factoryCalls != 0 {
		t.Fatal("constructing the metadata reader resolved the World client early")
	}
	scope := reader.ReadScope()
	scope.ActorIDs[0] = "caller-mutated"
	if reader.ReadScope().ActorIDs[0] == "caller-mutated" {
		t.Fatal("ReadScope exposed mutable internal actor IDs")
	}
	if _, err := reader.Snapshot(context.Background()); err != nil {
		t.Fatalf("read exact frozen scope: %v", err)
	}
	if factoryCalls != 1 || client.calls != 1 {
		t.Fatalf("snapshot calls factory=%d client=%d, want 1/1", factoryCalls, client.calls)
	}

	client.scope.WorldID = "retargeted-world"
	if _, err := reader.Snapshot(context.Background()); !errors.Is(err, appmeeting.ErrUnavailable) {
		t.Fatalf("mismatched authenticated client scope error=%v, want unavailable", err)
	}
	if client.calls != 1 {
		t.Fatal("snapshot was read after the authenticated client scope mismatched")
	}
}

func TestWorldReadScopeMatchesFrozenDeclarationWithoutExposingCredential(t *testing.T) {
	declaration := domainmeeting.TaskMeetingDeclaration{
		WorldBaseURL: "https://world.example.test", WorldCredentialEnv: "REN_WORLD_READ_TOKEN",
		WorldID: "house", TimelineID: "main", TimelineEpoch: "epoch-4",
		WorldActorBindings: []domainmeeting.WorldActorBinding{{CoreActorID: "mio", WorldActorID: "world-mio"}, {CoreActorID: "shiro", WorldActorID: "world-shiro"}},
	}
	scope := worldread.ReadScope{BaseURL: declaration.WorldBaseURL, WorldID: declaration.WorldID, TimelineID: declaration.TimelineID,
		TimelineEpoch: declaration.TimelineEpoch, ActorIDs: []string{"world-mio", "world-shiro"}}
	if !matchesConfiguredReadScope(scope, declaration) {
		t.Fatal("exact frozen scope was rejected")
	}
	for name, mutate := range map[string]func(*worldread.ReadScope){
		"endpoint":           func(s *worldread.ReadScope) { s.BaseURL = "https://other.example.test" },
		"world":              func(s *worldread.ReadScope) { s.WorldID = "other" },
		"timeline":           func(s *worldread.ReadScope) { s.TimelineEpoch = "new-epoch" },
		"actor expansion":    func(s *worldread.ReadScope) { s.ActorIDs = append(s.ActorIDs, "world-outsider") },
		"actor substitution": func(s *worldread.ReadScope) { s.ActorIDs[1] = "world-outsider" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := scope
			changed.ActorIDs = append([]string(nil), scope.ActorIDs...)
			mutate(&changed)
			if matchesConfiguredReadScope(changed, declaration) {
				t.Fatal("changed World client scope was accepted")
			}
		})
	}
	encoded, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "REN_WORLD_READ_TOKEN") || strings.Contains(string(encoded), "credential") {
		t.Fatalf("non-secret client scope unexpectedly contains credential metadata: %s", encoded)
	}
}

func TestWorldReadClientPublishesOnlyItsFrozenNonSecretScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	client, err := worldread.New(worldread.Config{BaseURL: server.URL, Credential: "secret-world-token", WorldID: "house", TimelineID: "main", TimelineEpoch: "epoch-4", ActorIDs: []string{"world-mio", "world-shiro"}})
	if err != nil {
		t.Fatal(err)
	}
	scope := client.ReadScope()
	if scope.BaseURL != server.URL || scope.WorldID != "house" || scope.TimelineID != "main" || scope.TimelineEpoch != "epoch-4" || len(scope.ActorIDs) != 2 {
		t.Fatalf("read scope = %+v", scope)
	}
	encoded, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret-world-token") {
		t.Fatalf("client credential leaked through scope projection: %s", encoded)
	}
}
