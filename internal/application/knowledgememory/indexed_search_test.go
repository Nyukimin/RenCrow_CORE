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
