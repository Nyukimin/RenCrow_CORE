package knowledgememory

import (
	"reflect"
	"strings"
	"testing"
)

func TestSearchTokensNormalizeJapaneseBigramsAndASCIITokens(t *testing.T) {
	got, err := SearchTokens("ＡＩ 日本語")
	if err != nil {
		t.Fatalf("SearchTokens() error = %v", err)
	}
	want := []string{"ai", "日本", "本語"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SearchTokens() = %#v, want %#v", got, want)
	}
}

func TestSearchTokensRejectsBlankAndOversizedQueries(t *testing.T) {
	for _, query := range []string{"", "   ", strings.Repeat("日本", 81)} {
		if _, err := SearchTokens(query); err == nil {
			t.Fatalf("SearchTokens(%q) should fail", query)
		}
	}
}

func TestIndexSourceTokensAcceptLongSourcesWithoutRelaxingLegacyProjectionOrQueryBounds(t *testing.T) {
	document := strings.Repeat("日本語", 20_000)
	if _, err := IndexTokens(document); err == nil {
		t.Fatal("IndexTokens unexpectedly accepted text over the legacy 160-rune contract")
	}
	if _, err := IndexSourceTokens(document); err != nil {
		t.Fatalf("IndexSourceTokens(long source) error = %v", err)
	}
	if _, err := SearchTokens(document); err == nil {
		t.Fatal("SearchTokens(long query) unexpectedly accepted a document-sized query")
	}
	if _, err := IndexSourceTokens(strings.Repeat("日", maxIndexedSourceRunes+1)); err == nil {
		t.Fatal("IndexSourceTokens unexpectedly accepted source text over its bounded source limit")
	}
}

func TestPersonalArchiveSearchRequiresPrivateUserScope(t *testing.T) {
	request := SearchRequest{Scope: SearchScope{Scope: SearchScopePublic}, Query: "diary", RecordType: "personal_archive"}
	if err := request.Validate(); err == nil {
		t.Fatal("public personal archive search should fail closed")
	}
	request.Scope = SearchScope{Scope: SearchScopeUser, UserID: "ren"}
	if err := request.Validate(); err != nil {
		t.Fatalf("private owner personal archive search rejected: %v", err)
	}
}

func TestSearchScopeRequiresFixedAuthenticatedBoundary(t *testing.T) {
	valid := []SearchScope{
		{Scope: SearchScopePublic},
		{Scope: SearchScopeUser, UserID: "user-1"},
	}
	for _, scope := range valid {
		if err := scope.Validate(); err != nil {
			t.Fatalf("valid scope %#v rejected: %v", scope, err)
		}
	}
	for _, scope := range []SearchScope{
		{},
		{Scope: SearchScopePublic, UserID: "user-1"},
		{Scope: SearchScopeUser},
		{Scope: "arbitrary", UserID: "user-1"},
	} {
		if err := scope.Validate(); err == nil {
			t.Fatalf("scope %#v should fail closed", scope)
		}
	}
}
