package taskexecution

import "testing"

func TestPolicyValidateAndAllowedAgents(t *testing.T) {
	p := Policy{SchemaVersion: 1, TaskKinds: map[string]KindPolicy{
		"movie.catalog.fetch": {AllowedAgents: AllowedAgents{Mode: "all_registered"}},
		"writing.novel":       {AllowedAgents: AllowedAgents{Mode: "explicit", ActorIDs: []string{"midori"}}},
	}}
	kinds := []string{"movie.catalog.fetch", "writing.novel"}
	actors := []string{"midori", "sora"}
	if err := p.Validate(kinds, actors); err != nil {
		t.Fatal(err)
	}
	allowed, err := p.Allowed("movie.catalog.fetch", "sora", actors, nil)
	if err != nil || !allowed {
		t.Fatalf("all_registered: allowed=%v err=%v", allowed, err)
	}
	allowed, err = p.Allowed("writing.novel", "sora", actors, nil)
	if err != nil || allowed {
		t.Fatalf("explicit: allowed=%v err=%v", allowed, err)
	}
	allowed, err = p.Allowed("movie.catalog.fetch", "sora", actors, &[]string{"midori"})
	if err != nil || allowed {
		t.Fatalf("subset must narrow: allowed=%v err=%v", allowed, err)
	}
	if _, err := p.Allowed("movie.catalog.fetch", "sora", actors, &[]string{}); err == nil {
		t.Fatal("explicit empty subset must be rejected")
	}
}

func TestPolicyRejectsUnknownAndNonCanonicalEntries(t *testing.T) {
	base := Policy{SchemaVersion: 1, TaskKinds: map[string]KindPolicy{"writing.novel": {AllowedAgents: AllowedAgents{Mode: "explicit", ActorIDs: []string{"midori"}}}}}
	for name, p := range map[string]Policy{
		"unknown kind":     {SchemaVersion: 1, TaskKinds: map[string]KindPolicy{"other": base.TaskKinds["writing.novel"]}},
		"unknown actor":    {SchemaVersion: 1, TaskKinds: map[string]KindPolicy{"writing.novel": {AllowedAgents: AllowedAgents{Mode: "explicit", ActorIDs: []string{"missing"}}}}},
		"empty explicit":   {SchemaVersion: 1, TaskKinds: map[string]KindPolicy{"writing.novel": {AllowedAgents: AllowedAgents{Mode: "explicit"}}}},
		"duplicate actors": {SchemaVersion: 1, TaskKinds: map[string]KindPolicy{"writing.novel": {AllowedAgents: AllowedAgents{Mode: "explicit", ActorIDs: []string{"midori", "midori"}}}}},
		"invalid mode":     {SchemaVersion: 1, TaskKinds: map[string]KindPolicy{"writing.novel": {AllowedAgents: AllowedAgents{Mode: "all"}}}},
		"case variant":     {SchemaVersion: 1, TaskKinds: map[string]KindPolicy{"Writing.Novel": base.TaskKinds["writing.novel"]}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := p.Validate([]string{"writing.novel"}, []string{"midori"}); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestPolicyCanonicalHashIgnoresMapAndSetOrder(t *testing.T) {
	a := Policy{SchemaVersion: 1, TaskKinds: map[string]KindPolicy{
		"a.kind": {AllowedAgents: AllowedAgents{Mode: "explicit", ActorIDs: []string{"midori", "sora"}}},
		"b.kind": {AllowedAgents: AllowedAgents{Mode: "all_registered"}},
	}}
	b := Policy{SchemaVersion: 1, TaskKinds: map[string]KindPolicy{
		"b.kind": {AllowedAgents: AllowedAgents{Mode: "all_registered"}},
		"a.kind": {AllowedAgents: AllowedAgents{Mode: "explicit", ActorIDs: []string{"sora", "midori"}}},
	}}
	ha, err := a.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	hb, err := b.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Fatalf("hashes differ: %s != %s", ha, hb)
	}
}
